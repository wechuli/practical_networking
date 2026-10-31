# Module 7: ICMP, Ping, and Traceroute

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 6 — ARP — Address Resolution Protocol
> **Estimated time:** 3 hours (the main lab runs offline in network namespaces)

## Overview

ICMP is the error reporting and diagnostic protocol of the IP layer. It is not a transport protocol — it does not carry application data. It is a control protocol — it tells the sender when something goes wrong with packet delivery. When a router drops a packet because there is no route, ICMP reports it. When a packet is too large for a link and the Don't Fragment bit is set, ICMP reports it. When a packet's TTL reaches zero, ICMP reports it. Without ICMP, IP would be completely silent about failures — packets would vanish and the sender would never know why. Even with it, many failures stay silent: a congested queue, a firewall DROP rule, or a router configured not to send errors produces nothing. ICMP tells you about the failures someone chose to report.

Ping and traceroute are the two most commonly used diagnostic tools in networking, and both are built on ICMP. `ping` uses ICMP Echo Request and Echo Reply to test whether a host is reachable and to measure round-trip latency. `traceroute` exploits the ICMP Time Exceeded message — sent by routers when they decrement a packet's TTL to zero — to map the hop-by-hop path between two hosts.

Understanding ICMP means understanding the diagnostic language of IP itself. When a firewall blocks ICMP, it is not just blocking `ping` — it is blocking the mechanism that tells senders about unreachable hosts, MTU limits, and routing problems. Many of the hardest-to-diagnose production issues — PMTUD black holes, silent packet drops, asymmetric path failures — are caused by misconfigured ICMP filtering.

## What You Will Build Toward

- Explain what ICMP is and why it is a control protocol, not a data protocol
- Read ping output and immediately extract RTT, TTL, and packet loss — and know what each means
- Identify the key ICMP message types by number and explain when each is generated
- Use traceroute, tracepath, and mtr to map network paths and identify where latency or loss occurs
- Understand why asterisks in traceroute output are not always a problem
- Explain ICMP Type 3 Code 4 (Fragmentation Needed) and its critical role in Path MTU Discovery
- Recognize when ICMP filtering is causing real production problems vs. when it is benign

---

## 7.1 What Is ICMP?

### What This Is

ICMP — Internet Control Message Protocol — is defined in RFC 792 for IPv4 and RFC 4443 for IPv6 (where it is called ICMPv6). It is a Layer 3 protocol that rides inside IP packets. In the IP header, ICMP traffic is identified by Protocol number 1 (IPv4) or Next Header 58 (IPv6).

ICMP is not TCP or UDP. It has no port numbers. There are no ICMP "connections." Firewalls that work with port-based rules cannot filter ICMP the same way they filter TCP or UDP — ICMP is filtered by message type and code instead. Stateful firewalls (iptables/nftables conntrack, cloud security groups) match an ICMP *error* to the flow it quotes and treat it as `RELATED` traffic; echo requests and replies are tracked like a tiny connection by their identifier.

### ICMP's Purpose

ICMP exists to carry error messages and operational information about IP. It serves three roles:

**1. Error Reporting.** When a router or host encounters a problem delivering a packet, it sends an ICMP error message back to the source. The error message includes the IP header and at least the first 8 bytes of the original packet that caused the problem — enough for the sender to match the error to a specific flow (those 8 bytes contain the TCP/UDP source and destination ports). RFC 1812 asks routers to quote as much as fits in 576 bytes, and Linux does: the lab's errors quote the whole probe.

The error goes to the packet's **source address**, from the router's address on the interface it sends the error out of. That is why traceroute shows one address per router (the one facing you, usually), and why a black-holed return path also loses the errors.

ICMP never generates errors about ICMP errors, about broadcast or multicast packets, or about non-first fragments — rules that prevent error storms.

**2. Diagnostics.** ICMP Echo Request and Echo Reply (ping) provide a simple test of reachability and round-trip time. ICMP Timestamp and Address Mask messages also exist but are rarely used in practice.

**3. Path Discovery.** ICMP Time Exceeded (generated when TTL reaches 0) enables traceroute. ICMP Fragmentation Needed (Type 3 Code 4) enables Path MTU Discovery.

### How ICMP Fits in the Stack

```
ICMP IS INSIDE IP — NOT ABOVE IT

  ┌────────────────────────────────────────────┐
  │ Layer 4:  TCP (Proto 6) / UDP (Proto 17)   │
  ├────────────────────────────────────────────┤
  │ Layer 3:  IP                               │
  │           ├── Carries TCP   (Protocol = 6) │
  │           ├── Carries UDP   (Protocol = 17)│
  │           └── Carries ICMP  (Protocol = 1) │  ← ICMP is here
  ├────────────────────────────────────────────┤
  │ Layer 2:  Ethernet                         │
  └────────────────────────────────────────────┘

  ICMP is NOT a Layer 4 protocol. It does not ride on top of TCP or UDP.
  It rides directly inside IP, at the same level as TCP and UDP.
  IP header Protocol field = 1 means ICMP.

  An ICMP packet on the wire:
  ┌──────────┬──────────┬───────────────────────────┐
  │ Ethernet │ IP Header│ ICMP Message              │
  │ Header   │ Proto=1  │ (Type, Code, Checksum,    │
  │          │          │  message-specific data)    │
  └──────────┴──────────┴───────────────────────────┘
```

### ICMP Message Format

```
ICMP MESSAGE STRUCTURE

  0                   1                   2                   3
  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |     Type      |     Code      |          Checksum             |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                   Message-Specific Data                       |
  |              (varies by Type and Code)                        |
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+

  Type (8 bits):  Identifies the ICMP message category
  Code (8 bits):  Sub-type within the category — gives more detail
  Checksum (16 bits): Error detection over the entire ICMP message

  For error messages, the data section contains:
  ┌────────────────────────────────────────────────────────────┐
  │  IP Header of the original packet that caused the error    │
  │  + First 8 bytes of the original packet's payload          │
  │    (enough to identify the TCP/UDP src port + dst port)    │
  └────────────────────────────────────────────────────────────┘
  This is how the sender knows WHICH flow triggered the error.
```

### Key ICMP Message Types (Summary)

```
TYPE  CODE   NAME                         WHEN GENERATED
────  ────   ──────────────────────────   ─────────────────────────────────────────
  0    0     Echo Reply                   Response to ping (Echo Request)
  3    —     Destination Unreachable       Packet could not be delivered
  3    0       Network Unreachable         No route to destination network
  3    1       Host Unreachable            Route exists but host is down/unreachable
  3    2       Protocol Unreachable        Destination host does not support protocol
  3    3       Port Unreachable            No UDP listener — or a firewall REJECT
  3    4       Fragmentation Needed        Packet too large, DF bit set (PMTUD)
  3   10       Host Prohibited             Firewall (iptables icmp-host-prohibited)
  3   13       Administratively Prohibited Firewall blocked the packet
  5    —     Redirect                      Router tells sender to use a better path
  8    0     Echo Request                  Ping — "are you there?"
 11    0     Time Exceeded — TTL           TTL reached 0 in transit (traceroute)
 11    1     Time Exceeded — Fragment      Fragment reassembly timeout
```

### Production Relevance

ICMP is both essential and controversial. Network security teams often want to block ICMP to reduce the attack surface. But blocking ICMP indiscriminately breaks real functionality:

- **Blocking Type 3 Code 4** breaks Path MTU Discovery. TCP connections establish normally (small SYN/SYN-ACK packets fit through), but large data transfers stall because the sender never learns that its packets are too large for a link in the path. This is a PMTUD black hole.
- **Blocking Type 3 Code 3** prevents UDP clients from learning that no one is listening: a DNS client gets a timeout after seconds instead of an immediate "connection refused".
- **Blocking Type 11** breaks traceroute. Not critical for production, but cripples diagnostics.
- **Blocking Type 0 and 8** breaks ping. Again not critical, but makes basic reachability testing impossible.

The safe rule: **never block ICMP Type 3** (Destination Unreachable) — and for IPv6, ICMPv6 types 1–4 and Neighbor Discovery (RFC 4890). This message type carries critical feedback that IP and TCP rely on. Rate limiting is the right defense against ICMP floods; every Linux host and router already rate-limits the errors it generates (7.4 shows the effect).

**How ICMP errors reach applications.** The kernel matches an error to the socket it quotes and turns it into an errno — the strings support engineers actually see in logs:

