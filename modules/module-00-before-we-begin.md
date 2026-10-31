# Module 0: Before We Begin

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** None — this is the starting point
> **Estimated time:** 1.5–2 hours (most of it is environment setup)

## Overview

Most engineers encounter networking the same way — something breaks in production, someone says "it's probably a network issue," and you stare at an error message with no clear path forward. `Connection timed out`. `DNS resolution failed`. `TLS handshake error`. `ECONNREFUSED`. These messages tell you something went wrong somewhere between your application and the thing it was trying to reach. Without a mental model of what that journey looks like, you are guessing.

This course is built around one goal: giving you the knowledge to stop guessing. By the end you will be able to start from an error on a failing host — a CI runner, a pod, a VM — and prove which step, which hop, and which setting caused it, using packet captures, kernel counters, logs, and small Go programs you wrote yourself.

This module is not about networking yet. It builds the scaffolding that makes the rest of the course stick: the lab environment, the Go toolbox you will extend in almost every module, the vocabulary used throughout, and the single most important exercise in the course.

## What You Will Build Toward

- Name the steps of a fresh HTTPS request — DNS, TCP connect, TLS, request, response — and say which one an error belongs to
- Set up a Linux lab with capture tools, Docker, and a Go toolchain
- Build and run `phases`, your first Go tool, and read its timing table
- Reproduce four failures on purpose — refused, timed out, name not found, untrusted certificate — and recognise each one
- Use *packet*, *frame*, *segment*, *socket*, *5-tuple*, and *RTT* precisely

---

## 0.1 How to Use This Course

### What This Is

This is not a certification course and it is not a reference manual. It is a progressive skill-building course: each module gives you something you can use immediately and builds on the ones before it. The theory exists to explain the tools. The tools exist to diagnose real problems.

Every module follows the same pattern: concept, mechanism, diagram, production context, hands-on lab, failure patterns. Most labs now include a Go program; the [Go lab toolbox](../labs/go/README.md) collects them, and the capstone combines them into one diagnostic CLI.

### How to Get the Most Out of It

Read each module with a terminal open, but distinguish inspection, runnable labs,
and configuration templates. Do not paste every example into your host shell.
Follow the [lab contract](../labs/README.md) for prerequisites, isolation, evidence,
and cleanup. Complete the relevant exercises before relying on a skill in production.

Write the Go programs yourself before reading the reference solution in
`labs/go`. Typing a dialer, a timeout, and an error check by hand is what makes
the error strings in a ticket familiar later.

End every lab with a short write-up: **symptom → evidence → root cause → fix**.
These become your personal runbook, and the habit is the core support skill this
course trains.

If you get stuck on a concept, skip to the lab and come back. Seeing the thing in practice often unlocks the explanation in a way that reading alone does not.

### Diagram

```
┌──────────────────────────────────────────────────────────────────────────┐
│                       COURSE PROGRESSION                                 │
│                                                                          │
│  0          1–4             5–9              10–15            16–23      │
│  Setup ──→ Foundations ──→ Addressing ──→ Transport and ──→ Production  │
│  & Go      history, a      & paths:        applications:     containers, │
│  toolbox   request end      IP, ARP,        UDP, TCP, DNS,    Kubernetes, │
│            to end, OSI      ICMP,           TLS, performance, firewalls,  │
│                             routing, BGP    HTTP/2, gRPC      VPNs, sockets,│
│                                                               capture,    │
│                                                               diagnosis   │
│                                                                          │
│  then: 24 Linux kernel internals → 25 Cloud and CI/CD paths              │
│        → 26 Capstones                                                    │
└──────────────────────────────────────────────────────────────────────────┘

Each module:
  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌──────────┐
  │ Concept  │──▶│Mechanism │──▶│   Lab    │──▶│ Failure  │──▶│ Write-up │
  │          │   │+ Diagram │   │ (+ Go)   │   │ Patterns │   │          │
  └──────────┘   └──────────┘   └──────────┘   └──────────┘   └──────────┘
```

