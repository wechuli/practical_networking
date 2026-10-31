package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

// InspectOptions controls Inspect.
type InspectOptions struct {
	Addr       string // host:port
	ServerName string // SNI and the name to verify; "" = the host part; "-" = send no SNI
	VerifyName string // name to verify if different from SNI
	CAFile     string // trust only these roots instead of the system pool
	Min, Max   uint16
	ALPN       []string
	Curves     []tls.CurveID
	CertFile   string // client certificate (chain) and key, for mTLS
	KeyFile    string
	KeyLog     string // append TLS secrets here (NSS key log format) for Wireshark
	Resume     bool   // connect twice and report session resumption
	Timeout    time.Duration
	Request    bool // send an HTTP request after the handshake
}

// InspectResult is what Inspect found.
type InspectResult struct {
	Verified bool
	Lines    []string
}

func (r *InspectResult) add(format string, a ...any) {
	r.Lines = append(r.Lines, fmt.Sprintf(format, a...))
}

// Inspect connects, shows what was negotiated and what the server sent, and
// verifies the chain the way a client would — separately, so a failed
// verification still shows the certificates.
func Inspect(ctx context.Context, o InspectOptions) (*InspectResult, error) {
	res := &InspectResult{}
	host, _, err := net.SplitHostPort(o.Addr)
	if err != nil {
		return nil, err
	}
	sni := o.ServerName
	switch {
	case sni == "-":
		sni = ""
	case sni == "" && net.ParseIP(host) == nil:
		sni = host
	}
	verifyName := o.VerifyName
	if verifyName == "" {
		verifyName = sni
		if verifyName == "" {
			verifyName = host
		}
	}
	cfg := &tls.Config{
		ServerName:         sni,
		InsecureSkipVerify: true, // we verify below, by hand, to report every detail
		MinVersion:         o.Min,
		MaxVersion:         o.Max,
		NextProtos:         o.ALPN,
		CurvePreferences:   o.Curves,
	}
	if o.Resume {
		cfg.ClientSessionCache = tls.NewLRUClientSessionCache(4)
	}
	if o.CertFile != "" {
		pair, err := tls.LoadX509KeyPair(o.CertFile, o.KeyFile)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	if o.KeyLog != "" {
		f, err := os.OpenFile(o.KeyLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		cfg.KeyLogWriter = f
	}
	shown := "SNI " + sni
	if sni == "" {
		shown = "no SNI"
	}
	res.add("connecting to %s, %s", o.Addr, shown)
	state, err := handshake(ctx, o, cfg, res, true)
	if err != nil {
		return res, nil
	}
	if o.Resume {
		res.add("second connection (same session cache):")
		if _, err := handshake(ctx, o, cfg, res, false); err != nil {
			return res, nil
		}
	}
	// Verify the chain the server sent.
	certs := state.PeerCertificates
	res.add("chain sent by the server: %d certificate(s)", len(certs))
	now := time.Now()
	for i, c := range certs {
		for j, l := range DescribeCert(c, now) {
			prefix := "    "
			if j == 0 {
				prefix = fmt.Sprintf("  %d ", i)
			}
			res.add("%s%s", prefix, l)
		}
	}
	if len(certs) == 1 && certs[0].Subject.String() != certs[0].Issuer.String() {
		res.add("  note: only the leaf was sent; a client must already have %q to build a chain", certs[0].Issuer.CommonName)
	}
	var roots *x509.CertPool
	trust := "system trust store"
	if o.CAFile != "" {
		pool, n, err := LoadPool(o.CAFile)
		if err != nil {
			return nil, err
		}
		roots, trust = pool, fmt.Sprintf("%s (%d CA)", o.CAFile, n)
	} else if roots, err = x509.SystemCertPool(); err != nil {
		return nil, err
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	chains, err := certs[0].Verify(x509.VerifyOptions{DNSName: verifyName, Roots: roots, Intermediates: inter,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now})
	if err != nil {
		res.add("verify %s against %s: FAILED", verifyName, trust)
		res.add("  go error: %v", err)
		res.add("  meaning:  %s", Explain(err))
		return res, nil
	}
	var path []string
	for _, c := range chains[0] {
		path = append(path, c.Subject.CommonName)
	}
	res.add("verify %s against %s: OK: %s", verifyName, trust, strings.Join(path, " → "))
	res.Verified = true
	return res, nil
}

func handshake(ctx context.Context, o InspectOptions, cfg *tls.Config, res *InspectResult, first bool) (tls.ConnectionState, error) {
	d := net.Dialer{Timeout: o.Timeout}
	start := time.Now()
	raw, err := d.DialContext(ctx, "tcp", o.Addr)
	if err != nil {
		res.add("  tcp connect failed: %v", err)
		return tls.ConnectionState{}, err
	}
	tcpTime := time.Since(start)
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(o.Timeout))
	c := tls.Client(raw, cfg)
	hs := time.Now()
	err = c.HandshakeContext(ctx)
	hsTime := time.Since(hs)
	if err != nil {
		res.add("  tcp connect %s, TLS handshake FAILED after %s", ms(tcpTime), ms(hsTime))
		res.add("  go error: %v", err)
		res.add("  meaning:  %s", explainHandshake(err))
		return tls.ConnectionState{}, err
	}
	st := c.ConnectionState()
	resumed := ""
	if st.DidResume {
		resumed = ", session RESUMED (no certificate exchanged)"
	}
	alpn := st.NegotiatedProtocol
	if alpn == "" {
		alpn = "none"
	}
	res.add("  tcp connect %s, TLS handshake %s: %s, %s, ALPN %s%s", ms(tcpTime), ms(hsTime), VersionName(st.Version),
		tls.CipherSuiteName(st.CipherSuite), alpn, resumed)
	if o.Request {
		host := cfg.ServerName
		if host == "" {
			host, _, _ = net.SplitHostPort(o.Addr)
		}
		if err := request(c, st.NegotiatedProtocol, host); err != nil {
			res.add("  request after the handshake FAILED: %v", err)
			res.add("  meaning:  %s", explainHandshake(err))
			return st, err
		} else {
			res.add("  %s", lastLine)
		}
	}
	_ = first
	return st, nil
}

var lastLine string

// request proves the connection works end to end (and, in TLS 1.3, is where a
// server's rejection of a client certificate, and its session tickets, arrive).
func request(c *tls.Conn, alpn, host string) error {
	if alpn == "h2" {
		// The HTTP/2 connection preface and an empty SETTINGS frame; the
		// server must answer with its own SETTINGS frame (RFC 9113 3.4).
		if _, err := io.WriteString(c, "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00"); err != nil {
			return err
		}
		var hdr [9]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return err
		}
		if hdr[3] != 0x4 {
			return fmt.Errorf("expected a SETTINGS frame, got frame type %d", hdr[3])
		}
		lastLine = "HTTP/2: the server's SETTINGS frame arrived"
		return nil
	}
	fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	r := bufio.NewReader(c)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	lastLine = "HTTP/1.1: " + strings.TrimSpace(line)
	return nil
}

func explainHandshake(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "protocol version"):
		return "no TLS version both sides allow (one side too old, or the other's minimum too high)"
	case strings.Contains(s, "certificate required"):
		return "the server requires a client certificate (mTLS) and none was sent"
	case strings.Contains(s, "bad certificate"), strings.Contains(s, "unknown certificate authority"):
		return "the server rejected the client certificate it was sent"
	case strings.Contains(s, "handshake failure"), strings.Contains(s, "no cipher suite"):
		return "no cipher suite / group / signature algorithm in common"
	case strings.Contains(s, "first record does not look like a TLS handshake"):
		return "the server answered in plaintext — not a TLS port (often HTTP)"
	case strings.Contains(s, "connection reset"), strings.Contains(s, "EOF"):
		return "the peer closed the connection during the handshake (a middlebox, or a server rejecting the ClientHello)"
	case strings.Contains(s, "timeout"):
		return "no answer to the ClientHello within the timeout"
	}
	return s
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d)/1e6) }
