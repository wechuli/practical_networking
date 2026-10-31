package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
)

// AppConfig is the capstone application's configuration: what it connects
// to and which CA it trusts. It is a file the learner may read and fix.
type AppConfig struct {
	URL     string `json:"url"`     // e.g. https://api/health — a short name, completed by the search list
	CAFile  string `json:"ca_file"` // the CA bundle the application trusts
	Timeout string `json:"timeout"` // per stage, e.g. "5s"
}

// LoadConfig reads and checks an AppConfig.
func LoadConfig(path string) (AppConfig, error) {
	var c AppConfig
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.URL == "" {
		return c, fmt.Errorf("%s: no url", path)
	}
	return c, nil
}

// ResolvConf is what the stub resolver will use, read the way the
// application reads it.
func ResolvConf(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "resolv.conf: " + err.Error()
	}
	defer f.Close()
	var ns, search, opts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "nameserver":
			ns = append(ns, fields[1])
		case "search", "domain":
			search = fields[1:]
		case "options":
			opts = append(opts, fields[1:]...)
		}
	}
	return fmt.Sprintf("resolv.conf: nameserver %s; search %s; options %s", orNone(ns), orNone(search), orNone(opts))
}

func orNone(v []string) string {
	if len(v) == 0 {
		return "(none)"
	}
	return strings.Join(v, " ")
}

// Probe does what the application does — resolve, connect, TLS with the
// configured trust, one HTTP GET — and reports each stage the way an
// application log would: what it tried and the error it got, no hints.
func Probe(ctx context.Context, c AppConfig, resolvConf string, w io.Writer) error {
	u, err := http.NewRequest("GET", c.URL, nil)
	if err != nil {
		return err
	}
	host, port := u.URL.Hostname(), u.URL.Port()
	if port == "" {
		port = "443"
	}
	timeout := 5 * time.Second
	if d, err := time.ParseDuration(c.Timeout); err == nil {
		timeout = d
	}
	fmt.Fprintf(w, "app: GET %s (trusting %s)\n", c.URL, c.CAFile)
	fmt.Fprintln(w, ResolvConf(resolvConf))

	// 1. name resolution, through the stub resolver and its search list
	r := &net.Resolver{PreferGo: true}
	start := time.Now()
	rctx, cancel := context.WithTimeout(ctx, 4*timeout)
	addrs, err := r.LookupHost(rctx, host)
	cancel()
	if err != nil {
		fmt.Fprintf(w, "resolve %s: FAILED after %s: %s\n", host, since(start), DescribeDNS(err))
		return err
	}
	fmt.Fprintf(w, "resolve %s: %s (%s)\n", host, strings.Join(addrs, " "), since(start))

	// 2. TCP to the first address
	addr := net.JoinHostPort(addrs[0], port)
	start = time.Now()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		fmt.Fprintf(w, "connect %s: FAILED after %s: %s\n", addr, since(start), DescribeConnect(err))
		return err
	}
	defer conn.Close()
	fmt.Fprintf(w, "connect %s: OK (%s)\n", addr, since(start))

	// 3. TLS, verifying the chain against the configured CA and the URL's host name
	pool := x509.NewCertPool()
	if pem, err := os.ReadFile(c.CAFile); err != nil {
		fmt.Fprintf(w, "tls: cannot read CA file: %v\n", err)
		return err
	} else if !pool.AppendCertsFromPEM(pem) {
		err := fmt.Errorf("no certificates in %s", c.CAFile)
		fmt.Fprintf(w, "tls: %v\n", err)
		return err
	}
	start = time.Now()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	tc := tls.Client(conn, &tls.Config{RootCAs: pool, ServerName: host})
	if err := tc.HandshakeContext(ctx); err != nil {
		fmt.Fprintf(w, "tls %s: FAILED after %s: %v\n", host, since(start), err)
		return err
	}
	st := tc.ConnectionState()
	fmt.Fprintf(w, "tls %s: OK, %s, certificate %q verified (%s)\n", host, versionName(st.Version), st.PeerCertificates[0].Subject.CommonName, since(start))

	// 4. HTTP
	start = time.Now()
	u.Host = u.URL.Host
	u.Header.Set("Connection", "close")
	if err := u.Write(tc); err != nil {
		fmt.Fprintf(w, "http: FAILED: %v\n", err)
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(tc), u)
	if err != nil {
		fmt.Fprintf(w, "http: FAILED after %s: %v\n", since(start), err)
		return err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
	resp.Body.Close()
	fmt.Fprintf(w, "http: %s (%s) %s\n", resp.Status, since(start), strings.TrimSpace(string(body)))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func since(t time.Time) string { return time.Since(t).Round(100 * time.Microsecond).String() }

func versionName(v uint16) string {
	switch v {
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	}
	return fmt.Sprintf("TLS 0x%04x", v)
}

// DescribeDNS says what kind of resolution failure Go's resolver reported.
// Go folds NXDOMAIN and "no A/AAAA records" into IsNotFound, and SERVFAIL
// and REFUSED into "server misbehaving".
func DescribeDNS(err error) string {
	var de *net.DNSError
	if !errors.As(err, &de) {
		return err.Error()
	}
	switch {
	case de.IsNotFound:
		return de.Error() + " [the name does not exist or has no address — for every name the search list produced]"
	case de.IsTimeout:
		return de.Error() + " [no answer from the nameserver(s)]"
	case strings.Contains(de.Err, "server misbehaving"):
		return de.Error() + " [the nameserver answered with an error: SERVFAIL or REFUSED]"
	}
	return de.Error()
}

// DescribeConnect names the usual connect failures.
func DescribeConnect(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return err.Error() + " [RST: the host answered, nothing listens on that port]"
	case errors.Is(err, syscall.ENETUNREACH):
		return err.Error() + " [no route to that network from here]"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return err.Error() + " [no route, ARP failure, or an ICMP unreachable]"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return err.Error() + " [no reply to the SYN: dropped, or nobody at that address]"
	}
	return err.Error()
}
