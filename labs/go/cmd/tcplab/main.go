//go:build linux

// Command tcplab is a small TCP test kit for the course. Its server can
// misbehave on purpose (never close, never read, never accept, reset), and its
// client reports how a connection attempt ended and what the kernel's TCP_INFO
// says about a transfer: retransmissions, RTT, congestion window, algorithm.
//
//	tcplab serve -addr 198.18.43.10:8080 -mode discard     # read everything, then close
//	tcplab serve -addr :8081 -mode hold                    # never close: CLOSE_WAIT leak
//	tcplab serve -addr :8082 -mode noread -rcvbuf 4096     # never read: zero window
//	tcplab serve -addr :8083 -mode noaccept -backlog 2     # never accept: queue overflow
//	tcplab serve -addr :8084 -mode reset                   # abort with RST
//	tcplab serve -addr :8085 -mode close                   # close (FIN) without answering
//	tcplab dial -to 198.18.43.10:8080 [-n 5] [-hold 5s]    # how did connect() end, and when?
//	tcplab send -to 198.18.43.10:8080 -bytes 20M -cc bbr   # transfer and report TCP_INFO
package main

import (
	"context"
	"encoding/json"
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
	switch os.Args[1] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		var o ServeOptions
		fs.StringVar(&o.Addr, "addr", ":8080", "TCP address to listen on")
		fs.StringVar(&o.Mode, "mode", "discard", "discard | echo | hold | noread | noaccept | reset | close")
		fs.IntVar(&o.Backlog, "backlog", 128, "listen() backlog (accept queue length)")
		fs.IntVar(&o.RcvBuf, "rcvbuf", 0, "SO_RCVBUF for accepted sockets in bytes; 0 = kernel autotuning")
		fs.BoolVar(&o.Quiet, "quiet", false, "do not log each connection")
		_ = fs.Parse(os.Args[2:])
		if err := Serve(ctx, o, os.Stdout); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "tcplab: %v\n", err)
			os.Exit(1)
		}
	case "dial":
		fs := flag.NewFlagSet("dial", flag.ExitOnError)
		var o DialOptions
		fs.StringVar(&o.To, "to", "", "destination host:port (required)")
		fs.IntVar(&o.Count, "n", 1, "number of connections to open at once")
		fs.DurationVar(&o.Timeout, "timeout", 0, "give up after this long; 0 = let the kernel decide (tcp_syn_retries)")
		fs.DurationVar(&o.Hold, "hold", 0, "keep successful connections open this long before closing")
		jsonOut := fs.Bool("json", false, "print the results as JSON")
		_ = fs.Parse(os.Args[2:])
		if o.To == "" || o.Count < 1 {
			fs.Usage()
			os.Exit(2)
		}
		rs := Dial(ctx, o)
		if *jsonOut {
			printJSON(rs)
		} else {
			fmt.Print(FormatDials(rs))
		}
		for _, r := range rs {
			if r.Outcome != "connected" {
				os.Exit(1)
			}
		}
	case "send":
		fs := flag.NewFlagSet("send", flag.ExitOnError)
		var o SendOptions
		var size string
		fs.StringVar(&o.To, "to", "", "destination host:port (required)")
		fs.StringVar(&size, "bytes", "1M", "how much to send: 1500, 64K, 20M")
		fs.StringVar(&o.CC, "cc", "", "congestion control for this socket (cubic, bbr, reno); empty = system default")
		fs.DurationVar(&o.Timeout, "timeout", 30*time.Second, "give up if the transfer has not finished by then")
		jsonOut := fs.Bool("json", false, "print the report as JSON")
		_ = fs.Parse(os.Args[2:])
		n, err := parseSize(size)
		if o.To == "" || err != nil {
			fs.Usage()
			os.Exit(2)
		}
		o.Bytes = n
		r, err := Send(ctx, o)
		if err != nil {
			fmt.Fprintf(os.Stderr, "tcplab: %v\n", err)
			os.Exit(1)
		}
		if *jsonOut {
			printJSON(r)
		} else {
			fmt.Print(r.Format())
		}
		if !r.Complete {
			os.Exit(1)
		}
	default:
		usage()
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: tcplab serve [-addr ADDR] [-mode discard|echo|hold|noread|noaccept|reset|close] [-backlog N] [-rcvbuf B] [-quiet]
       tcplab dial -to HOST:PORT [-n N] [-timeout D] [-hold D] [-json]
       tcplab send -to HOST:PORT [-bytes N] [-cc ALGO] [-timeout D] [-json]
`)
	os.Exit(2)
}
