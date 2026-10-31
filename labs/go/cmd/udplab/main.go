// Command udplab is a small UDP test kit for the course: an echo server and a
// sender that numbers its datagrams and reports loss, duplicates, reordering,
// round-trip times, retries, truncation, and the errors UDP can and cannot
// report.
//
//	udplab echo -addr 198.18.40.2:9999 [-delay 2s] [-log]
//	udplab send -to 198.18.40.2:9999 -n 100 -interval 20ms -timeout 500ms
//	udplab send -to 198.18.40.2:9999 -n 100 -retries 2      # application-level retry
//	udplab send -to 198.18.40.2:9999 -unconnected            # no ICMP errors delivered
//	udplab send -to 198.18.40.2:9999 -size 1000 -recvbuf 100 # see MSG_TRUNC
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	switch os.Args[1] {
	case "echo":
		fs := flag.NewFlagSet("echo", flag.ExitOnError)
		addr := fs.String("addr", ":9999", "UDP address to listen on")
		delay := fs.Duration("delay", 0, "wait this long before echoing each datagram")
		logEach := fs.Bool("log", false, "print one line per datagram received")
		_ = fs.Parse(os.Args[2:])
		if err := Echo(ctx, *addr, *delay, *logEach, os.Stdout); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "udplab: %v\n", err)
			os.Exit(1)
		}
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		var o SendOptions
		fs.StringVar(&o.To, "to", "", "destination host:port (required)")
		fs.IntVar(&o.Count, "n", 10, "number of datagrams")
		fs.IntVar(&o.Size, "size", 64, "payload bytes per datagram (minimum 16)")
		fs.DurationVar(&o.Interval, "interval", 100*time.Millisecond, "gap between datagrams")
		fs.DurationVar(&o.Timeout, "timeout", time.Second, "how long to wait for each echo")
		fs.IntVar(&o.Retries, "retries", 0, "resend a datagram this many times if its echo does not arrive")
		fs.BoolVar(&o.Unconnected, "unconnected", false, "use an unconnected socket (sendto), which never sees ICMP errors")
		fs.IntVar(&o.RecvBuf, "recvbuf", 0, "read replies into a buffer of this size (to see truncation); 0 = large enough")
		jsonOut := fs.Bool("json", false, "print the report as JSON")
		_ = fs.Parse(os.Args[2:])
		if o.To == "" || o.Count < 1 || o.Size < headerLen {
			fs.Usage()
			os.Exit(2)
		}
		r, err := Send(ctx, o)
		if err != nil {
			fmt.Fprintf(os.Stderr, "udplab: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(r)
		} else {
			fmt.Print(r.Format())
		}
		if r.Answered == 0 {
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, "usage: udplab echo [-addr ADDR] [-delay D] [-log]\n       udplab send -to HOST:PORT [-n N] [-size B] [-interval D] [-timeout D] [-retries R] [-unconnected] [-recvbuf B] [-json]\n")
	os.Exit(2)
}
