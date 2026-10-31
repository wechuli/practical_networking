package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// Proxy is a reverse proxy whose backend protocol can be chosen, to
// reproduce what load balancers do to gRPC and streaming responses:
//
//	backend h1   forward over HTTP/1.1 (an ALB target group with protocol version HTTP1)
//	backend h2c  forward over cleartext HTTP/2
//	buffer       read each whole response before sending it (a buffering proxy)
//
// It listens for HTTP/1.1 and h2c, so gRPC clients can reach it in cleartext.
func Proxy(listen, backend, backendProto string, buffer bool, log *logger) error {
	target, err := url.Parse(backend)
	if err != nil {
		return err
	}
	var bp http.Protocols
	switch backendProto {
	case "h2c":
		bp.SetUnencryptedHTTP2(true)
	default:
		bp.SetHTTP1(true)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{Proxy: nil, Protocols: &bp, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
	if buffer {
		rp.ModifyResponse = func(resp *http.Response) error {
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(b))
			resp.ContentLength = int64(len(b))
			resp.Header.Del("Content-Length")
			log.Printf("proxy: buffered %d bytes of %s before sending", len(b), resp.Request.URL.Path)
			return err
		}
	}
	var lp http.Protocols
	lp.SetHTTP1(true)
	lp.SetUnencryptedHTTP2(true)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("proxy: %s %s %s -> backend over %s", r.Proto, r.Method, r.URL.Path, backendProto)
		rp.ServeHTTP(w, r)
	})
	srv := &http.Server{Addr: listen, Handler: h, Protocols: &lp, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("proxy: %s -> %s (backend %s, buffer %v)", listen, backend, backendProto, buffer)
	return srv.ListenAndServe()
}

// Relay is a TCP relay that closes connections idle for longer than idle in
// both directions — what a load balancer's idle timeout does to a quiet
// WebSocket or long poll.
func Relay(listen, to string, idle time.Duration, log *logger) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("relay: %s -> %s, idle timeout %s", listen, to, idle)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func(c net.Conn) {
			defer c.Close()
			b, err := net.DialTimeout("tcp", to, 5*time.Second)
			if err != nil {
				log.Printf("relay: %v", err)
				return
			}
			defer b.Close()
			var mu sync.Mutex
			last := time.Now()
			touch := func() { mu.Lock(); last = time.Now(); mu.Unlock() }
			done := make(chan struct{}, 2)
			pipe := func(dst, src net.Conn) {
				buf := make([]byte, 32*1024)
				for {
					n, err := src.Read(buf)
					if n > 0 {
						touch()
						if _, werr := dst.Write(buf[:n]); werr != nil {
							break
						}
					}
					if err != nil {
						break
					}
				}
				done <- struct{}{}
			}
			go pipe(b, c)
			go pipe(c, b)
			start := time.Now()
			tick := time.NewTicker(100 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					mu.Lock()
					quiet := time.Since(last)
					mu.Unlock()
					if quiet >= idle {
						log.Printf("relay: %s idle for %s; closing both sides after %s", c.RemoteAddr(), idle, time.Since(start).Round(100*time.Millisecond))
						return
					}
				}
			}
		}(c)
	}
}
