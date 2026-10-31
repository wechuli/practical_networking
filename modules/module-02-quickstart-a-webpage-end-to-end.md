# Module 2: Quickstart — A Webpage End to End

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 1 — History of Networking; the Go toolbox from Module 0
> **Estimated time:** 2 hours

## Overview

Before the course dives into individual protocols and mechanisms, this module walks you through a single HTTPS request from start to finish. You will see every protocol layer enter and exit the picture — DNS, TCP, TLS, HTTP — as one cohesive sequence, not as isolated concepts. The details of each step are covered in depth in later modules. Right now, focus on the sequence, the shape of the conversation, and how each tool shows it.

A **fresh** HTTPS request over TCP — no cached name, no open connection — follows five steps:

```
DNS resolution → TCP connection → TLS handshake → HTTP request/response → connection close
```

That sequence is the skeleton of this course. Everything else is understanding each bone — and knowing which error belongs to which bone.

## What You Will Build Toward

- Name the five steps of a fresh HTTPS request and the protocol responsible for each
- Annotate real `curl -v` output, a DevTools timing breakdown, and a packet capture with those steps
- Read `curl -w` timings correctly (they are cumulative) and compare them with `phases`
- Explain what changes the sequence: caching, connection reuse, Happy Eyeballs, proxies, CDNs, redirects, HTTP/3
- Map an error message from curl, Go, or a browser to the step that failed
- Show that a healthy connection can end with an RST, and why

---

## 2.1 The 10,000-Foot View

When you type `https://example.com` in a browser and press Enter, the browser has one goal: retrieve and render the HTML at that URL. To do that, it must solve a series of problems, each handled by a different protocol:

```
THE FIVE STEPS OF A FRESH HTTPS REQUEST

  Problem                            Protocol   What happens
  ─────────────────────────────────  ─────────  ─────────────────────────────────────────
  "I have a name, need an address"   DNS        Ask a resolver: "What are the IPv4 (A)
                                                and IPv6 (AAAA) addresses of example.com?"

  "I have an address, need a         TCP        Open a reliable, ordered byte stream to
   reliable channel"                            that address on port 443.
                                                (3-way handshake: SYN → SYN-ACK → ACK)
                                                Routers, not TCP, find the path.

  "I have a channel, need trust      TLS        Agree on keys, verify the server's
   and privacy"                                 certificate, agree on the HTTP version (ALPN).

  "I have trust, need data"          HTTP       Send a GET; receive status, headers, body.

  "I'm done"                         TLS + TCP  close_notify, then FIN — or immediately,
                                                an RST. Often much later: connections are
                                                kept open for reuse.
```

This is a fresh HTTPS-over-TCP example. Caching, connection reuse, proxies,
plaintext protocols, and HTTP/3 change the sequence (2.4). Timings in diagrams are
synthetic illustrations; transcripts marked *captured* are real output. A slow
step narrows hypotheses without uniquely locating a fault.

---

## 2.2 Step-by-Step Walkthrough

This course uses documentation addresses such as `203.0.113.10` (RFC 5737) where a
real address would go out of date.

### Step 1 — DNS Resolution

The client needs an IP address. It does not know where `example.com` lives until it asks.

```
DNS RESOLUTION ON A TYPICAL UBUNTU HOST (simplified)

  Application             C library / OS           Recursive resolver      Root, TLD,
  (curl, a Go program)    (getaddrinfo, NSS)       (ISP, cloud, 1.1.1.1)   authoritative
     │                        │                        │                        │
     │ "example.com?"         │                        │                        │
     │───────────────────────▶│ 1. /etc/hosts          │                        │
     │                        │ 2. stub resolver       │                        │
     │                        │    (systemd-resolved,  │                        │
     │                        │    127.0.0.53)         │                        │
     │                        │── A? and AAAA? ───────▶│                        │
     │                        │                        │── (walks the hierarchy │
     │                        │                        │    on a cache miss) ──▶│
     │                        │                        │◀── answers + TTLs ─────│
     │                        │◀── 203.0.113.10, ──────│                        │
     │◀── addresses ──────────│    2001:db8::10        │                        │

  Cost: from well under a millisecond (cached nearby) to tens or hundreds of
  milliseconds (cache miss, distant resolver).
```

Two details matter in practice:

- **Not every program uses the same path.** Browsers may run their own DNS client,
  sometimes over HTTPS (DoH), and keep their own cache. Go programs use Go's own
  resolver by default and read `/etc/resolv.conf` themselves (Module 12). So
  `dig` working does not prove the application resolves the same way.
- **IPv4 and IPv6 are asked in parallel.** If AAAA records exist, the client may try
  IPv6 first (2.4).

