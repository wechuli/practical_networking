# Module 10: UDP — User Datagram Protocol

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 9 — BGP, Anycast & Internet Routing
> **Estimated time:** 2–3 hours (the lab runs offline in network namespaces)

## Overview

UDP is the simplest transport protocol in the TCP/IP stack. Its entire job is to take data from an application, slap an 8-byte header on it containing source port, destination port, length, and checksum, and hand it to IP for delivery. That is the complete specification. No handshake, no acknowledgment, no retransmission, no ordering, no flow control, no congestion control. UDP is fire-and-forget.

This simplicity is not a weakness — it is a deliberate design choice. TCP adds reliability, but that reliability costs latency (handshake before any data), overhead (20+ byte header, acknowledgment traffic), and complexity (state machines, timers, congestion windows). For many applications, that cost is not worth paying. DNS queries are a single request and a single response — if the response does not arrive, the client re-sends the query. Re-sending a DNS query is cheaper than maintaining a TCP connection for it. VoIP and live video need data to arrive fast; a retransmitted audio packet that arrives 500ms late is worse than dropping it. Fast-paced games send state updates many times a second; a lost update is superseded by the next one, so waiting for TCP to retransmit it (and holding back everything behind it) only adds lag.

UDP carries common DNS traffic, DHCP, NTP, and QUIC. It also matters when a UDP
socket reports an ICMP error or when protocol-specific firewall policy differs
from TCP. Incident prevalence depends on the environment; no universal DNS ranking
is assumed here.

## What You Will Build Toward

- Explain what UDP provides and what it deliberately omits, and why
- Draw the 8-byte UDP header from memory and explain every field
- Compare UDP to TCP for different workloads and justify the choice
- Capture and read a DNS query/response over UDP in tcpdump
- Explain how UDP error reporting works via ICMP Port Unreachable
- Identify the major protocols that use UDP and why each chose it over TCP
- Understand QUIC as the modern evolution — reliability built on top of UDP

---

## 10.1 What UDP Is

### What This Is

UDP — User Datagram Protocol — is defined in RFC 768 (1980). It is one of the shortest RFCs in existence: three pages. That brevity reflects the protocol's simplicity. UDP provides a way for applications to send datagrams (discrete messages) over IP using port numbers for multiplexing. Nothing else.

### UDP's Properties

```
WHAT UDP PROVIDES:
  ✓ Multiplexing via port numbers (source + destination port)
  ✓ Optional integrity checking via checksum
  ✓ Message boundaries (each send() = one datagram, received as one unit)

WHAT UDP DOES NOT PROVIDE:
  ✗ Connection establishment (no handshake)
  ✗ Reliable delivery (no acknowledgments, no retransmissions)
  ✗ Ordered delivery (datagrams may arrive out of order)
  ✗ Duplicate detection (same datagram may arrive twice)
  ✗ Flow control (no receiver window, no backpressure)
  ✗ Congestion control (no slow start, no cwnd)

  If the application needs any of these guarantees, the application
  must implement them itself — or use TCP instead.
```

### The UDP Header

The UDP header is 8 bytes. It is the smallest Layer 4 header in common use. Compare to TCP's minimum 20 bytes (and typically 32 bytes with options).

```
UDP HEADER (8 bytes)

  0                   1                   2                   3
  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |          Source Port          |       Destination Port        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |            Length             |           Checksum            |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

  Field            Bytes   What It Does
  ──────────────   ─────   ──────────────────────────────────────────
  Source Port       2      The sender's port number. Identifies the
                           sending process. Optional — can be 0 if
                           no reply is expected.

  Destination Port  2      The port the datagram is destined for.
                           Identifies the receiving process.

  Length            2      Total datagram length in bytes (header +
                           payload). Minimum = 8 (header only, no data).
                           Maximum = 65,535 bytes (theoretical, limited
                           by IP packet size: at most 65,507 bytes of
                           payload over IPv4 (65,535 − 20 − 8), and anything
                           over the path MTU minus 28 is fragmented — or,
                           with DF set, refused with EMSGSIZE).

  Checksum          2      Integrity check over the header, payload,
                           and a pseudo-header (src IP, dst IP, proto,
                           length). Optional in IPv4 (can be 0).
                           MANDATORY in IPv6 (RFC 8200; tunnel exceptions
                           in RFC 6935). On the sending host a capture
                           often shows "bad udp cksum": checksum offload,
                           Module 3.
```

### UDP vs TCP — Side by Side

```
                              UDP                         TCP
  ──────────────────────      ─────────────────────       ─────────────────────
  Header size                 8 bytes                     20+ bytes (typ. 32)
  Connection setup            None                        3-way handshake (1 RTT)
  Reliability                 None                        Full (seq/ack/retransmit)
  Ordering                    None                        Guaranteed
  Flow control                None                        Receiver window (rwnd)
  Congestion control          None                        Slow start, AIMD, BBR
  Message boundaries          Preserved                   Stream (no boundaries)
  State on server             None per client              Per-connection TCB
  Latency penalty             Zero (send immediately)     1 RTT minimum (handshake)
  Head-of-line blocking       None                        Yes (one lost segment
                                                           blocks all behind it)
  Error on closed port        ICMP Port Unreachable       TCP RST
                              (seen only by connected
                              sockets — see below)
  Typical use                 DNS, DHCP, NTP, VoIP,       HTTP, SSH, SMTP,
                              gaming, QUIC                 databases, file transfer
```

### Diagram: TCP vs UDP Communication

