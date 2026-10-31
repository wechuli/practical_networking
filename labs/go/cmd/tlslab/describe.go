package main

import (
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// VersionName prints a TLS version as people say it.
func VersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	}
	return fmt.Sprintf("0x%04x", v)
}

// ParseVersion accepts 1.0 … 1.3.
func ParseVersion(s string) (uint16, error) {
	switch s {
	case "", "0":
		return 0, nil
	case "1.0":
		return tls.VersionTLS10, nil
	case "1.1":
		return tls.VersionTLS11, nil
	case "1.2":
		return tls.VersionTLS12, nil
	case "1.3":
		return tls.VersionTLS13, nil
	}
	return 0, fmt.Errorf("unknown TLS version %q (use 1.0, 1.1, 1.2 or 1.3)", s)
}

func keyName(c *x509.Certificate) string {
	switch k := c.PublicKey.(type) {
	case *ecdsa.PublicKey:
		return "ECDSA " + k.Curve.Params().Name
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", k.N.BitLen())
	}
	return c.PublicKeyAlgorithm.String()
}

var ekuNames = map[x509.ExtKeyUsage]string{
	x509.ExtKeyUsageServerAuth: "serverAuth", x509.ExtKeyUsageClientAuth: "clientAuth",
	x509.ExtKeyUsageCodeSigning: "codeSigning", x509.ExtKeyUsageAny: "any",
}

// DescribeCert prints the fields a support engineer reads, one per line.
func DescribeCert(c *x509.Certificate, now time.Time) []string {
	var san []string
	san = append(san, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		san = append(san, "IP:"+ip.String())
	}
	if len(san) == 0 {
		san = []string{"(none)"}
	}
	var eku []string
	for _, u := range c.ExtKeyUsage {
		if n, ok := ekuNames[u]; ok {
			eku = append(eku, n)
		}
	}
	left := c.NotAfter.Sub(now)
	status := fmt.Sprintf("%d days left", int(left.Hours()/24))
	switch {
	case now.After(c.NotAfter):
		status = fmt.Sprintf("EXPIRED %d days ago", int(-left.Hours()/24))
	case now.Before(c.NotBefore):
		status = fmt.Sprintf("NOT YET VALID for %d more days", int(c.NotBefore.Sub(now).Hours()/24)+1)
	}
	kind := "leaf"
	if c.IsCA {
		kind = "CA"
	}
	if c.Subject.String() == c.Issuer.String() {
		kind += ", self-signed"
	}
	out := []string{
		fmt.Sprintf("subject  %s (%s)", c.Subject.CommonName, kind),
		fmt.Sprintf("issuer   %s", c.Issuer.CommonName),
		fmt.Sprintf("valid    %s → %s (%s)", c.NotBefore.UTC().Format("2006-01-02"), c.NotAfter.UTC().Format("2006-01-02"), status),
		fmt.Sprintf("key      %s, signature %s", keyName(c), c.SignatureAlgorithm),
	}
	if !c.IsCA {
		out = append(out, "names    "+strings.Join(san, ", "))
		if len(eku) > 0 {
			out = append(out, "usage    "+strings.Join(eku, ", "))
		}
	}
	return out
}

// Explain names the cause behind a Go x509 verification error in plain words.
func Explain(err error) string {
	var inv x509.CertificateInvalidError
	var host x509.HostnameError
	var unk x509.UnknownAuthorityError
	switch {
	case errors.As(err, &inv):
		switch inv.Reason {
		case x509.Expired:
			return "a certificate is outside its validity period (expired, or not yet valid — check the clock too)"
		case x509.IncompatibleUsage:
			return "the certificate is not allowed for this purpose (extended key usage lacks serverAuth or clientAuth)"
		case x509.NotAuthorizedToSign:
			return "a certificate in the chain signed another but is not a CA"
		case x509.TooManyIntermediates:
			return "the chain is longer than a CA's path length allows"
		}
		return "the certificate is invalid: " + inv.Detail
	case errors.As(err, &host):
		return "the name you connected to is not in the certificate's subjectAltName list"
	case errors.As(err, &unk):
		return "no chain from this certificate to a trusted root: an untrusted (or self-signed) issuer, or the server did not send the intermediate"
	}
	return err.Error()
}

// LoadPool reads PEM certificates from a file into a pool.
func LoadPool(path string) (*x509.CertPool, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	pool := x509.NewCertPool()
	n := 0
	for {
		var b *pem.Block
		b, data = pem.Decode(data)
		if b == nil {
			break
		}
		if c, err := x509.ParseCertificate(b.Bytes); err == nil {
			pool.AddCert(c)
			n++
		}
	}
	if n == 0 {
		return nil, 0, fmt.Errorf("%s: no certificates", path)
	}
	return pool, n, nil
}
