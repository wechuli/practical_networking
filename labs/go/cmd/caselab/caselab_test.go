package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, url, ca string) AppConfig {
	t.Helper()
	return AppConfig{URL: url, CAFile: ca, Timeout: "2s"}
}

func TestProbeStages(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "api ok")
	}))
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	_ = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	resolv := filepath.Join(dir, "resolv.conf")
	_ = os.WriteFile(resolv, []byte("nameserver 192.0.2.53\nsearch shop.test\noptions timeout:2\n"), 0o600)

	var out strings.Builder
	if err := Probe(context.Background(), writeConfig(t, srv.URL+"/health", ca), resolv, &out); err != nil {
		t.Fatalf("healthy probe failed: %v\n%s", err, out.String())
	}
	for _, want := range []string{"resolv.conf: nameserver 192.0.2.53; search shop.test; options timeout:2", "connect 127.0.0.1:", "tls 127.0.0.1: OK", "http: 200 OK", "api ok"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}

	// a CA that did not sign the server's certificate
	wrong := filepath.Join(dir, "wrong.pem")
	_ = os.WriteFile(wrong, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: mustSelfSigned(t)}), 0o600)
	out.Reset()
	if err := Probe(context.Background(), writeConfig(t, srv.URL, wrong), resolv, &out); err == nil || !strings.Contains(out.String(), "unknown authority") {
		t.Errorf("untrusted CA should fail at TLS:\n%s", out.String())
	}

	// nothing listening
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	out.Reset()
	if err := Probe(context.Background(), writeConfig(t, "https://"+addr+"/", ca), resolv, &out); err == nil || !strings.Contains(out.String(), "connection refused [RST") {
		t.Errorf("closed port should be refused:\n%s", out.String())
	}
}

// mustSelfSigned returns an unrelated self-signed CA certificate.
func mustSelfSigned(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Unrelated Root"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestDescribeDNS(t *testing.T) {
	cases := map[string]*net.DNSError{
		"does not exist":      {Err: "no such host", Name: "api", IsNotFound: true},
		"no answer from":      {Err: "i/o timeout", Name: "api", IsTimeout: true},
		"SERVFAIL or REFUSED": {Err: "server misbehaving", Name: "api"},
	}
	for want, e := range cases {
		if got := DescribeDNS(e); !strings.Contains(got, want) {
			t.Errorf("%v: got %q, want it to mention %q", e, got, want)
		}
	}
}

func TestCase6Dataset(t *testing.T) {
	var out strings.Builder
	if err := Case6("../../../observability/flows.csv", "../../../observability/metrics.json", &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{
		"3 timeouts in 20 attempts (15% of requests)",
		"REJECT records: 198.51.100.90:443 — a different destination",
		"SKIPDATA on eni-case-app",
		"ErrorPortAllocation 3",
		"eni-case-app 10.0.2.11          3 packets",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in\n%s", want, out.String())
		}
	}
	// a dataset where the REJECT is for the failing destination no longer supports the reading
	dir := t.TempDir()
	flows, _ := os.ReadFile("../../../observability/flows.csv")
	bad := strings.Replace(string(flows), "10.0.2.10,198.51.100.90,49155,443,6,3,180,1789027200,1789027259,REJECT", "10.0.2.10,203.0.113.80,49155,443,6,3,180,1789027200,1789027259,REJECT", 1)
	_ = os.WriteFile(filepath.Join(dir, "flows.csv"), []byte(bad), 0o600)
	if err := Case6(filepath.Join(dir, "flows.csv"), "../../../observability/metrics.json", &strings.Builder{}); err == nil {
		t.Error("a REJECT on the failing destination should fail the dataset check")
	}
}