| ICMP received | Linux errno | What the application prints |
| --- | --- | --- |
| Type 3 code 3 (port unreachable) for UDP | `ECONNREFUSED` (on a connected UDP socket) | dig: `communications error … connection refused` |
| Type 3 code 3 in reply to a TCP SYN (firewall REJECT default) | `ECONNREFUSED` | `Connection refused` — identical to a closed port |
| Type 3 code 1, 10, or 13 | `EHOSTUNREACH` | `No route to host` |
| Type 3 code 0 | `ENETUNREACH` | `Network is unreachable` |
| Type 3 code 4 | none; the kernel lowers the path MTU | nothing, unless it is blocked — then a hang |

The lab produces every row and shows the application's message next to the packet that caused it. A `No route to host` therefore does **not** mean the routing table is missing a route; it usually means a firewall said no or ARP failed somewhere.

---

## 7.2 Ping

### What This Is

`ping` sends ICMP Echo Request (Type 8 Code 0) packets to a target and listens for ICMP Echo Reply (Type 0 Code 0) packets in return. It is the simplest test of Layer 3 reachability: can a packet reach the destination and can a reply make it back?

### How Ping Works

```
PING — ECHO REQUEST / ECHO REPLY

  [Your Host]                                [Target Host]
       │                                          │
       │  ICMP Echo Request (Type 8, Code 0)      │
       │  IP src=10.0.1.50, dst=8.8.8.8           │
       │  Identifier=1234, Sequence=1              │
       │  Payload: timestamp + padding data        │
       │─────────────────────────────────────────▶│
       │                                          │
       │  ICMP Echo Reply (Type 0, Code 0)        │
       │  IP src=8.8.8.8, dst=10.0.1.50           │
       │  Identifier=1234, Sequence=1              │
       │  Payload: echoed back unchanged           │
       │◀─────────────────────────────────────────│
       │                                          │
       │  RTT = time(Reply received) -            │
       │        time(Request sent)                │
       │  = 12.3 ms                               │

  The Identifier field links a reply to a specific ping process.
  The Sequence field links a reply to a specific request.
  Multiple ping processes on the same host use different Identifiers.
```

### Reading Ping Output

```bash
ping -c 5 8.8.8.8
```

```
PING 8.8.8.8 (8.8.8.8) 56(84) bytes of data.
                         ↑  ↑
                    payload  total (56 payload + 8 ICMP header + 20 IP header)

64 bytes from 8.8.8.8: icmp_seq=1 ttl=118 time=12.3 ms
 ↑                                  ↑       ↑       ↑
 reply size                    sequence   TTL from   round-trip
 (56 data + 8 ICMP hdr)       number     the reply  time

64 bytes from 8.8.8.8: icmp_seq=2 ttl=118 time=11.9 ms
64 bytes from 8.8.8.8: icmp_seq=3 ttl=118 time=12.1 ms
64 bytes from 8.8.8.8: icmp_seq=4 ttl=118 time=12.5 ms
64 bytes from 8.8.8.8: icmp_seq=5 ttl=118 time=12.0 ms

--- 8.8.8.8 ping statistics ---
5 packets transmitted, 5 received, 0% packet loss, time 4005ms
                                    ↑
                               0% = perfect connectivity

rtt min/avg/max/mdev = 11.9/12.2/12.5/0.2 ms
    ↑    ↑    ↑    ↑
  best  mean  worst  standard deviation (jitter indicator)
```

### What Each Field Tells You

```
FIELD            WHAT IT MEANS                          WHAT TO WATCH FOR
───────────────  ─────────────────────────────────────  ───────────────────────
icmp_seq         Sequence number — increments by 1      Missing sequence numbers
                 for each packet sent                   = packet loss

ttl              TTL value in the reply. Tells you       Starting TTL varies by OS:
                 how many hops the reply traversed.      64=Linux, 128=Windows, 255=net equip
                 Original TTL - received TTL = hops.     Low TTL = many hops
                                                         Varying TTL = path changes

time             Round-trip time (RTT) in ms.            < 1 ms: same LAN or data center
                 Includes: propagation delay,            Light in fiber: ~5 µs/km one
                 processing delay, queuing delay.        way, so ≥ 1 ms RTT per 100 km
                                                         of path (paths are longer than
                                                         straight lines). Geostationary
                                                         satellite: ≥ ~480 ms RTT.
                                                         Highly variable = queuing

packet loss      Percentage of sent packets that         0%: healthy
                 received no reply.                      1-5%: marginal, investigate
                                                         > 5%: problem
                                                         100%: host unreachable or ICMP blocked

mdev             Standard deviation of RTT samples       Low = less variation
                 (iputils calls it mdev).
                 How consistent the RTT is.              High = congestion, bufferbloat,
                                                         or load-based path changes
```

### Useful Ping Options

```bash
# Basic: 5 packets
ping -c 5 8.8.8.8

# Specify interval (default 1 second). 0.2 = 5 packets/second
ping -c 20 -i 0.2 8.8.8.8

# Set packet size (payload bytes). Default is 56.
# Useful for MTU testing.
ping -c 3 -s 1472 8.8.8.8           # Max payload for 1500 MTU
ping -c 3 -s 1472 -M do 8.8.8.8     # -M do = set Don't Fragment (DF) bit

# Faster, still bounded probes to a target you own. Intervals below 0.2 s
# need root; flood mode (-f) is for your own lab only.
ping -c 10 -i 0.2 <owned-lab-target>

# Print a line for each missing reply (otherwise loss only shows in the summary)
ping -O -c 20 8.8.8.8

# Timestamp each line — useful when correlating with a capture or a log
ping -D -c 5 8.8.8.8

# Set TTL explicitly. Useful for testing how many hops away a host is.
ping -c 3 -t 5 8.8.8.8              # Dies at hop 5 if destination is further

# Quiet mode — only show summary
ping -c 5 -q 8.8.8.8

# Specify source interface or IP
ping -c 3 -I eth0 8.8.8.8
```

### What Ping Can and Cannot Tell You

```
PING CAN tell you:
  ✓ Layer 3 reachability — can packets reach the destination and return?
  ✓ Round-trip latency — how long does the journey take?
  ✓ Packet loss rate — are packets being dropped?
  ✓ TTL — how many hops (approximate)
  ✓ Basic path stability — is latency consistent?

PING CANNOT tell you:
  ✗ Whether a specific TCP/UDP port is open (use nc -zv or hping3 for that)
  ✗ Whether the application is working (use curl or protocol-specific tools)
  ✗ Whether ICMP is filtered — "no reply" does NOT mean "host is down"
  ✗ Where in the path the problem is (use traceroute/mtr for that)
  ✗ One-way latency (RTT includes both directions)
  ✗ Whether the forward and return paths are the same

CRITICAL RULE:
  ping succeeds    → small ICMP packets get there and back. A TCP port can
                     still be filtered, large packets can still be
                     black-holed (7.3), and the application can be down.
  ping fails       → EITHER Layer 3 is broken
                     OR ICMP is filtered (cloud load balancers and many
                     managed services never answer ping).

  Never conclude a host is unreachable based on ping alone.
  Always verify with a TCP probe: nc -zv <host> <port>
```

### Diagram: Ping Failure Modes

```
WHAT HAPPENS WHEN PING FAILS — THREE DIFFERENT CAUSES

  Cause 1: A router (or your host) reports the failure
  ────────────────────────────────────────────────────
  [You] ──ICMP Echo──▶ [Router] ──▶ ✗ no route / no ARP answer
                              │
                              └──ICMP Dest Unreachable──▶ [You]

  You see: "From <router> … Destination Net Unreachable" (no route at
  that router) or "From <router> … Destination Host Unreachable" (the
  last router could not ARP for the host, after ~3 s). "From <your own
  IP>" means your own host failed the same way. Genuine L3/L2 problem.


  Cause 2: ICMP filtered at firewall (host is alive)
  ──────────────────────────────────────────────────
  [You] ──ICMP Echo──▶ [Firewall] ──DROP──  (packet vanishes)

  You see: silence. Linux ping prints nothing per lost packet (use -O to
  print "no answer yet"); Windows prints "Request timed out".
  Meaning: A firewall dropped the ICMP packet. The host may be fine.
           Test with: nc -zv <host> 443 (TCP probe)


  Cause 3: Asymmetric path (outbound works, return fails)
  ───────────────────────────────────────────────────────
  [You] ──ICMP Echo──▶ [Target]   ← Request arrives
  [You]                [Target] ──ICMP Reply──▶ ✗ return path broken

  You see: 100% packet loss.
  Meaning: The target received the ping but its reply cannot reach you.
           Common with: wrong default gateway on target, stateful firewall
           blocking the return, or asymmetric routing.
           Verify by: capturing on the target to confirm requests arrive.
```

