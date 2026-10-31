//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Params describe the emulated path; each direction gets the same ones.
type Params struct {
	Delay   time.Duration
	Jitter  time.Duration
	Loss    float64
	RateBps float64 // bits per second; 0 = unlimited
	Limit   int     // packets that may wait at the bottleneck
	DropBig int     // one-shot: drop the DropBig-th packet of ≥ 1000 bytes travelling B→A from now
}

func (p Params) String() string {
	rate := "unlimited"
	if p.RateBps > 0 {
		rate = FormatRate(p.RateBps)
	}
	s := fmt.Sprintf("delay %s each way (RTT +%s), jitter %s, loss %.3g%%, rate %s, queue %d packets",
		p.Delay, 2*p.Delay, p.Jitter, p.Loss*100, rate, p.Limit)
	if p.DropBig > 0 {
		s += fmt.Sprintf(", will drop large packet #%d", p.DropBig)
	}
	return s
}

// Apply returns p changed by query values delay=, jitter=, loss=, rate=, limit=.
func (p Params) Apply(q url.Values) (Params, error) {
	for k, vs := range q {
		v := vs[0]
		var err error
		switch k {
		case "delay":
			p.Delay, err = time.ParseDuration(v)
		case "jitter":
			p.Jitter, err = time.ParseDuration(v)
		case "loss":
			p.Loss, err = strconv.ParseFloat(v, 64)
			if err == nil && (p.Loss < 0 || p.Loss > 1) {
				err = fmt.Errorf("loss must be between 0 and 1")
			}
		case "rate":
			p.RateBps, err = ParseRate(v)
		case "limit":
			p.Limit, err = strconv.Atoi(v)
		case "drop_big":
			p.DropBig, err = strconv.Atoi(v)
		default:
			err = fmt.Errorf("unknown setting %q", k)
		}
		if err != nil {
			return p, fmt.Errorf("%s=%s: %w", k, v, err)
		}
	}
	return p, nil
}