If DNS fails here, nothing else happens. A browser shows `ERR_NAME_NOT_RESOLVED`,
curl exits with code 6, Go reports `no such host`. This is why DNS is the first
thing to check in any connectivity failure.

### Step 2 — TCP 3-Way Handshake

With an address in hand, the client opens a TCP connection to port 443.

```
TCP 3-WAY HANDSHAKE

  Client                          Server (203.0.113.10:443)
     │                                 │
     │  SYN ──────────────────────────▶│   "Can we talk?" (+ MSS, window scale, SACK)
     │◀────────────────────── SYN-ACK  │   "Yes, I'm here."
     │  ACK ──────────────────────────▶│   "Great, let's go."
     │                                 │
     │  Connection ESTABLISHED         │
     │  Cost: 1 round-trip time        │

  At this point, there is a reliable, ordered byte stream between
  the client and the server. Nothing on it is encrypted yet.
```

### Step 3 — TLS Handshake

Immediately after the TCP handshake, the client starts TLS on the same connection.

```
TLS 1.3 HANDSHAKE — 1 round trip

  Client                                       Server
     │  ClientHello ─────────────────────────────▶│
     │   - versions: TLS 1.3, 1.2                 │
     │   - cipher suites, key share (X25519)      │
     │   - SNI: example.com   ← picks the cert    │
     │   - ALPN: h2, http/1.1 ← offers HTTP versions
     │                                            │
     │◀── ServerHello (+ key share) ──────────────│  clear text
     │◀── {EncryptedExtensions} ──────────────────│  ← ALPN choice: "h2"
     │◀── {Certificate} ──────────────────────────│  ← server's chain
     │◀── {CertificateVerify} ────────────────────│  ← proves it holds the key
     │◀── {Finished} ─────────────────────────────│
     │                                            │
     │   client verifies chain, name, and dates   │
     │── {Finished} ─────────────────────────────▶│  (the HTTP request can
     │                                            │   follow immediately)
     │  ══ Encrypted from here ══                 │

  {braces} = already encrypted. A capture shows the ClientHello and
  ServerHello, but not the certificate.
```

When the handshake succeeds, the client has keys shared only with this server, and
it has verified that the server holds the private key for a certificate a trusted
authority issued for this name. Both sides have also agreed which HTTP version to
speak, through **ALPN**. That agreement is why the same URL is HTTP/2 in curl and
HTTP/1.1 in a client that offers only `http/1.1`.

### Step 4 — HTTP Request and Response

Now the client sends the request over the encrypted connection. With ALPN `h2`, it
is HTTP/2: binary frames, with headers shown in lower case.

```
HTTP/2 REQUEST/RESPONSE (inside TLS)

  Client                                       Server
     │  connection preface + SETTINGS ───────────▶│
     │  HEADERS (stream 1)                        │
     │    :method GET  :path /                    │
     │    :authority example.com ────────────────▶│
     │                                            │  server works...
     │◀──────────── HEADERS (stream 1) ───────────│
     │               :status 200                  │
     │               content-type: text/html      │
     │               content-length: 1256         │
     │◀──────────── DATA (stream 1) ──────────────│
     │               <!doctype html>...           │

  With HTTP/1.1 the same exchange is text: "GET / HTTP/1.1", "Host: ...",
  then "HTTP/1.1 200 OK". Subresources (CSS, JS, images) reuse this
  connection — as more HTTP/2 streams, or in sequence with HTTP/1.1.
```

### Step 5 — Closing the Connection

The connection closes when a side is finished with it — after one request for a
command-line tool, after an idle timeout for a browser or connection pool. The
textbook close is four segments (FIN, ACK, FIN, ACK). Real closes are often
different, and this one, captured from a real curl request (lab Part 4), is typical:

```
CAPTURED CLOSE (curl 8.5, loopback)

  Client                                       Server
     │── TLS Alert: close_notify ───────────────▶│   "no more TLS data from me"
     │── FIN ───────────────────────────────────▶│   curl has closed its socket
     │◀─────────────────────────────────── ACK   │
     │◀──────────────── TLS Alert: close_notify  │   server answers the alert...
     │── RST ───────────────────────────────────▶│   ...to a socket that no longer
                                                     exists, so the client kernel
                                                     answers with a reset
```

**An RST at the end of a connection is not, by itself, an error.** It means data
arrived for a socket that had already been closed. Treat an RST as a problem when
it arrives *before* the conversation is complete — mid-response, or in reply to a
SYN (Modules 11 and 22).

The side that closes first normally enters TIME_WAIT. Duration and tuple reuse are
implementation-dependent; Linux commonly uses about 60 seconds.

### The Full Picture