---

## 0.2 The Single Most Important Exercise

Before any protocol, any header diagram, any tool — run through this exercise. It is the thread that connects every module in this course.

### What This Is

Open a terminal and run this command:

```bash
curl -v https://example.com 2>&1 | head -60
```

You will see output that looks chaotic right now. By the end of this course, every line of that output will be immediately readable to you. This exercise is here so you have a reference point — a before and after.

### What Is Actually Happening

For a fresh HTTPS-over-TCP connection, the following simplified sequence provides
a starting model. DNS may be cached, a connection may be reused, and teardown
occurs when the connection closes rather than after every request. HTTP/3 uses
QUIC, not this TCP handshake. Plaintext protocols do not have a TLS step. A
configured proxy adds a CONNECT step after the TCP connection (Module 23).
Diagrams and unattributed transcripts are illustrative; timings and addresses are
not fixed. This course uses documentation addresses such as `203.0.113.10`
(RFC 5737) where a real address would go out of date.

```
You type: curl -v https://example.com
              │
              ▼
┌─────────────────────────────────────────────────────────────────┐
│  STEP 1: DNS Resolution                          (Module 12)    │
│                                                                 │
│  "What is the IP address of example.com?"                       │
│                                                                 │
│  Your machine ──ask──▶ Recursive resolver ──ask──▶ root, TLD,   │
│                                                    authoritative│
│               ◀──answer──────────────────────────               │
│                                                                 │
│  Result: one or more A/AAAA records, e.g. 203.0.113.10          │
└─────────────────────────────────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────────┐
│  STEP 2: TCP Connection                          (Module 11)    │
│                                                                 │
│  Your machine          example.com server                       │
│       │                       │                                 │
│       │──── SYN ─────────────▶│   "I want to connect"          │
│       │◀─── SYN-ACK ──────────│   "Okay, I'm ready"            │
│       │──── ACK ─────────────▶│   "Great, let's go"            │
│       │                       │                                 │
│        [CONNECTED after 1 round trip]                           │
└─────────────────────────────────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────────┐
│  STEP 3: TLS 1.3 Handshake (simplified)          (Module 13)    │
│                                                                 │
│  Your machine                          example.com server       │
│       │── ClientHello ──────────────────────▶│                  │
│       │   versions, ciphers, key share,      │                  │
│       │   SNI "example.com", ALPN h2/http1.1 │                  │
│       │                                      │                  │
│       │◀─ ServerHello (+ key share) ─────────│                  │
│       │◀─ {EncryptedExtensions} ─────────────│  everything in   │
│       │◀─ {Certificate} ─────────────────────│  {braces} is     │
│       │◀─ {CertificateVerify} ───────────────│  already         │
│       │◀─ {Finished} ────────────────────────│  encrypted       │
│       │                                      │                  │
│       │  client verifies the certificate chain and hostname     │
│       │── {Finished} ───────────────────────▶│                  │
│                                                                 │
│        [ENCRYPTED after 1 more round trip]                      │
└─────────────────────────────────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────────┐
│  STEP 4: HTTP Request + Response                 (Module 15)    │
│                                                                 │
│  Your machine          example.com server                       │
│       │── GET / ─────────────▶│  HTTP/2 if both sides chose     │
│       │   Host: example.com   │  "h2" via ALPN, else HTTP/1.1   │
│       │                       │                                 │
│       │◀─ 200 ────────────────│                                 │
│       │   content-type: html  │                                 │
│       │   [body...]           │                                 │
└─────────────────────────────────────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────────────────────────────────────┐
│  STEP 5: TCP Teardown                            (Module 11)    │
│                                                                 │
│  Your machine          example.com server                       │
│       │──── FIN ─────────────▶│   either side may close first;  │
│       │◀─── FIN, ACK ─────────│   the reply ACK and FIN are     │
│       │──── ACK ─────────────▶│   often combined (3 segments)   │
│                [CLOSED]                                         │
│                                                                 │
│  Keep-alive and HTTP/2 hold the connection open for reuse;      │
│  some applications abort with RST instead of closing with FIN.  │
└─────────────────────────────────────────────────────────────────┘
```