// ParseRate accepts 20mbit, 1gbit, 500kbit, or bits per second.
func ParseRate(s string) (float64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := 1.0
	for _, u := range []struct {
		suffix string
		m      float64
	}{{"gbit", 1e9}, {"mbit", 1e6}, {"kbit", 1e3}, {"bit", 1}} {
		if strings.HasSuffix(s, u.suffix) {
			s, mult = strings.TrimSuffix(s, u.suffix), u.m
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad rate %q", s)
	}
	return v * mult, nil
}

// FormatRate prints bits per second the way tc does.
func FormatRate(bps float64) string {
	switch {
	case bps >= 1e9:
		return strconv.FormatFloat(bps/1e9, 'g', 4, 64) + "Gbit"
	case bps >= 1e6:
		return strconv.FormatFloat(bps/1e6, 'g', 4, 64) + "Mbit"
	case bps >= 1e3:
		return strconv.FormatFloat(bps/1e3, 'g', 4, 64) + "Kbit"
	}
	return strconv.FormatFloat(bps, 'g', 4, 64) + "bit"
}

// Path is both directions with shared, changeable parameters.
type Path struct {
	mu     sync.RWMutex
	params Params
	big    int // large packets seen since DropBig was set
	AB, BA *Direction
}

// BigDrop is the size from which drop_big counts packets: data-carrying
// packets, not ACKs or small requests.
const BigDrop = 1000

// dropThisBig counts a large B→A packet and reports whether it is the one to drop.
func (p *Path) dropThisBig(d *Direction, size int) bool {
	if size < BigDrop || d != p.BA {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.params.DropBig <= 0 {
		return false
	}
	p.big++
	if p.big == p.params.DropBig {
		p.params.DropBig, p.big = 0, 0
		return true
	}
	return false
}

// NewPath builds a path.
func NewPath(p Params) *Path {
	path := &Path{params: p}
	path.AB = &Direction{path: path}
	path.BA = &Direction{path: path}
	return path
}

// Params returns the current parameters.
func (p *Path) Params() Params { p.mu.RLock(); defer p.mu.RUnlock(); return p.params }

// SetParams changes them for packets arriving from now on.
func (p *Path) SetParams(np Params) { p.mu.Lock(); p.params = np; p.big = 0; p.mu.Unlock() }

// Stats are one direction's counters.
type Stats struct {
	Packets       uint64  `json:"packets"`
	Bytes         uint64  `json:"bytes"`
	LossDrops     uint64  `json:"dropped_random"`
	QueueDrops    uint64  `json:"dropped_queue_full"`
	TargetedDrops uint64  `json:"dropped_targeted"`
	Delivered     uint64  `json:"delivered"`
	MaxQueueDelay float64 `json:"max_queueing_delay_ms"`
}

// Direction is one way through the path: a bottleneck queue then a delay line.
type Direction struct {
	path     *Path
	mu       sync.Mutex
	stats    Stats
	nextFree time.Time // when the bottleneck finishes sending what is queued
	queued   []time.Time
}

// Stats returns a copy of the counters.
func (d *Direction) Stats() Stats { d.mu.Lock(); defer d.mu.Unlock(); return d.stats }

type pending struct {
	at  time.Time
	pkt []byte
}

// admit decides a packet's fate at time now: dropped, or delivered at a time.
func (d *Direction) admit(now time.Time, size int) (deliver time.Time, ok bool) {
	big := d.path.dropThisBig(d, size)
	p := d.path.Params()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stats.Packets++
	d.stats.Bytes += uint64(size)
	if big {
		d.stats.TargetedDrops++
		return time.Time{}, false
	}
	if p.Loss > 0 && rand.Float64() < p.Loss {
		d.stats.LossDrops++
		return time.Time{}, false
	}
	depart := now
	if p.RateBps > 0 {
		// Packets still waiting for the bottleneck at this moment.
		for len(d.queued) > 0 && !d.queued[0].After(now) {
			d.queued = d.queued[1:]
		}
		if len(d.queued) >= p.Limit {
			d.stats.QueueDrops++
			return time.Time{}, false
		}
		start := now
		if d.nextFree.After(start) {
			start = d.nextFree
		}
		serialise := time.Duration(float64(size*8) / p.RateBps * float64(time.Second))
		depart = start.Add(serialise)
		d.nextFree = depart
		d.queued = append(d.queued, depart)
		if q := float64(start.Sub(now)) / 1e6; q > d.stats.MaxQueueDelay {
			d.stats.MaxQueueDelay = q
		}
	}
	deliver = depart.Add(p.Delay)
	if p.Jitter > 0 {
		deliver = deliver.Add(time.Duration(rand.Int64N(int64(p.Jitter))))
	}
	return deliver, true
}

// Pump reads packets from the TUN device and writes them back after their
// delay, keeping their order.
func (d *Direction) Pump(ctx context.Context, tun io.ReadWriter) {
	line := make(chan pending, 65536)
	go func() {
		last := time.Time{}
		for pk := range line {
			at := pk.at
			if at.Before(last) { // jitter never reorders
				at = last
			}
			last = at
			if wait := time.Until(at); wait > 0 {
				time.Sleep(wait)
			}
			if _, err := tun.Write(pk.pkt); err == nil {
				d.mu.Lock()
				d.stats.Delivered++
				d.mu.Unlock()
			}
		}
	}()
	buf := make([]byte, 65536)
	for ctx.Err() == nil {
		n, err := tun.Read(buf)
		if err != nil {
			close(line)
			return
		}
		if at, ok := d.admit(time.Now(), n); ok {
			line <- pending{at, append([]byte(nil), buf[:n]...)}
		}
	}
	close(line)
}

// OpenTUN attaches to a TUN device (created beforehand with ip tuntap) in
// IFF_NO_PI mode: each read and write is exactly one IP packet.
func OpenTUN(name string) (*os.File, error) {
	fd, err := syscall.Open("/dev/net/tun", syscall.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	var ifr [40]byte
	copy(ifr[:16], name)
	const iffTUN, iffNoPI = 0x0001, 0x1000
	*(*uint16)(unsafe.Pointer(&ifr[16])) = iffTUN | iffNoPI
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TUNSETIFF, uintptr(unsafe.Pointer(&ifr[0]))); e != 0 {
		syscall.Close(fd)
		return nil, fmt.Errorf("TUNSETIFF: %w", e)
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun:"+name), nil
}
