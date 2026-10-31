// Command caselab supports the Module 26 capstones.
//
//	caselab probe -config FILE [-resolv /etc/resolv.conf]
//	    the capstone application: resolve, connect, TLS with its configured
//	    trust, one HTTP GET — reported stage by stage, like an application log
//	caselab case6 -flows flows.csv -metrics metrics.json
//	    the instructor's reading of the Capstone 6 dataset (an answer key)
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	switch os.Args[1] {
	case "probe":
		cfg := fs.String("config", "", "application config (JSON: url, ca_file, timeout)")
		resolv := fs.String("resolv", "/etc/resolv.conf", "resolver configuration to report")
		_ = fs.Parse(os.Args[2:])
		c, err := LoadConfig(*cfg)
		if err != nil {
			fmt.Fprintln(os.Stderr, "caselab:", err)
			os.Exit(2)
		}
		if err := Probe(ctx, c, *resolv, os.Stdout); err != nil {
			fmt.Println("RESULT: FAIL")
			os.Exit(1)
		}
		fmt.Println("RESULT: OK")
	case "case6":
		flows := fs.String("flows", "", "flow-log CSV")
		metrics := fs.String("metrics", "", "metrics JSON")
		_ = fs.Parse(os.Args[2:])
		if err := Case6(*flows, *metrics, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "caselab: dataset check failed:", err)
			os.Exit(1)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: caselab probe -config FILE [-resolv FILE]
       caselab case6 -flows FILE -metrics FILE
`)
	os.Exit(2)
}
