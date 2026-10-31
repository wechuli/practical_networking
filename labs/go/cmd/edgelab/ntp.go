package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"time"
)

// NTP timestamps count seconds since 1900 in 32.32 fixed point.
const ntpEpochOffset = 2208988800

func toNTP(t time.Time) uint64 {
	secs := uint64(t.Unix() + ntpEpochOffset)
	frac := uint64(t.Nanosecond()) << 32 / 1e9
	return secs<<32 | frac
}

func fromNTP(v uint64) time.Time {
	secs := int64(v>>32) - ntpEpochOffset
	nanos := (int64(v&0xffffffff) * 1e9) >> 32
	return time.Unix(secs, nanos)
}

// NTPServe answers NTPv4 client requests with this host's clock plus
// offset — a server that is wrong on purpose. requestDelay holds each
// request before it is timestamped and replyDelay holds each reply after,
// which is what one-way network delay on the way in and out looks like to
// the four timestamps (this kernel has no netem to delay the packets).
func NTPServe(ctx context.Context, addr string, offset, requestDelay, replyDelay time.Duration, stratum int, w io.Writer) error {
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); pc.Close() }()
	fmt.Fprintf(w, "edgelab ntp server on %s: stratum %d, clock offset %s, one-way delay in %s / out %s\n", pc.LocalAddr(), stratum, offset, requestDelay, replyDelay)
	for {
		buf := make([]byte, 512)
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return nil
		}
		if n < 48 || buf[0]&0x7 != 3 { // mode 3 = client
			continue
		}
		go func(req []byte, to net.Addr) {
			time.Sleep(requestDelay)
			_, _ = pc.WriteTo(ntpReply(req, offset, stratum, replyDelay), to)
		}(buf[:n], from)
	}
}

// ntpReply builds the server's answer; the transmit timestamp is taken
// before the reply is held for replyDelay.
func ntpReply(req []byte, offset time.Duration, stratum int, replyDelay time.Duration) []byte {
	recv := time.Now().Add(offset)
	resp := make([]byte, 48)
	resp[0] = 0<<6 | 4<<3 | 4 // LI 0, version 4, mode 4 (server)
	resp[1] = byte(stratum)
	resp[2] = req[2] // poll
	resp[3] = 0xEC   // precision ~ 2^-20 s
	copy(resp[12:16], "LAB\x00")
	binary.BigEndian.PutUint64(resp[16:24], toNTP(recv))                   // reference time
	copy(resp[24:32], req[40:48])                                          // originate = client's transmit
	binary.BigEndian.PutUint64(resp[32:40], toNTP(recv))                   // receive
	binary.BigEndian.PutUint64(resp[40:48], toNTP(time.Now().Add(offset))) // transmit
	time.Sleep(replyDelay)
	return resp
}

// NTPQuery sends n requests and computes offset and delay from the four
// timestamps (RFC 5905): t1 client send, t2 server receive, t3 server send,
// t4 client receive. offset = ((t2−t1)+(t3−t4))/2, delay = (t4−t1)−(t3−t2).
func NTPQuery(server string, n int) (string, error) {
	c, err := net.Dial("udp4", server)
	if err != nil {
		return "", err
	}
	defer c.Close()
	var b strings.Builder
	var offs, dels []time.Duration
	fmt.Fprintf(&b, "edgelab ntp query %s\n", server)
	for i := 0; i < n; i++ {
		req := make([]byte, 48)
		req[0] = 0<<6 | 4<<3 | 3
		t1 := time.Now()
		binary.BigEndian.PutUint64(req[40:48], toNTP(t1))
		if _, err := c.Write(req); err != nil {
			return "", err
		}
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		resp := make([]byte, 128)
		m, err := c.Read(resp)
		t4 := time.Now()
		if err != nil || m < 48 {
			fmt.Fprintf(&b, "  sample %d: no reply (%v)\n", i+1, err)
			continue
		}
		t2 := fromNTP(binary.BigEndian.Uint64(resp[32:40]))
		t3 := fromNTP(binary.BigEndian.Uint64(resp[40:48]))
		offset := (t2.Sub(t1) + t3.Sub(t4)) / 2
		delay := t4.Sub(t1) - t3.Sub(t2)
		offs, dels = append(offs, offset), append(dels, delay)
		fmt.Fprintf(&b, "  sample %d: stratum %d  offset %+.3f ms  delay %.3f ms\n", i+1, resp[1], ms(offset), ms(delay))
		time.Sleep(100 * time.Millisecond)
	}
	if len(offs) > 0 {
		fmt.Fprintf(&b, "  median of %d: offset %+.1f ms (%+.2f s), delay %.1f ms\n", len(offs), ms(median(offs)), ms(median(offs))/1000, ms(median(dels)))
	}
	return b.String(), nil
}

func ms(d time.Duration) float64 {
	v := float64(d.Microseconds()) / 1000
	if v > -0.05 && v < 0.05 {
		return 0 // print +0.0, not -0.0
	}
	return v
}

func median(v []time.Duration) time.Duration {
	s := append([]time.Duration(nil), v...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2]
}
