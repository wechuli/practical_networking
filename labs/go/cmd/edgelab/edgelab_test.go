package main

import (
	"bufio"
	"context"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNTPTimestampRoundTrip(t *testing.T) {
	now := time.Now()
	got := fromNTP(toNTP(now))
	if d := got.Sub(now); d < -time.Microsecond || d > time.Microsecond {
		t.Fatalf("round trip off by %v", d)
	}
}

func freeUDP(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	return addr
}

func serveNTP(t *testing.T, offset, in, out time.Duration) string {
	t.Helper()
	addr := freeUDP(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = NTPServe(ctx, addr, offset, in, out, 2, io.Discard) }()
	time.Sleep(50 * time.Millisecond)
	return addr
}

func TestNTPOffsetAndAsymmetry(t *testing.T) {
	out, err := NTPQuery(serveNTP(t, 5*time.Minute, 0, 0), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(+300.00 s)") {
		t.Errorf("a server 5 minutes ahead should show +300 s:\n%s", out)
	}
	// same clock, 0 ms in and 40 ms out: the formula splits the 40 ms
	// evenly and reports a −20 ms offset that is not there
	out, err = NTPQuery(serveNTP(t, 0, 0, 40*time.Millisecond), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "(-0.02 s), delay 4") {
		t.Errorf("40 ms of one-way asymmetry should read as −20 ms:\n%s", out)
	}
}

func TestBackendHandler(t *testing.T) {
	var served atomic.Int64
	srv := httptest.NewServer(BackendHandler(BackendOptions{ID: "b9"}, &served))
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/?app=health", nil)
	req.Header.Add("X-Forwarded-For", "6.6.6.6")
	req.Header.Add("X-Forwarded-For", "198.18.0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "backend=b9") || !strings.Contains(string(body), "xff=6.6.6.6 | 198.18.0.1") {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}
	if r, _ := http.Get(srv.URL + "/admin/health?code=500"); r != nil {
		r.Body.Close()
	}
	for path, want := range map[string]int{"/health": 500, "/?app=health": 500, "/": 200} {
		r, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		r.Body.Close()
		if r.StatusCode != want {
			t.Errorf("%s after /admin/health?code=500: got %d, want %d", path, r.StatusCode, want)
		}
	}
	if served.Load() != 3 {
		t.Errorf("served %d page requests, want 3", served.Load())
	}
}

func startBackend(t *testing.T, o BackendOptions) (string, *strings.Builder) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o.Addr = ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var log strings.Builder
	go func() { _ = Backend(ctx, o, &syncWriter{w: &log}) }()
	for i := 0; i < 50; i++ {
		if c, err := net.Dial("tcp4", o.Addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return o.Addr, &log
}

type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func TestProxyProtocol(t *testing.T) {
	addr, _ := startBackend(t, BackendOptions{ID: "pp", ProxyProtocol: true})
	// the readiness probe above was itself a connection without a header
	got := RawRequest(addr, `PROXY TCP4 203.0.113.7 198.18.0.1 40000 80\r\nGET / HTTP/1.1\r\nHost: x\r\n\r\n`)
	if got != "reply: HTTP/1.1 200 OK\n" {
		t.Fatalf("with a PROXY header: %q", got)
	}
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprint(c, "PROXY TCP4 203.0.113.7 198.18.0.1 40000 80\r\nGET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	body, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(body), "proxy-protocol-client=203.0.113.7:40000") {
		t.Errorf("client address from the PROXY header missing:\n%s", body)
	}
	if got := RawRequest(addr, `GET / HTTP/1.1\r\nHost: x\r\n\r\n`); !strings.HasPrefix(got, "no reply") {
		t.Errorf("without a header the backend should close; got %q", got)
	}
	plain, _ := startBackend(t, BackendOptions{ID: "plain"})
	if got := RawRequest(plain, `PROXY TCP4 203.0.113.7 198.18.0.1 40000 80\r\nGET / HTTP/1.1\r\nHost: x\r\n\r\n`); got != "reply: HTTP/1.1 400 Bad Request\n" {
		t.Errorf("a PROXY header to a plain backend: %q", got)
	}
}

func TestHitsCounts(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		if i%4 == 0 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprintf(w, "backend=b%d\n", i%2)
	}))
	defer srv.Close()
	out := Hits(context.Background(), HitsOptions{URL: srv.URL, N: 8, Timeout: time.Second})
	for _, want := range []string{"2 × HTTP 503", "4 × HTTP 200 from b1", "2 × HTTP 200 from b0", "failures at:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestForwardProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "origin saw %s", r.URL.Path)
	}))
	defer origin.Close()
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var log strings.Builder
	go func() { _ = ForwardProxy(ctx, addr, &syncWriter{w: &log}) }()
	time.Sleep(50 * time.Millisecond)

	// absolute-URI request
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(c, "GET %s/abs HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", origin.URL)
	b, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(b), "origin saw /abs") {
		t.Errorf("absolute URI through the proxy: %q", b)
	}
	// CONNECT tunnel
	c, err = net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	host := strings.TrimPrefix(origin.URL, "http://")
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host)
	r := bufio.NewReader(c)
	status, _ := r.ReadString('\n')
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT: %q", status)
	}
	_, _ = r.ReadString('\n') // blank line
	fmt.Fprintf(c, "GET /tunnel HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", host)
	b, _ = io.ReadAll(r)
	c.Close()
	if !strings.Contains(string(b), "origin saw /tunnel") {
		t.Errorf("request inside the tunnel: %q", b)
	}
}

func TestTLSClock(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	addr := strings.TrimPrefix(srv.URL, "https://")
	if got := TLSClock(addr, "example.com", ca, 0); !strings.Contains(got, ": OK (") {
		t.Errorf("right clock: %s", got)
	}
	leaf := srv.Certificate()
	before := -time.Since(leaf.NotBefore) - time.Hour
	if got := TLSClock(addr, "example.com", ca, before); !strings.Contains(got, "is before") {
		t.Errorf("clock before NotBefore: %s", got)
	}
}
