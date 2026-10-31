// Command labserver is a local HTTPS (and HTTP) server for the course labs.
// It creates a throwaway certificate authority and server certificate in
// memory, writes the CA certificate to a file for clients to trust, and serves
// a page whose size and response delay you choose, so labs can reproduce
// slow servers and large transfers without depending on any internet site.
//
//	labserver -name example.test -ca-out /tmp/labserver-ca.pem
//	curl --cacert /tmp/labserver-ca.pem --resolve example.test:8443:127.0.0.1 https://example.test:8443/
//
// The private keys never leave the process. Use it on loopback or in a lab
// namespace only; it is not a production server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var cfg Config
	flag.StringVar(&cfg.Addr, "addr", "127.0.0.1:8443", "HTTPS listen address")
	flag.StringVar(&cfg.PlainAddr, "http-addr", "", "optional plain HTTP listen address, e.g. 127.0.0.1:8080")
	flag.StringVar(&cfg.Name, "name", "example.test", "DNS name in the server certificate (127.0.0.1 and ::1 are always included)")
	flag.StringVar(&cfg.CAOut, "ca-out", "labserver-ca.pem", "where to write the CA certificate clients should trust")
	flag.IntVar(&cfg.BodyBytes, "size", 1024, "response body size in bytes")
	flag.DurationVar(&cfg.Delay, "delay", 0, "wait this long before responding (simulates a slow server)")
	flag.StringVar(&cfg.ID, "id", "", "value of the X-Served-By response header (default labserver), to tell instances apart")
	flag.Parse()

	srv, err := New(cfg)
	if err != nil {
		log.Fatalf("labserver: %v", err)
	}
	if err := os.WriteFile(cfg.CAOut, srv.CAPEM, 0o644); err != nil {
		log.Fatalf("labserver: writing CA: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errs := make(chan error, 2)
	tlsLn, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Fatalf("labserver: %v", err)
	}
	go func() { errs <- srv.HTTPS.ServeTLS(tlsLn, "", "") }()
	log.Printf("serving https://%s:%s/ on %s (CA written to %s; Ctrl-C to stop)",
		cfg.Name, port(tlsLn.Addr()), tlsLn.Addr(), cfg.CAOut)
	if cfg.PlainAddr != "" {
		ln, err := net.Listen("tcp", cfg.PlainAddr)
		if err != nil {
			log.Fatalf("labserver: %v", err)
		}
		go func() { errs <- srv.HTTP.Serve(ln) }()
		log.Printf("serving http://%s/", ln.Addr())
	}

	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("labserver: %v", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.HTTPS.Shutdown(shutdown)
	_ = srv.HTTP.Shutdown(shutdown)
	fmt.Fprintln(os.Stderr, "labserver: stopped")
}

func port(a net.Addr) string {
	_, p, _ := net.SplitHostPort(a.String())
	return p
}
