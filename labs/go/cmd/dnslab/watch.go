package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// WatchOptions controls Watch.
type WatchOptions struct {
	Servers  []string
	Name     string
	Type     uint16
	Interval time.Duration
	For      time.Duration
	Recurse  bool
}

// Change is one moment when a server's answer differed from its previous one.
type Change struct {
	Server string
	At     time.Duration
	From   string
	To     string
}

// answerKey reduces a response to what a client would act on: the rcode
// and the sorted record data (not the TTL, which counts down in caches).
func answerKey(m *Message, qtype uint16) (key string, ttl uint32) {
	if m.Header.Rcode != RcodeNoError {
		t, _ := NegativeTTL(m)
		return RcodeName(m.Header.Rcode), t
	}
	var data []string
	first := true
	for _, r := range m.Answer {
		if r.Type == qtype || r.Type == TypeCNAME {
			data = append(data, r.Data)
			if first || r.TTL < ttl {
				ttl, first = r.TTL, false
			}
		}
	}
	if len(data) == 0 {
		t, _ := NegativeTTL(m)
		return "NODATA", t
	}
	sort.Strings(data)
	return strings.Join(data, ", "), ttl
}

// Watch asks every server the same question every interval and prints a
// line whenever a server's answer changes, so a record change can be followed
// from the authoritative server through each cache.
func Watch(ctx context.Context, o WatchOptions, w io.Writer) []Change {
	last := map[string]string{}
	var changes []Change
	start := time.Now()
	tick := time.NewTicker(o.Interval)
	defer tick.Stop()
	for {
		at := time.Since(start)
		for _, s := range o.Servers {
			ex, err := Ask(ctx, s, o.Name, o.Type, QueryOptions{Recurse: o.Recurse, UDPSize: 1232}, false, 2*time.Second)
			key, ttl := "", uint32(0)
			if err != nil {
				key = Describe(err)
			} else {
				key, ttl = answerKey(ex.Msg, o.Type)
			}
			prev, seen := last[s]
			if !seen || prev != key {
				fmt.Fprintf(w, "%6.1fs  %-15s %-40s ttl %d\n", at.Seconds(), host(s), key, ttl)
				if seen {
					changes = append(changes, Change{s, at, prev, key})
				}
				last[s] = key
			}
		}
		if at >= o.For {
			break
		}
		select {
		case <-ctx.Done():
			return changes
		case <-tick.C:
		}
	}
	fmt.Fprintln(w, "summary:")
	for _, s := range o.Servers {
		var when []string
		for _, c := range changes {
			if c.Server == s {
				when = append(when, fmt.Sprintf("%.1fs (%s → %s)", c.At.Seconds(), c.From, c.To))
			}
		}
		if len(when) == 0 {
			fmt.Fprintf(w, "  %-15s no change: %s\n", host(s), last[s])
			continue
		}
		fmt.Fprintf(w, "  %-15s changed at %s\n", host(s), strings.Join(when, "; "))
	}
	return changes
}

func host(s string) string {
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}
