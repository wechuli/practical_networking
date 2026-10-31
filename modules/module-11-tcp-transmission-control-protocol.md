# Module 11: TCP — Transmission Control Protocol

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 10 — UDP — User Datagram Protocol
> **Estimated time:** 4–5 hours

## Overview

TCP turns IP's best-effort packets into a reliable, ordered byte stream between two applications — as long as the connection can make progress. When it cannot, TCP reports an error (eventually). It does not guarantee that the peer application processed the data, that an operation happened exactly once, or that anything was written to disk; an ACK means "the remote TCP has these bytes", nothing more. Retrying a request after a connection error therefore needs idempotency or deduplication at the application layer.

TCP is a set of engineering trade-offs, and most TCP trouble tickets are one of those trade-offs showing through:

- **Reliability costs latency.** The handshake costs a round trip before the first byte, and a lost packet is repaired only after the loss is detected — within a round trip if TCP notices quickly, after a retransmission timeout (200 ms or more, doubling each time) if it does not.
- **Ordering costs throughput.** A lost segment holds up every byte behind it (head-of-line blocking).
- **Fairness costs speed.** Congestion control deliberately sends less than the link might carry, and starts every new connection slowly.
- **State costs resources.** Each connection holds buffers, timers and a slot in the kernel's tables — at both ends, and in every stateful firewall, NAT and load balancer in between. Leaked or lingering states (CLOSE_WAIT, TIME_WAIT) are a classic production failure.

This module covers TCP at the depth a support engineer needs: the header fields in every capture; the handshake and the teardown flag by flag, including how each kind of connection failure looks on the wire and to the application; sequence numbers, retransmission and SACK; flow control and the zero window; congestion control (slow start, CUBIC, BBR) and bufferbloat; the TCP states and how `ss` shows them; and the options (MSS, SACK, window scaling, timestamps) negotiated in the SYN. The lab measures all of it on a real Linux kernel.

## What You Will Build Toward

- Read a handshake, a data transfer and a teardown in tcpdump output, including relative sequence numbers and TCP options
- Tell "connection refused", "connection timed out" and "no route to host" apart — on the wire, in the errno, and in how long each takes
- Predict the SYN retransmission schedule (and know it changed in Linux 6.5)
- Recognise a full accept queue, a CLOSE_WAIT leak, an orphaned FIN_WAIT_2 and a zero window in `ss` output
- Explain fast retransmit, SACK and the retransmission timeout, and why an outage of 3 s stalls TCP for about 5 s
- Explain flow control (rwnd), congestion control (cwnd), the bandwidth-delay product, and why a loss-based sender fills buffers
- Explain the MSS, SACK, window scaling and timestamp options, MSS clamping, and why tcpdump shows segments larger than the MTU

---

## 11.1 What TCP Provides

TCP — Transmission Control Protocol — was specified in RFC 793 (1981); RFC 9293 (2022) is the current consolidated specification. It is a transport-layer (Layer 4) protocol: connection-oriented, reliable, ordered, byte-stream delivery between two applications identified by IP address and port.

```
TCP PROVIDES
  ✓ Connections          An explicit handshake before data flows; both ends keep state.
  ✓ Reliable stream      Lost data is retransmitted until acknowledged, or the
                         connection fails with an error. An ACK means "my TCP has
                         these bytes" — not "the application processed them".
  ✓ Ordering             Bytes reach the application in the order they were sent,
                         even when IP delivers segments out of order.
  ✓ Duplicate removal    Sequence numbers let the receiver drop copies.
  ✓ Flow control         The receiver advertises how much buffer it has left
                         (the window); the sender never sends more.
  ✓ Congestion control   The sender limits itself to what the path seems able to
                         carry, backing off on loss (or ECN marks, or rising delay).
  ✓ Byte stream          No message boundaries: two writes may arrive as one read,
                         one write as several reads. Framing is the application's job.

THE PRICE
  ✗ Latency              One RTT for the handshake before any data (two or more
                         with TLS on top)
  ✗ Overhead             20–60 byte header per segment (UDP: 8)
  ✗ State                Per-connection buffers and timers at both ends and in
                         every stateful middlebox
  ✗ Head-of-line block   One lost segment delays every byte behind it
  ✗ Slow start           New connections begin at about 14 KB per round trip
```

Things TCP does **not** provide that people assume it does: message boundaries; any timeout an application would consider reasonable (an established connection with unacknowledged data retries for about 15 minutes by default; an idle connection whose peer vanished can sit forever unless keepalives are on); and encryption or authentication (that is TLS, Module 13). HTTP/3 does not use TCP at all — it runs over QUIC, on UDP (Module 10).

---

## 11.2 The TCP Header

```
TCP HEADER (RFC 9293): 20 bytes, up to 60 with options

   0                   1                   2                   3
   0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |          Source Port          |       Destination Port        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                        Sequence Number                        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                    Acknowledgment Number                      |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |  Data |       |C|E|U|A|P|R|S|F|                               |
  | Offset| Rsrvd |W|C|R|C|S|S|Y|I|            Window             |
  |       |       |R|E|G|K|H|T|N|N|                               |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |           Checksum            |         Urgent Pointer        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                    Options (if Data Offset > 5)               |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

There are eight flag bits. Older diagrams show a ninth, NS (ECN nonce, RFC 3540); that experiment was declared historic by RFC 8311 in 2018 and the bit is reserved again.

```
FIELD                 SIZE   WHAT IT DOES                               IN TCPDUMP
────────────────────  ─────  ─────────────────────────────────────────  ───────────────────────
Source port            16 b  Sender's port                              198.18.42.10.48628
Destination port       16 b  Receiver's port                            > 198.18.43.10.8080
Sequence number        32 b  Number of the first payload byte in this   seq 1:1449
                             segment (a SYN or FIN consumes one number)  (bytes 1–1448)
Acknowledgment number  32 b  Next byte the sender of this segment        ack 3001
                             expects: everything before it has arrived
Data offset             4 b  Header length in 32-bit words (5–15)
Flags                   8 b  CWR ECE URG ACK PSH RST SYN FIN            Flags [S.] etc.
Window                 16 b  Receive buffer space the sender of this     win 63
                             segment offers, in bytes << window scale
Checksum               16 b  Over header, payload and an IP pseudo-      (-v: "cksum … (correct)")
                             header (offloaded: often "incorrect" in a
                             capture on the sending host)
Urgent pointer         16 b  Only with URG; obsolete in practice
Options                0–40 B MSS, window scale, SACK, timestamps …     options [mss 1460,…]
```

### Flags as tcpdump prints them

```
FLAG   TCPDUMP  MEANING                                   WHEN YOU SEE IT
─────  ───────  ────────────────────────────────────────  ─────────────────────────────────
SYN    S        Synchronise: open, carries the initial    First packet of a connection
                sequence number and the options
ACK    .        The acknowledgment field is valid         Every segment after the first SYN
SYN+ACK S.      "Yes, and here is my sequence number"     Second packet of the handshake
PSH    P        Push: deliver to the application now;     Typically on the last segment of
                the sender has no more queued just now    a write; not a message boundary
FIN    F        No more data from me (graceful close)     Teardown; F. = FIN with ACK
RST    R        Reset: abort, discard state               Closed port, abort, unknown connection
URG    U        Urgent data (obsolete)                    Practically never
ECE    E        ECN echo: "a router marked congestion"    With ECN negotiated
CWR    W        Congestion window reduced (ECN response)  With ECN negotiated
```

Combinations read left to right: `[P.]` is PSH+ACK (data), `[F.]` FIN+ACK, `[R.]` RST+ACK (the usual answer to a SYN on a closed port), `[R]` a bare reset, `[S.E]` or `[SEW]` a SYN negotiating ECN.

**Relative sequence numbers.** tcpdump prints the real (random) sequence number in the SYN and SYN-ACK, then switches to numbers relative to it: `seq 1:1449` means bytes 1 to 1448 of this stream, `ack 1` means "nothing of yours yet besides your SYN". Pass `-S` to see absolute numbers — necessary when comparing captures taken on two hosts, or across a NAT or proxy that rewrites sequence numbers.

---

## 11.3 The Three-Way Handshake

Before any data flows, both ends agree to talk and learn each other's initial sequence number (ISN). Each side picks its ISN unpredictably (RFC 6528), so an off-path attacker cannot guess it and inject data.

```
THREE-WAY HANDSHAKE

  client 198.18.42.10:48628                         server 198.18.43.10:8080
     │  1  SYN  seq=2999890995                            │
     │     win 64240, mss 1460, sackOK, TS, wscale 10     │
     │───────────────────────────────────────────────────▶│ SYN_RECV
     │  2  SYN-ACK  seq=3450125985 ack=2999890996         │
     │     win 65160, mss 1460, sackOK, TS, wscale 10     │
     │◀───────────────────────────────────────────────────│
     │  3  ACK  ack=1 (relative)                          │
ESTAB│───────────────────────────────────────────────────▶│ ESTABLISHED
     │  data can follow immediately (or ride on packet 3) │