```
FULL REQUEST TIMELINE — illustrative, 15 ms RTT, fresh connection

  t=0ms    DNS query sent (A and AAAA)
  t=15ms   DNS answer: 203.0.113.10
  t=15ms   TCP SYN sent
  t=30ms   SYN-ACK received; ACK sent — connection established     (1 RTT)
  t=30ms   TLS ClientHello sent
  t=45ms   ServerHello … Finished received                          (1 RTT)
  t=45ms   TLS Finished + HTTP request sent together
  t=60ms   + server think time → first response byte arrives       (≥ 1 RTT)
  t=65ms   last body byte received
  later    close (after the client is done, or an idle timeout)

  At least 3 round trips before the first byte, plus DNS on a cache miss.
  On a 150 ms intercontinental path the same request needs ≥ 450 ms.
```

---

## 2.3 Seeing It Yourself

Four tools show the same request at different depths: `curl -v` (text), `phases`
(timings per step), browser DevTools (timings in a GUI), and a packet capture
(what was on the wire).

### curl -v — The Conversation in Text

*Captured* with curl 8.5.0 against the course's local test server (lab Part 1).
Progress-meter lines are removed. Against the internet the steps are identical; the
names, addresses, and certificate details differ.

```
* Added example.test:8443:127.0.0.1 to DNS cache            ← --resolve: DNS skipped
* Hostname example.test was found in DNS cache
*   Trying 127.0.0.1:8443...                                ← step 2: SYN sent
* Connected to example.test (127.0.0.1) port 8443           ← step 2: handshake done
* ALPN: curl offers h2,http/1.1                             ← step 3 begins
* TLSv1.3 (OUT), TLS handshake, Client hello (1):
*  CAfile: /tmp/labserver-ca.pem
*  CApath: /etc/ssl/certs
* TLSv1.3 (IN), TLS handshake, Server hello (2):
* TLSv1.3 (IN), TLS handshake, Encrypted Extensions (8):
* TLSv1.3 (IN), TLS handshake, Certificate (11):
* TLSv1.3 (IN), TLS handshake, CERT verify (15):
* TLSv1.3 (IN), TLS handshake, Finished (20):
* TLSv1.3 (OUT), TLS change cipher, Change cipher spec (1): ← compatibility only (below)
* TLSv1.3 (OUT), TLS handshake, Finished (20):
* SSL connection using TLSv1.3 / TLS_AES_128_GCM_SHA256 / X25519 / id-ecPublicKey
* ALPN: server accepted h2                                  ← why it is HTTP/2
* Server certificate:
*  subject: CN=example.test
*  start date: Oct 28 12:09:27 2026 GMT
*  expire date: Nov  4 13:09:27 2026 GMT
*  subjectAltName: host "example.test" matched cert's "example.test"
*  issuer: O=practical networking course; CN=practical networking labserver CA
*  SSL certificate verify ok.                               ← step 3 done
*   Certificate level 0: Public key type EC/prime256v1 (256/128 Bits/secBits), signed using ecdsa-with-SHA256
*   Certificate level 1: Public key type EC/prime256v1 (256/128 Bits/secBits), signed using ecdsa-with-SHA256
* TLSv1.3 (IN), TLS handshake, Newsession Ticket (4):       ← for resumption later
* using HTTP/2
* [HTTP/2] [1] OPENED stream for https://example.test:8443/
> GET / HTTP/2                                              ← step 4: request
> Host: example.test:8443
> User-Agent: curl/8.5.0
> Accept: */*
>
< HTTP/2 200                                                ← step 4: response
< content-type: text/html; charset=utf-8
< x-served-by: labserver
< content-length: 1256
< date: Wed, 28 Oct 2026 13:09:28 GMT
<
* Connection #0 to host example.test left intact            ← kept for reuse; closed
                                                              when curl exits
```

Three lines repay attention. `Trying` → `Connected` is the TCP handshake.
`ALPN: server accepted h2` decides the HTTP version. The client's `Change cipher
spec` in TLS 1.3 does nothing cryptographically: it is a compatibility record that
makes the handshake look like TLS 1.2 to middleboxes that would otherwise drop it
(RFC 8446, Appendix D.4).

### curl -w — Timings, and the Trap in Them

`curl -w` prints timing variables. They are **cumulative from the start of the
request**, not per step — the most common misreading of them. *Captured* against
the test server configured to wait 300 ms before responding:

```
$ curl -s -o /dev/null -w 'namelookup: %{time_namelookup}\n...' https://example.test:8444/
namelookup:    0.000013      DNS done            (skipped with --resolve)
connect:       0.000086      TCP connected
appconnect:    0.003106      TLS done
pretransfer:   0.003214      about to send the request
starttransfer: 0.304484      first response byte
total:         0.304534      last byte
```

Subtract adjacent values to get each step: TLS took 0.0031 − 0.0001 ≈ 3 ms, and the
server wait was 0.3045 − 0.0032 ≈ 301 ms. `phases` reports the same request with
the subtraction already done:

```
  STEP              START       TOOK  DETAIL
  connect            0.1ms      0.1ms  tcp 127.0.0.1:8444
  tls                0.3ms      1.5ms  TLS 1.3, TLS_AES_128_GCM_SHA256, ALPN h2
  request            1.9ms      0.1ms
  server wait        2.0ms    301.4ms  until the first response byte
  transfer         303.5ms      0.1ms  1256 bytes
