# Module 14: Network Performance

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 13 — TLS — Transport Layer Security
> **Estimated time:** 4–5 hours

## Overview

"It's slow" is the vaguest ticket there is, and the one where guessing costs the most. A slow request can be slow because of round trips (each handshake, each sequential request), because of a window that cannot cover the path's bandwidth-delay product, because of loss, because a queue somewhere is full, because a packet size does not fit the path, or because the server is busy — and the fixes for each are different and often opposite. Larger buffers fix a window limit and make a bufferbloat problem worse; a faster link does nothing for a round-trip-bound transaction.

This module gives the quantitative tools to tell these apart: the latency components and how they add up in a single request; bandwidth, throughput and goodput, and how to measure each at the right layer; the bandwidth-delay product and the window ÷ RTT ceiling; queueing and bufferbloat; the cost of loss (the Mathis model, and how BBR differs); MTU, MSS, PMTUD, black holes and PLPMTUD; and a measurement-first framework for slow connections.

Everything is measured in the lab on an emulated path — 50 ms of round-trip time, an optional 20 Mbit/s bottleneck with a chosen queue length, and chosen random loss — built with `pathlab`, a small userspace path emulator written for the course. It does what `tc netem` does (and works on kernels without netem), and its source is short enough to read.

## What You Will Build Toward

- Decompose a request's time with `curl -w` into DNS, TCP, TLS, server time and transfer, and know which round trips each costs
- Explain why the first large transfer on a new connection is slower than the second (slow start), and measure it
- Compute a path's bandwidth-delay product and predict window-limited throughput (window ÷ RTT) — then confirm it
- Distinguish bandwidth, throughput and goodput, and measure goodput correctly from a capture
- Recognise bufferbloat, and explain why a shorter queue gives the same throughput with a fraction of the delay
- Predict single-flow TCP throughput under loss with the Mathis model, and explain why BBR behaves differently
- Diagnose MTU problems: find a path MTU, recognise a PMTUD black hole, and know what PLPMTUD and MSS clamping each fix
- Work through a slow-connection report by measurement, layer by layer

---

## 14.1 Latency: Where the Time Goes

### The components of one-way delay

```
DELAY = PROPAGATION + TRANSMISSION + QUEUEING + PROCESSING

  Propagation   distance ÷ signal speed. Light in fibre covers ~200 km per millisecond
                (about 5 µs/km), so Nairobi–London (~6,800 km great circle, longer by
                cable) is at least ~34 ms one way, ~70 ms round trip, before any equipment.
                Fixed by physics and routing; only moving the endpoints helps (CDNs, regions).
  Transmission  packet size ÷ link rate: 1500 bytes take 12 µs at 1 Gbit/s, 120 µs at
                100 Mbit/s, 0.6 ms at 20 Mbit/s. Matters on slow links and adds up per hop.
  Queueing      time waiting behind other packets in a buffer — zero on an idle path,
                hundreds of milliseconds on a full one (14.5). The component that VARIES.
  Processing    lookup, ACLs, NAT, encryption in routers, firewalls and hosts: microseconds
                in hardware; can be milliseconds in overloaded software paths.
```

`ping` measures the **round trip**, so it includes both directions and the target's response time. The lab's path adds 25 ms in each direction; ping reports `rtt min/avg/max/mdev = 50.963/51.310/51.681/0.275 ms` — the 50 ms of emulated propagation, plus a fraction of a millisecond of real forwarding. `mdev` is the variation (jitter). A minimum that stays put while the average and maximum climb is queueing, not distance.

### How a request spends round trips

`curl -w` reports cumulative timestamps. The lab asks a small HTTPS API (the server adds 100 ms of work) across the 50 ms path:

```
dns 0.000483  tcp 0.051860  tls 0.108826  first byte 0.262247  total 0.262303  (2048 bytes)
```

| Phase | From → to | Measured | Round trips |
| --- | --- | --- | --- |
| DNS | 0 → `time_namelookup` | 0.5 ms (a hosts-file lookup) | 0 here; 1+ to a resolver |
| TCP | → `time_connect` | 51 ms | 1 |
| TLS 1.3 | → `time_appconnect` | 57 ms | 1 (+ certificate verification) |
| Request → first byte | → `time_starttransfer` | 153 ms | 1 + 100 ms of server work |
| Transfer | → `time_total` | 0.1 ms | (2 KB fits in the first flight) |

Three runs gave 262, 267 and 262 ms in total. Three of the four phases are pure round trips: on this path, a new HTTPS connection costs ~160 ms before the server even starts its 100 ms of work. At 150 ms RTT (a user on another continent) the same request takes ~550 ms; with TLS 1.2, one more round trip. That is why connection reuse, TLS 1.3, HTTP/2 multiplexing and serving from nearby (CDNs) matter more than bandwidth for interactive traffic.

### The first transfer on a connection is the slowest

