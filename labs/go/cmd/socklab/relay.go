//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// Relay forwards each client connection to the backend. In copy mode it uses
// io.Copy with one 32 KB buffer per direction: when the backend stops
// reading, the relay stops reading too, the client's window closes, and the
// client's write blocks (backpressure). In unbounded mode a reader goroutine
// keeps accepting data into a growing in-memory queue, so the client thinks
// it delivered everything while the relay's memory grows.
func Relay(ctx context.Context, listen, to, mode string, w io.Writer) error {
	if mode != "copy" && mode != "unbounded" {
		return fmt.Errorf("unknown mode %q", mode)
	}
	ln, err := net.Listen("tcp4", listen)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	fmt.Fprintf(w, "socklab relay %s -> %s (%s)\n", ln.Addr(), to, mode)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			b, err := net.Dial("tcp4", to)
			if err != nil {
				fmt.Fprintf(w, "relay: backend: %v\n", err)
				return
			}
			defer b.Close()
			go func() { _, _ = io.Copy(c, b) }()
			start := time.Now()
			var n int64
			if mode == "copy" {
				n, _ = io.Copy(b, c)
			} else {
				n = unbounded(c, b)
			}
			fmt.Fprintf(w, "relay: %s finished after %s, %d bytes accepted from the client\n", c.RemoteAddr(), time.Since(start).Round(time.Millisecond), n)
		}()
	}
}

func unbounded(c, b net.Conn) int64 {
	var mu sync.Mutex
	cond := sync.NewCond(&mu)
	var queue [][]byte
	done := false
	var total int64
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for {
			mu.Lock()
			for len(queue) == 0 && !done {
				cond.Wait()
			}
			if len(queue) == 0 && done {
				mu.Unlock()
				return
			}
			chunk := queue[0]
			queue = queue[1:]
			mu.Unlock()
			if _, err := b.Write(chunk); err != nil {
				return
			}
		}
	}()
	for {
		buf := make([]byte, 64*1024)
		n, err := c.Read(buf)
		if n > 0 {
			mu.Lock()
			queue = append(queue, buf[:n])
			total += int64(n)
			mu.Unlock()
			cond.Signal()
		}
		if err != nil {
			mu.Lock()
			done = true
			mu.Unlock()
			cond.Signal()
			<-drained // the client is done; keep the connection until the backend has it all
			return total
		}
	}
}
