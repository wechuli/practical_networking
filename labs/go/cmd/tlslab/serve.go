package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServeOptions controls Serve.
type ServeOptions struct {
	Addr     string
	Sites    map[string]string // SNI name → "chain.pem,key.pem"
	Default  string            // "chain.pem,key.pem" for unknown or absent SNI
	Min, Max uint16
	Curves   []tls.CurveID
	ClientCA string // require and verify client certificates against this CA file
	ALPN     []string
	Plain    string // also serve cleartext HTTP here (for the capture demo)
	// Authz, when set, authorizes requests by the client certificate's
	// identity: identity (CN, or a DNS/URI SAN) → allowed path prefixes.
	// A verified identity without a matching prefix gets 403.
	Authz map[string][]string
}

// Identities are the names a verified client certificate proves: its CN,
// DNS SANs and URI SANs (SPIFFE IDs are URI SANs).
func Identities(c *x509.Certificate) []string {
	ids := []string{c.Subject.CommonName}
	ids = append(ids, c.DNSNames...)
	for _, u := range c.URIs {
		ids = append(ids, u.String())
	}
	return ids
}

// Authorized reports whether any of ids may access path.
func Authorized(authz map[string][]string, ids []string, path string) (string, bool) {
	for _, id := range ids {
		for _, prefix := range authz[id] {
			if strings.HasPrefix(path, prefix) {
				return id, true
			}
		}
	}
	return "", false
}

func loadPair(spec string) (tls.Certificate, error) {
	parts := strings.SplitN(spec, ",", 2)
	if len(parts) != 2 {
		return tls.Certificate{}, fmt.Errorf("want CHAIN.pem,KEY.pem, got %q", spec)
	}
	return tls.LoadX509KeyPair(parts[0], parts[1])
}

// Serve runs an HTTPS server that picks its certificate by SNI and logs
// every ClientHello, negotiation and handshake failure.
func Serve(ctx context.Context, o ServeOptions, w io.Writer) error {
	var mu sync.Mutex
	logf := func(format string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		fmt.Fprintf(w, time.Now().Format("15:04:05.000 ")+format+"\n", a...)
	}
	certs := map[string]*tls.Certificate{}
	for name, spec := range o.Sites {
		c, err := loadPair(spec)
		if err != nil {
			return fmt.Errorf("site %s: %w", name, err)
		}
		certs[strings.ToLower(name)] = &c
	}
	var def *tls.Certificate
	if o.Default != "" {
		c, err := loadPair(o.Default)
		if err != nil {
			return err
		}
		def = &c
	}
	cfg := &tls.Config{
		MinVersion:       o.Min,
		MaxVersion:       o.Max,
		CurvePreferences: o.Curves,
		NextProtos:       o.ALPN,
		GetCertificate: func(h *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c, ok := certs[strings.ToLower(h.ServerName)]; ok {
				return c, nil
			}
			if def == nil {
				return nil, fmt.Errorf("no certificate for SNI %q", h.ServerName)
			}
			return def, nil
		},
	}
	if o.ClientCA != "" {
		pool, _, err := LoadPool(o.ClientCA)
		if err != nil {
			return err
		}
		cfg.ClientCAs, cfg.ClientAuth = pool, tls.RequireAndVerifyClientCert
	}
	cfg.GetConfigForClient = func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		sni := h.ServerName
		if sni == "" {
			sni = "(none)"
		}
		var vs []string
		for _, v := range h.SupportedVersions {
			vs = append(vs, VersionName(v))
		}
		logf("ClientHello from %s: SNI %s, versions %s, ALPN %v", h.Conn.RemoteAddr(), sni, strings.Join(vs, " "), h.SupportedProtos)
		return nil, nil
	}
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		client := "no client certificate"
		if len(cs.PeerCertificates) > 0 {
			client = "client certificate " + cs.PeerCertificates[0].Subject.CommonName
		}
		resumed := ""
		if cs.DidResume {
			resumed = ", resumed"
		}
		logf("handshake: SNI %q, %s, %s, ALPN %q, %s%s", cs.ServerName, VersionName(cs.Version), tls.CipherSuiteName(cs.CipherSuite), cs.NegotiatedProtocol, client, resumed)
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		cs := r.TLS
		who := "anonymous"
		if cs != nil && len(cs.PeerCertificates) > 0 {
			who = cs.PeerCertificates[0].Subject.CommonName
		}
		if o.Authz != nil {
			var ids []string
			if cs != nil && len(cs.PeerCertificates) > 0 {
				ids = Identities(cs.PeerCertificates[0])
			}
			if id, ok := Authorized(o.Authz, ids, r.URL.Path); ok {
				logf("authz: %s from %s → %s: allowed for %s", r.URL.Path, r.RemoteAddr, who, id)
			} else {
				logf("authz: %s from %s → %s: denied", r.URL.Path, r.RemoteAddr, who)
				http.Error(rw, fmt.Sprintf("403 forbidden: %s may not access %s", who, r.URL.Path), http.StatusForbidden)
				return
			}
		}
		fmt.Fprintf(rw, "hello from %s over %s (%s, %s), client %s\n", r.Host, VersionName(cs.Version),
			tls.CipherSuiteName(cs.CipherSuite), r.Proto, who)
	})
	srv := &http.Server{Addr: o.Addr, Handler: mux, TLSConfig: cfg, ErrorLog: log.New(logWriter{logf}, "", 0),
		ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", o.Addr)
	if err != nil {
		return err
	}
	var names []string
	for n := range certs {
		names = append(names, n)
	}
	sort.Strings(names)
	logf("tlslab serve: %s, versions %s–%s, sites %s, client certificates %s", ln.Addr(), orAny(o.Min, "TLS 1.2"), orAny(o.Max, "TLS 1.3"),
		strings.Join(names, " "), map[bool]string{true: "required", false: "not requested"}[o.ClientCA != ""])
	if o.Plain != "" {
		plain := &http.Server{Addr: o.Plain, ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			if u, _, ok := r.BasicAuth(); ok {
				fmt.Fprintf(rw, "welcome, %s (this page came over cleartext HTTP)\n", u)
				return
			}
			rw.Header().Set("WWW-Authenticate", `Basic realm="lab"`)
			http.Error(rw, "log in", http.StatusUnauthorized)
		})}
		go func() { _ = plain.ListenAndServe() }()
		go func() { <-ctx.Done(); plain.Close() }()
		logf("tlslab serve: cleartext HTTP on %s", o.Plain)
	}
	go func() { <-ctx.Done(); srv.Close() }()
	return srv.ServeTLS(ln, "", "")
}

func orAny(v uint16, def string) string {
	if v == 0 {
		return def
	}
	return VersionName(v)
}

type logWriter struct{ logf func(string, ...any) }

func (l logWriter) Write(p []byte) (int, error) {
	l.logf("%s", strings.TrimSpace(string(p)))
	return len(p), nil
}

// ParseCurves accepts names like X25519,P256,X25519MLKEM768.
func ParseCurves(s string) ([]tls.CurveID, error) {
	if s == "" {
		return nil, nil
	}
	known := map[string]tls.CurveID{"X25519": tls.X25519, "P256": tls.CurveP256, "P384": tls.CurveP384,
		"P521": tls.CurveP521, "X25519MLKEM768": tls.X25519MLKEM768}
	var out []tls.CurveID
	for _, n := range strings.Split(s, ",") {
		c, ok := known[strings.TrimSpace(n)]
		if !ok {
			return nil, fmt.Errorf("unknown group %q", n)
		}
		out = append(out, c)
	}
	return out, nil
}