```

The SYN consumes one sequence number, so the SYN-ACK acknowledges ISN + 1. Each side announces its options in its own SYN; an option is used only if both offered it (MSS is the exception: each side simply tells the other the largest segment it wants to receive). Options cannot be renegotiated later. The client can send data right after packet 3, so a request costs one round trip of handshake before the first byte — plus TLS (Module 13) on top.

The lab captured exactly this (offloads off, so segments are as on the wire):

```
198.18.42.10.48644 > 198.18.43.10.8080: Flags [S], seq 1842242094, win 64240, options [mss 1460,sackOK,TS val 4042507224 ecr 0,nop,wscale 10], length 0
198.18.43.10.8080 > 198.18.42.10.48644: Flags [S.], seq 4271748024, ack 1842242095, win 65160, options [mss 1460,sackOK,TS val 1579184339 ecr 4042507224,nop,wscale 10], length 0
198.18.42.10.48644 > 198.18.43.10.8080: Flags [.], ack 1, win 63, options [nop,nop,TS val 4042507224 ecr 1579184339], length 0
198.18.42.10.48644 > 198.18.43.10.8080: Flags [.], seq 1:1449, ack 1, win 63, …, length 1448
198.18.42.10.48644 > 198.18.43.10.8080: Flags [.], seq 1449:2897, ack 1, win 63, …, length 1448
198.18.42.10.48644 > 198.18.43.10.8080: Flags [P.], seq 2897:3001, ack 1, win 63, …, length 104
198.18.43.10.8080 > 198.18.42.10.48644: Flags [.], ack 1449, win 67, …
198.18.43.10.8080 > 198.18.42.10.48644: Flags [.], ack 2897, win 70, …
198.18.43.10.8080 > 198.18.42.10.48644: Flags [.], ack 3001, win 70, …
```

`win 63` after the handshake is not 63 bytes: the client offered `wscale 10`, so its window is 63 × 1024 ≈ 64 KB. The window in a SYN is never scaled. A 3000-byte write left as 1448 + 1448 + 104 bytes: 1460 (MSS) minus 12 bytes of timestamp option per segment. `PSH` is set on the last segment of the write.

### How connection attempts fail

The errno an application gets — and how long it took to get it — tells you what happened on the wire. All of these were measured in the lab:

| What the network did | Client sees | errno | How long | Typical cause |
| --- | --- | --- | --- | --- |
| SYN answered with `RST` from the server | Connection refused | `ECONNREFUSED` | one RTT (0.25 ms in the lab) | Nothing listening on that port; process down; wrong port |
| SYN answered with `RST` forged by a firewall (`REJECT --reject-with tcp-reset`) | Connection refused | `ECONNREFUSED` | one RTT (to the firewall) | Firewall policy; the RST's TTL gives it away |
| SYN answered with ICMP port unreachable (plain `REJECT`) | Connection refused | `ECONNREFUSED` | one RTT | Firewall policy |
| SYN answered with ICMP host unreachable / admin prohibited | No route to host | `EHOSTUNREACH` | one RTT — or ~3 s if it is an ARP failure on the local subnet | Firewall `REJECT --reject-with icmp-host-prohibited`; host down on a local subnet |
| No route in the client's table | Network is unreachable | `ENETUNREACH` | immediately | Missing default route, wrong VPN routes |
| SYN (or SYN-ACK) silently dropped | Connection timed out | `ETIMEDOUT` | about 2 minutes with defaults (7 s in the lab with `tcp_syn_retries=2`) | Firewall `DROP`, security group, host down behind a router, full accept queue |

Two diagnostic tricks follow from the table. **"Refused" fast means something answered** — either the host (nothing listening) or a firewall impersonating it; compare the RST's IP TTL with the server's normal replies. In the lab the server's genuine RST arrived with TTL 63 (one router hop from 64) and the firewall's forged one with TTL 64. **"Timed out" means silence**, and the exact connect duration is a fingerprint of the SYN retransmission schedule: a connect that "always takes 1 s" or "3 s" succeeded on a retransmitted SYN, meaning the first SYN (or its answer) was lost — a full accept queue, a rate limiter, a flaky path.

### The SYN retransmission schedule (changed in Linux 6.5)

When a SYN goes unanswered, the client retransmits it. Classically the gap doubles from the initial RTO of 1 s: SYNs at 0, 1, 3, 7, 15, 31 and 63 s, giving up at 127 s with the default `net.ipv4.tcp_syn_retries = 6`. Linux 6.5 (2023) added `net.ipv4.tcp_syn_linear_timeouts` (default 4): the first retries are 1 s apart, then the doubling starts. With defaults that is SYNs at 0, 1, 2, 3, 4, 5, 7, 11, 19, 35 and 67 s, giving up at about 131 s. The total time is nearly the same; the early retries come faster, so a single lost SYN costs 1 s instead of up to 3.

Measured in the lab's client namespace (Linux 6.18) by capturing SYNs to a port the router silently drops:

| `tcp_syn_linear_timeouts` | `tcp_syn_retries` | SYNs sent at (s) | `connect()` gave up at |
| --- | --- | --- | --- |
| 4 | 1 | 0, 1, 2 | ~3 s |
| 4 | 2 | 0, 1.02, 2.04, 3.07, 4.10, 5.12 | 7.1 s |
| 4 | 3 | 0, 1, 2, 3.1, 4.1, 5.1, 7.1, 11.3 | 19.5 s |
| 4 | 4 | 0, 1, 2, 3.1, 4.1, 5.1, 7.1, 11.2, 19.4 | ~35 s |
| 0 (classic) | 2 | 0, 1, 3 | ~7 s |
| 0 (classic) | 3 | 0, 1, 3, 7.1 | ~15 s |

The rule the kernel applies: an attempt ends at the first retransmission timer that fires after the classic budget of 2^(n+1) − 1 seconds (n = `tcp_syn_retries`: 3, 7, 15, 31 … 127 s), or once n + `tcp_syn_linear_timeouts` retransmissions have been sent. Ubuntu 22.04's 5.15 kernel uses the classic schedule; Ubuntu 24.04 (6.8) and later use the linear start. Applications that set their own connect timeout (most HTTP clients, database drivers) give up long before either — which is why the same blocked port shows "timed out after 10 s" in one tool and after 2 minutes in another.

### When the SYN-ACK or the final ACK is lost

- **SYN-ACK lost:** the server sits in SYN_RECV and retransmits the SYN-ACK (`tcp_synack_retries = 5`, about a minute); the client retransmits its SYN too. One loss costs about a second.
- **Final ACK lost:** the client is ESTABLISHED and usually sends data at once; that data segment carries the same ACK and completes the handshake, so a single lost third ACK is normally harmless. Trouble starts when every client-to-server packet after the SYN is dropped (an asymmetric or half-applied firewall rule, a stale conntrack entry): the client believes it is connected, its first write vanishes, and the error surfaces much later as a read timeout.
- **Server-speaks-first protocols** (SMTP, FTP, MySQL) look different: the client connects and waits. If the server's first data never arrives, the symptom is "connected, then hangs", not a connect error.

### A full accept queue

A listening socket has two queues: half-open connections (SYN received, SYN-ACK sent) and completed connections waiting for the application to call `accept()`. The second one's length is the `backlog` passed to `listen()`, capped by `net.core.somaxconn` (4096 on current kernels). When the accept queue is full, Linux **silently drops new SYNs** (and, with the default `tcp_abort_on_overflow = 0`, also drops final ACKs it cannot queue). Clients see their connects stall for a SYN retransmission — 1 s — or time out completely.

The lab's server listens with backlog 2 and never calls `accept()`; five clients connect at once:

```
connect 1: connected   after   0.38ms  SYN, SYN-ACK, ACK
connect 2: connected   after   0.32ms  SYN, SYN-ACK, ACK
connect 3: connected   after   0.09ms  SYN, SYN-ACK, ACK
connect 4: timed out   after    7.14s  ETIMEDOUT: no answer to the SYN or its retransmissions …
connect 5: timed out   after    7.14s  ETIMEDOUT: …
summary: 3 connected, 2 timed out

server$ ss -ltn '( sport = :8083 )'
State  Recv-Q Send-Q Local Address:Port Peer Address:Port
LISTEN 3      2       198.18.43.10:8083      0.0.0.0:*
server$ nstat -saz TcpExtListenOverflows TcpExtListenDrops
TcpExtListenOverflows           12                 0.0
TcpExtListenDrops               12                 0.0
```

Linux queues backlog + 1 connections. On a **listening** socket `ss` repurposes its columns: `Recv-Q` is the number of connections waiting in the accept queue and `Send-Q` is the backlog. `Recv-Q` at or above `Send-Q` means the application is not accepting fast enough (busy, blocked, single-threaded, or out of file descriptors). `TcpExtListenOverflows` counted 12 dropped SYNs: two clients × six SYNs each. Fix the application's accept rate first; raising the backlog only buys a burst allowance.

---

## 11.4 Connection Teardown

### The graceful close

Each direction of a TCP connection is closed separately. A FIN means "I will send no more data"; the other side may keep sending until it closes too.

```
TEARDOWN (the client closes first)

  client                                                 server
  ESTABLISHED                                            ESTABLISHED
     │  FIN  seq=3001                                       │
     │─────────────────────────────────────────────────────▶│ CLOSE_WAIT  (application told: EOF)
  FIN_WAIT_1                                                │
     │            ACK  ack=3002                             │
     │◀─────────────────────────────────────────────────────│
  FIN_WAIT_2                                                │  application calls close()
     │            FIN  seq=1                                │
     │◀─────────────────────────────────────────────────────│ LAST_ACK
  TIME_WAIT                                                 │
     │  ACK  ack=2                                          │
     │─────────────────────────────────────────────────────▶│ CLOSED
  (60 s on Linux)
  CLOSED