```

### Browser DevTools — Timing in a GUI

Chrome's Network tab → request → **Timing** shows (names vary slightly between
versions):

```
BROWSER TIMING ROWS (Chrome)

  Row                          Step it maps to
  ───────────────────────────  ─────────────────────────────────────────────────
  Queueing / Stalled           Before the network: browser limits, waiting for a
                               connection, cache lookups. Large values here are not
                               network latency.
  Proxy negotiation            Only when a proxy is configured
  DNS Lookup                   Step 1 (0 when cached or the connection is reused)
  Initial connection           Steps 2 and 3 together — TCP *and* TLS
  SSL                          Step 3 alone — a subset of Initial connection;
                               don't add it twice
  Request sent                 Step 4: request bytes written
  Waiting for server response  Step 4: request travel + server work + first byte
  (older: Waiting (TTFB))      back — usually headers, not necessarily the body
  Content Download             Step 4: the rest of the body
```

High connect or TLS time can also involve retries, CPU, proxies, or policy, not
only distance.

### tcpdump and tshark — What Was on the Wire

*Captured* on loopback for the same curl request (lab Part 4), with flags and
lengths only. Loopback has a 64 KB MTU, so the server's whole first flight fits in
one segment; on a real 1500-byte path it would be split across several.

```
 #  Direction          Flags  Len    tshark without keys          tshark with the key log
 1  client → server    [S]       0
 2  server → client    [S.]      0
 3  client → server    [.]       0
 4  client → server    [P.]    517   Client Hello (SNI=...)       Client Hello (SNI=example.test)
 5  server → client    [.]       0
 6  server → client    [P.]   1457   Server Hello, Change Cipher  Server Hello, Change Cipher Spec,
                                     Spec, Application Data ×5    Encrypted Extensions, Certificate,
                                                                  Certificate Verify, Finished,
                                                                  New Session Ticket
 7  client → server    [.]       0
 8  client → server    [P.]     64   Change Cipher Spec,          Change Cipher Spec, Finished
                                     Application Data
 9  client → server    [P.]     86   Application Data             HTTP2 Magic, SETTINGS, WINDOW_UPDATE
11  client → server    [P.]     64   Application Data             HEADERS[1]: GET /
15  server → client    [P.]    100   Application Data             HEADERS[1]: 200 OK
16  server → client    [P.]   1287   Application Data             DATA[1] (text/html)
18  client → server    [P.]     24   Application Data             Alert: Close Notify
19  client → server    [F.]      0
21  server → client    [P.]     24   Application Data             Alert: Close Notify
22  client → server    [R]       0
    (pure ACKs and HTTP/2 SETTINGS frames 10, 12–14, 17, 20 omitted)