Two things in this picture matter for troubleshooting from day one:

- **Each step costs at least one round trip before the next can start.** On a
  100 ms path, a fresh HTTPS request needs about 100 ms for TCP plus 100 ms for
  TLS 1.3 before the request is even sent — plus DNS if it isn't cached. That is
  why connection reuse matters, and why a slow request is not necessarily a slow
  server.
- **In TLS 1.3 the certificate is encrypted.** A capture shows the ClientHello and
  its SNI in clear text, but not the certificate. You inspect certificates with
  `openssl s_client` or by decrypting the capture with a key log (Module 13), not
  by reading packets.

### See It in Go

`curl -v` prints the steps as they happen. The course's first Go tool,
[`phases`](../labs/go/cmd/phases), times each step and, when the request fails,
names the step that was in progress and says what the error means on the wire.
Section 0.4 builds it and uses it.

### What You Will See in Production

A timeout can occur in DNS, connect, TLS, an established read/write, or a pool wait;
identify the operation and deadline before locating the failure. A certificate
verification error points to authentication, but not necessarily one particular
cause. An HTTP 503 proves a responder returned that status, possibly a proxy rather
than the origin. Combine the model with endpoint logs and appropriately scoped
captures; it does not eliminate a fixed percentage of possible causes.

The most useful question to ask of any network error is **"who answered?"** A
refusal, a reset, a 403, or a 503 each came from *something*. It might be the
server, but it might also be a firewall, a proxy, or a load balancer in between.
A timeout is the opposite: nothing answered. Much of this course is about finding
out which device sent the reply, or why none came.

---

## 0.3 Lab Environment Setup

### What This Is

Use the [versioned environment profiles](../labs/README.md). Namespace experiments
target a disposable Ubuntu 24.04 VM with root and Linux kernel networking features.
The Go tools and their tests also run unprivileged on macOS or any Linux machine.
Docker, policy-enforcing Kubernetes, browser tooling, and optional cloud accounts
have separate prerequisites. A WSL2 or container environment must expose the
required capabilities; its host dataplane may differ from a dedicated VM. Docker
Desktop runs containers inside a hidden VM, so its networking is not the host
networking shown in this course's diagrams.

### Setup Steps

Install the core tools. Run each block and verify it succeeds before continuing.

```bash
# Update package index
sudo apt-get update

# Core networking tools. The wireshark package asks whether non-root users may
# capture; answer No for now (capture permissions are covered below).
sudo apt-get install -y \
  tcpdump \
  tshark \
  wireshark \
  iproute2 \
  iputils-ping \
  traceroute \
  mtr-tiny \
  dnsutils \
  netcat-openbsd \
  curl \
  wget \
  openssl \
  hping3 \
  iperf3 \
  nmap \
  net-tools

# Additional isolated-lab tools (dnsmasq-base does not enable a host DHCP server)
sudo apt-get install -y iputils-arping dnsmasq-base busybox haproxy nginx-light \
  iptables nftables conntrack ethtool wireguard-tools jq nghttp2-client chrony

# Python is still needed for labs that have not been ported to Go yet
sudo apt-get install -y python3 python3-venv python3-pip
```

```bash
# Verify each critical tool is present
for tool in tcpdump tshark ip ss ping traceroute mtr dig curl openssl iperf3 jq; do
  command -v "$tool" >/dev/null && echo "✓ $tool" || echo "✗ $tool MISSING"
done
```

### Install Go

The labs need Go 1.24 or newer. Ubuntu 24.04's `golang` package is older, so
install the official release. This fetches the current version and verifies the
download against the checksum Go publishes before installing it:

