package main

// A minimal DNS message encoder and decoder (RFC 1035, plus EDNS from RFC 6891),
// written with the standard library only so every byte is visible. It packs
// queries and unpacks the record types the course uses; other types are kept
// as raw data and printed by number.

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Record types and classes the tool knows by name.
const (
	TypeA      uint16 = 1
	TypeNS     uint16 = 2
	TypeCNAME  uint16 = 5
	TypeSOA    uint16 = 6
	TypePTR    uint16 = 12
	TypeMX     uint16 = 15
	TypeTXT    uint16 = 16
	TypeAAAA   uint16 = 28
	TypeSRV    uint16 = 33
	TypeOPT    uint16 = 41
	TypeDS     uint16 = 43
	TypeRRSIG  uint16 = 46
	TypeDNSKEY uint16 = 48
	TypeCAA    uint16 = 257
	ClassIN    uint16 = 1
)

var typeNames = map[uint16]string{
	TypeA: "A", TypeNS: "NS", TypeCNAME: "CNAME", TypeSOA: "SOA", TypePTR: "PTR", TypeMX: "MX",
	TypeTXT: "TXT", TypeAAAA: "AAAA", TypeSRV: "SRV", TypeOPT: "OPT", TypeDS: "DS",
	TypeRRSIG: "RRSIG", TypeDNSKEY: "DNSKEY", TypeCAA: "CAA",
}

// TypeName prints a type as dig does: its mnemonic, or TYPEnnn.
func TypeName(t uint16) string {
	if n, ok := typeNames[t]; ok {
		return n
	}
	return "TYPE" + strconv.Itoa(int(t))
}

// ParseType accepts a mnemonic or TYPEnnn.
func ParseType(s string) (uint16, error) {
	s = strings.ToUpper(s)
	for t, n := range typeNames {
		if n == s {
			return t, nil
		}
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "TYPE")); err == nil && n > 0 && n < 65536 {
		return uint16(n), nil
	}
	return 0, fmt.Errorf("unknown record type %q", s)
}

// Response codes.
const (
	RcodeNoError  = 0
	RcodeFormErr  = 1
	RcodeServFail = 2
	RcodeNXDomain = 3
	RcodeNotImp   = 4
	RcodeRefused  = 5
)

// RcodeName prints a response code as dig does.
func RcodeName(r int) string {
	switch r {
	case RcodeNoError:
		return "NOERROR"
	case RcodeFormErr:
		return "FORMERR"
	case RcodeServFail:
		return "SERVFAIL"
	case RcodeNXDomain:
		return "NXDOMAIN"
	case RcodeNotImp:
		return "NOTIMP"
	case RcodeRefused:
		return "REFUSED"
	}
	return "RCODE" + strconv.Itoa(r)
}

// Header is the fixed 12-byte message header.
type Header struct {
	ID                         uint16
	QR, AA, TC, RD, RA, AD, CD bool
	Opcode                     int
	Rcode                      int
}

// Flags prints the set header flags in dig's order.
func (h Header) Flags() string {
	var f []string
	for _, x := range []struct {
		on   bool
		name string
	}{{h.QR, "qr"}, {h.AA, "aa"}, {h.TC, "tc"}, {h.RD, "rd"}, {h.RA, "ra"}, {h.AD, "ad"}, {h.CD, "cd"}} {
		if x.on {
			f = append(f, x.name)
		}
	}
	return strings.Join(f, " ")
}

// Question is what was asked.
type Question struct {
	Name  string
	Type  uint16
	Class uint16
}

// RR is one resource record. Data holds the decoded RDATA in presentation
// form; Target is the domain name inside NS, CNAME, PTR, MX and SRV records,
// and Addr the address inside A and AAAA records.
type RR struct {
	Name   string
	Type   uint16
	Class  uint16
	TTL    uint32
	Data   string
	Target string
	Addr   netip.Addr
	SOA    *SOA
	EDNS   *EDNS // only for the OPT pseudo-record
}