---

## 7.3 ICMP Types in Depth

### Type 3: Destination Unreachable

This is the most information-rich ICMP message type. The Code field tells you exactly why the packet could not be delivered.

```
ICMP TYPE 3 — DESTINATION UNREACHABLE — CODE TABLE

  CODE  NAME                         GENERATED BY    WHAT IT MEANS
  ────  ───────────────────────────  ──────────────  ──────────────────────────────────
   0    Network Unreachable          Router          No route to the destination network.
                                                     The last router in the path has no
                                                     matching entry in its routing table.

   1    Host Unreachable             Router          Route to the network exists, but the
                                                     specific host did not respond to ARP.
                                                     Host is down or not on that subnet.
                                                     Arrives ~3 s late: the ARP attempts.

   2    Protocol Unreachable         Dest host       The destination host does not support
                                                     the protocol in the IP header's Protocol
                                                     field. Rare in practice.

   3    Port Unreachable             Dest host       No process is listening on the
                                                     or firewall     destination UDP port. The host received
                                                                     the packet but had nowhere to deliver it.
                                                     (TCP uses RST for this; UDP uses ICMP.)
                                                     iptables REJECT sends this by default,
                                                     for TCP too — and TCP reports it as
                                                     "Connection refused".

   4    Fragmentation Needed and     Router          The packet is too large for the next
        DF Set                                       link, and the Don't Fragment bit is set.
                                                     THE MOST IMPORTANT ICMP MESSAGE FOR
                                                     PRODUCTION. Enables PMTUD. Includes
                                                     the MTU of the next hop in the message.

   9    Communication with Dest      Router          Administrative filtering is blocking
        Network Administratively                     traffic to the entire network.
        Prohibited

  10    Communication with Dest      Firewall        iptables --reject-with
        Host Administratively                        icmp-host-prohibited (common in older
        Prohibited                                   RHEL/CentOS default rule sets).

  13    Communication Administratively Firewall/     A firewall explicitly rejected the
        Prohibited                     Router        packet based on a policy rule. This is
                                                     the "the firewall blocked it" signal.
                                                     Applications see "No route to host".
```

### Type 3 Code 4: Fragmentation Needed — The PMTUD Signal

This message deserves special emphasis because blocking it causes one of the hardest-to-diagnose network failures.

```
PMTUD AND ICMP TYPE 3 CODE 4

  How Path MTU Discovery works:

  [Sender]                   [Router]                    [Destination]
     │                          │                             │
     │  IP packet, 1500 bytes   │                             │
     │  DF bit = 1 (Don't Frag) │                             │
     │─────────────────────────▶│                             │
     │                          │  Next link MTU = 1400       │
     │                          │  Packet too big, DF=1       │
     │                          │  CANNOT fragment             │
     │                          │  DROP the packet             │
     │                          │                             │
     │  ICMP Type 3 Code 4     │                             │
     │  "Frag Needed, MTU=1400"│                             │
     │◀─────────────────────────│                             │
     │                          │                             │
     │  Sender learns: max      │                             │
     │  packet size = 1400      │                             │
     │  Retransmits with        │                             │
     │  smaller packets         │                             │
     │─────────────────────────▶│─────────────────────────────▶│
     │                                                        │
                                                        Packets arrive


  PMTUD BLACK HOLE — when ICMP Type 3 Code 4 is blocked:

  [Sender]                   [Router]                    [Destination]
     │                          │                             │
     │  IP packet, 1500 bytes   │                             │
     │  DF bit = 1              │                             │
     │─────────────────────────▶│                             │
     │                          │  Next link MTU = 1400       │
     │                          │  DROP the packet            │
     │                          │                             │
     │  ICMP Type 3 Code 4     │                             │
     │  ┌──────────────────┐   │                             │
     │  │  BLOCKED by FW!  │   │                             │
     │  └──────────────────┘   │                             │
     │                          │                             │
     │  Sender never learns     │                             │
     │  Max packet size.        │                             │
     │  Keeps retransmitting    │                             │
     │  1500-byte packets.      │                             │
     │  ALL are dropped.        │                             │
     │  TCP connection hangs.   │                             │
     │                          │                             │

  SYMPTOM: TCP handshake works (SYN/SYN-ACK are small, ~60 bytes).
  Connection establishes. But as soon as data transfer starts (large
  packets), everything stalls. This is pathologically hard to diagnose
  without knowing about PMTUD. The lab reproduces it exactly: a
  300-byte response arrives in 0.7 ms; a 300 kB response from the same
  server over the same path times out with 0 bytes received.

  Whose packets are big matters. Downloads fail when the SERVER's
  packets are too big, so the server must receive the ICMP message;
  uploads fail when the client's are. And each side's advertised MSS
  comes from its own route's MTU — including a path MTU it already
  learned — so a host that has cached "mtu 1400" toward a peer starts
  new connections with MSS 1360 and never triggers the problem. That is
  why the black hole comes and goes, and why the lab flushes both ends.
```

**MSS clamping**, the usual fix when you cannot get ICMP through, rewrites the MSS option in SYN packets crossing a router so both ends agree on small segments from the start: `iptables -t mangle -A FORWARD -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu`. `--clamp-mss-to-pmtu` uses the MTU of the interface the SYN leaves through, so put the rule on the router at the edge of the small link (a VPN gateway, the tunnel endpoint) or use `--set-mss` with an explicit value. Cloud VPN gateways and many routers clamp by default; WireGuard and Docker overlay setups often need it added.

### Type 11: Time Exceeded — The Backbone of Traceroute

```
ICMP TYPE 11 — TIME EXCEEDED

  Code 0: TTL expired in transit
  ────────────────────────────
  Generated by a router when it decrements a packet's TTL to 0.
  The router drops the packet and sends Type 11 Code 0 back to the sender.

  This is exactly how traceroute works:

  [Sender]         [Router 1]      [Router 2]       [Destination]
     │  TTL=1          │               │                  │
     │────────────────▶│               │                  │
     │                 │ TTL→0, DROP   │                  │
     │  ICMP TTL Exc   │               │                  │
     │◀────────────────│               │                  │
     │  (from Router1)                 │                  │
     │                                 │                  │
     │  TTL=2          │               │                  │
     │────────────────▶│  TTL=1        │                  │
     │                 │──────────────▶│                  │
     │                 │               │ TTL→0, DROP      │
     │  ICMP TTL Exc   │               │                  │
     │◀────────────────────────────────│                  │
     │  (from Router2)                                    │
     │                                                    │
     │  TTL=3          │               │                  │
     │────────────────▶│──────────────▶│─────────────────▶│
     │                                                    │
     │  Echo Reply (or UDP Port Unreachable)              │
     │◀───────────────────────────────────────────────────│
     │  Destination reached!

  Each hop reveals its IP address by sending the ICMP Time Exceeded.
  Traceroute increments TTL from 1 to N until the destination responds.

  Code 1: Fragment reassembly time exceeded
  ──────────────────────────────────────────
  Generated when a host receives some (but not all) fragments of a
  packet and the reassembly timer expires. Much rarer than Code 0.
```

### Type 5: Redirect

```
ICMP TYPE 5 — REDIRECT

  Generated by a router when it receives a packet AND the best route
  for that packet goes back out the SAME interface it arrived on.
  This means the sender could have sent directly to the better next hop.

  [Host A] ──packet for 10.0.2.50──▶ [Router 1]
                                          │
           Router 1's best route for      │
           10.0.2.0/24 is via Router 2,   │
           which is on the SAME subnet    │
           as Host A.                     │
                                          ▼
  [Host A] ◀──ICMP Redirect: "Send to Router 2 instead"
           │
           └──updates routing cache: 10.0.2.50 via Router 2

  Linux hosts accept redirects by default (and ignore them when they
  forward packets themselves). secure_redirects=1 would limit them to
  gateways already in the routing table, but the kernel applies that only
  when shared_media=0; both default to 1, so a default Linux host accepts
  a redirect to any on-link address (the lab's r3 is not a configured
  gateway, and the client accepts it). The kernel first resolves the new gateway with ARP, then
  stores a route exception for that one destination, visible as
  "cache <redirected>" in `ip route get` and expiring after ~300 s.
  Many hardened baselines (CIS benchmarks) disable accepting and sending
  redirects, because an on-link attacker could use them to steer traffic.

  sysctl net.ipv4.conf.all.accept_redirects        (default: 1)
  sysctl net.ipv4.conf.all.secure_redirects        (default: 1)
  sysctl net.ipv4.conf.all.send_redirects           (default: 1)

  Seeing redirects in a capture usually means a host's default gateway
  is not the best router for some destination on its own LAN — a design
  smell worth fixing with a proper route.
```

