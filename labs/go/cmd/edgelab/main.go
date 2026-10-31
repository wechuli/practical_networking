// Command edgelab supplies the pieces of Module 23's labs that are easier to
// see when written out: a backend that reports what a load balancer passed
// it (peer, X-Forwarded-For, PROXY protocol), a client that counts which
// backend answered, a forward proxy, an NTP server that is wrong on purpose
// with a client that shows the offset arithmetic, and a TLS check with a
// shifted clock.
//
//	edgelab backend -addr :8080 -id b1 [-proxy-protocol] [-idle 5s] [-health 200]
//	edgelab hits -url http://LB/ [-n 30] [-interval 100ms] [-cookies] [-xff 1.2.3.4] [-env-proxy]
//	edgelab raw -to HOST:PORT 'PROXY TCP4 …\r\nGET / HTTP/1.1\r\n…'
//	edgelab fproxy -addr :3128
//	edgelab ntp serve -addr :123 [-offset 5m] [-request-delay D] [-reply-delay D] | edgelab ntp query -server HOST:123
//	edgelab tlsclock -connect HOST:443 -servername NAME -cafile CA [-offset -240h]
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
		fmt.Fprintf(os.Stderr, "edgelab: %v\n", err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "backend":
		var o BackendOptions
		fs.StringVar(&o.Addr, "addr", ":8080", "listen address")
		fs.StringVar(&o.ID, "id", "b1", "name in every response")
		fs.BoolVar(&o.ProxyProtocol, "proxy-protocol", false, "require a PROXY protocol v1 header on each connection")
		fs.DurationVar(&o.Idle, "idle", 0, "keep-alive idle timeout (0 = Go's default)")
		fs.IntVar(&o.HealthCode, "health", 200, "status returned by /health")
		_ = fs.Parse(os.Args[2:])
		if err := Backend(ctx, o, os.Stdout); err != nil {
			fail(err)
		}
	case "hits":
		var o HitsOptions
		fs.StringVar(&o.URL, "url", "", "URL")
		fs.IntVar(&o.N, "n", 30, "requests")
		fs.DurationVar(&o.Interval, "interval", 0, "pause between requests")
		fs.BoolVar(&o.Cookies, "cookies", false, "keep cookies between requests")
		fs.StringVar(&o.XFF, "xff", "", "send this X-Forwarded-For (spoofing test)")
		fs.DurationVar(&o.Timeout, "timeout", 3*time.Second, "per-request timeout")
		fs.IntVar(&o.Show, "show", 0, "print the first N responses")
		fs.BoolVar(&o.EnvProxy, "env-proxy", false, "honor HTTP_PROXY / http_proxy / NO_PROXY (Go's http.ProxyFromEnvironment)")
		_ = fs.Parse(os.Args[2:])
		fmt.Print(Hits(ctx, o))
	case "raw":
		to := fs.String("to", "", "host:port")
		_ = fs.Parse(os.Args[2:])
		fmt.Print(RawRequest(*to, fs.Arg(0)))
	case "fproxy":
		addr := fs.String("addr", ":3128", "listen address")
		_ = fs.Parse(os.Args[2:])
		if err := ForwardProxy(ctx, *addr, os.Stdout); err != nil {
			fail(err)
		}
	case "ntp":
		if len(os.Args) < 3 {
			usage()
		}
		fs = flag.NewFlagSet("ntp "+os.Args[2], flag.ExitOnError)
		switch os.Args[2] {
		case "serve":
			addr := fs.String("addr", ":123", "listen address")
			offset := fs.Duration("offset", 0, "add this to the time served")
			stratum := fs.Int("stratum", 2, "stratum to advertise")
			qd := fs.Duration("request-delay", 0, "hold each request this long before timestamping it (one-way delay in)")
			rd := fs.Duration("reply-delay", 0, "hold each reply this long after timestamping it (one-way delay out)")
			_ = fs.Parse(os.Args[3:])
			if err := NTPServe(ctx, *addr, *offset, *qd, *rd, *stratum, os.Stdout); err != nil {
				fail(err)
			}
		case "query":
			server := fs.String("server", "", "host:123")
			n := fs.Int("n", 3, "samples")
			_ = fs.Parse(os.Args[3:])
			out, err := NTPQuery(*server, *n)
			if err != nil {
				fail(err)
			}
			fmt.Print(out)
		default:
			usage()
		}
	case "tlsclock":
		addr := fs.String("connect", "", "host:port")
		name := fs.String("servername", "", "name to verify")
		ca := fs.String("cafile", "", "trusted CA")
		offset := fs.Duration("offset", 0, "pretend the clock is off by this much")
		_ = fs.Parse(os.Args[2:])
		fmt.Print(TLSClock(*addr, *name, *ca, *offset))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: edgelab backend -addr ADDR -id NAME [-proxy-protocol] [-idle D] [-health CODE]
       edgelab hits -url URL [-n N] [-interval D] [-cookies] [-xff IP] [-show N] [-env-proxy]
       edgelab raw -to HOST:PORT 'TEXT with \r\n'
       edgelab fproxy -addr ADDR
       edgelab ntp serve -addr ADDR [-offset D] [-request-delay D] [-reply-delay D] [-stratum N]
       edgelab ntp query -server HOST:PORT [-n N]
       edgelab tlsclock -connect HOST:PORT -servername NAME -cafile CA [-offset D]
`)
	os.Exit(2)
}
