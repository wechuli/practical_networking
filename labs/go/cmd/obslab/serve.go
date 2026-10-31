package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// ServeOptions configures one service of the lab application.
type ServeOptions struct {
	Name     string
	Addr     string
	Backend  string        // frontend: base URL of the backend
	Timeout  time.Duration // frontend: per-call timeout to the backend
	Spans    string
	Log      string
	SlowFrac float64
	Slow     time.Duration
	FailFrac float64
}

type faults struct {
	mu       sync.Mutex
	slowFrac float64
	slow     time.Duration
	failFrac float64
	rng      *rand.Rand
}

func (f *faults) roll() (slow time.Duration, fail bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rng.Float64() < f.slowFrac {
		slow = f.slow
	}
	return slow, f.rng.Float64() < f.failFrac
}

func (f *faults) String() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fmt.Sprintf("slow %.0f%% of requests by %s, fail %.0f%% with 503", f.slowFrac*100, f.slow, f.failFrac*100)
}

// Handler builds the service: "/" does the work, /metrics is Prometheus
// text, /admin/faults?slow=0.1&delay=300ms&fail=0.02 changes the faults.
func Handler(o ServeOptions) (http.Handler, error) {
	spans, err := OpenSpans(o.Spans)
	if err != nil {
		return nil, err
	}
	var logw io.Writer = io.Discard
	if o.Log != "" {
		f, err := os.OpenFile(o.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		logw = f
	}
	var logMu sync.Mutex
	logLine := func(v map[string]any) {
		b, _ := json.Marshal(v)
		logMu.Lock()
		defer logMu.Unlock()
		_, _ = logw.Write(append(b, '\n'))
	}
	m := NewMetrics(o.Name, DefaultBuckets)
	fl := &faults{slowFrac: o.SlowFrac, slow: o.Slow, failFrac: o.FailFrac, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
	timeout := o.Timeout
	if timeout == 0 {
		timeout = time.Second
	}
	client := &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
		DialContext: (&net.Dialer{Timeout: timeout}).DialContext}}

	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.Expose(w)
	})
	mux.HandleFunc("/admin/faults", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		fl.mu.Lock()
		if v, err := strconv.ParseFloat(q.Get("slow"), 64); err == nil {
			fl.slowFrac = v
		}
		if v, err := time.ParseDuration(q.Get("delay")); err == nil {
			fl.slow = v
		}
		if v, err := strconv.ParseFloat(q.Get("fail"), 64); err == nil {
			fl.failFrac = v
		}
		fl.mu.Unlock()
		fmt.Fprintf(w, "%s faults: %s\n", o.Name, fl)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		parent, err := ParseTraceparent(r.Header.Get("traceparent"))
		ctx := NewTrace(true)
		server := Span{Service: o.Name, Name: "GET " + r.URL.Path, Kind: "server"}
		if err == nil {
			ctx = parent.Child()
			server.ParentID = parent.SpanID
		}
		server.TraceID, server.SpanID = ctx.TraceID, ctx.SpanID
		code := http.StatusOK
		var problem string
		if o.Backend != "" {
			cctx := ctx.Child()
			cspan := Span{TraceID: cctx.TraceID, SpanID: cctx.SpanID, ParentID: ctx.SpanID, Service: o.Name, Name: "call backend", Kind: "client"}
			cstart := time.Now()
			req, _ := http.NewRequestWithContext(r.Context(), "GET", o.Backend+r.URL.Path, nil)
			req.Header.Set("traceparent", cctx.Header())
			resp, err := client.Do(req)
			if err != nil {
				var ne net.Error
				if errors.As(err, &ne) && ne.Timeout() || errors.Is(err, context.DeadlineExceeded) {
					code = http.StatusGatewayTimeout
				} else {
					code = http.StatusBadGateway
				}
				problem = err.Error()
				cspan.Status, cspan.Error = "error", problem
			} else {
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				code = resp.StatusCode
				cspan.HTTPStatus = code
				if code >= 500 {
					cspan.Status = "error"
				}
			}
			if ctx.Sampled {
				spans.End(cspan, cstart)
			}
		} else {
			slow, fail := fl.roll()
			if slow > 0 {
				time.Sleep(slow)
			}
			if fail {
				code = http.StatusServiceUnavailable
				problem = "injected failure"
			}
		}
		w.Header().Set("traceparent", ctx.Header())
		w.WriteHeader(code)
		fmt.Fprintf(w, "%s %d trace %s\n", o.Name, code, ctx.TraceID)
		d := time.Since(start)
		m.Observe(code, d.Seconds())
		server.HTTPStatus = code
		if code >= 500 {
			server.Status, server.Error = "error", problem
		}
		if ctx.Sampled {
			spans.End(server, start)
		}
		entry := map[string]any{"time": start.UTC().Format(time.RFC3339Nano), "service": o.Name, "trace_id": ctx.TraceID, "path": r.URL.Path,
			"status": code, "duration_ms": float64(d.Microseconds()) / 1000}
		if problem != "" {
			entry["error"] = problem
		}
		logLine(entry)
	})
	return mux, nil
}

// Serve runs the service until ctx ends.
func Serve(ctx context.Context, o ServeOptions, w io.Writer) error {
	h, err := Handler(o)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp4", o.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	fmt.Fprintf(w, "obslab %s on %s (backend %q)\n", o.Name, ln.Addr(), o.Backend)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