### ICMPv6

| ICMPv6 type | Meaning                               | Operational use                                            |
| ----------- | ------------------------------------- | ---------------------------------------------------------- |
| 1           | Destination Unreachable               | Inspect code and quoted flow                               |
| 2           | Packet Too Big                        | IPv6 PMTUD; routers do not fragment forwarded IPv6 packets |
| 3           | Time Exceeded                         | Hop-limit/reassembly failure                               |
| 4           | Parameter Problem                     | Header/option error                                        |
| 128 / 129   | Echo Request / Reply                  | Bounded reachability/RTT probe                             |
| 133 / 134   | Router Solicitation / Advertisement   | Router/prefix discovery and default-route configuration    |
| 135 / 136   | Neighbor Solicitation / Advertisement | Neighbor resolution, reachability, DAD                     |

ICMPv6 is not optional decoration: indiscriminate blocking can break address
configuration, neighbor discovery, and PMTUD. Apply validated, link-appropriate
policy. The [IPv6 lab](../labs/netlab/ipv6.json) (Module 5) captures these mechanisms without
external IPv6 connectivity. Types/codes are different from ICMPv4.

---

## 7.4 Traceroute and Path Analysis

### What This Is

Traceroute maps the hop-by-hop path between your host and a destination by exploiting TTL expiry. It sends packets with incrementing TTL values (1, 2, 3, ...) and records the ICMP Time Exceeded responses from each intermediate router. The result is a list of every router (hop) in the path, along with latency measurements for each.

### How Traceroute Works — Three Modes

Different traceroute implementations use different probe types. The mode matters because firewalls treat each type differently.

```
TRACEROUTE MODES

  Mode 1: UDP (default on Linux — traceroute)
  ────────────────────────────────────────────
  Sends UDP packets to high destination ports, starting at 33434 and
  adding one for every probe, so each reply can be matched to its probe.
  Each hop returns ICMP Time Exceeded.
  The destination returns ICMP Port Unreachable (Type 3 Code 3)
  because nothing is listening on that port.
  That Port Unreachable is how traceroute knows it reached the end.
  No root needed. A destination firewall that drops unknown UDP makes
  the last hop show * even though the host is up.


  Mode 2: ICMP (default on Windows — tracert; Linux: traceroute -I)
  ─────────────────────────────────────────────────────────────────
  Sends ICMP Echo Request packets with increasing TTL.
  Each hop returns ICMP Time Exceeded.
  The destination returns ICMP Echo Reply.
  More likely to be blocked than UDP mode because some devices
  rate-limit or drop ICMP Echo.


  Mode 3: TCP (Linux: traceroute -T -p 443, needs root)
  ──────────────────────────────────────────────────────
  Sends TCP SYN packets to a specific port (e.g., 443).
  Each hop returns ICMP Time Exceeded.
  The destination returns TCP SYN-ACK (or RST); traceroute then sends
  an RST so no connection is left half-open.
  Most likely to succeed through firewalls because port 443 is
  typically allowed. Best mode for tracing to web servers — and it
  follows the same ECMP/load-balancer path as real HTTPS traffic to
  that port more often than UDP or ICMP probes do.

  By default Linux traceroute sends 3 probes per hop and up to 16
  probes at once (-N 16). Bursts like that trip routers' ICMP rate
  limits, which shows up as random '*'s; -N 1 sends one at a time.
```

### Reading Traceroute Output

```bash
traceroute -n 8.8.8.8
```

```
traceroute to 8.8.8.8 (8.8.8.8), 30 hops max, 60 byte packets
 1  10.0.1.1       0.5 ms   0.4 ms   0.3 ms
    ↑               ↑        ↑        ↑
    hop 1 IP    3 probes per hop, each with its own RTT
    (your gateway)

 2  203.0.113.1    2.1 ms   1.9 ms   2.0 ms
    ↑
    ISP's first router

 3  198.51.100.5   5.4 ms   5.3 ms   5.5 ms

 4  * * *
    ↑
    Asterisks: this router did NOT send ICMP Time Exceeded.
    Reasons: ICMP rate-limited, ICMP filtered, or router configured
    to not respond. This is NOT necessarily a problem.

 5  216.239.48.9   10.8 ms  10.7 ms  10.9 ms

 6  8.8.8.8        12.1 ms  11.9 ms  12.0 ms
    ↑
    Destination reached — received ICMP Port Unreachable (UDP mode)
    or Echo Reply (ICMP mode) or TCP SYN-ACK/RST (TCP mode)
```

### Interpreting Results

```
PATTERN                    WHAT IT MEANS
─────────────────────────  ──────────────────────────────────────────
Steady increase in RTT     Consistent with longer paths, not a per-link measurement
Big jump at one hop        Could be forward/return paths or reply processing;
                           a difference of RTTs is not one-way link delay
* * * at one hop           ICMP is filtered at that hop. If the next
                           hops respond normally, this is NOT a problem.
* * * from hop N onward    No replies within the probe timeout. Filtering,
                           rate limits, return-path loss, or failure are possible.
Two IPs alternating        Routing loop — packets bounce between two
(A, B, A, B …)             routers until TTL expires. (One IP repeated
                           at consecutive hops is usually something else:
                           a device that does not decrement TTL, or
                           replies from a different interface.)
High RTT at one hop only   That specific router has high processing
(next hops normal)         delay. NOT a problem for through-traffic.
                           Routers deprioritize ICMP reply generation.
High RTT at one hop AND    A useful correlation, not proof of the exact faulty
all subsequent hops        link; compare destination traffic and reverse evidence.
```

### Critical Concept: Asterisks Are Not Always a Problem

```
 * * * IS NOT ALWAYS A FAILURE

  Hop 4 shows * * *. But hops 5 and 6 respond with normal RTT.

  1  10.0.1.1      0.5 ms
  2  203.0.113.1   2.1 ms
  3  198.51.100.5  5.4 ms
  4  * * *                    ← Router exists. It just doesn't send ICMP.
  5  216.239.48.9  10.8 ms   ← Traffic is flowing through hop 4 just fine.
  6  8.8.8.8       12.1 ms   ← Destination reached.

  The router at hop 4 is FORWARDING packets normally. It simply does
  not generate ICMP Time Exceeded messages (many routers are configured
  this way to reduce CPU load or for security reasons), or rate-limits
  them. Cloud networks are an extreme case: the virtual router that
  forwards a VM's packets is not a hop in the usual sense, so a trace
  from a VM may show '*' for the first hops, internal addresses that
  are not publicly routable, or a jump straight to the provider's edge.
  None of that tells you about your traffic's health. The lab reproduces a silent hop.

  WHEN * * * NEEDS APPLICATION-PATH COMPARISON:

  1  10.0.1.1      0.5 ms
  2  203.0.113.1   2.1 ms
  3  * * *
  4  * * *                    ← ALL subsequent hops are asterisks
  5  * * *                    ← No replies observed; location/cause unknown
  ...                            Test the actual application flow too.
  30 * * *                    ← Destination never reached.
```

### Asymmetric Paths

```
ASYMMETRIC PATHS — WHY TRACEROUTE CAN BE MISLEADING

  The forward path (Your Host → Destination) may be completely
  different from the return path (Destination → Your Host).

  Forward:  [You] → [ISP-A] → [IXP] → [ISP-B] → [Destination]
  Return:   [Destination] → [ISP-C] → [IXP] → [ISP-A] → [You]

  Traceroute shows you the forward path routers (from their ICMP replies).
  But the RTT you measure includes BOTH directions:
  - Forward: probe travels from you to hop N
  - Return: ICMP reply travels from hop N back to you

  If the return path is congested but the forward path is clear,
  you'll see high RTT at certain hops even though the forward path
  is healthy. This makes per-hop RTT measurements unreliable for
  pinpointing WHICH direction has the problem.
```

### mtr — The Superior Tool

`mtr` combines traceroute and ping into a single tool. It continuously sends probes and builds real-time statistics per hop: packet loss percentage, average RTT, best RTT, worst RTT, and standard deviation.

```bash
# Run mtr with 100 cycles, no DNS resolution, report mode
mtr -n -c 100 --report 8.8.8.8
```

