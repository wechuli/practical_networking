package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// LoadOptions controls Load.
type LoadOptions struct {
	URL     string
	N, C    int
	Sample  float64 // head sampling probability for new traces
	Spans   string
	Timeout time.Duration
}

// Load sends N requests from C workers, each starting a trace (sampled with
// probability Sample), and reports what the client saw.
func Load(ctx context.Context, o LoadOptions) (string, error) {
	spans, err := OpenSpans(o.Spans)
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: o.Timeout, Transport: &http.Transport{Proxy: nil, MaxIdleConnsPerHost: o.C}}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	var mu sync.Mutex
	var lat []time.Duration
	codes := map[string]int{}
	sampled := 0
	jobs := make(chan bool)
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < o.C; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range jobs {
				tc := NewTrace(s)
				sp := Span{TraceID: tc.TraceID, SpanID: tc.SpanID, Service: "load", Name: "GET " + o.URL, Kind: "client"}
				t0 := time.Now()
				req, _ := http.NewRequestWithContext(ctx, "GET", o.URL, nil)
				req.Header.Set("traceparent", tc.Header())
				resp, err := client.Do(req)
				key := ""
				if err != nil {
					key = "error: " + err.Error()
					sp.Status, sp.Error = "error", err.Error()
				} else {
					_, _ = io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					key = fmt.Sprint(resp.StatusCode)
					sp.HTTPStatus = resp.StatusCode
					if resp.StatusCode >= 500 {
						sp.Status = "error"
					}
				}
				d := time.Since(t0)
				if s {
					spans.End(sp, t0)
				}
				mu.Lock()
				codes[key]++
				lat = append(lat, d)
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < o.N; i++ {
		s := rng.Float64() < o.Sample
		if s {
			sampled++
		}
		jobs <- s
	}
	close(jobs)
	wg.Wait()
	total := time.Since(start)
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	var b strings.Builder
	fmt.Fprintf(&b, "obslab load: %d requests, %d workers, %s (%.0f req/s); traces sampled at the head: %d (%.0f%%)\n",
		o.N, o.C, total.Round(time.Millisecond), float64(o.N)/total.Seconds(), sampled, o.Sample*100)
	fmt.Fprintf(&b, "  client-measured latency: p50 %s  p90 %s  p99 %s  max %s\n", pct(lat, 0.5), pct(lat, 0.9), pct(lat, 0.99), pct(lat, 1))
	var keys []string
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %5d × %s\n", codes[k], k)
	}
	return b.String(), nil
}

func pct(lat []time.Duration, p float64) string {
	if len(lat) == 0 {
		return "-"
	}
	return fmtMS(lat[int(p*float64(len(lat)-1))])
}

func fmtMS(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d.Microseconds())/1000) }

// ReadSpans loads JSON-lines span files.
func ReadSpans(paths []string) ([]Span, error) {
	var out []Span
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var s Span
			if err := json.Unmarshal(sc.Bytes(), &s); err == nil {
				out = append(out, s)
			}
		}
		f.Close()
	}
	return out, nil
}

// Trace groups the spans of one request.
type Trace struct {
	ID    string
	Spans []Span
}

func (t Trace) find(service, kind string) (Span, bool) {
	for _, s := range t.Spans {
		if s.Service == service && s.Kind == kind {
			return s, true
		}
	}
	return Span{}, false
}

func (t Trace) root() Span {
	for _, s := range t.Spans {
		if s.ParentID == "" {
			return s
		}
	}
	return t.Spans[0]
}

func (t Trace) failed() bool {
	for _, s := range t.Spans {
		if s.Status == "error" {
			return true
		}
	}
	return false
}

// Group builds traces and checks parent links.
func Group(spans []Span) ([]Trace, int) {
	by := map[string]*Trace{}
	ids := map[string]bool{}
	for _, s := range spans {
		ids[s.TraceID+"/"+s.SpanID] = true
		if by[s.TraceID] == nil {
			by[s.TraceID] = &Trace{ID: s.TraceID}
		}
		by[s.TraceID].Spans = append(by[s.TraceID].Spans, s)
	}
	orphans := 0
	for _, s := range spans {
		if s.ParentID != "" && !ids[s.TraceID+"/"+s.ParentID] {
			orphans++
		}
	}
	var out []Trace
	for _, t := range by {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].root().DurationMS > out[j].root().DurationMS })
	return out, orphans
}

