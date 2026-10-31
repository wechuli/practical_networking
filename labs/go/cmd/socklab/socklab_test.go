//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestSummarize(t *testing.T) {
	got := summarize([]int{10, 10, 10, 970})
	if got != "10 ×3, 970" {
		t.Fatalf("summarize = %q", got)
	}
}

func TestErrnoName(t *testing.T) {
	err := &net.OpError{Op: "dial", Err: &net.AddrError{}}
	if errnoName(err) != "no errno" {
		t.Fatalf("errnoName(no errno) = %q", errnoName(err))
	}
	wrapped := fmt.Errorf("connect: %w", syscall.EADDRNOTAVAIL)
	if !strings.HasPrefix(errnoName(wrapped), "EADDRNOTAVAIL") {
		t.Fatalf("errnoName(EADDRNOTAVAIL) = %q", errnoName(wrapped))
	}
}

func startServe(t *testing.T, o ServeOptions) (string, *safeBuf) {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	o.Addr = ln.Addr().String()
	ln.Close()
	ctx, cancel := context.WithCancel(context.Background())
	buf := &safeBuf{}
	done := make(chan struct{})
	go func() { _ = Serve(ctx, o, buf); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp4", o.Addr); err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return o.Addr, buf
}

type safeBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *safeBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *safeBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func TestGreetAndModels(t *testing.T) {
	for _, model := range []string{"netpoll", "threads"} {
		addr, _ := startServe(t, ServeOptions{Mode: "greet", Model: model, ID: model})
		out := Greetings(addr, 5)
		if !strings.Contains(out, "5  hello from "+model) {
			t.Fatalf("%s: %s", model, out)
		}
	}
}

func TestChurnServerCloses(t *testing.T) {
	addr, _ := startServe(t, ServeOptions{Mode: "close", Model: "netpoll"})
	out := Churn(addr, 20, true)
	if !strings.Contains(out, "20 of 20") || !strings.Contains(out, "the server closes first") {
		t.Fatal(out)
	}
}

func TestBindConflict(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var out bytes.Buffer
	if err := Bind(ln.Addr().String(), true, false, &out); err == nil {
		t.Fatal("bind to a port with a live listener succeeded")
	}
	if !strings.Contains(out.String(), "EADDRINUSE") {
		t.Fatal(out.String())
	}
}

func TestHTTPCountsDials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4096))
	}))
	defer srv.Close()
	out, err := HTTP(context.Background(), HTTPOptions{URL: srv.URL, N: 50, C: 5, KeepAlive: true, IdlePerHost: 5, Drain: true, CloseBody: true, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	// Five workers need at least five connections. Go's transport may dial
	// a few more: a request that finds every connection busy starts a dial,
	// and if a connection is returned first the request takes that one and
	// the new connection goes to the idle pool. On loopback under -race this
	// happens in a few percent of runs, so bound it instead of requiring 5.
	var dials, onNew, onReused int
	if i := strings.Index(out, "TCP connections dialed: "); i >= 0 {
		fmt.Sscanf(out[i:], "TCP connections dialed: %d; requests on a new connection %d, on a reused one %d", &dials, &onNew, &onReused)
	}
	if !strings.Contains(out, "50 ok") || dials < 5 || dials > 10 || onReused < 40 {
		t.Fatal(out)
	}
	out, _ = HTTP(context.Background(), HTTPOptions{URL: srv.URL, N: 20, C: 2, KeepAlive: false, IdlePerHost: 2, Drain: true, CloseBody: true, Timeout: 5 * time.Second})
	if !strings.Contains(out, "dialed: 20;") {
		t.Fatal(out)
	}
}

func TestReadsMode(t *testing.T) {
	addr, log := startServe(t, ServeOptions{Mode: "reads", Model: "netpoll"})
	var out bytes.Buffer
	if err := Writes(addr, 10, 10, true, &out); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(log.String(), "sent 100 bytes") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(log.String(), "sent 100 bytes") {
		t.Fatal(log.String())
	}
}

func TestStatSelf(t *testing.T) {
	s, err := Stat(syscall.Getpid())
	if err != nil || !strings.Contains(s, "threads") {
		t.Fatal(s, err)
	}
}
