package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// logger writes timestamped lines; the lab's checks read the service logs.
type logger struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *logger) Printf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.w, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// WritePKI creates a CA and a leaf for names, writes ca.pem, cert.pem
// (leaf + CA) and key.pem to dir, and returns the leaf for a TLS server.
// The HTTP/3 server (h3lab.py) loads the same files, so both serve one
// certificate.
func WritePKI(dir string, names []string) (tls.Certificate, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tls.Certificate{}, err
	}
	sn := func() *big.Int { n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120)); return n }
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{
		SerialNumber: sn(), Subject: pkix.Name{CommonName: "weblab CA", Organization: []string{"Networking Course Lab"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: sn(), Subject: pkix.Name{CommonName: names[0]},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(30 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			leafTmpl.IPAddresses = append(leafTmpl.IPAddresses, ip)
		} else {
			leafTmpl.DNSNames = append(leafTmpl.DNSNames, n)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...)
	files := map[string][]byte{
		"ca.pem":   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"cert.pem": certPEM,
		"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
	for name, data := range files {
		mode := os.FileMode(0o644)
		if name == "key.pem" {
			mode = 0o600
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, mode); err != nil {
			return tls.Certificate{}, err
		}
	}
	return tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: key}, nil
}

// Handler is everything the lab server answers:
//
//	/obj?size=N&delay=D    N bytes after D (default 1024 bytes, no delay)
//	/events?n=N&every=D    server-sent events; resumes after Last-Event-ID
//	/ws                    WebSocket echo (HTTP/1.1 Upgrade)
//	/course.Echo/...       the gRPC service in labs/grpc/echo.proto
//	/                      which protocol and connection the request used
//
// ServerID, when set (serve -id), names this server in "/" responses so
// load balancing across replicas can be counted.
var ServerID string

func Handler(log *logger, altSvc string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/obj", func(w http.ResponseWriter, r *http.Request) {
		size, _ := strconv.Atoi(r.URL.Query().Get("size"))
		if r.URL.Query().Get("size") == "" {
			size = 1024
		}
		if d, err := time.ParseDuration(r.URL.Query().Get("delay")); err == nil && d > 0 {
			select {
			case <-time.After(d):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(size))
		buf := make([]byte, 32*1024)
		for i := range buf {
			buf[i] = 'a' + byte(i%26)
		}
		for left := size; left > 0; {
			n := min(left, len(buf))
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			left -= n
		}
	})
	mux.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) { serveEvents(w, r, log) })
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) { serveWebSocket(w, r, log) })
	mux.HandleFunc("/course.Echo/", func(w http.ResponseWriter, r *http.Request) { serveGRPC(w, r, log) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		alpn := "-"
		if r.TLS != nil {
			alpn = r.TLS.NegotiatedProtocol
		}
		if ServerID != "" {
			fmt.Fprintf(w, "proto %s alpn %s from %s served-by %s\n", r.Proto, alpn, r.RemoteAddr, ServerID)
			return
		}
		fmt.Fprintf(w, "proto %s alpn %s from %s\n", r.Proto, alpn, r.RemoteAddr)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s %s %s", r.Proto, r.RemoteAddr, r.Method, r.URL.RequestURI())
		if altSvc != "" {
			w.Header().Set("Alt-Svc", altSvc)
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") && !strings.HasPrefix(r.URL.Path, "/course.Echo/") {
			// A gRPC server answers unknown services in gRPC, not with an HTTP 404.
			svc := strings.TrimPrefix(r.URL.Path, "/")
			if i := strings.LastIndex(svc, "/"); i >= 0 {
				svc = svc[:i]
			}
			trailersOnly(w, 12, "unknown service "+svc)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// serveEvents streams text/event-stream: one event every interval, ids
// counting from 1, or from Last-Event-ID+1 when a client reconnects.
func serveEvents(w http.ResponseWriter, r *http.Request, log *logger) {
	q := r.URL.Query()
	n, err := strconv.Atoi(q.Get("n"))
	if err != nil || n <= 0 {
		n = 5
	}
	every, err := time.ParseDuration(q.Get("every"))
	if err != nil || every <= 0 {
		every = 500 * time.Millisecond
	}
	first := 1
	if last, err := strconv.Atoi(r.Header.Get("Last-Event-ID")); err == nil {
		first = last + 1
		log.Printf("events: resuming after Last-Event-ID %d", last)
	}
	f, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "retry: 1000\n\n")
	for id := first; id < first+n; id++ {
		if id > first {
			select {
			case <-time.After(every):
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprintf(w, "id: %d\nevent: tick\ndata: {\"n\":%d,\"sent\":\"%s\"}\n\n", id, id, time.Now().Format("15:04:05.000"))
		if f != nil {
			f.Flush()
		}
	}
}

// Serve runs the TLS listener (HTTP/1.1 and HTTP/2 by ALPN) and the
// cleartext listener (HTTP/1.1, and HTTP/2 with prior knowledge — "h2c").
func Serve(tlsAddr, plainAddr string, cert tls.Certificate, h http.Handler, log *logger, keylog io.Writer) error {
	errs := make(chan error, 2)
	connLog := func(c net.Conn, s http.ConnState) {
		if s == http.StateNew {
			log.Printf("conn new %s -> %s", c.RemoteAddr(), c.LocalAddr())
		}
	}
	if tlsAddr != "" {
		var p http.Protocols
		p.SetHTTP1(true)
		p.SetHTTP2(true)
		srv := &http.Server{Addr: tlsAddr, Handler: h, Protocols: &p, ConnState: connLog, ReadHeaderTimeout: 10 * time.Second,
			TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, KeyLogWriter: keylog}}
		go func() { errs <- srv.ListenAndServeTLS("", "") }()
	}
	if plainAddr != "" {
		var p http.Protocols
		p.SetHTTP1(true)
		p.SetUnencryptedHTTP2(true)
		srv := &http.Server{Addr: plainAddr, Handler: h, Protocols: &p, ConnState: connLog, ReadHeaderTimeout: 10 * time.Second}
		go func() { errs <- srv.ListenAndServe() }()
	}
	log.Printf("weblab: https on %s (h2, http/1.1), http on %s (http/1.1, h2c)", orDash(tlsAddr), orDash(plainAddr))
	return <-errs
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// splitList splits a comma-separated flag value.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