```
TCP — CONNECTION-ORIENTED (RELIABLE)

  [Client]                    [Server]
     │  SYN ─────────────────▶  │
     │  ◀──────────── SYN-ACK  │     1 RTT before any data
     │  ACK ─────────────────▶  │
     │                          │
     │  Data ────────────────▶  │     Data flows
     │  ◀──────────────── ACK  │     Every segment acknowledged
     │  Data ────────────────▶  │     Lost segment retransmitted
     │  ◀──────────────── ACK  │     Ordering guaranteed
     │                          │
     │  FIN ─────────────────▶  │     Teardown
     │  ◀──────────── FIN-ACK  │
     │  ACK ─────────────────▶  │
     │                          │

  Minimum overhead: 1 RTT handshake + ACKs for every segment


UDP — CONNECTIONLESS (UNRELIABLE)

  [Client]                    [Server]
     │                          │
     │  Datagram ────────────▶  │     No handshake
     │  ◀──────────── Reply    │     No ACK
     │                          │     No retransmission
     │  Datagram ────────────▶  │     No ordering
     │     (lost) ─ ─ ─ ✗      │     Lost = gone forever
     │                          │     (unless application re-sends)
     │  Datagram ────────────▶  │
     │  ◀──────────── Reply    │
     │                          │

  No transport handshake; still UDP/IP headers, socket state, and possible middlebox state
```

### How UDP Error Reporting Works

UDP has no built-in error signaling. When a UDP datagram arrives at a host and no process is listening on the destination port, the host sends an **ICMP Port Unreachable** (Type 3, Code 3) message back to the sender. This is the only error signal available.

```
UDP ERROR: PORT UNREACHABLE

  [Client]                              [Server — port 9999 not listening]
     │                                       │
     │  UDP datagram, dst port 9999   ──────▶│
     │                                       │  No process on port 9999!
     │  ◀── ICMP Type 3 Code 3 ─────────────│
     │      "Port 9999 unreachable"          │

  Compare to TCP:
  TCP sends RST (Reset) when the port is closed.
  UDP relies on ICMP, which is a different protocol entirely.

  IMPORTANT: If ICMP is blocked by a firewall, the sender NEVER learns
  that the port is closed. The datagram vanishes silently.
  This makes debugging UDP through firewalls significantly harder than TCP.

  EVEN WHEN THE ICMP ARRIVES, only some applications hear about it.
  Linux delivers the error (as ECONNREFUSED on the next send or receive)
  only to a CONNECTED UDP socket — one the application called connect()
  on, as dig and most clients do. A server-style UNCONNECTED socket
  (sendto/recvfrom) is never told; it just times out, unless it opts in
  with IP_RECVERR. The lab shows both, side by side.
```

### Production Relevance

UDP's lack of connection state means there is nothing for the server to "close" or "reset." A UDP "connection" in firewall terms is really just a conntrack entry — the firewall remembers that a packet went from source:port to dest:port and allows return traffic for an idle timeout. Linux uses `nf_conntrack_udp_timeout` = 30 s for a flow that has seen traffic in one direction only and `nf_conntrack_udp_timeout_stream` = 120 s once it has seen both directions; cloud security groups and NAT gateways have their own, provider-specific values. If the application stops sending, the entry expires and the return path closes. This is why UDP-based services can appear to "go silent" through stateful firewalls — the firewall expired the entry while the application was still waiting for a response. The lab reproduces it: a reply that comes 8 s later passes with a 30 s timeout and is dropped with a 5 s one.

---

## 10.2 When UDP Is the Right Choice

### What This Is

The choice between UDP and TCP is not "fast vs reliable." It is about which trade-offs make sense for the application's communication pattern. UDP is the right choice when at least one of these conditions is true:

### Decision Framework

```
CHOOSE UDP WHEN:

  1. Latency matters more than reliability
     ──────────────────────────────────────
     Voice/video calls: a dropped audio frame is inaudible.
     A retransmitted frame arriving 200ms late causes a glitch.
     Drop it and move on.

     Gaming: player position updates arrive 30-60 times/second.
     A missed update is corrected by the next one. Retransmission
     would cause stutter.

  2. The interaction is request-response (query/reply)
     ─────────────────────────────────────────────────
     DNS: one question, one answer. If it doesn't arrive,
     re-send the question. A new TCP connection for a single
     100-byte exchange adds a full RTT (the handshake) and, in the
     lab, turns 2 packets into 10.

     DHCP: client doesn't even have an IP yet — cannot do
     a TCP handshake.

  3. The application implements its own reliability
     ──────────────────────────────────────────────
     QUIC: builds reliable, multiplexed streams on top of UDP.
     Gets the benefits of reliability without TCP's head-of-line
     blocking problem. One lost packet on one stream doesn't
     block other streams.

     TFTP: simple file transfer with its own ACK mechanism
     built on top of UDP.

  4. Broadcast or multicast is needed
     ──────────────────────────────────
     TCP is point-to-point only. It cannot do broadcast or multicast.
     UDP supports both. Used by: service discovery (mDNS),
     streaming to multiple receivers (IPTV), DHCP.

  5. Minimal server-side state is required
     ──────────────────────────────────────
     Busy DNS servers answer hundreds of thousands of queries per
     second each. A TCP connection per query would cost a handshake,
     memory, and a file descriptor apiece. UDP requires no per-client
     state on the server (though firewalls and NATs on the path still
     keep some).


WHATEVER YOU CHOOSE, a UDP application owns what TCP would have done
for it: timeouts, retries with backoff, pacing, and some response to
congestion (RFC 8085 is the guide). The lab measures what retries buy.

CHOOSE TCP WHEN:

  1. Every byte must arrive, in order, exactly once
     (file transfer, web pages, database queries, email)

  2. The session is long-lived
     (SSH, database connections, HTTP keep-alive)

  3. Flow control is needed
     (large transfers where the receiver may be slower than sender)

  4. Congestion control is needed
     (bulk transfers that should share bandwidth fairly)
```

