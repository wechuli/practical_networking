package main

import (
	"encoding/binary"
	"net/netip"
	"strings"
	"testing"
)

// icmpError builds an ICMP error message (from the ICMP header on) quoting a
// probe, as a router would send it.
func icmpError(typ, code uint8, mtu uint16, proto uint8, ttl uint8, src, dst string, sport, dport uint16, ihl int) []byte {
	msg := make([]byte, 8, 8+ihl+8)
	msg[0], msg[1] = typ, code
	binary.BigEndian.PutUint16(msg[6:8], mtu)
	q := make([]byte, ihl+8)
	q[0] = 0x40 | byte(ihl/4)
	q[8], q[9] = ttl, proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(q[12:16], s[:])
	copy(q[16:20], d[:])
	binary.BigEndian.PutUint16(q[ihl:], sport)
	binary.BigEndian.PutUint16(q[ihl+2:], dport)
	return append(msg, q...)
}

func TestParseTimeExceeded(t *testing.T) {
	r, err := ParseError(icmpError(11, 0, 0, 17, 1, "198.18.7.1", "198.19.7.2", 51067, 33434, 20))
	if err != nil {
		t.Fatal(err)
	}
	if r.QuotedSrc.String() != "198.18.7.1" || r.QuotedDst.String() != "198.19.7.2" || r.SrcPort != 51067 ||
		r.DstPort != 33434 || r.QuotedTTL != 1 || r.Proto != 17 || r.Mark() != "" ||
		!strings.Contains(r.Meaning(), "TTL reached 0") {
		t.Errorf("%+v %q", r, r.Meaning())
	}
}

func TestParseQuotedHeaderWithOptions(t *testing.T) {
	r, err := ParseError(icmpError(11, 0, 0, 6, 1, "10.0.0.1", "10.0.0.2", 40000, 443, 24))
	if err != nil || r.SrcPort != 40000 || r.DstPort != 443 {
		t.Fatalf("IHL 6 quote: %+v, %v", r, err)
	}
}

func TestMarksAndMeanings(t *testing.T) {
	tests := []struct {
		code       uint8
		mtu        uint16
		mark, mean string
	}{
		{0, 0, "!N", "no route"},
		{1, 0, "!H", "could not ARP"},
		{3, 0, "", "destination answered"},
		{4, 1400, "!F-1400", "MTU 1400"},
		{10, 0, "!X", "firewall"},
		{13, 0, "!X", "firewall"},
	}
	for _, tt := range tests {
		r, err := ParseError(icmpError(3, tt.code, tt.mtu, 17, 1, "10.0.0.1", "10.0.0.2", 1, 2, 20))
		if err != nil {
			t.Fatal(err)
		}
		if r.Mark() != tt.mark || !strings.Contains(r.Meaning(), tt.mean) {
			t.Errorf("code %d: mark %q meaning %q", tt.code, r.Mark(), r.Meaning())
		}
	}
}

func TestRejectsOtherMessages(t *testing.T) {
	echo := make([]byte, 64)
	if _, err := ParseError(echo); err != errNotError {
		t.Errorf("echo reply: %v", err)
	}
	short := icmpError(11, 0, 0, 17, 1, "10.0.0.1", "10.0.0.2", 1, 2, 20)[:20]
	if _, err := ParseError(short); err == nil {
		t.Error("truncated quote accepted")
	}
}

func TestDoneAndFormat(t *testing.T) {
	mid := Hop{TTL: 2, Probes: []Probe{{Kind: "time-exceeded", From: "198.18.8.2", RTTms: 0.05, Meaning: "time exceeded: TTL reached 0 at this router"}, {Kind: "timeout"}}}
	end := Hop{TTL: 3, Probes: []Probe{{Kind: "port-unreachable", From: "198.19.7.2", RTTms: 0.04}}}
	if mid.Done() || !end.Done() || !end.Reached() || (Hop{Probes: []Probe{{Kind: "unreachable"}}}).Reached() {
		t.Error("Done")
	}
	if got := FormatHop(mid); !strings.HasPrefix(got, " 2  198.18.8.2  0.050 ms *") {
		t.Errorf("format: %q", got)
	}
}