// Breakdown explains where one trace's time went.
func Breakdown(t Trace) string {
	r := t.root()
	fs, okF := t.find("frontend", "server")
	fc, okC := t.find("frontend", "client")
	bs, okB := t.find("backend", "server")
	var b strings.Builder
	fmt.Fprintf(&b, "trace %s: %s %.1f ms, status %s", t.ID, r.Service, r.DurationMS, statusOf(r))
	if okF && r.Service == "load" {
		fmt.Fprintf(&b, " | client→frontend network %.1f ms", r.DurationMS-fs.DurationMS)
	}
	if okF && okC {
		fmt.Fprintf(&b, " | frontend own work %.1f ms", fs.DurationMS-fc.DurationMS)
	}
	if okC && okB {
		fmt.Fprintf(&b, " | frontend→backend network+queue %.1f ms | backend %.1f ms", fc.DurationMS-bs.DurationMS, bs.DurationMS)
	} else if okC {
		fmt.Fprintf(&b, " | call to backend %.1f ms with NO backend span (%s)", fc.DurationMS, fc.Error)
	}
	return b.String()
}

func statusOf(s Span) string {
	if s.HTTPStatus != 0 {
		return fmt.Sprintf("%d", s.HTTPStatus)
	}
	return s.Status
}

// Analyze summarizes span files: trace counts, broken links, the slowest
// traces with their breakdown, and what a tail-sampling rule would keep.
func Analyze(paths []string, slowest int, tailSlow time.Duration) (string, error) {
	spans, err := ReadSpans(paths)
	if err != nil {
		return "", err
	}
	traces, orphans := Group(spans)
	errs, kept, keptErr := 0, 0, 0
	for _, t := range traces {
		slow := t.root().DurationMS >= float64(tailSlow.Microseconds())/1000
		if t.failed() {
			errs++
		}
		if t.failed() || slow {
			kept++
			if t.failed() {
				keptErr++
			}
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "obslab spans: %d spans in %d traces, %d with an error, %d spans whose parent is missing\n", len(spans), len(traces), errs, orphans)
	if tailSlow > 0 {
		fmt.Fprintf(&b, "  a tail-sampling rule (keep errors and traces ≥ %s) keeps %d of %d traces (%.1f%%), including %d of %d error traces\n",
			tailSlow, kept, len(traces), 100*float64(kept)/float64(max(1, len(traces))), keptErr, errs)
	}
	durs := map[string][]time.Duration{}
	for _, sp := range spans {
		k := sp.Service + " " + sp.Kind
		durs[k] = append(durs[k], time.Duration(sp.DurationMS*float64(time.Millisecond)))
	}
	var ks []string
	for k := range durs {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	for _, k := range ks {
		d := durs[k]
		sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
		fmt.Fprintf(&b, "  %-16s spans: %5d  p50 %s  p90 %s  p99 %s  max %s\n", k, len(d), pct(d, 0.5), pct(d, 0.9), pct(d, 0.99), pct(d, 1))
	}
	for i := 0; i < slowest && i < len(traces); i++ {
		fmt.Fprintf(&b, "  %s\n", Breakdown(traces[i]))
	}
	return b.String(), nil
}

// Flow is one conntrack entry as a flow record.
type Flow struct {
	Proto, State             string
	Src, Dst, Sport, Dport   string
	Packets, Bytes           string
	ReplyPackets, ReplyBytes string
	Unreplied, Assured       bool
}

// ParseFlows reads `conntrack -L -o extended` (with nf_conntrack_acct=1).
func ParseFlows(r io.Reader) []Flow {
	var out []Flow
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 6 || (f[0] != "ipv4" && f[0] != "ipv6") {
			continue
		}
		fl := Flow{Proto: f[2]}
		seen := map[string]int{}
		for _, tok := range f[5:] {
			switch tok {
			case "[UNREPLIED]":
				fl.Unreplied = true
				continue
			case "[ASSURED]":
				fl.Assured = true
				continue
			}
			k, v, ok := strings.Cut(tok, "=")
			if !ok {
				if fl.State == "" && strings.ToUpper(tok) == tok && fl.Src == "" {
					fl.State = tok
				}
				continue
			}
			seen[k]++
			first := seen[k] == 1
			switch {
			case k == "src" && first:
				fl.Src = v
			case k == "dst" && first:
				fl.Dst = v
			case k == "sport" && first:
				fl.Sport = v
			case k == "dport" && first:
				fl.Dport = v
			case k == "packets" && first:
				fl.Packets = v
			case k == "bytes" && first:
				fl.Bytes = v
			case k == "packets":
				fl.ReplyPackets = v
			case k == "bytes":
				fl.ReplyBytes = v
			}
		}
		out = append(out, fl)
	}
	return out
}

// FormatFlows renders flows like a flow log: one line per 5-tuple, packets
// and bytes each way, and whether anything ever came back.
func FormatFlows(fl []Flow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-4s %-21s %-21s %-12s %14s %14s  %s\n", "prot", "source", "destination", "state", "pkts/bytes out", "pkts/bytes in", "verdict")
	for _, f := range fl {
		verdict := "two-way"
		if f.Unreplied {
			verdict = "NO REPLY (dropped, filtered or nothing there)"
		}
		fmt.Fprintf(&b, "%-4s %-21s %-21s %-12s %14s %14s  %s\n", f.Proto, f.Src+":"+f.Sport, f.Dst+":"+f.Dport, f.State,
			f.Packets+"/"+f.Bytes, f.ReplyPackets+"/"+f.ReplyBytes, verdict)
	}
	return b.String()
}

// Sub returns s − prev: what happened between two scrapes, as PromQL's
// increase() does for counters and histogram buckets.
func (s Scrape) Sub(prev Scrape) Scrape {
	out := Scrape{Codes: map[string]float64{}, Sum: s.Sum - prev.Sum, Count: s.Count - prev.Count}
	for c, v := range s.Codes {
		out.Codes[c] = v - prev.Codes[c]
	}
	for _, b := range s.Buckets {
		for _, p := range prev.Buckets {
			if p.LE == b.LE {
				b.Count -= p.Count
			}
		}
		out.Buckets = append(out.Buckets, b)
	}
	return out
}

// Quantiles scrapes a /metrics URL and estimates quantiles; with since, it
// first subtracts an earlier scrape saved with save.
func Quantiles(url string, qs []float64, save, since string) (string, error) {
	resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}).Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if save != "" {
		if err := os.WriteFile(save, raw, 0o644); err != nil {
			return "", err
		}
	}
	s, err := Parse(strings.NewReader(string(raw)))
	if err != nil {
		return "", err
	}
	window := "since start"
	if since != "" {
		f, err := os.Open(since)
		if err != nil {
			return "", err
		}
		prev, err := Parse(f)
		f.Close()
		if err != nil {
			return "", err
		}
		s = s.Sub(prev)
		window = "since " + since
	}
	var b strings.Builder
	var codes []string
	for c := range s.Codes {
		codes = append(codes, c)
	}
	sort.Strings(codes)
	fmt.Fprintf(&b, "obslab metrics %s (%s): %.0f requests", url, window, s.Count)
	for _, c := range codes {
		fmt.Fprintf(&b, ", %s: %.0f", c, s.Codes[c])
	}
	if s.Count > 0 {
		fmt.Fprintf(&b, "; mean %.1fms", s.Sum/s.Count*1000)
	}
	b.WriteString("\n")
	for _, q := range qs {
		fmt.Fprintf(&b, "  histogram_quantile(%g) ≈ %.1fms\n", q, s.Quantile(q)*1000)
	}
	return b.String(), nil
}

// SummarizeLogs counts a service log's requests by status and error text.
func SummarizeLogs(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	type key struct {
		status int
		err    string
	}
	counts := map[key]int{}
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e struct {
			Status int    `json:"status"`
			Error  string `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		n++
		counts[key{e.Status, e.Error}]++
	}
	var keys []key
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return counts[keys[i]] > counts[keys[j]] })
	var b strings.Builder
	fmt.Fprintf(&b, "obslab logs %s: %d lines\n", path, n)
	for _, k := range keys {
		e := k.err
		if e == "" {
			e = "-"
		}
		fmt.Fprintf(&b, "  %5d × status %d  error: %s\n", counts[k], k.status, e)
	}
	return b.String(), nil
}