```

In practice the server's ACK and FIN often travel together, as in the lab capture — three segments, not four:

```
198.18.42.10.48628 > 198.18.43.10.8080: Flags [F.], seq 3001, ack 1, …
198.18.43.10.8080 > 198.18.42.10.48628: Flags [F.], seq 1, ack 3002, …
198.18.42.10.48628 > 198.18.43.10.8080: Flags [.], ack 2, …
```

Whoever sends the first FIN is the **active closer** and ends in TIME_WAIT; the **passive closer** goes through CLOSE_WAIT and LAST_ACK to CLOSED and keeps nothing. Both are checked in the lab: right after the transfer, the client has `TIME-WAIT … timer:(timewait,59sec,0)` and the server has no TIME_WAIT socket at all.

A **half-close** — `shutdown(fd, SHUT_WR)` — sends a FIN while keeping the socket open for reading. The lab's client does exactly that: it sends its data, half-closes, and waits for the server's FIN as proof the server read everything (`write()` returning only means the local kernel buffered the data). The other end of a half-closed connection is legitimately in CLOSE_WAIT until it finishes sending.

### RST — the abortive close

A RST ends a connection at once: no FIN, no TIME_WAIT, unread data discarded at both ends. The peer's next `read()` or `write()` fails with `ECONNRESET` ("connection reset by peer"); a write after that gets `EPIPE` (and, unless ignored, `SIGPIPE`).

```
A HOST SENDS RST WHEN
  - a SYN arrives for a port nobody listens on                      (Flags [R.] answering [S])
  - an application closes with SO_LINGER set to 0 (deliberate abort)
  - an application closes a socket that still has unread received data (RFC 2525)
  - a segment arrives for a connection it has no record of — after a reboot, after the
    connection was reset, after a stateful device lost its entry, or for a stray old segment
  - a firewall or load balancer rejects (REJECT --reject-with tcp-reset), or an idle-timeout
    on a middlebox expires and it resets the next packet it sees (or both ends)
  NOT on a crash by itself: if a process dies, the kernel closes its sockets normally (FIN),
  or with RST if unread data was pending. If the whole host dies, nothing is sent at all.
```

In the lab, a server reads the first kilobyte and closes with `SO_LINGER 0`. The client was in the middle of writing 4 MiB:

```
transfer  INCOMPLETE after 0.68ms: wrote 130696 of 4194304 bytes
          ECONNRESET: the peer sent RST (it closed with unread data, aborted, or forgot the connection)