### QUIC — Reliability on Top of UDP

QUIC (RFC 9000) is the most significant evolution of UDP in production networking. It was developed by Google and standardized by the IETF. HTTP/3 runs exclusively over QUIC.

```
QUIC — WHAT IT IS AND WHY IT USES UDP

  The Problem with TCP for HTTP:
  ──────────────────────────────
  HTTP/2 multiplexes many streams over one TCP connection.
  If ONE TCP segment is lost, ALL streams are blocked until
  it's retransmitted. This is TCP head-of-line blocking.

  ┌─ HTTP/2 over TCP ───────────────────────────────────────┐
  │  Stream 1: ████████████████████                         │
  │  Stream 2: ██████████ ← BLOCKED (waiting for Stream 1  │
  │  Stream 3: ██████████    retransmission)                │
  │                    ↑                                    │
  │              Lost TCP segment affects ALL streams       │
  └─────────────────────────────────────────────────────────┘


  QUIC's Solution:
  ─────────────────
  QUIC runs over UDP. It implements its own reliability per-stream.
  If a packet for Stream 1 is lost, only Stream 1 waits for retransmission.
  Streams 2 and 3 continue unaffected.

  ┌─ HTTP/3 over QUIC over UDP ─────────────────────────────┐
  │  Stream 1: ██████████ ← retransmitting, only this waits │
  │  Stream 2: ████████████████████████ ← unblocked         │
  │  Stream 3: ████████████████████████ ← unblocked         │
  │                                                         │
  │  Loss on one stream does NOT block others               │
  └─────────────────────────────────────────────────────────┘


  Why UDP and not a new protocol?
  ─────────────────────────────
  Deploying a new transport protocol on the internet is nearly impossible.
  Middleboxes (firewalls, NATs, load balancers) only understand TCP and UDP.
  A new protocol number would be dropped by most networks.
  By using UDP as the carrier, QUIC works through existing infrastructure.

  QUIC provides (on top of UDP):
  ✓ Reliable, ordered delivery per stream
  ✓ Multiplexed streams without head-of-line blocking
  ✓ 1-RTT handshake for transport + TLS together (TCP + TLS 1.3 needs 2)
  ✓ Optional 0-RTT resumption with prior state, if the server accepts it
  ✓ Built-in TLS 1.3 encryption (always encrypted, no option to run without)
  ✓ Connection migration (survives IP address changes — mobile networks)

  What this means for troubleshooting:
  - QUIC is encrypted end to end, headers included; a capture shows
    UDP to port 443 and little else. Use SSLKEYLOGFILE (Module 2) to
    decrypt your own client's traffic.
  - Networks that block or rate-limit UDP 443 do not break the web —
    browsers fall back to TCP (HTTP/2) — but they do make HTTP/3 look
    "flaky". Check whether UDP 443 is allowed before blaming the server.
  - Clients learn that a server supports HTTP/3 from an `Alt-Svc`
    header (or DNS HTTPS records) on an earlier TCP connection.
```

---

## 10.3 Common Protocols Using UDP

### DNS — Port 53

DNS is the most common UDP protocol you will encounter in production.

```
DNS OVER UDP

  Client query:   "What is the A record for api.lab.test?"
  Server response: "api.lab.test → 192.0.2.10"

  [Client]                        [DNS Resolver]
     │                                │
     │  UDP src=52431, dst=53         │
     │  DNS Query: A example.com      │
     │───────────────────────────────▶│
     │                                │
     │  UDP src=53, dst=52431         │
     │  DNS Response: 192.0.2.10      │
     │◀───────────────────────────────│

  One packet out, one packet back. No handshake. One RTT.

  With a new TCP connection, the same query requires:
  SYN → SYN-ACK → ACK → Query → Response → FIN … — 10 packets in the
  lab, and 2 RTTs before the answer (one extra RTT for the handshake).


  WHEN DNS USES TCP INSTEAD:
  ─────────────────────────
  1. Response is larger than 512 bytes (original limit) or
    larger than the advertised EDNS payload limit (inspect it; 1232 is common
    since DNS Flag Day 2020)
     → Server sets the TC (Truncated) bit in the UDP response
     → Client retries the query over TCP
     → If TCP/53 is blocked, large answers fail while small ones work —
       the lab's 737-byte TXT record shows the retry

  2. AXFR uses TCP; a sufficiently small IXFR response can use UDP
     (transferring the entire DNS zone between servers)

  3. DNS over TLS (DoT) — TCP port 853; DNS over HTTPS (DoH) — HTTPS on
     443 (TCP or QUIC); DNS over QUIC (DoQ) — UDP port 853

  4. Some resolvers use TCP proactively, or after repeated UDP failures
```

### DHCP — Ports 67 (Server) / 68 (Client)

```
DHCP — WHY IT MUST USE UDP

  A host running DHCP does not yet have an IP address.
  It cannot establish a TCP connection (TCP requires an IP).
  DHCP uses UDP broadcast to discover servers:

  DORA Process (Discover-Offer-Request-Acknowledge):

  [Client 0.0.0.0:68]                    [DHCP Server x.x.x.x:67]
     │                                        │
     │  UDP Broadcast (255.255.255.255)       │
     │  DHCP Discover: "I need an IP"          │
     │────────────────────────────────────────▶│
     │                                        │
     │  UDP (Broadcast or Unicast)            │
     │  DHCP Offer: "Here's 10.0.1.50"       │
     │◀────────────────────────────────────────│
     │                                        │
     │  UDP Broadcast                         │
     │  DHCP Request: "I'll take 10.0.1.50"  │
     │────────────────────────────────────────▶│
     │                                        │
     │  UDP (Broadcast or Unicast)            │
     │  DHCP Acknowledge: "Confirmed"         │
     │◀────────────────────────────────────────│

  Client src: 0.0.0.0 (has no IP yet)
  Client dst: 255.255.255.255 (broadcast — doesn't know server IP)
  TCP cannot work here — no IP address to establish a connection with.
```