```bash
VERSION=$(curl -fsSL 'https://go.dev/VERSION?m=text' | head -1)   # e.g. go1.25.3
FILE="${VERSION}.linux-$(dpkg --print-architecture).tar.gz"       # amd64 or arm64
curl -fsSLO "https://go.dev/dl/${FILE}"
SUM=$(curl -fsSL 'https://go.dev/dl/?mode=json' |
      jq -r --arg f "$FILE" '.[].files[] | select(.filename == $f) | .sha256')
echo "${SUM}  ${FILE}" | sha256sum -c -        # must print: <file>: OK
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf "$FILE" && rm "$FILE"
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.profile
source ~/.profile
go version                                     # go1.24 or newer
```

If `sha256sum -c` prints anything other than `OK`, delete the file and stop; do not
install it.

### Capture Permissions

You can always capture with `sudo tcpdump`. To let your user capture with
Wireshark and `tshark` without sudo, use the distribution's `dumpcap` group:

```bash
sudo dpkg-reconfigure wireshark-common   # answer Yes
sudo usermod -aG wireshark "$USER"
```

Log out and back in for the group change to take effect. Only do this on a lab VM:
capture rights let you read other users' traffic, including credentials sent in
clear text.

### Docker

Install Docker using its documented Ubuntu repository and check the daemon and
Compose plugin. Membership in the Docker group is effectively root access.

```bash
docker run --rm hello-world
docker compose version
```

You should see a message confirming Docker can pull and run a container. If this
fails, check the daemon with `sudo systemctl status docker`. If the pull fails
behind a corporate proxy, note that the Docker *daemon* needs its own proxy
setting; your shell's `HTTPS_PROXY` does not apply to it (Module 16).

### Diagram

```
Your Lab Machine
┌──────────────────────────────────────────────────────────────┐
│                                                              │
│  ┌──────────────┐   ┌──────────────┐   ┌─────────────────┐  │
│  │   tcpdump    │   │   Wireshark  │   │    tshark       │  │
│  │  Capture     │   │  Analyze     │   │  Scripted       │  │
│  └──────────────┘   └──────────────┘   └─────────────────┘  │
│                                                              │
│  ┌──────────────┐   ┌──────────────┐   ┌─────────────────┐  │
│  │  ip / ss     │   │  dig         │   │  curl / openssl │  │
│  │  Interfaces  │   │  DNS Queries │   │  HTTP / TLS     │  │
│  │  Routes, TCP │   │              │   │  Testing        │  │
│  └──────────────┘   └──────────────┘   └─────────────────┘  │
│                                                              │
│  ┌──────────────┐   ┌──────────────┐   ┌─────────────────┐  │
│  │   Docker     │   │  iperf3      │   │  hping3 / mtr   │  │
│  │  Container   │   │  Throughput  │   │  Path Analysis  │  │
│  │  Networking  │   │  Testing     │   │  & Probing      │  │
│  └──────────────┘   └──────────────┘   └─────────────────┘  │
│                                                              │
│  ┌────────────────────────────────────────────────────────┐ │
│  │  Go toolbox (labs/go): phases, then one new tool per   │ │
│  │  module, combined into netdoctor in the capstone       │ │
│  └────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────┘
                              │
                              │ eth0 / ens3 / wlan0
                              ▼
                       [Your Network / Internet]
```

### What You Will See in Production

You will not always have all of these tools available. On a minimal cloud VM, a
CI runner, or a container you have exec'd into, you may only have `curl`, or not
even that. This is where the Go tools earn their place: a Go program compiles to
one static binary that you can copy onto the failing host and run, without
installing packages there. Build for another machine with
`GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/phases`.

---

## 0.4 Your Go Lab Toolbox

### What This Is

All Go labs live in one Go module, [`labs/go`](../labs/go/README.md), with one
command per tool under `cmd/`. Tools follow the same conventions:

- standard library first;
- exit 0 on success, 1 when the network operation failed, 2 for bad usage;
- `-json` for scripts and tickets;
- every failure explained as what happened on the wire plus the first command to run.

