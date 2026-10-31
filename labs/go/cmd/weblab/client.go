package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// Fetch is `weblab get`: request every URL (repeated N times) at once, the
// way a browser requests a page's resources, and report when each finished
// and over which connection.
type Fetch struct {
	URLs    []string
	N       int
	Proto   string // h1, h2 or h2c
	Conns   int    // HTTP/1.1: connections allowed per host (browsers: 6)
	CAFile  string
	Stagger time.Duration // start request i after i × Stagger (0: all at once)
	Page    bool          // fetch the first URL alone first, as a browser fetches a page before its resources
	KeyLog  io.Writer
	Out     io.Writer
}

// Result is one request's outcome.
type Result struct {
	Index  int
	URL    string
	Status string
	Proto  string
	Conn   string // client's local address: one per TCP connection
	Reused bool
	Bytes  int64
	Done   time.Duration
	Err    error
}

// Transport builds the client for a protocol.
func Transport(proto string, conns int, caFile string, keylog io.Writer) (*http.Transport, error) {
	cfg := &tls.Config{KeyLogWriter: keylog}
	if caFile != "" {
		pemData, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		cfg.RootCAs = x509.NewCertPool()
		cfg.RootCAs.AppendCertsFromPEM(pemData)
	}
	var p http.Protocols
	tr := &http.Transport{Proxy: nil, TLSClientConfig: cfg, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 100, Protocols: &p}
	switch proto {
	case "h1":
		p.SetHTTP1(true)
		tr.MaxConnsPerHost = conns
	case "h2":
		p.SetHTTP2(true)
	case "h2c":
		p.SetUnencryptedHTTP2(true)
	default:
		return nil, fmt.Errorf("unknown protocol %q (h1, h2, h2c)", proto)
	}
	return tr, nil
}

// Run performs the fetch and prints a line per request and a summary.
func (f Fetch) Run(ctx context.Context) ([]Result, error) {
	tr, err := Transport(f.Proto, f.Conns, f.CAFile, f.KeyLog)
	if err != nil {
		return nil, err
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 60 * time.Second}
	var urls []string
	for i := 0; i < max(f.N, 1); i++ {
		urls = append(urls, f.URLs...)
	}
	results := make([]Result, len(urls))
	start := time.Now()
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		fetch := func(i int, u string) {
			defer wg.Done()
			if f.Stagger > 0 {
				time.Sleep(time.Duration(i) * f.Stagger)
			}
			res := Result{Index: i + 1, URL: u}
			trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				res.Conn, res.Reused = info.Conn.LocalAddr().String(), info.Reused
			}}
			req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, u, nil)
			if err != nil {
				res.Err = err
				results[i] = res
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				res.Err, res.Done = err, time.Since(start)
				results[i] = res
				return
			}
			res.Bytes, err = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			res.Status, res.Proto, res.Done, res.Err = resp.Status, resp.Proto, time.Since(start), err
			results[i] = res
		}
		if i == 0 && f.Page {
			fetch(i, u) // the page, alone: its connection is then ready for the rest
			continue
		}
		go fetch(i, u)
	}
	wg.Wait()
	total := time.Since(start)
	conns := map[string]bool{}
	var bytes int64
	failed := 0
	byDone := append([]Result(nil), results...)
	sort.Slice(byDone, func(a, b int) bool { return byDone[a].Done < byDone[b].Done })
	for _, r := range byDone {
		if r.Err != nil {
			failed++
			fmt.Fprintf(f.Out, "#%-3d %-40s error after %s: %v\n", r.Index, short(r.URL), r.Done.Round(time.Millisecond), r.Err)
			continue
		}
		conns[r.Conn] = true
		bytes += r.Bytes
		fmt.Fprintf(f.Out, "#%-3d %-40s %s %s %7d bytes  done %6.0f ms  conn %s\n", r.Index, short(r.URL), r.Proto, strings.Fields(r.Status)[0], r.Bytes, float64(r.Done.Microseconds())/1000, r.Conn)
	}
	fmt.Fprintf(f.Out, "summary: %s, %d requests, %d TCP connections, %d bytes, %d failed, total %.0f ms\n",
		f.Proto, len(urls), len(conns), bytes, failed, float64(total.Microseconds())/1000)
	if failed > 0 {
		return results, fmt.Errorf("%d requests failed", failed)
	}
	return results, nil
}

func short(u string) string {
	if i := strings.Index(u, "://"); i >= 0 {
		if j := strings.Index(u[i+3:], "/"); j >= 0 {
			return u[i+3+j:]
		}
	}
	return u
}

// WatchEvents is `weblab sse`: read an event stream and print each event
// with the time it arrived, so a buffering proxy is visible.
func WatchEvents(ctx context.Context, url, lastID, caFile string, out io.Writer) error {
	tr, err := Transport("h1", 1, caFile, nil)
	if err != nil {
		return err
	}
	tr.Protocols.SetHTTP2(true)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	start := time.Now()
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	fmt.Fprintf(out, "%s %s, content-type %s\n", resp.Proto, resp.Status, resp.Header.Get("Content-Type"))
	sc := bufio.NewScanner(resp.Body)
	var id, data string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			id = line[4:]
		case strings.HasPrefix(line, "data: "):
			data = line[6:]
		case line == "" && data != "":
			fmt.Fprintf(out, "+%6.0f ms  id %s  %s\n", float64(time.Since(start).Microseconds())/1000, id, data)
			data = ""
		}
	}
	fmt.Fprintf(out, "stream ended after %.0f ms (last id %s)\n", float64(time.Since(start).Microseconds())/1000, id)
	return sc.Err()
}