// SOA is a decoded Start of Authority record.
type SOA struct {
	MName, RName                           string
	Serial, Refresh, Retry, Expire, MinTTL uint32
}

// EDNS is what an OPT record says: the sender's UDP size, the DO bit, and
// any Extended DNS Error (RFC 8914).
type EDNS struct {
	UDPSize  uint16
	DO       bool
	EDECode  int // -1 if none
	EDEText  string
	ExtRcode int
}

func (r RR) String() string {
	return fmt.Sprintf("%s\t%d\tIN\t%s\t%s", r.Name, r.TTL, TypeName(r.Type), r.Data)
}

// Message is a whole DNS message.
type Message struct {
	Header     Header
	Question   []Question
	Answer     []RR
	Authority  []RR
	Additional []RR
	Size       int // bytes on the wire
}

// EDNS returns the OPT record's contents, or nil.
func (m *Message) EDNS() *EDNS {
	for _, r := range m.Additional {
		if r.Type == TypeOPT {
			return r.EDNS
		}
	}
	return nil
}

// QueryOptions shapes a query.
type QueryOptions struct {
	Recurse bool   // RD: ask the server to recurse for us
	DNSSEC  bool   // DO: ask for DNSSEC records
	CD      bool   // checking disabled: don't validate for us
	UDPSize uint16 // EDNS buffer size; 0 = no EDNS at all
}

// PackQuery builds a query message.
func PackQuery(id uint16, name string, qtype uint16, o QueryOptions) ([]byte, error) {
	b := make([]byte, 12, 512)
	binary.BigEndian.PutUint16(b[0:], id)
	var flags uint16
	if o.Recurse {
		flags |= 1 << 8
	}
	if o.CD {
		flags |= 1 << 4
	}
	binary.BigEndian.PutUint16(b[2:], flags)
	binary.BigEndian.PutUint16(b[4:], 1) // QDCOUNT
	var err error
	if b, err = appendName(b, name); err != nil {
		return nil, err
	}
	b = binary.BigEndian.AppendUint16(b, qtype)
	b = binary.BigEndian.AppendUint16(b, ClassIN)
	if o.UDPSize > 0 {
		binary.BigEndian.PutUint16(b[10:], 1) // ARCOUNT
		b = append(b, 0)                      // root name
		b = binary.BigEndian.AppendUint16(b, TypeOPT)
		b = binary.BigEndian.AppendUint16(b, o.UDPSize) // class = UDP payload size
		var ttl uint32
		if o.DNSSEC {
			ttl |= 1 << 15 // DO bit
		}
		b = binary.BigEndian.AppendUint32(b, ttl)
		b = binary.BigEndian.AppendUint16(b, 0) // no options
	}
	return b, nil
}

// Canonical lowercases a name and ends it with a dot.
func Canonical(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if !strings.HasSuffix(name, ".") {
		name += "."
	}
	return name
}

func appendName(b []byte, name string) ([]byte, error) {
	name = Canonical(name)
	if name == "." {
		return append(b, 0), nil
	}
	if len(name) > 254 {
		return nil, errors.New("name longer than 255 bytes")
	}
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("bad label in %q", name)
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0), nil
}

var errShort = errors.New("message truncated")

// readName decodes a possibly compressed name starting at off and returns it
// with the offset just past it in the original position.
func readName(msg []byte, off int) (string, int, error) {
	var labels []string
	end := -1
	for jumps := 0; ; {
		if off >= len(msg) {
			return "", 0, errShort
		}
		l := int(msg[off])
		switch {
		case l == 0:
			if end < 0 {
				end = off + 1
			}
			if len(labels) == 0 {
				return ".", end, nil
			}
			return strings.ToLower(strings.Join(labels, ".")) + ".", end, nil
		case l&0xC0 == 0xC0: // compression pointer (RFC 1035 4.1.4)
			if off+1 >= len(msg) {
				return "", 0, errShort
			}
			if end < 0 {
				end = off + 2
			}
			if jumps++; jumps > 64 {
				return "", 0, errors.New("compression loop")
			}
			off = int(binary.BigEndian.Uint16(msg[off:]) & 0x3FFF)
		default:
			if off+1+l > len(msg) {
				return "", 0, errShort
			}
			labels = append(labels, string(msg[off+1:off+1+l]))
			off += 1 + l
		}
	}
}

