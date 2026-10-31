// Command gotrace is a small traceroute written for the course: it sends UDP
// or TCP SYN probes with increasing TTL, reads the ICMP replies on a raw
// socket, and explains each one. It exists to show how traceroute works, not
// to replace it.
//
//	sudo gotrace 198.19.7.2                 # UDP probes to ports 33434, 33435, …
//	sudo gotrace -fixed 198.19.7.2          # one UDP flow for every probe (ECMP-stable)
//	sudo gotrace -tcp -p 443 example.com    # TCP SYN probes to the port real traffic uses
//
// Probes are sent one at a time, so replies are matched unambiguously and
// router ICMP rate limits are rarely hit.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"time"
)

func main() {
	tcp := flag.Bool("tcp", false, "send TCP SYN probes instead of UDP")
	port := flag.Int("p", 0, "destination port (UDP: first port, default 33434; TCP: default 443)")
	fixed := flag.Bool("fixed", false, "UDP: use the same source and destination port for every probe (Paris-traceroute style), so ECMP routers send every probe the same way")
	maxHops := flag.Int("m", 30, "maximum TTL")
	first := flag.Int("f", 1, "first TTL")
	queries := flag.Int("q", 3, "probes per hop")
	wait := flag.Duration("w", 5*time.Second, "how long to wait for each reply (a router that has to ARP first answers after ~3 s)")
	jsonOut := flag.Bool("json", false, "print the hops as JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: gotrace [flags] HOST\n\nTraces the path to HOST with UDP or TCP probes and increasing TTL. Needs root (raw ICMP socket).\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || *maxHops < 1 || *maxHops > 255 || *first < 1 || *first > *maxHops || *queries < 1 {
		flag.Usage()
		os.Exit(2)
	}
	target, err := resolve(flag.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "gotrace: %v\n", err)
		os.Exit(2)
	}
	opt := Options{Target: target, Mode: "udp", Port: *port, Fixed: *fixed, MaxHops: *maxHops, Queries: *queries, Wait: *wait, FirstTTL: *first}
	if *tcp {
		opt.Mode = "tcp"
		if opt.Port == 0 {
			opt.Port = 443
		}
	} else if opt.Port == 0 {
		opt.Port = 33434
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if !*jsonOut {
		fmt.Printf("gotrace to %s (%s), %d hops max, %s probes to port %d%s\n", flag.Arg(0), target, opt.MaxHops,
			strings.ToUpper(opt.Mode), opt.Port, map[bool]string{true: " (fixed)", false: ""}[opt.Fixed && opt.Mode == "udp"])
	}
	report := func(h Hop) {
		if !*jsonOut {
			fmt.Println(FormatHop(h))
		}
	}
	hops, err := Run(ctx, opt, report)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(hops)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "gotrace: %v\n", err)
		os.Exit(1)
	}
	switch {
	case len(hops) > 0 && hops[len(hops)-1].Reached():
	case len(hops) > 0 && hops[len(hops)-1].Done():
		fmt.Fprintln(os.Stderr, "gotrace: a router reported the destination unreachable")
		os.Exit(1)
	default:
		fmt.Fprintln(os.Stderr, "gotrace: the destination did not answer within the hop limit")
		os.Exit(1)
	}
}

// FormatHop prints a hop the way traceroute does, plus the meaning of the
// last distinct reply.
func FormatHop(h Hop) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%2d ", h.TTL)
	last := ""
	meaning := ""
	for _, p := range h.Probes {
		if p.Kind == "timeout" {
			b.WriteString(" *")
			continue
		}
		if p.From != last {
			fmt.Fprintf(&b, " %s", p.From)
			last = p.From
		}
		fmt.Fprintf(&b, "  %.3f ms", p.RTTms)
		if p.Mark != "" {
			b.WriteString(" " + p.Mark)
		}
		meaning = p.Meaning
	}
	if meaning != "" {
		fmt.Fprintf(&b, "   (%s)", meaning)
	}
	return b.String()
}

func resolve(host string) (netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		if !a.Is4() {
			return a, fmt.Errorf("%s: only IPv4 is supported", host)
		}
		return a, nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(context.Background(), "ip4", host)
	if err != nil {
		return netip.Addr{}, err
	}
	return addrs[0], nil
}
