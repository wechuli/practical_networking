package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// HitsOptions controls Hits.
type HitsOptions struct {
	URL      string
	N        int
	Interval time.Duration
	Cookies  bool
	XFF      string
	Timeout  time.Duration
	Show     int  // print the first N response bodies
	EnvProxy bool // use HTTP_PROXY/http_proxy/NO_PROXY like most Go programs do
}

// Hits sends N requests one after another and counts which backend
// answered each, and how many failed and when.
func Hits(ctx context.Context, o HitsOptions) string {
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	if o.EnvProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	c := &http.Client{Transport: tr, Timeout: o.Timeout}
	if o.Cookies {
		c.Jar, _ = cookiejar.New(nil)
	}
	counts := map[string]int{}
	var errTimes, slow []string
	start := time.Now()
	var b strings.Builder
	for i := 0; i < o.N; i++ {
		if i > 0 && o.Interval > 0 {
			time.Sleep(o.Interval)
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", o.URL, nil)
		if o.XFF != "" {
			req.Header.Set("X-Forwarded-For", o.XFF)
		}
		at := time.Since(start)
		resp, err := c.Do(req)
		if took := time.Since(start) - at; took > 250*time.Millisecond {
			slow = append(slow, fmt.Sprintf("%.1fs (took %.1fs)", at.Seconds(), took.Seconds()))
		}
		if err != nil {
			counts["error: "+shortErr(err)]++
			errTimes = append(errTimes, fmt.Sprintf("%.1fs", at.Seconds()))
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		line := strings.TrimSpace(string(body))
		if i < o.Show {
			fmt.Fprintf(&b, "  response %d: HTTP %d %s\n", i+1, resp.StatusCode, line)
		}
		key := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if f := field(line, "backend="); f != "" {
			key += " from " + f
		}
		if resp.StatusCode >= 500 {
			errTimes = append(errTimes, fmt.Sprintf("%.1fs", at.Seconds()))
		}
		counts[key]++
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	head := fmt.Sprintf("edgelab hits: %d requests to %s in %s (new connection each, cookies %v)\n", o.N, o.URL, time.Since(start).Round(time.Millisecond), o.Cookies)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %4d × %s\n", counts[k], k)
	}
	if len(errTimes) > 0 {
		fmt.Fprintf(&b, "  failures at: %s\n", strings.Join(errTimes, " "))
	}
	if len(slow) > 0 {
		fmt.Fprintf(&b, "  slow (> 250 ms) at: %s\n", strings.Join(slow, ", "))
	}
	return head + b.String()
}

func field(line, prefix string) string {
	for _, f := range strings.Fields(line) {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix)
		}
	}
	return ""
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		return s[i+2:]
	}
	return s
}

// ForwardProxy is a minimal HTTP forward proxy: CONNECT tunnels for HTTPS
// and absolute-URI requests for plain HTTP, logging each.
func ForwardProxy(ctx context.Context, addr string, w io.Writer) error {
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
	}
	tr := &http.Transport{Proxy: nil}
	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			logf("CONNECT %s from %s (the proxy sees only the destination; TLS runs inside the tunnel)", r.Host, r.RemoteAddr)
			up, err := net.DialTimeout("tcp4", r.Host, 5*time.Second)
			if err != nil {
				http.Error(rw, err.Error(), http.StatusBadGateway)
				return
			}
			hj, _ := rw.(http.Hijacker)
			cc, buf, err := hj.Hijack()
			if err != nil {
				up.Close()
				return
			}
			fmt.Fprint(cc, "HTTP/1.1 200 Connection established\r\n\r\n")
			go func() {
				if buf.Reader.Buffered() > 0 {
					b := make([]byte, buf.Reader.Buffered())
					_, _ = buf.Read(b)
					_, _ = up.Write(b)
				}
				_, _ = io.Copy(up, cc)
				up.Close()
			}()
			_, _ = io.Copy(cc, up)
			cc.Close()
			return
		}
		if !r.URL.IsAbs() {
			http.Error(rw, "this is a forward proxy: send an absolute URI or CONNECT", http.StatusBadRequest)
			return
		}
		logf("GET-style %s %s from %s (plain HTTP: the proxy sees the whole request)", r.Method, r.URL, r.RemoteAddr)
		r.RequestURI = ""
		resp, err := tr.RoundTrip(r)
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				rw.Header().Add(k, v)
			}
		}
		rw.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(rw, resp.Body)
	})}
	go func() { <-ctx.Done(); srv.Close() }()
	logf("edgelab forward proxy on %s", ln.Addr())
	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// TLSClock connects with the clock shifted by offset and reports whether
// the certificate verifies at that time.
func TLSClock(addr, serverName, caFile string, offset time.Duration) string {
	pool := x509.NewCertPool()
	if pem, err := os.ReadFile(caFile); err == nil {
		pool.AppendCertsFromPEM(pem)
	}
	now := time.Now().Add(offset)
	conf := &tls.Config{RootCAs: pool, ServerName: serverName, Time: func() time.Time { return now }}
	c, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp4", addr, conf)
	sign := "+"
	if offset < 0 {
		sign = ""
	}
	label := fmt.Sprintf("clock %s%v (%s)", sign, offset, now.UTC().Format("2006-01-02 15:04 UTC"))
	if err != nil {
		return fmt.Sprintf("%s: FAILED: %v\n", label, err)
	}
	defer c.Close()
	leaf := c.ConnectionState().PeerCertificates[0]
	return fmt.Sprintf("%s: OK (certificate valid %s → %s)\n", label, leaf.NotBefore.UTC().Format("2006-01-02 15:04"), leaf.NotAfter.UTC().Format("2006-01-02 15:04"))
}

// RawRequest writes text to addr and prints the reply's first line.
func RawRequest(addr, text string) string {
	c, err := net.DialTimeout("tcp4", addr, 3*time.Second)
	if err != nil {
		return "error: " + err.Error() + "\n"
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = io.WriteString(c, strings.ReplaceAll(text, `\r\n`, "\r\n"))
	line, err := bufio.NewReader(c).ReadString('\n')
	if line == "" && err != nil {
		return fmt.Sprintf("no reply (%v)\n", err)
	}
	return "reply: " + strings.TrimSpace(line) + "\n"
}
