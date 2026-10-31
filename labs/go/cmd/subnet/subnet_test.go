package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestAnalyzeIPv4(t *testing.T) {
	tests := []struct {
		in                                      string
		network, broadcast, first, last, usable string
	}{
		{"192.168.1.0/24", "192.168.1.0", "192.168.1.255", "192.168.1.1", "192.168.1.254", "254"},
		{"10.0.48.0/20", "10.0.48.0", "10.0.63.255", "10.0.48.1", "10.0.63.254", "4094"},
		{"10.0.1.50/24", "10.0.1.0", "10.0.1.255", "10.0.1.1", "10.0.1.254", "254"},
		{"172.16.0.0/28", "172.16.0.0", "172.16.0.15", "172.16.0.1", "172.16.0.14", "14"},
		{"10.0.100.0/30", "10.0.100.0", "10.0.100.3", "10.0.100.1", "10.0.100.2", "2"},
		{"10.0.100.0/31", "10.0.100.0", "", "10.0.100.0", "10.0.100.1", "2"},
		{"10.0.100.7", "10.0.100.7", "", "10.0.100.7", "10.0.100.7", "1"},
	}
	for _, tt := range tests {
		i, err := Analyze(tt.in, "")
		if err != nil {
			t.Fatal(err)
		}
		got := []string{i.Network, i.Broadcast, i.First, i.Last, i.Usable}
		want := []string{tt.network, tt.broadcast, tt.first, tt.last, tt.usable}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("%s: got %v, want %v", tt.in, got, want)
		}
	}
	i, _ := Analyze("10.0.1.50/24", "")
	if i.Host != "10.0.1.50" || i.Mask != "255.255.255.0" || i.Prefix != "10.0.1.0/24" {
		t.Errorf("host/mask/prefix: %+v", i)
	}
}

func TestAnalyzeIPv6(t *testing.T) {
	i, err := Analyze("fd42:1::99/64", "")
	if err != nil {
		t.Fatal(err)
	}
	if i.Network != "fd42:1::" || i.Last != "fd42:1::ffff:ffff:ffff:ffff" || i.Addresses != "18446744073709551616" || i.Broadcast != "" {
		t.Errorf("%+v", i)
	}
	if i, _ := Analyze("2001:db8::/56", ""); len(i.Notes) == 0 {
		t.Error("a /56 subnet should carry the /64 note")
	}
}

func TestCloudReservations(t *testing.T) {
	tests := []struct {
		provider, prefix, usable, first string
		reserved                        int
	}{
		{"aws", "10.0.20.0/24", "251", "10.0.20.4", 5},
		{"azure", "10.0.20.0/24", "251", "10.0.20.4", 5},
		{"gcp", "10.0.20.0/24", "252", "10.0.20.2", 4},
		{"aws", "10.0.20.0/28", "11", "10.0.20.4", 5},
		{"gcp", "10.0.20.0/29", "4", "10.0.20.2", 4},
	}
	for _, tt := range tests {
		i, err := Analyze(tt.prefix, tt.provider)
		if err != nil {
			t.Fatal(err)
		}
		c := i.Cloud
		if c.Error != "" || c.Usable != tt.usable || c.First != tt.first || len(c.Reserved) != tt.reserved {
			t.Errorf("%s %s: %+v", tt.provider, tt.prefix, c)
		}
	}
	gcp, _ := Analyze("10.1.2.0/24", "gcp")
	if got := strings.Join(gcp.Cloud.Reserved, " "); got != "10.1.2.0 10.1.2.1 10.1.2.254 10.1.2.255" {
		t.Errorf("gcp reserved %s", got)
	}
	for _, bad := range []struct{ p, prefix string }{{"aws", "10.0.0.0/29"}, {"aws", "10.0.0.0/15"}, {"azure", "10.0.0.0/30"}} {
		if i, _ := Analyze(bad.prefix, bad.p); i.Cloud.Error == "" {
			t.Errorf("%s %s should be rejected", bad.p, bad.prefix)
		}
	}
	if _, err := Analyze("10.0.0.0/24", "oracle"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestClassify(t *testing.T) {
	tests := map[string]string{
		"10.1.2.3":         "private",
		"172.31.255.1":     "private",
		"172.32.0.1":       "public",
		"169.254.169.254":  "metadata",
		"169.254.10.1":     "link-local",
		"100.100.1.1":      "carrier-grade NAT",
		"198.18.5.1":       "benchmarking",
		"203.0.113.80":     "documentation",
		"8.8.8.8":          "public",
		"fe80::1":          "link-local",
		"fd42:1::1":        "unique local",
		"::ffff:10.0.1.20": "IPv4-mapped",
		"2001:db8::1":      "documentation",
		"2606:4700::1111":  "global unicast",
	}
	for in, want := range tests {
		if got := Classify(netip.MustParseAddr(in)); !strings.Contains(got, want) {
			t.Errorf("%s: %q does not mention %q", in, got, want)
		}
	}
}

func TestContains(t *testing.T) {
	if in, msg, _ := Contains("10.0.1.50/24", "10.0.1.200"); !in || !strings.Contains(msg, "on-link") {
		t.Error(msg)
	}
	if in, msg, _ := Contains("10.0.1.50/24", "10.0.2.50"); in || !strings.Contains(msg, "router") {
		t.Error(msg)
	}
	if in, _, _ := Contains("10.0.1.0/24", "fd42::1"); in {
		t.Error("cross-family address reported inside")
	}
}

func TestRejectsGarbage(t *testing.T) {
	for _, s := range []string{"10.0.0.0/33", "10.0.0", "hello/24"} {
		if _, err := Analyze(s, ""); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}
