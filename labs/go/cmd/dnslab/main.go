// Command dnslab is the course's DNS toolkit, written against the standard
// library only (its DNS message code is in msg.go, to be read).
//
//	dnslab query -s 198.18.50.2 [-norec] [-tcp] [-dnssec] [-cd] [-bufsize 1232] NAME [TYPE]
//	dnslab trace -root 198.18.50.2 NAME [TYPE]      # resolve iteratively, printing every referral
//	dnslab watch -s 198.18.54.2,198.18.60.2 -for 40s NAME [TYPE]   # when does each server's answer change?
//	dnslab lookup NAME                             # Go's own resolver (reads /etc/resolv.conf)
package main

import (
	"context"
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
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	switch os.Args[1] {
	case "query":
		server := fs.String("s", "", "server to ask (required)")
		norec := fs.Bool("norec", false, "clear RD: ask an authoritative server, not a resolver")
		tcp := fs.Bool("tcp", false, "use TCP")
		dnssec := fs.Bool("dnssec", false, "set DO: ask for DNSSEC records")
		cd := fs.Bool("cd", false, "set CD: ask the resolver not to validate")
		bufsize := fs.Int("bufsize", 1232, "EDNS UDP buffer size; 0 sends no EDNS (classic 512-byte limit)")
		timeout := fs.Duration("timeout", 3*time.Second, "how long to wait")
		_ = fs.Parse(os.Args[2:])
		name, qtype := args(fs)
		if *server == "" {
			fs.Usage()
			os.Exit(2)
		}
		ex, err := Ask(ctx, *server, name, qtype, QueryOptions{Recurse: !*norec, DNSSEC: *dnssec, CD: *cd, UDPSize: uint16(*bufsize)}, *tcp, *timeout)
		if err != nil {
			fmt.Printf("%s: %s\n", *server, Describe(err))
			os.Exit(1)
		}
		printExchange(ex)
	case "trace":
		roots := fs.String("root", "198.18.50.2", "root server addresses (comma-separated root hints)")
		timeout := fs.Duration("timeout", 2*time.Second, "per-query timeout")
		_ = fs.Parse(os.Args[2:])
		name, qtype := args(fs)
		t := &Tracer{Timeout: *timeout, Out: os.Stdout}
		for _, r := range strings.Split(*roots, ",") {
			a, err := netip.ParseAddr(strings.TrimSpace(r))
			if err != nil {
				fmt.Fprintf(os.Stderr, "dnslab: bad root address %q\n", r)
				os.Exit(2)
			}
			t.Roots = append(t.Roots, a)
		}
		res, err := t.Resolve(ctx, name, qtype)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dnslab: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("result: %s after %d queries in %s\n", RcodeName(res.Rcode), res.Steps, fmtRTT(res.Elapsed))
		if res.Rcode != RcodeNoError {
			os.Exit(1)
		}
	case "watch":
		servers := fs.String("s", "", "servers to ask, comma-separated (required)")
		interval := fs.Duration("interval", time.Second, "time between rounds")
		dur := fs.Duration("for", 30*time.Second, "how long to watch")
		norec := fs.Bool("norec", false, "clear RD (for authoritative servers)")
		_ = fs.Parse(os.Args[2:])
		name, qtype := args(fs)
		if *servers == "" {
			fs.Usage()
			os.Exit(2)
		}
		Watch(ctx, WatchOptions{Servers: strings.Split(*servers, ","), Name: name, Type: qtype,
			Interval: *interval, For: *dur, Recurse: !*norec}, os.Stdout)
	case "lookup":
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			fs.Usage()
			os.Exit(2)
		}
		start := time.Now()
		addrs, err := net.DefaultResolver.LookupHost(ctx, fs.Arg(0))
		took := time.Since(start)
		if err != nil {
			fmt.Printf("lookup %s failed after %s: %v\n", fs.Arg(0), fmtRTT(took), err)
			os.Exit(1)
		}
		fmt.Printf("%s → %s in %s\n", fs.Arg(0), strings.Join(addrs, ", "), fmtRTT(took))
	default:
		usage()
	}
}

func args(fs *flag.FlagSet) (string, uint16) {
	if fs.NArg() < 1 || fs.NArg() > 2 {
		fs.Usage()
		os.Exit(2)
	}
	qtype := TypeA
	if fs.NArg() == 2 {
		t, err := ParseType(fs.Arg(1))
		if err != nil {
			fmt.Fprintf(os.Stderr, "dnslab: %v\n", err)
			os.Exit(2)
		}
		qtype = t
	}
	return fs.Arg(0), qtype
}

func printExchange(ex *Exchange) {
	m := ex.Msg
	proto := "UDP"
	if ex.TCP {
		proto = "TCP"
	}
	if ex.Retried {
		proto = "UDP truncated (tc), then TCP"
	}
	fmt.Printf("%s from %s over %s in %s, %d bytes\n", RcodeName(m.Header.Rcode), ex.Server, proto, fmtRTT(ex.RTT), m.Size)
	fmt.Printf("flags: %s\n", m.Header.Flags())
	if e := m.EDNS(); e != nil {
		fmt.Printf("edns: udp %d", e.UDPSize)
		if e.EDECode >= 0 {
			fmt.Printf(", EDE %d (%s) %s", e.EDECode, EDEName(e.EDECode), e.EDEText)
		}
		fmt.Println()
	}
	for _, sec := range []struct {
		name string
		rrs  []RR
	}{{"answer", m.Answer}, {"authority", m.Authority}, {"additional", m.Additional}} {
		for _, r := range sec.rrs {
			if r.Type != TypeOPT {
				fmt.Printf("%-10s %s\n", sec.name, r)
			}
		}
	}
	fmt.Println(Summary(m))
	if ttl, soa := NegativeTTL(m); soa != nil && len(m.Answer) == 0 {
		fmt.Printf("negative answer cacheable for %d s = min(SOA TTL %d, MINIMUM %d)\n", ttl, soa.TTL, soa.SOA.MinTTL)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: dnslab query -s SERVER [-norec] [-tcp] [-dnssec] [-cd] [-bufsize N] NAME [TYPE]
       dnslab trace [-root ADDRS] NAME [TYPE]
       dnslab watch -s SERVERS [-interval D] [-for D] [-norec] NAME [TYPE]
       dnslab lookup NAME
`)
	os.Exit(2)
}