The first tool, `phases`, makes one HTTP(S) request and times each step using
Go's `net/http/httptrace` hooks. Read [its source](../labs/go/cmd/phases/run.go)
after the exercises: about 440 lines, a third of them the error explanations.

### Build and Test

```bash
cd labs/go
go vet ./...
go test -race ./...           # local servers on 127.0.0.1 only; no root, no internet
go build -o bin/ ./cmd/...
```

**Verify:** `go test` prints `ok  github.com/wechuli/practical_networking/labs/go/cmd/phases`.

### Exercise 1 — A Healthy Request

```bash
./bin/phases https://example.com
```

Expected shape (your times, addresses, sizes, and certificate details will differ):

```
GET https://example.com

  STEP              START       TOOK  DETAIL
  dns                0.2ms     18.4ms  example.com → 203.0.113.10, 2001:db8::10
  connect           18.8ms     91.2ms  tcp 203.0.113.10:443
  tls              110.1ms     97.6ms  TLS 1.3, TLS_AES_128_GCM_SHA256, ALPN h2
  request          207.9ms      0.1ms
  server wait      208.0ms     95.3ms  until the first response byte
  transfer         303.4ms      0.2ms  513 bytes

  result: HTTP/2.0 200 OK from 203.0.113.10:443, 513 bytes in 303.7ms
  certificate: CN=example.com,...
    issuer CN=...,O=...,C=US, expires 2027-01-15 (79 days), 2 cert(s) sent
```

**Verify:** `connect`, `tls`, and `server wait` each took roughly one round trip
to the server. Compare the connect time with `ping -c 5 example.com`; if ping
works on your network, the averages should be close. If they aren't, ICMP may be
treated differently from TCP on your path (Module 7).

Run it a second time. DNS is usually faster now, because the answer is cached
somewhere between you and the authoritative server.

If you see `via proxy …` at the top, your environment sets `HTTPS_PROXY`. Every
connect and TLS time then includes the proxy hop, and the proxy, not your machine,
resolves the destination name.

### Exercise 2 — Skip DNS

```bash
IP=$(dig +short example.com A | head -1)
./bin/phases -resolve "example.com:443:${IP}" https://example.com
```

**Verify:** there is no `dns` row, and the TLS step still succeeds. TLS checks the
certificate against the *name in the URL* (`example.com`), not the IP you
connected to. Now repeat it with an IP that belongs to a different site, such as
the first address from `dig +short github.com`. TLS fails, in one of two ways
depending on that server: a hostname mismatch (it sent its own valid certificate,
issued for a different name) or a handshake failure (it refused a name it doesn't
host). This is what happens when stale DNS or a wrong hosts-file entry sends a
client to the wrong server.

### Exercise 3 — Four Failures on Purpose

Run each command and read the `FAILED during` and `meaning` lines. If your
environment sets `HTTP_PROXY` or `HTTPS_PROXY`, add `-noproxy` to every command
here except the first; otherwise the proxy makes the connection and reports its
own errors instead (loopback addresses are never proxied).

| Command | Expected failed step | What happened on the wire |
| --- | --- | --- |
| `./bin/phases http://127.0.0.1:9/` | `connect` | The SYN was answered with an RST: nothing listens on port 9 |
| `./bin/phases -timeout 3s http://192.0.2.1/` | `connect` | No reply to the SYN at all. 192.0.2.0/24 is a documentation range that should not be routed; on some networks you get "network unreachable" instead |
| `./bin/phases https://does-not-exist.invalid/` | `dns` | The resolver answered "no such name". `.invalid` is reserved never to exist (RFC 6761) |
| `./bin/phases https://self-signed.badssl.com/` | `tls` | The certificate chain does not lead to a trusted authority |

The refused case, as captured while writing this module:

