package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// WebSocket (RFC 6455), both ends, by hand.

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// Accept is Sec-WebSocket-Accept for a Sec-WebSocket-Key:
// base64(SHA-1(key + the protocol's fixed GUID)). It proves the server read
// this handshake; it is not authentication.
func Accept(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

const (
	opCont  = 0x0
	opText  = 0x1
	opBin   = 0x2
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

var opNames = map[byte]string{opCont: "continuation", opText: "text", opBin: "binary", opClose: "close", opPing: "ping", opPong: "pong"}

// WSFrame is one frame as it crossed the wire.
type WSFrame struct {
	Fin     bool
	Op      byte
	Masked  bool
	Payload []byte // unmasked
	Wire    int    // bytes on the wire, header included
}

// AppendFrame encodes a frame. Clients must mask (mask != nil); servers must not.
func AppendFrame(b []byte, op byte, payload []byte, mask []byte) []byte {
	b = append(b, 0x80|op)
	m := byte(0)
	if mask != nil {
		m = 0x80
	}
	switch n := len(payload); {
	case n < 126:
		b = append(b, m|byte(n))
	case n < 65536:
		b = append(b, m|126, byte(n>>8), byte(n))
	default:
		b = append(b, m|127)
		b = binary.BigEndian.AppendUint64(b, uint64(n))
	}
	if mask == nil {
		return append(b, payload...)
	}
	b = append(b, mask...)
	for i, c := range payload {
		b = append(b, c^mask[i%4])
	}
	return b
}

// ReadFrame decodes one frame.
func ReadFrame(r io.Reader) (WSFrame, error) {
	var h [2]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return WSFrame{}, err
	}
	f := WSFrame{Fin: h[0]&0x80 != 0, Op: h[0] & 0x0f, Masked: h[1]&0x80 != 0, Wire: 2}
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var e [2]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return f, err
		}
		n, f.Wire = uint64(binary.BigEndian.Uint16(e[:])), f.Wire+2
	case 127:
		var e [8]byte
		if _, err := io.ReadFull(r, e[:]); err != nil {
			return f, err
		}
		n, f.Wire = binary.BigEndian.Uint64(e[:]), f.Wire+8
	}
	if n > 1<<20 {
		return f, fmt.Errorf("frame of %d bytes is too large for this lab", n)
	}
	var mask [4]byte
	if f.Masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return f, err
		}
		f.Wire += 4
	}
	f.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		return f, err
	}
	f.Wire += int(n)
	if f.Masked {
		for i := range f.Payload {
			f.Payload[i] ^= mask[i%4]
		}
	}
	return f, nil
}

