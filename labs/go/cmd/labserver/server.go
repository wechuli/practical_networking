package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Config describes the server to build.
type Config struct {
	Addr      string        // HTTPS listen address
	PlainAddr string        // optional plain HTTP listen address
	Name      string        // DNS name for the certificate
	CAOut     string        // where main writes the CA certificate
	BodyBytes int           // response body size
	Delay     time.Duration // wait before writing the response
	ID        string        // X-Served-By value, to tell instances apart (anycast labs)
}

// Server holds the configured HTTPS and HTTP servers and the CA certificate.
type Server struct {
	HTTPS *http.Server
	HTTP  *http.Server
	CAPEM []byte
}

// New creates a CA and a server certificate signed by it, so clients exercise
// a real two-certificate chain rather than a single self-signed leaf.
func New(cfg Config) (*Server, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("a certificate name is required")
	}
	if cfg.BodyBytes < 0 {
		return nil, fmt.Errorf("size must not be negative")
	}
	caCert, caKey, err := newCA()
	if err != nil {
		return nil, err
	}
	leaf, err := newLeaf(cfg.Name, caCert, caKey)
	if err != nil {
		return nil, err
	}
	handler := pageHandler(cfg.BodyBytes, cfg.Delay, cfg.ID)
	s := &Server{
		HTTPS: &http.Server{
			Handler:           handler,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{leaf}},
			ReadHeaderTimeout: 10 * time.Second,
		},
		HTTP:  &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second},
		CAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caCert.Raw}),
	}
	return s, nil
}

func serial() (*big.Int, error) {
	return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
}

func newCA() (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "practical networking labserver CA", Organization: []string{"practical networking course"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(7 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func newLeaf(name string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	sn, err := serial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	// Send the leaf and the CA, as a real server sends its intermediate chain.
	return tls.Certificate{Certificate: [][]byte{der, ca.Raw}, PrivateKey: key}, nil
}

// pageHandler serves an HTML page of exactly size bytes after delay. The
// response is sent in one write, so Content-Length is set and the body is not chunked.
func pageHandler(size int, delay time.Duration, id string) http.Handler {
	if id == "" {
		id = "labserver"
	}
	page := makePage(size)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(page)))
		w.Header().Set("X-Served-By", id)
		_, _ = w.Write(page)
	})
}

func makePage(size int) []byte {
	head := []byte("<!doctype html>\n<html><head><title>labserver</title></head><body>\n")
	tail := []byte("</body></html>\n")
	if size <= len(head)+len(tail) {
		return bytes.Repeat([]byte("x"), size)
	}
	filler := bytes.Repeat([]byte("<p>practical networking lab page</p>\n"), size/37+1)
	page := append(append([]byte{}, head...), filler[:size-len(head)-len(tail)]...)
	return append(page, tail...)
}
