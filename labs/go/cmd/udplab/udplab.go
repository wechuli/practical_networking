package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Each datagram starts with a 16-byte header: a magic number, the sequence
// number, and the attempt number, so replies can be matched to what was sent.
const (
	headerLen = 16
	magic     = 0x75647031 // "udp1"
)

func encode(buf []byte, seq uint64, attempt uint32) {
	binary.BigEndian.PutUint32(buf[0:4], magic)
	binary.BigEndian.PutUint64(buf[4:12], seq)
	binary.BigEndian.PutUint32(buf[12:16], attempt)
}

func decode(buf []byte) (seq uint64, attempt uint32, ok bool) {
	if len(buf) < headerLen || binary.BigEndian.Uint32(buf[0:4]) != magic {
		return 0, 0, false
	}
	return binary.BigEndian.Uint64(buf[4:12]), binary.BigEndian.Uint32(buf[12:16]), true
}

// Echo answers every datagram with an identical one, optionally after a delay.
func Echo(ctx context.Context, addr string, delay time.Duration, logEach bool, w io.Writer) error {
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); pc.Close() }()
	fmt.Fprintf(w, "udplab echo: listening on %s (reply delay %s)\n", pc.LocalAddr(), delay)
	buf := make([]byte, 65535)
	for n := 1; ; n++ {
		size, from, err := pc.ReadFrom(buf)
		if err != nil {
			return err
		}
		if logEach {
			seq, attempt, _ := decode(buf[:size])
			fmt.Fprintf(w, "datagram %d from %s: %d bytes (seq %d, attempt %d)\n", n, from, size, seq, attempt)
		}
		msg := append([]byte(nil), buf[:size]...)
		if delay > 0 {
			go func() { time.Sleep(delay); _, _ = pc.WriteTo(msg, from) }()
			continue
		}
		if _, err := pc.WriteTo(msg, from); err != nil && logEach {
			fmt.Fprintf(w, "reply to %s failed: %v\n", from, err)
		}
	}
}

// SendOptions controls Send.
type SendOptions struct {
	To          string
	Count       int
	Size        int
	Interval    time.Duration
	Timeout     time.Duration
	Retries     int
	Unconnected bool
	RecvBuf     int
}

// Report summarises a run.
type Report struct {
	To          string             `json:"to"`
	Socket      string             `json:"socket"` // connected or unconnected
	Sent        int                `json:"sent"`   // datagrams transmitted, retries included
	Requests    int                `json:"requests"`
	Answered    int                `json:"answered"` // requests whose echo arrived (first or retried)
	FirstTry    int                `json:"answered_first_try"`
	Duplicates  int                `json:"duplicates"`
	Reordered   int                `json:"reordered"`
	Truncated   int                `json:"truncated"`
	Errors      map[string]int     `json:"errors,omitempty"` // socket errors, e.g. connection refused
	RTTms       map[string]float64 `json:"rtt_ms,omitempty"`
	TruncDetail string             `json:"truncation,omitempty"`
}

