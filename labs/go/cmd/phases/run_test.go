package main

import (
	"context"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tlsServer starts an HTTPS server with HTTP/2 enabled and writes its
// self-signed certificate (valid for 127.0.0.1 and example.com) to a PEM file.
func tlsServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, string) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv, caFile
}

func hello(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("hello")) }

func run(t *testing.T, opts Options) *Report {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = 5 * time.Second
	}
	r, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return r
}

func stepNames(r *Report) []string {
	var names []string
	for _, s := range r.Steps {
		names = append(names, s.Name)
	}
	return names
}

func TestSuccessfulHTTPS(t *testing.T) {
	srv, ca := tlsServer(t, hello)
	r := run(t, Options{URL: srv.URL, CAFile: ca})
	if r.Error != "" {
		t.Fatalf("unexpected failure: %s", r.Error)
	}
	if r.Proto != "HTTP/2.0" || !strings.HasPrefix(r.Status, "200") || r.BodyBytes != 5 {
		t.Fatalf("got %s %s, %d bytes", r.Proto, r.Status, r.BodyBytes)
	}
	// An IP-literal URL needs no DNS step.
	want := []string{phaseConnect, phaseTLS, phaseRequest, phaseWait, phaseTransfer}
	if got := stepNames(r); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps = %v, want %v", got, want)
	}
	if r.TLS == nil || r.TLS.Version != "TLS 1.3" || r.TLS.ALPN != "h2" {
		t.Fatalf("TLS = %+v", r.TLS)
	}
}

func TestNameIsResolvedFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(hello))
	t.Cleanup(srv.Close)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	r := run(t, Options{URL: "http://localhost:" + port + "/"})
	if r.Error != "" {
		t.Fatalf("unexpected failure: %s", r.Error)
	}
	if len(r.Steps) == 0 || r.Steps[0].Name != phaseDNS || !r.Steps[0].Done {
		t.Fatalf("first step should be a completed DNS lookup: %+v", r.Steps)
	}
}

func TestUntrustedCertificate(t *testing.T) {
	srv, _ := tlsServer(t, hello)
	r := run(t, Options{URL: srv.URL}) // no -cacert: the test CA is unknown
	if r.FailedPhase != phaseTLS || !strings.Contains(r.Meaning, "authority this client trusts") {
		t.Fatalf("phase %q, meaning %q", r.FailedPhase, r.Meaning)
	}
}

func TestHostnameMismatchViaResolve(t *testing.T) {
	srv, ca := tlsServer(t, hello)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	r := run(t, Options{
		URL:     "https://wrong.test:" + port + "/",
		Resolve: "wrong.test:" + port + ":127.0.0.1",
		CAFile:  ca,
		NoProxy: true, // a non-loopback name would otherwise go to any proxy in the environment
	})
	for _, s := range r.Steps {
		if s.Name == phaseDNS {
			t.Fatal("-resolve should skip DNS")
		}
	}
	if r.FailedPhase != phaseTLS || !strings.Contains(r.Meaning, "not issued for this hostname") {
		t.Fatalf("phase %q, meaning %q", r.FailedPhase, r.Meaning)
	}
}

func TestPlainHTTPOnHTTPSURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(hello))
	t.Cleanup(srv.Close)
	r := run(t, Options{URL: strings.Replace(srv.URL, "http://", "https://", 1)})
	if !strings.Contains(r.Meaning, "did not answer with TLS") {
		t.Fatalf("phase %q, error %q, meaning %q", r.FailedPhase, r.Error, r.Meaning)
	}
}

func TestConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens now: the kernel answers the SYN with an RST
	r := run(t, Options{URL: "http://" + addr + "/"})
	if r.FailedPhase != phaseConnect || !strings.Contains(r.Meaning, "connection refused") {
		t.Fatalf("phase %q, meaning %q", r.FailedPhase, r.Meaning)
	}
}