```

Read it the way you would read a production capture:

- **Frames 1–3** are the TCP handshake: SYN, SYN-ACK, ACK, all with length 0.
- **Without keys,** everything after the ServerHello is `Application Data`. You can
  see *that* the handshake finished and data flowed, and the sizes, but not the
  certificate, the request, or the status code.
- **With the key log** (written by curl because `SSLKEYLOGFILE` was set; Module 13),
  tshark decrypts every record: the encrypted handshake messages, the HTTP/2
  request and response, and the TLS alerts.
- **TCP segments are not TLS records or HTTP messages.** Frame 6 carries seven TLS
  records; on a real network one record can span several segments. Never identify
  messages by packet size or the PSH flag. Use the dissector.
- **Frames 18–22** are the close from Step 5, ending in an RST.

---

## 2.4 What Changes the Sequence

The five-step picture is for a fresh connection to the origin. Most requests in
production are not that, and each difference changes what you see and where
failures can come from.

| Situation | What changes | What you see |
| --- | --- | --- |
| **Cached DNS** | Step 1 is skipped or near-instant | DevTools DNS 0 ms from the browser's cache. `phases` always shows a DNS step, because Go keeps no DNS cache of its own; it is fast when a local stub resolver answered from its cache |
| **Connection reuse** (keep-alive, HTTP/2 multiplexing) | Steps 1–3 skipped entirely | curl: `Re-using existing connection`; `phases -count 2`: second request has only request, server wait, transfer |
| **TLS session resumption** | A new connection, but an abbreviated TLS handshake using a ticket (frame 6's New Session Ticket) | TLS step shorter; certificate not resent |
| **Happy Eyeballs** (RFC 8305) | With both A and AAAA records, the client tries IPv6 first and starts IPv4 if IPv6 hasn't connected within a short delay (RFC 8305 suggests 250 ms; Go uses 300 ms, curl 200 ms) | Two connect attempts. An IPv6 path that silently drops SYNs shows up as a consistent ~200–300 ms penalty, not a failure |
| **Forward proxy** (`HTTPS_PROXY`) | DNS and TCP go to the *proxy*; the client sends `CONNECT host:443`; the proxy resolves the name and connects; TLS then runs end to end through the tunnel | A `proxy tunnel` step in `phases`; refusals as `403`/`407` from the proxy (Module 0) |
| **CDN or load balancer terminating TLS** | Your TLS session and TCP connection end at the edge; the edge makes its own connection to the origin | The certificate, RTT, and many errors (502/503/504) belong to the edge, not the origin |
| **TLS-inspecting proxy or firewall** | The middlebox terminates your TLS and presents its own certificate | `unknown authority` errors on clients that don't trust its CA (Module 13) |
| **Redirect** (`http://` → `https://`, or to `www.`) | The first response is a 301/302/307/308; the whole sequence repeats for the new URL, often on a new connection | Two sets of steps; `curl -L` follows, `phases` stops at the redirect |
| **HTTP/3** | After an `Alt-Svc` response header, a browser may switch later requests to QUIC over UDP 443: no TCP handshake, TLS built into QUIC | UDP 443 in captures; a firewall blocking UDP 443 causes a silent fallback to TCP |

The table is a checklist for "this request doesn't look like the textbook". When a
capture or timing breakdown seems to be missing a step, one of these is usually why.

---

## 2.5 Which Step Failed?

The step that failed is the single most useful fact about a network error. These
messages were *captured* while writing this module (curl 8.5.0, Go 1.24), apart from
the browser codes, which are Chrome's documented error names.

| Step | curl (exit code: message) | Go (`net/http`) | Chrome | What happened |
| --- | --- | --- | --- | --- |
| 1 DNS | `(6) Could not resolve host: does-not-exist.invalid` | `dial tcp: lookup does-not-exist.invalid on <resolver>:53: no such host` | `ERR_NAME_NOT_RESOLVED` | Resolver answered "no such name", or didn't answer |
| 2 TCP, refused | `(7) Failed to connect to 127.0.0.1 port 9 after 0 ms: Couldn't connect to server` | `dial tcp 127.0.0.1:9: connect: connection refused` | `ERR_CONNECTION_REFUSED` | SYN answered by RST or ICMP unreachable — instantly |
| 2 TCP, dropped | `(28) Connection timed out after 3002 milliseconds` | `dial tcp …: i/o timeout` | `ERR_CONNECTION_TIMED_OUT` | SYNs unanswered until the deadline |
| Proxy | `(56) CONNECT tunnel failed, response 403` | `Forbidden` (as plain text) | `ERR_TUNNEL_CONNECTION_FAILED` | Proxy refused to open the tunnel |
| 3 TLS, trust | `(60) SSL certificate problem: self-signed certificate in certificate chain` (labserver; a lone self-signed leaf says `self-signed certificate`, a missing intermediate `unable to get local issuer certificate`) | `tls: failed to verify certificate: x509: certificate signed by unknown authority` | `ERR_CERT_AUTHORITY_INVALID` | Chain doesn't reach a trusted root |
| 3 TLS, name | `(60) SSL: no alternative certificate subject name matches target host name 'wrong.test'` | `x509: certificate is valid for example.test, not wrong.test` | `ERR_CERT_COMMON_NAME_INVALID` | Valid certificate, wrong name |
| 4 HTTP | exit 0 with a 4xx/5xx status (use `--fail` to get exit 22) | a response with that status, no error | the error page | Something answered at the HTTP level — check *who* (the `server`, `via`, or provider headers) |

Notice that curl reports **both** a refused and a dropped connection as exit 7 or
28, while the underlying difference is visible only in the time taken (0 ms vs the
full timeout) or in a capture. Two more observations from the captures:

- A firewall `REJECT` is indistinguishable from "nothing listening" in the error
  text. Both are refusals, so a capture or the firewall's counters must tell you which.
- An address with no route on this machine (here `192.0.2.1`) fails instantly with
  exit 7, not a timeout, because the kernel never sends a SYN at all.

---

## Hands-On Lab: One Request, Five Ways

### Lab Goals

- Annotate real `curl -v` output against the five steps
- Separate the steps with `curl -w` and `phases`, and see a slow server in each
- See connection reuse remove steps 1–3
- Capture the request, read it with and without TLS keys, and find the RST at the end
- Produce refused, dropped, and TLS failures and record how each tool reports them