```
GET http://127.0.0.1:9/

  STEP              START       TOOK  DETAIL
  connect            0.1ms      0.1ms  tcp 127.0.0.1:9 ✗ dial tcp 127.0.0.1:9: connect: connection refused

  FAILED during: connect (after 0.2ms)
  error:   Get "http://127.0.0.1:9/": dial tcp 127.0.0.1:9: connect: connection refused
  meaning: TCP: connection refused — the SYN was answered with an RST (or ICMP port unreachable).
           Something on the path replied: no listener on that port, a firewall REJECT rule, or a
           proxy. A capture shows which IP sent the RST.
```

**Verify the difference between refused and timed out in packets.** In a second
terminal, start a capture, then rerun the first two rows:

```bash
sudo tcpdump -ni any 'tcp port 9 or host 192.0.2.1'
```

The refused case shows `Flags [S]` followed immediately by `Flags [R.]`. The
timed-out case shows `Flags [S]` repeated with growing gaps (the kernel's SYN
retransmissions) and nothing coming back. Notice how long the refusal took
(well under a millisecond) compared with the timeout (the full deadline). That
contrast is the first diagnostic fact this course trains you to notice.

### Exercise 4 — When the Proxy Says No

If your network forces traffic through an HTTP proxy, `phases` shows a step that
`curl` users often miss. The TCP connection to the *proxy* succeeds, then the proxy
refuses to open a tunnel to the destination. This output was captured from a
sandbox whose egress proxy only allows listed domains:

```
GET https://example.com
  via proxy http://127.0.0.1:34857 (connect and TLS timings include the proxy hop)

  STEP              START       TOOK  DETAIL
  connect            0.1ms      0.4ms  tcp 127.0.0.1:34857

  FAILED during: proxy tunnel (after 249.4ms)
  error:   Get "https://example.com": Forbidden
  meaning: Proxy: the TCP connection to the proxy worked, but the proxy refused to open a CONNECT
           tunnel to this host (HTTP 403) — usually a destination allow-list. ...
```

The bare error text, `Forbidden`, is all many programs print. Without knowing a
proxy was involved, it reads like the destination server rejected you. It didn't:
the request never left the proxy. A 407 status means the proxy wants credentials
instead. Use `-proxy URL` to test a specific proxy and `-noproxy` to bypass it
where direct egress exists.

### Write-Up

For each failure in Exercises 3 and 4, write one line: the error text, the failed
step, and who answered (or that nobody did). Keep the file; you will extend it in
every module.

---

## 0.5 Vocabulary You Will Use Every Day

These are not definitions to memorize — they are terms that will appear in every module. Read them now so they feel familiar when they come up in context.

### The Terms

**Frame** — A unit of data at the link (Ethernet) layer. Has a destination MAC,
source MAC, and EtherType, and carries a packet as its payload, followed by a frame
check sequence (FCS). Captures usually don't show the FCS, because the NIC checks
and strips it.

**Packet** — A unit of data at the network (IP) layer. Has a source IP, destination IP, and a payload.

**Segment** — A unit of data at the transport layer for TCP. Has source and destination ports, sequence numbers, and carries application data. UDP's equivalent is a **datagram**.

**Protocol** — A set of rules two systems agree on for how to communicate. TCP is a protocol. DNS is a protocol. TLS is a protocol.

**Port** — A 16-bit number (0–65535) identifying a service or conversation endpoint
on a host. Port 0 is reserved; asking the OS to bind port 0 means "pick any free
port". Port 443 is HTTPS by convention, and port 53 is DNS.

**Socket** — The operating system's object for one end of a network conversation:
in Linux, a file descriptor your program reads and writes. A socket is bound to a
**socket address**, an IP address plus a port such as `203.0.113.10:443`.

**5-tuple** — Protocol, source IP, source port, destination IP, destination port.
It uniquely identifies a TCP connection or UDP flow, and it is what firewalls,
NAT devices, and load balancers track. Two connections to the same server differ
only by source port.

**Latency** — How long data takes to get from one point to another. Tools almost
always report **round-trip time (RTT)**: there and back, because one-way delay
needs synchronised clocks to measure. Measured in milliseconds. Not the same as
throughput.