// Unpack decodes a message.
func Unpack(msg []byte) (*Message, error) {
	if len(msg) < 12 {
		return nil, errShort
	}
	m := &Message{Size: len(msg)}
	f := binary.BigEndian.Uint16(msg[2:])
	m.Header = Header{
		ID: binary.BigEndian.Uint16(msg), QR: f&(1<<15) != 0, Opcode: int(f>>11) & 0xF,
		AA: f&(1<<10) != 0, TC: f&(1<<9) != 0, RD: f&(1<<8) != 0, RA: f&(1<<7) != 0,
		AD: f&(1<<5) != 0, CD: f&(1<<4) != 0, Rcode: int(f & 0xF),
	}
	counts := []int{int(binary.BigEndian.Uint16(msg[4:])), int(binary.BigEndian.Uint16(msg[6:])),
		int(binary.BigEndian.Uint16(msg[8:])), int(binary.BigEndian.Uint16(msg[10:]))}
	off := 12
	for i := 0; i < counts[0]; i++ {
		name, n, err := readName(msg, off)
		if err != nil {
			return nil, err
		}
		if n+4 > len(msg) {
			return nil, errShort
		}
		m.Question = append(m.Question, Question{name, binary.BigEndian.Uint16(msg[n:]), binary.BigEndian.Uint16(msg[n+2:])})
		off = n + 4
	}
	for s, dst := range []*[]RR{&m.Answer, &m.Authority, &m.Additional} {
		for i := 0; i < counts[s+1]; i++ {
			rr, n, err := readRR(msg, off)
			if err != nil {
				if m.Header.TC {
					return m, nil // a truncated answer may stop mid-record
				}
				return nil, err
			}
			*dst = append(*dst, rr)
			off = n
		}
	}
	if e := m.EDNS(); e != nil && e.ExtRcode != 0 {
		m.Header.Rcode |= e.ExtRcode << 4
	}
	return m, nil
}

