//go:build linux

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Writes sends n writes of size bytes on one connection and closes it.
func Writes(to string, n, size int, nodelay bool, w io.Writer) error {
	c, err := net.Dial("tcp4", to)
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.(*net.TCPConn).SetNoDelay(nodelay); err != nil {
		return err
	}
	msg := make([]byte, size)
	for i := range msg {
		msg[i] = 'a' + byte(i%26)
	}
	for i := 0; i < n; i++ {
		if _, err := c.Write(msg); err != nil {
			return err
		}
	}
	mode := "TCP_NODELAY on (Go's default)"
	if !nodelay {
		mode = "Nagle's algorithm on (TCP_NODELAY off)"
	}
	fmt.Fprintf(w, "socklab writes: %d write() calls of %d bytes = %d bytes from %s, %s\n", n, size, n*size, c.LocalAddr(), mode)
	return nil
}

// Hold opens connections one at a time, keeping all of them, until n are
// open or an attempt fails; it reports where and why it stopped.
func Hold(ctx context.Context, to string, n int, hold time.Duration) string {
	var conns []net.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	var b strings.Builder
	ports := map[string]bool{}
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp4", to, 3*time.Second)
		if err != nil {
			fmt.Fprintf(&b, "socklab hold: %d connections open to %s; attempt %d failed: %v\n  → %s\n", len(conns), to, i+1, err, errnoName(err))
			break
		}
		conns = append(conns, c)
		_, p, _ := net.SplitHostPort(c.LocalAddr().String())
		ports[p] = true
	}
	if len(conns) == n {
		fmt.Fprintf(&b, "socklab hold: all %d connections open to %s\n", n, to)
	}
	if len(conns) > 0 {
		fmt.Fprintf(&b, "  local ports used: %d (%s … %s)\n", len(ports), portOf(conns[0]), portOf(conns[len(conns)-1]))
	}
	if hold > 0 {
		select {
		case <-time.After(hold):
		case <-ctx.Done():
		}
	}
	return b.String()
}

func portOf(c net.Conn) string {
	_, p, _ := net.SplitHostPort(c.LocalAddr().String())
	return p
}

// Churn connects and closes n times in a row. When the client closes first,
// each connection leaves a TIME_WAIT behind on the client; with serverFirst
// the client waits for the server's FIN, and the TIME_WAIT is the server's.
func Churn(to string, n int, serverFirst bool) string {
	ok := 0
	errs := map[string]int{}
	first := ""
	start := time.Now()
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp4", to, 3*time.Second)
		if err != nil {
			k := errnoName(err)
			if errs[k] == 0 && first == "" {
				first = fmt.Sprintf("first failure at attempt %d: %v", i+1, err)
			}
			errs[k]++
			continue
		}
		ok++
		if serverFirst {
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_, _ = io.Copy(io.Discard, c) // wait for the server's FIN, so the server closes first
		}
		c.Close()
	}
	var b strings.Builder
	who := "the client closes first"
	if serverFirst {
		who = "the server closes first"
	}
	fmt.Fprintf(&b, "socklab churn: %d of %d connect+close cycles to %s succeeded in %s (%s)\n", ok, n, to, time.Since(start).Round(time.Millisecond), who)
	if first != "" {
		fmt.Fprintf(&b, "  %s\n", first)
		var ks []string
		for k := range errs {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			fmt.Fprintf(&b, "  %d × %s\n", errs[k], k)
		}
	}
	return b.String()
}

// Bind creates a listening socket with exactly the options asked for (the
// net package always sets SO_REUSEADDR, so this uses raw system calls).
func Bind(addr string, reuseAddr, reusePort bool, w io.Writer) error {
	ta, err := net.ResolveTCPAddr("tcp4", addr)
	if err != nil {
		return err
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	opts := []string{}
	if reuseAddr {
		opts = append(opts, "SO_REUSEADDR")
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1); err != nil {
			return err
		}
	}
	if reusePort {
		opts = append(opts, "SO_REUSEPORT")
		if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, 0xf, 1); err != nil {
			return err
		}
	}
	if len(opts) == 0 {
		opts = append(opts, "no options")
	}
	sa := &syscall.SockaddrInet4{Port: ta.Port}
	copy(sa.Addr[:], ta.IP.To4())
	if err := syscall.Bind(fd, sa); err != nil {
		fmt.Fprintf(w, "socklab bind %s (%s): bind() failed: %v → %s\n", addr, strings.Join(opts, ", "), err, errnoName(err))
		return err
	}
	if err := syscall.Listen(fd, 16); err != nil {
		fmt.Fprintf(w, "socklab bind %s (%s): listen() failed: %v → %s\n", addr, strings.Join(opts, ", "), err, errnoName(err))
		return err
	}
	fmt.Fprintf(w, "socklab bind %s (%s): bind() and listen() succeeded\n", addr, strings.Join(opts, ", "))
	return nil
}