The lab downloads the same 4 MiB twice in one curl invocation, so the second download reuses the connection:

```
dns 0.000373  tcp 0.052401  tls 0.110018  first byte 0.161664  total 0.617470  (4194304 bytes)
dns 0.000024  tcp 0.000000  tls 0.000000  first byte 0.051101  total 0.116657  (4194304 bytes)
```

The first transfer phase took 456 ms; the second 66 ms — **7× faster** on the same path (three runs: 0.594–0.617 s vs 0.117–0.118 s in total). The second skipped the handshakes, and its congestion window was already open: the first spent most of its time in slow start, doubling from 10 segments per round trip. At 50 ms RTT, reaching a window that covers 4 MiB takes about 9 round trips. Any short transfer on a new connection pays this.

---

## 14.2 Bandwidth, Throughput and Goodput

```
BANDWIDTH   the capacity of a link (20 Mbit/s for the lab's bottleneck)
THROUGHPUT  bits per second actually carried at some layer — always say which: interface
            counters include Ethernet/IP/TCP headers and retransmissions; iperf3 reports
            application bytes
GOODPUT     useful application bytes delivered per second: the file size ÷ the transfer
            time. Excludes headers, retransmissions, protocol chatter.
```

The lab measured all three for a 4 MiB HTTP download through 1% random loss, **capturing at the sender**:

```
duration 0.774 s; IP bytes sent 4255445; TCP payload sent 4233545, of which retransmitted 39096 bytes in 27 segments
throughput (IP bytes / time) 44.0 Mbit/s; goodput (4194304 file bytes / time) 43.4 Mbit/s
```

Overhead and retransmissions cost 1.4% here; at higher loss or with smaller packets the gap grows. Two measurement traps:

