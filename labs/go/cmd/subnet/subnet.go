package main

import (
	"fmt"
	"math/big"
	"net/netip"
	"strings"
)

// Info describes one prefix: its boundaries, its size, and what kind of
// address space it is in.
type Info struct {
	Input     string   `json:"input"`
	Prefix    string   `json:"prefix"`
	Host      string   `json:"host,omitempty"` // set when the input had host bits, e.g. 10.0.1.50/24
	Mask      string   `json:"mask,omitempty"` // IPv4 only
	Addresses string   `json:"addresses"`      // decimal; IPv6 counts exceed 64 bits
	Network   string   `json:"network"`
	Broadcast string   `json:"broadcast,omitempty"` // IPv4 /0–/30 only
	First     string   `json:"first_usable"`
	Last      string   `json:"last_usable"`
	Usable    string   `json:"usable"`
	Kind      string   `json:"kind"`
	Notes     []string `json:"notes,omitempty"`
	Cloud     *Cloud   `json:"cloud,omitempty"`
}

// Cloud is the same subnet after a provider's reserved addresses.
type Cloud struct {
	Key      string   `json:"key"`
	Provider string   `json:"provider"`
	Reserved []string `json:"reserved"`
	Usable   string   `json:"usable"`
	First    string   `json:"first_usable"`
	Error    string   `json:"error,omitempty"`
}

// Analyze parses "a.b.c.d/n" (or an IPv6 prefix) and computes its properties.
// A bare address is treated as a /32 or /128.
func Analyze(input, provider string) (*Info, error) {
	p, err := parse(input)
	if err != nil {
		return nil, err
	}
	addr := p.Addr()
	net := p.Masked()
	info := &Info{Input: input, Prefix: net.String(), Network: net.Addr().String(), Kind: Classify(addr)}
	if addr != net.Addr() {
		info.Host = addr.String()
	}
	bits := addr.BitLen()
	hostBits := bits - p.Bits()
	total := new(big.Int).Lsh(big.NewInt(1), uint(hostBits))
	info.Addresses = total.String()
	last := lastAddr(net)

	if addr.Is4() {
		info.Mask = maskString(p.Bits())
		switch {
		case p.Bits() == 32:
			info.First, info.Last, info.Usable = info.Network, info.Network, "1"
			info.Notes = append(info.Notes, "a /32 is a single address: a host route, loopback, or virtual IP")
		case p.Bits() == 31:
			info.First, info.Last, info.Usable = info.Network, last.String(), "2"
			info.Notes = append(info.Notes, "a /31 has no network or broadcast address; both are usable on a point-to-point link (RFC 3021)")
		default:
			info.Broadcast = last.String()
			info.First, info.Last = net.Addr().Next().String(), last.Prev().String()
			info.Usable = new(big.Int).Sub(total, big.NewInt(2)).String()
		}
	} else {
		// IPv6 has no broadcast address; every address in the prefix is usable
		// (the all-zeros one is the subnet-router anycast address).
		info.First, info.Last, info.Usable = info.Network, last.String(), info.Addresses
		if p.Bits() != 64 && p.Bits() < 127 {
			info.Notes = append(info.Notes, fmt.Sprintf("IPv6 subnets are normally /64; SLAAC does not work on a /%d", p.Bits()))
		}
	}
	if provider != "" {
		c, err := cloud(provider, net)
		if err != nil {
			return nil, err
		}
		info.Cloud = c
	}
	return info, nil
}