func readRR(msg []byte, off int) (RR, int, error) {
	name, off, err := readName(msg, off)
	if err != nil {
		return RR{}, 0, err
	}
	if off+10 > len(msg) {
		return RR{}, 0, errShort
	}
	r := RR{Name: name, Type: binary.BigEndian.Uint16(msg[off:]), Class: binary.BigEndian.Uint16(msg[off+2:]),
		TTL: binary.BigEndian.Uint32(msg[off+4:])}
	rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
	start := off + 10
	end := start + rdlen
	if end > len(msg) {
		return RR{}, 0, errShort
	}
	rd := msg[start:end]
	switch r.Type {
	case TypeA, TypeAAAA:
		if a, ok := netip.AddrFromSlice(rd); ok {
			r.Addr = a.Unmap()
			r.Data = r.Addr.String()
		}
	case TypeNS, TypeCNAME, TypePTR:
		if r.Target, _, err = readName(msg, start); err != nil {
			return RR{}, 0, err
		}
		r.Data = r.Target
	case TypeMX:
		if rdlen < 3 {
			return RR{}, 0, errShort
		}
		if r.Target, _, err = readName(msg, start+2); err != nil {
			return RR{}, 0, err
		}
		r.Data = fmt.Sprintf("%d %s", binary.BigEndian.Uint16(rd), r.Target)
	case TypeSRV:
		if rdlen < 7 {
			return RR{}, 0, errShort
		}
		if r.Target, _, err = readName(msg, start+6); err != nil {
			return RR{}, 0, err
		}
		r.Data = fmt.Sprintf("%d %d %d %s", binary.BigEndian.Uint16(rd), binary.BigEndian.Uint16(rd[2:]), binary.BigEndian.Uint16(rd[4:]), r.Target)
	case TypeSOA:
		mname, n, err := readName(msg, start)
		if err != nil {
			return RR{}, 0, err
		}
		rname, n, err := readName(msg, n)
		if err != nil {
			return RR{}, 0, err
		}
		if n+20 > end {
			return RR{}, 0, errShort
		}
		u := func(i int) uint32 { return binary.BigEndian.Uint32(msg[n+4*i:]) }
		r.SOA = &SOA{mname, rname, u(0), u(1), u(2), u(3), u(4)}
		r.Data = fmt.Sprintf("%s %s %d %d %d %d %d", mname, rname, u(0), u(1), u(2), u(3), u(4))
	case TypeTXT:
		var parts []string
		for i := 0; i < len(rd); {
			l := int(rd[i])
			if i+1+l > len(rd) {
				return RR{}, 0, errShort
			}
			txt := string(rd[i+1 : i+1+l])
			if len(txt) > 40 {
				txt = fmt.Sprintf("%s…(%d bytes)", txt[:16], l)
			}
			parts = append(parts, strconv.Quote(txt))
			i += 1 + l
		}
		r.Data = strings.Join(parts, " ")
	case TypeDS:
		if rdlen >= 4 {
			r.Data = fmt.Sprintf("%d %d %d %s", binary.BigEndian.Uint16(rd), rd[2], rd[3], strings.ToUpper(hex.EncodeToString(rd[4:])))
		}
	case TypeRRSIG:
		if rdlen >= 18 {
			signer, _, _ := readName(msg, start+18)
			r.Data = fmt.Sprintf("%s alg %d labels %d ttl %d expires %d inception %d tag %d %s …",
				TypeName(binary.BigEndian.Uint16(rd)), rd[2], rd[3], binary.BigEndian.Uint32(rd[4:]),
				binary.BigEndian.Uint32(rd[8:]), binary.BigEndian.Uint32(rd[12:]), binary.BigEndian.Uint16(rd[16:]), signer)
		}
	case TypeDNSKEY:
		if rdlen >= 4 {
			r.Data = fmt.Sprintf("%d %d %d (%d-byte key)", binary.BigEndian.Uint16(rd), rd[2], rd[3], rdlen-4)
		}
	case TypeOPT:
		e := &EDNS{UDPSize: r.Class, DO: r.TTL&(1<<15) != 0, EDECode: -1, ExtRcode: int(r.TTL >> 24)}
		for i := 0; i+4 <= len(rd); {
			code, l := binary.BigEndian.Uint16(rd[i:]), int(binary.BigEndian.Uint16(rd[i+2:]))
			if i+4+l > len(rd) {
				break
			}
			if code == 15 && l >= 2 { // Extended DNS Error
				e.EDECode = int(binary.BigEndian.Uint16(rd[i+4:]))
				e.EDEText = string(rd[i+6 : i+4+l])
			}
			i += 4 + l
		}
		r.EDNS = e
		r.Data = fmt.Sprintf("udp %d", e.UDPSize)
	default:
		r.Data = fmt.Sprintf("\\# %d %s", rdlen, hex.EncodeToString(rd))
	}
	return r, end, nil
}

// EDEName names the common Extended DNS Error codes (RFC 8914).
func EDEName(code int) string {
	names := map[int]string{0: "Other", 1: "Unsupported DNSKEY Algorithm", 3: "Stale Answer", 4: "Forged Answer",
		5: "DNSSEC Indeterminate", 6: "DNSSEC Bogus", 7: "Signature Expired", 8: "Signature Not Yet Valid",
		9: "DNSKEY Missing", 10: "RRSIGs Missing", 15: "Blocked", 18: "Prohibited", 20: "Not Authoritative",
		22: "No Reachable Authority", 23: "Network Error"}
	if n, ok := names[code]; ok {
		return n
	}
	return "code " + strconv.Itoa(code)
}
