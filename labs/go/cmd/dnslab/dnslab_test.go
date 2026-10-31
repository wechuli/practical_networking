package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// --- helpers that build responses, so the tests do not trust the code under test

type rr struct {
	name  string
	typ   uint16
	ttl   uint32
	rdata []byte
}

func wire(name string) []byte {
	b, err := appendName(nil, name)
	if err != nil {
		panic(err)
	}
	return b
}

func aRR(name string, ttl uint32, ip string) rr {
	a := netip.MustParseAddr(ip).As4()
	return rr{name, TypeA, ttl, a[:]}
}
func nsRR(name string, ttl uint32, target string) rr { return rr{name, TypeNS, ttl, wire(target)} }
func cnameRR(name string, ttl uint32, target string) rr {
	return rr{name, TypeCNAME, ttl, wire(target)}
}
func soaRR(name string, ttl, minimum uint32) rr {
	d := append(wire("ns."+name), wire("hostmaster."+name)...)
	for _, v := range []uint32{1, 3600, 900, 604800, minimum} {
		d = binary.BigEndian.AppendUint32(d, v)
	}
	return rr{name, TypeSOA, ttl, d}
}

// reply answers query q with the given flags and sections.
func reply(q []byte, aa, tc bool, rcode int, ans, auth, add []rr) []byte {
	m, err := Unpack(q)
	if err != nil {
		panic(err)
	}
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b, m.Header.ID)
	f := uint16(1<<15) | uint16(rcode)
	if aa {
		f |= 1 << 10
	}
	if tc {
		f |= 1 << 9
	}
	binary.BigEndian.PutUint16(b[2:], f)
	binary.BigEndian.PutUint16(b[4:], 1)
	binary.BigEndian.PutUint16(b[6:], uint16(len(ans)))
	binary.BigEndian.PutUint16(b[8:], uint16(len(auth)))
	binary.BigEndian.PutUint16(b[10:], uint16(len(add)))
	b = append(b, wire(m.Question[0].Name)...)
	b = binary.BigEndian.AppendUint16(b, m.Question[0].Type)
	b = binary.BigEndian.AppendUint16(b, ClassIN)
	for _, sec := range [][]rr{ans, auth, add} {
		for _, r := range sec {
			b = append(b, wire(r.name)...)
			b = binary.BigEndian.AppendUint16(b, r.typ)
			b = binary.BigEndian.AppendUint16(b, ClassIN)
			b = binary.BigEndian.AppendUint32(b, r.ttl)
			b = binary.BigEndian.AppendUint16(b, uint16(len(r.rdata)))
			b = append(b, r.rdata...)
		}
	}
	return b
}

// fakeServer answers UDP and TCP on addr with handle.
func fakeServer(t *testing.T, addr string, handle func(q []byte, tcp bool) []byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Skipf("cannot listen on %s: %v", addr, err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		pc.Close()
		t.Skipf("cannot listen on tcp %s: %v", addr, err)
	}
	t.Cleanup(func() { pc.Close(); ln.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if r := handle(append([]byte(nil), buf[:n]...), false); r != nil {
				_, _ = pc.WriteTo(r, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				var l [2]byte
				if _, err := io.ReadFull(c, l[:]); err != nil {
					return
				}
				q := make([]byte, binary.BigEndian.Uint16(l[:]))
				if _, err := io.ReadFull(c, q); err != nil {
					return
				}
				r := handle(q, true)
				_, _ = c.Write(append(binary.BigEndian.AppendUint16(nil, uint16(len(r))), r...))
			}()
		}
	}()
	_, p, _ := net.SplitHostPort(pc.LocalAddr().String())
	_ = port
	return p
}

// --- tests

func TestPackQuery(t *testing.T) {
	b, err := PackQuery(0x1234, "App.Shop.Test", TypeA, QueryOptions{Recurse: true, DNSSEC: true, UDPSize: 1232})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 1}
	if !bytes.Equal(b[:12], want) {
		t.Fatalf("header % x, want % x", b[:12], want)
	}
	if !bytes.Contains(b, []byte("\x03app\x04shop\x04test\x00\x00\x01\x00\x01")) {
		t.Errorf("question not lower-cased label form: % x", b)
	}
	// OPT: root name, type 41, class = 1232, TTL with DO (0x8000)
	if !bytes.HasSuffix(b, []byte{0, 0, 41, 0x04, 0xd0, 0, 0, 0x80, 0, 0, 0}) {
		t.Errorf("OPT record: % x", b[len(b)-11:])
	}
	if _, err := PackQuery(1, strings.Repeat("a", 64)+".test", TypeA, QueryOptions{}); err == nil {
		t.Error("a 64-byte label was accepted")
	}
}

