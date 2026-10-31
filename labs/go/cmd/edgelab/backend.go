package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// BackendOptions configures one backend behind a load balancer.
type BackendOptions struct {
	Addr          string
	ID            string
	ProxyProtocol bool          // expect a PROXY protocol v1 header on every connection
	Idle          time.Duration // http.Server IdleTimeout (keep-alive)
	HealthCode    int
}

// proxyConn carries the client address from a PROXY header.
type proxyConn struct {
	net.Conn
	r      *bufio.Reader
	client string
}

func (c *proxyConn) Read(p []byte) (int, error) { return c.r.Read(p) }

type proxyListener struct {
	net.Listener
	log func(string, ...any)
}

// Accept reads "PROXY TCP4 src dst sport dport\r\n" before handing the
// connection to the HTTP server; a connection without it is closed.
func (l proxyListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(c)
		line, err := r.ReadString('\n')
		_ = c.SetReadDeadline(time.Time{})
		f := strings.Fields(line)
		if err != nil || len(f) != 6 || f[0] != "PROXY" {
			l.log("PROXY protocol: %s sent %q instead of a PROXY header — closing", c.RemoteAddr(), strings.TrimSpace(firstN(line, 60)))
			c.Close()
			continue
		}
		return &proxyConn{Conn: c, r: r, client: net.JoinHostPort(f[2], f[4])}, nil
	}
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

type ctxKey struct{}

// BackendHandler answers "/" with what the backend can see about the
// request, and /health with the configured status; /admin/health?code=N
// changes it.
func BackendHandler(o BackendOptions, served *atomic.Int64) http.Handler {
	var mu sync.Mutex
	code := o.HealthCode
	if code == 0 {
		code = 200
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := code
		mu.Unlock()
		w.WriteHeader(c)
		fmt.Fprintf(w, "%s health %d\n", o.ID, c)
	})
	mux.HandleFunc("/admin/health", func(w http.ResponseWriter, r *http.Request) {
		if c, err := strconv.Atoi(r.URL.Query().Get("code")); err == nil {
			mu.Lock()
			code = c
			mu.Unlock()
		}
		fmt.Fprintf(w, "%s health now %d\n", o.ID, code)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		proxied := "-"
		if v, ok := r.Context().Value(ctxKey{}).(string); ok {
			proxied = v
		}
		xff := strings.Join(r.Header.Values("X-Forwarded-For"), " | ")
		if xff == "" {
			xff = "-"
		}
		mu.Lock()
		c := code
		mu.Unlock()
		if r.URL.Query().Get("app") == "health" && c != 200 {
			// the application is as broken as its health endpoint says
			w.WriteHeader(http.StatusInternalServerError)
		}
		fmt.Fprintf(w, "backend=%s peer=%s xff=%s proxy-protocol-client=%s cookie=%s\n", o.ID, r.RemoteAddr, xff, proxied, orDash(r.Header.Get("Cookie")))
	})
	return mux
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Backend serves until ctx ends.
func Backend(ctx context.Context, o BackendOptions, w io.Writer) error {
	ln, err := net.Listen("tcp4", o.Addr)
	if err != nil {
		return err
	}
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
	}
	if o.ProxyProtocol {
		ln = proxyListener{Listener: ln, log: logf}
	}
	var served atomic.Int64
	srv := &http.Server{Handler: BackendHandler(o, &served), IdleTimeout: o.Idle, ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			if pc, ok := c.(*proxyConn); ok {
				return context.WithValue(ctx, ctxKey{}, pc.client)
			}
			return ctx
		},
		ConnState: func(c net.Conn, s http.ConnState) {
			if s == http.StateNew {
				if pc, ok := c.(*proxyConn); ok {
					logf("connection from %s (PROXY header: client %s)", c.RemoteAddr(), pc.client)
				} else {
					logf("connection from %s", c.RemoteAddr())
				}
			}
		},
		ErrorLog: nil,
	}
	go func() { <-ctx.Done(); srv.Close() }()
	logf("edgelab backend %s on %s (PROXY protocol %v, idle timeout %s)", o.ID, ln.Addr(), o.ProxyProtocol, o.Idle)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logf("served %d requests", served.Load())
	return nil
}
