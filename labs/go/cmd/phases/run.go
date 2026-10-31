package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options configures one measured request.
type Options struct {
	URL     string
	Timeout time.Duration
	Resolve string    // host:port:addr — dial addr instead of resolving host
	CAFile  string    // extra trusted CAs (PEM)
	KeyLog  io.Writer // TLS key log for Wireshark decryption; nil disables
	NoProxy bool      // ignore HTTP_PROXY/HTTPS_PROXY/NO_PROXY and connect directly
	Proxy   string    // explicit proxy URL, overriding the environment
	Count   int       // sequential requests on one client (RunAll); 1 if unset
}

// Step is one timed phase of the request. Times are milliseconds from the start.
type Step struct {
	Name    string  `json:"name"`
	StartMS float64 `json:"start_ms"`
	TookMS  float64 `json:"took_ms"`
	Done    bool    `json:"done"`
	Detail  string  `json:"detail,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// TLSInfo summarises the negotiated session and the server's leaf certificate.
type TLSInfo struct {
	Version   string    `json:"version"`
	Cipher    string    `json:"cipher"`
	ALPN      string    `json:"alpn,omitempty"`
	Subject   string    `json:"subject"`
	Issuer    string    `json:"issuer"`
	NotAfter  time.Time `json:"not_after"`
	ChainSize int       `json:"chain_size"`
}

// Report is everything phases learned about the request.
type Report struct {
	Method      string   `json:"method"`
	URL         string   `json:"url"`
	Proxy       string   `json:"proxy,omitempty"`
	Resolved    string   `json:"resolved_override,omitempty"`
	Steps       []Step   `json:"steps"`
	RemoteAddr  string   `json:"remote_addr,omitempty"`
	Reused      bool     `json:"reused_connection"`
	Proto       string   `json:"proto,omitempty"`
	Status      string   `json:"status,omitempty"`
	BodyBytes   int64    `json:"body_bytes"`
	TLS         *TLSInfo `json:"tls,omitempty"`
	TotalMS     float64  `json:"total_ms"`
	FailedPhase string   `json:"failed_phase,omitempty"`
	Error       string   `json:"error,omitempty"`
	Meaning     string   `json:"meaning,omitempty"`
}

// Phase names, in the order a fresh request passes through them.
const (
	phaseDNS      = "dns"
	phaseConnect  = "connect"
	phaseTLS      = "tls"
	phaseRequest  = "request"
	phaseWait     = "server wait"
	phaseTransfer = "transfer"
	phaseTunnel   = "proxy tunnel" // CONNECT through an HTTP proxy; not traced by net/http
)

// recorder collects trace callbacks. Callbacks can arrive from several
// goroutines (Happy Eyeballs dials IPv4 and IPv6 in parallel), so it locks.
type recorder struct {
	mu      sync.Mutex
	t0      time.Time
	steps   []Step
	open    map[string]int // callback key -> index of a step still in progress
	early   map[string]float64
	current string // phase most recently started
	remote  string
}

func newRecorder() *recorder {
	return &recorder{t0: time.Now(), open: map[string]int{}, early: map[string]float64{}}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func (r *recorder) start(key, phase, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := ms(time.Since(r.t0))
	if end, ok := r.early[key]; ok {
		// The step's end was reported before its start: over HTTP/2 the first
		// response byte can be seen before the request write is reported done.
		// Its start is not known precisely; place it at its end time, in
		// time order, so it is listed before any step that began after it
		// (the transfer).
		delete(r.early, key)
		at := min(now, end)
		step := Step{Name: phase, StartMS: at, TookMS: max(0, end-now), Done: true,
			Detail: strings.TrimSpace(detail + " (overlapped the request write)")}
		i := len(r.steps)
		for i > 0 && r.steps[i-1].StartMS > at {
			i--
		}
		r.steps = slices.Insert(r.steps, i, step)
		for k, idx := range r.open {
			if idx >= i {
				r.open[k] = idx + 1
			}
		}
		return
	}
	r.open[key] = len(r.steps)
	r.steps = append(r.steps, Step{Name: phase, StartMS: now, Detail: detail})
	r.current = phase
}

func (r *recorder) done(key, detail string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.open[key]
	if !ok {
		if err == nil {
			r.early[key] = ms(time.Since(r.t0)) // start may still be reported
		}
		return
	}
	delete(r.open, key)
	s := &r.steps[i]
	s.TookMS = ms(time.Since(r.t0)) - s.StartMS
	s.Done = true
	if detail != "" {
		if s.Detail != "" {
			s.Detail += " → " + detail
		} else {
			s.Detail = detail
		}
	}
	if err != nil {
		s.Error = err.Error()
	}
}

func (r *recorder) snapshot() ([]Step, string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.steps...), r.current, r.remote
}

// parseResolve splits curl-style host:port:addr. addr may be an IPv6
// literal, with or without brackets.
func parseResolve(s string) (hostPort, target string, err error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", fmt.Errorf("-resolve wants host:port:addr, got %q", s)
	}
	addr := strings.TrimSuffix(strings.TrimPrefix(parts[2], "["), "]")
	if net.ParseIP(addr) == nil {
		return "", "", fmt.Errorf("-resolve address %q is not an IP address", addr)
	}
	return net.JoinHostPort(parts[0], parts[1]), net.JoinHostPort(addr, parts[1]), nil
}

func tlsConfig(opts Options) (*tls.Config, error) {
	cfg := &tls.Config{KeyLogWriter: opts.KeyLog}
	if opts.CAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(opts.CAFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificates found in %s", opts.CAFile)
	}
	cfg.RootCAs = pool
	return cfg, nil
}

// Run performs one GET and returns its report. The returned error is only for
// invalid options; network failures are recorded in the report.
func Run(ctx context.Context, opts Options) (*Report, error) {
	opts.Count = 1
	reports, err := RunAll(ctx, opts)
	if err != nil {
		return nil, err
	}
	return reports[0], nil
}

// RunAll performs opts.Count sequential GETs (at least one) through one
// client. With more than one, keep-alives are on, so later requests can reuse
// the first connection and skip DNS, connect, and TLS — the point of the
// exercise. Each request gets its own deadline and report.
func RunAll(ctx context.Context, opts Options) ([]*Report, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	if opts.Count < 1 {
		opts.Count = 1
	}
	u, err := url.Parse(opts.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("want an http:// or https:// URL, got %q", opts.URL)
	}
	tlsCfg, err := tlsConfig(opts)
	if err != nil {
		return nil, err
	}

	dialer := &net.Dialer{}
	dial := dialer.DialContext
	resolved := ""
	if opts.Resolve != "" {
		hostPort, target, err := parseResolve(opts.Resolve)
		if err != nil {
			return nil, err
		}
		resolved = hostPort + " → " + target
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			if addr == hostPort {
				addr = target
			}
			return dialer.DialContext(ctx, network, addr)
		}
	}

	proxy := http.ProxyFromEnvironment
	switch {
	case opts.NoProxy:
		proxy = nil
	case opts.Proxy != "":
		pu, err := url.Parse(opts.Proxy)
		if err != nil || pu.Host == "" {
			return nil, fmt.Errorf("-proxy wants a URL such as http://proxy:3128, got %q", opts.Proxy)
		}
		proxy = http.ProxyURL(pu)
	}
	transport := &http.Transport{
		Proxy:             proxy,
		DialContext:       dial,
		TLSClientConfig:   tlsCfg,
		ForceAttemptHTTP2: true, // a custom DialContext otherwise disables HTTP/2
		// A single request always gets a fresh connection, so every phase shows.
		DisableKeepAlives: opts.Count == 1,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		// Report the redirect itself, like curl without -L.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	var reports []*Report
	for i := 0; i < opts.Count; i++ {
		r, err := once(ctx, client, transport, u, opts.Timeout)
		if err != nil {
			return nil, err
		}
		r.Resolved = resolved
		reports = append(reports, r)
	}
	return reports, nil
}

// once performs a single traced GET with its own deadline.
func once(ctx context.Context, client *http.Client, transport *http.Transport, u *url.URL, timeout time.Duration) (*Report, error) {
	report := &Report{Method: http.MethodGet, URL: u.String()}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rec := newRecorder()
	trace := &httptrace.ClientTrace{
		DNSStart: func(i httptrace.DNSStartInfo) { rec.start(phaseDNS, phaseDNS, i.Host) },
		DNSDone: func(i httptrace.DNSDoneInfo) {
			addrs := make([]string, len(i.Addrs))
			for n, a := range i.Addrs {
				addrs[n] = a.String()
			}
			rec.done(phaseDNS, strings.Join(addrs, ", "), i.Err)
		},
		ConnectStart: func(network, addr string) {
			rec.start(phaseConnect+" "+addr, phaseConnect, network+" "+addr)
		},
		ConnectDone: func(network, addr string, err error) {
			rec.done(phaseConnect+" "+addr, "", err)
		},
		TLSHandshakeStart: func() { rec.start(phaseTLS, phaseTLS, "") },
		TLSHandshakeDone: func(cs tls.ConnectionState, err error) {
			detail := ""
			if err == nil {
				detail = fmt.Sprintf("%s, %s", tls.VersionName(cs.Version), tls.CipherSuiteName(cs.CipherSuite))
				if cs.NegotiatedProtocol != "" {
					detail += ", ALPN " + cs.NegotiatedProtocol
				}
			}
			rec.done(phaseTLS, detail, err)
		},
		GotConn: func(i httptrace.GotConnInfo) {
			rec.mu.Lock()
			rec.remote = i.Conn.RemoteAddr().String()
			report.Reused = i.Reused // read after rec.snapshot(), which takes the same lock
			rec.mu.Unlock()
			detail := ""
			if i.Reused {
				detail = "on a reused connection"
			}
			rec.start(phaseRequest, phaseRequest, detail)
		},
		WroteRequest: func(i httptrace.WroteRequestInfo) {
			rec.done(phaseRequest, "", i.Err)
			if i.Err == nil {
				rec.start(phaseWait, phaseWait, "until the first response byte")
			}
		},
		GotFirstResponseByte: func() { rec.done(phaseWait, "", nil) },
	}

	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "phases (practical networking course)")
	if transport.Proxy != nil {
		if p, err := transport.Proxy(req); err == nil && p != nil {
			report.Proxy = p.Redacted()
		}
	}

	resp, err := client.Do(req)
	if err == nil {
		report.Proto, report.Status = resp.Proto, resp.Status
		if resp.TLS != nil {
			report.TLS = tlsInfo(resp.TLS)
		}
		rec.start(phaseTransfer, phaseTransfer, "")
		report.BodyBytes, err = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		rec.done(phaseTransfer, fmt.Sprintf("%d bytes", report.BodyBytes), err)
	}
	steps, current, remote := rec.snapshot()
	report.Steps, report.RemoteAddr = steps, remote
	report.TotalMS = ms(time.Since(rec.t0))
	if err != nil {
		report.FailedPhase = current
		if current == "" { // failed before any traced step, e.g. reaching a proxy
			report.FailedPhase = "before connect"
		}
		report.Error = err.Error()
		report.Meaning = Diagnose(err)
		if report.Proxy != "" && u.Scheme == "https" && tunnelRefused(steps, current) {
			report.FailedPhase = phaseTunnel
			report.Meaning = proxyMeaning(err)
		}
	}
	return report, nil
}

// tunnelRefused reports whether an HTTPS request through a proxy failed after
// the TCP connection to the proxy succeeded but before TLS began — that is,
// while the proxy was handling the CONNECT request.
func tunnelRefused(steps []Step, current string) bool {
	if current != phaseConnect || len(steps) == 0 {
		return false
	}
	last := steps[len(steps)-1]
	return last.Name == phaseConnect && last.Done && last.Error == ""
}

func proxyMeaning(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Proxy Authentication Required"):
		return "Proxy: the proxy wants credentials (HTTP 407) before it will open a tunnel. " +
			"Check the user:password in the proxy URL or the proxy's authentication scheme."
	case strings.Contains(msg, "Forbidden"):
		return "Proxy: the TCP connection to the proxy worked, but the proxy refused to open a CONNECT tunnel " +
			"to this host (HTTP 403) — usually a destination allow-list. Ask for the host to be allowed, or test with -noproxy where direct egress exists."
	}
	return "Proxy: the TCP connection to the proxy worked, but the CONNECT tunnel was not opened. " +
		"The proxy's reply is in the error above; its logs say why."
}

func tlsInfo(cs *tls.ConnectionState) *TLSInfo {
	info := &TLSInfo{
		Version:   tls.VersionName(cs.Version),
		Cipher:    tls.CipherSuiteName(cs.CipherSuite),
		ALPN:      cs.NegotiatedProtocol,
		ChainSize: len(cs.PeerCertificates),
	}
	if len(cs.PeerCertificates) > 0 {
		leaf := cs.PeerCertificates[0]
		info.Subject, info.Issuer, info.NotAfter = leaf.Subject.String(), leaf.Issuer.String(), leaf.NotAfter
	}
	return info
}

// Diagnose turns a request error into what it means on the wire and the
// first thing to check. Order matters: DNS and certificate errors also
// satisfy the generic timeout and net.Error checks further down.
func Diagnose(err error) string {
	var (
		dnsErr     *net.DNSError
		hostErr    x509.HostnameError
		authErr    x509.UnknownAuthorityError
		invalidErr x509.CertificateInvalidError
		verifyErr  *tls.CertificateVerificationError
		recordErr  tls.RecordHeaderError
		netErr     net.Error
	)
	switch {
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return "DNS: this resolver says the name does not exist (NXDOMAIN, or no record of the requested type). " +
			"Compare resolvers with `dig @<resolver> <name>`; suspect a private or split-horizon zone that this network cannot see."
	case errors.As(err, &dnsErr) && dnsErr.IsTimeout:
		return "DNS: the resolver did not answer before the deadline. " +
			"Check the nameserver in /etc/resolv.conf and that UDP and TCP 53 to it are allowed."
	case errors.As(err, &dnsErr):
		return "DNS: the lookup failed without a definite answer (often SERVFAIL or an unreachable resolver). " +
			"Query the resolver directly with dig and read the status line."
	case errors.As(err, &hostErr):
		return "TLS: the certificate is trusted but not issued for this hostname. " +
			"Check the SANs with `openssl s_client -connect host:443 -servername host`; suspect the wrong IP or a missing SNI route."
	case errors.As(err, &authErr):
		return "TLS: the certificate chain does not lead to an authority this client trusts — a missing intermediate, " +
			"a private CA, or a TLS-inspecting proxy. Compare the issuer from `openssl s_client -showcerts` inside and outside this network."
	case errors.As(err, &invalidErr) && invalidErr.Reason == x509.Expired:
		return "TLS: the certificate is expired or not yet valid. Check its dates and this machine's clock (`timedatectl`)."
	case errors.As(err, &verifyErr) || errors.As(err, &invalidErr):
		return "TLS: certificate verification failed. Inspect the chain with `openssl s_client -showcerts`."
	case errors.As(err, &recordErr) || strings.Contains(err.Error(), "server gave HTTP response to HTTPS client"):
		return "TLS: the other end did not answer with TLS — a plain-HTTP port, or a proxy or firewall answering instead of the server."
	case errors.Is(err, errConnRefused):
		return "TCP: connection refused — the SYN was answered with an RST or an ICMP port unreachable. Something on the path replied: " +
			"no listener on that port, a firewall REJECT rule (iptables' default REJECT sends ICMP port unreachable), or a proxy. " +
			"A capture shows which IP sent the RST or the ICMP message."
	case errors.Is(err, errConnReset):
		return "TCP: connection reset — an RST arrived on an established connection. Suspect an idle timeout on a NAT, load balancer, " +
			"or firewall, a TLS-inspecting middlebox, or the server process closing abruptly."
	case errors.Is(err, errHostUnreach):
		return "IP: no route to host — an ICMP host unreachable or administratively prohibited came back (a router could not ARP for the host, " +
			"or a firewall REJECT with icmp-host-prohibited/icmp-admin-prohibited), or ARP/NDP for an on-link address failed here (about 3 s). " +
			"A capture shows which address sent the ICMP message."
	case errors.Is(err, errNetUnreach):
		return "IP: network unreachable — this machine has no route to the destination (check `ip route get <ip>`), " +
			"or a router on the path answered ICMP net unreachable (a capture shows which)."
	case errors.Is(err, errAddrNotAvail):
		return "Local: cannot assign requested address — no free local port or address (ephemeral ports or SNAT ports exhausted, or a bad bind address)."
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()):
		return "Timeout: nothing answered before the deadline. In the connect phase this usually means SYNs are being dropped " +
			"(firewall DROP, security group, missing route or return route); in server wait, the server or a proxy is slow."
	}
	return "Unclassified error; read it together with the failed phase."
}

// WriteText prints the report as an aligned table.
func (r *Report) WriteText(w io.Writer) {
	fmt.Fprintf(w, "%s %s\n", r.Method, r.URL)
	if r.Proxy != "" {
		fmt.Fprintf(w, "  via proxy %s (connect and TLS timings include the proxy hop)\n", r.Proxy)
	}
	if r.Resolved != "" {
		if r.Proxy != "" {
			fmt.Fprintf(w, "  -resolve ignored: the proxy resolves the name, not this machine (use -noproxy to compare)\n")
		} else {
			fmt.Fprintf(w, "  DNS skipped: %s\n", r.Resolved)
		}
	}
	fmt.Fprintf(w, "\n  %-12s %10s %10s  %s\n", "STEP", "START", "TOOK", "DETAIL")
	for _, s := range r.Steps {
		took, detail := fmt.Sprintf("%.1fms", s.TookMS), s.Detail
		if !s.Done {
			took, detail = "…", strings.TrimSpace(detail+" (did not finish)")
		}
		if s.Error != "" {
			detail = strings.TrimSpace(detail + " ✗ " + s.Error)
		}
		fmt.Fprintf(w, "  %-12s %9.1fms %10s  %s\n", s.Name, s.StartMS, took, detail)
	}
	fmt.Fprintln(w)
	if r.Error != "" {
		fmt.Fprintf(w, "  FAILED during: %s (after %.1fms)\n  error:   %s\n  meaning: %s\n", r.FailedPhase, r.TotalMS, r.Error, r.Meaning)
		return
	}
	fmt.Fprintf(w, "  result: %s %s from %s, %d bytes in %.1fms\n", r.Proto, r.Status, r.RemoteAddr, r.BodyBytes, r.TotalMS)
	if r.Reused {
		fmt.Fprintln(w, "  connection reused: no DNS, connect, or TLS on this request")
		return
	}
	if t := r.TLS; t != nil && t.Subject != "" {
		days := int(time.Until(t.NotAfter).Hours() / 24)
		fmt.Fprintf(w, "  certificate: %s\n    issuer %s, expires %s (%d days), %d cert(s) sent\n",
			t.Subject, t.Issuer, t.NotAfter.Format("2006-01-02"), days, t.ChainSize)
	}
}