func headerHas(h http.Header, key, token string) bool {
	for _, v := range h.Values(key) {
		for _, t := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// serveWebSocket upgrades an HTTP/1.1 request and echoes messages.
func serveWebSocket(w http.ResponseWriter, r *http.Request, log *logger) {
	if r.ProtoMajor != 1 || !headerHas(r.Header, "Connection", "upgrade") || !headerHas(r.Header, "Upgrade", "websocket") {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "this endpoint needs an HTTP/1.1 WebSocket upgrade", http.StatusUpgradeRequired)
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	if r.Header.Get("Sec-WebSocket-Version") != "13" || key == "" {
		w.Header().Set("Sec-WebSocket-Version", "13")
		http.Error(w, "unsupported WebSocket version", http.StatusBadRequest)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", Accept(key))
	_ = rw.Flush()
	log.Printf("ws %s: upgraded (101), key %s", r.RemoteAddr, key)
	start := time.Now()
	for {
		f, err := ReadFrame(rw)
		if err != nil {
			log.Printf("ws %s: connection ended after %s: %v", r.RemoteAddr, time.Since(start).Round(time.Millisecond), err)
			return
		}
		log.Printf("ws %s: recv %s %d bytes (%d on the wire, masked=%v)", r.RemoteAddr, opNames[f.Op], len(f.Payload), f.Wire, f.Masked)
		if !f.Masked {
			_, _ = conn.Write(AppendFrame(nil, opClose, []byte{0x03, 0xea}, nil)) // 1002 protocol error
			log.Printf("ws %s: unmasked client frame; closing with 1002", r.RemoteAddr)
			return
		}
		var out []byte
		switch f.Op {
		case opText, opBin:
			out = AppendFrame(nil, f.Op, f.Payload, nil)
		case opPing:
			out = AppendFrame(nil, opPong, f.Payload, nil)
		case opClose:
			_, _ = conn.Write(AppendFrame(nil, opClose, f.Payload, nil))
			log.Printf("ws %s: close handshake done", r.RemoteAddr)
			return
		}
		if out != nil {
			if _, err := conn.Write(out); err != nil {
				return
			}
		}
	}
}

// WSClient is `weblab ws`: connect, send messages, optionally stay idle
// (with or without pings) to see what a proxy's idle timeout does.
type WSClient struct {
	URL    string
	CAFile string
	Send   []string
	Hold   time.Duration
	Ping   time.Duration
	Out    io.Writer
}

// Run returns an error if the handshake fails or the connection is cut
// while held.
func (c WSClient) Run() error {
	u, err := url.Parse(c.URL)
	if err != nil {
		return err
	}
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), map[string]string{"ws": "80", "wss": "443"}[u.Scheme])
	}
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	if u.Scheme == "wss" {
		cfg := &tls.Config{ServerName: u.Hostname(), NextProtos: []string{"http/1.1"}}
		if c.CAFile != "" {
			pemData, err := os.ReadFile(c.CAFile)
			if err != nil {
				return err
			}
			cfg.RootCAs = x509.NewCertPool()
			cfg.RootCAs.AppendCertsFromPEM(pemData)
		}
		tc := tls.Client(conn, cfg)
		if err := tc.Handshake(); err != nil {
			return err
		}
		conn = tc
	}
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	key := base64.StdEncoding.EncodeToString(nonce)
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", u.RequestURI(), u.Host, key)
	for _, l := range strings.Split(strings.TrimSpace(req), "\r\n") {
		fmt.Fprintf(c.Out, "> %s\n", l)
	}
	if _, err := io.WriteString(conn, req); err != nil {
		return err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "< %s\n", resp.Status)
	for _, k := range []string{"Upgrade", "Connection", "Sec-Websocket-Accept"} {
		if v := resp.Header.Get(k); v != "" {
			fmt.Fprintf(c.Out, "< %s: %s\n", k, v)
		}
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("no upgrade: %s", resp.Status)
	}
	want := Accept(key)
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != want {
		return fmt.Errorf("Sec-WebSocket-Accept %q, want %q", got, want)
	}
	fmt.Fprintf(c.Out, "  accept checks out: base64(sha1(key + %s)) = %s\n", wsGUID, want)
	mask := make([]byte, 4)
	for _, m := range c.Send {
		_, _ = rand.Read(mask)
		frame := AppendFrame(nil, opText, []byte(m), mask)
		fmt.Fprintf(c.Out, "> text %q: %d bytes on the wire (2 header + 4 mask + %d payload): % x\n", m, len(frame), len(m), frame)
		if _, err := conn.Write(frame); err != nil {
			return err
		}
		f, err := ReadFrame(br)
		if err != nil {
			return err
		}
		fmt.Fprintf(c.Out, "< %s %q: %d bytes on the wire, masked=%v\n", opNames[f.Op], f.Payload, f.Wire, f.Masked)
	}
	if c.Hold > 0 {
		fmt.Fprintf(c.Out, "  holding the connection for %s (ping every %s)\n", c.Hold, map[bool]string{true: c.Ping.String(), false: "never"}[c.Ping > 0])
		start := time.Now()
		deadline := start.Add(c.Hold)
		next := start.Add(c.Ping)
		for time.Now().Before(deadline) {
			wait := time.Until(deadline)
			if c.Ping > 0 && time.Until(next) < wait {
				wait = time.Until(next)
			}
			_ = conn.SetReadDeadline(time.Now().Add(wait))
			f, err := ReadFrame(br)
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				if c.Ping > 0 && !time.Now().Before(next) {
					_, _ = rand.Read(mask)
					if _, err := conn.Write(AppendFrame(nil, opPing, []byte("keepalive"), mask)); err != nil {
						return fmt.Errorf("connection lost after %s idle: %w", time.Since(start).Round(time.Millisecond), err)
					}
					next = next.Add(c.Ping)
				}
				continue
			}
			if err != nil {
				fmt.Fprintf(c.Out, "  connection closed by the peer after %s: %v\n", time.Since(start).Round(100*time.Millisecond), err)
				return fmt.Errorf("connection cut after %s", time.Since(start).Round(100*time.Millisecond))
			}
			fmt.Fprintf(c.Out, "< %s at +%s\n", opNames[f.Op], time.Since(start).Round(100*time.Millisecond))
		}
		_ = conn.SetReadDeadline(time.Time{})
		fmt.Fprintf(c.Out, "  still open after %s\n", c.Hold)
	}
	_, _ = rand.Read(mask)
	_, _ = conn.Write(AppendFrame(nil, opClose, []byte{0x03, 0xe8}, mask)) // 1000 normal closure
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for { // skip a late pong; the peer answers our close with its own
		f, err := ReadFrame(br)
		if err != nil {
			return nil
		}
		if f.Op == opClose {
			fmt.Fprintf(c.Out, "< close %d (normal closure)\n", binary.BigEndian.Uint16(append(f.Payload, 0, 0)[:2]))
			return nil
		}
	}
}
