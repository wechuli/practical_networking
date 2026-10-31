//go:build linux

// Command socklab shows how applications use sockets and how that goes wrong:
// stream framing, thread-per-connection vs an event loop, descriptor limits,
// port exhaustion, address reuse, connection reuse in HTTP clients, and
// backpressure in a proxy.
//
//	socklab serve -addr :7000 [-mode echo|discard|hold|reads|close] [-model netpoll|threads] [-reuseport] [-id NAME] [-for D]
//	socklab writes -to HOST:PORT [-n 100] [-size 10] [-nodelay=true]     # many small writes on one connection
//	socklab hold -to HOST:PORT [-n N] [-hold D]                           # open connections one by one and keep them
//	socklab churn -to HOST:PORT [-n N] [-server-closes]                   # connect and close, as fast as possible
//	socklab greet -to HOST:PORT [-n 300]                                  # which server answered (serve -mode greet)
//	socklab bind -addr HOST:PORT [-reuseaddr] [-reuseport]               # bind and listen with chosen options
//	socklab http -url URL [-n 200] [-c 20] [-keepalive] [-idle-per-host 2] [-drain] [-close-body]
//	socklab relay -listen ADDR -to ADDR [-mode copy|unbounded]           # a proxy, with or without backpressure
//	socklab udp-sink -addr ADDR [-per 1ms] [-rcvbuf B]                    # a slow UDP consumer
//	socklab udp-blast -to HOST:PORT [-n 10000] [-size 200]                # as fast as possible
//	socklab stat -pid PID                                                 # threads, RSS, descriptors, epoll registrations
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "socklab: %v\n", err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		var o ServeOptions
		fs.StringVar(&o.Addr, "addr", ":7000", "listen address")
		fs.StringVar(&o.Mode, "mode", "echo", "echo | discard | hold | reads | close")
		fs.StringVar(&o.Model, "model", "netpoll", "netpoll (Go's epoll-based runtime) | threads (one blocked OS thread per connection)")
		fs.BoolVar(&o.ReusePort, "reuseport", false, "set SO_REUSEPORT so several servers can share the port")
		fs.StringVar(&o.ID, "id", "", "name printed in each greeting (reuseport experiments)")
		fs.DurationVar(&o.For, "for", 0, "exit after this long (0 = until interrupted)")
		pidfile := fs.String("pidfile", "", "write this process's PID here")
		_ = fs.Parse(os.Args[2:])
		writePid(*pidfile)
		if o.For > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, o.For)
			defer cancel()
		}
		if err := Serve(ctx, o, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case "writes":
		to := fs.String("to", "", "host:port")
		n := fs.Int("n", 100, "number of writes")
		size := fs.Int("size", 10, "bytes per write")
		nodelay := fs.Bool("nodelay", true, "TCP_NODELAY (Go's default); false enables Nagle's algorithm")
		_ = fs.Parse(os.Args[2:])
		if err := Writes(*to, *n, *size, *nodelay, os.Stdout); err != nil {
			fail(err)
		}
	case "hold":
		to := fs.String("to", "", "host:port")
		n := fs.Int("n", 100, "connections to open")
		hold := fs.Duration("hold", 0, "keep them open this long after the last attempt")
		_ = fs.Parse(os.Args[2:])
		r := Hold(ctx, *to, *n, *hold)
		fmt.Print(r)
	case "churn":
		to := fs.String("to", "", "host:port")
		n := fs.Int("n", 100, "connections to open and close")
		sf := fs.Bool("server-closes", false, "wait for the server to close first (use with serve -mode close)")
		_ = fs.Parse(os.Args[2:])
		fmt.Print(Churn(*to, *n, *sf))
	case "greet":
		to := fs.String("to", "", "host:port")
		n := fs.Int("n", 300, "connections")
		_ = fs.Parse(os.Args[2:])
		fmt.Print(Greetings(*to, *n))
	case "bind":
		addr := fs.String("addr", "", "host:port")
		ra := fs.Bool("reuseaddr", false, "set SO_REUSEADDR")
		rp := fs.Bool("reuseport", false, "set SO_REUSEPORT")
		_ = fs.Parse(os.Args[2:])
		if err := Bind(*addr, *ra, *rp, os.Stdout); err != nil {
			os.Exit(1)
		}
	case "http":
		var o HTTPOptions
		fs.StringVar(&o.URL, "url", "", "URL to fetch")
		fs.IntVar(&o.N, "n", 200, "requests")
		fs.IntVar(&o.C, "c", 20, "concurrent workers")
		fs.BoolVar(&o.KeepAlive, "keepalive", true, "reuse connections")
		fs.IntVar(&o.IdlePerHost, "idle-per-host", 2, "Transport.MaxIdleConnsPerHost (Go's default is 2)")
		fs.BoolVar(&o.Drain, "drain", true, "read each response body to the end")
		fs.BoolVar(&o.CloseBody, "close-body", true, "close each response body")
		fs.DurationVar(&o.Timeout, "timeout", 10*time.Second, "per-request timeout")
		_ = fs.Parse(os.Args[2:])
		r, err := HTTP(ctx, o)
		if err != nil {
			fail(err)
		}
		fmt.Print(r)
	case "relay":
		listen := fs.String("listen", ":7100", "listen address")
		to := fs.String("to", "", "backend host:port")
		mode := fs.String("mode", "copy", "copy (bounded: backpressure reaches the client) | unbounded (buffer everything in memory)")
		pidfile := fs.String("pidfile", "", "write this process's PID here")
		_ = fs.Parse(os.Args[2:])
		writePid(*pidfile)
		if err := Relay(ctx, *listen, *to, *mode, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case "udp-sink":
		addr := fs.String("addr", ":7200", "listen address")
		per := fs.Duration("per", time.Millisecond, "processing time per datagram")
		rcvbuf := fs.Int("rcvbuf", 0, "SO_RCVBUF (0 = default, net.core.rmem_default; capped at net.core.rmem_max)")
		force := fs.Bool("force", false, "use SO_RCVBUFFORCE (root) to exceed net.core.rmem_max")
		_ = fs.Parse(os.Args[2:])
		if err := UDPSink(ctx, *addr, *per, *rcvbuf, *force, os.Stdout); err != nil {
			fail(err)
		}
	case "udp-blast":
		to := fs.String("to", "", "host:port")
		n := fs.Int("n", 10000, "datagrams")
		size := fs.Int("size", 200, "bytes each")
		_ = fs.Parse(os.Args[2:])
		if err := UDPBlast(*to, *n, *size, os.Stdout); err != nil {
			fail(err)
		}
	case "stat":
		pid := fs.Int("pid", 0, "process to inspect")
		_ = fs.Parse(os.Args[2:])
		s, err := Stat(*pid)
		if err != nil {
			fail(err)
		}
		fmt.Println(s)
	default:
		usage()
	}
}

func writePid(path string) {
	if path != "" {
		_ = os.WriteFile(path, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o644)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: socklab serve -addr ADDR [-mode echo|discard|hold|reads|close] [-model netpoll|threads] [-reuseport] [-id NAME] [-for D] [-pidfile F]
       socklab writes -to HOST:PORT [-n N] [-size B] [-nodelay=false]
       socklab hold -to HOST:PORT [-n N] [-hold D]
       socklab churn -to HOST:PORT [-n N] [-server-closes]
       socklab greet -to HOST:PORT [-n N]
       socklab bind -addr HOST:PORT [-reuseaddr] [-reuseport]
       socklab http -url URL [-n N] [-c C] [-keepalive=false] [-idle-per-host N] [-drain=false] [-close-body=false]
       socklab relay -listen ADDR -to ADDR [-mode copy|unbounded] [-pidfile F]
       socklab udp-sink -addr ADDR [-per D] [-rcvbuf B [-force]]
       socklab udp-blast -to HOST:PORT [-n N] [-size B]
       socklab stat -pid PID
`)
	os.Exit(2)
}
