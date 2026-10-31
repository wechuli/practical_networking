package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// start serves cfg on loopback and returns a client that trusts its CA and
// sends every request to it, whatever the hostname.
func start(t *testing.T, cfg Config) (*http.Client, string) {
	t.Helper()
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.HTTPS.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = srv.HTTPS.Close() })

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(srv.CAPEM) {
		t.Fatal("CA PEM did not parse")
	}
	addr := ln.Addr().String()
	transport := &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool},
		ForceAttemptHTTP2: true,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}
	t.Cleanup(transport.CloseIdleConnections)
	_, port, _ := net.SplitHostPort(addr)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}, port
}

func TestServesTrustedChainOverHTTP2(t *testing.T) {
	client, port := start(t, Config{Name: "example.test", BodyBytes: 1500})
	resp, err := client.Get("https://example.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.ProtoMajor != 2 || len(body) != 1500 || resp.ContentLength != 1500 {
		t.Fatalf("got %s, %d bytes (Content-Length %d)", resp.Proto, len(body), resp.ContentLength)
	}
	chain := resp.TLS.PeerCertificates
	if len(chain) != 2 || chain[0].Subject.CommonName != "example.test" || !chain[1].IsCA {
		t.Fatalf("chain = %d certs, leaf %q", len(chain), chain[0].Subject.CommonName)
	}
}

func TestCertificateCoversLoopbackIP(t *testing.T) {
	client, port := start(t, Config{Name: "example.test", BodyBytes: 10})
	resp, err := client.Get("https://127.0.0.1:" + port + "/")
	if err != nil {
		t.Fatalf("an IP-literal URL should verify: %v", err)
	}
	resp.Body.Close()
}

func TestWrongNameFailsVerification(t *testing.T) {
	client, port := start(t, Config{Name: "example.test", BodyBytes: 10})
	_, err := client.Get("https://wrong.test:" + port + "/")
	var hostErr x509.HostnameError
	if err == nil || !errors.As(err, &hostErr) {
		t.Fatalf("want a hostname error, got %v", err)
	}
}

func TestDelayIsApplied(t *testing.T) {
	client, port := start(t, Config{Name: "example.test", BodyBytes: 10, Delay: 200 * time.Millisecond})
	begin := time.Now()
	resp, err := client.Get("https://example.test:" + port + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if took := time.Since(begin); took < 200*time.Millisecond {
		t.Fatalf("response after %v, want at least the 200ms delay", took)
	}
}

func TestMakePageSizes(t *testing.T) {
	for _, n := range []int{0, 1, 50, 81, 82, 1024, 100000} {
		if got := len(makePage(n)); got != n {
			t.Errorf("makePage(%d) has %d bytes", n, got)
		}
	}
}

func TestRejectsBadConfig(t *testing.T) {
	if _, err := New(Config{Name: "", BodyBytes: 1}); err == nil {
		t.Error("empty name should be rejected")
	}
	if _, err := New(Config{Name: "x.test", BodyBytes: -1}); err == nil {
		t.Error("negative size should be rejected")
	}
}