// Send sends Count numbered datagrams one at a time and waits for each echo.
func Send(ctx context.Context, o SendOptions) (*Report, error) {
	raddr, err := net.ResolveUDPAddr("udp", o.To)
	if err != nil {
		return nil, err
	}
	var conn *net.UDPConn
	r := &Report{To: raddr.String(), Socket: "connected", Errors: map[string]int{}}
	if o.Unconnected {
		conn, err = net.ListenUDP("udp", nil)
		r.Socket = "unconnected"
	} else {
		conn, err = net.DialUDP("udp", nil, raddr)
	}
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	out := make([]byte, o.Size)
	bufSize := o.RecvBuf
	if bufSize == 0 {
		bufSize = 65535
	}
	in := make([]byte, bufSize)
	oob := make([]byte, 64)
	seen := map[uint64]bool{}
	var rtts []float64
	var highest uint64

	for seq := uint64(1); seq <= uint64(o.Count); seq++ {
		if ctx.Err() != nil {
			break
		}
		r.Requests++
		answered := false
		for attempt := uint32(0); attempt <= uint32(o.Retries) && !answered; attempt++ {
			encode(out, seq, attempt)
			start := time.Now()
			if o.Unconnected {
				_, err = conn.WriteToUDP(out, raddr)
			} else {
				_, err = conn.Write(out)
			}
			r.Sent++
			if err != nil {
				r.Errors[errName(err)]++
				continue
			}
			deadline := start.Add(o.Timeout)
			for {
				_ = conn.SetReadDeadline(deadline)
				n, _, flags, _, err := conn.ReadMsgUDP(in, oob)
				if err != nil {
					var ne net.Error
					if !(errors.As(err, &ne) && ne.Timeout()) {
						r.Errors[errName(err)]++
						// An ICMP error ends this attempt; wait out the rest of
						// the timeout so the pacing stays the same.
						time.Sleep(time.Until(deadline))
					}
					break
				}
				if flags&syscall.MSG_TRUNC != 0 {
					r.Truncated++
					r.TruncDetail = fmt.Sprintf("read %d bytes into a %d-byte buffer; the kernel set MSG_TRUNC and discarded the rest of a %d-byte datagram", n, bufSize, o.Size)
				}
				got, _, ok := decode(in[:n])
				if !ok {
					if r.Truncated > 0 && n >= headerLen {
						got, _, ok = decode(in[:headerLen])
					}
					if !ok {
						continue
					}
				}
				if seen[got] {
					r.Duplicates++
					continue
				}
				if got != seq {
					// A late echo of an earlier request: count it, keep waiting.
					if got < highest {
						r.Reordered++
					}
					seen[got] = true
					continue
				}
				seen[got] = true
				if got > highest {
					highest = got
				}
				answered = true
				r.Answered++
				if attempt == 0 {
					r.FirstTry++
				}
				rtts = append(rtts, float64(time.Since(start).Microseconds())/1000)
				break
			}
		}
		if seq < uint64(o.Count) {
			time.Sleep(o.Interval)
		}
	}
	if len(rtts) > 0 {
		sort.Float64s(rtts)
		sum := 0.0
		for _, v := range rtts {
			sum += v
		}
		r.RTTms = map[string]float64{"min": rtts[0], "avg": sum / float64(len(rtts)), "p50": rtts[len(rtts)/2],
			"p99": rtts[(len(rtts)*99)/100], "max": rtts[len(rtts)-1]}
	}
	if len(r.Errors) == 0 {
		r.Errors = nil
	}
	return r, nil
}

func errName(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused (ICMP port unreachable)"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "no route to host (ICMP host unreachable)"
	case errors.Is(err, syscall.ENETUNREACH):
		return "network unreachable"
	case errors.Is(err, syscall.EMSGSIZE):
		return "message too long (EMSGSIZE)"
	}
	return err.Error()
}

// Format renders the report as text.
func (r *Report) Format() string {
	var b strings.Builder
	pct := func(a, n int) float64 {
		if n == 0 {
			return 0
		}
		return 100 * float64(a) / float64(n)
	}
	fmt.Fprintf(&b, "udplab send to %s (%s socket)\n", r.To, r.Socket)
	fmt.Fprintf(&b, "requests %d, datagrams sent %d (retries %d)\n", r.Requests, r.Sent, r.Sent-r.Requests)
	fmt.Fprintf(&b, "answered %d/%d = %.1f%% (first try %d = %.1f%%)\n", r.Answered, r.Requests, pct(r.Answered, r.Requests), r.FirstTry, pct(r.FirstTry, r.Requests))
	fmt.Fprintf(&b, "duplicates %d, late or reordered %d, truncated %d\n", r.Duplicates, r.Reordered, r.Truncated)
	if r.RTTms != nil {
		fmt.Fprintf(&b, "rtt ms: min %.3f  avg %.3f  p50 %.3f  p99 %.3f  max %.3f\n", r.RTTms["min"], r.RTTms["avg"], r.RTTms["p50"], r.RTTms["p99"], r.RTTms["max"])
	}
	keys := make([]string, 0, len(r.Errors))
	for k := range r.Errors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "socket error: %s × %d\n", k, r.Errors[k])
	}
	if r.Socket == "unconnected" && r.Answered < r.Requests && len(r.Errors) == 0 {
		b.WriteString("note: an unconnected UDP socket is never told about ICMP errors; lost and refused look the same (silence)\n")
	}
	if r.TruncDetail != "" {
		fmt.Fprintf(&b, "truncation: %s\n", r.TruncDetail)
	}
	return b.String()
}