func TestUnpackCompression(t *testing.T) {
	q, _ := PackQuery(7, "www.shop.test", TypeA, QueryOptions{})
	r := reply(q, true, false, 0, []rr{cnameRR("www.shop.test", 10, "edge.shop.test")}, nil, nil)
	// Replace the answer's owner name with a pointer to the question name (offset 12).
	qend := 12 + len(wire("www.shop.test")) + 4
	compressed := append(append(append([]byte(nil), r[:qend]...), 0xC0, 12), r[qend+len(wire("www.shop.test")):]...)
	m, err := Unpack(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if m.Answer[0].Name != "www.shop.test." || m.Answer[0].Target != "edge.shop.test." {
		t.Fatalf("%+v", m.Answer[0])
	}
	loop := append([]byte(nil), compressed...)
	loop[qend], loop[qend+1] = 0xC0, byte(qend) // a pointer to itself
	if _, err := Unpack(loop); err == nil {
		t.Error("a compression loop was accepted")
	}
	if _, err := Unpack(compressed[:20]); err == nil {
		t.Error("a truncated message was accepted")
	}
}

func TestNegativeTTL(t *testing.T) {
	q, _ := PackQuery(1, "new.shop.test", TypeA, QueryOptions{})
	m, err := Unpack(reply(q, true, false, RcodeNXDomain, nil, []rr{soaRR("shop.test", 20, 3600)}, nil))
	if err != nil {
		t.Fatal(err)
	}
	if ttl, soa := NegativeTTL(m); ttl != 20 || soa == nil {
		t.Fatalf("ttl %d", ttl)
	}
	if !strings.HasPrefix(Summary(m), "NXDOMAIN") {
		t.Error(Summary(m))
	}
	m, _ = Unpack(reply(q, true, false, 0, nil, []rr{soaRR("shop.test", 3600, 300)}, nil))
	if ttl, _ := NegativeTTL(m); ttl != 300 || !strings.HasPrefix(Summary(m), "NODATA") {
		t.Fatalf("ttl %d, %s", ttl, Summary(m))
	}
}

func TestAskFallsBackToTCPWhenTruncated(t *testing.T) {
	port := fakeServer(t, "127.0.0.1:0", func(q []byte, tcp bool) []byte {
		if !tcp {
			return reply(q, true, true, 0, nil, nil, nil)
		}
		return reply(q, true, false, 0, []rr{aRR("big.shop.test", 30, "198.18.70.1")}, nil, nil)
	})
	ex, err := Ask(context.Background(), "127.0.0.1:"+port, "big.shop.test", TypeA, QueryOptions{UDPSize: 1232}, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !ex.Retried || !ex.TCP || len(ex.Msg.Answer) != 1 {
		t.Fatalf("%+v", ex)
	}
}

func TestAskIgnoresWrongID(t *testing.T) {
	port := fakeServer(t, "127.0.0.1:0", func(q []byte, tcp bool) []byte {
		r := reply(q, true, false, 0, []rr{aRR("x.test", 1, "192.0.2.1")}, nil, nil)
		r[0] ^= 0xFF // a different transaction ID: a spoofing attempt or a stray
		return r
	})
	_, err := Ask(context.Background(), "127.0.0.1:"+port, "x.test", TypeA, QueryOptions{}, false, 300*time.Millisecond)
	if err == nil || !strings.Contains(Describe(err), "timeout") {
		t.Fatalf("got %v", err)
	}
}

func TestTraceFollowsReferralsAndCNAME(t *testing.T) {
	// root on 127.0.0.1, test. on 127.0.0.2, shop.test. on 127.0.0.3, one shared port.
	port := fakeServer(t, "127.0.0.1:0", func(q []byte, _ bool) []byte {
		return reply(q, false, false, 0, nil, []rr{nsRR("test", 86400, "ns.tld.test")}, []rr{aRR("ns.tld.test", 86400, "127.0.0.2")})
	})
	fakeServer(t, "127.0.0.2:"+port, func(q []byte, _ bool) []byte {
		return reply(q, false, false, 0, nil, []rr{nsRR("shop.test", 3600, "ns.shop.test")}, []rr{aRR("ns.shop.test", 3600, "127.0.0.3")})
	})
	fakeServer(t, "127.0.0.3:"+port, func(q []byte, _ bool) []byte {
		m, _ := Unpack(q)
		switch m.Question[0].Name {
		case "www.shop.test.":
			return reply(q, true, false, 0, []rr{cnameRR("www.shop.test", 10, "edge.shop.test")}, nil, nil)
		case "edge.shop.test.":
			return reply(q, true, false, 0, []rr{aRR("edge.shop.test", 10, "198.18.70.30")}, nil, nil)
		}
		return reply(q, true, false, RcodeNXDomain, nil, []rr{soaRR("shop.test", 20, 3600)}, nil)
	})
	var out bytes.Buffer
	tr := &Tracer{Roots: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Port: port, Timeout: time.Second, Out: &out}
	res, err := tr.Resolve(context.Background(), "www.shop.test", TypeA)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rcode != RcodeNoError || res.Steps != 6 || len(res.Answer) != 2 || res.Answer[1].Data != "198.18.70.30" {
		t.Fatalf("%+v\n%s", res, out.String())
	}
	if !strings.Contains(out.String(), "start again from the root") {
		t.Error(out.String())
	}
	out.Reset()
	tr = &Tracer{Roots: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, Port: port, Timeout: time.Second, Out: &out}
	res, _ = tr.Resolve(context.Background(), "nope.shop.test", TypeA)
	if res.Rcode != RcodeNXDomain || !strings.Contains(out.String(), "cacheable for 20 s") {
		t.Fatalf("%+v\n%s", res, out.String())
	}
}

func TestWatchReportsChanges(t *testing.T) {
	start := time.Now()
	port := fakeServer(t, "127.0.0.1:0", func(q []byte, _ bool) []byte {
		ip := "192.0.2.1"
		if time.Since(start) > 250*time.Millisecond {
			ip = "192.0.2.2"
		}
		return reply(q, true, false, 0, []rr{aRR("app.test", 5, ip)}, nil, nil)
	})
	var out bytes.Buffer
	ch := Watch(context.Background(), WatchOptions{Servers: []string{"127.0.0.1:" + port}, Name: "app.test", Type: TypeA,
		Interval: 100 * time.Millisecond, For: 600 * time.Millisecond}, &out)
	if len(ch) != 1 || ch[0].From != "192.0.2.1" || ch[0].To != "192.0.2.2" {
		t.Fatalf("%+v\n%s", ch, out.String())
	}
}

func TestParseType(t *testing.T) {
	for in, want := range map[string]uint16{"a": TypeA, "AAAA": TypeAAAA, "txt": TypeTXT, "TYPE65": 65} {
		if got, err := ParseType(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	if _, err := ParseType("bogus"); err == nil {
		t.Error("bogus accepted")
	}
}