func TestSlowServerTimesOutInServerWait(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	r := run(t, Options{URL: srv.URL, Timeout: 300 * time.Millisecond})
	if r.FailedPhase != phaseWait || !strings.HasPrefix(r.Meaning, "Timeout") {
		t.Fatalf("phase %q, meaning %q", r.FailedPhase, r.Meaning)
	}
	last := r.Steps[len(r.Steps)-1]
	if last.Name != phaseWait || last.Done {
		t.Fatalf("server wait should be the unfinished step: %+v", last)
	}
}

func TestDiagnoseDNS(t *testing.T) {
	cases := map[string]error{
		"does not exist": &net.DNSError{Err: "no such host", Name: "x.invalid", IsNotFound: true},
		"did not answer": &net.DNSError{Err: "i/o timeout", Name: "x.example", IsTimeout: true},
		"SERVFAIL":       &net.DNSError{Err: "server misbehaving", Name: "x.example"},
	}
	for want, err := range cases {
		wrapped := &net.OpError{Op: "dial", Net: "tcp", Err: err}
		if got := Diagnose(wrapped); !strings.Contains(got, want) {
			t.Errorf("Diagnose(%v) = %q, want it to mention %q", err, got, want)
		}
	}
	if got := Diagnose(errors.New("something else")); !strings.HasPrefix(got, "Unclassified") {
		t.Errorf("unknown error classified as %q", got)
	}
}

func TestParseResolve(t *testing.T) {
	for in, want := range map[string]string{
		"example.com:443:203.0.113.10":  "203.0.113.10:443",
		"example.com:443:[2001:db8::1]": "[2001:db8::1]:443",
		"example.com:443:2001:db8::1":   "[2001:db8::1]:443",
	} {
		_, got, err := parseResolve(in)
		if err != nil || got != want {
			t.Errorf("parseResolve(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"example.com:443", "example.com:443:not-an-ip", "::1"} {
		if _, _, err := parseResolve(bad); err == nil {
			t.Errorf("parseResolve(%q) should fail", bad)
		}
	}
}

// fakeProxy answers every CONNECT with the given status, like a proxy
// enforcing an allow-list (403) or authentication (407).
func fakeProxy(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Errorf("proxy got %s, want CONNECT", r.Method)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestProxyRefusesTunnel(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusForbidden:         "refused to open a CONNECT tunnel",
		http.StatusProxyAuthRequired: "wants credentials",
		http.StatusBadGateway:        "tunnel was not opened",
	} {
		r := run(t, Options{URL: "https://registry.example.test/", Proxy: fakeProxy(t, status)})
		if r.FailedPhase != phaseTunnel || !strings.Contains(r.Meaning, want) {
			t.Errorf("status %d: phase %q, error %q, meaning %q", status, r.FailedPhase, r.Error, r.Meaning)
		}
	}
}

func TestReuseSkipsSetupPhases(t *testing.T) {
	srv, ca := tlsServer(t, hello)
	reports, err := RunAll(context.Background(), Options{URL: srv.URL, CAFile: ca, Count: 3, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 3 {
		t.Fatalf("got %d reports, want 3", len(reports))
	}
	if reports[0].Reused || !strings.Contains(strings.Join(stepNames(reports[0]), ","), phaseTLS) {
		t.Fatalf("first request should set up a new connection: %v", stepNames(reports[0]))
	}
	for _, r := range reports[1:] {
		want := []string{phaseRequest, phaseWait, phaseTransfer}
		if !r.Reused || strings.Join(stepNames(r), ",") != strings.Join(want, ",") {
			t.Fatalf("later request: reused=%v steps=%v, want reused with %v", r.Reused, stepNames(r), want)
		}
	}
}

func TestRecorderEndBeforeStart(t *testing.T) {
	r := newRecorder()
	r.done(phaseWait, "", nil) // first byte reported before the write completed
	r.start(phaseWait, phaseWait, "until the first response byte")
	steps, _, _ := r.snapshot()
	if len(steps) != 1 || !steps[0].Done || steps[0].TookMS != 0 {
		t.Fatalf("steps = %+v, want one finished step of 0ms", steps)
	}
	if len(r.open) != 0 {
		t.Fatalf("no step should be left open: %v", r.open)
	}
}