### NTP — Port 123

```
NTP — TIME SYNCHRONIZATION

  Accurate time is critical for:
  - TLS certificate validation (expired cert = wrong clock)
  - Kerberos authentication (max 5-minute clock skew)
  - Distributed databases (transaction ordering)
  - Log correlation across multiple servers

  NTP uses UDP because:
  - Request/response is a single small packet each
  - Time accuracy requires minimal, predictable latency
  - TCP handshake would add RTT to every measurement
  - NTP packet: 48 bytes — TCP overhead would double the size

  [Client]                    [NTP Server]
     │  UDP dst=123            │
     │  "What time is it?"     │
     │────────────────────────▶│
     │                         │
     │  UDP src=123            │
     │  Timestamps + offset    │
     │◀────────────────────────│

  NTP measures the round-trip time and uses it to calculate
  the clock offset, correcting for network delay.
```

### Other UDP Protocols

```
PROTOCOL    PORT      WHAT IT DOES                      WHY UDP
──────────  ────      ────────────────────────────────  ─────────────────────
SNMP        161/162   Network management: polling        Simple request/response.
                      device stats, receiving traps       High query volume.

Syslog      514       Log forwarding between hosts       Fire and forget.
                                                          Some loss acceptable.

TFTP        69        Trivial File Transfer. Simple       Lock-step ACKs (one block
                      firmware loading for network         in flight), so it is slow
                      devices; PXE boot.                   over long RTTs; tiny client.

RTP         Dynamic   Real-Time Transport. Audio/video    Latency-critical.
                      streaming (VoIP, video calls).       Loss tolerable.

WireGuard   51820     VPN tunnel. Encapsulates            Tunnelling TCP inside
                      encrypted packets.                   TCP fights two sets of
                                                           retransmissions; UDP
                                                           leaves that to the inner
                                                           protocol.

QUIC        443       Modern transport for HTTP/3.        Builds own reliability.
                      Multiplexed, encrypted streams.      Avoids TCP HOL blocking.

mDNS        5353      Multicast DNS. Local service        Multicast requires UDP.
                      discovery (.local domains).          TCP is point-to-point only.

VXLAN       4789      Virtual Extensible LAN.             Encapsulation protocol
                      Overlay network tunneling.           for network virtualization.
                      (Linux's legacy default and
                      flannel use 8472 — check which
                      port your firewall must allow.)

IPsec NAT-T 4500      ESP wrapped in UDP to cross NAT.    NAT devices cannot track
                      Cloud site-to-site VPNs use it.     raw ESP (protocol 50).

Geneve      6081      Overlay encapsulation (AWS GWLB,     Same reasons as VXLAN.
                      OVN, some CNIs).
```

---

## Hands-On Lab: UDP in Action

### Lab Goals

- Capture DNS over UDP and over TCP and count what each costs; watch a truncated answer force a TCP retry
- Prove UDP has no handshake, and see message boundaries preserved
- See ICMP port unreachable reach a connected socket and never reach an unconnected one
- Lose part of a datagram to a small read buffer (MSG_TRUNC)
- Measure loss, and what application retries buy, against the probability arithmetic
- Watch a stateful firewall's UDP idle timeout drop a late reply

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iproute2`, `tcpdump`, `iptables`, `conntrack`, `dnsutils` (dig), `dnsmasq-base`
- Part 3 uses your own machine and the internet

The lab uses [`udplab`](../labs/go/cmd/udplab), a small Go tool written for it: `udplab echo` answers datagrams (optionally after a delay), and `udplab send` numbers its datagrams and reports how many were answered, retried, duplicated, reordered, or truncated, the RTT distribution, and any error the socket reported.

---

### Lab Part 1: Run the UDP Lab

[`udp.json`](../labs/netlab/udp.json):

```
  client 198.18.40.10 ── fw (stateful: allows replies, drops anything else inbound) ── server 198.18.41.10
                                                                                       dnsmasq :53 (api.lab.test, big.lab.test TXT)
                                                                                       udplab echo :9999, slow echo :9998 (replies after 8 s)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/udp.json        # about 5 minutes: the loss trials wait out timeouts
```

Excerpts from real runs:

**DNS over UDP vs TCP.**

```
check 1/23: a DNS query over UDP: two packets, no handshake
  | IP (tos 0x0, ttl 64, id 27075, offset 0, flags [none], proto UDP (17), length 81)
  |     198.18.40.10.40410 > 198.18.41.10.53: 33877+ [1au] A? api.lab.test. (53)
  | IP (tos 0x0, ttl 63, id 52443, offset 0, flags [DF], proto UDP (17), length 85)
  |     198.18.41.10.53 > 198.18.40.10.40410: 33877* 1/0/1 api.lab.test. A 192.0.2.10 (57)
check 3/23: count them
  | udp packets: 2
  | tcp packets: 10
