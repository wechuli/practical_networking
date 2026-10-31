package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"
)

// Options controls a trace.
type Options struct {
	Target   netip.Addr
	Mode     string // "udp" or "tcp"
	Port     int    // UDP: first destination port (incremented per probe unless Fixed); TCP: destination port
	Fixed    bool   // UDP: keep one source and destination port for every probe, so ECMP hashes all probes alike
	MaxHops  int
	Queries  int
	Wait     time.Duration
	FirstTTL int
}

// Probe is the result of one probe.
type Probe struct {
	TTL     int     `json:"ttl"`
	From    string  `json:"from,omitempty"` // empty: no answer before the wait expired
	RTTms   float64 `json:"rtt_ms,omitempty"`
	Kind    string  `json:"kind"` // time-exceeded, unreachable, port-unreachable, syn-ack, rst, timeout
	Mark    string  `json:"mark,omitempty"`
	Meaning string  `json:"meaning,omitempty"`
	Port    int     `json:"port"`
}

// Hop groups the probes sent with one TTL.
type Hop struct {
	TTL    int     `json:"ttl"`
	Probes []Probe `json:"probes"`
}

// Done reports whether the trace should stop after this hop: the
// destination answered, or a router said it cannot be reached.
func (h Hop) Done() bool {
	for _, p := range h.Probes {
		switch p.Kind {
		case "port-unreachable", "syn-ack", "rst", "unreachable":
			return true
		}
	}
	return false
}

// Reached reports whether the destination itself answered.
func (h Hop) Reached() bool {
	for _, p := range h.Probes {
		switch p.Kind {
		case "port-unreachable", "syn-ack", "rst":
			return true
		}
	}
	return false
}

// tracer sends probes one at a time. With a single probe outstanding, an
// ICMP reply quoting our destination and ports can only belong to it.
type tracer struct {
	opt  Options
	icmp net.PacketConn
	flow *net.UDPConn // with -fixed: the one socket every probe is sent from
}

// Run traces the path, calling report after each hop.
func Run(ctx context.Context, opt Options, report func(Hop)) ([]Hop, error) {
	conn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			return nil, fmt.Errorf("reading ICMP needs a raw socket: run as root or grant CAP_NET_RAW (%w)", err)
		}
		return nil, err
	}
	defer conn.Close()
	t := &tracer{opt: opt, icmp: conn}
	if opt.Fixed && opt.Mode == "udp" {
		// One socket for the whole trace: ECMP routers hash the flow, so
		// every probe follows the same path (the Paris traceroute idea).
		// One socket rather than one port: Linux may reuse a socket's
		// random flow hash when forwarding (see the module), so a new
		// socket can take a new path even with the same ports.
		c, err := net.Dial("udp4", net.JoinHostPort(opt.Target.String(), fmt.Sprint(opt.Port)))
		if err != nil {
			return nil, err
		}
		defer c.Close()
		t.flow = c.(*net.UDPConn)
	}
	var hops []Hop
	seq := 0
	for ttl := opt.FirstTTL; ttl <= opt.MaxHops; ttl++ {
		h := Hop{TTL: ttl}
		for q := 0; q < opt.Queries; q++ {
			if ctx.Err() != nil {
				return hops, ctx.Err()
			}
			port := opt.Port
			if opt.Mode == "udp" && !opt.Fixed {
				port += seq
			}
			seq++
			var p Probe
			if opt.Mode == "tcp" {
				p, err = t.probeTCP(ctx, ttl, port)
			} else {
				p, err = t.probeUDP(ttl, port)
			}
			if err != nil {
				return hops, err
			}
			h.Probes = append(h.Probes, p)
		}
		hops = append(hops, h)
		if report != nil {
			report(h)
		}
		if h.Done() {
			break
		}
	}
	return hops, nil
}

