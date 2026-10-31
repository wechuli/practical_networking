package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// A lab certificate authority: a certificate and the key that signs with it.
type authority struct {
	cert *x509.Certificate
	key  crypto.Signer
}

func newKey(rsaKey bool) (crypto.Signer, error) {
	if rsaKey {
		return rsa.GenerateKey(rand.Reader, 2048)
	}
	return ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

// LeafSpec describes one end-entity certificate.
type LeafSpec struct {
	File      string
	CN        string
	DNS       []string
	IPs       []net.IP
	NotBefore time.Time
	NotAfter  time.Time
	EKU       []x509.ExtKeyUsage
	RSA       bool
}

func makeCA(cn string, parent *authority, pathLen int, now time.Time, years int) (*authority, error) {
	key, err := newKey(false)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Networking Course Lab"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(years, 0, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            pathLen,
		MaxPathLenZero:        pathLen == 0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	signerCert, signerKey := tmpl, crypto.Signer(key)
	if parent != nil {
		signerCert, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signerCert, key.Public(), signerKey)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return &authority{cert, key}, err
}

func makeLeaf(s LeafSpec, issuer *authority) (*x509.Certificate, crypto.Signer, error) {
	key, err := newKey(s.RSA)
	if err != nil {
		return nil, nil, err
	}
	ku := x509.KeyUsageDigitalSignature
	if s.RSA {
		ku |= x509.KeyUsageKeyEncipherment
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: s.CN},
		DNSNames:              s.DNS,
		IPAddresses:           s.IPs,
		NotBefore:             s.NotBefore,
		NotAfter:              s.NotAfter,
		KeyUsage:              ku,
		ExtKeyUsage:           s.EKU,
		BasicConstraintsValid: true,
	}
	parent, signer := tmpl, key
	if issuer != nil {
		parent, signer = issuer.cert, issuer.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), signer)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func writePEM(path, kind string, der []byte, mode os.FileMode) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), mode)
}

func writeKey(path string, key crypto.Signer) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "PRIVATE KEY", der, 0o600)
}

// GeneratePKI writes the course's lab PKI to dir:
//
//	root.pem                    the lab root CA (what clients should trust)
//	intermediate.pem            the issuing CA, signed by the root
//	NAME.pem / NAME-chain.pem   a leaf, and leaf + intermediate (what servers should send)
//	NAME.key                    its private key
//	inspection-ca.pem / .key    a separate "corporate inspection" CA for the intercepting proxy
//	rogue-root.pem              an unrelated root that signs rogue.pem
func GeneratePKI(dir string, now time.Time) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	root, err := makeCA("Lab Root CA", nil, 1, now, 10)
	if err != nil {
		return nil, err
	}
	inter, err := makeCA("Lab Issuing CA 1", root, 0, now, 3)
	if err != nil {
		return nil, err
	}
	inspection, err := makeCA("Corp TLS Inspection CA", nil, 0, now, 5)
	if err != nil {
		return nil, err
	}
	rogue, err := makeCA("Rogue Root CA", nil, 0, now, 5)
	if err != nil {
		return nil, err
	}
	var written []string
	for name, ca := range map[string]*authority{"root": root, "intermediate": inter, "inspection-ca": inspection, "rogue-root": rogue} {
		if err := writePEM(filepath.Join(dir, name+".pem"), "CERTIFICATE", ca.cert.Raw, 0o644); err != nil {
			return nil, err
		}
		written = append(written, name+".pem")
	}
	if err := writeKey(filepath.Join(dir, "inspection-ca.key"), inspection.key); err != nil {
		return nil, err
	}
	srv := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	day := 24 * time.Hour
	valid := func(s LeafSpec) LeafSpec {
		if s.NotBefore.IsZero() {
			s.NotBefore = now.Add(-day)
		}
		if s.NotAfter.IsZero() {
			s.NotAfter = now.Add(90 * day)
		}
		if s.EKU == nil {
			s.EKU = srv
		}
		return s
	}
	leaves := []struct {
		spec   LeafSpec
		issuer *authority
	}{
		{valid(LeafSpec{File: "shop", CN: "shop.test", DNS: []string{"shop.test", "www.shop.test", "noint.shop.test"}}), inter},
		{valid(LeafSpec{File: "rsa", CN: "rsa.shop.test", DNS: []string{"rsa.shop.test"}, RSA: true}), inter},
		{valid(LeafSpec{File: "expired", CN: "expired.shop.test", DNS: []string{"expired.shop.test"}, NotBefore: now.Add(-91 * day), NotAfter: now.Add(-day)}), inter},
		{valid(LeafSpec{File: "future", CN: "future.shop.test", DNS: []string{"future.shop.test"}, NotBefore: now.Add(2 * day), NotAfter: now.Add(92 * day)}), inter},
		{valid(LeafSpec{File: "other", CN: "other.test", DNS: []string{"other.test"}}), inter},
		{valid(LeafSpec{File: "wild", CN: "*.wild.test", DNS: []string{"*.wild.test"}}), inter},
		{valid(LeafSpec{File: "clientonly", CN: "eku.shop.test", DNS: []string{"eku.shop.test"}, EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}), inter},
		{valid(LeafSpec{File: "default", CN: "default.test", DNS: []string{"default.test"}}), inter},
		{valid(LeafSpec{File: "api", CN: "api.shop.test", DNS: []string{"api.shop.test", "api"}}), inter}, // an internal service also known by its short name (capstone 2 and 4)
		{valid(LeafSpec{File: "selfsigned", CN: "self.shop.test", DNS: []string{"self.shop.test"}}), nil},
		{valid(LeafSpec{File: "rogue", CN: "rogue.shop.test", DNS: []string{"rogue.shop.test"}}), rogue},
		{valid(LeafSpec{File: "client", CN: "orders-service", EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}), inter},
		{valid(LeafSpec{File: "rogue-client", CN: "orders-service", EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}), rogue},
	}
	for _, l := range leaves {
		cert, key, err := makeLeaf(l.spec, l.issuer)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", l.spec.File, err)
		}
		base := filepath.Join(dir, l.spec.File)
		if err := writePEM(base+".pem", "CERTIFICATE", cert.Raw, 0o644); err != nil {
			return nil, err
		}
		chain := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		if l.issuer == inter {
			chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: inter.cert.Raw})...)
		}
		if err := os.WriteFile(base+"-chain.pem", chain, 0o644); err != nil {
			return nil, err
		}
		if err := writeKey(base+".key", key); err != nil {
			return nil, err
		}
		written = append(written, l.spec.File+".pem", l.spec.File+"-chain.pem", l.spec.File+".key")
	}
	return written, nil
}

// signFor mints a certificate for name, signed by ca — what an
// intercepting proxy does for every site it inspects.
func signFor(name string, ca *authority) (*x509.Certificate, crypto.Signer, error) {
	now := time.Now()
	s := LeafSpec{CN: name, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour), EKU: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(name); ip != nil {
		s.IPs = []net.IP{ip}
	} else {
		s.DNS = []string{name}
	}
	return makeLeaf(s, ca)
}