**Throughput** — How much data moves between two points per unit of time. Measured in Mbit/s or Gbit/s. Latency limits throughput (Module 14), but the two often fail independently.

**Encapsulation** — Wrapping data from one layer inside the structure of the layer below it. An HTTP request (application) is encapsulated in a TCP segment (transport), which is encapsulated in an IP packet (network), which is encapsulated in an Ethernet frame (link).

### Diagram

```
ENCAPSULATION — data gets wrapped at each layer

Application data:  [ HTTP Request: GET / HTTP/1.1 ]
                                │
                                ▼ TCP wraps it
Transport:         [ TCP Header | HTTP Request ]
                                │
                                ▼ IP wraps it
Network:           [ IP Header | TCP Header | HTTP Request ]
                                │
                                ▼ Ethernet wraps it
Link:              [ Eth Header | IP Header | TCP Header | HTTP Request | FCS ]
                                │
                                ▼
                         Sent on the wire
                                │
                                ▼
                   [ Eth Header | IP Header | TCP Header | HTTP Request | FCS ]
                                │
                                ▼ Ethernet checks the FCS, strips its header
Network:           [ IP Header | TCP Header | HTTP Request ]
                                │
                                ▼ IP strips its header
Transport:         [ TCP Header | HTTP Request ]
                                │
                                ▼ TCP strips its header
Application:       [ HTTP Request: GET / HTTP/1.1 ]
```

At each layer going down, a header is added. At each layer going up on the receiving side, a header is removed. This is decapsulation. When engineers say a device "operates at Layer X," they mean it reads and acts on the header for that layer. With HTTPS, a TLS record sits between TCP and HTTP, so anything that only sees packets can read the IP and TCP headers but not the HTTP request.

---

## Common Failure Patterns

