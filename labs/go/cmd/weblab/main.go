// Command weblab is the course's web-protocol lab: a server and clients for
// HTTP/1.1 and HTTP/2, gRPC built by hand on HTTP/2, WebSockets, and
// server-sent events, plus the proxies that break them.
//
//	weblab serve [-tls :443] [-plain :80] [-name host,...] [-pki DIR] [-alt-svc V] [-keylog F] [-id NAME]
//	weblab get   [-proto h1|h2|h2c] [-conns 6] [-n 1] [-page] [-stagger D] [-cacert F] [-keylog F] URL...
//	weblab grpc  [-tls] [-cacert F] [-timeout D] HOST:PORT course.Echo/Echo VALUE
//	weblab ws    [-cacert F] [-hold D] [-ping D] URL [MESSAGE...]
//	weblab sse   [-cacert F] [-last-id N] URL
//	weblab proxy -listen ADDR -to URL [-backend h1|h2c] [-buffer]
//	weblab relay -listen ADDR -to HOST:PORT -idle D
//	weblab accept KEY       # Sec-WebSocket-Accept for a Sec-WebSocket-Key
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	log := &logger{w: os.Stdout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[2:]
	switch os.Args[1] {
	case "serve":
		stop() // servers exit on SIGTERM/SIGINT as usual
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		tlsAddr := fs.String("tls", ":443", "HTTPS listen address (HTTP/2 and HTTP/1.1 by ALPN); empty to disable")
		plain := fs.String("plain", ":80", "cleartext listen address (HTTP/1.1 and h2c); empty to disable")
		names := fs.String("name", "localhost,127.0.0.1", "comma-separated names for the certificate")
		pki := fs.String("pki", "", "directory for ca.pem, cert.pem and key.pem (default: a new temporary directory)")
		alt := fs.String("alt-svc", "", `Alt-Svc header to send, e.g. h3=":443"; ma=3600`)
		keylog := fs.String("keylog", "", "append TLS secrets to this file (NSS key log format)")
		fs.StringVar(&ServerID, "id", "", "name this server in / responses (served-by ID)")
		_ = fs.Parse(args)
		if *pki == "" {
			dir, err := os.MkdirTemp("", "weblab-pki-")
			if err != nil {
				fail(err)
			}
			*pki = dir
			log.Printf("weblab: certificates in %s", dir)
		}
		cert, err := WritePKI(*pki, splitList(*names))
		if err != nil {
			fail(err)
		}
		var kl io.Writer
		if *keylog != "" {
			f, err := os.OpenFile(*keylog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				fail(err)
			}
			kl = f
		}
		fail(Serve(*tlsAddr, *plain, cert, Handler(log, *alt), log, kl))
	case "get":
		fs := flag.NewFlagSet("get", flag.ExitOnError)
		f := Fetch{Out: os.Stdout}
		fs.StringVar(&f.Proto, "proto", "h2", "h1, h2 (TLS, ALPN) or h2c (cleartext HTTP/2, prior knowledge)")
		fs.IntVar(&f.Conns, "conns", 6, "HTTP/1.1 connections per host")
		fs.IntVar(&f.N, "n", 1, "request each URL this many times, all at once")
		fs.StringVar(&f.CAFile, "cacert", "", "CA certificate to trust")
		fs.DurationVar(&f.Stagger, "stagger", 0, "start each request this long after the previous one (keeps their order)")
		fs.BoolVar(&f.Page, "page", false, "fetch the first URL alone, then the rest at once (a page, then its resources)")
		keylog := fs.String("keylog", "", "append TLS secrets to this file")
		_ = fs.Parse(args)
		f.URLs = fs.Args()
		if len(f.URLs) == 0 {
			usage()
		}
		if *keylog != "" {
			kf, err := os.OpenFile(*keylog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				fail(err)
			}
			defer kf.Close()
			f.KeyLog = kf
		}
		if _, err := f.Run(ctx); err != nil {
			fail(err)
		}
	case "grpc":
		fs := flag.NewFlagSet("grpc", flag.ExitOnError)
		c := GRPCCall{Out: os.Stdout}
		fs.BoolVar(&c.TLS, "tls", false, "use TLS (otherwise cleartext h2c)")
		fs.StringVar(&c.CAFile, "cacert", "", "CA certificate to trust")
		fs.DurationVar(&c.Timeout, "timeout", 0, "deadline, sent as grpc-timeout")
		_ = fs.Parse(args)
		if fs.NArg() != 3 {
			usage()
		}
		c.Target, c.Method, c.Value = fs.Arg(0), fs.Arg(1), fs.Arg(2)
		code, err := c.Run(ctx)
		if err != nil {
			fail(err)
		}
		os.Exit(min(code, 1))
	case "ws":
		fs := flag.NewFlagSet("ws", flag.ExitOnError)
		c := WSClient{Out: os.Stdout}
		fs.StringVar(&c.CAFile, "cacert", "", "CA certificate to trust (wss://)")
		fs.DurationVar(&c.Hold, "hold", 0, "keep the connection open this long after the messages")
		fs.DurationVar(&c.Ping, "ping", 0, "while holding, send a ping this often")
		_ = fs.Parse(args)
		if fs.NArg() < 1 {
			usage()
		}
		c.URL, c.Send = fs.Arg(0), fs.Args()[1:]
		if err := c.Run(); err != nil {
			fail(err)
		}
	case "sse":
		fs := flag.NewFlagSet("sse", flag.ExitOnError)
		ca := fs.String("cacert", "", "CA certificate to trust")
		last := fs.String("last-id", "", "send Last-Event-ID, as a reconnecting EventSource does")
		_ = fs.Parse(args)
		if fs.NArg() != 1 {
			usage()
		}
		if err := WatchEvents(ctx, fs.Arg(0), *last, *ca, os.Stdout); err != nil {
			fail(err)
		}
	case "proxy":
		stop() // servers exit on SIGTERM/SIGINT as usual
		fs := flag.NewFlagSet("proxy", flag.ExitOnError)
		listen := fs.String("listen", ":8080", "listen address (HTTP/1.1 and h2c)")
		to := fs.String("to", "", "backend URL, e.g. http://127.0.0.1:80")
		backend := fs.String("backend", "h1", "protocol to the backend: h1 or h2c")
		buffer := fs.Bool("buffer", false, "buffer whole responses before forwarding")
		_ = fs.Parse(args)
		fail(Proxy(*listen, *to, *backend, *buffer, log))
	case "relay":
		stop() // servers exit on SIGTERM/SIGINT as usual
		fs := flag.NewFlagSet("relay", flag.ExitOnError)
		listen := fs.String("listen", ":8081", "listen address")
		to := fs.String("to", "", "backend host:port")
		idle := fs.Duration("idle", 5*time.Second, "close connections idle this long")
		_ = fs.Parse(args)
		fail(Relay(*listen, *to, *idle, log))
	case "accept":
		if len(args) != 1 {
			usage()
		}
		fmt.Println(Accept(args[0]))
	default:
		usage()
	}
}

func fail(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "weblab: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  weblab serve [-tls :443] [-plain :80] [-name host,...] [-pki DIR] [-alt-svc V] [-keylog F] [-id NAME]
  weblab get   [-proto h1|h2|h2c] [-conns 6] [-n 1] [-page] [-stagger D] [-cacert F] [-keylog F] URL...
  weblab grpc  [-tls] [-cacert F] [-timeout D] HOST:PORT course.Echo/Echo VALUE
  weblab ws    [-cacert F] [-hold D] [-ping D] URL [MESSAGE...]
  weblab sse   [-cacert F] [-last-id N] URL
  weblab proxy -listen ADDR -to URL [-backend h1|h2c] [-buffer]
  weblab relay -listen ADDR -to HOST:PORT -idle D
  weblab accept KEY
`)
	os.Exit(2)
}
