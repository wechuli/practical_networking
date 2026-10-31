package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
)

// Reply is what an ICMP error message tells us about the probe it quotes.
type Reply struct {
	Type, Code uint8
	MTU        int        // for "fragmentation needed"
	Proto      uint8      // protocol of the quoted packet (6 TCP, 17 UDP)
	QuotedSrc  netip.Addr // the probe's source address
	QuotedDst  netip.Addr // the probe's destination address
	QuotedTTL  uint8      // the probe's TTL when it reached the sender of the error
	SrcPort    uint16
	DstPort    uint16
}

// ICMP types and codes this tool understands (RFC 792).
const (
	typeEchoReply      = 0
	typeUnreachable    = 3
	typeTimeExceeded   = 11
	codeNetUnreach     = 0
	codeHostUnreach    = 1
	codeProtoUnreach   = 2
	codePortUnreach    = 3
	codeFragNeeded     = 4
	codeNetProhibited  = 9
	codeHostProhibited = 10
	codeAdminProhibit  = 13
)

var errNotError = errors.New("not an ICMP error message")

// ParseError decodes an ICMPv4 Destination Unreachable or Time Exceeded
// message: 8 bytes of ICMP header, then the quoted IP header and at least the
// first 8 bytes of the quoted payload, which hold the TCP/UDP ports.
// msg starts at the ICMP header (a raw ip4:icmp socket delivers it that way).
func ParseError(msg []byte) (Reply, error) {
	var r Reply
	if len(msg) < 8 {
		return r, fmt.Errorf("ICMP message too short: %d bytes", len(msg))
	}
	r.Type, r.Code = msg[0], msg[1]
	if r.Type != typeUnreachable && r.Type != typeTimeExceeded {
		return r, errNotError
	}
	if r.Type == typeUnreachable && r.Code == codeFragNeeded {
		r.MTU = int(binary.BigEndian.Uint16(msg[6:8]))
	}
	q := msg[8:]
	if len(q) < 20 || q[0]>>4 != 4 {
		return r, fmt.Errorf("quoted packet is not IPv4")
	}
	ihl := int(q[0]&0x0f) * 4
	if ihl < 20 || len(q) < ihl+8 {
		return r, fmt.Errorf("quoted packet truncated: %d bytes, header %d", len(q), ihl)
	}
	r.QuotedTTL = q[8]
	r.Proto = q[9]
	r.QuotedSrc = netip.AddrFrom4([4]byte(q[12:16]))
	r.QuotedDst = netip.AddrFrom4([4]byte(q[16:20]))
	if r.Proto == 6 || r.Proto == 17 {
		r.SrcPort = binary.BigEndian.Uint16(q[ihl : ihl+2])
		r.DstPort = binary.BigEndian.Uint16(q[ihl+2 : ihl+4])
	}
	return r, nil
}

// Mark is the short annotation traceroute prints for an unreachable reply.
func (r Reply) Mark() string {
	if r.Type == typeTimeExceeded {
		return ""
	}
	switch r.Code {
	case codeNetUnreach:
		return "!N"
	case codeHostUnreach:
		return "!H"
	case codeProtoUnreach:
		return "!P"
	case codePortUnreach:
		return "" // the normal end of a UDP trace
	case codeFragNeeded:
		return fmt.Sprintf("!F-%d", r.MTU)
	case codeNetProhibited, codeHostProhibited, codeAdminProhibit:
		return "!X"
	}
	return fmt.Sprintf("!<%d>", r.Code)
}

// Meaning explains a reply in words.
func (r Reply) Meaning() string {
	if r.Type == typeTimeExceeded {
		if r.Code == 1 {
			return "fragment reassembly time exceeded"
		}
		return "time exceeded: TTL reached 0 at this router"
	}
	switch r.Code {
	case codeNetUnreach:
		return "net unreachable: this router has no route"
	case codeHostUnreach:
		return "host unreachable: the last router could not ARP for the host"
	case codeProtoUnreach:
		return "protocol unreachable"
	case codePortUnreach:
		return "port unreachable: the destination answered (nothing listens on that UDP port)"
	case codeFragNeeded:
		return fmt.Sprintf("fragmentation needed: next-hop MTU %d", r.MTU)
	case codeNetProhibited, codeHostProhibited, codeAdminProhibit:
		return "administratively prohibited: a firewall rejected the probe"
	}
	return fmt.Sprintf("destination unreachable, code %d", r.Code)
}