```
HOST:                      Loss%  Snt   Last   Avg   Best  Wrst  StDev
 1. 10.0.1.1                0.0%  100    0.5   0.5   0.3   1.2   0.1
 2. 203.0.113.1             0.0%  100    2.1   2.0   1.8   3.5   0.2
 3. 198.51.100.5            0.0%  100    5.3   5.5   5.1   8.2   0.4
 4. ???                    100.0%  100    0.0   0.0   0.0   0.0   0.0
 5. 216.239.48.9            0.0%  100   10.8  10.9  10.5  13.1   0.3
 6. 8.8.8.8                 0.0%  100   12.0  12.2  11.8  15.0   0.4
                                         ↑     ↑     ↑     ↑     ↑
                                       latest  avg  best  worst  jitter

Reading this:
- Hop 4: 100% loss means the router doesn't answer ICMP. But all hops
  after it show 0% loss, so hop 4 is forwarding traffic normally.
- Hop 6 (destination): 0% loss, ~12ms average. Healthy connection.
- If hop 3 showed 5% loss AND hops 4-6 also showed 5% loss,
  the problem is AT or BEFORE hop 3 — the loss propagates forward.
```

**Rate limiting, measured.** Every Linux router limits the ICMP errors it generates (`net.ipv4.icmp_ratelimit`, 1000 ms per destination with a small burst, for the types in `icmp_ratemask` — including Time Exceeded and Destination Unreachable). The lab runs mtr twice over the same healthy path: at 1 probe per second every hop shows 0.0% loss; at 10 per second (`-i 0.1`) both routers show 82% "loss" while the destination shows 0.0%. Nothing was dropped — the routers simply declined to answer most probes. Hardware routers do the same in their control plane.

**Key interpretation:** isolated intermediate reply loss with successful downstream
probes is consistent with control-plane filtering/rate limiting. Loss continuing
downstream is worth investigating, but does not prove a particular hop or link is
faulty. Probe paths, return paths, ECMP, sampling, and response policies can differ.

### traceroute vs tracepath vs mtr

```
TOOL          WHAT IT DOES                                  WHEN TO USE
────────────  ────────────────────────────────────────────  ─────────────────────
traceroute    Sends probes with increasing TTL.              General-purpose path
              Requires root for ICMP/TCP modes.              mapping. One-shot.
              Modes: UDP (default), ICMP (-I), TCP (-T).

tracepath     Like traceroute but automatically discovers    MTU debugging. Does not
              path MTU. No root required (uses UDP).         require root access.
              Prints "pmtu N" where it learns a smaller
              MTU, and "Resume: pmtu N" for the path.
              It also caches that MTU in the kernel, which
              changes later TCP behavior (lab check 23).

mtr           Continuous traceroute + ping combined.         Best for production
              Shows per-hop statistics over many probes.     diagnosis. Run for 100+
              Identifies where loss accumulates.             cycles for reliable data.
              ICMP probes by default; -u UDP, -T TCP (-P     Share mtr reports in
              port). Interactive by default, --report for    tickets, with the probe
              batch. Intervals under 1 s need root.          mode and interval.
```

---

## Hands-On Lab: ICMP, Ping, and Traceroute

### Lab Goals

- Watch traceroute work in all three modes and read the probes and replies field by field
- See a silent hop and ICMP rate limiting produce "loss" that is not loss
- Make every important Destination Unreachable code happen, and see what the application reports for each
- Receive an ICMP Redirect and watch the client act on it
- Build a PMTUD black hole — small responses work, large ones hang — and fix it with MSS clamping

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iproute2`, `iputils-ping`, `iputils-tracepath`, `traceroute`, `mtr-tiny`, `tcpdump`, `iptables`, `curl`, `dnsutils` (for `dig`)
- Parts 1–2 are offline. Part 3 needs internet access; Part 4 needs Docker.

---

### Lab Part 1: Run the ICMP Lab

[`icmp.json`](../labs/netlab/icmp.json) builds a small routed network:

```
  LAN 198.18.7.0/24 (bridge)                 transit, MTU 1400            server LAN
 client .1 ── r1 .254 ═══════════════════════ r2 .2  ─────────────── server 198.19.7.2
         │    (eth1 198.18.8.1)                 (eth1 198.19.7.254)    labserver :8080 (300 kB)
         │                                                                       :8081 (300 B)
         └─── r3 .252 ── host2 198.19.8.2      r2 firewall: REJECT :8082 (default)
              (r1's route to 198.19.8.0/24          REJECT :8083 (icmp-admin-prohibited)
               points at r3: a redirect waits)
```

r1 has no default route, so anything outside the lab is "net unreachable" there.

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/icmp.json      # about 50 s; 32 checks
```

Excerpts from a real run follow (timestamps and IDs trimmed; `-v` IP header lines shortened where noted).

**Traceroute, UDP mode.** One probe at a time (`-q 1 -N 1`) to keep the capture readable:

```
check 1/32: traceroute (UDP): …
  |  1  198.18.7.254  0.196 ms
  |  2  198.18.8.2  0.084 ms
  |  3  198.19.7.2  0.079 ms
  | IP (ttl 1, proto UDP (17), length 60)
  |     198.18.7.1.51067 > 198.19.7.2.33434: UDP, length 32
  | IP (tos 0xc0, ttl 64, proto ICMP (1), length 88)
  |     198.18.7.254 > 198.18.7.1: ICMP time exceeded in-transit, length 68
  | 	IP (ttl 1, proto UDP (17), length 60)
  |     198.18.7.1.51067 > 198.19.7.2.33434: UDP, length 32
  | IP (ttl 2, …) 198.18.7.1.57304 > 198.19.7.2.33435: UDP, length 32
  | IP (tos 0xc0, ttl 63, …) 198.18.8.2 > 198.18.7.1: ICMP time exceeded in-transit, length 68
  | IP (ttl 3, …) 198.18.7.1.57269 > 198.19.7.2.33436: UDP, length 32
  | IP (tos 0xc0, ttl 62, …) 198.19.7.2 > 198.18.7.1: ICMP 198.19.7.2 udp port 33436 unreachable, length 68
```

Read it line by line:

- Probe TTLs go 1, 2, 3 and destination ports 33434, 33435, 33436: one port per probe, so each reply identifies its probe.
- The indented lines are the quote — the original probe's IP header and UDP header (Linux quotes all of it), with the TTL *as it arrived* at the router: 1.
- Each error arrives with a TTL that tells you how far away its sender is: r1's 64, r2's 63 (r1 decremented it on the way back), the server's 62.
- `tos 0xc0` is CS6, the marking Linux gives the errors it generates.
- The last hop answers with **port unreachable**, not time exceeded: that is how traceroute knows it arrived.

**ICMP and TCP modes** (checks 2–3) produce the same three hops. In ICMP mode the destination's answer is an `echo reply`; in TCP mode the server answers the SYN to port 8080 with `Flags [S.]`, and traceroute immediately sends `Flags [R]` so the server does not keep a half-open connection. In TCP-mode captures you may also see `cksum … (incorrect -> …)` on packets from this host: checksum offload, as in Module 3, not corruption.

**A silent hop is not a broken hop.** Check 5 makes r2 drop its own Time Exceeded messages (what many operators configure):

```
check 6/32: hop 2 now shows '*', yet hop 3 answers: the router still forwards
  |  1  198.18.7.254  0.051 ms
  |  2  *
  |  3  198.19.7.2  0.040 ms
```

**ICMP rate limiting looks like loss.**

```
check 8/32: 10 probes a second: both routers 'lose' most replies (ICMP rate limiting), the destination loses none
  | HOST: vm                          Loss%   Snt   Last   Avg  Best  Wrst StDev
  |   1.|-- 198.18.7.254              82.0%    50    0.1   0.1   0.1   0.1   0.0
  |   2.|-- 198.18.8.2                82.0%    50    0.1   0.1   0.1   0.1   0.0
  |   3.|-- 198.19.7.2                 0.0%    50    0.1   0.1   0.1   0.2   0.0
check 9/32: one probe a second: no loss anywhere
  |   1.|-- 198.18.7.254               0.0%    10    0.1   0.1   0.1   0.1   0.0
  |   2.|-- 198.18.8.2                 0.0%    10    0.1   0.1   0.1   0.2   0.0
  |   3.|-- 198.19.7.2                 0.0%    10    0.1   0.1   0.1   0.1   0.0
```

Same network, same moment, nothing dropped. The 82% is the routers' `icmp_ratelimit` (about one Time Exceeded per second per destination after a short burst): 50 probes in 5 s, 9 answered. Echo replies from the destination are not limited the same way, so its column stays at 0.0%. This is the single most common misreading of an mtr report in tickets.

**Destination Unreachable, every code — and what the application says.**

