package main

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"
)

// Relay forwards TCP connections to target, holding every chunk for delay
// in each direction — a stand-in for a long path, so handshake round trips
// can be counted by their cost. The client's own TCP handshake is with the
// relay and is not delayed; everything after it is.
func Relay(ctx context.Context, listen, target string, delay time.Duration, w io.Writer) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	fmt.Fprintf(w, "tlslab relay: %s → %s, +%s each way (+%s per round trip)\n", ln.Addr(), target, delay, 2*delay)
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer c.Close()
			u, err := net.DialTimeout("tcp", target, 5*time.Second)
			if err != nil {
				fmt.Fprintf(w, "relay: %v\n", err)
				return
			}
			defer u.Close()
			var wg sync.WaitGroup
			wg.Add(2)
			go func() { defer wg.Done(); delayedCopy(u, c, delay) }()
			go func() { defer wg.Done(); delayedCopy(c, u, delay) }()
			wg.Wait()
		}()
	}
}

type chunk struct {
	at   time.Time
	data []byte
}

func delayedCopy(dst, src net.Conn, delay time.Duration) {
	q := make(chan chunk, 1024)
	go func() {
		defer close(q)
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				q <- chunk{time.Now().Add(delay), append([]byte(nil), buf[:n]...)}
			}
			if err != nil {
				return
			}
		}
	}()
	for ch := range q {
		time.Sleep(time.Until(ch.at))
		if _, err := dst.Write(ch.data); err != nil {
			break
		}
	}
	if tc, ok := dst.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
}

// Intercept is a transparent TLS-inspecting proxy, like a corporate
// firewall's: it accepts connections redirected to it, reads the SNI, mints
// a certificate for that name signed by its own CA, and opens its own
// verified TLS connection to the real server, relaying the plaintext between
// the two. Clients that do not trust the inspection CA fail verification.
func Intercept(ctx context.Context, listen, caCert, caKey, upstreamCA string, port string, w io.Writer) error {
	ca, err := loadAuthority(caCert, caKey)
	if err != nil {
		return err
	}
	var upstreamRoots *x509.CertPool
	if upstreamCA != "" {
		if upstreamRoots, _, err = LoadPool(upstreamCA); err != nil {
			return err
		}
	}
	var mu sync.Mutex
	minted := map[string]*tls.Certificate{}
	cfg := &tls.Config{GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
		name := h.ServerName
		if name == "" {
			return nil, fmt.Errorf("no SNI: cannot tell which site to impersonate")
		}
		mu.Lock()
		defer mu.Unlock()
		if c, ok := minted[name]; ok {
			return c, nil
		}
		cert, key, err := signFor(name, ca)
		if err != nil {
			return nil, err
		}
		c := &tls.Certificate{Certificate: [][]byte{cert.Raw, ca.cert.Raw}, PrivateKey: key, Leaf: cert}
		minted[name] = c
		return c, nil
	}}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	go func() { <-ctx.Done(); ln.Close() }()
	fmt.Fprintf(w, "tlslab intercept: %s, impersonating every site with %q\n", ln.Addr(), ca.cert.Subject.CommonName)
	for {
		raw, err := ln.Accept()
		if err != nil {
			return err
		}
		go func() {
			defer raw.Close()
			c := tls.Server(raw, cfg)
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			if err := c.HandshakeContext(ctx); err != nil {
				fmt.Fprintf(w, "intercept: client %s: handshake failed: %v\n", raw.RemoteAddr(), err)
				return
			}
			name := c.ConnectionState().ServerName
			up, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", net.JoinHostPort(name, port),
				&tls.Config{ServerName: name, RootCAs: upstreamRoots})
			if err != nil {
				fmt.Fprintf(w, "intercept: %s: upstream failed: %v\n", name, err)
				return
			}
			defer up.Close()
			fmt.Fprintf(w, "intercept: %s for %s: real certificate issued by %q, client shown one from %q\n",
				name, raw.RemoteAddr(), up.ConnectionState().PeerCertificates[0].Issuer.CommonName, ca.cert.Subject.CommonName)
			_ = c.SetDeadline(time.Time{})
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(up, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
			<-done
		}()
	}
}

func loadAuthority(certFile, keyFile string) (*authority, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, err
	}
	key, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("%s: unusable key", keyFile)
	}
	return &authority{cert: cert, key: key}, nil
}

// readPEMCerts is used by tests.
func readPEMCerts(path string) ([]*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []*x509.Certificate
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			return out, nil
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}
