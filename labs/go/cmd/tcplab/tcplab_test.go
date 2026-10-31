//go:build linux

package main

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// freePort reserves a loopback port and releases it for Serve to use.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func startServer(t *testing.T, o ServeOptions) string {
	t.Helper()
	if o.Addr == "" {
		o.Addr = freePort(t)
	}
	if o.Backlog == 0 {
		o.Backlog = 128
	}
	o.Quiet = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errc := make(chan error, 1)
	go func() { errc <- Serve(ctx, o, io.Discard) }()
	// Wait until it accepts connections (or, for noaccept, completes handshakes).
	for i := 0; i < 50; i++ {
		select {
		case err := <-errc:
			t.Fatalf("serve: %v", err)
		default:
		}
		if c, err := net.Dial("tcp4", o.Addr); err == nil {
			c.Close()
			return o.Addr
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return ""
}

func TestDialRefused(t *testing.T) {
	rs := Dial(context.Background(), DialOptions{To: freePort(t), Count: 1, Timeout: 2 * time.Second})
	if rs[0].Outcome != "refused" {
		t.Fatalf("got %+v", rs[0])
	}
	if !strings.Contains(FormatDials(rs), "ECONNREFUSED") {
		t.Error(FormatDials(rs))
	}
}

func TestDialConnected(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "discard"})
	rs := Dial(context.Background(), DialOptions{To: addr, Count: 3, Timeout: 2 * time.Second})
	out := FormatDials(rs)
	if !strings.Contains(out, "summary: 3 connected") {
		t.Fatal(out)
	}
}

func TestSendComplete(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "discard"})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 1 << 20, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || r.Written != 1<<20 || r.Mbps <= 0 {
		t.Fatalf("%+v", r)
	}
	if r.Info.SndMSS == 0 || r.Info.Options&optSACK == 0 || r.CC == "" {
		t.Errorf("tcp_info looks empty: %+v", r.Info)
	}
	if !strings.Contains(r.Format(), "complete: 1048576 bytes") {
		t.Error(r.Format())
	}
}

func TestSendEcho(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "echo"})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 100 << 10, Timeout: 5 * time.Second})
	if err != nil || !r.Complete {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestSendCongestionControl(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "discard"})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 64 << 10, CC: "reno", Timeout: 5 * time.Second})
	if err != nil {
		t.Skipf("reno not settable here: %v", err)
	}
	if r.CC != "reno" {
		t.Errorf("cc %q", r.CC)
	}
	if _, err := Send(context.Background(), SendOptions{To: addr, Bytes: 1, CC: "no-such-algo", Timeout: time.Second}); err == nil {
		t.Error("an unknown algorithm was accepted")
	}
}

func TestSendHoldNeverCloses(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "hold"})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 10 << 10, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || r.Written != 10<<10 || !strings.Contains(r.Problem, "never closed") {
		t.Fatalf("%+v", r)
	}
	if r.Info.State != "FIN_WAIT2" {
		t.Errorf("state %s, want FIN_WAIT2", r.Info.State)
	}
}

func TestSendReset(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "reset"})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 4 << 20, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || !(strings.Contains(r.Problem, "ECONNRESET") || strings.Contains(r.Problem, "EPIPE")) {
		t.Fatalf("%+v", r)
	}
}

func TestSendZeroWindow(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "noread", RcvBuf: 4096})
	r, err := Send(context.Background(), SendOptions{To: addr, Bytes: 8 << 20, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Complete || r.Written >= 8<<20 || !strings.Contains(r.Problem, "zero window") {
		t.Fatalf("%+v", r)
	}
}

func TestNoAcceptQueueFills(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "noaccept", Backlog: 1})
	// startServer's probe connection already sits in the queue; with backlog 1
	// Linux queues backlog+1 = 2, so one more fits and the rest are dropped.
	rs := Dial(context.Background(), DialOptions{To: addr, Count: 4, Timeout: 500 * time.Millisecond})
	var ok, gaveUp int
	for _, r := range rs {
		switch r.Outcome {
		case "connected":
			ok++
		case "gave up":
			gaveUp++
		}
	}
	if ok == 0 || gaveUp == 0 {
		t.Fatalf("connected %d, gave up %d:\n%s", ok, gaveUp, FormatDials(rs))
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"1500": 1500, "64K": 65536, "20M": 20 << 20, "1G": 1 << 30} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "1T", "M"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if fmtSize(20<<20) != "20 MiB" || fmtSize(1500) != "1500 bytes" {
		t.Error(fmtSize(20<<20), fmtSize(1500))
	}
}

func TestClassify(t *testing.T) {
	if o, _ := Classify(nil); o != "connected" {
		t.Error(o)
	}
	if o, _ := Classify(context.DeadlineExceeded); o != "gave up" {
		t.Error(o)
	}
}

func TestSendToClosingServer(t *testing.T) {
	addr := startServer(t, ServeOptions{Mode: "close"})
	c, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("GET / HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 10)); n != 0 || err != io.EOF {
		t.Fatalf("read %d, %v; want a clean EOF", n, err)
	}
}