| Symptom | Likely Cause | How to Confirm | Fix |
| --- | --- | --- | --- |
| `tcpdump: command not found` | Package not installed | `command -v tcpdump` | `sudo apt-get install tcpdump` |
| `tcpdump: eth0: You don't have permission to perform this capture on that device` | Not root and no capture rights | `id` | Use `sudo tcpdump`; for unprivileged Wireshark/tshark, join the `wireshark` group as in 0.3 |
| `tshark: Couldn't run /usr/bin/dumpcap` | Not in the `wireshark` group, or not logged in again since joining | `id` | `sudo usermod -aG wireshark $USER`, then log out and in |
| `docker: permission denied while trying to connect to the Docker daemon socket` | User not in the `docker` group | `id` | `sudo usermod -aG docker $USER`, then log out and in (this grants root-equivalent access) |
| `go: command not found` | Go not on `PATH` | `ls /usr/local/go/bin/go` | Add `/usr/local/go/bin` to `PATH` as in 0.3 and open a new shell |
| `go: downloading go1.24.x` on first build, or `toolchain not available` | Distribution Go is older than 1.24 and is fetching a newer toolchain (or can't) | `go version`; `command -v go` | Install the official release (0.3) and make sure it comes first on `PATH` |
| `go: downloading ...: dial tcp ...: i/o timeout` or `Forbidden` | Module downloads blocked or proxied | `go env GOPROXY`; `./bin/phases https://proxy.golang.org` | Set `HTTPS_PROXY` for the `go` command, or an internal `GOPROXY` |
| `hping3: command not found` | Not available on minimal installs | `apt-cache policy hping3` | `sudo apt-get install hping3` |

---

## Summary

- A fresh HTTPS-over-TCP connection is one useful example: DNS, TCP, TLS, HTTP,
  eventual teardown. Caching, reuse, proxies, plaintext traffic, and QUIC change the path.
- Each step costs at least a round trip, so the step timings tell you as much as the errors do.
- Knowing which step failed — and who answered, if anyone — cuts your diagnostic surface immediately.
- The Go toolbox runs unprivileged anywhere; namespace and packet labs need a Linux VM with root.
- A frame is Layer 2, a packet Layer 3, a segment (TCP) or datagram (UDP) Layer 4 — the terms are not interchangeable.
- A socket is an OS object bound to a socket address; a connection is identified by its 5-tuple.
- Latency is usually measured as RTT, and latency and throughput often fail independently.

## What's Next

Module 1 explains the history behind design choices you will meet every day: why
the internet routes packets rather than circuits, where the 1500-byte Ethernet MTU
and the 16-bit port space come from, and why NAT exists. Keep the distinction in
mind: those are protocol limits, while the path MTU and the configured
ephemeral-port range are operational limits that vary from network to network.

---

## Quick Reference Card

```
MODULE 0: BEFORE WE BEGIN — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

KEY CONCEPTS
  Frame           Unit of data at Layer 2 (Ethernet)
  Packet          Unit of data at Layer 3 (IP)
  Segment         Unit of data at Layer 4 (TCP); UDP: datagram
  Socket          OS endpoint object, bound to IP address + port
  5-tuple         proto, src IP, src port, dst IP, dst port
  Encapsulation   Each layer wraps the one above it with a header
  RTT             Round-trip time — what latency tools measure
  Throughput      Volume of data transferred per unit of time

THE 5-STEP SEQUENCE (fresh HTTPS over TCP)
  1. DNS           Resolve hostname → IP address
  2. TCP           3-way handshake                    1 RTT
  3. TLS           Negotiate keys, verify cert        1 RTT (TLS 1.3)
  4. Application   Exchange data (HTTP, gRPC, etc.)   ≥ 1 RTT
  5. Teardown      Close with FIN, or abort with RST
  (+ proxy CONNECT after step 2 if HTTPS_PROXY is set)

QUICK ENVIRONMENT VERIFY
  command -v tcpdump tshark ip ss dig curl openssl iperf3 docker go

WHEN YOU SEE THIS            FIRST HYPOTHESIS (then prove it)
  no such host / NXDOMAIN    Step 1: this resolver can't find the name
  connection refused         Step 2: something replied RST — server,
                             firewall REJECT, or proxy; capture to see who
  i/o timeout during connect Step 2: SYNs unanswered — dropped or no route
  Forbidden / 407 via proxy  Proxy refused the CONNECT tunnel
  x509 / certificate errors  Step 3: chain, hostname, or validity dates
  timeout after connecting   Step 4: server or a middlebox is slow

GO TOOLBOX
  cd labs/go && go test -race ./... && go build -o bin/ ./cmd/...
  ./bin/phases [-resolve host:port:ip] [-proxy URL | -noproxy] [-json] URL

TOOL → LAYER MAPPING
  tcpdump / tshark / Wireshark   Any layer (reads raw frames)
  ip                             Layers 2–3 (links, addresses, routes)
  ss                             Layer 4 (sockets, TCP state)
  ping, traceroute, mtr          Layer 3 (ICMP; TCP/UDP probe modes too)
  dig                            Application (DNS)
  openssl s_client               TLS over TCP
  curl -v, phases                Application, with every step underneath
```

---

## References

- `man tcpdump`, `man ip`, `man ss`, `man dig`, `man curl`
- Go `net/http/httptrace` package — <https://pkg.go.dev/net/http/httptrace>
- Go downloads and checksums — <https://go.dev/dl/>
- RFC 9293 — Transmission Control Protocol — <https://www.rfc-editor.org/rfc/rfc9293>
- RFC 8446 — TLS 1.3 — <https://www.rfc-editor.org/rfc/rfc8446>
- RFC 5737 — IPv4 address blocks reserved for documentation — <https://www.rfc-editor.org/rfc/rfc5737>
- RFC 6761 — Special-use domain names, including `.invalid` — <https://www.rfc-editor.org/rfc/rfc6761>
- badssl.com — public endpoints with deliberately broken TLS — <https://badssl.com>
