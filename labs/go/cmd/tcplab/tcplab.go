//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ServeOptions controls Serve.
type ServeOptions struct {
	Addr    string
	Mode    string
	Backlog int
	RcvBuf  int
	Quiet   bool
}

var modes = map[string]string{
	"discard":  "read until the peer closes, then close",
	"echo":     "send back what arrives, close when the peer closes",
	"hold":     "read until the peer closes, then never close (the socket stays in CLOSE_WAIT)",
	"noread":   "accept and never read (the receive window fills and drops to zero)",
	"noaccept": "never call accept() (the accept queue fills)",
	"reset":    "read the first bytes, then abort with RST (SO_LINGER 0)",
	"close":    "read the first bytes, then close normally (FIN) without answering",
}

// listen builds an IPv4 listener by hand so the backlog and SO_RCVBUF can be
// chosen; the net package always uses somaxconn and sets no buffer size.
// SO_RCVBUF must be set before listen(): accepted sockets inherit it, and the
// window scale offered in the SYN-ACK is computed from it.
func listen(addr string, backlog, rcvbuf int) (net.Listener, error) {
	ta, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	fail := func(what string, err error) (net.Listener, error) {
		syscall.Close(fd)
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
		return fail("SO_REUSEADDR", err)
	}
	if rcvbuf > 0 {
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, rcvbuf); err != nil {
			return fail("SO_RCVBUF", err)
		}
	}
	sa := &syscall.SockaddrInet4{Port: ta.Port}
	if ip := ta.IP.To4(); ip != nil {
		copy(sa.Addr[:], ip)
	}
	if err := syscall.Bind(fd, sa); err != nil {
		return fail("bind "+addr, err)
	}
	if err := syscall.Listen(fd, backlog); err != nil {
		return fail("listen", err)
	}
	f := os.NewFile(uintptr(fd), "tcplab-listener")
	defer f.Close() // FileListener dups the descriptor
	return net.FileListener(f)
}

// Serve accepts connections and treats them according to the mode.
func Serve(ctx context.Context, o ServeOptions, w io.Writer) error {
	what, ok := modes[o.Mode]
	if !ok {
		return fmt.Errorf("unknown mode %q", o.Mode)
	}
	ln, err := listen(o.Addr, o.Backlog, o.RcvBuf)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		if !o.Quiet {
			mu.Lock()
			fmt.Fprintf(w, format+"\n", a...)
			mu.Unlock()
		}
	}
	logf("tcplab serve: listening on %s, backlog %d, mode %s: %s", ln.Addr(), o.Backlog, o.Mode, what)
	if o.Mode == "noaccept" {
		<-ctx.Done()
		return nil
	}
	// Connections that are deliberately left open must stay referenced, or the
	// garbage collector's finalizer would close them behind our back.
	var kept []net.Conn
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		tc := c.(*net.TCPConn)
		_ = tc.SetKeepAlive(false) // Go enables 15 s keepalives by default; the kernel default is off
		peer := c.RemoteAddr().String()
		logf("accepted %s", peer)
		switch o.Mode {
		case "hold", "noread":
			mu.Lock()
			kept = append(kept, c)
			mu.Unlock()
		}
		go func() {
			start := time.Now()
			switch o.Mode {
			case "discard":
				n, err := io.Copy(io.Discard, c)
				logf("%s: received %d bytes in %s%s; closing", peer, n, time.Since(start).Round(time.Millisecond), errSuffix(err))
				c.Close()
			case "echo":
				n, err := io.Copy(c, c)
				logf("%s: echoed %d bytes%s; closing", peer, n, errSuffix(err))
				c.Close()
			case "hold":
				n, err := io.Copy(io.Discard, c)
				logf("%s: peer closed its side after %d bytes%s; NOT closing ours (CLOSE_WAIT)", peer, n, errSuffix(err))
			case "noread":
				// nothing: the bytes pile up in the receive buffer
			case "close":
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				c.Close()
				logf("%s: read %d bytes, then closed without answering (FIN)", peer, n)
			case "reset":
				buf := make([]byte, 1024)
				n, _ := c.Read(buf)
				_ = tc.SetLinger(0)
				c.Close()
				logf("%s: read %d bytes, then closed with SO_LINGER 0 (RST)", peer, n)
			}
		}()
	}
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return " (" + err.Error() + ")"
}