```

Read the DNS fields tcpdump prints: `33877` is the transaction ID (the reply repeats it); `+` means recursion desired; `[1au]` is one additional record — the EDNS OPT record dig adds; `A?` is the question; in the reply `*` means authoritative, `1/0/1` counts answer/authority/additional records, and `(53)`/`(57)` are the DNS message sizes. IP length 81 = 20 (IP) + 8 (UDP) + 53. The query left with DF clear and the reply with DF set — two programs, two socket settings. Over TCP the same exchange took 10 packets: handshake, query, ACK, answer, ACK, and a 4-packet close. (dig's SYN advertises `mss 1220`: BIND's tools deliberately keep DNS segments small to avoid fragmentation trouble.)

**Truncation.**

```
check 4/23: a 737-byte answer without EDNS: the server sets TC (truncated), dig retries over TCP
  | ;; Truncated, retrying in TCP mode.
  | ;; SERVER: 198.18.41.10#53(198.18.41.10) (TCP)
  | ;; MSG SIZE  rcvd: 737
  | 198.18.40.10.58979 > 198.18.41.10.53: 47659+ TXT? big.lab.test. (30)
  | 198.18.41.10.53 > 198.18.40.10.58979: 47659*| 0/0/0 (30)
  | 198.18.40.10.34273 > 198.18.41.10.53: Flags [S], …
check 5/23: the truncated UDP answer itself, as dig sees it when told not to retry
  | ;; flags: qr aa tc rd ra; QUERY: 1, ANSWER: 0, AUTHORITY: 0, ADDITIONAL: 0
```

`+noedns` limits the UDP answer to the classic 512 bytes; the 737-byte answer does not fit, so the server sends an empty answer with the TC bit (`|` in tcpdump, `tc` in dig's flags) and the client repeats the question over TCP. A network that allows UDP/53 but blocks TCP/53 therefore breaks exactly the large answers — DNSSEC-signed responses, many TXT records, long CNAME chains — and nothing else.

**No handshake, and boundaries kept.**

```
check 6/23: an application exchange: one datagram each way; the receiver saw message boundaries intact
  | 198.18.40.10.43845 > 198.18.41.10.9999: UDP, length 100
  | 198.18.41.10.9999 > 198.18.40.10.43845: UDP, length 100
check 7/23: while it runs, a connected UDP socket shows as ESTAB in ss — a local setting, no packet is sent
  | ESTAB 0      0       198.18.40.10:43845 198.18.41.10:9999 users:(("udplab",pid=983,fd=3))
check 8/23: the server's socket is UNCONN: it answers anyone
  | UNCONN 0      0       198.18.41.10:9999      0.0.0.0:*    users:(("udplab",pid=922,fd=3))
```

The first packet carries data. `ESTAB` for UDP only means the client called `connect()`: the kernel fixed the peer address and will filter replies from anyone else — and, as the next checks show, it will now deliver ICMP errors.

**Who hears about ICMP errors.**

```
check 9/23: a closed port answers with ICMP port unreachable; the connected socket reports ECONNREFUSED
  | socket error: connection refused (ICMP port unreachable) × 2
  | 198.18.41.10 > 198.18.40.10: ICMP 198.18.41.10 udp port 9997 unreachable, length 64
check 10/23: the same ICMP, an unconnected socket: the kernel never tells it, so it waits out its timeout
  | answered 0/2 = 0.0% (first try 0 = 0.0%)
  | note: an unconnected UDP socket is never told about ICMP errors; lost and refused look the same (silence)
```

Same packets on the wire, two different experiences. A client library that uses unconnected sockets reports "timeout" for a service that is simply not running — so check the capture for the ICMP message before concluding that packets are being lost.

**Truncated reads.**

```
check 11/23: a reply bigger than the read buffer is cut, not split: MSG_TRUNC, and the rest is gone
  | truncation: read 100 bytes into a 100-byte buffer; the kernel set MSG_TRUNC and discarded the rest of a 1000-byte datagram
```

A TCP reader with a small buffer just reads again; a UDP reader loses the remainder of that datagram for good. Size receive buffers for the protocol's largest message.

**Loss and retries, measured.** The server drops 30% of the lab's datagrams in each direction (`iptables -m statistic --mode random --probability 0.3`), and `udplab send` makes 500 requests:

| Run | No retries (expect 0.7 × 0.7 = 49%) | 2 retries (expect 1 − 0.51³ = 86.7%) |
| --- | --- | --- |
| 1 | 250/500 = 50.0% | 426/500 = 85.2% (874 datagrams sent for 500 requests) |
| 2 | 238/500 = 47.6% | 428/500 = 85.6% |
| 3 | 245/500 = 49.0% | 442/500 = 88.4% |

Then DNS under 50% loss each way, 40 lookups per row:

| Transport | Expectation | Run 1 | Run 2 | Run 3 |
| --- | --- | --- | --- | --- |
| UDP, 1 try (`+tries=1`) | 0.5 × 0.5 = 25% (10/40) | 9/40 | 11/40 | 6/40 |
| UDP, 3 tries | 1 − 0.75³ ≈ 58% (23/40) | 26/40 | 23/40 | 22/40 |
| TCP, 1 try, 6 s limit | most, if time allows | 32/40 | 27/40 | 28/40 |

Forty trials is a small sample — 6/40 and 11/40 are both ordinary outcomes when the true rate is 25% — which is the honest way to read any loss measurement.

Three lessons. Loss compounds across the two directions of a request/response — a link with "only" 30% loss each way fails half the exchanges. Retries recover most of it, at the price of extra traffic precisely when the network is struggling (hence RFC 8085's insistence on backoff). And TCP's own retransmission is not magic: under heavy loss its exponential backoff runs out the clock (the SYN alone is retried after 1, 3, 7 s…), so some lookups still fail within 6 s.

**A stateful firewall forgets a quiet flow.**

```
check 20/23: the slow service replies after 8 s; with the firewall's default 30 s UDP timeout the reply gets through
  | answered 1/1 = 100.0% (first try 1 = 100.0%)
  | rtt ms: min 8005.910 …