func (t *tracer) probeUDP(ttl, port int) (Probe, error) {
	var c net.Conn
	if t.flow != nil {
		raw, err := t.flow.SyscallConn()
		if err != nil {
			return Probe{}, err
		}
		if err := setTTL(ttl)("", "", raw); err != nil {
			return Probe{}, err
		}
		c = t.flow
	} else {
		d := net.Dialer{Control: setTTL(ttl)}
		nc, err := d.Dial("udp4", net.JoinHostPort(t.opt.Target.String(), fmt.Sprint(port)))
		if err != nil {
			return Probe{}, err
		}
		defer nc.Close()
		c = nc
	}
	local := c.LocalAddr().(*net.UDPAddr)
	start := time.Now()
	if _, err := c.Write(make([]byte, 32)); err != nil && !isICMPErrno(err) {
		return Probe{}, err
	}
	return t.await(ttl, port, uint16(local.Port), 17, start), nil
}

func (t *tracer) probeTCP(ctx context.Context, ttl, port int) (Probe, error) {
	ctx, cancel := context.WithTimeout(ctx, t.opt.Wait)
	defer cancel()
	type result struct {
		err  error
		took time.Duration
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		d := net.Dialer{Control: setTTL(ttl)}
		c, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(t.opt.Target.String(), fmt.Sprint(port)))
		took := time.Since(start)
		if c != nil {
			c.Close() // the handshake completed: the destination answered SYN-ACK
		}
		done <- result{err, took}
	}()
	// The SYN's source port is not known before connect() binds it, so the
	// reply is matched by destination address and port only; probes are
	// sequential, so that is unambiguous.
	icmpReply := make(chan Probe, 1)
	go func() { icmpReply <- t.await(ttl, port, 0, 6, start) }()
	for {
		select {
		case r := <-done:
			switch {
			case r.err == nil:
				return Probe{TTL: ttl, From: t.opt.Target.String(), RTTms: ms(r.took), Kind: "syn-ack", Port: port,
					Meaning: "the destination accepted the connection (SYN-ACK)"}, nil
			case errors.Is(r.err, syscall.ECONNREFUSED):
				return Probe{TTL: ttl, From: t.opt.Target.String(), RTTms: ms(r.took), Kind: "rst", Port: port,
					Meaning: "the destination (or a firewall REJECT) refused the connection"}, nil
			}
			done = nil // timed out or an ICMP-derived error: let the ICMP reader decide
		case p := <-icmpReply:
			cancel()
			return p, nil
		}
	}
}

// await reads ICMP messages until one quotes our probe or the wait expires.
func (t *tracer) await(ttl, dstPort int, srcPort uint16, proto uint8, start time.Time) Probe {
	deadline := start.Add(t.opt.Wait)
	buf := make([]byte, 1500)
	for {
		if err := t.icmp.SetReadDeadline(deadline); err != nil {
			break
		}
		n, from, err := t.icmp.ReadFrom(buf)
		if err != nil {
			break // timeout
		}
		r, err := ParseError(buf[:n])
		if err != nil || r.Proto != proto || r.QuotedDst != t.opt.Target || int(r.DstPort) != dstPort {
			continue
		}
		if srcPort != 0 && r.SrcPort != srcPort {
			continue
		}
		p := Probe{TTL: ttl, From: from.String(), RTTms: ms(time.Since(start)), Port: dstPort, Mark: r.Mark(), Meaning: r.Meaning()}
		switch {
		case r.Type == typeTimeExceeded:
			p.Kind = "time-exceeded"
		case r.Code == codePortUnreach && proto == 17:
			p.Kind = "port-unreachable"
		case r.Code == codeFragNeeded:
			p.Kind = "frag-needed"
		default:
			p.Kind = "unreachable"
		}
		return p
	}
	return Probe{TTL: ttl, Kind: "timeout", Port: dstPort}
}

func setTTL(ttl int) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// isICMPErrno reports errors a connected UDP socket may return because an
// earlier probe's ICMP error was queued on it.
func isICMPErrno(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH)
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