```
check 10/32: no host 198.19.7.99 behind r2: after 3 s of ARP, r2 returns Host Unreachable (type 3 code 1)
  | From 198.18.8.2 icmp_seq=1 Destination Host Unreachable
  | 20:46:24.268861 … 198.18.7.1 > 198.19.7.99: ICMP echo request
  | 20:46:27.343930 … 198.18.8.2 > 198.18.7.1: ICMP host 198.19.7.99 unreachable, length 92
check 11/32: r1 has no route to 203.0.113.0/24 and no default: Net Unreachable (type 3 code 0)
  | From 198.18.7.254 icmp_seq=1 Destination Net Unreachable
  | … 198.18.7.254 > 198.18.7.1: ICMP net 203.0.113.5 unreachable, length 92
check 12/32: an application connecting there gets ENETUNREACH within milliseconds, from the router's ICMP
  | connect … ✗ dial tcp 203.0.113.5:80: connect: network is unreachable
  | FAILED during: connect (after 0.4ms)
check 13/32: to 198.19.7.99 it gets EHOSTUNREACH, but only after r2's 3 s of ARP
  | connect … ✗ dial tcp 198.19.7.99:80: connect: no route to host
  | FAILED during: connect (after 3079.2ms)
check 14/32: no DNS server on 198.19.7.2: UDP gets Port Unreachable (type 3 code 3), which dig reports as 'connection refused'
  | ;; communications error to 198.19.7.2#53: connection refused
  | … 198.19.7.2 > 198.18.7.1: ICMP 198.19.7.2 udp port 53 unreachable, length 89
check 15/32: r2's plain REJECT rule answers a TCP SYN with ICMP port unreachable: the client sees 'Connection refused', exactly like a closed port
  | * connect to 198.19.7.2 port 8082 from 198.18.7.1 port 45534 failed: Connection refused
  | … 198.18.8.2 > 198.18.7.1: ICMP 198.19.7.2 tcp port 8082 unreachable, length 68
check 16/32: REJECT with icmp-admin-prohibited (type 3 code 13): the client sees 'No route to host'
  | * connect to 198.19.7.2 port 8083 from 198.18.7.1 port 54920 failed: No route to host
  | … 198.18.8.2 > 198.18.7.1: ICMP host 198.19.7.2 unreachable - admin prohibited filter, length 68
```

Three lessons hide in these lines. **The sender of the ICMP message is the evidence**: a "Connection refused" sent by `198.18.8.2` (r2) came from a firewall, not from the server — curl alone cannot tell you that, a capture can. **Timing is evidence**: net unreachable arrived in 0.4 ms (a routing decision), host unreachable in 3,079 ms (three ARP attempts at the last router). **`No route to host` rarely means a missing route**: here it was a firewall rule. Checks 12 and 13 use the course's [`phases`](../labs/go/cmd/phases) tool, whose explanation of each errno now names these ICMP causes.

**The same path, traced by Go.** Checks 17–19 run [`gotrace`](../labs/go/cmd/gotrace), a traceroute in under 500 lines of standard-library Go (comments included): it opens UDP or TCP sockets with `IP_TTL` set, reads ICMP on a raw socket, and matches each error to its probe by the addresses and ports in the quote — the parsing is the "quoted IP header + 8 bytes" rule from 7.1, in code. It sends one probe at a time, so router rate limits rarely bite, and it explains every reply:

```
check 18/32: gotrace to the missing host: r2's !H arrives about 3 s after the probe
  |  1  198.18.7.254  0.073 ms   (time exceeded: TTL reached 0 at this router)
  |  2  198.18.8.2  0.023 ms   (time exceeded: TTL reached 0 at this router)
  |  3  198.18.8.2  1854.513 ms !H   (host unreachable: the last router could not ARP for the host)
check 19/32: gotrace in TCP mode into r2's admin-prohibited rule: !X at hop 3, sent by r2
  |  3  198.18.8.2  0.194 ms !X   (administratively prohibited: a firewall rejected the probe)
```

Note hop 3's address is r2, not the destination — the hop where the trace ends is whoever answered, and the `!H`/`!X` mark is the reason. The !H took 1.9–3.1 s across runs (r2's ARP attempts, sometimes already partly done by an earlier check); that is why traceroute's default wait is 5 s. `-fixed` keeps one destination port for every probe so ECMP routers hash every probe onto the same path (the idea behind Paris traceroute).

**Redirects.** r1's route to 198.19.8.0/24 points back out the interface the packet came in on, to r3 on the client's own LAN:

```
check 20/32: r1 forwards to r3 on the same LAN it came from, so it tells the client: ICMP Redirect (type 5); …
  | From 198.18.7.254: icmp_seq=1 Redirect Host(New nexthop: 198.18.7.252)
  | 64 bytes from 198.19.8.2: icmp_seq=1 ttl=63 time=0.146 ms
  | From 198.18.7.254: icmp_seq=2 Redirect Host(New nexthop: 198.18.7.252)
  | 64 bytes from 198.19.8.2: icmp_seq=2 ttl=63 time=0.113 ms
  | 198.19.8.2 via 198.18.7.252 dev eth0 src 198.18.7.1 uid 0
  |     cache <redirected> expires 299sec
```

r1 still forwards each packet (the pings succeed), and tells the client about the better next hop. The kernel ARPs for r3, stores a 300-second exception for that one destination, and a new ping run afterwards goes straight to r3 with no redirects (try it in Part 2).

**PMTUD working, then black-holed, then fixed.** The r1–r2 link has an MTU of 1400; the server's LAN has 1500. Downloads send big packets from server to client, so the server is the one that must hear about the small link:

```
check 21/32: PMTUD working: 300 kB from server to client; …
  | 200 300000 bytes in 0.001053s
  | IP (tos 0xc0, ttl 64, …, proto ICMP (1), length 576)
  |     198.19.7.254 > 198.19.7.2: ICMP 198.18.7.1 unreachable - need to frag (mtu 1400), length 556
  | 	IP (ttl 64, …, flags [DF], proto TCP (6), length 4148)
  |     198.19.7.2.8080 > 198.18.7.1.53214: Flags [P.], …, length 4096: HTTP, length: 4096
check 22/32: the server cached the path MTU toward the client
  | 198.18.7.1 via 198.19.7.254 dev eth0 src 198.19.7.2 uid 0
  |     cache expires 599sec mtu 1400
```

The quoted packet is 4,148 bytes: the server handed its NIC a TSO "super-packet" (Module 3), and r2 judged it by the size of the segments it would become. Then r2 stops sending Fragmentation Needed, both ends forget what they learned, and:

```
check 27/32: a small response still works: every packet fits
  | 200 300 bytes in 0.000947s
check 28/32: a large one hangs: handshake fine, full-size data segments vanish
  | curl: (28) Operation timed out after 5002 milliseconds with 0 bytes received
  | 000 0 bytes in 5.002395s
```

Same server, same path, same moment. Finally r1 clamps the MSS of SYNs leaving through the small link:

```
check 32/32: the SYN arrives with MSS 1360 (1400 - 40), so the server never sends a packet too big for the path
  | 200 300000 bytes in 0.001316s
  | IP 198.18.7.1.33532 > 198.19.7.2.8080: Flags [S], …, options [mss 1360,sackOK,TS val … ,nop,wscale 10], length 0
```

