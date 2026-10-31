// Command phases times each step of one HTTP(S) request — DNS, TCP connect,
// TLS, sending the request, waiting for the server, and reading the body — and,
// when the request fails, names the step that was in progress and explains
// what the error means on the wire.
//
// It is the Go counterpart of `curl -v` from Module 0 and the first tool of the
// course toolbox.
//
//	phases https://example.com
//	phases -resolve example.com:443:203.0.113.10 https://example.com
//	phases -json -timeout 5s https://api.github.com
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	var opts Options
	flag.DurationVar(&opts.Timeout, "timeout", 10*time.Second, "overall deadline for the whole request")
	flag.StringVar(&opts.Resolve, "resolve", "", "skip DNS: connect to ADDR for HOST:PORT, given as host:port:addr (like curl --resolve)")
	flag.StringVar(&opts.CAFile, "cacert", "", "PEM file of extra CA certificates to trust, added to the system pool")
	flag.StringVar(&opts.Proxy, "proxy", "", "use this proxy URL instead of HTTPS_PROXY/HTTP_PROXY (like curl -x)")
	flag.BoolVar(&opts.NoProxy, "noproxy", false, "ignore HTTP_PROXY/HTTPS_PROXY/NO_PROXY and connect directly")
	flag.IntVar(&opts.Count, "count", 1, "make N sequential requests on one client to see connection reuse")
	jsonOut := flag.Bool("json", false, "print the report as JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: phases [flags] URL\n\nTimes each step of one request and names the step that failed.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	opts.URL = flag.Arg(0)

	if path := os.Getenv("SSLKEYLOGFILE"); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "phases: SSLKEYLOGFILE: %v\n", err)
			os.Exit(2)
		}
		defer f.Close()
		opts.KeyLog = f
	}

	reports, err := RunAll(context.Background(), opts)
	if err != nil { // invalid options, not a network failure
		fmt.Fprintf(os.Stderr, "phases: %v\n", err)
		os.Exit(2)
	}
	failed := false
	for _, r := range reports {
		failed = failed || r.Error != ""
	}
	switch {
	case *jsonOut && len(reports) == 1:
		printJSON(reports[0])
	case *jsonOut:
		printJSON(reports)
	default:
		for i, r := range reports {
			if len(reports) > 1 {
				fmt.Printf("── request %d of %d ──\n", i+1, len(reports))
			}
			r.WriteText(os.Stdout)
			fmt.Println()
		}
	}
	if failed {
		os.Exit(1)
	}
}

func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