func parse(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not a prefix like 10.0.1.0/24 or 2001:db8::/64", s)
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address", s)
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 0x80 >> (i % 8)
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

func maskString(bits int) string {
	m := uint32(0)
	if bits > 0 {
		m = ^uint32(0) << (32 - bits)
	}
	return fmt.Sprintf("%d.%d.%d.%d", m>>24, m>>16&0xff, m>>8&0xff, m&0xff)
}

// Contains reports whether addr is inside the prefix and explains what that
// means for forwarding.
func Contains(prefix, addr string) (bool, string, error) {
	p, err := parse(prefix)
	if err != nil {
		return false, "", err
	}
	a, err := netip.ParseAddr(addr)
	if err != nil {
		return false, "", fmt.Errorf("%q is not an IP address", addr)
	}
	net := p.Masked()
	if a.BitLen() != net.Addr().BitLen() {
		return false, fmt.Sprintf("%s is a different address family from %s", a, net), nil
	}
	if net.Contains(a) {
		return true, fmt.Sprintf("%s is inside %s: on-link, reached directly (ARP/NDP, no router)", a, net), nil
	}
	return false, fmt.Sprintf("%s is outside %s: sent to a router (a route or the default gateway)", a, net), nil
}

// provider reservation rules, as documented by each provider.
type rule struct {
	name          string
	first         int  // addresses reserved at the start
	secondToLast  bool // Google Cloud reserves the second-to-last address
	minBits       int  // smallest subnet (longest prefix)
	maxBits       int  // largest subnet (shortest prefix)
	documentation string
}

var providers = map[string]rule{
	"aws":   {"AWS", 4, false, 28, 16, "https://docs.aws.amazon.com/vpc/latest/userguide/subnet-sizing.html"},
	"azure": {"Azure", 4, false, 29, 0, "https://learn.microsoft.com/azure/virtual-network/virtual-networks-faq"},
	"gcp":   {"Google Cloud", 2, true, 29, 4, "https://cloud.google.com/vpc/docs/subnets"},
}

func cloud(name string, net netip.Prefix) (*Cloud, error) {
	r, ok := providers[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("unknown provider %q (use aws, azure, or gcp)", name)
	}
	c := &Cloud{Key: strings.ToLower(name), Provider: r.name}
	if !net.Addr().Is4() {
		c.Error = "provider reservations here cover IPv4 subnets only"
		return c, nil
	}
	if net.Bits() > r.minBits || net.Bits() < r.maxBits {
		c.Error = fmt.Sprintf("%s IPv4 subnets must be /%d or larger", r.name, r.minBits)
		if r.maxBits > 0 {
			c.Error = fmt.Sprintf("%s IPv4 subnets must be between /%d and /%d", r.name, r.maxBits, r.minBits)
		}
		c.Error += " (" + r.documentation + ")"
		return c, nil
	}
	a := net.Addr()
	for i := 0; i < r.first; i++ {
		c.Reserved = append(c.Reserved, a.String())
		a = a.Next()
	}
	c.First = a.String()
	last := lastAddr(net)
	if r.secondToLast {
		c.Reserved = append(c.Reserved, last.Prev().String())
	}
	c.Reserved = append(c.Reserved, last.String())
	total := 1 << (32 - net.Bits())
	c.Usable = fmt.Sprint(total - len(c.Reserved))
	return c, nil
}

type special struct {
	prefix netip.Prefix
	kind   string
}

// The special-purpose ranges a support engineer meets most often, longest
// prefixes first so the most specific name wins.
var specials = func() []special {
	list := []struct{ p, k string }{
		{"169.254.169.254/32", "cloud instance metadata endpoint (link-local)"},
		{"fd00:ec2::254/128", "AWS instance metadata endpoint (IPv6)"},
		{"255.255.255.255/32", "limited broadcast (RFC 919)"},
		{"192.0.2.0/24", "documentation (RFC 5737), never real"},
		{"198.51.100.0/24", "documentation (RFC 5737), never real"},
		{"203.0.113.0/24", "documentation (RFC 5737), never real"},
		{"198.18.0.0/15", "benchmarking (RFC 2544), used by this course's labs"},
		{"100.64.0.0/10", "shared address space for carrier-grade NAT (RFC 6598); also Tailscale"},
		{"169.254.0.0/16", "IPv4 link-local (RFC 3927): no DHCP lease, or a local service"},
		{"127.0.0.0/8", "loopback, never leaves the host"},
		{"10.0.0.0/8", "private (RFC 1918)"},
		{"172.16.0.0/12", "private (RFC 1918)"},
		{"192.168.0.0/16", "private (RFC 1918)"},
		{"0.0.0.0/8", "\"this network\": unspecified source, default route, or bind-all"},
		{"224.0.0.0/4", "multicast"},
		{"240.0.0.0/4", "reserved (former class E)"},
		{"::1/128", "IPv6 loopback"},
		{"::/128", "IPv6 unspecified address"},
		{"::ffff:0:0/96", "IPv4-mapped IPv6: an IPv4 peer seen through an IPv6 socket"},
		{"64:ff9b::/96", "NAT64 well-known prefix: last 32 bits are an IPv4 address"},
		{"2001:db8::/32", "IPv6 documentation (RFC 3849), never real"},
		{"fe80::/10", "IPv6 link-local: one link only; needs %interface to use"},
		{"fc00::/7", "IPv6 unique local address (ULA), private like RFC 1918"},
		{"ff00::/8", "IPv6 multicast"},
		{"2000::/3", "IPv6 global unicast"},
	}
	out := make([]special, 0, len(list))
	for _, e := range list {
		out = append(out, special{netip.MustParsePrefix(e.p), e.k})
	}
	return out
}()

// Classify names the special-purpose range an address is in, or "public".
func Classify(a netip.Addr) string {
	a = a.WithZone("")
	best := -1
	kind := ""
	for _, s := range specials {
		if s.prefix.Contains(a) && s.prefix.Bits() > best {
			best, kind = s.prefix.Bits(), s.kind
		}
	}
	if kind != "" {
		return kind
	}
	if a.Is4() {
		return "public IPv4 (globally routable unless the IANA special-purpose registry says otherwise)"
	}
	return "IPv6 outside the common special ranges; check the IANA registry"
}