### Prerequisites

- Linux VM (Ubuntu 24.04) with root for Part 4; Parts 1–3 run anywhere with Go and curl
- `curl`, `tcpdump`, `tshark`, and the Go toolbox built (Module 0):

```bash
cd labs/go
go build -o bin/ ./cmd/...
```

Everything except Part 5's optional internet comparison runs offline against
**`labserver`**, the course's local HTTPS server. It creates a throwaway CA and a
server certificate in memory and writes the CA certificate to a file for clients
to trust.

```bash
# Terminal 1 — a normal server and a slow one (300 ms before responding)
./bin/labserver -name example.test -size 1256 -ca-out /tmp/labserver-ca.pem &
./bin/labserver -addr 127.0.0.1:8444 -name example.test -size 1256 -delay 300ms \
  -ca-out /tmp/labserver-ca-slow.pem &
```

**Verify:** each prints `serving https://example.test:<port>/ …`. Stop them at the end
with `kill %1 %2`.

Because `example.test` is not a real name, every client below uses curl's
`--resolve` (or `phases -resolve`) to map it to 127.0.0.1, skipping DNS. If your
shell sets a proxy, `--noproxy '*'` keeps curl from sending loopback-bound names to it.

```bash
C=(--noproxy '*' --resolve example.test:8443:127.0.0.1 --cacert /tmp/labserver-ca.pem)
```

`C` is a bash array, so `"${C[@]}"` passes each option as one word and the `*` is
never expanded by the shell.

---

### Part 1: Annotate curl -v

```bash
curl -v "${C[@]}" https://example.test:8443/ -o /dev/null 2>&1 | tee /tmp/curl-verbose.txt
```

Label each line with its step, using the annotated transcript in 2.3 as a guide.

**Verify:**

- [ ] You can point to where TCP ends (`Connected to`) and TLS begins (`Client hello`)
- [ ] `ALPN: server accepted h2` appears, and the request line is `GET / HTTP/2`
- [ ] Two `Certificate level` lines: the server sent its certificate and the lab CA
- [ ] The last line says the connection was `left intact`

Now force HTTP/1.1 and compare:

```bash
curl -v --http1.1 "${C[@]}" https://example.test:8443/ -o /dev/null 2>&1 | grep -E 'ALPN|^> GET|^< HTTP'
```

**Verify:** curl offers only `http/1.1`, the server accepts it, and the request is
`GET / HTTP/1.1`. Same server, same TLS, different HTTP version, decided entirely by ALPN.

### Part 2: Timings — curl -w vs phases

```bash
FMT='namelookup:    %{time_namelookup}\nconnect:       %{time_connect}\nappconnect:    %{time_appconnect}\npretransfer:   %{time_pretransfer}\nstarttransfer: %{time_starttransfer}\ntotal:         %{time_total}\n'
curl -s -o /dev/null -w "$FMT" --noproxy '*' --resolve example.test:8444:127.0.0.1 \
  --cacert /tmp/labserver-ca-slow.pem https://example.test:8444/

./bin/phases -noproxy -resolve example.test:8444:127.0.0.1 \
  -cacert /tmp/labserver-ca-slow.pem https://example.test:8444/
```

**Verify:** `starttransfer − pretransfer` from curl and `server wait` from `phases` are
both about 300 ms, matching the server's `-delay`. Repeat against port 8443 (no
delay) and compare. A support engineer's first question about a slow request is
"which of these numbers is large?"

### Part 3: Connection Reuse

```bash
curl -v "${C[@]}" https://example.test:8443/ https://example.test:8443/ -o /dev/null -o /dev/null 2>&1 \
  | grep -E 'Trying|Connected|Re-using|left intact'
./bin/phases -noproxy -resolve example.test:8443:127.0.0.1 -cacert /tmp/labserver-ca.pem \
  -count 2 https://example.test:8443/
```

*Captured* output:

```
*   Trying 127.0.0.1:8443...
* Connected to example.test (127.0.0.1) port 8443
* Connection #0 to host example.test left intact
* Re-using existing connection with host example.test
* Connection #0 to host example.test left intact
```

```
── request 2 of 2 ──
  STEP              START       TOOK  DETAIL
  request            0.0ms      0.0ms  on a reused connection
  server wait        0.1ms      0.3ms  until the first response byte
  transfer           0.4ms      0.0ms  1256 bytes

  connection reused: no DNS, connect, or TLS on this request
```

**Verify:** one `Trying`/`Connected`, two requests. The second `phases` request has
no connect or TLS step. On a 100 ms path, that reuse saves at least 200 ms per
request. This is why connection pools exist, and why a pool that keeps reconnecting
(Module 20) makes a service mysteriously slow.

