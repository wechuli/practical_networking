package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pki(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := GeneratePKI(dir, time.Now()); err != nil {
		t.Fatal(err)
	}
	return dir
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

func start(t *testing.T, o ServeOptions) string {
	t.Helper()
	o.Addr = freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Serve(ctx, o, discard{}) }()
	for i := 0; i < 100; i++ {
		if c, err := net.Dial("tcp", o.Addr); err == nil {
			c.Close()
			return o.Addr
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server did not start")
	return ""
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func site(dir, name string) string {
	return filepath.Join(dir, name+"-chain.pem") + "," + filepath.Join(dir, name+".key")
}

func inspect(t *testing.T, o InspectOptions) *InspectResult {
	t.Helper()
	if o.Timeout == 0 {
		o.Timeout = 3 * time.Second
	}
	o.Request = true
	r, err := Inspect(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPKIChainsVerify(t *testing.T) {
	dir := pki(t)
	roots, _, err := LoadPool(filepath.Join(dir, "root.pem"))
	if err != nil {
		t.Fatal(err)
	}
	chain, err := readPEMCerts(filepath.Join(dir, "shop-chain.pem"))
	if err != nil || len(chain) != 2 {
		t.Fatalf("%v %d", err, len(chain))
	}
	inter := x509.NewCertPool()
	inter.AddCert(chain[1])
	if _, err := chain[0].Verify(x509.VerifyOptions{DNSName: "www.shop.test", Roots: roots, Intermediates: inter}); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string][2]string{"expired": {"expired.shop.test", "expired"}, "other": {"shop.test", "not shop.test"},
		"clientonly": {"eku.shop.test", "incompatible key usage"}} {
		want := tc[1]
		c, _ := readPEMCerts(filepath.Join(dir, name+"-chain.pem"))
		_, err := c[0].Verify(x509.VerifyOptions{DNSName: tc[0], Roots: roots, Intermediates: inter,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestInspectReportsFailures(t *testing.T) {
	dir := pki(t)
	addr := start(t, ServeOptions{Sites: map[string]string{
		"shop.test": site(dir, "shop"), "expired.shop.test": site(dir, "expired"), "wrong.shop.test": site(dir, "other"),
		"noint.shop.test": filepath.Join(dir, "shop.pem") + "," + filepath.Join(dir, "shop.key"),
	}, Default: site(dir, "default")})
	root := filepath.Join(dir, "root.pem")
	cases := map[string]string{
		"shop.test":         "OK: shop.test → Lab Issuing CA 1 → Lab Root CA",
		"expired.shop.test": "certificate has expired",
		"wrong.shop.test":   "valid for other.test, not wrong.shop.test",
		"noint.shop.test":   "only the leaf was sent",
		"-":                 "doesn't contain any IP SANs",
	}
	for sni, want := range cases {
		r := inspect(t, InspectOptions{Addr: addr, ServerName: sni, CAFile: root})
		out := strings.Join(r.Lines, "\n")
		if !strings.Contains(out, want) {
			t.Errorf("%s: want %q in\n%s", sni, want, out)
		}
		if r.Verified != (sni == "shop.test") {
			t.Errorf("%s: verified=%v", sni, r.Verified)
		}
	}
}

func TestResumptionAndVersions(t *testing.T) {
	dir := pki(t)
	addr := start(t, ServeOptions{Sites: map[string]string{"shop.test": site(dir, "shop")}, Max: tls.VersionTLS12})
	r := inspect(t, InspectOptions{Addr: addr, ServerName: "shop.test", CAFile: filepath.Join(dir, "root.pem"), Resume: true, ALPN: []string{"http/1.1"}})
	out := strings.Join(r.Lines, "\n")
	if !strings.Contains(out, "TLS 1.2") || !strings.Contains(out, "RESUMED") {
		t.Fatal(out)
	}
	r = inspect(t, InspectOptions{Addr: addr, ServerName: "shop.test", Min: tls.VersionTLS13})
	if out := strings.Join(r.Lines, "\n"); !strings.Contains(out, "protocol version") {
		t.Fatal(out)
	}
}

func TestMutualTLS(t *testing.T) {
	dir := pki(t)
	addr := start(t, ServeOptions{Sites: map[string]string{"shop.test": site(dir, "shop")}, ClientCA: filepath.Join(dir, "root.pem")})
	root := filepath.Join(dir, "root.pem")
	r := inspect(t, InspectOptions{Addr: addr, ServerName: "shop.test", CAFile: root})
	if out := strings.Join(r.Lines, "\n"); !strings.Contains(out, "certificate required") {
		t.Fatal(out)
	}
	r = inspect(t, InspectOptions{Addr: addr, ServerName: "shop.test", CAFile: root, CertFile: filepath.Join(dir, "client-chain.pem"), KeyFile: filepath.Join(dir, "client.key")})
	if !r.Verified {
		t.Fatal(strings.Join(r.Lines, "\n"))
	}
}

func TestInterceptMintsForSNI(t *testing.T) {
	dir := pki(t)
	up := start(t, ServeOptions{Sites: map[string]string{"localhost": site(dir, "shop")}, Default: site(dir, "shop")})
	_, port, _ := net.SplitHostPort(up)
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() {
		_ = Intercept(ctx, addr, filepath.Join(dir, "inspection-ca.pem"), filepath.Join(dir, "inspection-ca.key"), "", port, discard{})
	}()
	time.Sleep(100 * time.Millisecond)
	r := inspect(t, InspectOptions{Addr: addr, ServerName: "localhost", CAFile: filepath.Join(dir, "inspection-ca.pem"), ALPN: []string{"http/1.1"}})
	out := strings.Join(r.Lines, "\n")
	if !strings.Contains(out, "issuer   Corp TLS Inspection CA") || !strings.Contains(out, "OK: localhost → Corp TLS Inspection CA") {
		t.Fatal(out)
	}
}

func TestRelayAddsDelay(t *testing.T) {
	dir := pki(t)
	up := start(t, ServeOptions{Sites: map[string]string{"shop.test": site(dir, "shop")}})
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = Relay(ctx, addr, up, 20*time.Millisecond, discard{}) }()
	time.Sleep(100 * time.Millisecond)
	r := inspect(t, InspectOptions{Addr: addr, ServerName: "shop.test", CAFile: filepath.Join(dir, "root.pem"), ALPN: []string{"http/1.1"}})
	var line string
	for _, l := range r.Lines {
		if strings.Contains(l, "TLS handshake") {
			line = l
		}
	}
	// One round trip through the relay is at least 40 ms.
	var hs float64
	if _, err := fmtSscan(line, &hs); err != nil || hs < 40 {
		t.Fatalf("handshake %v ms from %q", hs, line)
	}
}

func TestExplainAndVersions(t *testing.T) {
	if v, err := ParseVersion("1.2"); err != nil || v != tls.VersionTLS12 {
		t.Error(v, err)
	}
	if _, err := ParseVersion("1.4"); err == nil {
		t.Error("1.4 accepted")
	}
	if _, err := ParseCurves("P256,X25519MLKEM768"); err != nil {
		t.Error(err)
	}
	if !strings.Contains(Explain(x509.HostnameError{}), "subjectAltName") {
		t.Error("hostname")
	}
}

// fmtSscan pulls the handshake milliseconds out of an inspect line.
func fmtSscan(line string, hs *float64) (int, error) {
	i := strings.Index(line, "TLS handshake ")
	if i < 0 {
		return 0, net.UnknownNetworkError("no handshake in line")
	}
	return fmtSscanf(line[i+len("TLS handshake "):], hs)
}

func TestAuthorized(t *testing.T) {
	authz := map[string][]string{"orders-service": {"/orders"}, "spiffe://course.test/ns/shop/sa/admin": {"/admin", "/orders"}}
	cases := []struct {
		ids  []string
		path string
		who  string
		ok   bool
	}{
		{[]string{"orders-service"}, "/orders/42", "orders-service", true},
		{[]string{"orders-service"}, "/admin", "", false},
		{[]string{"someone-else"}, "/orders", "", false},
		{[]string{"cn", "spiffe://course.test/ns/shop/sa/admin"}, "/admin/x", "spiffe://course.test/ns/shop/sa/admin", true},
	}
	for _, c := range cases {
		who, ok := Authorized(authz, c.ids, c.path)
		if who != c.who || ok != c.ok {
			t.Errorf("Authorized(%v, %s) = %q, %v; want %q, %v", c.ids, c.path, who, ok, c.who, c.ok)
		}
	}
}