// DialOptions controls Dial.
type DialOptions struct {
	To      string
	Count   int
	Timeout time.Duration
	Hold    time.Duration
}

// DialResult is how one connect() ended.
type DialResult struct {
	N       int           `json:"n"`
	Outcome string        `json:"outcome"` // connected, refused, timed out, unreachable, gave up, error
	Detail  string        `json:"detail"`
	Local   string        `json:"local,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Classify turns a connect error into the outcome a support engineer cares
// about, naming what came back on the wire.
func Classify(err error) (outcome, detail string) {
	switch {
	case err == nil:
		return "connected", "SYN, SYN-ACK, ACK"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "refused", "ECONNREFUSED: RST came back (nothing listening, or a firewall REJECT) or an ICMP port unreachable"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "timed out", "ETIMEDOUT: no answer to the SYN or its retransmissions (tcp_syn_retries); something dropped them"
	case errors.Is(err, syscall.EHOSTUNREACH):
		return "unreachable", "EHOSTUNREACH: an ICMP host unreachable or prohibited came back (ARP failure, or a firewall REJECT)"
	case errors.Is(err, syscall.ENETUNREACH):
		return "unreachable", "ENETUNREACH: no route, or an ICMP network unreachable"
	case errors.Is(err, os.ErrDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "gave up", "the -timeout ran out before the kernel finished retrying"
	case errors.Is(err, context.Canceled):
		return "gave up", "interrupted"
	default:
		return "error", err.Error()
	}
}

// Dial opens Count connections at once and reports how each attempt ended.
func Dial(ctx context.Context, o DialOptions) []DialResult {
	rs := make([]DialResult, o.Count)
	var wg sync.WaitGroup
	for i := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d := net.Dialer{Timeout: o.Timeout, KeepAlive: -1}
			start := time.Now()
			c, err := d.DialContext(ctx, "tcp4", o.To)
			r := DialResult{N: i + 1, Elapsed: time.Since(start)}
			r.Outcome, r.Detail = Classify(err)
			if c != nil {
				r.Local = c.LocalAddr().String()
				if o.Hold > 0 {
					select {
					case <-time.After(o.Hold):
					case <-ctx.Done():
					}
				}
				c.Close()
			}
			rs[i] = r
		}()
	}
	wg.Wait()
	return rs
}

// FormatDials renders Dial results, one line per attempt.
func FormatDials(rs []DialResult) string {
	var b strings.Builder
	counts := map[string]int{}
	for _, r := range rs {
		counts[r.Outcome]++
		fmt.Fprintf(&b, "connect %d: %-11s after %8s  %s\n", r.N, r.Outcome, fmtDur(r.Elapsed), r.Detail)
	}
	if len(rs) > 1 {
		var parts []string
		for _, k := range []string{"connected", "refused", "timed out", "unreachable", "gave up", "error"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
			}
		}
		fmt.Fprintf(&b, "summary: %s\n", strings.Join(parts, ", "))
	}
	return b.String()
}

func fmtDur(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/1e6)
	case d < time.Second:
		return fmt.Sprintf("%.1fms", float64(d)/1e6)
	default:
		return fmt.Sprintf("%.2fs", d.Seconds())
	}
}

// SendOptions controls Send.
type SendOptions struct {
	To      string
	Bytes   int64
	CC      string
	Timeout time.Duration
}

// SendReport describes one transfer.
type SendReport struct {
	To       string        `json:"to"`
	CC       string        `json:"cc"`
	Bytes    int64         `json:"bytes"`
	Written  int64         `json:"written"`
	Complete bool          `json:"complete"` // every byte sent and the peer closed after reading them
	Problem  string        `json:"problem,omitempty"`
	Connect  time.Duration `json:"connect_ns"`
	Transfer time.Duration `json:"transfer_ns"`
	Mbps     float64       `json:"mbit_per_s"`
	Info     Info          `json:"tcp_info"`
}

// Send connects, writes Bytes, half-closes, and waits for the peer to close.
// The peer closing after our FIN is the only proof every byte was read:
// write() returning only means the kernel buffered the data.
func Send(ctx context.Context, o SendOptions) (*SendReport, error) {
	r := &SendReport{To: o.To, CC: o.CC, Bytes: o.Bytes}
	d := net.Dialer{KeepAlive: -1, Timeout: o.Timeout}
	if o.CC != "" {
		d.Control = func(_, _ string, rc syscall.RawConn) error {
			var serr error
			if err := rc.Control(func(fd uintptr) {
				serr = syscall.SetsockoptString(int(fd), syscall.IPPROTO_TCP, syscall.TCP_CONGESTION, o.CC)
			}); err != nil {
				return err
			}
			if serr != nil {
				return fmt.Errorf("congestion control %q: %w (see net.ipv4.tcp_available_congestion_control)", o.CC, serr)
			}
			return nil
		}
	}
	start := time.Now()
	c, err := d.DialContext(ctx, "tcp4", o.To)
	if err != nil {
		outcome, detail := Classify(err)
		return nil, fmt.Errorf("connect %s: %s: %s", outcome, o.To, detail)
	}
	defer c.Close()
	tc := c.(*net.TCPConn)
	r.Connect = time.Since(start)
	if r.CC == "" {
		r.CC = congestion(tc)
	}
	start = time.Now()
	_ = tc.SetDeadline(start.Add(o.Timeout))
	stop := context.AfterFunc(ctx, func() { _ = tc.SetDeadline(time.Now()) })
	defer stop()

	buf := make([]byte, 64<<10)
	for r.Written < o.Bytes {
		chunk := buf
		if left := o.Bytes - r.Written; left < int64(len(chunk)) {
			chunk = chunk[:left]
		}
		n, err := tc.Write(chunk)
		r.Written += int64(n)
		if err != nil {
			r.Problem = writeProblem(err)
			break
		}
	}
	if r.Problem == "" {
		_ = tc.CloseWrite()
		_, err = io.Copy(io.Discard, tc)
		switch {
		case err == nil:
			r.Complete = true
		case errors.Is(err, os.ErrDeadlineExceeded):
			r.Problem = "all bytes written, but the peer never closed its side (it may not have read them, or never closes)"
		default:
			r.Problem = writeProblem(err)
		}
	}
	r.Transfer = time.Since(start)
	r.Info, _ = tcpInfo(tc)
	if r.Complete && r.Transfer > 0 {
		r.Mbps = float64(r.Written) * 8 / r.Transfer.Seconds() / 1e6
	}
	return r, nil
}

func writeProblem(err error) string {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return "timed out with data still unsent: the send buffer is full and not draining (zero window or a dead path)"
	case errors.Is(err, syscall.ECONNRESET):
		return "ECONNRESET: the peer sent RST (it closed with unread data, aborted, or forgot the connection)"
	case errors.Is(err, syscall.EPIPE):
		return "EPIPE: writing after the connection was reset"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "ETIMEDOUT: data went unacknowledged through every retransmission (tcp_retries2)"
	default:
		return err.Error()
	}
}

// Format renders the report.
func (r *SendReport) Format() string {
	var b strings.Builder
	fmt.Fprintf(&b, "tcplab send: %s to %s (cc %s)\n", fmtSize(r.Bytes), r.To, r.CC)
	fmt.Fprintf(&b, "  connect   %s (one round trip: SYN, SYN-ACK)\n", fmtDur(r.Connect))
	if r.Complete {
		fmt.Fprintf(&b, "  transfer  complete: %d bytes in %s = %.1f Mbit/s\n", r.Written, fmtDur(r.Transfer), r.Mbps)
	} else {
		fmt.Fprintf(&b, "  transfer  INCOMPLETE after %s: wrote %d of %d bytes\n", fmtDur(r.Transfer), r.Written, r.Bytes)
		fmt.Fprintf(&b, "            %s\n", r.Problem)
	}
	i := r.Info
	fmt.Fprintf(&b, "  tcp_info  state %s, rtt %.2f ms (var %.2f), rto %d ms, mss %d\n",
		i.State, float64(i.RTTus)/1000, float64(i.RTTVarUs)/1000, i.RTOus/1000, i.SndMSS)
	fmt.Fprintf(&b, "            cwnd %d segments, ssthresh %s, retransmitted %d segments in total\n",
		i.Cwnd, i.SsthreshString(), i.TotalRetrans)
	fmt.Fprintf(&b, "            unacked %d, unanswered probes %d, backoff %d, options %s\n",
		i.Unacked, i.Probes, i.Backoff, i.OptionString())
	return b.String()
}

func parseSize(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("bad size %q", s)
	}
	return n * mult, nil
}

func fmtSize(n int64) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10 && n%(1<<10) == 0:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