### Part 4: Capture It

```bash
sudo timeout --signal=INT 5 tcpdump -U -i lo -nn 'tcp port 8443' -w /tmp/quickstart.pcap &
sleep 1
SSLKEYLOGFILE=/tmp/keys.log curl -s "${C[@]}" https://example.test:8443/ -o /dev/null
wait            # until tcpdump has written everything and exited

tcpdump -nn -r /tmp/quickstart.pcap
tshark -r /tmp/quickstart.pcap -T fields -e frame.number -e tcp.len -e _ws.col.Info
tshark -r /tmp/quickstart.pcap -o tls.keylog_file:/tmp/keys.log \
  -T fields -e frame.number -e tcp.len -e _ws.col.Info
```

`-U` writes each packet as it arrives. Reading a capture file before tcpdump has
exited can show a truncated conversation. That happened while preparing this
module, and it looked exactly like a connection that never closed.

**Verify:**

- [ ] The first three packets are SYN, SYN-ACK, ACK, all with length 0
- [ ] Without keys, tshark names the Client Hello (with SNI) and Server Hello, then shows `Application Data`
- [ ] With keys, you can see `Certificate`, `HEADERS[1]: GET /`, `HEADERS[1]: 200 OK`, and `DATA[1]`
- [ ] The capture ends with close_notify, FIN, close_notify, then RST from the client — and the request still succeeded

`/tmp/keys.log` can decrypt everything in that capture. Delete it when you are done,
and never set `SSLKEYLOGFILE` on a machine handling real traffic.

### Part 5: Make It Fail, Five Ways

Run each of these and record the curl exit code, the message, and how long it took.
Then run the same URL through `phases` (with `-noproxy`, `-resolve`, and `-cacert`
as needed) and compare its failed step and explanation.

```bash
curl -sS --noproxy '*' https://does-not-exist.invalid/ -o /dev/null; echo "exit=$?"         # DNS
curl -sS --noproxy '*' http://127.0.0.1:9/ -o /dev/null; echo "exit=$?"                     # refused
curl -sS --noproxy '*' --resolve example.test:8443:127.0.0.1 \
  https://example.test:8443/ -o /dev/null; echo "exit=$?"                                   # untrusted
curl -sS --noproxy '*' --resolve wrong.test:8443:127.0.0.1 --cacert /tmp/labserver-ca.pem \
  https://wrong.test:8443/ -o /dev/null; echo "exit=$?"                                     # wrong name
```

For **dropped versus rejected**, use a throwaway network namespace so no firewall
rule touches your host:

```bash
sudo ip netns add m2lab
sudo ip netns exec m2lab ip link set lo up
sudo ip netns exec m2lab iptables -A INPUT -p tcp --dport 8444 -j DROP
sudo ip netns exec m2lab iptables -A INPUT -p tcp --dport 8445 -j REJECT --reject-with tcp-reset
for p in 8444 8445; do
  sudo ip netns exec m2lab curl -sS --noproxy '*' --max-time 3 http://127.0.0.1:$p/ -o /dev/null
  echo "port $p exit=$?"
done
sudo ip netns del m2lab        # removes the namespace and its rules
```

*Captured* result:

```
curl: (28) Connection timed out after 3002 milliseconds
port 8444 exit=28
curl: (7) Failed to connect to 127.0.0.1 port 8445 after 0 ms: Couldn't connect to server
port 8445 exit=7
```

**Verify:** DROP produces a timeout after the full 3 s, and REJECT produces an instant
refusal, identical in wording to "nothing listening". Add a third rule using
`-j REJECT` with no option (an ICMP port-unreachable) and confirm it is also reported
as refused.

**Optional, needs internet:** run `curl -v https://example.com` and `./bin/phases
https://example.com` and compare them with the loopback transcripts. Expect larger
times in every step and a certificate from a public authority. Expect a `proxy
tunnel` failure if your network enforces an allow-list.

### Write-Up

Fill in the table from 2.5 with your own results, adding one column: how long each
failure took. Then answer in one sentence each:

1. Why did the healthy capture end with an RST?
2. Which `curl -w` value would you quote to prove a server, not the network, was slow?
3. A user reports `ERR_CONNECTION_REFUSED`. Name two different devices that could have sent the refusal.

---

## Common Failure Patterns

