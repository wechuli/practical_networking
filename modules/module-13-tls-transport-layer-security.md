# Module 13: TLS — Transport Layer Security

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 12 — DNS — Domain Name System
> **Estimated time:** 4–5 hours

## Overview

TLS is the "S" in HTTPS, and the layer that protects SMTP, IMAP, database connections, gRPC, Kubernetes API calls and most service-to-service traffic. It sits on top of a reliable transport (TCP, or QUIC's own transport for HTTP/3) and below the application protocol. Without it, anyone on the path — a shared Wi-Fi network, a compromised router, a misconfigured mirror port — reads credentials and data directly from the wire; the lab captures exactly that.

TLS gives three properties: **confidentiality** (only the endpoints can read the data), **integrity** (any modification is detected), and **authentication** of the server — and optionally the client — through certificates. The first two come from the handshake's key exchange and an authenticated cipher. The third, and almost every TLS incident, comes from the certificate system: a chain of signatures from the server's certificate up to a root the client already trusts, and a name in the certificate that matches the name the client asked for.

TLS failures are frequent and slow to diagnose because they surface as vague messages — "SSL error", "handshake failure", "certificate verify failed", "connection reset" — from very different causes: an expired certificate, a server that does not send its intermediate, a name that is not in the certificate, a client whose trust store is missing a root, a protocol version one side refuses, a client certificate that is missing or wrong, or a middlebox that replaces the certificate on purpose. This module teaches the handshake well enough to read it in a capture, and the failures well enough to name each one from the error text — in OpenSSL's words, Go's, Python's, Node's and Java's. The lab builds a private PKI and servers that fail in every common way, and measures what each handshake costs in round trips.

## What You Will Build Toward

- Show, in a capture, what TLS hides (credentials, URLs, the certificate in 1.3) and what it does not (the server name in SNI, sizes, timing)
- Read a TLS 1.2 and a TLS 1.3 handshake message by message, and decrypt one with a key log
- Predict and measure the round trips a handshake costs: TLS 1.3, TLS 1.2, resumption, HelloRetryRequest
- Read a certificate: subject, issuer, subjectAltName, validity, key, extended key usage; follow and verify a chain
- Name the cause of a verification failure from its error text in curl/OpenSSL, Go, Python, Node and Java
- Explain SNI, the default certificate, ECH, ALPN, and mTLS (including why a client sometimes sends no certificate at all)
- Prove that a TLS-inspecting middlebox is in the path, and fix each runtime's trust correctly

---

## 13.1 Why TLS Exists

The lab logs in to a page over plain HTTP with `curl -u alice:s3cret` and captures it:

```
198.18.90.10.33598 > 198.18.91.10.80: Flags [P.], … length 111: HTTP: GET / HTTP/1.1
GET / HTTP/1.1
Authorization: Basic YWxpY2U6czNjcmV0          ← echo YWxpY2U6czNjcmV0 | base64 -d → alice:s3cret
```

Basic authentication is base64, not encryption. The same request over HTTPS produces a capture in which neither the header nor the password appears anywhere — but the string `shop.test` does, once: in the ClientHello's **SNI** extension, which tells the server which site the client wants (13.6). TLS hides content; it does not hide who talks to whom, the server name (unless ECH is used), how much, or when.

```
WHAT TLS PROVIDES
  Confidentiality   Application data is encrypted with a symmetric AEAD cipher
                    (AES-GCM or ChaCha20-Poly1305) under keys only the endpoints know.
  Integrity         Every record carries an authentication tag; a changed bit makes the
                    record fail and the connection end (with an alert).
  Authentication    The server proves it holds the private key for a certificate that
                    names it and chains to a root the client trusts. Optionally the client
                    proves the same (mutual TLS, 13.8).
  NOT provided      Hiding the endpoints' addresses, the SNI (without ECH), traffic volume
                    and timing; authorisation (who may do what); protection of data once it
                    leaves TLS at a load balancer or proxy (13.9).
```

---

## 13.2 Certificates and PKI

### What is in a certificate

An X.509 certificate binds a public key to names, signed by an issuer. The lab's `tlslab inspect` prints the fields that matter:

```
  0 subject  shop.test (leaf)
    issuer   Lab Issuing CA 1
    valid    2026-10-28 → 2027-01-27 (89 days left)
    key      ECDSA P-256, signature ECDSA-SHA256
    names    shop.test, www.shop.test, noint.shop.test        ← subjectAltName (SAN)
    usage    serverAuth                                      ← extended key usage
  1 subject  Lab Issuing CA 1 (CA)
    issuer   Lab Root CA
    valid    2026-10-29 → 2029-10-29 (1095 days left)
```

```
FIELD                  WHAT CLIENTS CHECK
─────────────────────  ──────────────────────────────────────────────────────────────────
subjectAltName (SAN)   The name in the URL must match one entry (RFC 6125). A wildcard
                       *.wild.test matches exactly one label: a.wild.test, but not wild.test
                       or a.b.wild.test (all three measured). IP addresses must appear as IP
                       entries. The subject CN is ignored for hostnames by modern clients.
Validity               notBefore ≤ now ≤ notAfter, by the CLIENT's clock — a wrong clock
                       makes valid certificates "expired" or "not yet valid".
Issuer + signature     Must be signed by the next certificate up the chain.
Basic constraints      Only certificates with CA:TRUE may sign others; pathLen limits depth.
Key usage / EKU        serverAuth for servers, clientAuth for client certificates.
Public key             ECDSA P-256 or RSA 2048+ in practice.
```

### Chains and trust stores

```
  Lab Root CA            self-signed; in the client's TRUST STORE (never needs to be sent)
      │ signs
  Lab Issuing CA 1       intermediate; the SERVER must send it
      │ signs
  shop.test              the leaf; the server sends it first

  The server sends: leaf + intermediate(s)       (the lab's shop-chain.pem; certbot's fullchain.pem)
  The client builds: leaf → intermediate → a root it already has, checks every signature,
  validity, constraint and usage, and that the name matches.
```

A **trust store** is the set of roots a client accepts. Each operating system and many runtimes keep their own (13.10). Public CAs are in the stores because browsers' and OS vendors' root programs accepted them; an organisation's private CA is trusted only where someone installed it.

**Self-signed** certificates are their own issuer. They are fine for a single test endpoint the client explicitly trusts, and for private roots; for anything else, a private CA (one root, then issued certificates) is easier to manage than trusting individual leaves.

### Public certificates today

Publicly trusted certificates are issued after **domain validation**, now almost always automated with ACME (RFC 8555; Let's Encrypt, ZeroSSL, Google Trust Services, and the commercial CAs). Lifetimes are shrinking on a published schedule (CA/Browser Forum ballot SC-081): at most 200 days for certificates issued from 15 March 2026, 100 days from 15 March 2027, and 47 days from 15 March 2029. Let's Encrypt issues 90-day certificates today, moving to 64 days in February 2027 and 45 days in February 2028, with 6-day certificates available on request. The operational consequence is simple: renewal must be automatic and monitored, because manual renewal at this cadence fails. Every publicly trusted certificate is also logged in public **Certificate Transparency** logs (search them at crt.sh), which is how you can find out what certificates exist for your names.

---

## 13.3 The TLS 1.2 Handshake

```
TLS 1.2 FULL HANDSHAKE (ECDHE) — 2 round trips after TCP

  Client                                                   Server
    │── ClientHello ─────────────────────────────────────▶│  versions, cipher suites, random,
    │   (SNI, supported groups, signature algorithms, ALPN)│  extensions
    │◀────────────────────────────────────── ServerHello ─│  chosen suite, random
    │◀────────────────────────────────────── Certificate ─│  chain — IN CLEARTEXT
    │◀──────────────────────────────── ServerKeyExchange ─│  ECDHE public key, signed with the
    │◀────────────────────────────────── ServerHelloDone ─│  certificate's key
    │── ClientKeyExchange ───────────────────────────────▶│  client's ECDHE public key
    │── ChangeCipherSpec, Finished (encrypted) ──────────▶│  both now derive the keys
    │◀────────────────────── ChangeCipherSpec, Finished ──│
    │══ application data ════════════════════════════════│
```

The lab's capture of a TLS 1.2 handshake, decoded with tshark (handshake message types: 1 ClientHello, 2 ServerHello, 11 Certificate, 12 ServerKeyExchange, 14 ServerHelloDone, 16 ClientKeyExchange):

```
198.18.90.10 → :8443   1          shop.test   583 bytes     ClientHello, SNI visible
198.18.91.10 →         2,11,12,14             1243 bytes    ServerHello … ServerHelloDone, certificate visible
198.18.90.10 → :8443   16                     159 bytes     ClientKeyExchange
```

**Key exchange.** With **ECDHE**, each side generates a fresh key pair for this connection and they agree on a secret no one else can compute; the certificate's key only signs the exchange. Recorded traffic stays safe even if the server's private key leaks later — **forward secrecy**. The older **RSA key exchange**, in which the client encrypted the secret to the server's certificate key, has none: steal the key once, decrypt every recorded session. TLS 1.3 removed it.

**Cipher suites** in TLS 1.2 name everything at once:

```
TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256      (what the lab negotiated)
    │     │          │           └── hash for the key derivation (PRF)
    │     │          └── bulk cipher and mode: AES-128 in GCM (AEAD)
    │     └── certificate / signature type: ECDSA (RSA for an RSA certificate)
    └── key exchange: ECDHE
```

Use ECDHE with AES-GCM or ChaCha20-Poly1305. Avoid static RSA key exchange, CBC-mode suites (a long history of padding-oracle attacks), RC4, 3DES, and anything EXPORT or NULL. TLS 1.0 and 1.1 are formally deprecated (RFC 8996); the lab's server that only speaks them is refused by every modern client (13.7).

---

## 13.4 TLS 1.3

```
TLS 1.3 FULL HANDSHAKE — 1 round trip after TCP

  Client                                                   Server
    │── ClientHello + key_share ─────────────────────────▶│  the client GUESSES a group and
    │   (SNI, supported_versions, groups, ALPN)            │  sends its public key at once
    │◀──────────────────────────── ServerHello + key_share│  keys are derived HERE
    │◀───── {EncryptedExtensions} {Certificate}            │  everything from here on is
    │       {CertificateVerify} {Finished} ───────────────│  encrypted, including the certificate
    │── {Finished} ──────────────────────────────────────▶│
    │══ application data (the client may send right away) │
    │◀──────────────────────────── {NewSessionTicket} ────│  for resumption, after the handshake
```

The lab's capture of TLS 1.3 shows a passive observer **only two handshake messages**: the ClientHello (with SNI) and the ServerHello. The certificate and everything after travel inside encrypted records. With the session secrets curl wrote to `SSLKEYLOGFILE`, tshark decrypts the rest: `8,11,15,20,4` — EncryptedExtensions, Certificate, CertificateVerify, Finished, NewSessionTicket — and the request itself, `GET shop.test /`.

What else changed:

```
TLS 1.3 (RFC 8446, 2018)
  Removed   static RSA and static DH key exchange, CBC and RC4 suites, SHA-1 and MD5 signatures,
            compression, renegotiation, custom DH groups
  Suites    only the AEAD and hash: TLS_AES_128_GCM_SHA256, TLS_AES_256_GCM_SHA384,
            TLS_CHACHA20_POLY1305_SHA256 (plus two CCM suites). Key exchange and authentication
            are negotiated separately (groups, signature algorithms), so "TLS_AES_128_GCM_SHA256"
            says nothing about them.
  Groups    X25519 and P-256 classically; the hybrid post-quantum group X25519MLKEM768 is now the
            default in Chrome, Firefox, Go 1.24+ and OpenSSL 3.5+
  Resumption  PSK from a session ticket, normally combined with a fresh (EC)DHE exchange
  0-RTT     a resuming client may send "early data" with its first flight. It can be replayed by
            an attacker and is not forward-secret, so servers must accept it only for requests
            that are safe to repeat — many disable it.
```

**The post-quantum key share makes ClientHellos bigger.** The lab captured both clients' ClientHellos: curl with OpenSSL 3.0 sent a **583-byte** frame; Go 1.24, which adds an X25519MLKEM768 share of 1,216 bytes, sent **1,555 bytes**. A ClientHello that no longer fits in one TCP segment breaks some middleboxes that expect it to — one cause of "TLS fails from this network only, with this newer client".

---

## 13.5 What a Handshake Costs: Round Trips, Measured

The handshake's cost is round trips, so it hurts most on long paths. The lab puts a relay in front of the server that adds 25 ms in each direction — 50 ms per round trip — and measures with `tlslab inspect` (Go) and curl's `%{time_appconnect}`. The TCP handshake is with the relay and costs nothing; everything after it pays 50 ms per round trip. Three runs:

| Handshake | Expected | Go, full | Go, resumed | curl |
| --- | --- | --- | --- | --- |
| TLS 1.3 | 1 RTT | 52.7 / 52.8 / 52.7 ms | 52.3 / 52.8 / 52.1 ms | 58 / 55 / 61 ms |
| TLS 1.2 | 2 RTT (1 resumed) | 103.8 / 103.3 / 110.7 ms | 51.9 / 51.7 / 51.6 ms | 107 / 106 / 108 ms |
| TLS 1.3, server accepts only P-256 | 2 RTT (HelloRetryRequest) | 103.4 / 103.7 / 104.0 ms | 103.3 / 103.1 / 102.8 ms | 106 / 107 / 106 ms |

Three lessons:

- **TLS 1.3 saves a round trip** over TLS 1.2 on every new connection. Added to TCP's handshake, a new HTTPS connection costs 2 RTTs with TLS 1.3 and 3 with TLS 1.2 before the first request byte — 300 ms versus 450 ms at 150 ms RTT.
- **Resumption saves the round trip in TLS 1.2, not in TLS 1.3** (where it is already one). In 1.3 it saves the certificate transfer and signature work instead — the resumed handshake carried no Certificate.
- **A HelloRetryRequest silently doubles the cost.** The client guessed groups (X25519MLKEM768, X25519) the server does not accept, so the server asks again. Resumption does not help, because the client keeps guessing the same way. A server (or load balancer) whose group list does not include what clients send by default pays an extra round trip on every connection — invisible unless you measure.

For latency, the fixes are protocol-level: TLS 1.3, connection reuse (HTTP keep-alive, HTTP/2 multiplexing, pools), session resumption, and terminating TLS close to users (CDNs).

---

## 13.6 SNI, ALPN and ECH

**SNI** (Server Name Indication) puts the requested hostname in the ClientHello, so one IP address can serve many sites with different certificates — every CDN, shared load balancer and Kubernetes ingress depends on it. The lab's server holds fourteen names; it picks the certificate by SNI. A client that sends no SNI gets the server's **default certificate**: connecting to `https://198.18.91.10/` (curl and Go send no SNI for an IP address) returned the `default.test` certificate and failed with `x509: cannot validate certificate for 198.18.91.10 because it doesn't contain any IP SANs` (Go) / `no alternative certificate subject name matches target host name '198.18.91.10'` (curl). The same happens with old clients and with tools invoked against an IP or a different name than the certificate's. `openssl s_client -servername NAME` and `curl --resolve NAME:443:IP` / `--connect-to` test a specific name against a specific address.

**ALPN** (Application-Layer Protocol Negotiation) lets client and server agree on HTTP/2 (`h2`) or HTTP/1.1 inside the handshake. The lab's server offers `h2,http/1.1`; curl and Go chose `h2` unless told otherwise. A TLS-terminating proxy that does not offer `h2` silently downgrades clients to HTTP/1.1 — visible in the lab's inspection experiment.

**ECH** (Encrypted Client Hello, RFC 9849, published March 2026) encrypts the real ClientHello — SNI included — inside an outer ClientHello with a public name, using keys the server publishes in DNS (the HTTPS record, Module 12). It needs support on both sides (it is enabled in Chrome and Firefox, and on large CDNs) and hides the name from passive observers and from middleboxes that filter or route on SNI. Ordinary TLS 1.3 by itself does not hide SNI.

---

## 13.7 Certificate Validation and Its Failures

Validation is a series of independent checks; each failure has its own message. The lab serves eleven names with one trust anchor (the lab root) and asks Go's `crypto/x509` and curl/OpenSSL about each:

| Site | What is wrong | Go (`crypto/x509`) | curl / OpenSSL (exit 60) |
| --- | --- | --- | --- |
| shop.test | nothing | OK: shop.test → Lab Issuing CA 1 → Lab Root CA | exit 0 |
| noint.shop.test | server sends only the leaf | `x509: certificate signed by unknown authority` | `unable to get local issuer certificate` |
| expired.shop.test | notAfter was yesterday | `x509: certificate has expired or is not yet valid: current time … is after …` | `certificate has expired` |
| future.shop.test | notBefore is in two days | `… is not yet valid: current time … is before …` | `certificate is not yet valid` |
| wrong.shop.test | certificate for other.test | `x509: certificate is valid for other.test, not wrong.shop.test` | `no alternative certificate subject name matches target host name 'wrong.shop.test'` |
| a.wild.test | `*.wild.test` | OK | exit 0 |
| wild.test | wildcard does not cover the apex | `certificate is valid for *.wild.test, not wild.test` | `no alternative certificate subject name matches …` |
| a.b.wild.test | wildcard covers one label | `certificate is valid for *.wild.test, not a.b.wild.test` | `no alternative certificate subject name matches …` |
| self.shop.test | self-signed | `x509: certificate signed by unknown authority` | `self-signed certificate` |
| rogue.shop.test | issued by an untrusted CA | `x509: certificate signed by unknown authority` | `unable to get local issuer certificate` |
| eku.shop.test | clientAuth-only certificate | `x509: certificate specifies an incompatible key usage` | `unsuitable certificate purpose` |

And outside the certificates:

| Situation | Go | curl / OpenSSL |
| --- | --- | --- |
| Good chain, lab root not in the client's store | `x509: certificate signed by unknown authority` | `unable to get local issuer certificate` |
| Server allows only TLS 1.0/1.1 | `remote error: tls: protocol version not supported` | `(35) … tlsv1 alert protocol version` |
| No SNI, default certificate | `cannot validate certificate for 198.18.91.10 because it doesn't contain any IP SANs` | `no alternative certificate subject name matches target host name '198.18.91.10'` |

Reading the table as a support engineer:

- **"Unknown authority" / "unable to get local issuer certificate" has three common causes** — the server did not send its intermediate, the issuer is not trusted by *this* client, or a middlebox replaced the certificate (13.10). Look at what was sent (`openssl s_client -showcerts`, `tlslab inspect`) and who issued it before changing anything. A missing intermediate often "works in the browser" because browsers cache intermediates or fetch them from the certificate's AIA URL; command-line tools and most libraries do not.
- **Expiry is evaluated by the client's clock.** A device with a wrong clock reports valid certificates as expired or not yet valid.
- **Name mismatches are about the name the client used**, which may differ from the one the user typed: a service connecting by IP, an internal alias, a load balancer health check, a wrong SNI.
- **Wrong EKU** appears when a client certificate is installed as a server certificate, or when a CA hierarchy splits server and client issuance (13.8).

**Revocation.** OCSP and CRLs let a CA say "this certificate is no longer valid before its expiry". In practice browsers use their own pushed revocation sets, OCSP is being retired (Let's Encrypt ended its OCSP service in 2025 in favour of CRLs), and most non-browser clients do not check revocation at all — one reason lifetimes are being shortened. Certificate Transparency is not revocation: it makes issuance visible.

---

## 13.8 Mutual TLS

In mTLS the server sends a CertificateRequest listing the CAs it accepts, and the client answers with its own Certificate and a CertificateVerify signature. The lab's mTLS server trusts the lab root for clients:

```
no client certificate          Go:   TLS handshake 1.3 ms: TLS 1.3 …          ← the handshake "succeeds"
                                     request after the handshake FAILED: remote error: tls: certificate required
                               curl: (56) OpenSSL SSL_read: … tlsv13 alert certificate required
lab client certificate         curl: hello from shop.test:9443 over TLS 1.3 …, client orders-service
client cert from another CA    curl: (56) OpenSSL SSL_read: … tlsv1 alert unknown ca
                               server log: tls: failed to verify certificate: x509: certificate signed by unknown authority
                               Go:   certificate required — Go never sent it
```

Two behaviours explain many mTLS tickets:

- **In TLS 1.3 the client finishes its side of the handshake before the server has checked the client certificate**, so a rejection arrives as an alert on the first read or write, not as a handshake error. Applications report it from wherever they first touch the connection — "connection reset", "certificate required" on a read, a broken HTTP/2 stream.
- **A client may send no certificate at all.** Go (and many other stacks) select a certificate whose issuer is in the server's list of acceptable CAs; if none matches, they send nothing, and the server reports "certificate required" or "client didn't provide a certificate" even though one was configured. Check the issuer of the client certificate against what the server asks for (`openssl s_client` prints "Acceptable client certificate CA names").

mTLS authenticates *who* the client is; deciding *what it may do* is still authorisation, in the application or proxy (Module 18). Since mid-2026 public CAs no longer issue certificates with the clientAuth EKU (Chrome's root program requires separate server and client hierarchies; Let's Encrypt stopped in 2026), so mTLS client certificates come from private CAs — service meshes, SPIFFE, an internal PKI.

---

## 13.9 TLS in Practice

### openssl s_client and friends

```bash
openssl s_client -connect shop.test:443 -servername shop.test -showcerts </dev/null    # chain as sent
openssl s_client -connect host:443 -servername host -verify_hostname host -verify_return_error </dev/null
openssl s_client -connect host:443 -servername host </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
openssl s_client -connect host:443 -tls1_2          # or -tls1_3; -groups P-256; -alpn h2
openssl s_client -connect host:443 -CAfile root.pem -cert client.pem -key client.key   # mTLS
openssl x509 -in cert.pem -noout -text              # everything in a certificate file
openssl verify -CAfile root.pem -untrusted intermediate.pem leaf.pem                  # a chain offline
curl -v https://host/ ; curl -w '%{time_connect} %{time_appconnect}\n' -o /dev/null -s https://host/
tlslab inspect -connect host:443 [-cafile CA] [-servername N|-] [-max 1.2] [-resume] [-keylog F]
```

`-servername` sets SNI; it does **not** turn on hostname verification (`-verify_hostname` does). `s_client`'s `Verify return code:` line is the summary: 0 (ok), 10 (expired), 18 (self-signed), 19 (self-signed in chain), 20 (unable to get local issuer certificate), 21 (unable to verify the first certificate), 62 (hostname mismatch).

### Decrypting with a key log

Clients built on OpenSSL, NSS and BoringSSL (curl, Firefox, Chrome) write their session secrets to the file named by `SSLKEYLOGFILE`; Go writes them through `tls.Config.KeyLogWriter` (`tlslab inspect -keylog FILE`). Wireshark/tshark decrypt a capture with that file (`-o tls.keylog_file:keys.log`). The file decrypts everything it covers: create it with `umask 077`, keep it out of tickets, and delete it after use.

### Termination, re-encryption, passthrough

```
termination     client ══TLS══▶ load balancer ──plain──▶ backend      LB sees HTTP; backend sees the LB
re-encryption   client ══TLS══▶ load balancer ══TLS══▶ backend        two separate TLS sessions
passthrough     client ══════════════TLS═══════════════▶ backend      LB routes on SNI only
```

Wherever TLS terminates, the client's certificate (mTLS), its TLS version and cipher, and its IP address stop at that hop unless forwarded explicitly (headers such as `X-Forwarded-For`, `X-Forwarded-Client-Cert`, or the PROXY protocol). The backend's own certificate, in re-encryption, is validated by the load balancer — with its own trust store and its own name rules.

---

## 13.10 Trust Stores per Runtime and TLS-Inspecting Proxies

### Every runtime has its own trust

| Runtime | Default trust | How to add a CA |
| --- | --- | --- |
| curl, Python `ssl`, Ruby, PHP (OpenSSL) | the OS bundle (`/etc/ssl/certs/ca-certificates.crt` on Debian/Ubuntu) | `--cacert`, `CURL_CA_BUNDLE`; `SSL_CERT_FILE` / `SSL_CERT_DIR` for OpenSSL-based libraries |
| Python `requests` | `certifi`'s bundled Mozilla list, **not** the OS store | `REQUESTS_CA_BUNDLE`, or `truststore` to use the OS store |
| Go | the OS bundle | `SSL_CERT_FILE` / `SSL_CERT_DIR` (replace, not add), or a `RootCAs` pool in code |
| Node.js | a bundled Mozilla list | `NODE_EXTRA_CA_CERTS` (adds); `--use-openssl-ca` or `--use-system-ca` (newer versions) for the OS store |
| Java | its own `cacerts` keystore | `keytool -importcert` into a truststore; `-Djavax.net.ssl.trustStore=…` |
| Docker daemon (registries) | the OS store | `/etc/docker/certs.d/<registry>/ca.crt` |
| OS store itself | — | Ubuntu: copy to `/usr/local/share/ca-certificates/NAME.crt`, run `update-ca-certificates` (also refreshes Java's `cacerts` on Debian/Ubuntu) |

That is why "it works in the browser and in curl, but not in our Node service" (or in the Java job, or in `requests`) is common: each one consulted a different list. Containers add another layer: a minimal image may ship with no CA bundle at all, or an old one.

### TLS inspection

Corporate firewalls, secure web gateways, some antivirus products and CI egress proxies **inspect TLS**: they terminate the client's connection with a certificate they mint on the fly for the requested name, signed by their own CA, and open their own connection to the real server. Machines managed by the organisation trust that CA; anything else — a CI runner's container, a language runtime with its own store, a developer's VM — fails.

The lab's router runs such a proxy. With inspection on (`iptables … REDIRECT --to-ports 8443`), the same `tlslab inspect` from the client shows a different issuer, and three runtimes each trusting only the lab root fail in their own words:

```
client$ tlslab inspect -connect shop.test:443 -cafile root.pem
    issuer   Corp TLS Inspection CA
  go error: x509: certificate signed by unknown authority
router$ tlslab inspect -connect shop.test:443 -cafile root.pem       ← from outside the inspected path
    issuer   Lab Issuing CA 1
verify shop.test …: OK: shop.test → Lab Issuing CA 1 → Lab Root CA

curl:    (60) SSL certificate problem: self-signed certificate in certificate chain
Go:      tls: failed to verify certificate: x509: certificate signed by unknown authority
Python:  URLError … [SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed: self-signed certificate in certificate chain
Node:    SELF_SIGNED_CERT_IN_CHAIN self-signed certificate in certificate chain           (by hand)
Java:    PKIX path building failed: … unable to find valid certification path to requested target   (by hand)
proxy log: real certificate issued by "Lab Issuing CA 1", client shown one from "Corp TLS Inspection CA"
```

"Self-signed certificate in certificate chain" is the typical inspection signature: the proxy sends its own (self-signed) CA certificate with the leaf. **Proving inspection** is one comparison: the issuer seen from inside the network versus from outside it (another network, a phone hotspot, a cloud VM) — or simply an issuer you do not recognise, named after a security product.

**Fixing it correctly** means adding the inspection CA to the trust each runtime uses — the lab's fix check builds a bundle of the lab root plus the inspection CA, and curl (`--cacert`), Go (`SSL_CERT_FILE`) and Python (`SSL_CERT_FILE`) then all succeed; Node (`NODE_EXTRA_CA_CERTS`) and Java (`keytool -importcert`) were fixed the same way by hand. Do **not** disable verification (`curl -k`, `verify=False`, `InsecureSkipVerify`, `NODE_TLS_REJECT_UNAUTHORIZED=0`): that accepts every interceptor, not just your employer's. Also expect inspection to break things it cannot imitate: mTLS to the real server (the proxy cannot present the client's certificate), certificate pinning, and HTTP/2 when the proxy does not offer `h2`. Ask for the destination to be exempted from inspection in those cases.

This course's own build environment is an example: it sets `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE` and `NODE_EXTRA_CA_CERTS` to a proxy's CA bundle — which is why every lab command unsets them first, so results do not depend on the shell they run from.

---

## Hands-On Lab: A Private PKI and Servers That Fail on Purpose

### Lab Goals

- Capture cleartext credentials, then the same request over TLS; read TLS 1.2 and 1.3 handshakes and decrypt one
- Measure handshake round trips: TLS 1.3, TLS 1.2, resumption, HelloRetryRequest; compare ClientHello sizes
- Produce eleven certificate verdicts and read them in Go and curl; no SNI; a protocol-version refusal
- mTLS with no, a valid and a foreign client certificate
- Detect a TLS-inspecting middlebox and fix curl, Go and Python trust (Node and Java by hand)

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `curl`, `tcpdump`, `tshark` (Wireshark's command-line tool), `iptables`, `python3`; optionally Node.js and a JDK for Part 2
- Part 3 uses your own machine and the internet

The lab uses [`tlslab`](../labs/go/cmd/tlslab), written for this module on Go's `crypto/tls` and `crypto/x509`: `pki` creates the lab PKI (a root, an issuing CA, a good leaf and ten broken ones, client certificates, a rogue CA and an "inspection" CA); `serve` is an HTTPS server that picks its certificate by SNI and logs every ClientHello, negotiation and handshake failure; `inspect` (the `tlsinspect` lab once planned) connects, prints the version, cipher, ALPN, timing, the chain as sent, and verifies it separately so a failure still shows every certificate; `relay` adds a fixed delay to a path; `intercept` is a transparent TLS-inspecting proxy; `get` is Go's HTTP client with the system trust store.

---

### Lab Part 1: Run the TLS Lab

[`tls.json`](../labs/netlab/tls.json):

```
  client 198.18.90.10 ──── router 198.18.90.1 ──── server 198.18.91.10
  /etc/hosts: shop.test     relays :4443 → :443     :443  HTTPS, 14 names by SNI (+ :80 cleartext login)
  and 13 other names        :4444 → :8443           :8443 TLS 1.2 only
  → 198.18.91.10            :4445 → :7443           :7443 key exchange group P-256 only
                            (+25 ms each way)       :9443 mTLS (client certificates from the lab root)
                            :8443 intercepting      :1443 TLS 1.0–1.1 only
                            proxy (off until a check
                            redirects port 443 to it)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/tls.json        # about 10 seconds
```

The excerpts in 13.1–13.10 come from three runs; the full outputs and the captures are in the evidence directory (`handshake-types.txt`, `decrypted.txt`, `rtt-*.txt`, `failures-go.txt`, `failures-curl.txt`, `inspected-*.txt`, …). Check 5's decoded handshake list, for example:

```
check 5/30: what a passive observer can read: in 1.3 only ClientHello (1) and ServerHello (2); in 1.2 also …
  | 198.18.90.10 443 1 shop.test 583
  | 198.18.91.10 44998 2  1536
  | 198.18.90.10 8443 1 shop.test 583
  | 198.18.91.10 43270 2,11,12,14  1243
  | 198.18.90.10 8443 16  159
```

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/tls.json
alias lab='sudo ./bin/netlab exec ../netlab/tls.json'
P=$(ls -d /tmp/netlab-tls-*/pki | tail -1)       # the PKI the lab generated (path printed by "up")
```

The lab's checks unset proxy and CA-bundle variables (`HTTPS_PROXY`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, `NODE_OPTIONS`, `JAVA_TOOL_OPTIONS`); if your shell sets any of them, unset them in the commands below too.


1. **Read the chain with OpenSSL.** `lab client -- openssl s_client -connect shop.test:443 -servername shop.test -CAfile $P/root.pem -showcerts </dev/null` — two certificates (`0 s:CN = shop.test`, `1 s:CN = Lab Issuing CA 1`) and `Verify return code: 0 (ok)`. Repeat for `noint.shop.test`: one certificate and `Verify return code: 21 (unable to verify the first certificate)`. Then verify offline: `openssl verify -CAfile $P/root.pem -untrusted $P/intermediate.pem $P/shop.pem` → `OK`.
2. **Node and Java under inspection.** Turn inspection on (`lab router -- iptables -t nat -A PREROUTING -i eth0 -p tcp --dport 443 -j REDIRECT --to-ports 8443`). With `NODE_EXTRA_CA_CERTS=$P/root.pem`, `https.get('https://shop.test/', {agent:false}, …)` fails with `SELF_SIGNED_CERT_IN_CHAIN`; with a bundle of root plus inspection CA it returns 200. For Java, import the lab root into a truststore (`keytool -importcert -noprompt -alias labroot -file $P/root.pem -keystore lab.jks -storepass changeit`) and run a `java.net.http.HttpClient` request with `-Djavax.net.ssl.trustStore=lab.jks`: `PKIX path building failed … unable to find valid certification path to requested target`; import `$P/inspection-ca.pem` too and it returns 200. (Unset `NODE_EXTRA_CA_CERTS`, `NODE_OPTIONS` and `JAVA_TOOL_OPTIONS` first if your shell sets them.)
3. **The server's view of a bad client.** `lab client -- curl -sk --cert $P/rogue-client-chain.pem --key $P/rogue-client.key https://shop.test:9443/` and read the server's log in the evidence directory (`service-server-mtls.log`): `tls: failed to verify certificate: x509: certificate signed by unknown authority`. Then compare with Go: `lab client -- ./bin/tlslab inspect -connect shop.test:9443 -cafile $P/root.pem -cert $P/rogue-client-chain.pem -key $P/rogue-client.key` reports `certificate required`, because Go chose not to send a certificate the server's CA list does not cover.
4. **Force TLS 1.2 or a group.** `lab client -- ./bin/tlslab inspect -connect 198.18.90.1:4443 -servername shop.test -cafile $P/root.pem -max 1.2` takes ~100 ms where 1.3 takes ~50; `-groups P256` against the P-256-only relay (`:4445`) avoids the HelloRetryRequest and brings it back to ~50 ms.

`sudo ./bin/netlab down ../netlab/tls.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
openssl s_client -connect example.com:443 -servername example.com </dev/null 2>/dev/null | openssl x509 -noout -subject -issuer -dates -ext subjectAltName
curl -sv https://example.com/ -o /dev/null 2>&1 | grep -E 'SSL connection|ALPN|subject|issuer|expire'
curl -w 'tcp %{time_connect}s tls %{time_appconnect}s\n' -o /dev/null -s https://example.com/   # your real RTT cost
echo | openssl s_client -connect example.com:443 -servername example.com -tls1_2 2>/dev/null | grep -E 'Protocol|Cipher'
# Is something inspecting your traffic? Compare the issuer here and from another network:
echo | openssl s_client -connect github.com:443 -servername github.com 2>/dev/null | openssl x509 -noout -issuer
# What exists for your domain (Certificate Transparency):  https://crt.sh/?q=example.com
```

With a browser: open the site's certificate viewer, and compare `chrome://net-internals` / `about:networking` with what curl reports.

---

## Real-World Troubleshooting Scenario

### Scenario: "HTTPS Fails for Some Customers After the Certificate Renewal"

**The situation.** The certificate on `api.example.com` was renewed on Monday by a new automation job. Browsers are fine. On Tuesday, two partners report failures from their integrations: one Java service says `PKIX path building failed`, one Python script says `certificate verify failed: unable to get local issuer certificate`. Your own `curl https://api.example.com/` from a laptop works.

**Step 1 — What does the server send, from outside?**

```bash
openssl s_client -connect api.example.com:443 -servername api.example.com -showcerts </dev/null 2>/dev/null | grep -E '^ [0-9] s:|^   i:'
 0 s:CN = api.example.com
   i:C = US, O = Let's Encrypt, CN = E7
```

One certificate. The previous deployment served two: the leaf and its intermediate.

**Step 2 — Why does it work for some clients?** `openssl s_client … -verify_return_error` from outside fails with `Verify return code: 21 (unable to verify the first certificate)`, so the chain really is broken. Browsers hide this: they keep intermediates they have seen before and can fetch a missing one from the URL in the certificate's Authority Information Access extension. The engineer's own curl works for a different reason — `curl -v` shows the certificate it validated was issued by the company's inspection CA: the corporate proxy terminates TLS and presents its own complete chain, so that test never saw the real server's chain. The partners' Java and Python clients have no intermediate cache and fail.

**Step 3 — Root cause.** The new job deployed `cert.pem` (leaf only) instead of `fullchain.pem`. Nothing in the job checked the served chain.

**The fix.** Serve the full chain (`fullchain.pem`, or leaf followed by the intermediate), reload, and verify from outside with a client that does not cache intermediates: `openssl s_client … -verify_return_error`, or `tlslab inspect -connect api.example.com:443`. Add that check to the renewal job and to monitoring alongside expiry — with certificates renewed every 45–90 days, chain mistakes reach production often.

**What made it fast:** looking at what the server *sends* rather than what a browser *shows*; knowing that browsers paper over missing intermediates; and noticing that the engineer's own successful test went through an inspecting proxy and proved nothing about the partners' path.

---

## Common Failure Patterns

| Symptom | TLS cause | How to confirm | Fix |
| --- | --- | --- | --- |
| `certificate has expired` / `ERR_CERT_DATE_INVALID` | Expired (or the client's clock is wrong) | `openssl x509 -noout -dates`; check the client clock | Renew; automate and monitor renewal |
| `unable to get local issuer certificate`, Java `PKIX path building failed` | Missing intermediate, untrusted root, or inspection | `s_client -showcerts`: how many certificates, which issuer | Serve the full chain; trust the right CA in that runtime |
| Works in browsers, fails in code or CI | Intermediate missing (browsers compensate) or runtime trust store differs | Test with `openssl s_client -verify_return_error` from the failing host | Full chain; per-runtime CA configuration (13.10) |
| `self-signed certificate in certificate chain` | TLS inspection (or a private CA not trusted here) | Compare the issuer inside vs outside the network | Trust the inspection CA properly, or get an exemption |
| Hostname mismatch | Name not in SAN; wildcard depth; connecting by IP or alias; wrong SNI | `x509 -ext subjectAltName`; what name did the client use? | Reissue with the right SANs; use the right name/SNI |
| Wrong certificate entirely (a default one) | No SNI or unknown SNI at a shared front end | `s_client` with and without `-servername` | Send SNI; configure the name on the front end |
| `protocol version` alert | One side TLS 1.0/1.1 only, or minimum set too high | `s_client -tls1_2` / `-tls1_3` | Upgrade the old side; align minimums |
| `handshake failure` alert | No common cipher suite, group or signature algorithm | `s_client -cipher`/`-groups`; server config | Modern defaults on both sides |
| Every handshake is one round trip slower than expected | TLS 1.2, or HelloRetryRequest from a narrow group list | `curl -w %{time_appconnect}`; capture shows a second ServerHello | TLS 1.3; accept X25519 (and the PQ hybrid) |
| TLS fails from one network with new clients only | Middlebox mishandles a large (post-quantum) ClientHello | Capture: ClientHello split across segments, then reset | Fix or bypass the middlebox |
| mTLS: `certificate required` though a certificate is configured | Client cert's issuer not in the server's acceptable CAs (client sends none) | `s_client` "Acceptable client certificate CA names" | Issue from the CA the server trusts; fix the server's CA list |
| mTLS error appears after the handshake | TLS 1.3 reports the server's rejection on first read | Server log; alert on first read | As above; read server logs, not only client errors |
| `unsuitable certificate purpose` / `incompatible key usage` | EKU lacks serverAuth (or clientAuth) | `x509 -text` → Extended Key Usage | Reissue with the right EKU |
| HTTP/2 silently becomes HTTP/1.1 | A TLS-terminating proxy without `h2` in ALPN | `curl -v` ALPN line, directly vs through the proxy | Enable h2 on the proxy |

---

## Summary

- TLS encrypts and authenticates the content of a connection; the SNI (unless ECH is used), endpoints, sizes and timing remain visible. Basic auth over plain HTTP is readable on the wire.
- TLS 1.2 needs two round trips (one when resumed) and sends the certificate in cleartext; TLS 1.3 needs one, encrypts everything after the ServerHello, and gets forward secrecy from (EC)DHE. A HelloRetryRequest adds a round trip to every connection. Measured through a 50 ms relay: ~52 ms, ~103 ms, ~52 ms resumed (1.2), ~104 ms with HRR.
- A certificate is trusted when the chain the server sends links to a root in the client's store, every certificate is valid by the client's clock, the usage is right, and the name the client used is in the SAN list. Each failure has a distinct message — learn them in OpenSSL's and Go's words.
- Servers must send their intermediates; browsers hide the mistake, libraries do not.
- SNI selects the certificate; no SNI gets the default certificate. ALPN negotiates HTTP/2. ECH (RFC 9849) encrypts the ClientHello.
- mTLS authenticates clients; in TLS 1.3 rejections arrive after the handshake, and clients may send no certificate if its issuer does not match the server's list. Public CAs no longer issue clientAuth certificates.
- Each runtime has its own trust store; TLS-inspecting middleboxes replace certificates and must be trusted explicitly in each — never by disabling verification.
- Certificate lifetimes are falling to 47 days by 2029: automate renewal and monitor both expiry and the served chain.

## What's Next

Module 14 covers network performance — latency, bandwidth, the bandwidth-delay product, MTU and PMTUD in depth, and how to measure rather than guess where time goes. This module's round-trip measurements are a first example: a handshake's cost is the path's RTT times the number of round trips.

---

## Quick Reference Card

```
MODULE 13: TLS — QUICK REFERENCE
════════════════════════════════

PROVIDES      confidentiality · integrity · server (and optionally client) authentication
DOES NOT HIDE SNI (without ECH) · IPs · sizes · timing

HANDSHAKES    TLS 1.2: 2 RTT (1 resumed), certificate in cleartext
              TLS 1.3: 1 RTT (resumed: 1 RTT, no certificate), all but Client/ServerHello encrypted
              HelloRetryRequest: +1 RTT every time (server lacks the client's key share group)
              + TCP's 1 RTT before any of it; 0-RTT early data is replayable

MESSAGE TYPES 1 ClientHello · 2 ServerHello · 4 NewSessionTicket · 8 EncryptedExtensions
              11 Certificate · 12 ServerKeyExchange · 14 ServerHelloDone · 15 CertificateVerify
              16 ClientKeyExchange · 20 Finished

CHAIN         leaf (server sends) → intermediate (server sends) → root (client's trust store)
VALIDATION    signatures · validity (client clock) · CA constraints · EKU · name ∈ SAN
              *.example.com matches a.example.com only — not example.com, not a.b.example.com

ERRORS        curl/OpenSSL                          Go
              unable to get local issuer certificate  x509: certificate signed by unknown authority
              self-signed certificate (in chain)      x509: certificate signed by unknown authority
              certificate has expired / not yet valid x509: certificate has expired or is not yet valid
              no alternative … matches target host    x509: certificate is valid for X, not Y
              unsuitable certificate purpose          x509: certificate specifies an incompatible key usage
              tlsv1 alert protocol version            remote error: tls: protocol version not supported
              tlsv13 alert certificate required       remote error: tls: certificate required
              s_client Verify return codes: 0 ok · 10 expired · 18/19 self-signed · 20 local issuer · 21 first cert · 62 hostname

COMMANDS      openssl s_client -connect H:443 -servername H -showcerts [-verify_hostname H -verify_return_error]
              openssl x509 -noout -subject -issuer -dates -ext subjectAltName
              openssl verify -CAfile root.pem -untrusted inter.pem leaf.pem
              curl -v · curl -w '%{time_connect} %{time_appconnect}' · curl --resolve/--connect-to
              tlslab inspect -connect H:443 [-cafile] [-servername|-] [-max 1.2] [-resume] [-keylog]
              SSLKEYLOGFILE=keys.log …; tshark -o tls.keylog_file:keys.log

TRUST         curl/OpenSSL/Python ssl: OS bundle, SSL_CERT_FILE · requests: certifi, REQUESTS_CA_BUNDLE
              Go: OS bundle, SSL_CERT_FILE/SSL_CERT_DIR · Node: bundled, NODE_EXTRA_CA_CERTS
              Java: cacerts, keytool · Docker registries: /etc/docker/certs.d/<host>/ca.crt
              Ubuntu OS store: /usr/local/share/ca-certificates/*.crt + update-ca-certificates
INSPECTION    issuer differs inside vs outside · "self-signed certificate in certificate chain"
              fix: trust the inspection CA per runtime, or exempt the destination — never -k

LIFETIMES     public certs ≤ 200 days (from 2026-03-15) → 100 (2027) → 47 (2029)
              Let's Encrypt 90 → 64 days (Feb 2027) → 45 (Feb 2028); no clientAuth since 2026
```

---

## References

- RFC 8446 — The Transport Layer Security (TLS) Protocol Version 1.3
- RFC 5246 — The Transport Layer Security (TLS) Protocol Version 1.2
- RFC 8996 — Deprecating TLS 1.0 and TLS 1.1
- RFC 9849 — TLS Encrypted Client Hello (2026)
- RFC 7301 — TLS Application-Layer Protocol Negotiation (ALPN); RFC 6066 — TLS extensions (SNI)
- RFC 5280 — Internet X.509 PKI Certificate and CRL Profile
- RFC 6125 / RFC 9525 — Service identity verification (hostname matching)
- RFC 8555 — ACME; RFC 9162 — Certificate Transparency 2.0
- CA/Browser Forum Ballot SC-081v3 — schedule of reducing certificate validity (2025)
- Let's Encrypt: "Decreasing Certificate Lifetimes to 45 Days"; "Ending TLS Client Authentication Certificate Support in 2026"; "Ending OCSP Support in 2025"
- Go `crypto/tls` and `crypto/x509` documentation; `man openssl-s_client`, `man openssl-x509`, `man openssl-verify`
- Wireshark TLS decryption guide (key log files)
- Mozilla Server Side TLS / SSL Configuration Generator — https://ssl-config.mozilla.org/
- Certificate search — https://crt.sh/
