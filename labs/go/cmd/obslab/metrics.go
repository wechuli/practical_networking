package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DefaultBuckets are the Prometheus client libraries' default histogram
// buckets, in seconds.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Metrics is a request counter by status code plus a latency histogram,
// exposed in the Prometheus text format.
type Metrics struct {
	mu      sync.Mutex
	service string
	buckets []float64
	counts  []uint64 // per bucket (not cumulative)
	inf     uint64
	sum     float64
	codes   map[int]uint64
}

// NewMetrics creates metrics with the given bucket upper bounds.
func NewMetrics(service string, buckets []float64) *Metrics {
	return &Metrics{service: service, buckets: buckets, counts: make([]uint64, len(buckets)), codes: map[int]uint64{}}
}

// Observe records one request.
func (m *Metrics) Observe(code int, seconds float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.codes[code]++
	m.sum += seconds
	for i, b := range m.buckets {
		if seconds <= b {
			m.counts[i]++
			return
		}
	}
	m.inf++
}

// Expose renders the exposition format.
func (m *Metrics) Expose(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fmt.Fprintf(w, "# HELP http_requests_total Requests served, by status code.\n# TYPE http_requests_total counter\n")
	var codes []int
	var total uint64
	for c := range m.codes {
		codes = append(codes, c)
	}
	sort.Ints(codes)
	for _, c := range codes {
		fmt.Fprintf(w, "http_requests_total{service=%q,code=\"%d\"} %d\n", m.service, c, m.codes[c])
		total += m.codes[c]
	}
	fmt.Fprintf(w, "# HELP http_request_duration_seconds Time to serve a request.\n# TYPE http_request_duration_seconds histogram\n")
	var cum uint64
	for i, b := range m.buckets {
		cum += m.counts[i]
		fmt.Fprintf(w, "http_request_duration_seconds_bucket{service=%q,le=\"%s\"} %d\n", m.service, strconv.FormatFloat(b, 'f', -1, 64), cum)
	}
	fmt.Fprintf(w, "http_request_duration_seconds_bucket{service=%q,le=\"+Inf\"} %d\n", m.service, cum+m.inf)
	fmt.Fprintf(w, "http_request_duration_seconds_sum{service=%q} %g\n", m.service, m.sum)
	fmt.Fprintf(w, "http_request_duration_seconds_count{service=%q} %d\n", m.service, total)
}

// Scrape is what Parse extracts from an exposition.
type Scrape struct {
	Codes   map[string]float64
	Buckets []Bucket // cumulative, sorted by bound, +Inf last
	Sum     float64
	Count   float64
}

// Bucket is one cumulative histogram bucket.
type Bucket struct {
	LE    float64
	Count float64
}

func labelValue(line, name string) string {
	i := strings.Index(line, name+"=\"")
	if i < 0 {
		return ""
	}
	rest := line[i+len(name)+2:]
	return rest[:strings.IndexByte(rest, '"')]
}

// Parse reads the subset of the text format that Metrics writes.
func Parse(r io.Reader) (Scrape, error) {
	s := Scrape{Codes: map[string]float64{}}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		f := strings.Fields(line)
		v, err := strconv.ParseFloat(f[len(f)-1], 64)
		if err != nil {
			return s, fmt.Errorf("bad sample %q", line)
		}
		switch {
		case strings.HasPrefix(line, "http_requests_total{"):
			s.Codes[labelValue(line, "code")] += v
		case strings.HasPrefix(line, "http_request_duration_seconds_bucket{"):
			le := labelValue(line, "le")
			b := math.Inf(1)
			if le != "+Inf" {
				if b, err = strconv.ParseFloat(le, 64); err != nil {
					return s, err
				}
			}
			s.Buckets = append(s.Buckets, Bucket{LE: b, Count: v})
		case strings.HasPrefix(line, "http_request_duration_seconds_sum"):
			s.Sum = v
		case strings.HasPrefix(line, "http_request_duration_seconds_count"):
			s.Count = v
		}
	}
	sort.Slice(s.Buckets, func(i, j int) bool { return s.Buckets[i].LE < s.Buckets[j].LE })
	return s, sc.Err()
}

// Quantile estimates the q-quantile the way PromQL's histogram_quantile
// does: find the bucket holding rank q×count and interpolate linearly
// inside it, assuming observations are spread evenly across the bucket.
func (s Scrape) Quantile(q float64) float64 {
	if len(s.Buckets) == 0 {
		return math.NaN()
	}
	total := s.Buckets[len(s.Buckets)-1].Count
	if total == 0 {
		return math.NaN()
	}
	rank := q * total
	lower, prev := 0.0, 0.0
	for i, b := range s.Buckets {
		if b.Count >= rank {
			if math.IsInf(b.LE, 1) {
				if i == 0 {
					return math.NaN()
				}
				return s.Buckets[i-1].LE // PromQL returns the highest finite bound
			}
			if b.Count == prev {
				return b.LE
			}
			return lower + (b.LE-lower)*(rank-prev)/(b.Count-prev)
		}
		lower, prev = b.LE, b.Count
	}
	return s.Buckets[len(s.Buckets)-1].LE
}