- **Capture where the bytes are sent.** A capture at the receiver never sees the lost originals — only the copies that arrived — so it shows no "wasted" bytes at all. (The lab first tried this: the receiver-side capture's payload added up to exactly the file size.)
- **Short transfers do not reach steady state.** This 4 MiB download averaged 43 Mbit/s through 1% loss, far above the ~2.7 Mbit/s that a long flow achieves at the same loss (14.6): it finished mostly inside slow start, before loss had cut the window much. Benchmark with transfers long enough for the steady state you care about — and remember that real users' transfers are often short.

**iperf3** measures throughput between two hosts without an application: `iperf3 -s` on one end, `iperf3 -c HOST [-t 10] [-R reverse] [-P 4 streams] [-w window] [-C cubic|bbr] [-u -b 30M UDP] [-J JSON]` on the other. Use it to separate "the network can't" from "the application doesn't".

---

## 14.3 The Bandwidth-Delay Product and the Window Ceiling

A TCP sender can have at most one window of unacknowledged data in flight, and an acknowledgment takes a round trip to come back. So:

```
throughput ≤ window ÷ RTT                 BDP = bandwidth × RTT

  20 Mbit/s × 50 ms     = 125,000 bytes   (the lab's bottleneck path)
  100 Mbit/s × 50 ms    = 625 KB
  1 Gbit/s × 100 ms     = 12.5 MB         (one intercontinental flow at full rate)
  10 Gbit/s × 0.5 ms    = 625 KB          (a data centre)

  To fill a path, the window (min of receive window and congestion window) must be ≥ BDP.
```

The lab limits iperf3's socket buffers on the 20 Mbit/s, 50 ms path (BDP 125 KB):

| Window | Predicted window ÷ RTT | Measured (3 runs) |
| --- | --- | --- |
| 16 KB (`-w 16K`) | 2.6 Mbit/s | 2.70, 2.70, 2.49 Mbit/s |
| 64 KB (`-w 64K`) | 10.5 Mbit/s | 10.58, 10.58, 10.58 Mbit/s |
| autotuned (no `-w`) | the link: 20 Mbit/s | 18.55, 18.57, 18.52 Mbit/s |

A window-limited flow shows no retransmissions and a steady, low rate: the link is idle most of each round trip, waiting for ACKs. That is the classic "the 10 Gbit/s link to the other region only gives 300 Mbit/s per connection" ticket.

### Buffers on Linux

Linux autotunes each connection's buffers between the bounds of `net.ipv4.tcp_rmem` and `net.ipv4.tcp_wmem` (min, default, max; Ubuntu: `4096 131072 6291456` and `4096 16384 4194304`; newer kernels raise the receive maximum to 32 MB). The maximum must be at least the BDP of the longest fast path you need to fill with one flow — 6 MB covers 1 Gbit/s only up to ~50 ms RTT.

```bash
sysctl net.ipv4.tcp_rmem net.ipv4.tcp_wmem net.ipv4.tcp_moderate_rcvbuf   # autotuning bounds; 1 = on
sudo sysctl -w net.ipv4.tcp_rmem="4096 131072 33554432"   # allow 32 MB receive windows
sudo sysctl -w net.ipv4.tcp_wmem="4096 16384 33554432"
ss -tni dst HOST                                           # per connection: rcv_space, cwnd, rtt, *_limited
```

- **Do not set `SO_RCVBUF`/`SO_SNDBUF` in applications** unless you mean it: an explicit size turns autotuning off for that socket and fixes the window (this is what `iperf3 -w` does, and why it caps throughput above). `net.core.rmem_max`/`wmem_max` cap what applications may request, not what autotuning uses.
- `ss -ti` reports `rwnd_limited` and `sndbuf_limited` (time spent limited by the peer's window or the local send buffer) — the direct evidence for this diagnosis. Module 11's zero-window lab showed `rwnd_limited … 100%`.
- Parallel streams (`iperf3 -P 4`, multi-connection downloaders) multiply the window — a valid workaround, and a reason single-stream and multi-stream tests disagree.

---

## 14.4 Queues and Bufferbloat

When packets arrive at a link faster than it can send, they wait in a queue. A queue absorbs bursts; a queue that stays full only adds delay. A loss-based sender (CUBIC, Reno) keeps increasing its window until the queue overflows, so on a bottleneck with a deep buffer it **keeps the queue full**, and every other flow through that bottleneck waits behind it.

The lab sends one CUBIC flow through the 20 Mbit/s bottleneck while pinging the server, first with a 1000-packet queue (600 ms of buffering at 20 Mbit/s), then with a 30-packet queue (18 ms):

| Queue | Throughput (3 runs) | Ping during the transfer, avg / max | Retransmissions |
| --- | --- | --- | --- |
| 1000 packets | 18.46, 18.80, 18.82 Mbit/s | 228 / 347, 282 / 459, 324 / 507 ms | 0–2 |
| 30 packets | 18.58, 18.58, 18.59 Mbit/s | 58 / 68, 59 / 68, 59 / 67 ms | 144–151 |

Same throughput; one-fifth of the delay. The deep queue avoided drops — and made the 50 ms path behave like a 300 ms one for everyone sharing it. This is **bufferbloat**: the reason a home connection "becomes unusable when someone uploads", and why a video call stutters next to a backup job. The fixes:

- **Active queue management** at the bottleneck: `fq_codel` (Linux's default qdisc on most distributions) and CAKE keep queues short and give each flow its own queue, so a bulk upload cannot delay a ping or a DNS lookup. ECN lets AQM mark instead of drop.
- **Shape slightly below the real bottleneck** so the queue forms where AQM runs (your router), not in the modem or ISP equipment.
- **Delay-aware senders** (BBR, 14.6) keep less data queued.

**UDP has no congestion control.** iperf3 offering 30 Mbit/s of UDP to the 20 Mbit/s bottleneck lost **34.4%** of its datagrams in every run (4,450 of 12,947), and the path's counters showed thousands of `dropped_queue_full`. A TCP flow would have slowed down; a UDP application must do so itself (Module 10).

---

## 14.5 The Cost of Loss

### Loss-based TCP: the Mathis model

A loss-based sender halves (Reno) or cuts by 30% (CUBIC) its window at each loss and grows it back about one segment per round trip. In steady state with random loss rate *p*, that gives the **Mathis model** (Mathis, Semke, Mahdavi and Ott, 1997):

```
throughput ≈ (MSS ÷ RTT) × (C ÷ √p)          C ≈ 1.22 (√(3/2)) for Reno-like behaviour

  1448-byte segments, 51.4 ms RTT:
    p = 0.1%   →  8.7 Mbit/s
    p = 1%     →  2.7 Mbit/s
    p = 2%     →  1.9 Mbit/s
  The same 1% loss at 5 ms RTT → 28 Mbit/s; at 150 ms → 0.9 Mbit/s.
```

Throughput falls with the RTT and with the square root of the loss rate, **and does not depend on the link's bandwidth at all**: 1% loss caps one flow at a few Mbit/s on a 10 Gbit/s intercontinental link. Ping shows "1% loss" and calls the path healthy; for a bulk transfer it is an outage.

The lab measured it on the 50 ms path with no bandwidth limit (the emulator alone moves about 450 Mbit/s), six-second iperf3 runs, loss applied in both directions:

| Random loss | Mathis prediction | CUBIC, 3 runs | BBR, 3 runs |
| --- | --- | --- | --- |
| 0.1% | 8.7 Mbit/s | 14.9, 15.9, 40.4 Mbit/s | 284, 275, 275 Mbit/s |
| 1% | 2.7 Mbit/s | 4.0, 4.7, 2.1 Mbit/s | 202, 208, 204 Mbit/s |
| 2% | 1.9 Mbit/s | 1.9, 3.3, 1.2 Mbit/s | 188, 187, 191 Mbit/s |

How to read it:

- **CUBIC lands near the model at 1–2% loss**, within a factor of two either way, and the short runs vary a lot — which is the honest shape of single-flow loss measurements. At 0.1% CUBIC does better than the Reno-based formula (CUBIC's cubic growth recovers faster after loss on paths with a large window), but it is still a twentieth of what BBR gets.
- **BBR barely notices random loss.** It models the bottleneck's bandwidth and the path's minimum RTT and paces at that rate; it does not interpret each loss as congestion. Hence 200 Mbit/s at 1% loss, at the cost of 1,000+ retransmissions per run. On paths where loss is genuinely from congestion, BBR v1 can be unfair to loss-based flows — one reason it is not the default.
- **Fix the loss first.** Changing congestion control is a mitigation; the cause (a bad link, a duplex mismatch, an overloaded device, a rate limiter, a lossy radio) still needs finding. Loss on one hop shows in Module 7's tools; loss inside a host shows in `nstat` and interface counters.

### Choosing congestion control on Linux

```bash
sysctl net.ipv4.tcp_congestion_control net.ipv4.tcp_available_congestion_control
sudo modprobe tcp_bbr && sudo sysctl -w net.ipv4.tcp_congestion_control=bbr   # new sockets
sudo ip route change default via GW dev eth0 congctl bbr                         # per route
iperf3 -c HOST -C bbr                                                             # per socket (TCP_CONGESTION)
ss -ti | grep -o 'bbr\|cubic'                                                     # what each socket uses
```

Older guides insist BBR "requires the fq qdisc". Since Linux 4.13 TCP paces internally when fq is absent; fq is still a good companion but not a requirement.

---

## 14.6 MTU, MSS and Path MTU Discovery

### Sizes at each layer

```
Ethernet frame:  14 header + up to 1500 (MTU) + 4 FCS  (+ 4 per VLAN tag)
IP packet:       ≤ MTU: 20 (IPv4) or 40 (IPv6) header + payload
TCP segment:     ≤ MSS payload; MSS = MTU − IP header − 20 = 1460 (IPv4), 1440 (IPv6)
                 with timestamps, 1448 bytes of data per full segment

Common smaller MTUs:   PPPoE 1492 · WireGuard 1420 (60 bytes of overhead over IPv4, 80 over IPv6)
                       IPsec ~1400–1440 (cipher and NAT-T dependent) · VXLAN 1450 (50 bytes)
                       GRE 1476 (24 bytes) · GCP VPC default 1460 · AWS site-to-site VPN 1446
Jumbo frames:          9000 (AWS: 9001 inside a VPC, 8500 across Transit Gateway, 1500 to the
                       internet and across inter-region peering; GCP: up to 8896; Azure: 1500
                       by default, larger on supported VM families — check current docs)
```

Jumbo frames cut per-packet work for bulk traffic inside one network, but every device on the path must agree, and anything leaving that network falls back to 1500. With TSO/GSO/GRO the per-packet savings are smaller than the raw packet-count ratio suggests (Module 11).

### PMTUD and its failure

Each sender's IP stack sets DF on TCP packets and relies on routers to report "fragmentation needed" (ICMP type 3 code 4, or ICMPv6 Packet Too Big) when a packet does not fit a link — **Path MTU Discovery** (RFC 1191, RFC 8201). Module 7 covered the mechanism and its ICMP; here it is measured end to end.

The lab narrows the router's client-side link to MTU 1400 — a hop in the middle, so neither endpoint's own MTU or MSS reveals it:

```
server$ ping -c1 -M do -s 1472 198.18.100.10
From 198.18.101.1 icmp_seq=1 Frag needed and DF set (mtu = 1400)
server$ ping -c1 -M do -s 1372 198.18.100.10
1380 bytes from 198.18.100.10: icmp_seq=1 ttl=62 time=50.9 ms         ← 1372 + 8 ICMP + 20 IP = 1400
server$ tracepath -n 198.18.100.10
     Resume: pmtu 1400 hops 3 back 3
```

Then the router stops sending "fragmentation needed" — what a firewall that drops all ICMP does — and the client downloads from the server (`iperf3 -R`, per-second Mbit/s):

| Server setting | Per-second throughput (run 1) | Three runs |
| --- | --- | --- |
| `tcp_mtu_probing=0` (Ubuntu's default) | 0.0 0.0 0.0 0.0 0.0 0.0 0.0 0.0 0.0 0.0 | nothing, for 10 s, in every run |
| `tcp_mtu_probing=1` (PLPMTUD on black-hole detection) | 0.0 0.0 0.0 0.0 44.0 462.6 435.4 … | data from the 5th second in every run |
| `tcp_mtu_probing=0` + `TCPMSS --clamp-mss-to-pmtu` on the router | 289.3 436.2 437.3 435.0 439.4 | full speed at once |

This is the **PMTUD black hole**: the TCP handshake works (small packets), the request works (small), and the response — full-size segments — disappears without a trace. The connection hangs instead of failing; the application eventually reports a read timeout. Captures show the same full-size segment retransmitted with exponentially growing gaps (Module 11's RTO backoff).

The three outcomes are three different fixes:

- **Restore the ICMP** (allow ICMP type 3 / ICMPv6 type 2 through firewalls and security groups). The real fix: PMTUD then works for everyone. Blocking all ICMP "for security" is what causes this.
- **PLPMTUD** (RFC 4821, RFC 8899; Linux `net.ipv4.tcp_mtu_probing=1`): after repeated timeouts the sender falls back to small segments (`tcp_base_mss`, 1024) and probes upward. It recovered every lab run, but only after about four seconds of nothing — fine for a bulk transfer, a failure for an interactive API with a 2-second timeout. `=2` probes from the start. QUIC (HTTP/3) does its own PMTUD this way, without ICMP.
- **MSS clamping** at the device that knows about the narrow link (tunnel endpoints, PPPoE routers, VPN gateways): rewrite the MSS in SYNs so neither side ever sends a segment too big. `--clamp-mss-to-pmtu` uses the MTU of the routes towards both the SYN's destination and its sender — it handled the lab's narrow return path — or `--set-mss N` fixes a value. It only helps TCP; UDP and ICMP still see the black hole.

Note the `ttl=62` above: the lab's emulated router counts twice (packets cross the kernel's forwarding path on the way into the emulator and again on the way out).

---

## 14.7 Diagnosing a Slow Connection — Measure, Then Localise

```
1. WHAT exactly is slow?     One request or all? Small requests or large transfers? From where?
                             Since when? Get a reproducer: a URL, a size, a client location.
2. WHERE does the time go?   curl -w '%{time_namelookup} %{time_connect} %{time_appconnect}
                             %{time_starttransfer} %{time_total}' — from the affected client.
     DNS large               → Module 12 (resolver, search list, timeouts)
     connect large           → RTT or SYN retransmissions (Module 11: exactly 1 s, 3 s, …?)
     appconnect − connect    → TLS round trips (13.5), certificate fetching, HelloRetryRequest
     starttransfer − appconnect → server time (logs, APM) — or one network round trip
     total − starttransfer   → transfer: steps 3–5
3. Is the transfer window-, loss-, queue- or size-limited?
     ss -tni on the sender: rtt, cwnd, rwnd_limited/sndbuf_limited, retrans, delivery_rate
     window-limited          → throughput ≈ window ÷ RTT, no retransmissions (14.3)
     loss-limited            → retransmissions; Mathis-scale throughput (14.5)
     queue-limited           → RTT rises under load; ping during the transfer (14.4)
     size-limited (MTU)      → small works, full-size segments retransmitted, no ACK (14.6)
4. Is it the network at all? iperf3 between the same hosts (or nearby ones), single and multi
     stream; compare with the application's rate. If iperf3 is fast, look at the application,
     its buffers, its disk, its CPU.
5. Localise along the path:  mtr / tracepath from both ends; compare RTT and loss per hop
     (Module 7's caveats apply); compare with a healthy client or region.
```

The order matters less than the habit: **measure the phase that is slow before changing anything**, and change one thing at a time.

---

## Hands-On Lab: Performance on an Emulated Path

### Lab Goals

- Measure RTT and a request's timing breakdown; compare a cold and a reused connection
- Confirm window ÷ RTT against the BDP with three window sizes
- Measure bufferbloat with deep and short queues, and UDP overload
- Measure single-flow TCP throughput under loss against the Mathis model, CUBIC and BBR
- Measure goodput vs throughput from a sender-side capture
- Find a path MTU, create a PMTUD black hole, and compare PLPMTUD and MSS clamping

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iperf3`, `curl`, `tcpdump`, `tshark`, `iptables`, `iputils-tracepath`, `python3`; the `tun` and `tcp_bbr` kernel modules

The lab uses [`pathlab`](../labs/go/cmd/pathlab), the course's userspace path emulator: in the router's namespace, policy routing sends forwarded packets into two TUN devices (one per direction); `pathlab` reads each packet, drops it with the configured probability, passes it through a rate-limited bottleneck with a bounded queue (tail drop when full), holds it for the configured delay, and writes it back for the kernel to forward. `pathlab set delay=25ms rate=20mbit limit=30 loss=0.01` changes the path while it runs; `pathlab stats` prints both directions' counters (packets, random drops, queue-full drops, maximum queueing delay). On a kernel with `tc netem` the same experiments work with `tc qdisc add dev eth1 root netem delay 25ms rate 20mbit limit 30 loss 1%` on both router interfaces. The HTTPS endpoints are the course's `labserver`.

---

### Lab Part 1: Run the Performance Lab

[`perf.json`](../labs/netlab/perf.json):

```
  client 198.18.100.10 ──── router (pathlab: 25 ms each way; rate, queue, loss as set) ──── server 198.18.101.10
                                                                                            iperf3 :5201
                                                                                            api.perf.test :8443 (2 KB, +100 ms)
                                                                                            files.perf.test :9443 / :8080 (4 MiB)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/perf.json        # about 2 minutes
```

The tables in 14.1–14.6 are from three runs; the evidence directory holds each check's output (`curl-timing.txt`, `reuse.txt`, `windows.txt`, `bloat-*.txt`, `udp-overload.txt`, `loss-sweep.txt`, `goodput.pcap` and `goodput.txt`, `pmtu.txt`, `blackhole.txt`, `plpmtud.txt`, `clamped.txt`) and the path's counters (`path-*.txt`). The lab runs iperf3 with the system's default congestion control except where a check names one; Ubuntu's default is CUBIC.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/perf.json
alias lab='sudo ./bin/netlab exec ../netlab/perf.json'
```

Unset `HTTPS_PROXY`/`HTTP_PROXY` in the client commands if your shell sets them.

1. **Predict, then measure.** `lab router -- ./bin/pathlab set delay=75ms rate=50mbit limit=100`. Compute the BDP (50 Mbit/s × 150 ms = 937 KB) and the 64 KB-window ceiling (3.5 Mbit/s), then run `lab client -- iperf3 -c 198.18.101.10 -t 8 -w 64K` (measured: 3.60 Mbit/s) and without `-w` (41.4 Mbit/s: at 150 ms, slow start and its losses take a large share of 8 seconds). Repeat the curl timing from Part 1 (add `--cacert` with the `api-ca.pem` from the evidence directory): measured tcp 0.152, tls 0.311, first byte 0.565 s — each round trip now costs 150 ms instead of 50.
2. **Watch slow start.** `lab client -- iperf3 -c 198.18.101.10 -t 3 -i 0.25`: the rate doubles interval by interval (4, 4, 17, 55, 80 Mbit/s with `Cwnd` 31 KB → 1.75 MB), overshoots the 50 Mbit/s bottleneck, and the next intervals show hundreds of retransmissions as the queue overflows. (iperf3's sender-side intervals count bytes handed to the socket, so they swing above the link rate.) While it runs, `lab client -- ss -tni dst 198.18.101.10` shows `cwnd`, `rtt` and `delivery_rate` (the first socket listed is iperf3's idle control connection).
3. **Bufferbloat with BBR.** `lab router -- ./bin/pathlab set delay=25ms rate=20mbit limit=1000`, then ping alongside an 8-second iperf3 with `-C bbr` and again with `-C cubic` (`lab client -- ping -i 0.2 -c 40 -q 198.18.101.10 & lab client -- iperf3 -c 198.18.101.10 -t 8 -C bbr`). Measured: both 18.8–18.9 Mbit/s; ping avg/max 77 / 156 ms with BBR (the peak is its startup phase) vs 354 / 560 ms with CUBIC. Module 11 measured the same effect in TCP's own RTT (6 ms vs 136 ms).
4. **Find a path MTU by hand.** `lab router -- ./bin/pathlab set rate=0` and `lab router -- ip link set eth0 mtu 1400`, then binary-search with `lab server -- ping -c1 -W1 -M do -s SIZE 198.18.100.10`. The first oversized ping gets `Frag needed and DF set (mtu = 1400)`; after that the server has cached the path MTU and later oversized pings fail locally (`ping: local error: message too long, mtu=1400`) without sending anything. 1372 is the largest size that gets a reply. Now block the router's ICMP (`lab router -- iptables -A OUTPUT -p icmp --icmp-type fragmentation-needed -j DROP`), clear the server's cache (`lab server -- ip route flush cache`) and repeat: 1373 now gets no answer at all — `100% packet loss`, indistinguishable from loss, which is why a size-dependent timeout is the signature to look for.

`sudo ./bin/netlab down ../netlab/perf.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
curl -w 'dns %{time_namelookup} tcp %{time_connect} tls %{time_appconnect} ttfb %{time_starttransfer} total %{time_total}\n' -o /dev/null -s https://example.com/
ping -c 20 -q 1.1.1.1                       # then repeat while a large upload runs: bufferbloat?
tracepath -n example.com                    # the path MTU to a real destination
ping -c 3 -M do -s 1472 1.1.1.1             # does 1500 fit? (behind a VPN it usually doesn't)
ss -tni | grep -E 'cwnd|rtt' | head          # your connections' windows and RTTs
sysctl net.ipv4.tcp_congestion_control net.ipv4.tcp_rmem net.ipv4.tcp_mtu_probing
```

A public bufferbloat test (for example Waveform's or the one built into Cloudflare's speed test) measures latency under load on your own connection.

---

## Real-World Troubleshooting Scenario

### Scenario: "Large Downloads Stall Over the VPN"

**The situation.** An internal file service works for everyone in the office. Engineers connected through the company VPN can browse its web UI and call its small API endpoints, but downloads of more than a few kilobytes stall at "0%" and fail after a minute. Nothing changed on the service; the VPN was migrated to a new gateway last week.

**Step 1 — Which phase is slow?**

```bash
curl -w 'tcp %{time_connect} tls %{time_appconnect} ttfb %{time_starttransfer} total %{time_total}\n' -o /dev/null --max-time 60 https://files.internal.example/report.pdf
tcp 0.031 tls 0.064 ttfb 0.097 total 60.001        # handshakes and first byte fine; the body never comes
```

**Step 2 — Is it size-dependent?** A small endpoint works through the same connection path; a large one does not. Over the VPN:

```bash
ping -c2 -M do -s 1372 files.internal.example      # replies
ping -c2 -M do -s 1400 files.internal.example      # no reply, no "Frag needed" either
tracepath -n files.internal.example                # stops reporting pmtu at the VPN gateway
```

A size threshold with no ICMP error: a PMTUD black hole. The new gateway's tunnel MTU is 1420 (WireGuard), and its firewall policy drops ICMP.

**Step 3 — Confirm with a capture on the server.** The server sends full-size 1448-byte segments; the same sequence number repeats at 0.2, 0.6, 1.4, 3.0 s …; no ACK ever covers it; no ICMP arrives.

**The fix.** On the VPN gateway, allow ICMP "fragmentation needed" (and ICMPv6 Packet Too Big) back towards senders, and clamp the MSS on the tunnel (`iptables -t mangle -A FORWARD -o wg0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu`). Re-test with the large download. Enabling `tcp_mtu_probing=1` on the servers is a useful safety net — the lab showed it recovers after a few seconds — but not a substitute for a gateway that tells senders the truth.

**What made it fast:** the curl phases showed the stall was in the transfer, not the handshakes; the size dependence pointed at MTU; DF pings found the threshold; and the absence of any ICMP error explained why TCP never adapted.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| Every request ~N × RTT slow, bandwidth irrelevant | Round trips: new connections, TLS 1.2, sequential requests | `curl -w` phases; count handshakes | Reuse connections, TLS 1.3, HTTP/2, move closer (CDN) |
| First transfer slow, later ones fast | Slow start on a new connection | Same transfer twice on one connection (curl URL URL) | Connection reuse; keep connections warm |
| Single flow stuck at a low, steady rate; no retransmissions | Window-limited (window < BDP) | Throughput ≈ window ÷ RTT; `ss -ti` rwnd_limited / sndbuf_limited | Raise `tcp_rmem`/`tcp_wmem` max; remove fixed SO_RCVBUF; parallel streams |
| Throughput collapses with "only" 1% loss | Loss-based congestion control (Mathis) | Retransmissions; compare with the model | Find and fix the loss; BBR as a mitigation |
| Latency soars whenever a big transfer runs | Bufferbloat at the bottleneck | Ping during the transfer vs idle | fq_codel/CAKE at the bottleneck; shape just below line rate |
| UDP application loses data under load | No congestion control; queue overflow | iperf3 -u at the offered rate; queue drop counters | Application rate control; fair queueing |
| Small requests fine, large transfers hang | PMTUD black hole | DF pings find a threshold with no ICMP error; repeated full-size retransmissions | Allow ICMP type 3/PTB; MSS clamping; PLPMTUD as a safety net |
| Works on one network, fails over VPN/tunnel | Tunnel MTU smaller than 1500 | `tracepath`; `ip link` on the tunnel | MSS clamp on the tunnel; correct tunnel MTU |
| Jumbo frames fine inside the VPC, failures beyond | MTU mismatch at the boundary | `ping -M do -s 8972` inside vs outside | Clamp/limit at the boundary; 1500 for external paths |
| iperf3 fast, application slow | Application, disk, CPU, or its own buffers | Compare iperf3 with app throughput; profile | Fix the application |
| Single-stream test slow, multi-stream fast | Per-flow window or loss limit | `-P 1` vs `-P 8` | Raise buffers / fix loss; accept multi-stream |

---

## Summary

- One-way delay is propagation + transmission + queueing + processing; propagation is fixed by distance (~5 µs/km in fibre), queueing is what varies. Ping measures the round trip.
- Interactive requests are round-trip-bound: on the lab's 50 ms path a new HTTPS connection spent ~160 ms on round trips before 100 ms of server work. A reused connection skipped those and slow start: 4 MiB took 0.12 s instead of 0.6 s.
- Throughput ≤ window ÷ RTT; to fill a path the window must reach the BDP. Measured: 16 KB → 2.7 Mbit/s, 64 KB → 10.6 Mbit/s, autotuned → 18.6 Mbit/s of 20.
- Goodput is file bytes ÷ time; measure overhead and retransmissions at the sender — a receiver never sees what was lost.
- A full deep queue adds its whole depth to everyone's latency: same 18.5 Mbit/s, ping 228–324 ms with 1000 packets queued vs 58 ms with 30.
- Single-flow loss-based TCP is limited to about (MSS/RTT) × 1.22/√p regardless of bandwidth (2.7 Mbit/s at 1% loss and 50 ms); measured CUBIC landed within a factor of two; BBR held ~200 Mbit/s.
- PMTUD needs ICMP "fragmentation needed"; without it, full-size packets vanish and connections hang. PLPMTUD recovered after ~4 s; MSS clamping avoided the problem entirely.
- Diagnose slowness by measuring which phase is slow, then whether the transfer is window-, loss-, queue- or size-limited — before changing anything.

## What's Next

Module 15 covers HTTP/2, HTTP/3, gRPC and WebSockets — protocols designed around the costs measured here: multiplexing to avoid new connections and head-of-line blocking, QUIC to save handshake round trips and recover from loss per stream.

---

## Quick Reference Card

```
MODULE 14: NETWORK PERFORMANCE — QUICK REFERENCE
════════════════════════════════════════════════

DELAY            propagation (~5 µs/km fibre) + transmission (size/rate) + queueing + processing
CURL PHASES      curl -w '%{time_namelookup} %{time_connect} %{time_appconnect} %{time_starttransfer} %{time_total}'
                 TCP 1 RTT · TLS 1.3 1 RTT (1.2: 2) · request 1 RTT + server time · then transfer
BDP              bandwidth × RTT;  throughput ≤ window ÷ RTT
                 20 Mbit/s × 50 ms = 125 KB · 1 Gbit/s × 100 ms = 12.5 MB
MATHIS           throughput ≈ (MSS/RTT) × 1.22/√p   (1448 B, 50 ms: 1% → 2.7 Mbit/s, 0.1% → 8.7)
GOODPUT          useful bytes ÷ time; measure retransmissions at the SENDER
BUFFERBLOAT      ping rises under load; fix with fq_codel/CAKE at the bottleneck

MTU / MSS        Ethernet 1500 · MSS 1460 (v4) / 1440 (v6) · 1448 with timestamps
                 PPPoE 1492 · WireGuard 1420 · VXLAN 1450 · GRE 1476 · GCP 1460 · jumbo 9000/9001
PATH MTU         ping -M do -s 1472 HOST (1472+28=1500) · tracepath -n HOST
BLACK HOLE       small OK, large hangs, no ICMP → allow ICMP type 3 code 4 / ICMPv6 PTB
                 MSS clamp: iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
                 PLPMTUD: sysctl net.ipv4.tcp_mtu_probing=1 (recovers after timeouts; =2 always)

TOOLS            iperf3 -s · iperf3 -c H [-t] [-R] [-P N] [-w W] [-C cc] [-u -b R] [-J]
                 ss -tni (rtt, cwnd, rwnd_limited, sndbuf_limited, retrans, delivery_rate)
                 mtr -rwc 100 H · tshark -z conv,tcp / io,stat · nstat
                 pathlab set delay=25ms rate=20mbit limit=30 loss=0.01 · pathlab stats
                 tc qdisc add dev IF root netem delay 25ms rate 20mbit limit 30 loss 1%

TUNING           net.ipv4.tcp_rmem / tcp_wmem (autotuning max ≥ BDP) · avoid fixed SO_RCVBUF
                 net.ipv4.tcp_congestion_control=bbr (+ modprobe tcp_bbr) · per route: congctl bbr
```

---

## References

- Mathis, Semke, Mahdavi, Ott — "The Macroscopic Behavior of the TCP Congestion Avoidance Algorithm", ACM CCR, 1997
- RFC 1191 / RFC 8201 — Path MTU Discovery for IPv4 / IPv6
- RFC 4821 / RFC 8899 — Packetization Layer Path MTU Discovery (TCP / datagram transports)
- RFC 5681 — TCP Congestion Control; RFC 9438 — CUBIC; Cardwell et al., "BBR: Congestion-Based Congestion Control", ACM Queue 2016
- RFC 8289 — Controlled Delay (CoDel); RFC 8290 — FQ-CoDel; RFC 7567 — IETF Recommendations Regarding Active Queue Management
- RFC 7323 — TCP Extensions for High Performance (window scaling)
- Linux `Documentation/networking/ip-sysctl.rst` (tcp_rmem, tcp_wmem, tcp_mtu_probing, tcp_base_mss)
- `man iperf3`, `man ss`, `man tracepath`, `man tc-netem`, `man tc-fq_codel`, `man iptables-extensions` (TCPMSS)
- AWS, Azure and Google Cloud documentation on network MTU (jumbo frames, VPN, peering, Transit Gateway)
