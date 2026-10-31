// Command obslab is a two-service application instrumented by hand, so the
// course can measure what each kind of evidence shows: W3C trace context
// and spans, Prometheus-format metrics with histograms, structured logs with
// trace IDs, and conntrack flow records.
//
//	obslab serve -name backend -addr :8081 [-slow 0.1 -delay 300ms] [-fail 0.02] -spans F -log F
//	obslab serve -name frontend -addr :8080 -backend http://B:8081 -spans F -log F
//	obslab load -url http://F:8080/ -n 1000 -c 20 [-sample 0.1] [-spans F]
//	obslab spans [-slowest 5] [-tail-slow 250ms] FILE...
//	obslab metrics -url http://F:8080/metrics [-q 0.5,0.9,0.99]
//	obslab logs FILE...                    # requests by status and error
//	conntrack -L -o extended | obslab flows
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
		fmt.Fprintf(os.Stderr, "obslab: %v\n", err)
		os.Exit(1)
	}
	switch os.Args[1] {
	case "serve":
		var o ServeOptions
		fs.StringVar(&o.Name, "name", "backend", "service name (frontend or backend)")
		fs.StringVar(&o.Addr, "addr", ":8080", "listen address")
		fs.StringVar(&o.Backend, "backend", "", "base URL of the backend (makes this a frontend)")
		fs.DurationVar(&o.Timeout, "timeout", time.Second, "frontend: timeout for each backend call")
		fs.StringVar(&o.Spans, "spans", "", "append spans (JSON lines) here")
		fs.StringVar(&o.Log, "log", "", "append request logs (JSON lines) here")
		fs.Float64Var(&o.SlowFrac, "slow", 0, "fraction of requests to delay")
		fs.DurationVar(&o.Slow, "delay", 300*time.Millisecond, "how long to delay them")
		fs.Float64Var(&o.FailFrac, "fail", 0, "fraction of requests to fail with 503")
		_ = fs.Parse(os.Args[2:])
		if err := Serve(ctx, o, os.Stdout); err != nil {
			fail(err)
		}
	case "load":
		var o LoadOptions
		fs.StringVar(&o.URL, "url", "", "URL")
		fs.IntVar(&o.N, "n", 1000, "requests")
		fs.IntVar(&o.C, "c", 20, "concurrent workers")
		fs.Float64Var(&o.Sample, "sample", 1, "head-sampling probability")
		fs.StringVar(&o.Spans, "spans", "", "append the client's spans here")
		fs.DurationVar(&o.Timeout, "timeout", 5*time.Second, "per-request timeout")
		_ = fs.Parse(os.Args[2:])
		out, err := Load(ctx, o)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "spans":
		slowest := fs.Int("slowest", 5, "show the N slowest traces with their breakdown")
		tail := fs.Duration("tail-slow", 0, "report what a tail-sampling rule (errors, or slower than this) would keep")
		_ = fs.Parse(os.Args[2:])
		out, err := Analyze(fs.Args(), *slowest, *tail)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "metrics":
		url := fs.String("url", "", "/metrics URL")
		q := fs.String("q", "0.5,0.9,0.99", "quantiles")
		save := fs.String("save", "", "also save this scrape to a file")
		since := fs.String("since", "", "subtract a scrape saved earlier (counters only grow)")
		_ = fs.Parse(os.Args[2:])
		var qs []float64
		for _, s := range strings.Split(*q, ",") {
			v, err := strconv.ParseFloat(s, 64)
			if err != nil {
				fail(err)
			}
			qs = append(qs, v)
		}
		out, err := Quantiles(*url, qs, *save, *since)
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "logs":
		for _, p := range os.Args[2:] {
			out, err := SummarizeLogs(p)
			if err != nil {
				fail(err)
			}
			fmt.Print(out)
		}
	case "flows":
		fmt.Print(FormatFlows(ParseFlows(os.Stdin)))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: obslab serve -name NAME -addr ADDR [-backend URL] [-slow F -delay D] [-fail F] [-spans FILE] [-log FILE]
       obslab load -url URL [-n N] [-c C] [-sample P] [-spans FILE]
       obslab spans [-slowest N] [-tail-slow D] FILE...
       obslab metrics -url URL [-q 0.5,0.9,0.99] [-save F] [-since F]
       obslab logs FILE...
       conntrack -L -o extended | obslab flows
`)
	os.Exit(2)
}