// HTTPOptions controls HTTP.
type HTTPOptions struct {
	URL         string
	N, C        int
	KeepAlive   bool
	IdlePerHost int
	Drain       bool
	CloseBody   bool
	Timeout     time.Duration
}

// HTTP runs N requests from C workers through one http.Transport and reports
// how many TCP connections it opened, latency percentiles and errors.
func HTTP(ctx context.Context, o HTTPOptions) (string, error) {
	var dials atomic.Int64
	d := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			return d.DialContext(ctx, network, addr)
		},
		DisableKeepAlives:   !o.KeepAlive,
		MaxIdleConnsPerHost: o.IdlePerHost,
		MaxIdleConns:        100,
		IdleConnTimeout:     90 * time.Second,
	}
	client := &http.Client{Transport: tr, Timeout: o.Timeout}
	var newConns, reused atomic.Int64
	trace := &httptrace.ClientTrace{GotConn: func(i httptrace.GotConnInfo) {
		if i.Reused {
			reused.Add(1)
		} else {
			newConns.Add(1)
		}
	}}
	jobs := make(chan int)
	var mu sync.Mutex
	var lat []time.Duration
	errs := map[string]int{}
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < o.C; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				t0 := time.Now()
				req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", o.URL, nil)
				resp, err := client.Do(req)
				if err == nil {
					if o.Drain {
						_, _ = io.Copy(io.Discard, resp.Body)
					}
					if o.CloseBody {
						resp.Body.Close()
					}
				}
				d := time.Since(t0)
				mu.Lock()
				if err != nil {
					msg := err.Error()
					if i := strings.LastIndex(msg, ": "); i >= 0 {
						msg = msg[i+2:]
					}
					errs[msg+" ("+errnoName(err)+")"]++
				} else {
					lat = append(lat, d)
				}
				mu.Unlock()
			}
		}()
	}
	for i := 0; i < o.N; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	total := time.Since(start)
	tr.CloseIdleConnections()
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	pct := func(p float64) time.Duration {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(p*float64(len(lat)-1))].Round(10 * time.Microsecond)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "socklab http: %d requests, %d workers, keep-alive %v, MaxIdleConnsPerHost %d, drain %v, close %v\n",
		o.N, o.C, o.KeepAlive, o.IdlePerHost, o.Drain, o.CloseBody)
	fmt.Fprintf(&b, "  %d ok in %s (%.0f req/s); latency p50 %s, p99 %s, max %s\n",
		len(lat), total.Round(time.Millisecond), float64(len(lat))/total.Seconds(), pct(0.5), pct(0.99), pct(1))
	fmt.Fprintf(&b, "  TCP connections dialed: %d; requests on a new connection %d, on a reused one %d\n", dials.Load(), newConns.Load(), reused.Load())
	for k, v := range errs {
		fmt.Fprintf(&b, "  %d × %s\n", v, k)
	}
	return b.String(), nil
}

// Stat reports what a process costs: threads, resident memory, descriptors,
// and how many descriptors its epoll instances watch.
func Stat(pid int) (string, error) {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return "", err
	}
	field := func(name string) string {
		for _, l := range strings.Split(string(status), "\n") {
			if strings.HasPrefix(l, name+":") {
				return strings.Join(strings.Fields(strings.TrimPrefix(l, name+":")), " ")
			}
		}
		return "?"
	}
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil {
		return "", err
	}
	sockets, watched, epolls := 0, 0, 0
	for _, e := range fds {
		target, _ := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, e.Name()))
		switch {
		case strings.HasPrefix(target, "socket:"):
			sockets++
		case target == "anon_inode:[eventpoll]":
			epolls++
			info, _ := os.ReadFile(fmt.Sprintf("/proc/%d/fdinfo/%s", pid, e.Name()))
			watched += strings.Count(string(info), "tfd:")
		}
	}
	return fmt.Sprintf("pid %d: threads %s, RSS %s, open descriptors %d (sockets %d), epoll instances %d watching %d descriptors",
		pid, field("Threads"), field("VmRSS"), len(fds), sockets, epolls, watched), nil
}

// Greetings opens n connections one after another, reads the first line from
// each (serve -mode greet), and counts which server answered.
func Greetings(to string, n int) string {
	counts := map[string]int{}
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp4", to, 3*time.Second)
		if err != nil {
			counts["error: "+errnoName(err)]++
			continue
		}
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		line, err := bufio.NewReader(c).ReadString('\n')
		c.Close()
		if err != nil {
			counts["no greeting: "+err.Error()]++
			continue
		}
		counts[strings.TrimSpace(line)]++
	}
	var keys []string
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "socklab greet: %d connections to %s\n", n, to)
	for _, k := range keys {
		fmt.Fprintf(&b, "  %4d  %s\n", counts[k], k)
	}
	return b.String()
}
