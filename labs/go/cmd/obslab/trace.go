package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// TraceContext is the W3C Trace Context carried in the traceparent header:
// version 00, a 16-byte trace ID, the 8-byte ID of the caller's span and
// flags (bit 0 = sampled).
type TraceContext struct {
	TraceID string
	SpanID  string
	Sampled bool
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// NewTrace starts a trace with a fresh root span ID.
func NewTrace(sampled bool) TraceContext {
	return TraceContext{TraceID: randomHex(16), SpanID: randomHex(8), Sampled: sampled}
}

// Child returns the context for a new span in the same trace.
func (t TraceContext) Child() TraceContext {
	return TraceContext{TraceID: t.TraceID, SpanID: randomHex(8), Sampled: t.Sampled}
}

// Header renders traceparent.
func (t TraceContext) Header() string {
	flags := "00"
	if t.Sampled {
		flags = "01"
	}
	return "00-" + t.TraceID + "-" + t.SpanID + "-" + flags
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s != strings.Repeat("0", n)
}

// ParseTraceparent validates a traceparent header (version 00 only).
func ParseTraceparent(h string) (TraceContext, error) {
	p := strings.Split(strings.TrimSpace(h), "-")
	if len(p) != 4 || p[0] != "00" || !isHex(p[1], 32) || !isHex(p[2], 16) || len(p[3]) != 2 {
		return TraceContext{}, fmt.Errorf("invalid traceparent %q", h)
	}
	flags, err := hex.DecodeString(p[3])
	if err != nil {
		return TraceContext{}, fmt.Errorf("invalid traceparent flags %q", p[3])
	}
	return TraceContext{TraceID: p[1], SpanID: p[2], Sampled: flags[0]&1 == 1}, nil
}

// Span is one timed operation, in the shape most tracing backends store.
type Span struct {
	TraceID    string  `json:"trace_id"`
	SpanID     string  `json:"span_id"`
	ParentID   string  `json:"parent_span_id,omitempty"`
	Service    string  `json:"service"`
	Name       string  `json:"name"`
	Kind       string  `json:"kind"` // server | client
	Start      int64   `json:"start_unix_nano"`
	DurationMS float64 `json:"duration_ms"`
	Status     string  `json:"status"` // ok | error
	HTTPStatus int     `json:"http.status_code,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// SpanWriter appends spans as JSON lines; a nil writer drops them.
type SpanWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// OpenSpans opens path for appending ("" = no export).
func OpenSpans(path string) (*SpanWriter, error) {
	if path == "" {
		return &SpanWriter{}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &SpanWriter{w: f}, nil
}

// End records a span that started at start.
func (s *SpanWriter) End(sp Span, start time.Time) {
	if s == nil || s.w == nil {
		return
	}
	sp.Start = start.UnixNano()
	sp.DurationMS = float64(time.Since(start).Microseconds()) / 1000
	if sp.Status == "" {
		sp.Status = "ok"
	}
	b, _ := json.Marshal(sp)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.w.Write(append(b, '\n'))
}
