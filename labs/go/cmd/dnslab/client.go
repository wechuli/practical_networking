package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"time"
)

// Exchange is one query and its answer, with how it travelled.
type Exchange struct {
	Server  string
	TCP     bool // the answer came over TCP
	Retried bool // UDP came back truncated, so the query was repeated over TCP
	RTT     time.Duration
	Msg     *Message
}

// Ask sends one query to server (host or host:port). Over UDP it checks the
// reply's ID and question, and repeats the query over TCP when the reply is
// truncated, as RFC 7766 requires.
func Ask(ctx context.Context, server, name string, qtype uint16, o QueryOptions, tcp bool, timeout time.Duration) (*Exchange, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	id := uint16(rand.N(65536))
	q, err := PackQuery(id, name, qtype, o)
	if err != nil {
		return nil, err
	}
	ex := &Exchange{Server: server}
	start := time.Now()
	var raw []byte
	if !tcp {
		raw, err = udpExchange(ctx, server, q, id, timeout)
		if err != nil {
			return nil, err
		}
		m, err := Unpack(raw)
		if err != nil {
			return nil, err
		}
		if !m.Header.TC {
			ex.RTT, ex.Msg = time.Since(start), m
			return ex, nil
		}
		ex.Retried = true
	}
	ex.TCP = true
	raw, err = tcpExchange(ctx, server, q, timeout)
	if err != nil {
		return nil, err
	}
	m, err := Unpack(raw)
	if err != nil {
		return nil, err
	}
	if m.Header.ID != id {
		return nil, errors.New("reply ID does not match the query")
	}
	ex.RTT, ex.Msg = time.Since(start), m
	return ex, nil
}

func udpExchange(ctx context.Context, server string, q []byte, id uint16, timeout time.Duration) ([]byte, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	for {
		n, err := c.Read(buf)
		if err != nil {
			return nil, err
		}
		// Ignore anything that is not the answer to this query (RFC 5452):
		// a reply with another ID is a stray or a spoofing attempt.
		if n >= 12 && binary.BigEndian.Uint16(buf) == id {
			return append([]byte(nil), buf[:n]...), nil
		}
	}
}

func tcpExchange(ctx context.Context, server string, q []byte, timeout time.Duration) ([]byte, error) {
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	// Over TCP every message is preceded by its length (RFC 1035 4.2.2).
	if _, err := c.Write(binary.BigEndian.AppendUint16(nil, uint16(len(q)))); err != nil {
		return nil, err
	}
	if _, err := c.Write(q); err != nil {
		return nil, err
	}
	var l [2]byte
	if _, err := io.ReadFull(c, l[:]); err != nil {
		return nil, err
	}
	buf := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(c, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Describe classifies a failed exchange the way an operator would.
func Describe(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "no reply (timeout)"
	case err != nil && isRefused(err):
		return "ICMP port unreachable: nothing listens on port 53"
	case err != nil:
		return err.Error()
	}
	return ""
}

func isRefused(err error) bool {
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Err != nil && errors.Is(oe.Err, errConnRefused)
}

// Summary is a one-line description of what a response says.
func Summary(m *Message) string {
	var s string
	switch {
	case m.Header.Rcode == RcodeNXDomain:
		s = "NXDOMAIN: the name does not exist"
	case m.Header.Rcode != RcodeNoError:
		s = RcodeName(m.Header.Rcode)
	case len(m.Answer) > 0:
		s = fmt.Sprintf("answer (%d records)", len(m.Answer))
	case isReferral(m):
		s = "referral to " + m.Authority[0].Name
	default:
		s = "NODATA: the name exists but has no records of this type"
	}
	if e := m.EDNS(); e != nil && e.EDECode >= 0 {
		s += fmt.Sprintf(" — EDE %d (%s)", e.EDECode, EDEName(e.EDECode))
		if e.EDEText != "" {
			s += ": " + e.EDEText
		}
	}
	return s
}

func isReferral(m *Message) bool {
	if m.Header.Rcode != RcodeNoError || len(m.Answer) > 0 || m.Header.AA {
		return false
	}
	for _, r := range m.Authority {
		if r.Type == TypeNS {
			return true
		}
	}
	return false
}

// NegativeTTL is how long a resolver may cache an NXDOMAIN or NODATA answer:
// the smaller of the SOA record's own TTL and its MINIMUM field (RFC 2308).
func NegativeTTL(m *Message) (ttl uint32, soa *RR) {
	for i, r := range m.Authority {
		if r.Type == TypeSOA && r.SOA != nil {
			ttl = r.TTL
			if r.SOA.MinTTL < ttl {
				ttl = r.SOA.MinTTL
			}
			return ttl, &m.Authority[i]
		}
	}
	return 0, nil
}
