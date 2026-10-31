// Command tlslab is the course's TLS toolkit, built on Go's crypto/tls and
// crypto/x509 only.
//
//	tlslab pki -dir DIR                        # a lab PKI: root, intermediate, good and broken leaves
//	tlslab serve -addr :443 -site shop.test=shop-chain.pem,shop.key … [-default …] [-max 1.2] [-clientca root.pem]
//	tlslab inspect -connect shop.test:443 [-cafile root.pem] [-servername N] [-max 1.2] [-resume] [-keylog F]
//	tlslab get https://shop.test/              # Go's HTTP client: system trust (SSL_CERT_FILE), proxies
//	tlslab relay -listen :4443 -to 198.18.80.10:443 -delay 50ms     # a "long" path
//	tlslab intercept -listen :8443 -ca inspection-ca.pem -cakey inspection-ca.key   # TLS inspection
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, " ") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	fail := func(err error) {
		fmt.Fprintf(os.Stderr, "tlslab: %v\n", err)
		os.Exit(1)
	}
	versions := func(min, max string) (uint16, uint16) {
		lo, err := ParseVersion(min)
		if err != nil {
			fail(err)
		}
		hi, err := ParseVersion(max)
		if err != nil {
			fail(err)
		}
		return lo, hi
	}
	switch os.Args[1] {
	case "pki":
		dir := fs.String("dir", "pki", "output directory")
		_ = fs.Parse(os.Args[2:])
		files, err := GeneratePKI(*dir, time.Now())
		if err != nil {
			fail(err)
		}
		fmt.Printf("tlslab pki: wrote %d files to %s\n", len(files), *dir)
	case "serve":
		var o ServeOptions
		var sites multiFlag
		fs.StringVar(&o.Addr, "addr", ":443", "listen address")
		fs.Var(&sites, "site", "NAME=CHAIN.pem,KEY.pem (repeat): the certificate for that SNI")
		fs.StringVar(&o.Default, "default", "", "CHAIN.pem,KEY.pem for an unknown or missing SNI")
		min := fs.String("min", "", "minimum TLS version (1.0–1.3); default 1.2")
		max := fs.String("max", "", "maximum TLS version; default 1.3")
		curves := fs.String("groups", "", "key exchange groups the server accepts, e.g. P256 (default: Go's)")
		fs.StringVar(&o.ClientCA, "clientca", "", "require client certificates issued by this CA (mTLS)")
		alpn := fs.String("alpn", "h2,http/1.1", "ALPN protocols offered")
		fs.StringVar(&o.Plain, "plain", "", "also serve cleartext HTTP (with a Basic-auth login) on this address")
		var authz multiFlag
		fs.Var(&authz, "authz", "IDENTITY=/path/prefix (repeat): with -clientca, allow that certificate identity (CN or SAN) those paths; others get 403")
		_ = fs.Parse(os.Args[2:])
		o.Min, o.Max = versions(*min, *max)
		var err error
		if o.Curves, err = ParseCurves(*curves); err != nil {
			fail(err)
		}
		o.ALPN = strings.Split(*alpn, ",")
		for _, a := range authz {
			id, prefix, ok := strings.Cut(a, "=")
			if !ok {
				fail(fmt.Errorf("-authz wants IDENTITY=/prefix, got %q", a))
			}
			if o.Authz == nil {
				o.Authz = map[string][]string{}
			}
			o.Authz[id] = append(o.Authz[id], prefix)
		}
		o.Sites = map[string]string{}
		for _, s := range sites {
			name, spec, ok := strings.Cut(s, "=")
			if !ok {
				fail(fmt.Errorf("-site wants NAME=CHAIN,KEY, got %q", s))
			}
			o.Sites[name] = spec
		}
		if err := Serve(ctx, o, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case "inspect":
		var o InspectOptions
		fs.StringVar(&o.Addr, "connect", "", "host:port (required)")
		fs.StringVar(&o.ServerName, "servername", "", "SNI to send (default: the host); - sends none")
		fs.StringVar(&o.VerifyName, "verifyname", "", "name to check the certificate against (default: the SNI)")
		fs.StringVar(&o.CAFile, "cafile", "", "trust only the CAs in this file (default: the system store)")
		min := fs.String("min", "", "minimum TLS version")
		max := fs.String("max", "", "maximum TLS version")
		curves := fs.String("groups", "", "key exchange groups to offer")
		alpn := fs.String("alpn", "h2,http/1.1", "ALPN protocols to offer; empty for none")
		fs.StringVar(&o.CertFile, "cert", "", "client certificate chain (mTLS)")
		fs.StringVar(&o.KeyFile, "key", "", "client key")
		fs.StringVar(&o.KeyLog, "keylog", "", "append session secrets to this file (Wireshark's TLS key log)")
		fs.BoolVar(&o.Resume, "resume", false, "connect twice with a session cache")
		fs.BoolVar(&o.Request, "request", true, "send GET / after the handshake")
		fs.DurationVar(&o.Timeout, "timeout", 5*time.Second, "per-connection timeout")
		_ = fs.Parse(os.Args[2:])
		if o.Addr == "" {
			fs.Usage()
			os.Exit(2)
		}
		if o.Resume {
			o.Request = true // TLS 1.3 tickets arrive after the handshake and are read with the response
		}
		o.Min, o.Max = versions(*min, *max)
		var err error
		if o.Curves, err = ParseCurves(*curves); err != nil {
			fail(err)
		}
		if *alpn != "" {
			o.ALPN = strings.Split(*alpn, ",")
		}
		res, err := Inspect(ctx, o)
		if err != nil {
			fail(err)
		}
		fmt.Println(strings.Join(res.Lines, "\n"))
		if !res.Verified {
			os.Exit(1)
		}
	case "get":
		timeout := fs.Duration("timeout", 5*time.Second, "request timeout")
		_ = fs.Parse(os.Args[2:])
		if fs.NArg() != 1 {
			fs.Usage()
			os.Exit(2)
		}
		c := &http.Client{Timeout: *timeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{}}}
		resp, err := c.Get(fs.Arg(0))
		if err != nil {
			fmt.Printf("go get %s: %v\n", fs.Arg(0), err)
			os.Exit(1)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		fmt.Printf("go get %s: %s, %s, %s: %s", fs.Arg(0), resp.Status, resp.Proto, VersionName(resp.TLS.Version), body)
	case "relay":
		listen := fs.String("listen", ":4443", "listen address")
		to := fs.String("to", "", "target host:port (required)")
		delay := fs.Duration("delay", 50*time.Millisecond, "added delay in each direction")
		_ = fs.Parse(os.Args[2:])
		if err := Relay(ctx, *listen, *to, *delay, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	case "intercept":
		listen := fs.String("listen", ":8443", "listen address (redirect port 443 here)")
		ca := fs.String("ca", "inspection-ca.pem", "inspection CA certificate")
		key := fs.String("cakey", "inspection-ca.key", "inspection CA key")
		upstream := fs.String("upstream-ca", "", "CA file to verify the real servers with (default: system store)")
		port := fs.String("port", "443", "upstream port")
		_ = fs.Parse(os.Args[2:])
		if err := Intercept(ctx, *listen, *ca, *key, *upstream, *port, os.Stdout); err != nil && ctx.Err() == nil {
			fail(err)
		}
	default:
		usage()
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: tlslab pki -dir DIR
       tlslab serve -addr ADDR -site NAME=CHAIN,KEY … [-default CHAIN,KEY] [-min V] [-max V] [-groups G] [-clientca CA [-authz ID=/prefix …]] [-plain ADDR]
       tlslab inspect -connect HOST:PORT [-servername N|-] [-cafile CA] [-min V] [-max V] [-cert C -key K] [-resume] [-keylog F]
       tlslab get URL
       tlslab relay -listen ADDR -to HOST:PORT [-delay D]
       tlslab intercept -listen ADDR -ca CA -cakey KEY [-upstream-ca CA]
`)
	os.Exit(2)
}
