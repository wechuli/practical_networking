//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// ServeOptions controls Serve.
type ServeOptions struct {
	Addr      string
	Mode      string // echo | discard | greet | reads | close
	Model     string // netpoll | threads
	ReusePort bool
	ID        string
	For       time.Duration
}

var serveModes = map[string]string{
	"echo":    "send back what arrives",
	"discard": "read and drop until the peer closes",
	"greet":   "send one line naming this server, then read until the peer closes",
	"reads":   "report the size of every read() — the receiver's view of the byte stream",
	"close":   "close at once after accept (this side closes first, so it holds TIME_WAIT)",
}

// Serve accepts connections until ctx ends. Accept errors such as EMFILE are
// logged and retried with a growing delay, as net/http does, instead of
// ending the server.
func Serve(ctx context.Context, o ServeOptions, w io.Writer) error {
	if _, ok := serveModes[o.Mode]; !ok {
		return fmt.Errorf("unknown mode %q", o.Mode)
	}
	if o.Model != "netpoll" && o.Model != "threads" {
		return fmt.Errorf("unknown model %q", o.Model)
	}
	lc := net.ListenConfig{}
	if o.ReusePort {
		lc.Control = func(network, address string, c syscall.RawConn) error {
			var serr error
			if err := c.Control(func(fd uintptr) {
				serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, 0xf /* SO_REUSEPORT */, 1)
			}); err != nil {
				return err
			}
			return serr
		}
	}
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, "%s %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, a...))
	}
	if o.Model == "threads" {
		return serveThreads(ctx, o, logf)
	}
	ln, err := lc.Listen(ctx, "tcp4", o.Addr)
	if err != nil {
		return err
	}
	logf("socklab serve %s: mode %s (%s), model %s, reuseport %v, pid %d", ln.Addr(), o.Mode, serveModes[o.Mode], o.Model, o.ReusePort, syscallGetpid())
	go func() { <-ctx.Done(); ln.Close() }()
	var accepted, acceptErrs atomic.Int64
	defer func() {
		logf("served %d connections, %d accept errors", accepted.Load(), acceptErrs.Load())
	}()
	var delay time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			acceptErrs.Add(1)
			if delay == 0 {
				delay = 5 * time.Millisecond
			} else if delay *= 2; delay > time.Second {
				delay = time.Second
			}
			logf("accept error: %v (%s); retrying in %s", err, errnoName(err), delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
			continue
		}
		delay = 0
		accepted.Add(1)
		go serveConn(c, o, logf)
	}
}

func serveConn(c net.Conn, o ServeOptions, logf func(string, ...any)) {
	defer c.Close()
	switch o.Mode {
	case "echo":
		_, _ = io.Copy(c, c)
	case "discard":
		_, _ = io.Copy(io.Discard, c)
	case "greet":
		fmt.Fprintf(c, "hello from %s\n", o.ID)
		_, _ = io.Copy(io.Discard, c)
	case "close":
	case "reads":
		buf := make([]byte, 64*1024)
		var sizes []int
		total := 0
		for {
			n, err := c.Read(buf)
			if n > 0 {
				sizes = append(sizes, n)
				total += n
			}
			if err != nil {
				break
			}
		}
		logf("reads: %s sent %d bytes; the server's read() returned them in %d pieces: %s", c.RemoteAddr(), total, len(sizes), summarize(sizes))
	}
}

// serveThreads is thread-per-connection with no event loop at all: a
// blocking listening socket made with raw system calls, accept(2) on a
// pinned thread, and one pinned OS thread per connection blocked in read(2).
// Go's netpoller (epoll) never sees these descriptors.
func serveThreads(ctx context.Context, o ServeOptions, logf func(string, ...any)) error {
	ta, err := net.ResolveTCPAddr("tcp4", o.Addr)
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if o.ReusePort {
		_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, 0xf, 1)
	}
	sa := &syscall.SockaddrInet4{Port: ta.Port}
	copy(sa.Addr[:], ta.IP.To4())
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return err
	}
	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		syscall.Close(fd)
		return err
	}
	logf("socklab serve %s: mode %s (%s), model threads, reuseport %v, pid %d", o.Addr, o.Mode, serveModes[o.Mode], o.ReusePort, syscallGetpid())
	errc := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		for {
			nfd, _, err := syscall.Accept4(fd, syscall.SOCK_CLOEXEC)
			if err != nil {
				if err == syscall.EINTR {
					continue
				}
				if err == syscall.EMFILE || err == syscall.ENFILE {
					logf("accept error: %v (%s); retrying in 100ms", err, errnoName(err))
					time.Sleep(100 * time.Millisecond)
					continue
				}
				errc <- err
				return
			}
			go serveThread(nfd, o)
		}
	}()
	select {
	case <-ctx.Done():
		syscall.Close(fd)
		return nil
	case err := <-errc:
		return err
	}
}

func serveThread(fd int, o ServeOptions) {
	runtime.LockOSThread() // never unlocked: the thread exits with the goroutine
	defer syscall.Close(fd)
	if o.Mode == "greet" {
		_, _ = syscall.Write(fd, []byte("hello from "+o.ID+"\n"))
	}
	if o.Mode == "close" {
		return
	}
	buf := make([]byte, 64*1024)
	for {
		n, err := syscall.Read(fd, buf)
		if n <= 0 || err != nil {
			return
		}
		if o.Mode == "echo" {
			_, _ = syscall.Write(fd, buf[:n])
		}
	}
}

// summarize prints read sizes compactly: "10 ×3, 970".
func summarize(sizes []int) string {
	var parts []string
	for i := 0; i < len(sizes); {
		j := i
		for j < len(sizes) && sizes[j] == sizes[i] {
			j++
		}
		if j-i > 1 {
			parts = append(parts, fmt.Sprintf("%d ×%d", sizes[i], j-i))
		} else {
			parts = append(parts, fmt.Sprint(sizes[i]))
		}
		i = j
		if len(parts) == 12 && i < len(sizes) {
			parts = append(parts, "…")
			break
		}
	}
	return strings.Join(parts, ", ")
}

var errnoNames = map[syscall.Errno]string{
	syscall.EADDRNOTAVAIL: "EADDRNOTAVAIL: no free local port for this destination",
	syscall.EADDRINUSE:    "EADDRINUSE: another socket holds this address",
	syscall.EMFILE:        "EMFILE: this process has reached its open-files limit",
	syscall.ENFILE:        "ENFILE: the system-wide file table is full",
	syscall.ECONNREFUSED:  "ECONNREFUSED: RST or ICMP port unreachable",
	syscall.ECONNRESET:    "ECONNRESET: the peer reset the connection",
	syscall.EPIPE:         "EPIPE: writing to a connection the peer reset",
	syscall.ETIMEDOUT:     "ETIMEDOUT: no answer within the retries",
	syscall.EHOSTUNREACH:  "EHOSTUNREACH: no route, or an ICMP host unreachable / prohibited",
	syscall.ENETUNREACH:   "ENETUNREACH: no route to that network",
}

// errnoName explains the errno inside err, if there is one.
func errnoName(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		if s, ok := errnoNames[en]; ok {
			return s
		}
		return fmt.Sprintf("errno %d", int(en))
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errDeadline) {
		return "timeout"
	}
	return "no errno"
}

var errDeadline = errors.New("i/o timeout")

func syscallGetpid() int { return syscall.Getpid() }
