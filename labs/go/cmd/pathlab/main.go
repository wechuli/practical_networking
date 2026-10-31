//go:build linux

// Command pathlab is a userspace path emulator — a small, readable stand-in
// for tc netem. It reads packets from two TUN devices (one per direction),
// delays them, rate-limits them through a bounded queue, drops a fraction at
// random, and writes them back so the kernel forwards them on.
//
// In a router namespace (see the course's perf lab):
//
//	ip tuntap add dev pl-ab mode tun; ip tuntap add dev pl-ba mode tun
//	ip rule add iif eth0 table 100; ip route add default dev pl-ab table 100
//	ip rule add iif eth1 table 101; ip route add default dev pl-ba table 101
//	pathlab run -ab pl-ab -ba pl-ba -delay 25ms -rate 20mbit -limit 100
//	pathlab set loss=0.01 rate=100mbit      # change it while it runs
//	pathlab set drop_big=3                  # drop exactly the 3rd packet of ≥ 1000 bytes going B→A
//	pathlab stats                           # counters for both directions
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const control = "127.0.0.1:9911"

var controlAddr = control

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		ab := fs.String("ab", "pl-ab", "TUN device carrying traffic from side A to side B")
		ba := fs.String("ba", "pl-ba", "TUN device carrying traffic from side B to side A")
		var p Params
		fs.DurationVar(&p.Delay, "delay", 0, "one-way delay added in each direction")
		fs.DurationVar(&p.Jitter, "jitter", 0, "random extra delay, 0..jitter (order is kept)")
		fs.Float64Var(&p.Loss, "loss", 0, "fraction of packets dropped at random in each direction (0.01 = 1%)")
		rate := fs.String("rate", "0", "bottleneck rate per direction, e.g. 20mbit; 0 = unlimited")
		fs.IntVar(&p.Limit, "limit", 1000, "queue length in packets at the bottleneck (tail drop beyond it)")
		listen := fs.String("control", control, "address for `pathlab set` and `pathlab stats`")
		_ = fs.Parse(os.Args[2:])
		r, err := ParseRate(*rate)
		if err != nil {
			fail(err)
		}
		p.RateBps = r
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := Run(ctx, *ab, *ba, p, *listen, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case "set":
		q := url.Values{}
		args := os.Args[2:]
		if len(args) >= 2 && args[0] == "-c" {
			controlAddr, args = args[1], args[2:]
		}
		for _, kv := range args {
			k, v, ok := strings.Cut(kv, "=")
			if !ok {
				fail(fmt.Errorf("want key=value, got %q", kv))
			}
			q.Set(k, v)
		}
		get("/set?" + q.Encode())
	case "stats":
		if len(os.Args) == 4 && os.Args[2] == "-c" {
			controlAddr = os.Args[3]
		}
		get("/stats")
	default:
		usage()
	}
}

func get(path string) {
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	resp, err := c.Get("http://" + controlAddr + path)
	if err != nil {
		fail(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(os.Stdout, resp.Body)
	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

// Run moves packets through both directions until ctx ends.
func Run(ctx context.Context, abName, baName string, p Params, listen string, w io.Writer) error {
	ab, err := OpenTUN(abName)
	if err != nil {
		return fmt.Errorf("%s: %w", abName, err)
	}
	defer ab.Close()
	ba, err := OpenTUN(baName)
	if err != nil {
		return fmt.Errorf("%s: %w", baName, err)
	}
	defer ba.Close()
	path := NewPath(p)
	dirs := []*Direction{path.AB, path.BA}
	go dirs[0].Pump(ctx, ab)
	go dirs[1].Pump(ctx, ba)

	mux := http.NewServeMux()
	mux.HandleFunc("/set", func(rw http.ResponseWriter, r *http.Request) {
		np, err := path.Params().Apply(r.URL.Query())
		if err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		path.SetParams(np)
		fmt.Fprintf(w, "%s set %s\n", time.Now().Format("15:04:05.000"), np)
		fmt.Fprintf(rw, "pathlab: %s\n", np)
	})
	mux.HandleFunc("/stats", func(rw http.ResponseWriter, r *http.Request) {
		enc := json.NewEncoder(rw)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"params": path.Params().String(), "a_to_b": path.AB.Stats(), "b_to_a": path.BA.Stats()})
	})
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { <-ctx.Done(); srv.Close() }()
	fmt.Fprintf(w, "pathlab: %s ⇄ %s, %s; control on %s\n", abName, baName, p, listen)
	err = srv.Serve(ln)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "pathlab: %v\n", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: pathlab run [-ab TUN] [-ba TUN] [-delay D] [-jitter D] [-loss P] [-rate R] [-limit N]
       pathlab set [-c ADDR] delay=25ms loss=0.01 rate=20mbit limit=100 jitter=0 drop_big=N
       pathlab stats [-c ADDR]
`)
	os.Exit(2)
}