198.18.43.10.8084 > 198.18.42.10.57790: Flags [R.], seq 79536240, ack 845617186, win 77, …
198.18.43.10.8084 > 198.18.42.10.57790: Flags [R], seq 79536240, win 0, length 0
198.18.43.10.8084 > 198.18.42.10.57790: Flags [R], seq 79536240, win 0, length 0
… 15 RSTs in total
```

The first RST is the abort; each later one answers a data segment that was already in flight for a connection the server no longer has. A burst of RSTs like this in a capture is the aftermath of one abort, not fifteen problems.

### TIME_WAIT

```
TIME_WAIT
  Held by the active closer after it sends the final ACK.
  Linux: a fixed 60 s (TCP_TIMEWAIT_LEN, compiled into the kernel; not a sysctl, and not
  derived from MSL — RFC 9293's 2 × MSL would be 4 minutes).

  WHY
  1. If the final ACK is lost, the peer retransmits its FIN; TIME_WAIT can re-ACK it
     instead of answering with RST.
  2. Old duplicate segments of this connection die out before the same 4-tuple
     (source IP, source port, destination IP, destination port) can be reused.

  WHAT IT COSTS
  A small "minisocket" (no buffers, no file descriptor) and the use of that exact 4-tuple
  for 60 s. Thousands of TIME_WAIT sockets on a server are normal and cheap.
  It becomes a problem only for one client making very many short connections to the
  SAME destination IP and port: each needs a distinct source port, and the default
  ephemeral range (net.ipv4.ip_local_port_range = 32768 60999) holds 28,232 —
  about 470 new connections per second, sustained, before connects fail with
  EADDRNOTAVAIL ("Cannot assign requested address"). Module 20 measures this.

  MITIGATIONS, BEST FIRST
  - Reuse connections (HTTP keep-alive, connection pools) — removes the problem.
  - Spread over more destination ports/IPs or more source IPs.
  - net.ipv4.tcp_tw_reuse: 0 = off, 1 = on, 2 = loopback only (the default since 4.20).
    Lets a NEW OUTGOING connection take over a TIME_WAIT 4-tuple when timestamps prove
    old segments cannot be confused; it does nothing for incoming connections.
  - Widen ip_local_port_range (avoid ports your services listen on).
  - NOT net.ipv4.tcp_tw_recycle: it broke clients behind NAT and was removed in Linux 4.12.
  - NOT lowering tcp_fin_timeout: that is the FIN_WAIT_2 timeout, not TIME_WAIT's.
  - net.ipv4.tcp_max_tw_buckets caps the number of TIME_WAIT sockets; above it the kernel
    destroys them early and logs "TCP: time wait bucket table overflow".
```

### FIN_WAIT_2 and CLOSE_WAIT — the two halves of a stuck close

If the passive side never closes, the active side waits in FIN_WAIT_2 and the passive side sits in CLOSE_WAIT:

- **CLOSE_WAIT** has no timeout. The kernel waits for the application to call `close()`; until it does, the socket keeps its file descriptor and buffers. A socket that stays in CLOSE_WAIT is almost always an application that does not notice EOF (a read returning 0) or forgets to close on an error path. The lab's `hold` server shows it, with the owning process named by `ss -p`: `CLOSE-WAIT 0 0 198.18.43.10:8081 198.18.42.10:52326 users:(("tcplab",pid=1862,fd=3))`.
- **FIN_WAIT_2** times out only when the socket is *orphaned* — the application has already closed its descriptor. Then the kernel reaps it after `net.ipv4.tcp_fin_timeout` (60 s); `ss -o` shows such orphans with a `timer:(timewait,59sec,0)`, because Linux parks them in the same minisocket structure as TIME_WAIT. A process that only called `shutdown()` and still holds the socket waits for the peer's FIN indefinitely.

---

## 11.5 Sequence Numbers, Acknowledgments and Retransmission

### How TCP tracks data

Every byte has a sequence number. A segment's `seq` is the number of its first byte; an `ack` is the number of the next byte the receiver expects — "everything before this has arrived". ACKs are cumulative: one ACK can acknowledge many segments, and receivers delay ACKs to send fewer of them — Linux acknowledges at least every second full segment, and otherwise waits typically 40 ms (at most 200 ms); during slow start it ACKs immediately ("quickack").

```
sender: seq 1:1449      ───▶  receiver: ack 1449
sender: seq 1449:2897   ───▶  receiver: ack 2897
sender: seq 2897:3001   ───▶  receiver: ack 3001     ← all 3000 bytes received
```

### Detecting a loss: duplicate ACKs, SACK, RACK

```
LOSS OF ONE SEGMENT (classic view)

  seq 1001:2001 ───▶                      ack 2001
  seq 2001:3001 ──✗ (lost)
  seq 3001:4001 ───▶                      ack 2001, SACK {3001:4001}   duplicate ACK 1
  seq 4001:5001 ───▶                      ack 2001, SACK {3001:5001}   duplicate ACK 2
  seq 5001:6001 ───▶                      ack 2001, SACK {3001:6001}   duplicate ACK 3
                     → fast retransmit of 2001:3001, about one RTT after the loss
                                          ack 6001                      hole filled
```

The receiver keeps acknowledging the last in-order byte; each such duplicate ACK says "something arrived, but there is a hole". Classic TCP (RFC 5681) retransmits after three duplicate ACKs. With SACK (11.9) the receiver also lists the blocks it does hold, so the sender repairs exactly the holes. Modern Linux detects loss primarily by time (RACK, RFC 8985): a segment is presumed lost when a segment sent sufficiently later has already been acknowledged — robust against reordering and against losses at the end of a burst. For a tail loss, where nothing follows to trigger duplicate ACKs, Linux sends a **tail loss probe** (TLP) after about two RTTs, retransmitting the last segment to provoke a SACK.

The lab's 1% loss runs show what these mechanisms buy. Five 20 MiB transfers through a router dropping 1% of the client's segments, six runs of the lab:

| Run | Transfer times (5 × 20 MiB) | Fast retransmits per transfer | RTO timeouts |
| --- | --- | --- | --- |
| 1 | 29–42 ms (4.0–5.7 Gbit/s) | 126–153 | 0 |
| 2 | 37–59 ms (2.8–4.6 Gbit/s) | 133–162 | 0 |
| 3 | 33–46 ms (3.7–5.0 Gbit/s) | 135–183 | 0 |
| 4 | 38–55 ms, and one of 239 ms (0.7 Gbit/s) | 139–157 | 0 |
| 5 | 34–45 ms (3.7–4.9 Gbit/s) | 118–259 | 0 |
| 6 | 30–36 ms (4.7–5.7 Gbit/s) | 131–188 | 0 |

About 14,500 segments per transfer, 1% of them lost, every hole repaired by fast retransmit, not one timeout in 30 transfers. (One transfer took 239 ms instead of ~40 ms without any counted timeout — a stall the counters do not explain, and the reason to measure several runs before believing one.) The receiver's ACKs during the loss carry SACK blocks — `ack 330577, … sack 1 {332025:366777}` says "I have 332025 to 366776, I am missing 330577 to 332024".

### The retransmission timeout (RTO)

When the ACK clock stops entirely — every segment or every ACK lost — only the timer is left.

```
RTO (RFC 6298, as Linux implements it)
  RTO = SRTT + 4 × RTTVAR, measured continuously from ACKs (and timestamps)
  Initial RTO (before any RTT sample, e.g. for the SYN):  1 s
  Minimum RTO:  200 ms  (TCP_RTO_MIN; per-route "ip route … rto_min 50ms";
                         net.ipv4.tcp_rto_min_us on recent kernels)
  Maximum RTO:  120 s   (net.ipv4.tcp_rto_max_ms on recent kernels)
  Each timeout DOUBLES the RTO (exponential backoff) and collapses cwnd to 1 segment.

  The 200 ms floor is enormous next to a LAN RTT: the lab's links have an RTT of 0.05 ms,
  and tcp_info reports "rtt 0.05 ms … rto 204 ms". One timeout there costs about 4,000 RTTs.

  An established connection gives up after net.ipv4.tcp_retries2 = 15 timeouts —
  about 924.6 s (15.4 minutes) with a 200 ms starting RTO — and the application finally
  gets ETIMEDOUT. TCP_USER_TIMEOUT (a socket option) caps this per socket.
```

**An outage is stretched by backoff.** In the lab, the router black-holes all traffic for 3 s in the middle of a 10 MiB transfer. The client's silences, from the capture (4.9–5.1 s in total over six runs):

```
silent 0.21 s, then seq 4913064:4914512,      ← tail loss probe: the last segment sent
silent 0.32 s, then seq 4650976:4652424,      ← RTO: the first unacknowledged segment
silent 0.64 s, then seq 4650976:4652424,      ← RTO doubled
silent 1.25 s, then seq 4650976:4652424,      ← doubled again
silent 2.46 s, then seq 4650976:4652424,      ← the path has been back for ~1.9 s already
total silence 4.88 s; the path was down for 3 s
```

The path returned partway through the last wait, but TCP only found out at its next retransmission. A 30 s outage would leave connections silent for up to a minute longer than the outage itself. This is why, after a network blip, "the network is fine now but the application is still stuck" can be perfectly normal TCP behaviour for some seconds — and why long outages are better handled by application-level timeouts and reconnects than by waiting on TCP.

---

## 11.6 Flow Control

The receiver advertises how much more data it can buffer — the **receive window** (rwnd) — in every segment. The sender never has more unacknowledged data in flight than that window. When the receiving application stops reading, its buffer fills, the advertised window falls to zero, and the sender stops.

```
ZERO WINDOW

  sender                                   receiver (application not reading)
    │ data ─────────────────────────────▶ │ buffer filling
    │ ◀──────────────── ack, win 1448     │
    │ data ─────────────────────────────▶ │ buffer full
    │ ◀──────────────── ack, win 0        │ "stop"
    │ window probe (persist timer) ─────▶ │
    │ ◀──────────────── ack, win 0        │ probes back off: 0.2, 0.4, 0.9, 1.7, 3.5 s …
    │ …                                   │ application reads
    │ ◀──────────────── window update     │ "resume"
```

The sender's **persist timer** sends a probe — on Linux a zero-length segment carrying an old sequence number — so the receiver must answer with its current window; without probes, a lost window update would deadlock both sides. Probes back off exponentially, and as long as they are answered the connection never times out: a reader that stops reading can hold a writer for hours.

The lab's server never reads, with `SO_RCVBUF` set to 4096 bytes (the kernel doubles it for overhead, and offers a 2896-byte window with `wscale 0`). The client tries to send 1 MiB:

```
transfer  INCOMPLETE after 4.00s: wrote 69504 of 1048576 bytes
          timed out with data still unsent: the send buffer is full and not draining …
198.18.43.10.8082 > 198.18.42.10.34100: Flags [.], ack 5793, win 0, …
198.18.42.10.34100 > 198.18.43.10.8082: Flags [.], ack 1, win 63, …   ← probe after 0.22 s
198.18.43.10.8082 > 198.18.42.10.34100: Flags [.], ack 5793, win 0, …
198.18.42.10.34100 > 198.18.43.10.8082: Flags [.], ack 1, win 63, …   ← after 0.44 s
198.18.42.10.34100 > 198.18.43.10.8082: Flags [.], ack 1, win 63, …   ← after 0.86 s
198.18.42.10.34100 > 198.18.43.10.8082: Flags [.], ack 1, win 63, …   ← after 1.73 s

client$ ss -tnoi '( dport = :8082 )'
FIN-WAIT-1 0  63713  198.18.42.10:34100 198.18.43.10:8082 timer:(persist,2.380ms,0)
   … backoff:4 … bytes_acked:5793 … rwnd_limited:4348ms(100.0%) … notsent:63713 …
server$ ss -tn '( sport = :8082 )'
ESTAB 5792   0       198.18.43.10:8082 198.18.42.10:34100
```

Reading the evidence like a support engineer:

- On the **receiver**, `Recv-Q 5792` on an established socket is data the kernel received and acknowledged but the application has not read. A persistently non-zero Recv-Q means a slow or stuck reader.
- On the **sender**, `Send-Q 63713` is data written but not yet acknowledged (here, not even sent: `notsent`), and `rwnd_limited … (100.0%)` says the receiver's window, not the network, limited the connection the whole time. `timer:(persist,…)` is the persist timer. (The client process had exited by then, so the socket is orphaned in FIN-WAIT-1 with its FIN still queued behind the data.)
- A zero window is an **end-host problem** — the receiving application, or its CPU or disk — not packet loss. A capture filter for it: `tcp[14:2] = 0 and tcp[tcpflags] & tcp-ack != 0` (window field zero), or Wireshark's `tcp.analysis.zero_window`.

Normally nobody sets buffer sizes: Linux autotunes each connection's receive buffer between the bounds in `net.ipv4.tcp_rmem` (min, default, max — `4096 131072 6291456` on Ubuntu, a 32 MB maximum on newer kernels), growing it to fit the measured bandwidth-delay product. **Setting `SO_RCVBUF` explicitly turns autotuning off** for that socket, which is a common cause of mysteriously capped throughput on long paths. `net.core.rmem_max` caps what `SO_RCVBUF` may request, not what autotuning may use.

---

## 11.7 Congestion Control

Flow control protects the receiver; congestion control protects the network. The sender keeps a **congestion window** (cwnd), its estimate of how much data the path can hold, and may have at most min(cwnd, rwnd) bytes unacknowledged. Throughput is therefore bounded by window ÷ RTT:

```
THE BANDWIDTH-DELAY PRODUCT (BDP)
  To fill a path you need BDP = bandwidth × RTT bytes in flight.
    100 Mbit/s × 50 ms  = 625 KB
    1 Gbit/s × 100 ms   = 12.5 MB
  With a 64 KB window (no window scaling) at 100 ms RTT:
    64 KB ÷ 0.1 s ≈ 5.2 Mbit/s, however fast the link.
  In the lab, CUBIC on a 20 Mbit/s bottleneck ran with cwnd 237 segments at RTT 136 ms:
    237 × 1448 B × 8 ÷ 0.136 s ≈ 20.2 Mbit/s — the link rate, as measured (19.1 Mbit/s goodput).
```

### Slow start and its exit

A new connection starts with cwnd = 10 segments (about 14 KB, RFC 6928) and adds one segment per segment acknowledged — roughly doubling every RTT — until it sees loss, reaches `ssthresh`, or (CUBIC's HyStart) notices the RTT rising and leaves slow start early. That is why the lab's loss-free transfers report a finite `ssthresh` with zero retransmissions: HyStart set it.

On a 100 ms path, reaching a 1 MB window takes about 7 RTTs — 0.7 s — so a short transfer on a new connection never reaches full speed. Reusing connections (HTTP keep-alive, pools, HTTP/2 multiplexing) avoids both the handshake and slow start. After an idle period Linux also shrinks cwnd again (`net.ipv4.tcp_slow_start_after_idle = 1`).

### Loss-based: Reno and CUBIC

```
LOSS-BASED CONGESTION CONTROL (Reno illustration)

  cwnd │            loss ×                      loss ×
       │             ╱│                          ╱│
       │           ╱  │ halve                  ╱  │
       │         ╱    └──────╱─────────────╱      └──── …
       │       ╱  slow       congestion avoidance:
       │     ╱    start      +1 segment per RTT
       │  ╱       (doubling per RTT)
       └──────────────────────────────────────────── time

  Fast-retransmit loss:  ssthresh = cwnd × β; cwnd = ssthresh   (Reno β = 0.5, CUBIC β = 0.7)
  RTO timeout:           ssthresh = cwnd × β; cwnd = 1 segment and slow start again
```

- **Reno** grows by one segment per RTT and halves on loss.
- **CUBIC** (RFC 9438; Linux's default since 2.6.19, and still the default on Ubuntu) grows along a cubic curve in time — fast while far below the window where the last loss happened, cautiously near it — and backs off to 70%. It is far better than Reno on long fat paths.

Both treat **loss as the signal of congestion**. Two consequences follow. First, random (non-congestion) loss on a long path costs a lot: the Mathis approximation bounds a loss-based flow at about (MSS ÷ RTT) × 1.22 ÷ √p. At 1% loss with 1448-byte segments that is ~2.8 Mbit/s at 50 ms RTT, but ~2.8 Gbit/s at the lab's 0.05 ms RTT — which is why the lab's 1% loss barely slowed anything, and why the same loss rate on an intercontinental path is a disaster. (An `iptables -m statistic` drop cannot add delay; `tc netem` can — see Lab Part 3.) Second, a loss-based sender keeps growing until a buffer overflows, so on a bottleneck with a deep buffer it keeps that buffer full: **bufferbloat**.

### Model-based: BBR

BBR (Google, 2016; in Linux since 4.9 as `tcp_bbr`) does not wait for loss. It measures the delivery rate and the minimum RTT and paces packets at the estimated bottleneck bandwidth, keeping roughly one BDP in flight. It tolerates random loss far better than CUBIC and keeps queues short. Mainline Linux ships the original BBR (v1); later versions are developed out of tree. BBR v1 can be unfair to CUBIC flows sharing a bottleneck, which is one reason distributions do not make it the default.

The lab measured the difference on a 20 Mbit/s bottleneck with 200 ms of buffer (`tc … tbf rate 20mbit … latency 200ms`), sending 6 MiB while pinging the server:

| Algorithm | Goodput | TCP's RTT (tcp_info, end) | cwnd | Ping during the transfer (avg / max), 3 runs |
| --- | --- | --- | --- | --- |
| CUBIC | 19.1 Mbit/s | 136 ms | 237 segments | 65.8 / 122, 65.6 / 122, 66.4 / 121 ms |
| BBR | 19.1 Mbit/s | 5.9–6.0 ms | 12 segments | 6.7 / 21, 8.1 / 32, 6.6 / 21 ms |

The empty path's RTT is 0.05 ms. Both algorithms filled the link; CUBIC did it by queueing 340 KB in the router, adding 136 ms of delay **for every other flow through that bottleneck** — the video call next to the backup. Bufferbloat is why "the internet is slow whenever someone uploads" in homes and branch offices; the fixes are smart queue management at the bottleneck (fq_codel, CAKE) and delay-aware senders.

```
LINUX CONGESTION CONTROL
  sysctl net.ipv4.tcp_congestion_control             # default for new sockets
  sysctl net.ipv4.tcp_available_congestion_control   # loaded algorithms
  sysctl net.ipv4.tcp_allowed_congestion_control     # what unprivileged programs, and
                                                     #   non-initial network namespaces,
                                                     #   may select
  modprobe tcp_bbr; sysctl -w net.ipv4.tcp_congestion_control=bbr
  ip route change default via … congctl bbr          # per route
  setsockopt(fd, IPPROTO_TCP, TCP_CONGESTION, "bbr") # per socket (tcplab send -cc bbr)
  ss -ti                                             # each socket's algorithm, cwnd, rtt, …
```

ECN (Explicit Congestion Notification, RFC 3168) lets routers mark packets instead of dropping them. Linux's default `net.ipv4.tcp_ecn = 2` accepts ECN when the peer asks but does not request it on outgoing connections.

---

## 11.8 TCP States

```
TCP STATE MACHINE (the paths you will see)

                          ┌──────────── CLOSED ────────────┐
                 listen() │                                │ connect(): send SYN
                          ▼                                ▼
                       LISTEN                          SYN_SENT
            recv SYN,     │                                │ recv SYN-ACK,
            send SYN-ACK  ▼                                │ send ACK
                       SYN_RECV ── recv ACK ──▶ ESTABLISHED ◀┘
                                                    │
                       active close: close()        │       passive close: recv FIN
                       send FIN   ┌─────────────────┴─────────────────┐ send ACK
                                  ▼                                   ▼
                             FIN_WAIT_1                           CLOSE_WAIT
               recv ACK of FIN │     │ recv FIN (simultaneous)         │ close(): send FIN
                               ▼     ▼                                 ▼
                         FIN_WAIT_2  CLOSING                       LAST_ACK
                     recv FIN │        │ recv ACK                      │ recv ACK
                              ▼        ▼                               ▼
                             TIME_WAIT (60 s on Linux) ──▶ CLOSED   CLOSED
```

`ss` prints these with hyphens (`SYN-SENT`, `CLOSE-WAIT`, `TIME-WAIT`), `tcp_info` and the RFC with underscores.

### States that point at problems

| State | Normal? | Danger sign | Usual meaning |
| --- | --- | --- | --- |
| ESTABLISHED | Yes | Count climbing without bound | Connection leak: the application opens and never closes, or a pool without a cap |
| SYN_SENT | Transient | Many, lingering | Outbound connects not answered: firewall DROP, host down, peer's accept queue full |
| SYN_RECV | Transient | Many | SYN flood, or the clients' final ACKs are not arriving (asymmetric path); with `tcp_syncookies = 1` (default) the server survives floods |
| CLOSE_WAIT | Briefly | Persistent or growing | The application received EOF and has not called `close()`; with **no process** in `ss -p`, connections still sitting in the accept queue of a listener that is not accepting (the lab shows both) |
| FIN_WAIT_2 | Briefly | Many | The peer has not closed its side (it is in CLOSE_WAIT); orphans are reaped after `tcp_fin_timeout` |
| LAST_ACK | Briefly | Many | The final ACK is not arriving — often the peer already vanished |
| TIME_WAIT | Yes | Connect errors `EADDRNOTAVAIL` to one destination | The active closer; cheap unless one client churns connections to one destination (11.4) |

### Reading socket state

```bash
ss -tan                                 # all TCP sockets, numeric, with state
ss -tan state close-wait                # one state (names: established, syn-sent, syn-recv,
                                        #   fin-wait-1, fin-wait-2, time-wait, close-wait,
                                        #   last-ack, closing, listening, all, connected …)
ss -tanp '( dport = :5432 )'            # filter by port; -p shows the owning process
ss -tano state time-wait                # -o shows timers: timewait, persist, keepalive, on (RTO)
ss -tni '( dst 10.0.10.20 )'            # -i: rtt, cwnd, retrans, rwnd_limited, bytes_acked …
ss -ltn                                 # listeners: Recv-Q = queued connections, Send-Q = backlog
ss -tan | awk 'NR>1 {print $1}' | sort | uniq -c     # count by state
nstat -az | grep -E 'TcpRetransSegs|TCPTimeouts|TCPFastRetrans|ListenOverflows|TCPAbortOn'
```

Two traps in `ss -s`. Its `estab` figure comes from the kernel's `CurrEstab` counter, which counts ESTABLISHED **plus CLOSE_WAIT** sockets. And its total, `closed` and `orphaned` figures come from counters shared by the whole host — inside a container (a network namespace) they include every other container's sockets; the lab's namespaces all reported the host's `TCP: 22` while holding a handful each. Count states with `ss -tan` when it matters.

---

## 11.9 TCP Options in Practice

### MSS — maximum segment size

```
MSS
  Announced in each side's SYN: the largest TCP payload THIS side wants to receive.
  Derived from the interface MTU: 1500 − 20 (IPv4) − 20 (TCP) = 1460 (IPv6: 1440).
  Each side sends segments no larger than the PEER's MSS (and its path MTU, Module 7).

  Timestamps use 12 bytes of option space in every segment, so the payload of a
  full segment is 1460 − 12 = 1448: the lab's "length 1448", and tcp_info's "mss 1448".

  Tunnels shrink the MTU (WireGuard 1420, IPsec ~1400, VXLAN 1450 …), so the MSS
  must shrink too. If PMTU discovery is broken (ICMP blocked, Module 7), clamp it:

  iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
  iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1360
```

The rule matches every packet with SYN set — SYNs and SYN-ACKs — so both directions are clamped. In the lab, the router rewrote the MSS to 1200: the client's capture shows the SYN-ACK arriving with `mss 1200` (the server never sent that value), and the client then sending `length 1188` segments (1200 − 12). MSS clamping is a property of the path, invisible to both endpoints except in a capture taken on each side.

### Why tcpdump shows segments bigger than the MTU

The lab's first capture, on the sending host with default settings, shows one 3000-byte segment — `seq 1:3001 … length 3000` — on a 1500-byte MTU link. That is **TSO/GSO** (TCP / generic segmentation offload): the kernel hands one large "super-packet" down the stack and the NIC (or the last moment before the wire) cuts it into MSS-sized segments. A capture on the sender sees the super-packet; the wire never carries it. On the receiving side, **GRO/LRO** merges segments the same way. `ethtool -k eth0 | grep offload` shows the settings; the lab turns them off (`ethtool -K eth0 tso off gso off`) to see the real segments — and so its packet-dropping rule drops single segments rather than whole super-packets. Checksums appear "incorrect" in sender-side captures for the same reason: checksum offload fills them in later.

### SACK — selective acknowledgment

Negotiated by `sackOK` in both SYNs (RFC 2018, on by default: `net.ipv4.tcp_sack = 1`). The receiver lists up to three or four blocks it holds beyond the cumulative ACK — `ack 330577 … sack 1 {332025:366777}` — so the sender retransmits only the hole instead of everything after it. D-SACK (RFC 2883) reports a segment received twice, which tells the sender a retransmission was unnecessary (the lab's zero-window capture has one: `dsack_dups:1` in `ss -i`). Without SACK, multiple losses in one window take one RTT each to discover.

### Window scaling

```
WINDOW SCALING (RFC 7323)
  The window field is 16 bits: at most 65,535 bytes — 5.2 Mbit/s at 100 ms RTT.
  The wscale option (a shift count, 0–14) in each SYN multiplies that side's later
  windows by 2^wscale; 14 allows windows up to ~1 GiB.
    lab: "wscale 10" → "win 63" means 63 × 1024 = 64,512 bytes
    Ubuntu 22.04/24.04 defaults usually show wscale 7 (× 128)
  Both SYNs must carry the option, or neither side scales.
  The windows in the SYN and SYN-ACK themselves are never scaled.
  Each side picks its own shift from its maximum receive buffer — the lab's tiny-buffer
  server offered wscale 0.
```

Window scaling problems are rarely about the endpoints today. They come from middleboxes that strip the option from the SYN but not the SYN-ACK (or rewrite one), so one side scales and the other does not: windows look absurdly small or large and throughput collapses. A capture of the handshake on both sides settles it.

### Timestamps

The timestamp option (10 bytes, padded to 12) carries TSval (the sender's clock) and TSecr (the last TSval received from the peer). It gives an RTT sample from every ACK, protects against wrapped sequence numbers on very fast links (PAWS), and is what makes `tcp_tw_reuse` safe. Linux randomises the timestamp offset per connection, so TSval does not reveal uptime. Some middleboxes drop SYNs with unfamiliar options or broken timestamps; `net.ipv4.tcp_timestamps = 0` is a diagnostic step, not a fix.

### Keepalive

An idle connection exchanges no packets at all, so neither side notices a peer that crashed or a NAT/firewall/load balancer that silently dropped its state. TCP keepalive (off unless the application sets `SO_KEEPALIVE`) sends probes after `net.ipv4.tcp_keepalive_time` = 7200 s of idleness, every `tcp_keepalive_intvl` = 75 s, giving up after `tcp_keepalive_probes` = 9. Two hours is longer than nearly every middlebox idle timeout, so long-lived idle connections (database pools, message brokers) need application keepalives or per-socket settings (`TCP_KEEPIDLE` …) below the shortest timeout on the path. Some runtimes enable keepalive themselves — Go's `net` package sets 15 s by default, which `tcplab` deliberately turns off.

---

## 11.10 How TCP Events Surface as Application Errors

Tickets rarely arrive with a packet capture; they arrive with an error string from some runtime. Every string below was produced against the lab's targets (refused: port 9 on the server; no route: the router's `icmp-host-prohibited` REJECT; timeouts: the router's DROP rule, with `tcp_syn_retries = 2` for the kernel's own timeout and a 2 s deadline for the application's; reset: the `reset` server, while writing; clean close: the `close` server, which reads the request and closes without answering):

| Wire event | errno | Go | Python | Node.js | Java | curl exit |
| --- | --- | --- | --- | --- | --- | --- |
| SYN → RST, or ICMP port unreachable | `ECONNREFUSED` | `dial tcp 198.18.43.10:9: connect: connection refused` | `ConnectionRefusedError: [Errno 111] Connection refused` | `ECONNREFUSED` | `java.net.ConnectException: Connection refused` | 7 |
| SYN → ICMP host unreachable / prohibited | `EHOSTUNREACH` | `connect: no route to host` | `OSError: [Errno 113] No route to host` | `EHOSTUNREACH` | `java.net.NoRouteToHostException: No route to host` | 7 |
| SYN unanswered until the kernel gives up | `ETIMEDOUT` | `connect: connection timed out` | `TimeoutError: [Errno 110] Connection timed out` | `ETIMEDOUT` | `java.net.ConnectException: Connection timed out` | 28 when its own timeout fires first (it usually does) |
| SYN unanswered, the application's deadline first | none | `dial tcp …: i/o timeout` | `TimeoutError: timed out` | the library's own timeout (no errno) | `java.net.SocketTimeoutException: Connect timed out` | 28 |
| RST on an established connection | `ECONNRESET` | `write: connection reset by peer` (or `read: …`) | `ConnectionResetError: [Errno 104] Connection reset by peer` | `ECONNRESET` | `java.net.SocketException: Connection reset by peer` (or `Connection reset`) | 56 (`Recv failure: Connection reset by peer`) |
| Writing again after the reset | `EPIPE` | `write: broken pipe` | `BrokenPipeError: [Errno 32] Broken pipe` | `EPIPE` | `SocketException: Broken pipe` | — |
| FIN before any answer (clean close, read returns 0) | none | `EOF` (from `http.Get`) | `RemoteDisconnected: Remote end closed connection without response` (`http.client`); `b''` from `recv` | `ECONNRESET`, "socket hang up" (`http`) | `java.net.SocketException: Unexpected end of file from server` | 52 (`Empty reply from server`) |

Three things to take from the table. **The application deadline usually fires before the kernel's**, so "timeout" in a log rarely means TCP gave up — it means the program's own budget (often 1–30 s) ran out while SYNs or data were being retransmitted. **"No route to host" is about ICMP, not the routing table**, when a route exists: find which device sent the ICMP. And **names lie across runtimes**: Node reports a perfectly clean FIN-before-response as `ECONNRESET` "socket hang up", so confirm with a capture whether an RST was ever sent — a server closing an idle keep-alive connection just as the client reuses it produces exactly this error, with no reset anywhere. The capture for each row is in the lab's evidence directory (`refused.pcap`, `reject-prohibited.pcap`, `syn-drop.pcap`, `reset.pcap`; `close-no-answer.txt` for the clean close), which makes a reference set to compare tickets against.

---

## Hands-On Lab: TCP Under the Microscope

### Lab Goals

- Capture a whole connection; read options, relative sequence numbers, PSH and the FIN exchange
- See TSO produce "impossible" 3000-byte segments, and MSS clamping rewrite the handshake
- Produce refused, forged-refused, unreachable and timed-out connects; time the SYN retransmissions
- Fill an accept queue and find the evidence on the server
- Create CLOSE_WAIT, an orphaned FIN_WAIT_2, an RST abort and a zero window, and read each in `ss`
- Measure fast retransmit under loss, bufferbloat with CUBIC vs BBR, and an outage stretched by RTO backoff

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iproute2` (with `tc`), `tcpdump`, `iptables`, `ethtool`, `iputils-ping`
- The `tcp_bbr` and `sch_tbf` kernel modules (both in Ubuntu's standard kernel packages)
- Part 3 uses your own machine and the internet

The lab uses [`tcplab`](../labs/go/cmd/tcplab), a Go tool written for it. `tcplab serve` listens with a chosen backlog and receive buffer and can misbehave on purpose: `discard` (read, then close), `echo`, `hold` (never close — CLOSE_WAIT), `noread` (zero window), `noaccept` (fill the accept queue), `reset` (abort with RST), `close` (close normally without answering). `tcplab dial` reports how each `connect()` ended, the errno, and how long it took. `tcplab send` transfers data, half-closes, waits for the server's FIN as proof of delivery, and prints the kernel's `tcp_info` for the connection: RTT, RTO, MSS, cwnd, ssthresh, retransmissions, probes and the negotiated options.

---

### Lab Part 1: Run the TCP Lab

[`tcp.json`](../labs/netlab/tcp.json):

```
  client 198.18.42.10 ──── router ──── server 198.18.43.10

  client: tcp_syn_retries = 2
  router: DROP :8085 · REJECT tcp-reset :8086 · REJECT (ICMP port unreachable) :8087
          REJECT icmp-host-prohibited :8088
  server: tcplab serve  :8080 discard  :8081 hold  :8082 noread (4 KB buffer)
                        :8083 noaccept (backlog 2)  :8084 reset  :8089 close
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/tcp.json        # about 40 seconds
```

Excerpts from real runs (Linux 6.18; on Ubuntu 24.04 the default congestion control is `cubic` where these transcripts show `bbr`, and wscale is usually 7):

**A whole connection, then the same one with offloads off.**

```
check 1/35: a whole connection: SYN, SYN-ACK, ACK, data, FIN from each side; options in the SYN
  | Flags [S], seq 2999890995, win 64240, options [mss 1460,sackOK,TS val 3634304095 ecr 0,nop,wscale 10], length 0
  | Flags [S.], seq 3450125985, ack 2999890996, win 65160, options [mss 1460,sackOK,TS val 2904196904 ecr 3634304095,nop,wscale 10], length 0
  | Flags [.], ack 1, win 63, …
  | Flags [P.], seq 1:3001, ack 1, win 63, …, length 3000
  | Flags [.], ack 3001, win 70, …
  | Flags [F.], seq 3001, ack 1, …
  | Flags [F.], seq 1, ack 3002, …
  | Flags [.], ack 2, …
check 2/35: the active closer (the client sent the first FIN) keeps TIME_WAIT for 60 s
  | 0      0       198.18.42.10:48628 198.18.43.10:8080 timer:(timewait,59sec,0)
check 3/35: the passive closer went through LAST_ACK to CLOSED: no TIME_WAIT here
check 4/35: 'length 3000' was bigger than the MTU: that was a TSO/GSO super-packet …
  | tcp-segmentation-offload: on
  | generic-segmentation-offload: on
check 6/35: now 3000 bytes leave as 1448 + 1448 + 104 …
check 8/35: the SYN-ACK arrives with mss 1200, so the client sends 1188-byte segments
  | Flags [S.], seq 1783213563, ack 1072870974, win 65160, options [mss 1200,sackOK,…
  | Flags [.], seq 1:1189, ack 1, … length 1188
```

Check the arithmetic in each line: the SYN-ACK's `ack` is the client's ISN + 1; the client's FIN is at `seq 3001` (after 3000 bytes) and the server acknowledges `3002` because a FIN, like a SYN, consumes one sequence number.

**Four ways to fail a connect.**

```
check 10/35: nothing listens on port 9 …
  | connect 1: refused     after   0.25ms  ECONNREFUSED: …
  | IP (tos 0x0, ttl 63, …) 198.18.43.10.9 > 198.18.42.10.52798: Flags [R.], …
check 11/35: a firewall REJECT --reject-with tcp-reset looks the same to the application; the TTL gives the forger away
  | IP (tos 0x0, ttl 64, …) 198.18.43.10.8086 > 198.18.42.10.46468: Flags [R.], …
check 12/35: plain REJECT sends ICMP port unreachable, which TCP also reports as ECONNREFUSED
  | 198.18.42.1 > 198.18.42.10: ICMP 198.18.43.10 tcp port 8087 unreachable, length 68
check 13/35: REJECT with icmp-host-prohibited: EHOSTUNREACH, 'No route to host', though a route exists
  | connect 1: unreachable after   0.27ms  EHOSTUNREACH: …
  | 198.18.42.1 > 198.18.42.10: ICMP host 198.18.43.10 unreachable - admin prohibited, length 68
check 14/35: a DROP rule: SYNs go unanswered …
  | connect 1: timed out   after    7.13s  ETIMEDOUT: …
check 15/35: when each SYN left …
  | SYN 1 at  0.00 s
  | SYN 2 at  1.02 s
  | SYN 3 at  2.04 s
  | SYN 4 at  3.07 s
  | SYN 5 at  4.10 s
  | SYN 6 at  5.12 s
  | net.ipv4.tcp_syn_retries = 2
  | net.ipv4.tcp_syn_linear_timeouts = 4
```

The same 7.1 s and the same six SYNs in all three runs. Note that the ICMP messages come from the router (198.18.42.1), not the server: whoever sent an ICMP error is the device to talk to. "No route to host" with a perfectly good route is almost always a firewall REJECT.

**Queues and stuck states.** Checks 16–25 produce the accept-queue overflow, CLOSE_WAIT, the orphaned FIN_WAIT_2, the RST abort, the clean close that curl calls an empty reply, and the zero window shown in 11.3–11.6 and 11.10. Each check saves its `ss` output in the evidence directory; compare the client's and server's view of the same connection.

**Loss, bufferbloat and an outage.** Checks 26–35 produce the 1% loss table in 11.5, the CUBIC vs BBR table in 11.7, and the outage silences in 11.5. The baseline transfer (check 26) can itself show some retransmissions with no configured loss: at several Gbit/s a forwarding queue in the virtual path overflows — congestion loss, exactly what congestion control exists to respond to.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/tcp.json
alias lab='sudo ./bin/netlab exec ../netlab/tcp.json'
```

1. **Watch TIME_WAIT expire, and an abort leave none.** `lab client -- ./bin/tcplab send -to 198.18.43.10:8080 -bytes 1K`, then run `lab client -- ss -tano state time-wait` a few times: the timer counts down from 60 s and the socket disappears. Now send to the `reset` server instead (`-to 198.18.43.10:8084 -bytes 1M`): the connection ends in `ECONNRESET`, and `ss` shows no TIME_WAIT for it on either side — a reset discards all state at once.
2. **Change the SYN schedule.** `lab client -- sysctl -w net.ipv4.tcp_syn_retries=1`, then `lab client -- ./bin/tcplab dial -to 198.18.43.10:8085`: it gives up after about 3 s (SYNs at 0, 1, 2 s). Add `net.ipv4.tcp_syn_linear_timeouts=0` and retry with `tcp_syn_retries=3`: four SYNs at 0, 1, 3, 7 s, about 15 s in total.
3. **Find the stuck reader from the sender.** `lab client -- ./bin/tcplab send -to 198.18.43.10:8082 -bytes 1M -timeout 30s &`, then `lab client -- ss -tnoi '( dport = :8082 )'` while it runs: `rwnd_limited` near 100% and a `persist` timer identify a receiver that is not reading, from the sending side alone.
4. **The accept queue with an application timeout.** On a freshly started lab, `lab client -- ./bin/tcplab dial -to 198.18.43.10:8083 -n 5 -timeout 3s`: three connect, and the two whose SYNs are dropped report `gave up` after 3 s rather than `timed out` — the client's own deadline expired before the kernel's. That is what most real clients (HTTP libraries, database drivers) report for a full accept queue: a connect timeout, with nothing logged on the server except the overflow counter.
5. **Congestion control per socket.** `lab router -- tc qdisc add dev eth1 root tbf rate 20mbit burst 32kbit latency 200ms`, then compare `-cc cubic`, `-cc reno` and `-cc bbr` for a 6M transfer while `lab client -- ping -c 20 -i 0.15 198.18.43.10` runs. Reno and CUBIC both fill the buffer — in the course's run Reno pushed ping to 203 ms, the whole 200 ms of buffer — while BBR kept it near 5 ms.

`sudo ./bin/netlab down ../netlab/tcp.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
ss -tan | awk 'NR>1 {print $1}' | sort | uniq -c        # your machine's state distribution
ss -tni state established | head -20                     # rtt, cwnd, retrans per connection
sysctl net.ipv4.tcp_congestion_control net.ipv4.tcp_syn_retries net.ipv4.tcp_syn_linear_timeouts
nstat -az | grep -E 'TcpRetransSegs|TCPTimeouts|TCPFastRetrans|ListenOverflows'

# A real handshake to a real server (plain HTTP so the payload is readable):
sudo tcpdump -i any -nn -c 12 'tcp port 80 and host example.com' &
curl -so /dev/null http://example.com/
```

Compare the SYN-ACK's options with the lab's: many large sites answer with a different MSS (often 1460 or less, sometimes clamped), a different window scale, or no timestamps. The RTT between your SYN and their SYN-ACK is the network round trip to the nearest edge server; the gap between your request and the first byte of the response adds the server's processing time.

**Delay and loss together (needs `sch_netem`).** The lab cannot add delay — its kernel has no `netem` — so the Mathis effect in 11.7 is not measured there. On a machine whose kernel has it (Ubuntu's standard kernel does; cloud kernels may need `linux-modules-extra-$(uname -r)`), in the lab's router: `tc qdisc add dev eth1 root netem delay 25ms loss 1%`, which gives a 50 ms RTT with 1% loss toward the server. Then compare `tcplab send -bytes 20M -cc cubic` with `-cc bbr`. Expect CUBIC to fall to a few Mbit/s, as the formula predicts, and BBR to stay far higher. This exercise was not run on the course's lab kernel.

---

## Real-World Troubleshooting Scenario

### Scenario: "The API Is Leaking Connections — File Descriptors Exhausted"

**The situation.** A Node.js API in a Kubernetes pod starts returning `500` errors with `EMFILE: too many open files`. Its file descriptor count climbs steadily for hours until it reaches the process limit of 65,536; a restart fixes it for a while. The developers say: "We use a connection pool. We close everything."

**Step 1 — Count states inside the pod.**

```bash
kubectl exec -it <pod> -- ss -tan | awk 'NR>1 {print $1}' | sort | uniq -c
```

```
     48 ESTAB
  62847 CLOSE-WAIT
    200 TIME-WAIT
      1 LISTEN
```

(`ss -s` would have shown roughly `estab 62895` — ESTABLISHED and CLOSE_WAIT together — and a meaningless host-wide `closed` figure. Count states with `ss -tan`.)

**Step 2 — What CLOSE_WAIT means.** The remote end sent FIN and the local kernel acknowledged it; the local application has been told (a read returned EOF, or an `end`/`close` event fired) and has not closed its socket. CLOSE_WAIT has no timeout, and each one holds a file descriptor. Tens of thousands that only grow are a lifecycle bug in this process — not the network, not Kubernetes, not the database server.

**Step 3 — Who, and to where.**

```bash
kubectl exec -it <pod> -- ss -tanp state close-wait | head -5
```

```
Recv-Q Send-Q Local Address:Port   Peer Address:Port  Process
0      0      10.0.5.40:34521      10.0.10.20:5432    users:(("node",pid=1,fd=4123))
0      0      10.0.5.40:34522      10.0.10.20:5432    users:(("node",pid=1,fd=4124))
0      0      10.0.5.40:34523      10.0.10.20:5432    users:(("node",pid=1,fd=4125))
```

All of them are connections to PostgreSQL on 10.0.10.20:5432, owned by the API process. Something on the database side is closing connections — and the pool is not disposing of them when it happens.

**Step 4 — Root cause.** Which closure, and why, needs evidence from the database side: its logs show the disconnect reason (an `idle_session_timeout` or `idle_in_transaction_session_timeout` being reached, an administrator terminating backends, a failover, a proxy such as PgBouncer recycling server connections). Here, the database had `idle_session_timeout` set to 10 minutes; the pool kept idle connections longer than that, and the pool's error handling never destroyed a connection whose socket emitted `end` while idle. A capture (`tcpdump 'host 10.0.10.20 and tcp[tcpflags] & tcp-fin != 0'`) shows the FIN arriving from the database with no FIN ever sent back.

**The fix.** Destroy connections on `end`/`error` events (or upgrade the pool library that missed them); set the pool's idle timeout below the server's; alert on CLOSE_WAIT count per process. The TCP knowledge that made it fast: CLOSE_WAIT is the side that *received* the FIN, so the bug is in the program holding those sockets — the `-p` column says which.

---

## Common Failure Patterns

| Symptom | TCP-level cause | How to confirm | Fix |
| --- | --- | --- | --- |
| "Connection refused", instantly | RST from the host (nothing listening) or a firewall REJECT | Capture shows `[R.]` answering `[S]`; compare the RST's TTL; or an ICMP port unreachable | Start the service / correct the port; or fix the firewall rule |
| "Connection timed out" after 2 min (or the client's own timeout) | SYNs dropped (DROP rule, security group, host down behind a router) | SYN retransmissions in a capture, no answer; `ss` shows SYN-SENT | Find the dropping hop; allow the port |
| "No route to host" though routes are fine | Firewall REJECT with icmp-host-prohibited, or ARP failure on the local subnet | ICMP "admin prohibited" in a capture and the router that sent it | Fix the firewall rule; check the target host is up |
| Connects take exactly 1 s, 3 s or 7 s sometimes | First SYN (or SYN-ACK) lost — often a full accept queue | Server: `ss -ltn` Recv-Q ≥ Send-Q, `nstat` ListenOverflows rising | Make the application accept faster; then raise backlog/somaxconn |
| `EMFILE`, file descriptors climbing | CLOSE_WAIT accumulating | `ss -tanp state close-wait` names process and peer | Close sockets on EOF/error in the application |
| `EADDRNOTAVAIL` on connect | Ephemeral ports to one destination exhausted by TIME_WAIT | `ss -tan state time-wait '( dst IP )' \| wc -l` near the port range size | Reuse connections; more destination IPs/ports; `tcp_tw_reuse=1` for outgoing |
| "Connection reset by peer" mid-request | Peer aborted, a process closed with unread data, or a middlebox idle timeout reset the connection | RST in a capture; its timing after idle periods; which side sent it (TTL) | Keepalives below the middlebox idle timeout; fix the aborting peer |
| Long-lived idle connections die silently; first request after idle hangs | NAT/firewall/LB dropped state without telling anyone | First packet after idle retransmits with no answer, then RST or timeout | TCP or application keepalives shorter than the idle timeout |
| Transfers stall; sender's write blocks | Zero window: the receiving application is not reading | `win 0` in a capture; receiver Recv-Q high; sender `rwnd_limited` | Fix the slow consumer (CPU, disk, blocking code) |
| Large transfers slow on a long path, fine nearby | Window too small for the BDP (SO_RCVBUF fixed, no window scaling, buffer maxima) | `ss -ti` rwnd_limited or small cwnd; wscale missing in the SYN | Leave autotuning on; raise tcp_rmem/wmem maxima; fix the middlebox stripping wscale |
| Throughput collapses with a little loss on a long path | Loss-based congestion control (Mathis) | `ss -ti` retrans; `nstat` TCPFastRetrans/TCPTimeouts | Fix the loss; consider BBR for the sender |
| Everything slow while one large upload runs | Bufferbloat at the bottleneck | Ping RTT rises from tens to hundreds of ms under load | Smart queueing (fq_codel/CAKE) at the bottleneck; shape below the link rate |
| Connection hangs once data is large (handshake fine) | PMTUD black hole (Module 7) | Small requests work; full-size segments retransmitted without ACK | Allow ICMP "fragmentation needed"; clamp MSS |
| "Network recovered" but the application stalls for seconds more | RTO backoff after an outage | Capture: retransmissions at doubling intervals | Expected; application-level timeouts and reconnects for long outages |

---

## Summary

- TCP delivers an ordered byte stream or reports an error. An ACK means the remote TCP has the bytes — not that the application processed them; application retries need idempotency.
- tcpdump shows flags (`S`, `.`, `P`, `F`, `R`), relative sequence numbers after the handshake (`-S` for absolute), windows that must be multiplied by the negotiated scale, and the options of each SYN.
- How a connect fails tells you what happened: RST or ICMP port unreachable → refused, at once; ICMP prohibited → no route to host; silence → timed out after the SYN retransmission schedule (1 s steps then doubling on Linux 6.5+, about 131 s by default).
- A full accept queue drops SYNs silently: connects take 1 s extra or time out; the server shows Recv-Q ≥ Send-Q on the listener and ListenOverflows.
- The active closer holds TIME_WAIT for a fixed 60 s; the passive closer passes through CLOSE_WAIT and LAST_ACK. Persistent CLOSE_WAIT is an application not closing; TIME_WAIT is a problem only for one client churning connections to one destination.
- Loss is repaired by fast retransmit (SACK, RACK) within about an RTT, or by the retransmission timeout — at least 200 ms and doubling, which stretches a 3 s outage into ~5 s of silence.
- Flow control: a zero window means the receiving application is not reading. Congestion control: throughput ≈ cwnd ÷ RTT; loss-based CUBIC fills buffers (bufferbloat) and suffers from random loss on long paths; BBR paces at the measured bottleneck rate.
- The MSS follows the MTU and can be clamped in transit; TSO/GSO makes sender-side captures show segments larger than the MTU.
- Connection reuse avoids the handshake, slow start and TIME_WAIT churn at once.

## What's Next

Module 12 covers DNS from the application's stub resolver to the authoritative servers: records, caching and TTLs, and DNS over both UDP and TCP. Test DNS when the failing operation depends on resolving a name — and use this module's connect-failure table to tell whether the failure happened before or after the name was resolved.

---

## Quick Reference Card

```
MODULE 11: TCP — QUICK REFERENCE
════════════════════════════════

HEADER (20–60 B): ports, seq, ack, data offset, flags CWR ECE URG ACK PSH RST SYN FIN,
                  window (<< wscale), checksum, urgent ptr, options

TCPDUMP FLAGS     [S] SYN   [S.] SYN-ACK   [.] ACK   [P.] data, push   [F.] FIN
                  [R] / [R.] reset   E = ECE, W = CWR
                  seq/ack relative after the handshake; -S for absolute

HANDSHAKE         SYN → SYN-ACK → ACK; options (mss, sackOK, TS, wscale) in the SYNs only
TEARDOWN          FIN → ACK → FIN → ACK (often FIN+ACK combined: 3 packets)
                  active closer → TIME_WAIT 60 s; passive → CLOSE_WAIT → LAST_ACK

CONNECT FAILURES
  refused (ECONNREFUSED)       RST or ICMP port unreachable — immediate
  no route (EHOSTUNREACH)      ICMP host unreachable/prohibited — or ARP failure (~3 s)
  timed out (ETIMEDOUT)        silence; SYNs at 0,1,2,3,4,5,7,11,19,35,67 s, give up ~131 s
                               (Linux ≥ 6.5; before: 0,1,3,7,15,31,63, give up 127 s)

TIMERS            RTO ≥ 200 ms, doubles per timeout, max 120 s; initial (SYN) 1 s
                  tcp_retries2 = 15 → ~15 min for an established connection
                  TIME_WAIT 60 s fixed; orphaned FIN_WAIT_2: tcp_fin_timeout 60 s
                  keepalive (if enabled): 7200 s idle, 75 s × 9 probes
                  zero window: persist probes, backing off, never time out

SS
  ss -tan [state X]            states          ss -tanp      + process
  ss -tano                     + timers        ss -tni       + rtt, cwnd, retrans, rwnd_limited
  ss -ltn                      listeners: Recv-Q = queued conns, Send-Q = backlog
  ss -s                        estab includes CLOSE_WAIT; totals are host-wide in containers
NSTAT             TcpRetransSegs TcpExtTCPTimeouts TcpExtTCPFastRetrans
                  TcpExtListenOverflows TcpExtListenDrops

OPTIONS
  MSS 1460 (IPv4/1500) → 1448 payload with timestamps; clamp: -j TCPMSS --clamp-mss-to-pmtu
  SACK      sack 1 {start:end} = blocks held beyond the hole
  wscale    window × 2^n, n ≤ 14; SYN windows unscaled
  TS        RTT samples, PAWS, makes tcp_tw_reuse safe
  TSO/GSO   sender-side captures show segments > MTU (ethtool -K eth0 tso off gso off)

CONGESTION
  throughput ≈ min(cwnd, rwnd) ÷ RTT;  BDP = bandwidth × RTT
  initial cwnd 10 segments; slow start doubles per RTT; HyStart may exit early
  CUBIC (default): loss-based, β 0.7; Reno β 0.5; BBR: rate/RTT model, short queues
  Mathis: loss-based ≲ (MSS/RTT) × 1.22/√p
  sysctl net.ipv4.tcp_congestion_control; per socket: TCP_CONGESTION; ss -ti shows it

SYSCTLS WORTH KNOWING
  net.ipv4.ip_local_port_range = 32768 60999       net.ipv4.tcp_tw_reuse = 2 (loopback)
  net.ipv4.tcp_syn_retries = 6                     net.ipv4.tcp_syn_linear_timeouts = 4
  net.ipv4.tcp_synack_retries = 5                  net.core.somaxconn = 4096
  net.ipv4.tcp_rmem / tcp_wmem (autotuning)        net.ipv4.tcp_fin_timeout = 60
  net.ipv4.tcp_slow_start_after_idle = 1           net.ipv4.tcp_ecn = 2
```

---

## References

- RFC 9293 — Transmission Control Protocol (TCP) — the consolidated specification (2022)
- RFC 793 — Transmission Control Protocol — the original specification (1981)
- RFC 6528 — Defending against Sequence Number Attacks
- RFC 5681 — TCP Congestion Control (slow start, congestion avoidance, fast retransmit)
- RFC 6298 — Computing TCP's Retransmission Timer
- RFC 6928 — Increasing TCP's Initial Window
- RFC 8985 — The RACK-TLP Loss Detection Algorithm for TCP
- RFC 2018 — TCP Selective Acknowledgment Options; RFC 2883 — D-SACK
- RFC 7323 — TCP Extensions for High Performance (window scaling, timestamps)
- RFC 9438 — CUBIC for Fast and Long-Distance Networks
- RFC 3168 — Explicit Congestion Notification; RFC 8311 — relaxing ECN restrictions (NS bit historic)
- RFC 2525 — Known TCP Implementation Problems (RST on close with unread data)
- Cardwell et al., "BBR: Congestion-Based Congestion Control", ACM Queue, 2016
- Mathis, Semke, Mahdavi, Ott, "The Macroscopic Behavior of the TCP Congestion Avoidance Algorithm", 1997
- Linux kernel documentation: `Documentation/networking/ip-sysctl.rst` (all `net.ipv4.tcp_*` settings, including `tcp_syn_linear_timeouts`)
- `man 7 tcp`, `man 8 ss`, `man 8 nstat`, `man 8 tc-tbf`, `man 8 tc-netem`, `man 8 ethtool`