check 21/23: the firewall's idle timeout for UDP flows that have not seen a reply: 30 s; lower it to 5 s
  | net.netfilter.nf_conntrack_udp_timeout = 30
  | net.netfilter.nf_conntrack_udp_timeout_stream = 120
check 22/23: now the 8-second reply arrives after the firewall forgot the flow, and is dropped
  | answered 0/1 = 0.0% (first try 0 = 0.0%)
check 23/23: the evidence: the default-drop rule counted the late reply …
  |        1       92 DROP       0    --  *      *       0.0.0.0/0            0.0.0.0/0
```

92 bytes is the late reply (20 + 8 + 64). Cloud NAT gateways and security groups behave the same way with their own timeouts, which is why long-polling or rarely-chatty UDP protocols (SIP, some game and IoT protocols, WireGuard) send keepalives — WireGuard's `PersistentKeepalive = 25` exists for exactly this.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/udp.json
alias lab='sudo ./bin/netlab exec ../netlab/udp.json'
```

1. **conntrack's UDP states.** `lab client -- ./bin/udplab send -to 198.18.41.10:9997 -n 1`, then `lab fw -- conntrack -L -p udp`: the flow to the closed port is `[UNREPLIED]` — the ICMP error does not count as a reply. Repeat to port 9999 and compare.
2. **The server counts what it refuses.** `lab server -- nstat -az UdpNoPorts`, send three datagrams to the closed port 9997, and read it again: it rises by three — one per ICMP port unreachable sent. On a real server, a rising `UdpNoPorts` means clients are talking to a service that is not there; a rising `UdpRcvbufErrors` means datagrams arrived faster than the application read them.
3. **EMSGSIZE and fragments.** `lab client -- ./bin/udplab send -to 198.18.41.10:9999 -n 1 -size 70000` reports `message too long (EMSGSIZE)`: no such datagram can exist. With `-size 3000` it is answered, and a capture on fw shows it as three IP fragments (1500, 1500, and 68 bytes, offsets 0, 1480, 2960) — every one of which must arrive for the datagram to exist.
`sudo ./bin/netlab down ../netlab/udp.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
ss -uanp                                   # UDP sockets: UNCONN servers, ESTAB connected clients
dig example.com +stats | grep -E 'SERVER|Query time|MSG SIZE'
dig example.com +tcp +stats | grep -E 'SERVER|Query time'
dig DNSKEY . +dnssec | grep 'MSG SIZE'     # a large answer: over the EDNS limit? watch for TC and a TCP retry
nstat -az | grep -E 'Udp(InErrors|RcvbufErrors|NoPorts)'   # datagrams dropped by your own kernel
```

`UdpNoPorts` counts datagrams that arrived for a port nobody listens on (each one answered with ICMP port unreachable); `UdpRcvbufErrors` counts datagrams dropped because an application did not read fast enough — loss that no network capture between hosts will show. Linux's `netem` (`tc qdisc add dev eth0 root netem loss 10%`) is the other way to impair a link; it is in Ubuntu's standard kernel but may need `linux-modules-extra` on cloud kernels, which is why the lab uses `iptables -m statistic` instead.

---

## Real-World Troubleshooting Scenario

### Scenario: "DNS Queries From Kubernetes Pods Are Intermittently Failing"

**The situation:**

A Kubernetes cluster runs applications that make frequent DNS lookups to resolve internal service names. Monitoring shows that approximately 2-5% of DNS queries from pods return `SERVFAIL` or time out, while the same queries from the node host succeed consistently. The CoreDNS pods are healthy, CPU and memory are normal, and the CoreDNS logs show no errors.

**The diagnosis:**

**Step 1 — Capture DNS traffic from a failing pod:**

```bash
kubectl exec -it <pod-name> -- tcpdump -i eth0 -nn 'udp port 53' -c 20
```

**Observation:** Some DNS queries go out but no response ever comes back. The query packets are sent, but responses are missing for the failing queries.

**Step 2 — Capture on the CoreDNS pod:**

```bash
# Find the CoreDNS pod
kubectl get pods -n kube-system -l k8s-app=kube-dns

# Capture incoming queries
kubectl exec -it <coredns-pod> -n kube-system -- tcpdump -i eth0 -nn 'udp port 53' -c 50
```

**Observation to establish:** correlate each failing question by endpoints,
transaction ID, question, and time. If CoreDNS received and answered that question,
a first-query insertion drop cannot explain it; inspect the return path. A returned
SERVFAIL is a different observation from no response. Use a debug container when
the CoreDNS image has no shell or tcpdump.

**Step 3 — Check conntrack table on the node:**

```bash
# On the node where the failing pod runs
sudo conntrack -S | grep drop
```

**Output:**

```
cpu=0           found=0 invalid=1342 insert=0 insert_failed=298 drop=298 ...
                                                                    ↑
                                                            DROPS! Conntrack
                                                            is dropping packets.
```

**Hypothesis, not yet a root cause:** conntrack counters may indicate contention
or drops but must be measured as deltas for the failing flow. Different pod source
IPs mean different five-tuples even when ports match. Historical simultaneous
A/AAAA resolver behavior has exposed UDP conntrack races in specific kernels;
verify the actual resolver, relevant kernel fix, and distribution backport instead
of assuming a universal before/after Linux 5.0 boundary.

A candidate historical mechanism, requiring a reproducer and matching evidence, is:

