package main

import (
	"context"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestTraceparent(t *testing.T) {
	tc, err := ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if err != nil || !tc.Sampled || tc.SpanID != "00f067aa0ba902b7" {
		t.Fatalf("%+v %v", tc, err)
	}
	for _, bad := range []string{"", "01-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01", "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-xyz-00f067aa0ba902b7-01"} {
		if _, err := ParseTraceparent(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	c := tc.Child()
	if c.TraceID != tc.TraceID || c.SpanID == tc.SpanID || !strings.HasSuffix(c.Header(), "-01") {
		t.Fatalf("child %+v", c)
	}
}

func TestQuantileInterpolates(t *testing.T) {
	m := NewMetrics("x", []float64{0.1, 0.5})
	for i := 0; i < 90; i++ {
		m.Observe(200, 0.05)
	}
	for i := 0; i < 10; i++ {
		m.Observe(200, 0.3)
	}
	var b strings.Builder
	m.Expose(&b)
	s, err := Parse(strings.NewReader(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	// rank 99 lies 9/10 of the way through the (0.1, 0.5] bucket: 0.1 + 0.4×0.9 = 0.46
	if q := s.Quantile(0.99); math.Abs(q-0.46) > 1e-9 {
		t.Fatalf("p99 = %v", q)
	}
	prev := s
	m.Observe(503, 0.05)
	b.Reset()
	m.Expose(&b)
	s2, _ := Parse(strings.NewReader(b.String()))
	d := s2.Sub(prev)
	if d.Count != 1 || d.Codes["503"] != 1 || d.Codes["200"] != 0 {
		t.Fatalf("delta %+v", d)
	}
}

func TestParseFlows(t *testing.T) {
	in := "ipv4     2 tcp      6 118 SYN_SENT src=10.0.0.1 dst=10.0.0.2 sport=40000 dport=8081 packets=1 bytes=60 [UNREPLIED] src=10.0.0.2 dst=10.0.0.1 sport=8081 dport=40000 packets=0 bytes=0 mark=0 use=1\n"
	fl := ParseFlows(strings.NewReader(in))
	if len(fl) != 1 || !fl[0].Unreplied || fl[0].State != "SYN_SENT" || fl[0].Packets != "1" || fl[0].ReplyPackets != "0" || fl[0].Dport != "8081" {
		t.Fatalf("%+v", fl)
	}
	if !strings.Contains(FormatFlows(fl), "NO REPLY") {
		t.Fatal(FormatFlows(fl))
	}
}

func TestPropagationAcrossServices(t *testing.T) {
	dir := t.TempDir()
	be, err := Handler(ServeOptions{Name: "backend", Spans: filepath.Join(dir, "b.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	bsrv := httptest.NewServer(be)
	defer bsrv.Close()
	fe, err := Handler(ServeOptions{Name: "frontend", Backend: bsrv.URL, Spans: filepath.Join(dir, "f.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	fsrv := httptest.NewServer(fe)
	defer fsrv.Close()
	out, err := Load(context.Background(), LoadOptions{URL: fsrv.URL + "/", N: 10, C: 2, Sample: 1, Spans: filepath.Join(dir, "l.jsonl"), Timeout: 5e9})
	if err != nil || !strings.Contains(out, "10 × 200") {
		t.Fatal(out, err)
	}
	spans, err := ReadSpans([]string{filepath.Join(dir, "l.jsonl"), filepath.Join(dir, "f.jsonl"), filepath.Join(dir, "b.jsonl")})
	if err != nil {
		t.Fatal(err)
	}
	traces, orphans := Group(spans)
	if len(spans) != 40 || len(traces) != 10 || orphans != 0 {
		t.Fatalf("%d spans, %d traces, %d orphans", len(spans), len(traces), orphans)
	}
	if !strings.Contains(Breakdown(traces[0]), "| backend ") {
		t.Fatal(Breakdown(traces[0]))
	}
}