Why "both ends forget"? Writing this lab, the black hole first refused to appear: an earlier `tracepath` from the client had cached `mtu 1400` toward the server, and Linux derives the MSS it *advertises* from that route — the client's next SYN said `mss 1360` instead of 1460, the server sent small segments, and the download worked. The PMTU cache on either host can hide or reveal the problem, which is why black holes are intermittent in production.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/icmp.json
alias lab='sudo ./bin/netlab exec ../netlab/icmp.json'
```

1. **Default traceroute bursts.** `lab client -- traceroute -n 198.19.7.2` (3 probes per hop, up to 16 at once) three times in a row. The first run is clean; the next ones can show rows of `* * *` and the server appearing at hop 8 instead of 3, because the server rate-limits its Port Unreachable replies too and traceroute keeps probing further. A path cannot get longer in two seconds — this is rate limiting. Compare `-N 1 -q 1`.
2. **Redirect, applied.** `lab client -- ping -c 2 198.19.8.2` twice. The second run shows no redirects; `lab client -- ip route get 198.19.8.2` shows `<redirected>`. Then `lab client -- sysctl -w net.ipv4.conf.all.accept_redirects=0 net.ipv4.conf.eth0.accept_redirects=0`, `ip route flush cache`, and ping again: r1 still sends a (rate-limited) redirect, but `ip route get` shows no `<redirected>` exception and the traffic keeps going through r1.
3. **The MSS follows the cache.** `lab client -- ip route flush cache`, capture SYNs on the client, `curl` the server; then run `tracepath` once and repeat. (`mss 1460` before, `mss 1360` after.)
4. **Find a path MTU with ping.** `for s in 1472 1400 1372 1300; do lab client -- ping -c1 -W1 -M do -s $s 198.19.7.2 | grep -E 'bytes from|Frag needed|too long'; done`. (1372 + 28 = 1400 is the largest that fits.)
5. **Make the black hole visible from the server.** With the black hole in place (checks 24–26), capture on r2's `eth1` during the big download: retransmissions of the same large segment, and no ICMP leaving r2.

`sudo ./bin/netlab down ../netlab/icmp.json` removes the lab.

---

### Lab Part 3 (Internet Required): The Real Path

```bash
ping -c 5 1.1.1.1                                  # RTT, TTL, loss
ping -O -D -c 20 -i 0.5 1.1.1.1                    # timestamped, missing replies printed
traceroute -n 1.1.1.1                              # UDP
sudo traceroute -n -I 1.1.1.1                      # ICMP
sudo traceroute -n -T -p 443 1.1.1.1               # TCP to the port real traffic uses
tracepath -n 1.1.1.1                               # path MTU, no root
mtr -n -r -c 100 1.1.1.1                           # 1 probe/s: compare with -i 0.2 (root)
```

Compare the three traceroute modes hop by hop; differences are policy, not health. In the mtr report, look at the destination row first: if it shows 0% loss, intermediate "loss" is rate limiting or filtering. Estimate the minimum possible RTT to a target from distance (≥ 1 ms per 100 km of fiber) and compare it with what ping measures; the difference is routing detours plus queuing.

---

### Lab Part 4 (Docker): ICMP Between Containers

```bash
docker network create --subnet=172.30.0.0/24 icmp-lab
docker run -d --name icmp-a --network icmp-lab --ip 172.30.0.10 nicolaka/netshoot sleep 3600
docker run -d --name icmp-b --network icmp-lab --ip 172.30.0.20 nicolaka/netshoot sleep 3600
docker exec icmp-a ping -c 3 172.30.0.20                     # ttl=64: same bridge, no router
docker exec icmp-a ping -c 3 1.1.1.1                         # leaves via the host: TTL counts the host as a hop
docker exec icmp-a traceroute -n -m 3 1.1.1.1                # hop 1 is the bridge gateway 172.30.0.1
docker rm -f icmp-a icmp-b && docker network rm icmp-lab
```

Container images often lack `ping`, or include BusyBox `ping` with different flags; unprivileged ICMP needs `net.ipv4.ping_group_range` or the `NET_RAW` capability, which Docker grants by default but many Kubernetes security policies drop — "ping: permission denied" inside a pod is a policy result, not a network one.

---

## Real-World Troubleshooting Scenario

### Scenario: "Large File Uploads Hang But Small API Calls Work Fine"

**The situation:**

In this synthetic case, a SaaS company deploys a file upload service behind an AWS Network Load Balancer. The service accepts uploads over HTTPS. Small requests (JSON API calls under 1KB) work perfectly. But file uploads larger than ~1400 bytes consistently hang after the TCP handshake completes. The connection establishes, the TLS handshake succeeds, and then... the upload stalls. After 30 seconds, the client times out. The development team suspects a bug in their upload handler. The networking team says "ping works, so the network is fine."

**The root cause: PMTUD black hole.**

The path between the clients and the NLB traverses a VPN tunnel between the company's office and the AWS VPC. The VPN adds 58 bytes of encapsulation overhead, reducing the effective MTU from 1500 to 1442. A firewall between the VPN gateway and the office clients drops all ICMP traffic — including Type 3 Code 4 (Fragmentation Needed), which the VPN gateway generates toward the clients whose packets are too big. (Uploads fail because the *clients'* packets are big; downloads through the same path may work, because those large packets travel the other way and the gateway's errors to the NLB side are not filtered.)

**The mechanics of the failure:**

1. TCP SYN (60 bytes) → fits in 1442 MTU → arrives successfully
2. TLS ClientHello (~300 bytes) → fits → arrives
3. Actual file data: TCP sends a full 1460-byte MSS segment. With IP+TCP headers: 1500 bytes
4. The VPN gateway must encapsulate: 1500 + 58 = 1558 bytes. This exceeds the tunnel MTU
5. The DF bit is set (Linux, Windows, and macOS set DF on TCP by default). The VPN gateway cannot fragment
6. The VPN gateway drops the packet and sends ICMP Type 3 Code 4 back
7. **The firewall drops the ICMP message.** The sender never receives it
8. The TCP sender retransmits the same 1500-byte segment with exponential backoff. Same result each time. (Linux can recover by itself only if `net.ipv4.tcp_mtu_probing` is enabled; it is 0 by default. Windows has its own black-hole detection.)
9. The application's timeout (30 s here) fires long before TCP gives up (Linux: `tcp_retries2`, about 15 minutes)

**How the tools from this lab uncover it:**

**Step 1 — Prove it is MTU-related with ping:**

```bash
# From the client, test with different payload sizes through the VPN
ping -c 3 -s 1400 -M do <upload-service-ip>    # Works (1400 + 28 = 1428 < 1442)
ping -c 3 -s 1420 -M do <upload-service-ip>    # Fails or hangs (1420 + 28 = 1448 > 1442)
```

The point where ping switches from working to failing reveals the path MTU.

**Step 2 — Verify ICMP Type 3 Code 4 is being suppressed:**

```bash
# Capture ICMP on the client while sending the large ping
sudo tcpdump -i any -nn -v 'icmp and icmp[icmptype] == 3' &
ping -c 3 -s 1420 -M do <upload-service-ip>
```

If you see no ICMP Destination Unreachable message, the "Fragmentation Needed" response is being blocked somewhere in the path.

**Step 3 — Watch a real upload stall:**

```bash
sudo tcpdump -i eth0 -nn 'host <upload-service-ip> and (tcp or icmp)'
# start the upload; you will see the handshake, then the same full-size
# segment (length 1448 with timestamps) retransmitted at growing intervals,
# and no ICMP coming back
```

`tracepath -n <upload-service-ip>` from the client is the complementary test: where ICMP is allowed it prints `pmtu 1442` at the VPN hop; here it never learns a smaller MTU and its probes simply stop getting answers past that hop.

**Step 4 — The fix:**

- **Immediate:** Clamp MSS on the VPN gateway to 1400 (1442 MTU - 20 IP - 20 TCP = 1402, round down to 1400). This tells TCP endpoints to use smaller segments: `iptables -t mangle -A FORWARD -o tun0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1400` (or `--clamp-mss-to-pmtu` when the rule matches SYNs leaving through the tunnel interface, as in the lab's check 29)
- **Also immediate:** Allow ICMP Type 3 through the security group. This lets PMTUD work as designed.
- **Long-term:** Permit necessary validated ICMP error feedback with appropriate
  state/type policy; it is useful infrastructure, not exempt from security analysis.

**Time to diagnosis:** the signature (small works, large hangs, handshake fine) is recognizable in minutes once you know it; the lab's checks 27–28 reproduce it exactly.

---

## Common Failure Patterns

| Symptom                                    | ICMP-Related Cause                                      | How to Confirm                                                               | Fix                                            |
| ------------------------------------------ | ------------------------------------------------------- | ---------------------------------------------------------------------------- | ---------------------------------------------- |
| Ping works, large transfers hang           | PMTUD black hole (Type 3 Code 4 blocked)                | `ping -s 1400 -M do <IP>` — find where it fails                              | Allow ICMP Type 3, clamp MSS                   |
| "Destination Host Unreachable"             | ARP failed: `From` your IP = your host; `From` a router = its last hop (~3 s) | Read the `From` address; `ip neigh` there shows FAILED              | Verify target is up and on the correct subnet  |
| "Connection refused" but the service listens | A firewall REJECT (ICMP port unreachable or RST)      | Capture: the ICMP/RST comes from a router or firewall address, not the server | Fix the firewall rule                          |
| "No route to host" though the route exists | ICMP admin/host prohibited from a firewall, or ARP failure | Capture for ICMP type 3 code 10/13 and its source                        | Fix the rule, or the address/VLAN              |
| DNS lookups fail instantly with "connection refused" | No DNS server listening: ICMP port unreachable for UDP 53 | `dig @server` shows "communications error … connection refused"  | Start or fix the DNS service                   |
| "Network Unreachable"                      | No route to destination at some router                  | `ip route get <IP>` — check local routes; `traceroute` to see where it stops | Add the missing route                          |
| `ping` times out, `nc -zv` succeeds        | ICMP is filtered but TCP is allowed                     | `nc -zv <host> 443` works; host is alive                                     | (Not a real problem — ICMP is just filtered)   |
| Traceroute shows \* \* \* everywhere       | UDP/ICMP probes are all dropped                         | `traceroute -T -p 443 <host>` — try TCP mode                                 | Use TCP traceroute                             |
| mtr shows loss at hop N, all later hops OK | Hop N rate-limits ICMP (normal behavior)                | Loss doesn't propagate; slower probing (`-i 1`) reduces it                   | Not a problem — ignore                         |
| mtr shows loss at hop N AND all later hops | Genuine packet loss at or before hop N                  | Loss propagates to all subsequent hops                                       | Contact the network owner at hop N             |
| traceroute shows same two IPs alternating  | Routing loop                                            | TTL expires, packets bounce forever                                          | Fix the routing configuration causing the loop |
| High latency at one mtr hop only           | Router deprioritizes ICMP (control plane vs data plane) | Later hops show normal latency                                               | Not a real problem for transit traffic         |

---

## Summary

- ICMP is a Layer 3 control protocol (IP Protocol 1) that provides error reporting and diagnostics. It is not a transport protocol — it carries no application data.
- `ping` uses ICMP Echo Request (Type 8) and Echo Reply (Type 0) to test Layer 3 reachability and measure RTT. Ping failure does not prove a host is down — ICMP may be filtered — and ping success does not prove a port or large packets get through. Always cross-check with a TCP probe.
- ICMP errors become the errors applications print: port unreachable → `Connection refused` (UDP, and TCP behind a default iptables REJECT); host/admin prohibited → `No route to host`; net unreachable → `Network is unreachable`. The source address of the ICMP message says who answered, and its timing (instant vs ~3 s) says why.
- ICMP Type 3 (Destination Unreachable) is the most important error message. Code 0 = no route, Code 1 = host unreachable, Code 3 = port unreachable (UDP), Code 4 = fragmentation needed (PMTUD), Code 13 = administratively prohibited (firewall).
- **Never block ICMP Type 3 Code 4.** Doing so creates PMTUD black holes — TCP connections establish but stall on large data transfers. This is one of the hardest production issues to diagnose.
- `traceroute` exploits ICMP Time Exceeded (Type 11) by sending probes with incrementing TTL values. Three modes exist: UDP (default Linux), ICMP (default Windows), TCP (best for firewalled paths).
- Asterisks mean no response arrived in time. Even a missing tail in several probe
  modes does not uniquely locate application traffic loss at the last visible hop.
- mtr shows probe-response statistics. Routers rate-limit the ICMP errors they generate, so intermediate "loss" grows with the probe rate (the lab: 0% at 1/s, 82% at 10/s, on a path that dropped nothing). Read the destination row first, and correlate persistent downstream loss with end-to-end traffic and endpoint captures before attributing it to a network owner.
- PMTUD black holes hit whichever side sends the big packets, come and go with each host's cached path MTU, and are fixed by letting Type 3 Code 4 through or by clamping the MSS at the small link.
- Asymmetric paths mean traceroute shows the forward path, but RTT measurements include the return path. High RTT at a hop may be caused by return path congestion, not forward path congestion.

## What's Next

Module 8 covers Routing — how packets find their way across networks. You have now seen the IP header's TTL field exploited by traceroute, and ICMP error messages that report routing failures. The next module explains the routing table itself: how `ip route` works, what longest prefix match means, static vs. dynamic routing, NAT, and how routers make the hop-by-hop forwarding decisions that IP depends on.

---

## Quick Reference Card

```
MODULE 7: ICMP, PING, AND TRACEROUTE — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

