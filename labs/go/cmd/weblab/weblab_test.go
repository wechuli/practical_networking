package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAcceptMatchesRFC6455Example(t *testing.T) {
	// RFC 6455 section 1.3.
	if got := Accept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatal(got)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	for _, n := range []int{0, 5, 125, 126, 70000} {
		payload := bytes.Repeat([]byte("x"), n)
		for _, mask := range [][]byte{nil, {1, 2, 3, 4}} {
			wire := AppendFrame(nil, opBin, payload, mask)
			f, err := ReadFrame(bytes.NewReader(wire))
			if err != nil || !bytes.Equal(f.Payload, payload) || f.Masked != (mask != nil) || f.Wire != len(wire) {
				t.Fatalf("n=%d mask=%v: %+v %v", n, mask, f.Wire, err)
			}
		}
	}
	// A 5-byte masked text frame is 2 header + 4 mask + 5 payload bytes.
	if n := len(AppendFrame(nil, opText, []byte("hello"), []byte{9, 9, 9, 9})); n != 11 {
		t.Fatal(n)
	}
}

func TestStringValue(t *testing.T) {
	b := EncodeString("hello")
	if !bytes.Equal(b, []byte{0x0a, 5, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("% x", b)
	}
	if s, err := DecodeString(b); err != nil || s != "hello" {
		t.Fatal(s, err)
	}
	if EncodeString("") != nil {
		t.Fatal("proto3 omits default values")
	}
	// Unknown varint field 2 is skipped.
	if s, err := DecodeString(append([]byte{0x10, 0x96, 0x01}, b...)); err != nil || s != "hello" {
		t.Fatal(s, err)
	}
}

func TestTimeoutsAndStatusMapping(t *testing.T) {
	for in, want := range map[string]time.Duration{"500m": 500 * time.Millisecond, "2S": 2 * time.Second, "393449u": 393449 * time.Microsecond, "1M": time.Minute} {
		if got, err := ParseTimeout(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := ParseTimeout("5x"); err == nil {
		t.Error("bad unit accepted")
	}
	if FormatTimeout(1500*time.Millisecond) != "1500m" {
		t.Error(FormatTimeout(1500 * time.Millisecond))
	}
	for httpStatus, code := range map[int]int{400: 13, 404: 12, 502: 14, 503: 14, 500: 2} {
		if HTTPToGRPC(httpStatus) != code {
			t.Errorf("HTTP %d -> %d", httpStatus, HTTPToGRPC(httpStatus))
		}
	}
	if percentEncode("a b%\n") != "a b%25%0A" {
		t.Error(percentEncode("a b%\n"))
	}
}

func h2cServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(Handler(&logger{w: io.Discard}, ""))
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &p
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestGRPCCalls(t *testing.T) {
	srv := h2cServer(t)
	target := strings.TrimPrefix(srv.URL, "http://")
	cases := []struct {
		method, value string
		timeout       time.Duration
		code          int
		want          string
	}{
		{"course.Echo/Echo", "hi", 0, 0, `< DATA message "hi"`},
		{"course.Echo/Echo", "", 0, 3, "trailers-only response, HTTP 200"},
		{"course.Echo/Nope", "x", 0, 12, "unknown method Nope"},
		{"course.Echo/Slow", "x", 200 * time.Millisecond, 4, "DEADLINE_EXCEEDED"},
		{"course.Echo/Watch", "w", 0, 0, `"w 5"`},
	}
	for _, c := range cases {
		var out bytes.Buffer
		code, err := GRPCCall{Target: target, Method: c.method, Value: c.value, Timeout: c.timeout, Out: &out}.Run(context.Background())
		if err != nil || code != c.code || !strings.Contains(out.String(), c.want) {
			t.Errorf("%s(%q): code %d err %v, output:\n%s", c.method, c.value, code, err, out.String())
		}
	}
}

func TestGRPCOverHTTP1IsRejected(t *testing.T) {
	srv := h2cServer(t)
	resp, err := http.Post(srv.URL+"/course.Echo/Echo", "application/grpc", bytes.NewReader(Frame(EncodeString("x"))))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusHTTPVersionNotSupported || !strings.Contains(string(b), "gRPC requires HTTP/2") {
		t.Fatal(resp.Status, string(b))
	}
}

func TestWebSocketEcho(t *testing.T) {
	srv := h2cServer(t)
	var out bytes.Buffer
	err := WSClient{URL: strings.Replace(srv.URL, "http", "ws", 1) + "/ws", Send: []string{"hello"}, Hold: 300 * time.Millisecond, Ping: 100 * time.Millisecond, Out: &out}.Run()
	if err != nil {
		t.Fatal(err, out.String())
	}
	for _, want := range []string{"101 Switching Protocols", "accept checks out", `< text "hello"`, "pong", "still open", "close 1000"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestEventsResume(t *testing.T) {
	srv := h2cServer(t)
	var out bytes.Buffer
	if err := WatchEvents(context.Background(), srv.URL+"/events?n=2&every=10ms", "7", "", &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "id 8") || !strings.Contains(out.String(), "id 9") || strings.Contains(out.String(), "id 1 ") {
		t.Fatal(out.String())
	}
}

func TestFetchCountsConnections(t *testing.T) {
	srv := h2cServer(t)
	for proto, conns := range map[string]int{"h2c": 1, "h1": 3} {
		var out bytes.Buffer
		res, err := Fetch{URLs: []string{srv.URL + "/obj?size=10&delay=50ms"}, N: 6, Proto: proto, Conns: 3, Page: true, Out: &out}.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, r := range res {
			seen[r.Conn] = true
		}
		if len(seen) != conns {
			t.Errorf("%s: %d connections, want %d\n%s", proto, len(seen), conns, out.String())
		}
	}
}