1. glibc sending both A and AAAA queries simultaneously (two UDP packets from the same source port)
2. Linux conntrack racing on the entry creation for identical 5-tuples
3. UDP having no retransmission — if the packet is dropped, the DNS query simply fails

**Why TCP DNS would not have this problem:** TCP creates the conntrack entry during the SYN handshake, and a dropped SYN is retransmitted after a second. The race only affects the first packet of a flow — which for UDP is the query itself, and a dropped query is recovered only by the resolver's own retry, after its timeout (glibc's default is 5 s: the signature of this problem was lookups that took exactly 5 seconds).

**Choose a fix only after discrimination:** reproduce with the application's actual
libc lookup, not an A-only dig loop. Consult the distribution's kernel advisory and
upgrade/backport where the identified race applies. Resolver-specific options such
as `single-request-reopen` or TCP can be diagnostic/workaround comparisons, with
latency and compatibility trade-offs. Lowering ndots changes search amplification,
not the underlying race. NodeLocal DNSCache changes the path and cache behavior;
verify its mode rather than claiming every configuration avoids all conntrack.

**Why UDP knowledge mattered:**

- Understanding that UDP has no transport retransmission explained why a drop became a 5-second stall or a failure (the lab measures what application retries recover)
- Understanding that UDP "connections" in conntrack are created on the first packet (the query) explained where the drop happens
- Understanding that UDP's fire-and-forget nature means the application must handle the failure (and `dig` with `+tries=1` proves the failure)

---

## Common Failure Patterns

| Symptom                                    | UDP-Related Cause                                         | How to Confirm                                      | Fix                                                  |
| ------------------------------------------ | --------------------------------------------------------- | --------------------------------------------------- | ---------------------------------------------------- |
| DNS queries intermittently fail            | Conntrack race (Kubernetes), packet loss, or ICMP blocked | `conntrack -S \| grep drop`, `tcpdump udp port 53`  | NodeLocal DNS, single-request-reopen, upgrade kernel |
| "Connection refused" for UDP service       | Client received ICMP Port Unreachable (connected socket)  | `tcpdump icmp` on client — look for Type 3 Code 3   | Start the service / check the port number            |
| UDP client times out, service not running  | Unconnected socket: the ICMP error is never delivered     | Capture shows ICMP port unreachable; `nstat UdpNoPorts` on server | Start the service; prefer connected sockets for clients |
| Small DNS answers work, large ones fail    | TCP/53 blocked (truncated UDP answer retried over TCP) or fragments dropped | `dig +noedns` shows `tc`; `dig +tcp` fails          | Allow TCP/53; keep EDNS buffer at 1232               |
| Datagrams "lost" though the network delivered them | Receiver too slow or buffer too small            | `nstat -az UdpRcvbufErrors` rising on the receiver  | Read faster, raise SO_RCVBUF                         |
| Lookups take exactly 5 s, then succeed     | First query dropped (conntrack race, loss); resolver retried after its timeout | Capture: query with no answer, identical retry 5 s later | Fix the drop; tune resolver timeout/attempts         |
| UDP service works locally, not remotely    | Firewall blocking UDP but allowing TCP                    | An application-level probe (dig, `udplab send`) — `nc -u -z` "succeeds" without any reply | Add UDP firewall rule                                |
| VoIP/video calls dropping                  | UDP packet loss on the path                               | `mtr --udp -c 100 <host>` — check Loss%             | Investigate link congestion, QoS                     |
| NTP sync failing, clock drifting           | UDP port 123 blocked by firewall                          | `tcpdump udp port 123` — no responses               | Allow UDP 123 outbound                               |
| DHCP not assigning IPs                     | UDP ports 67/68 blocked, no DHCP server reachable         | `tcpdump -i eth0 udp port 67 or port 68`            | Check DHCP server, allow broadcasts                  |
| UDP responses stop after idle period       | Stateful firewall conntrack entry expired                 | Check firewall conntrack timeout; send keepalives   | Increase UDP conntrack timeout or add keepalives     |
| Application sees no error but data is lost | UDP silently drops with no notification to sender         | `tcpdump` on both sender and receiver — compare     | Application must implement its own detection         |

---

## Summary

- UDP is a minimal transport protocol: 8-byte header, no handshake, no acknowledgments, no retransmissions, no ordering, no flow control, no congestion control. It adds port-based multiplexing to IP and nothing else.
- UDP's simplicity is its strength. It adds zero latency (no handshake), minimal overhead (8 bytes vs 20+ for TCP), and requires no per-client state on the server. This makes it ideal for DNS, DHCP, NTP, VoIP, gaming, and streaming — and it makes the application responsible for timeouts, retries, and backoff.
- Loss compounds across a request and its response: 30% loss each way answered 48–50% of requests in the lab (0.7² = 49%); two retries raised it to 85% (1 − 0.51³ ≈ 87%).
- When a UDP datagram arrives at a closed port, the host sends ICMP Port Unreachable (Type 3 Code 3). Only a connected socket is told (ECONNREFUSED); an unconnected one just times out. If ICMP is blocked, nobody is told — the datagram silently disappears. This makes UDP harder to debug through firewalls than TCP.
- UDP preserves message boundaries: each `send()` produces one datagram, received as one `recv()`. TCP is a byte stream with no inherent message boundaries.
- QUIC is the most important modern use of UDP. It builds reliable, multiplexed, encrypted streams on top of UDP, solving TCP's head-of-line blocking problem. HTTP/3 runs over QUIC.
- DNS defaults to UDP for speed (2 packets, 1 RTT) but falls back to TCP when responses exceed the EDNS buffer size, for zone transfers, and for DNS over TLS.
- Stateful firewalls track UDP "connections" via conntrack entries with an idle timeout. If an application stops sending UDP packets, the conntrack entry expires and return traffic is blocked. This is a common cause of "it was working, then stopped" for UDP services.
- In Kubernetes, the combination of UDP's fire-and-forget nature and Linux conntrack race conditions causes the well-known DNS resolution failures. Understanding this requires understanding both UDP's lack of retransmission and how conntrack handles UDP flows.

