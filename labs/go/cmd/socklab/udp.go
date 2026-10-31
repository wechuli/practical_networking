//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"
)

// UDPSink receives datagrams but processes each one slowly (per), as a busy
// syslog collector or metrics agent might. Whatever arrives while the socket's
// receive buffer is full is dropped by the kernel (UdpRcvbufErrors).
func UDPSink(ctx context.Context, addr string, per time.Duration, rcvbuf int, force bool, w io.Writer) error {
	pc, err := net.ListenPacket("udp4", addr)
	if err != nil {
		return err
	}
	defer pc.Close()
	switch {
	case rcvbuf > 0 && force:
		// SO_RCVBUFFORCE (root/CAP_NET_ADMIN) ignores net.core.rmem_max.
		rc, err := pc.(*net.UDPConn).SyscallConn()
		if err != nil {
			return err
		}
		var serr error
		if err := rc.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, rcvbuf)
		}); err != nil {
			return err
		}
		if serr != nil {
			return serr
		}
	case rcvbuf > 0:
		// SO_RCVBUF: silently capped at net.core.rmem_max (then doubled by the kernel).
		if err := pc.(*net.UDPConn).SetReadBuffer(rcvbuf); err != nil {
			return err
		}
	}
	go func() { <-ctx.Done(); pc.Close() }()
	buf := make([]byte, 65536)
	got := 0
	defer func() { fmt.Fprintf(w, "socklab udp-sink %s: processed %d datagrams (%s each)\n", addr, got, per) }()
	for {
		_ = pc.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := pc.ReadFrom(buf); err != nil {
			return nil // quiet for 2 s, or interrupted: report and stop
		}
		got++
		if per > 0 {
			time.Sleep(per)
		}
	}
}

// UDPBlast sends n datagrams of size bytes as fast as the socket allows.
func UDPBlast(to string, n, size int, w io.Writer) error {
	c, err := net.Dial("udp4", to)
	if err != nil {
		return err
	}
	defer c.Close()
	msg := make([]byte, size)
	sent := 0
	start := time.Now()
	for i := 0; i < n; i++ {
		if _, err := c.Write(msg); err == nil {
			sent++
		}
	}
	fmt.Fprintf(w, "socklab udp-blast: sent %d of %d datagrams of %d bytes to %s in %s; UDP never blocks or reports a drop to the sender\n", sent, n, size, to, time.Since(start).Round(time.Millisecond))
	return nil
}