| Symptom | Likely Cause | How to Confirm | Fix |
| --- | --- | --- | --- |
| Every request slow by ~200–300 ms, no errors | Broken IPv6 path; Happy Eyeballs falls back to IPv4 after its delay | `phases` with `-resolve` to the IPv4 and then the IPv6 address; `curl -4` vs `curl -6` | Fix IPv6 routing or remove the AAAA record; don't just disable IPv6 on clients |
| `starttransfer` large, everything before it small | Server or upstream slow, or a middlebox queuing the request | `curl -w`, `phases` server wait; server-side timing logs | Investigate the server; the network is not the bottleneck |
| `appconnect − connect` large | Slow TLS: high RTT, lost handshake packets, server CPU, OCSP, or an inspecting proxy | Capture the handshake; compare with `connect − namelookup` (one RTT) | Depends on cause; TLS 1.3 and session resumption reduce round trips |
| Works with `curl`, fails in the app | Different resolver, proxy settings, or trust store | Run `phases` (Go behaves like Go apps) and compare `curl -v` | Align the app's configuration; see Modules 12, 13, 16 |
| Capture shows RST at the end of every connection | Normal close where data arrived after the socket closed | Check the request completed (status and full body) before the RST | Usually nothing; investigate only RSTs mid-transfer |
| Capture shows only the start of a conversation | Capture file read before tcpdump flushed it, or a capture filter that misses the return path | Re-read after tcpdump exits; use `-U`; filter on host and port, not one direction | Capture again correctly |

---

## Summary

- A fresh HTTPS-over-TCP request costs at least three round trips before the first
  response byte: TCP, TLS 1.3, and the request itself — plus DNS on a cache miss.
- ALPN in the TLS handshake decides HTTP/1.1 vs HTTP/2; `ALPN: server accepted h2` is the line to look for.
- `curl -w` times are cumulative; subtract adjacent values. `phases` reports per-step times directly.
- Connection reuse removes DNS, TCP, and TLS from later requests, which is why pools and keep-alive matter so much on long paths.
- Without keys a capture shows the handshake's clear-text parts and the sizes and timing of everything else; with a key log it shows everything.
- A healthy connection can end with an RST. Judge an RST by *when* it arrives.
- Each failing step has a characteristic error in curl, Go, and browsers — and refused vs dropped differs mostly in how long it took.

## What's Next

Module 3 covers Networking Fundamentals — the vocabulary (LAN, WAN, bandwidth, latency, packets, frames, MAC addresses, switches, routers) that underlies every protocol you just watched in action. It also starts the namespace labs, where you will build the network itself rather than only observing traffic on it.

---

## Quick Reference

```
THE FIVE STEPS OF A FRESH HTTPS REQUEST

  Step     Protocol   Observe with                  Key indicator
  ───────  ─────────  ────────────────────────────  ─────────────────────────────────
  1 DNS    DNS        dig, tcpdump port 53, phases  addresses returned
  2 TCP    TCP        curl -v "Connected to",       [S] [S.] [.], length 0
                      tcpdump
  3 TLS    TLS        curl -v, openssl s_client,    "SSL connection using TLSv1.3",
                      tshark                        "ALPN: server accepted h2"
  4 HTTP   HTTP/1.1   curl -v, DevTools, phases     status line, server wait time
           or HTTP/2
  5 close  TLS + TCP  tcpdump, tshark               close_notify, [F.], maybe [R]

CURL -w (cumulative seconds from the start)
  connect − namelookup        TCP handshake   ≈ 1 RTT
  appconnect − connect        TLS handshake   ≈ 1 RTT (TLS 1.3)
  starttransfer − pretransfer server wait     RTT + server time
  total − starttransfer       body transfer

PHASES
  phases [-resolve host:port:ip] [-cacert ca.pem] [-count N] [-noproxy] URL

WHICH STEP FAILED
  curl 6   / no such host / ERR_NAME_NOT_RESOLVED      DNS
  curl 7   / connection refused (instant)              TCP: RST came back
  curl 28  / i/o timeout                               TCP: no reply — dropped?
  curl 56  / "CONNECT tunnel failed, response 403"     proxy refused the tunnel
  curl 60  / x509 / ERR_CERT_*                         TLS: trust, name, or dates
  exit 0 + 4xx/5xx                                     HTTP: ask who answered

DEVTOOLS
  Initial connection = TCP + TLS (SSL is a subset — don't add it twice)
  Waiting for server response (TTFB) = request travel + server work + first byte
```

---

## References

- RFC 8446 — TLS 1.3, including Appendix D.4 (middlebox compatibility mode)
- RFC 7301 — TLS Application-Layer Protocol Negotiation (ALPN)
- RFC 8305 — Happy Eyeballs Version 2
- RFC 9113 — HTTP/2
- RFC 9110 — HTTP Semantics (status codes, redirects)
- curl `--write-out` variables — <https://everything.curl.dev/usingcurl/verbose/writeout>
- Chrome DevTools: understanding resource timing — <https://developer.chrome.com/docs/devtools/network/reference>
- Go `net/http/httptrace` — <https://pkg.go.dev/net/http/httptrace>