## What's Next

Module 11 covers TCP — the Transmission Control Protocol. Now that you understand what UDP provides (minimal multiplexing) and what it omits (everything else), the next module covers TCP's approach to the same problem: reliable, ordered, connection-oriented delivery with all the complexity that entails — the 3-way handshake, sequence numbers, acknowledgments, flow control, congestion control, and the full TCP state machine.

---

## Quick Reference Card

```
MODULE 10: UDP — USER DATAGRAM PROTOCOL — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

UDP HEADER (8 bytes)
  Source Port       (2 bytes)   Sender's port
  Destination Port  (2 bytes)   Receiver's port
  Length            (2 bytes)   Total datagram size (header + payload)
  Checksum          (2 bytes)   Integrity check (optional IPv4, mandatory IPv6)

UDP vs TCP
  UDP: 8-byte header, no handshake, no reliability, no ordering
  TCP: 20+ byte header, 3-way handshake, full reliability, ordered

COMMON UDP PROTOCOLS
  DNS       Port 53       Name resolution (falls back to TCP for large responses)
  DHCP      Ports 67/68   IP address assignment (requires broadcast)
  NTP       Port 123      Time synchronization
  SNMP      Ports 161/162 Network monitoring
  Syslog    Port 514      Log forwarding
  QUIC      Port 443      HTTP/3 transport (reliability built on top of UDP)
  WireGuard Port 51820    VPN tunneling
  VXLAN     Port 4789     Network overlay encapsulation

ERROR HANDLING
  Closed UDP port → ICMP Type 3 Code 3 (Port Unreachable)
  Connected socket → ECONNREFUSED; unconnected socket → nothing (timeout)
  If ICMP blocked → silent drop, no error to sender

ESSENTIAL COMMANDS
  ss -ulnp                     List UDP listening sockets
  ss -uanp                     List all UDP sockets (incl. "connected")
  sudo tcpdump -nn 'udp port 53'   Capture DNS traffic
  sudo tcpdump -nn 'udp'           Capture all UDP traffic
  dig @8.8.8.8 example.com         UDP DNS query (default)
  dig @8.8.8.8 example.com +tcp    TCP DNS query (for comparison)
  nc -u -zv <host> <port>          Sends a datagram; "succeeded" does NOT mean anyone answered
  sudo tc qdisc add dev eth0 root netem loss 10%   Simulate packet loss (needs sch_netem)
  sudo tc qdisc del dev eth0 root                  Remove simulation
  iptables -A INPUT -p udp --dport N -m statistic --mode random --probability 0.1 -j DROP
                                                   Loss without netem
  nstat -az | grep Udp            Kernel drop counters (NoPorts, RcvbufErrors, InErrors)
  udplab send -to H:P -n 100      Course tool: answered %, retries, RTTs, socket errors
  sudo conntrack -L -p udp         List UDP conntrack entries
  sudo conntrack -S                Conntrack statistics (check drops)

WHEN TO USE UDP vs TCP
  UDP: latency-critical, request/response, broadcast/multicast, app-level reliability
  TCP: every-byte-matters, long-lived sessions, flow/congestion control needed

FIREWALLS AND UDP
  Stateful FW tracks UDP via conntrack (no real connection to track)
  Linux conntrack: 30 s until a reply is seen, 120 s once both directions
  have traffic (nf_conntrack_udp_timeout / _stream); clouds differ
  If idle too long, entry expires → return traffic blocked
  Fix: application keepalives or increase UDP timeout

TROUBLESHOOTING SIGNALS
  DNS query no response          UDP port 53 blocked, or conntrack drop
  ICMP Port Unreachable          Service not running on that port
  Intermittent DNS failures      Conntrack race (Kubernetes), packet loss
  UDP works then stops           Conntrack entry expired on stateful firewall
  nc -u times out, nc -t works   Firewall allows TCP but blocks UDP
```

---

## References

- RFC 768 — User Datagram Protocol — the original specification (3 pages)
- RFC 9000 — QUIC: A UDP-Based Multiplexed and Secure Transport
- RFC 9114 — HTTP/3
- RFC 1035 — Domain Names — Implementation and Specification (DNS)
- RFC 2131 — Dynamic Host Configuration Protocol (DHCP)
- RFC 5905 — Network Time Protocol Version 4 (NTP)
- `man udp` — Linux UDP socket implementation
- `man ss` — socket statistics (UDP and TCP)
- `man tc-netem` — network emulation for testing
- RFC 8085 — UDP Usage Guidelines (timeouts, retries, congestion control for UDP applications)
- RFC 8900 — IP Fragmentation Considered Fragile; DNS Flag Day 2020 (EDNS buffer 1232)
- RFC 7766 — DNS Transport over TCP; RFC 7858 (DoT), RFC 8484 (DoH), RFC 9250 (DoQ)
- `man 7 udp` — `IP_RECVERR`, connected vs unconnected error reporting
- Course tools: [`cmd/udplab`](../labs/go/cmd/udplab), [`netlab/udp.json`](../labs/netlab/udp.json)
- `man conntrack` — connection tracking tool
- Kubernetes DNS resolution known issues — https://github.com/kubernetes/kubernetes/issues/56903