ICMP BASICS
  Protocol:     IP Protocol 1 (not TCP or UDP)
  Layer:        3 (Network) — a control protocol, not a data protocol
  No ports:     Filtered by Type/Code, not port numbers
  Key rule:     NEVER block ICMP Type 3 (breaks PMTUD and error reporting)

KEY ICMP TYPES
  Type 0     Echo Reply               Response to ping
  Type 3     Destination Unreachable   Error — packet could not be delivered
    Code 0     Network Unreachable     No route to network
    Code 1     Host Unreachable        Host down or ARP failed
    Code 3     Port Unreachable        No UDP listener (TCP uses RST instead)
    Code 4     Frag Needed + DF Set    PMTUD signal — CRITICAL, never block
    Code 13    Admin Prohibited        Firewall explicitly blocked it
  Type 5     Redirect                  "Use a better next hop"
  Type 8     Echo Request              Ping probe
  Type 11    Time Exceeded             TTL reached 0 — used by traceroute
    Code 0     TTL expired in transit  Normal traceroute reply
    Code 1     Fragment reassembly     Fragments didn't all arrive in time

ICMP → WHAT THE APPLICATION SAYS (Linux)
  3/3  port unreachable    ECONNREFUSED  "Connection refused" (UDP; TCP via REJECT)
  3/1, 3/10, 3/13          EHOSTUNREACH  "No route to host"
  3/0  net unreachable     ENETUNREACH   "Network is unreachable"
  3/4  frag needed         (none)        path MTU lowered; if blocked: a hang

ESSENTIAL COMMANDS
  ping -c 5 <host>               Basic reachability + RTT
  ping -O -D -c 20 <host>        Print missing replies, timestamp lines
  ping -s 1472 -M do <host>      Test PMTUD (max for 1500 MTU)
  ping -c 10 -i 0.2 <owned-ip>  Bounded probes on an owned test path
  traceroute -n <host>           UDP mode (default Linux)
  traceroute -n -I <host>        ICMP mode (requires root)
  traceroute -n -T -p 443 <host> TCP mode (best through firewalls)
  traceroute -n -N 1 -q 1 <host> One probe at a time (avoids rate limits)
  tracepath -n <host>            Discover path MTU (no root needed)
  mtr -n -c 100 --report <host>  Continuous path analysis (100 probes)
  sudo tcpdump -i any -nn icmp   Capture all ICMP traffic
  tcpdump 'icmp[icmptype]==3'    Only Destination Unreachable
  ip route get <host>            Shows a cached "mtu N" or "<redirected>" 

READING PING OUTPUT
  ttl=118     Reply traversed ~10 hops (128-118, assuming Windows/cloud origin)
  time=12ms   Round-trip latency to destination
  0% loss     All packets received replies
  mdev=0.2ms  Low jitter — consistent path

READING MTR OUTPUT
  Loss at hop N, 0% at N+1     Hop N filters or rate-limits ICMP — NOT a real problem
  Loss at hop N and later     Correlate with actual destination traffic
  Big RTT jump between hops   Compare paths and processing, not just distance
  High StDev at one hop        Congestion or queuing at that hop

TRACEROUTE * * *
  Followed by normal hops  →  Hop just doesn't send ICMP. Not a problem.
  All * * * from that point -> No replies observed; test application path

PMTUD BLACK HOLE DIAGNOSIS
  1. Small packets work, large fail    → suspect MTU
  2. ping -s 1400 -M do <host>        → find the break point
  3. tcpdump for ICMP Type 3 Code 4   → if absent, someone blocked it
  4. Fix: allow ICMP Type 3 and/or clamp MSS
```

---

## References

- RFC 792 — Internet Control Message Protocol (ICMP) — the original specification
- RFC 4443 — ICMPv6 for IPv6
- RFC 1191 — Path MTU Discovery
- RFC 4821 — Packetization Layer Path MTU Discovery (PLPMTUD)
- RFC 1812 — Requirements for IP Version 4 Routers — ICMP generation rules
- RFC 1122 — Requirements for Internet Hosts — how hosts must react to ICMP
- RFC 4890 — Recommendations for Filtering ICMPv6 Messages in Firewalls
- Linux kernel documentation, `Documentation/networking/ip-sysctl.rst` — `icmp_ratelimit`, `icmp_ratemask`, `accept_redirects`, `secure_redirects`, `shared_media`, `tcp_mtu_probing`
- Course lab: [`netlab/icmp.json`](../labs/netlab/icmp.json); errno explanations in [`phases`](../labs/go/cmd/phases)
- `man ping` — ICMP echo tool with MTU probing flags
- `man traceroute` — path tracing with UDP, ICMP, and TCP modes
- `man tracepath` — path MTU discovery tool
- `man mtr` — continuous network diagnostic tool
- `man tcpdump` — packet capture with ICMP filter syntax
- `man iptables-extensions` — `REJECT --reject-with` and `TCPMSS`
