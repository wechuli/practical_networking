package main

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func startEcho(t *testing.T, delay time.Duration) string {
	t.Helper()
	// Reserve a port, then let Echo listen on it.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Echo(ctx, addr, delay, false, io.Discard) }()
	time.Sleep(50 * time.Millisecond)
	return addr
}

func TestEncodeDecode(t *testing.T) {
	b := make([]byte, 64)
	encode(b, 42, 3)
	seq, attempt, ok := decode(b)
	if !ok || seq != 42 || attempt != 3 {
		t.Fatal(seq, attempt, ok)
	}
	if _, _, ok := decode(make([]byte, 64)); ok {
		t.Error("zero buffer decoded")
	}
}

func TestSendAllAnswered(t *testing.T) {
	addr := startEcho(t, 0)
	r, err := Send(context.Background(), SendOptions{To: addr, Count: 20, Size: 100, Interval: time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Answered != 20 || r.FirstTry != 20 || r.Sent != 20 || r.RTTms == nil {
		t.Errorf("%+v", r)
	}
}

func TestClosedPortConnectedVsUnconnected(t *testing.T) {
	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	addr := pc.LocalAddr().String()
	pc.Close() // nothing listens now: the kernel answers ICMP port unreachable
	c, err := Send(context.Background(), SendOptions{To: addr, Count: 3, Size: 32, Interval: time.Millisecond, Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if c.Answered != 0 || c.Errors["connection refused (ICMP port unreachable)"] == 0 {
		t.Errorf("connected: %+v", c)
	}
	u, err := Send(context.Background(), SendOptions{To: addr, Count: 3, Size: 32, Interval: time.Millisecond, Timeout: 200 * time.Millisecond, Unconnected: true})
	if err != nil {
		t.Fatal(err)
	}
	if u.Answered != 0 || len(u.Errors) != 0 || !strings.Contains(u.Format(), "never told about ICMP errors") {
		t.Errorf("unconnected: %+v", u)
	}
}

func TestTruncation(t *testing.T) {
	addr := startEcho(t, 0)
	r, err := Send(context.Background(), SendOptions{To: addr, Count: 2, Size: 1000, RecvBuf: 100, Interval: time.Millisecond, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Truncated != 2 || r.Answered != 2 || !strings.Contains(r.TruncDetail, "MSG_TRUNC") {
		t.Errorf("%+v", r)
	}
}

func TestRetriesRecoverFromSlowEcho(t *testing.T) {
	// Replies arrive after 300 ms; with a 200 ms timeout the first attempt
	// "fails", and the late echo is counted when it arrives during the retry.
	addr := startEcho(t, 300*time.Millisecond)
	r, err := Send(context.Background(), SendOptions{To: addr, Count: 1, Size: 32, Timeout: 200 * time.Millisecond, Retries: 2})
	if err != nil {
		t.Fatal(err)
	}
	if r.Sent < 2 || r.FirstTry != 0 {
		t.Errorf("%+v", r)
	}
}
