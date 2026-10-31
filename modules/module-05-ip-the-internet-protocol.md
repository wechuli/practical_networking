# Module 5: IP — The Internet Protocol

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 4 — The OSI Model
> **Estimated time:** 3–4 hours (the labs run offline in network namespaces)

## Overview

IP is the protocol that makes the internet an internet. Everything else — TCP, HTTP, DNS, TLS — rides on top of IP. Every packet that crosses a network boundary carries an IP header. Every routing decision, every firewall rule, every security group, every VPC subnet, every load balancer decision starts with an IP address. If you do not understand IP addressing, subnetting, and the IP header, you cannot troubleshoot any of these systems.

IP is a Layer 3 protocol. Its two core jobs are **addressing** (giving every interface an identifier that is unique within the network it is used in) and **routing** (moving packets hop by hop from source to destination through intermediate routers). The header carries a few supporting mechanisms for those jobs — TTL to kill looping packets, a protocol number to hand the payload to TCP, UDP, or ICMP, and (in IPv4) fragmentation — but nothing more. IP does not guarantee delivery, does not guarantee ordering, does not detect duplicates, and does not retransmit lost packets. It is best-effort. Those guarantees are the job of TCP (Layer 4). IP just gets packets from here to there — or silently drops them trying.

This module covers IPv4 addressing and subnetting in the depth you need to actually carve up a cloud VPC, read a routing table, and understand why a packet is or is not arriving. It covers the IPv4 packet header field by field so you can read a tcpdump capture and immediately know what every number means. It covers IPv6 because dual-stack is now the reality in cloud environments. And it covers the special addresses you will encounter in production — loopback, link-local, broadcast, multicast — so you know what they mean when you see them instead of assuming something is misconfigured.

## What You Will Build Toward

- Read an IPv4 address and immediately identify whether it is public, private, loopback, or link-local
- Subnet a network in your head: given a CIDR block, calculate the network address, broadcast address, and usable host range
- Read every field in an IPv4 packet header captured with tcpdump and explain what it does
- Explain why TTL exists and what it means when traceroute shows asterisks
- Understand IPv6 addressing well enough to work in dual-stack cloud environments
- Design a VPC subnet scheme for a multi-tier application using CIDR notation

---

## 5.1 What IP Does (and Doesn't Do)

### What This Is

IP — Internet Protocol — is the network layer protocol that provides logical addressing and routing. It is defined in RFC 791 (IPv4, 1981; still the standard, with updates such as RFC 6864 on the ID field) and RFC 8200 (IPv6, 2017, which replaced RFC 2460 from 1998). Every device on an IP network has at least one IP address, and every packet contains a source IP and a destination IP that routers use to make forwarding decisions.

### The Two Jobs of IP

**Job 1: Addressing.** IP assigns a logical address to every network interface. Unlike MAC addresses (which are flat and non-hierarchical), IP addresses are hierarchical — they encode both the network and the host within that network. This hierarchy is what makes routing possible. A router does not need to know the location of every host on the internet; it only needs to know how to reach each network, and the network prefix in the IP address tells it where to forward.

**Job 2: Routing.** IP moves packets from source to destination by making hop-by-hop forwarding decisions. Each router examines the destination IP, looks it up in its routing table using longest prefix match, and forwards the packet to the next hop. No router knows the entire path — each one only knows the next step. The packet hops from router to router until it reaches the destination network.

### What IP Does NOT Do

This is equally important. IP is **connectionless** and **best-effort**:

- **No connections.** IP packets are independent; each one is forwarded on its own merits, and no router keeps per-connection state for IP itself. Two packets to the same destination *may* take different paths — after a route change, or across load-balanced links. In practice most routers spread traffic with **ECMP per-flow hashing** (source/destination address, protocol, ports), so one TCP connection usually stays on one path and different connections take different ones. That is why a problem on one of several equal-cost paths shows up as "some connections fail, retries succeed" rather than as every connection losing a few packets.
- **No reliability.** If a packet is lost (dropped by a congested router, corrupted on the wire), IP does not retransmit it. The sender is usually not notified; some drops do produce an ICMP error (TTL exceeded, destination unreachable, fragmentation needed — Module 7), but a congested queue, a firewall DROP rule, or a corrupted frame produces nothing.
- **No ordering.** Packets can arrive out of order. IP does not reorder them.
- **No duplicate detection.** The same data might arrive twice. IP does not deduplicate.
- **No flow control.** IP does not slow down the sender if the receiver is overwhelmed.

All of these guarantees are provided by TCP (Layer 4). UDP explicitly chooses not to provide them. Understanding this split — IP provides the path, TCP provides the reliability — is essential for diagnosing why packets are lost, duplicated, or arrive out of order.

### Diagram

```
WHAT IP PROVIDES vs WHAT IT DOES NOT

  ┌─────────────────────────────────────────────────────────────┐
  │  IP PROVIDES                                                │
  │                                                             │
  │  ✓ Logical addressing  — every host has a unique address    │
  │  ✓ Routing             — hop-by-hop forwarding to dest      │
  │  ✓ Fragmentation       — splitting packets that are too     │
  │                          large for a link (IPv4 only)       │
  │  ✓ TTL                 — prevents infinite routing loops    │
  │  ✓ Protocol muxing     — tells the next layer whether       │
  │                          payload is TCP, UDP, or ICMP       │
  │  ✓ Error reporting     — via its companion, ICMP            │
  │                          (not a delivery guarantee)         │
  └─────────────────────────────────────────────────────────────┘

  ┌─────────────────────────────────────────────────────────────┐
  │  IP DOES NOT PROVIDE                                        │
  │                                                             │
  │  ✗ Reliable delivery   — packets can be silently dropped    │
  │  ✗ Ordering            — packets may arrive out of order    │
  │  ✗ Duplicate detection — same packet may arrive twice       │
  │  ✗ Flow control        — no mechanism to slow the sender    │
  │  ✗ Connection state    — each packet is independent         │
  │  ✗ Error correction    — IPv4 detects header corruption     │
  │                          only (header checksum) and drops;  │
  │                          IPv6 has no header checksum        │
  └─────────────────────────────────────────────────────────────┘

  These missing guarantees are provided by TCP (Layer 4):
  ┌──────────────┐
  │     TCP      │ ← reliability, ordering, flow control
  ├──────────────┤
  │      IP      │ ← addressing and routing (best effort)
  ├──────────────┤
  │   Ethernet   │ ← local delivery by MAC address
  └──────────────┘
```

### Production Relevance

When an application logs "connection timed out," many engineers immediately suspect the application or DNS. But the timeout often happens at the IP layer — the packet was routed into a black hole, a misconfigured route sent it to the wrong network, or (most often in the cloud) a security group, NSG, or firewall dropped it without a reply. `ip route get <destination>` tells you which route, interface, and source address *your* host will use; `ping` and `traceroute` probe the path beyond it.

Use them as evidence, not as a gate. As Module 4 showed, many cloud services and firewalls drop ICMP while the TCP port you care about works, so a failed ping proves only that ICMP did not come back. The decisive test is a connection to the real port (`curl -v`, `nc -vz`, or the `phases` tool) plus a capture showing what came back: nothing (a drop), a RST (refused), or an ICMP error naming the router that gave up.

---

## 5.2 IPv4 Addressing

### What This Is

An IPv4 address is a 32-bit number that uniquely identifies a network interface. It is written in dotted decimal notation: four octets (bytes) separated by dots. Each octet ranges from 0 to 255.

```
  Binary:    11000000 . 10101000 . 00000001 . 01100100
  Decimal:   192      . 168      . 1        . 100

  32 bits total → 2^32 = 4,294,967,296 possible addresses
  (About 4.3 billion — not enough for the modern internet)
```

### The Structure: Network Part and Host Part

Every IP address has two parts: a **network portion** (identifies which network) and a **host portion** (identifies which device on that network). The boundary between them is defined by the subnet mask (covered in 5.3).

```
  IP Address:   192.168.1.100
  Subnet Mask:  255.255.255.0  (/24)

  Network part: 192.168.1      ← identifies the network
  Host part:              .100 ← identifies the device on that network

  All devices with 192.168.1.x (where x = 1–254) are on the same network.
  A router is needed to reach 192.168.2.x — that is a different network.
```

### Historical: Classful Addressing (Why You Still See It)

Before CIDR (1993), addresses were divided into fixed classes:

```
CLASS   FIRST OCTET    DEFAULT MASK     NETWORKS    HOSTS/NET    RANGE
─────   ───────────    ────────────     ────────    ─────────    ─────────────────
  A       1 – 126       /8  (255.0.0.0)      126     16,777,214   1.0.0.0 – 126.255.255.255
  B     128 – 191      /16 (255.255.0.0)   16,384        65,534   128.0.0.0 – 191.255.255.255
  C     192 – 223      /24 (255.255.255.0) 2,097,152        254   192.0.0.0 – 223.255.255.255
  D     224 – 239       —  (multicast)         —            —     224.0.0.0 – 239.255.255.255
  E     240 – 255       —  (reserved)          —            —     240.0.0.0 – 255.255.255.255
```

**Why this is obsolete:** A company needing 500 addresses would be assigned a Class B (/16 = 65,534 addresses), wasting over 65,000 addresses. This massive waste accelerated IPv4 exhaustion. CIDR replaced classful addressing by allowing subnet masks at any bit boundary, not just /8, /16, or /24.

**Why you still see it:** People still say "Class C" when they mean a /24, and "Class A" when they mean a /8. The terminology is wrong but entrenched. What matters is the CIDR prefix length, not the class.

### Public vs Private Address Space

RFC 1918 (1996) reserved three blocks for private use — addresses that internet routers must not route and ISPs filter, so anyone may reuse them inside their own network:

```
PRIVATE ADDRESS RANGES (RFC 1918)

  ┌──────────────────────┬────────────┬──────────────┬───────────────────┐
  │  Range               │   CIDR     │  # Addresses │  Common Use       │
  ├──────────────────────┼────────────┼──────────────┼───────────────────┤
  │  10.0.0.0 –          │  10.0.0.0  │  16,777,216  │  Cloud VPCs,      │
  │  10.255.255.255      │    /8      │  (16M)       │  large enterprises│
  ├──────────────────────┼────────────┼──────────────┼───────────────────┤
  │  172.16.0.0 –        │  172.16.0.0│  1,048,576   │  Docker default   │
  │  172.31.255.255      │    /12     │  (1M)        │  (172.17.0.0/16)  │
  ├──────────────────────┼────────────┼──────────────┼───────────────────┤
  │  192.168.0.0 –       │  192.168.0.0│    65,536   │  Home networks,   │
  │  192.168.255.255     │    /16     │  (65K)       │  small labs        │
  └──────────────────────┴────────────┴──────────────┴───────────────────┘

  KEY RULE: Private addresses CANNOT be routed on the public internet.
  To reach the internet, private addresses must be translated to public
  addresses using NAT (Network Address Translation).
```

**How to immediately classify an address:**

```
  10.x.x.x           → Private (RFC 1918)
  172.16.x.x–31.x.x  → Private (RFC 1918); 172.32.x.x is outside this block
  192.168.x.x         → Private (RFC 1918)
  127.x.x.x           → Loopback (never leaves the host)
  169.254.x.x         → Link-local: a client that got no DHCP lease, or an
                        intentional service (cloud metadata 169.254.169.254)
  100.64.x.x–127.x.x → CGNAT (RFC 6598, shared address space)
  224.x.x.x–239.x.x  → Multicast
  192.0.2.x, 198.51.100.x, 203.0.113.x → Documentation (RFC 5737); never real
  198.18.x.x–198.19.x.x → Benchmarking (RFC 2544); this course's lab networks
  Other addresses      → Check IANA special-purpose registries and actual routes
```

### Diagram

```
PUBLIC vs PRIVATE — HOW NAT CONNECTS THEM

  Private Network (10.0.1.0/24)               Public Internet
  ┌────────────────────────┐                  ┌──────────────┐
  │                        │                  │              │
  │  10.0.1.10 (Web App)  │                  │  Web Server  │
  │  10.0.1.11 (API)      │──── NAT GW ─────│ 198.51.100.80│
  │  10.0.1.12 (Worker)   │  203.0.113.5    │              │
  │                        │  (public IP)     │              │
  └────────────────────────┘                  └──────────────┘

  Inside the VPC: packets have source 10.0.1.10
  After NAT:      packets have source 203.0.113.5
  The internet sees 203.0.113.5, never 10.0.1.10.
  The NAT gateway maintains a translation table to route replies back.
```

### Production Relevance

Address classification does not prove reachability, NAT, provider, or workload.
172.17.0.2 could be Docker or another private network; 10/8 is not cloud-specific.
169.254/16 is IPv4 link-local, possibly intentional or a client fallback. Check
configuration and logs. Non-RFC1918 space also includes loopback, multicast,
documentation, shared CGNAT, benchmarking, and reserved ranges; consult the
[IANA IPv4 registry](https://www.iana.org/assignments/iana-ipv4-special-registry/).

---

## 5.3 Subnetting and CIDR

### What This Is

Subnetting divides a large network into smaller, more manageable networks called subnets. CIDR (Classless Inter-Domain Routing, RFC 4632) is the notation system that replaced classful addressing. Instead of fixed /8, /16, /24 boundaries, CIDR allows the network/host boundary at any bit position.

**Why subnetting exists:**

- **Efficiency:** Allocate only the addresses you need, not an entire /16 when you need 50 hosts.
- **Security:** Place different tiers (web, app, database) in different subnets with different firewall rules.
- **Performance:** Reduce broadcast domain size — fewer hosts = fewer broadcasts = less noise.
- **Organization:** Map network structure to organizational or architectural boundaries.

### CIDR Notation

The `/n` after an IP address tells you how many bits (from the left) are the network portion. The remaining bits are the host portion.

```
CIDR NOTATION — HOW TO READ IT

  192.168.1.0/24
                ↑
                24 bits are the network portion
                32 - 24 = 8 bits are the host portion
                2^8 = 256 addresses (254 usable, 2 reserved)

  10.0.0.0/16
            ↑
            16 bits are the network portion
            32 - 16 = 16 bits are the host portion
            2^16 = 65,536 addresses (65,534 usable)

  10.0.0.0/8
            ↑
            8 bits are the network portion
            32 - 8 = 24 bits are the host portion
            2^24 = 16,777,216 addresses
```

### The Subnet Mask

The subnet mask is a 32-bit number where the network bits are all 1s and the host bits are all 0s. It is another way of expressing the same information as the CIDR prefix length.

```
CIDR     SUBNET MASK          BINARY MASK
────     ───────────────      ─────────────────────────────────
/8       255.0.0.0            11111111.00000000.00000000.00000000
/16      255.255.0.0          11111111.11111111.00000000.00000000
/20      255.255.240.0        11111111.11111111.11110000.00000000
/24      255.255.255.0        11111111.11111111.11111111.00000000
/25      255.255.255.128      11111111.11111111.11111111.10000000
/26      255.255.255.192      11111111.11111111.11111111.11000000
/27      255.255.255.224      11111111.11111111.11111111.11100000
/28      255.255.255.240      11111111.11111111.11111111.11110000
/30      255.255.255.252      11111111.11111111.11111111.11111100
/31      255.255.255.254      11111111.11111111.11111111.11111110
/32      255.255.255.255      11111111.11111111.11111111.11111111
```

### How to Calculate Subnet Properties

Given a CIDR block, you need to compute four things: the network address, the broadcast address, the first usable host, and the last usable host.

**Formula:**

```
  Given: IP/prefix  (e.g., 192.168.1.100/24)

  Host bits       = 32 - prefix
  Total addresses = 2^(host bits)
  Ordinary IPv4 subnet hosts = 2^(host bits) - 2 (network + broadcast)
  Exceptions: /31 point-to-point uses both addresses; /32 is one host route.

  Network address = IP AND subnet mask   (all host bits = 0)
  Broadcast addr  = IP OR (~subnet mask) (all host bits = 1)
  First usable    = Network address + 1
  Last usable     = Broadcast address - 1
```

### Worked Examples

```
EXAMPLE 1: 192.168.1.0/24

  Prefix:        /24  → 24 network bits, 8 host bits
  Total addrs:   2^8 = 256
  Usable hosts:  256 - 2 = 254

  Network addr:  192.168.1.0     (host bits all 0)
  Broadcast:     192.168.1.255   (host bits all 1)
  First usable:  192.168.1.1
  Last usable:   192.168.1.254

  ┌─────────────────────────────────────────────┐
  │  192.168.1.0/24                             │
  │                                             │
  │  .0         = Network address (reserved)    │
  │  .1 – .254  = Usable host addresses (254)   │
  │  .255       = Broadcast address (reserved)  │
  └─────────────────────────────────────────────┘


EXAMPLE 2: 10.0.0.0/20

  Prefix:        /20  → 20 network bits, 12 host bits
  Total addrs:   2^12 = 4,096
  Usable hosts:  4,096 - 2 = 4,094

  Network addr:  10.0.0.0
  Broadcast:     10.0.15.255     ← .15 not .255 — because /20 boundary

  How to get .15:
    Third octet binary for /20: 0000|0000
                                ↑ /20 means the first 4 bits of this octet
                                  are network, the last 4 are host.
    Host bits max = 1111 = 15 in decimal
    So broadcast is 10.0.15.255

  First usable:  10.0.0.1
  Last usable:   10.0.15.254

  ┌─────────────────────────────────────────────┐
  │  10.0.0.0/20                                │
  │                                             │
  │  10.0.0.0         = Network address         │
  │  10.0.0.1         = First usable host       │
  │  10.0.15.254      = Last usable host        │
  │  10.0.15.255      = Broadcast address       │
  │  Total: 4,094 usable hosts                  │
  └─────────────────────────────────────────────┘


EXAMPLE 3: 172.16.0.0/28

  Prefix:        /28  → 28 network bits, 4 host bits
  Total addrs:   2^4 = 16
  Usable hosts:  16 - 2 = 14

  Network addr:  172.16.0.0
  Broadcast:     172.16.0.15    (4 host bits all 1 = 15)
  First usable:  172.16.0.1
  Last usable:   172.16.0.14

  ┌───────────────────────────────────────┐
  │  172.16.0.0/28                        │
  │  .0 = Network  .1–.14 = Hosts  .15 = Bcast │
  │  Only 14 usable addresses              │
  └───────────────────────────────────────┘
```

### CIDR Quick Reference Table

```
PREFIX   ADDRESSES   USABLE    COMMON USE
──────   ─────────   ──────    ──────────────────────────────────
/8       16,777,216  16,777,214  Entire 10.x.x.x private space
/12       1,048,576   1,048,574  The 172.16.0.0/12 private block (Docker carves its networks from it)
/16          65,536      65,534  AWS default VPC (172.31.0.0/16); largest AWS VPC CIDR block
/20           4,096       4,094  Large cloud subnet
/24             256         254  Standard cloud subnet, small LAN
/25             128         126  Split a /24 in two
/26              64          62  Small subnet for a tier
/27              32          30  Small tier, small DMZ
/28              16          14  Smallest AWS subnet
/29               8           6  Smallest Azure and Google Cloud subnet
/30               4           2  Traditional point-to-point router link
/31               2           2  Point-to-point link (RFC 3021): no network/broadcast, both usable
/32               1           1  Host route (single address), loopback or VIP
```

> **Cloud reservations:** the providers reserve more than the protocol's two addresses in every subnet, so "how many hosts fit" is smaller than the table says:
>
> | Provider | Reserved per IPv4 subnet | Usable in a /24 | Smallest subnet |
> | --- | --- | --- | --- |
> | AWS | 5: network, .1 VPC router, .2 DNS (the VPC base +2), .3 future use, broadcast | 251 | /28 (11 usable) |
> | Azure | 5: network, .1 default gateway, .2 and .3 Azure DNS mapping, broadcast | 251 | /29 (3 usable) |
> | Google Cloud | 4 in the primary range: network, .1 gateway, second-to-last, broadcast | 252 | /29 (4 usable) |
>
> Google Cloud's secondary ranges (used for GKE pods and services) reserve nothing. Check the provider's current documentation before sizing a subnet for a node pool or a runner fleet; running out of addresses shows up as "insufficient IP addresses" or pods stuck in `Pending`, not as a network error.

The total VPC CIDR is an address pool, not one subnet with the above host count.
Apply provider reservations per created subnet. The /31 and /32 protocol cases
do not mean a cloud subnet API accepts those prefix lengths.

### Practical: Subnet Design for a Multi-Tier App

```
SCENARIO: Design subnets for a 3-tier app in a VPC

  VPC CIDR: 10.0.0.0/16  (65,534 usable addresses)

  ┌─────────────────────────────────────────────────────────────────┐
  │  VPC: 10.0.0.0/16                                              │
  │                                                                 │
  │  Availability Zone A          Availability Zone B               │
  │  ┌──────────────────┐         ┌──────────────────┐             │
  │  │ Public Subnet    │         │ Public Subnet    │             │
  │  │ 10.0.1.0/24      │         │ 10.0.3.0/24      │             │
  │  │ (ALB, NAT GW)    │         │ (ALB, NAT GW)    │             │
  │  ├──────────────────┤         ├──────────────────┤             │
  │  │ Private Subnet   │         │ Private Subnet   │             │
  │  │ 10.0.10.0/24     │         │ 10.0.11.0/24     │             │
  │  │ (App servers)     │         │ (App servers)     │             │
  │  ├──────────────────┤         ├──────────────────┤             │
  │  │ Data Subnet      │         │ Data Subnet      │             │
  │  │ 10.0.20.0/24     │         │ 10.0.21.0/24     │             │
  │  │ (RDS, Redis)     │         │ (RDS, Redis)     │             │
  │  └──────────────────┘         └──────────────────┘             │
  │                                                                 │
  │  Gap at 10.0.2.0, 10.0.4–9.0, 10.0.12–19.0, etc.             │
  │  Left intentionally for future expansion.                       │
  └─────────────────────────────────────────────────────────────────┘

  Design principles:
  - Public subnets: hold load balancers and NAT gateways. Route to IGW.
  - Private subnets: hold application instances. Route to NAT GW (no direct internet).
  - Data subnets: hold databases. No internet access at all.
  - Number subnets with gaps to allow inserting new ones later.
  - Mirror across AZs for high availability.
```

---

## 5.4 IPv4 Packet Header Deep Dive

### What This Is

Every IPv4 packet begins with a 20-byte header (minimum, no options). This header is what routers read to make forwarding decisions, what firewalls inspect to apply rules, and what `tcpdump` decodes when you capture traffic. Understanding every field means you can read a raw packet capture and immediately know what is happening.

### The Header

```
IPv4 HEADER (20 bytes minimum)

  0                   1                   2                   3
  0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |Version|  IHL  |    DSCP   |ECN|         Total Length          |  ← Row 1
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |         Identification        |Flags|     Fragment Offset     |  ← Row 2
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |  Time to Live |    Protocol   |        Header Checksum        |  ← Row 3
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                       Source IP Address                       |  ← Row 4
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                    Destination IP Address                     |  ← Row 5
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
  |                    Options (if IHL > 5)                       |  ← Optional
  +-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

### Field-by-Field Explanation

```
FIELD               BITS   WHAT IT DOES                           WHAT YOU SEE IN TCPDUMP
────────────────    ────   ───────────────────────────────────    ───────────────────────
Version              4     IP version. Always 4 for IPv4.         (not shown separately)

IHL                  4     Internet Header Length in 32-bit        Shown if options present.
                           words. Minimum = 5 (20 bytes).         Usually 5 (no options).
                           Maximum = 15 (60 bytes).

DSCP + ECN           8     The old ToS byte. Top 6 bits: DSCP,    "tos 0x0" in tcpdump -v
                           a QoS class (0 = best effort).          (the whole byte). Linux
                           Bottom 2 bits: ECN, congestion          marks its own ICMP errors
                           signals (Module 13).                    "tos 0xc0" = DSCP 48 (CS6).

Total Length        16     Total packet size in bytes (header      "length 1500" in tcpdump.
                           + data). Maximum = 65,535 bytes.        Actual size of this packet.

Identification      16     Groups the fragments of one packet.    "id XXXX" (tcpdump -v).
                           Only meaningful when fragmentation      Same ID on all fragments
                           can happen (RFC 6864); with DF set,     of one packet. Linux may
                           senders may use any value.              send DF packets with id 0.

Flags                3     Bit 0: Reserved (0)                    "flags [DF]" = Don't Fragment
                           Bit 1: DF (Don't Fragment)              "flags [+]"  = More Fragments
                           Bit 2: MF (More Fragments)              "flags [none]" = neither

Fragment Offset     13     Where this fragment belongs in the      "offset 1376" — tcpdump
                           original packet. Measured in 8-byte     converts to bytes. Non-zero,
                           units. 0 = first (or only) fragment.    or offset 0 with [+], = a fragment

TTL                  8     Time To Live. Decremented by 1 at      "ttl 64" (Linux default)
                           each router hop. When it reaches 0,     "ttl 128" (Windows default)
                           the router drops the packet and         "ttl 255" (network equipment)
                           sends ICMP Time Exceeded.               Low TTL = many hops traversed

Protocol             8     Identifies the Layer 4 protocol.       "proto TCP (6)"
                           1 = ICMP, 6 = TCP, 17 = UDP,           "proto UDP (17)"
                           47 = GRE, 50 = ESP (IPsec)             "proto ICMP (1)"

Header Checksum     16     Error detection for the header only.   tcpdump -v prints "bad cksum"
                           Recalculated at each hop (because       only when it is wrong.
                           TTL changes). If invalid, packet        On a received packet that
                           is silently dropped.                    means corruption. (Bad TCP/
                                                                   UDP checksums on packets your
                                                                   own host sends are usually
                                                                   checksum offload — Module 3.)

Source IP           32     The sender's IP address.               "10.0.1.50.52431 >"
                                                                   (source IP and port)

Destination IP      32     The receiver's IP address.             "> 203.0.113.80.443"
                                                                   (destination IP and port)
```

### TTL — Time to Live

TTL is one of the most important fields for troubleshooting. It prevents routing loops from consuming infinite bandwidth.

```
HOW TTL WORKS

  Sender sets TTL = 64 (Linux default)

  [Sender]  TTL=64  →  [Router 1]  TTL=63  →  [Router 2]  TTL=62  →  [Destination]
                         ↑ decrements          ↑ decrements
                           by 1                  by 1

  If there is a routing loop:

  [Router A]  TTL=3  →  [Router B]  TTL=2  →  [Router A]  TTL=1  →  [Router B]
                                                                          ↑
                                                                    TTL=0!
                                                                    Drop packet.
                                                                    Send ICMP
                                                                    Time Exceeded
                                                                    back to sender.

  This is exactly how traceroute works:
   - Send packets with TTL=1 → first router drops, sends ICMP → you learn hop 1
   - Send packets with TTL=2 → second router drops, sends ICMP → you learn hop 2
   - Continue incrementing until the destination is reached
```

**Reading TTL in practice:**

```
  TTL = 64    → Packet came from a Linux host, likely 0 hops away (same subnet)
  TTL = 63    → Packet traversed 1 hop from a Linux host
  TTL = 56    → Packet traversed 8 hops from a Linux host (64 - 56)
  TTL = 128   → Common Windows default
  TTL = 255   → Network equipment (routers, switches)
  TTL = 1     → Packet is about to expire — only 1 hop left
```

TTL estimates are guesses: the sender's initial value is a convention, not a rule, and anything that terminates and re-originates traffic (a proxy, a load balancer, a NAT64 gateway) starts a fresh TTL.

**TTL as a deliberate limit.** AWS IMDSv2 puts the instance metadata token in a reply whose IP TTL equals the instance's *response hop limit*. With a limit of 1 the reply reaches the instance's own network stack but expires at the next hop — the bridge or veth routing into a container — so code in a container on that instance cannot get credentials while the same command on the host works. Raising the limit to 2 (or using pod- or task-level IAM roles) fixes it. A one-byte header field, used as an access control.

### Fragmentation

```
IP FRAGMENTATION — WHEN PACKETS ARE TOO BIG

  Original packet: 4000 bytes, MTU of link: 1500 bytes

  WITHOUT DF (Don't Fragment) bit set:
  ┌──────────────────────┐
  │ Original: 4000 bytes │
  └──────────┬───────────┘
             │ Router fragments
             ▼
  ┌─────────────────┐  ┌─────────────────┐  ┌─────────────────┐
  │ Fragment 1      │  │ Fragment 2      │  │ Fragment 3      │
  │ 1500 bytes      │  │ 1500 bytes      │  │ 1040 bytes      │
  │ Offset=0, MF=1  │  │ Offset=185,MF=1 │  │ Offset=370,MF=0 │
  │ ID=12345        │  │ ID=12345        │  │ ID=12345        │
  └─────────────────┘  └─────────────────┘  └─────────────────┘
  Same ID links them.   Offset tells order.   MF=0 means last.

  Each fragment repeats the 20-byte header, so fragments 1 and 2 carry
  1480 data bytes (a multiple of 8): 3980 = 1480 + 1480 + 1020.
  Offsets are in 8-byte units (185 × 8 = 1480); tcpdump prints them in
  bytes: offset 0, 1480, 2960. Only the first fragment carries the TCP or
  UDP header, so later fragments have no ports — a firewall or load
  balancer that matches on ports cannot classify them without reassembly.
  The receiver (not a router) reassembles; losing one fragment loses the
  whole packet.

  WITH DF bit set (modern default — Path MTU Discovery):
  ┌──────────────────────┐
  │ Original: 4000 bytes │
  │ DF=1                 │
  └──────────┬───────────┘
             │ Router cannot fragment!
             ▼
  ICMP Type 3 Code 4: "Fragmentation Needed and DF Set"
  Message includes: MTU of the next hop (e.g., 1400)
  Sender caches that path MTU and sends smaller packets
  (TCP resends the lost segment at the smaller size).

  Linux sets DF on TCP and, by default, on ping; the lab below shows
  the ICMP error, the cached "mtu 1400" in `ip route get`, and the next
  oversize ping failing locally. IPv6 routers never fragment: there is
  no DF bit because every IPv6 packet behaves as if DF were set (5.5).

  IF ICMP IS BLOCKED (firewall drops ICMP):
  → Sender never gets the "too big" message
  → Packets silently disappear
  → TCP connection hangs after SYN/SYN-ACK succeeds (small packets work)
  → Large data transfer stalls
  → This is a PMTUD BLACK HOLE — one of the hardest problems to diagnose
```

### DSCP / ToS — Quality of Service

The DSCP (Differentiated Services Code Point) field marks packets for priority handling by routers. In most environments, it defaults to 0 (best effort). You will see non-zero values in environments with QoS policies — VoIP traffic marked as EF (Expedited Forwarding, DSCP 46) to prioritize voice over data, or video traffic marked as AF41.

Do not assume DSCP does anything in the cloud. Providers generally do not prioritise customer traffic by its markings inside their networks, and whether a marking survives a given path (VPC to VPC, through a NAT gateway, across the internet, over Direct Connect or ExpressRoute) varies by provider and path. DSCP matters mostly on networks you control end to end: on-premises voice and video, and the private circuit into the cloud when the provider documents QoS for it. To find out, capture on both ends and compare the `tos` byte.

---

## 5.5 IPv6

### What This Is

IPv6 is the successor to IPv4, designed to solve the address exhaustion problem. IPv4 has 2^32 (4.3 billion) addresses — not enough for the modern internet. NAT extended the usable life of IPv4 by allowing thousands of devices to share a single public address, but NAT introduces complexity, breaks end-to-end connectivity, and complicates protocols that embed IP addresses in their payloads.

IPv6 uses 128-bit addresses: 2^128 = 340 undecillion addresses. That is approximately 3.4 × 10^38 — enough to assign trillions of addresses to every person on earth.

### IPv6 Address Format

```
IPv6 ADDRESS FORMAT

  Full notation:
  2001:0db8:0000:0000:0000:0000:0000:0001

  8 groups of 4 hex digits, separated by colons.
  Each group = 16 bits. 8 × 16 = 128 bits.

  SHORTENING RULES:
  1. Leading zeros in each group can be omitted:
     2001:0db8:0000:0000:0000:0000:0000:0001
     → 2001:db8:0:0:0:0:0:1

  2. One consecutive run of all-zero groups can be replaced with :::
     2001:db8:0:0:0:0:0:1
     → 2001:db8::1

  3. :: can only appear ONCE (otherwise ambiguous).

  4. Tools print the canonical form (RFC 5952): lowercase, :: replaces
     the LONGEST run of zero groups, never a single group. Compare
     addresses in that form; a string match of "2001:DB8:0::1" against
     "2001:db8::1" fails although they are the same address.

  In URLs and host:port strings the address goes in brackets, because
  the colons would be ambiguous: https://[2001:db8::1]:8443/

  EXAMPLES:
  Loopback:       ::1               (full: 0000:0000:0000:0000:0000:0000:0000:0001)
  All zeros:      ::                (the unspecified address)
  Link-local:     fe80::1           (fe80::/10; in practice always fe80::/64)
  Google DNS:     2001:4860:4860::8888
  AWS VPC:        2600:1f18:xxxx::  (one of Amazon's blocks)

  SIZE CONVENTION: every LAN or cloud subnet is a /64 (64 bits of
  network, 64 bits of interface identifier). SLAAC needs exactly /64.
  Sites get a /48 or /56 to carve into /64s; an AWS VPC gets a /56, for
  example. Cloud subnets are /64s (AWS assigns each instance addresses or
  a /80 prefix from it). There is no "running out" inside a subnet, and
  no host-count subnetting math.

```

### IPv6 Address Types

```
IPv6 ADDRESS TYPES

  TYPE          PREFIX        SCOPE              EQUIVALENT IN IPv4
  ──────────    ──────────    ─────────────────  ───────────────────
  Global        2000::/3      Internet-routable  Public IPv4 address
  Unicast                     (assigned by ISP)

  Link-Local    fe80::/10     Single link only   169.254.x.x
                              (never routed)      (auto-assigned)
                              Auto-configured     Always present on
                              on every interface  every IPv6 interface;
                                                  needs the interface:
                                                  ping fe80::1%eth0

  Unique        fc00::/7      Private network     10.x.x.x / 172.16.x.x /
  Local (ULA)   (fd00::/8     (not internet-      192.168.x.x
                 in practice)  routable)

  Multicast     ff00::/8      One-to-many         224.0.0.0/4

  Loopback      ::1           Local host only     127.0.0.1

  KEY DIFFERENCE: IPv6 has NO broadcast.
  What IPv4 does with broadcast, IPv6 does with multicast.
  ff02::1 = all nodes on link (equivalent of IPv4 broadcast)
  ff02::2 = all routers on link
```

### The IPv6 Header

The IPv6 header is a fixed 40 bytes, simpler than IPv4's, and routers do less work per packet:

```
IPv6 HEADER (40 bytes, fixed)

  Version (4) | Traffic Class (8) | Flow Label (20)
  Payload Length (16)   | Next Header (8) | Hop Limit (8)
  Source Address (128)
  Destination Address (128)

  IPv4 FIELD          IN IPv6
  ─────────────────   ──────────────────────────────────────────────
  TTL                 Hop Limit (same job; ping -6 shows "ttl=")
  Protocol            Next Header (58 = ICMPv6, 6 = TCP, 17 = UDP),
                      or the type of an extension header that follows
  DSCP/ECN (ToS)      Traffic Class
  ID, flags, offset   Gone. Only the SENDER may fragment, using a
                      Fragment extension header. Routers never fragment.
  Header checksum     Gone. TCP/UDP checksums cover the addresses, and
                      the UDP checksum becomes mandatory.
  Options             Extension headers, chained by Next Header
  (none)              Flow Label: lets routers hash a flow without
                      reading past extension headers to the ports
```

The minimum link MTU is 1280 bytes. A router that cannot forward a packet sends ICMPv6 **Packet Too Big** with the next-hop MTU, and the sender must make its packets smaller. Blocking ICMPv6 therefore breaks IPv6 far more thoroughly than blocking ICMP breaks IPv4: there is no fallback to fragmentation in the network, and Neighbor Discovery (below) is ICMPv6 too. RFC 4890 lists which ICMPv6 types a firewall must let through.

### Neighbor Discovery — ARP's Replacement

IPv6 has no ARP and no broadcast. Neighbor Discovery (RFC 4861) does the same jobs with ICMPv6 messages sent to multicast groups:

```
NEIGHBOR DISCOVERY MESSAGES (all ICMPv6)

  TYPE  NAME                         JOB                       IPv4 EQUIVALENT
  ────  ───────────────────────────  ────────────────────────  ────────────────
  133   Router Solicitation (RS)     "any routers here?"       (DHCP discover)
  134   Router Advertisement (RA)    prefix, default router,   DHCP offer +
                                     MTU, DNS (RDNSS)          default gateway
  135   Neighbor Solicitation (NS)   "who has fd42:1::1?"      ARP request
  136   Neighbor Advertisement (NA)  "I do; my MAC is ..."     ARP reply
  137   Redirect                     "use that router instead" ICMP redirect
    2   Packet Too Big               "next hop MTU is 1280"    ICMP frag needed

  An NS is not broadcast. It goes to the target's SOLICITED-NODE
  multicast group, ff02::1:ffXX:XXXX (the last 24 bits of the target
  address), so only hosts whose address ends the same way process it.
  fd42:1::99 → ff02::1:ff00:99.

  `ip -6 neigh` is the IPv6 `ip neigh`: states REACHABLE, STALE, DELAY,
  PROBE, FAILED work as in Module 3; "router" marks a neighbor that
  sent an RA.
```

### SLAAC — Stateless Address Autoconfiguration

An IPv6 host can configure itself without a DHCP server. The router advertises a prefix; the host picks its own address inside it and checks that nobody else has it.

```
SLAAC — HOW IPv6 HOSTS CONFIGURE THEMSELVES

  1. Interface comes up. The host builds a link-local address,
     fe80:: + a 64-bit interface identifier, and runs Duplicate
     Address Detection (step 5) on it.

  2. Host sends a Router Solicitation (RS) to ff02::2 (all routers).
     Routers also send unsolicited RAs periodically to ff02::1.

  3. Router answers with a Router Advertisement (RA), sent from its
     link-local address:
     - Prefix information: 2001:db8:1::/64 with the A (autonomous)
       flag, meaning "build your own address in this"
     - Router lifetime > 0: "use me as a default router". The default
       route therefore points at a link-local address (fe80::...).
     - M/O flags: get addresses (M) or other settings (O) from DHCPv6
     - Optional: MTU, DNS servers (RDNSS, RFC 8106)

  4. Host builds a global address: prefix + interface identifier.
     The identifier is often NOT the MAC-derived EUI-64 any more:
     NetworkManager, Windows, and macOS use stable but opaque
     identifiers (RFC 7217), and many systems add short-lived temporary
     "privacy" addresses (RFC 8981) for outgoing connections. Do not
     try to read a MAC out of an IPv6 address.

  5. Duplicate Address Detection (DAD) on each new address: the host
     sends an NS for its own candidate address, from the unspecified
     address ::, to the solicited-node group. While it waits (about
     1 second) the address is "tentative" and unusable. If anyone
     answers with an NA, the address is marked "dadfailed" and never
     used. Silence permits the address; it is not a guarantee that
     nobody will claim it later.

  Result: Host has both:
    fe80::xxxx          (link-local, for local communication)
    2001:db8:1::xxxx    (global, for internet communication)
    plus a default route "via fe80::... proto ra"
```

Cloud VMs usually get their IPv6 address from DHCPv6 or the provider's guest agent rather than from SLAAC, but the default route still typically arrives by RA (`proto ra`), and NDP still resolves neighbors, so `ip -6 route` and `ip -6 neigh` read the same as in the lab.

### IPv6 in Production

```
DUAL-STACK — THE TRANSITION REALITY

  Most production environments run both IPv4 and IPv6 simultaneously.

  ┌────────────────────────────────────────────────┐
  │  Server                                        │
  │                                                │
  │  eth0:                                         │
  │    IPv4: 10.0.1.50/24                          │
  │    IPv6: 2001:db8:1::50/64                     │
  │    IPv6: fe80::a1b2:c3d4:e5f6:7890 (link-local)│
  │                                                │
  │  Applications listen on:                       │
  │    0.0.0.0:443    (all IPv4 addresses)         │
  │    [::]:443       (all IPv6 addresses, and on  │
  │                    Linux IPv4 too, unless      │
  │                    IPV6_V6ONLY is set)         │
  │    (netstat prints [::]:443 as :::443)         │
  └────────────────────────────────────────────────┘

  DNS returns both A (IPv4) and AAAA (IPv6) records.
  Client chooses which to use — browsers and many libraries use
  Happy Eyeballs (RFC 8305):
  - Start connecting to the first IPv6 address
  - If it has not connected after a short delay, start IPv4 too
    (RFC 8305 recommends 250 ms; Chrome uses 300 ms; curl 200 ms)
  - Use whichever connects first, cancel the other
  An IPv4-mapped peer shows up in IPv6 logs as ::ffff:10.0.1.20.
```

**Production pitfalls:**

- An IPv4-only listener cannot accept IPv6. A `[::]` listener accepts IPv4-mapped
  connections only where platform policy and `IPV6_V6ONLY` permit it. Test both
  address families or bind separate listeners; do not assume portable dual-stack.
- A firewall allows traffic on IPv4 but not on IPv6 (separate rule sets). IPv6 clients are blocked.
- A client resolves AAAA and tries IPv6 first, but IPv6 is broken somewhere on the path (silently dropped). With Happy Eyeballs the cost is a few hundred milliseconds per new connection; without it (many language runtimes, older tools, `getaddrinfo` loops in hand-written code) the client waits its whole connect timeout — seconds to minutes — before trying IPv4. Symptoms: "the site is slow for some users", or a CI job that hangs only on runners with IPv6. Prove it with `curl -4` vs `curl -6`, or `phases` on each address.
- An IPv6 firewall that drops all ICMPv6 breaks Neighbor Discovery and Packet Too Big, so connections stall on large transfers or never start (RFC 4890).

---

## 5.6 Special and Reserved Addresses

### What This Is

Certain IP addresses have special meanings defined by IANA and various RFCs. Recognizing them on sight tells you immediately what is happening when they appear in logs, packet captures, or routing tables.

### IPv4 Special Addresses

```
ADDRESS/RANGE               NAME             RFC      WHAT IT MEANS IN PRODUCTION
───────────────────         ─────────────    ─────    ──────────────────────────────────────
127.0.0.0/8                 Loopback         1122     Traffic to 127.x.x.x never leaves the
(typically 127.0.0.1)                                 host. Used for local-only services.
                                                      If you see 127.0.0.1 in a remote
                                                      connection attempt, something is wrong.

0.0.0.0/8                   "This network"   1122     Meaning depends on context:
                                                      - In a route: 0.0.0.0/0 = default route
                                                        (matches any destination)
                                                      - In bind(): 0.0.0.0 = INADDR_ANY
                                                        (listen on all interfaces)
                                                      - As source: "I don't have an IP yet"
                                                        (DHCP Discover)

169.254.0.0/16              Link-local       3927     Link-local IPv4, possibly intentional
                                                      or a client fallback. Not forwarded
                                                      by routers. Inspect the intended
                                                      configuration before blaming DHCP.

                                                      Exception: 169.254.169.254 is the
                                                      cloud metadata endpoint in AWS, GCP,
                                                      and Azure (AWS also offers IPv6
                                                      fd00:ec2::254). Not a DHCP failure.

100.64.0.0/10               CGNAT / Shared   6598     Carrier-Grade NAT. Used by ISPs to
                                                      NAT subscribers behind another layer
                                                      of NAT. Tailscale uses this range
                                                      for its VPN addresses (100.x.x.x).

224.0.0.0/4                 Multicast        1112     One-to-many delivery. Not used for
(224.0.0.0 – 239.255.255.255)                        normal unicast traffic.
                                                      224.0.0.1 = all hosts on link
                                                      224.0.0.2 = all routers on link
                                                      239.x.x.x = organization-local

255.255.255.255             Broadcast        919      The limited broadcast address. Sent to
                                                      all hosts on the local network. DHCP
                                                      can use it; ARP is not an IP packet.
                                                      Never forwarded by
                                                      routers.

198.51.100.0/24             Documentation    5737     Reserved for use in documentation and
203.0.113.0/24              ranges                    examples. If you see these in
192.0.2.0/24                                          production configs, someone copy-pasted
                                                      from a tutorial.

198.18.0.0/15               Benchmarking     2544     Reserved for network device testing;
                                                      never on the internet. This course's
                                                      labs use it so they cannot clash with
                                                      your real networks.
```

### IPv6 Special Addresses

```
ADDRESS                     NAME             WHAT IT MEANS
───────────────────         ─────────────    ──────────────────────────────────────
::1                         Loopback         IPv6 equivalent of 127.0.0.1

::                          Unspecified       IPv6 equivalent of 0.0.0.0
                                              "I don't have an address yet"

fe80::/10                   Link-local       Auto-configured on every IPv6 interface.
                                              Never routed beyond the local link.
                                              Used for neighbor discovery, SLAAC.

fc00::/7 (fd00::/8)        Unique Local     IPv6 equivalent of RFC 1918 private space.
                            Address (ULA)     Not routed on the internet.

ff00::/8                    Multicast        ff02::1 = all nodes on link
                                              ff02::2 = all routers on link
                                              ff02::fb = mDNS

2001:db8::/32               Documentation    Reserved for examples. If seen in
                                              production: someone copy-pasted.

::ffff:0:0/96               IPv4-mapped      An IPv4 peer seen through an IPv6
                                              socket: ::ffff:10.0.1.20 in a log
                                              is an IPv4 client, not IPv6.

64:ff9b::/96                NAT64             Used for IPv6-to-IPv4 translation.
                                              Last 32 bits contain the IPv4 address.
```

### Diagram: Special Address Decision Tree

```
  WHAT IS THIS IP ADDRESS?

  Start: Look at the IP
         │
         ├── Starts with 10.?           → Private (RFC 1918)
         ├── Starts with 172.16–31.?    → Private (RFC 1918)
         ├── Starts with 192.168.?      → Private (RFC 1918)
         ├── Starts with 127.?          → Loopback (localhost)
         ├── Starts with 169.254.?      → Link-local (no DHCP lease, or a service)
         │   └── Is it 169.254.169.254?  → Cloud instance metadata endpoint
         ├── Starts with 100.64–127.?   → CGNAT (ISP NAT or Tailscale)
         ├── Starts with 224–239.?      → Multicast
         ├── Is it 255.255.255.255?     → Broadcast
         ├── Is it 0.0.0.0?            → Default route or INADDR_ANY
         ├── Starts with 198.51.100.?   → Documentation range (should not be in prod)
         ├── Starts with 203.0.113.?    → Documentation range (should not be in prod)
         ├── Starts with 192.0.2.?      → Documentation range (should not be in prod)
         ├── Starts with 198.18–19.?    → Benchmarking / lab range
         └── Other space                 → Check IANA classification and routes

  IPv6:
         ├── ::1                         → Loopback
         ├── ::                          → Unspecified
         ├── ::ffff:a.b.c.d              → IPv4 client on an IPv6 socket
         ├── fe80::                      → Link-local
         ├── fd00::                      → Unique Local (private)
         ├── ff00::                      → Multicast
         ├── 2001:db8::                  → Documentation
         └── 2000::/3                    → Global unicast (public)
```

### Production Relevance

**Metadata context:** 169.254.169.254 is used by several cloud metadata services,
but request headers, tokens, paths, and network controls differ. AWS IMDSv2 uses a
token and a configured response hop limit. A route match alone neither establishes
metadata access nor proves an IAM failure is networking. Avoid fetching or printing
credentials while testing metadata; inspect configured access mode and SDK errors.

**The 0.0.0.0 confusion:** When an app binds to `0.0.0.0:8080`, it means "listen on port 8080 on all interfaces." When you see `0.0.0.0/0` in a route table, it means "this route matches any destination" (the default route). When you see `0.0.0.0` as a source IP in a DHCP Discover packet, it means "I don't have an IP address yet." Same address, three completely different meanings depending on context.

---

## Hands-On Lab: IP Addressing, Subnetting, and the IP Header

### Lab Goals

- Read your own host's addresses, routes, and the route the kernel picks for a destination
- Do subnet arithmetic by hand, then check it with the course's `subnet` tool, including cloud reservations
- Read every IPv4 header field in a capture, and watch TTL decrement at a router
- Make a router send *Time Exceeded* and *Fragmentation Needed*, see the path MTU cached, and see fragments on the wire
- Tell a missing route ("Network is unreachable", nothing sent) from a missing reply, and fix reachability with on-link routes
- Watch IPv6 configure itself: Router Solicitation and Advertisement, SLAAC, Neighbor Discovery, duplicate address detection, and Packet Too Big

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built:
  ```bash
  cd labs/go && go build -o bin/ ./cmd/...
  ```
- `iproute2`, `iputils-ping`, `tcpdump`; `dnsmasq` for Part 5
  (`sudo apt-get install -y dnsmasq-base` gives the binary without starting a system DNS service)
- Parts 2–5 run offline inside network namespaces. Part 1 reads your real host; Part 6 needs internet access.

---

### Lab Part 1: Read Your Own Host

```bash
ip -brief address show          # every interface, its state, and its addresses
ip route show                   # IPv4 routing table
ip route get 192.0.2.10         # which route, interface, and source address would be used
ip -6 address show              # IPv6 addresses; fe80:: ones are link-local
ip -6 route show
```

`ip route get` sends nothing; it asks the kernel for its decision. That makes it safe to run against any address, including one you are not allowed to probe. Illustrative output on a cloud VM:

```
lo               UNKNOWN        127.0.0.1/8 ::1/128
eth0             UP             10.0.1.50/24 fe80::a1b2:c3d4:e5f6:7890/64
docker0          DOWN           172.17.0.1/16

192.0.2.10 via 10.0.1.1 dev eth0 src 10.0.1.50 uid 1000
    cache
```

For each address, say what kind it is (5.2, 5.6), or ask the tool:

```bash
./bin/subnet 10.0.1.50 172.17.0.1 169.254.169.254 fe80::a1b2:c3d4:e5f6:7890
```

`docker0` is DOWN because no container is attached, not because something is broken. `ip route get 169.254.169.254` answers on any host with a default route, so a route proves nothing about whether a metadata service exists there (5.6).

---

### Lab Part 2: Subnetting by Hand, Checked by Code

Work these out on paper first, then check them:

```
Exercise: Given 10.0.48.0/20, calculate:
  - Network address: ?       (answer: 10.0.48.0)
  - Broadcast: ?             (answer: 10.0.63.255)
  - First usable: ?          (answer: 10.0.48.1)
  - Last usable: ?           (answer: 10.0.63.254)
  - Total usable hosts: ?    (answer: 4,094)

  How to get the broadcast:
  /20 = 20 network bits, 12 host bits
  Third octet: 48 = 00110000 in binary
  /20 means first 4 bits of third octet are network: 0011|0000
  Host bits (last 4 bits) all 1s: 0011|1111 = 63
  So broadcast third octet = 63, fourth octet = 255
  Broadcast = 10.0.63.255
```

```bash
cd labs/go
./bin/subnet 10.0.48.0/20
```

```
prefix      10.0.48.0/20
mask        255.255.240.0
kind        private (RFC 1918)
addresses   4096
network     10.0.48.0
broadcast   10.0.63.255
usable      10.0.48.1 – 10.0.63.254 (4094 addresses)
```

Then the cases that catch people out:

```bash
./bin/subnet 172.16.5.0/28
./bin/subnet 10.0.100.0/31                      # RFC 3021: both addresses usable
./bin/subnet -cloud aws   10.0.20.0/24          # 251 usable, first host .4
./bin/subnet -cloud gcp   10.0.20.0/24          # 252 usable; second-to-last reserved
./bin/subnet -cloud aws   10.0.20.0/29          # rejected: AWS subnets stop at /28
./bin/subnet 10.0.1.50/24 10.0.1.200 10.0.2.50  # which addresses are on-link?
```

The last command is the question behind half of all "can't reach" tickets: is the destination inside my subnet (the kernel ARPs for it directly) or outside (the packet goes to a router, so a route and the router's own policy decide)? The tool's source, [`cmd/subnet`](../labs/go/cmd/subnet), is short: `net/netip` does the parsing, and the arithmetic is the AND/OR of 5.3. The `ipcalc` package (`apt-get install ipcalc`) gives similar output if you prefer it.

---

### Lab Part 3: IPv4 on the Wire

[`ip.json`](../labs/netlab/ip.json) builds a client and a server in two subnets joined by a Linux router whose server-side link has an MTU of 1400, plus two hosts, `a` and `b`, on one cable but in different subnets:

```
  client 198.18.5.1/24 ── eth0 router eth1 ── server 198.19.5.2/24
                         .254        .254
                                MTU 1400 on this link

  a 198.51.100.10/25 ───────────── b 198.51.100.200/25   (one cable, two /25s)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/ip.json
```

Every check prints its command and output and saves evidence (pcaps and their decoded text). The transcript below is from a real run; timestamps and IDs will differ.

**The routing decision and the header.** The client's kernel picks the router as next hop, then the server sees the request one hop older than it was sent:

```
check 1/14: the kernel's routing decision for the server: next hop, interface, source address
  | 198.19.5.2 via 198.18.5.254 dev eth0 src 198.18.5.1 uid 0
  |     cache

check 3/14: IPv4 header fields: the request arrives with TTL 63 (one router), the reply leaves with TTL 64
  | 17:18:05.598129 IP (tos 0x0, ttl 63, id 28833, offset 0, flags [DF], proto ICMP (1), length 84)
  |     198.18.5.1 > 198.19.5.2: ICMP echo request, id 1599, seq 1, length 64
  | 17:18:05.598148 IP (tos 0x0, ttl 64, id 62675, offset 0, flags [none], proto ICMP (1), length 84)
  |     198.19.5.2 > 198.18.5.1: ICMP echo reply, id 1599, seq 1, length 64
```

Read the first header field by field against 5.4:

```
IP (tos 0x0, ttl 63, id 28833, offset 0, flags [DF], proto ICMP (1), length 84)
    ↑        ↑       ↑          ↑         ↑           ↑               ↑
    DSCP 0,  sent    ID: only   not a     Don't       payload is     20-byte header
    ECN 0    with 64, matters   fragment  Fragment    ICMP           + 8 ICMP header
             one hop  if frag-                                       + 56 data = 84
             later    mented
```

The request carries DF and the reply does not: Linux ping asks for path MTU discovery, while the kernel's own echo replies do not. Captured on the client instead, the reply would show `ttl 63` — the router decrements both directions. That is how you estimate hop counts from received TTLs: a reply with `ttl 56` from a Linux host crossed 8 routers.

**TTL expiry — the mechanism traceroute uses.** A ping with TTL 1 reaches the router, which cannot forward a packet whose TTL would become 0:

```
check 4/14: TTL 1: the router decrements it to 0, drops the packet, and says so (this is how traceroute works)
  | 17:12:34.715636 IP (tos 0x0, ttl 1, id 16288, offset 0, flags [DF], proto ICMP (1), length 84)
  |     198.18.5.1 > 198.19.5.2: ICMP echo request, id 539, seq 1, length 64
  | 17:12:34.715704 IP (tos 0xc0, ttl 64, id 40399, offset 0, flags [none], proto ICMP (1), length 112)
  |     198.18.5.254 > 198.18.5.1: ICMP time exceeded in-transit, length 92
  | 	IP (tos 0x0, ttl 1, id 16288, offset 0, flags [DF], proto ICMP (1), length 84)
  |     198.18.5.1 > 198.19.5.2: ICMP echo request, id 539, seq 1, length 64
```

The error comes **from the router's address**, and it quotes the header of the packet it killed (the indented lines) — that quote is how the sender matches errors to probes. `tos 0xc0` is DSCP 48 (CS6), which Linux puts on the ICMP errors it generates. Traceroute repeats this with TTL 1, 2, 3, …; a hop that never answers shows `* * *`, which means that router does not send (or a firewall drops) Time Exceeded, not that traffic is lost there (Module 7).

**DF meets a smaller link.** A 1428-byte packet with DF set cannot cross the 1400-byte link:

```
check 5/14: DF set and too big for the 1400-byte link: the router drops it and reports the MTU
  | From 198.18.5.254 icmp_seq=1 Frag needed and DF set (mtu = 1400)
  | 17:12:35.105057 IP (tos 0x0, ttl 64, id 0, offset 0, flags [DF], proto ICMP (1), length 1428)
  |     198.18.5.1 > 198.19.5.2: ICMP echo request, id 542, seq 1, length 1408
  | 17:12:35.105094 IP (tos 0xc0, ttl 64, id 40469, offset 0, flags [none], proto ICMP (1), length 576)
  |     198.18.5.254 > 198.18.5.1: ICMP 198.19.5.2 unreachable - need to frag (mtu 1400), length 556

check 6/14: the client cached the smaller path MTU for that destination
  | 198.19.5.2 via 198.18.5.254 dev eth0 src 198.18.5.1 uid 0
  |     cache expires 599sec mtu 1400

check 7/14: the next DF ping fails locally: the kernel already knows the path MTU
  | ping: local error: message too long, mtu=1400
```

That is path MTU discovery working: one ICMP message (type 3, code 4) and the sender's kernel remembers the limit for ten minutes. Note `id 0` on the DF packet — with fragmentation impossible, Linux does not bother with an ID. When a firewall drops that ICMP message, check 5 would show the request and nothing else, check 6 would show no `mtu 1400`, and a TCP transfer would stall after the handshake: the PMTUD black hole of 5.4 and Module 14.

**DF clear: fragments.** The same oversize packet without DF is split by the router:

```
check 8/14: DF clear: the packet is fragmented, the first piece has More Fragments set
  | IP (tos 0x0, ttl 63, id 16525, offset 0, flags [+], proto ICMP (1), length 1396)
  |     198.18.5.1 > 198.19.5.2: ICMP echo request, id 548, seq 1, length 1376
  | IP (tos 0x0, ttl 63, id 16525, offset 1376, flags [none], proto ICMP (1), length 652)
  |     198.18.5.1 > 198.19.5.2: ip-proto-1
```

Both pieces share `id 16525`. The first has `flags [+]` (More Fragments) and 1376 data bytes — the largest multiple of 8 that fits 1400 − 20. The second starts at byte offset 1376 and has no ICMP header at all, so tcpdump can only say `ip-proto-1`: a port-based firewall rule has nothing to match in it. 1376 + 632 = 2008 bytes = the 8-byte ICMP header + 2000 data bytes.

**No route versus no reply.**

```
check 9/14: no route at all: the error is immediate and local, no packet is sent
  | ping: connect: Network is unreachable

check 10/14: same cable, different subnets: no route to 198.51.100.200 from a /25 that ends at .127
  | ping: connect: Network is unreachable
```

"Network is unreachable" from your own kernel means the routing table had no match — nothing was transmitted, so no capture or firewall on the path is involved. Compare Module 4's "Destination Host Unreachable" (a route existed, ARP got no answer) and a plain timeout (packets left, nothing came back). Hosts `a` and `b` share a cable, but each one's connected route covers only its own /25.

**On-link routes.** A route with `dev` and no `via` tells the kernel the destination is directly on that link:

```
check 11/14: add an on-link route on a       (ip route add 198.51.100.128/25 dev eth0)
check 12/14: and the return route on b       (ip route add 198.51.100.0/25 dev eth0)
check 13/14: now a ARPs for b directly and the ping works
  PASS after 1.04s: reachable (2/2 replies)

check 14/14: a's routing table
  | 198.51.100.0/25 dev eth0 proto kernel scope link src 198.51.100.10
  | 198.51.100.128/25 dev eth0 scope link
```

`proto kernel` marks the route the kernel created from the address; the added one has no `proto` because you made it. Both directions need a route — with only check 11, `a`'s packets reach `b` but `b` has no route for the replies. This is a teaching trick, not a pattern to copy: in a cloud VPC every subnet already shares the VPC's local route, and the prefix boundary was never a security boundary — security groups, NSGs, and firewall rules are.

**Do it by hand.** `sudo ./bin/netlab up ../netlab/ip.json` leaves the lab running. Then try:

```bash
sudo ./bin/netlab exec ../netlab/ip.json client -- tracepath -n 198.19.5.2   # "pmtu 1400" hop by hop (apt-get install iputils-tracepath)
sudo ./bin/netlab exec ../netlab/ip.json client -- ping -c 1 -t 2 198.19.5.2  # TTL 2 is enough: one router
sudo ./bin/netlab exec ../netlab/ip.json router -- ip route                   # two connected routes, nothing else
sudo ./bin/netlab down ../netlab/ip.json
```

---

### Lab Part 4: Break It Yourself

With the lab up, predict the result, then run it:

1. `netlab exec … router -- sysctl -w net.ipv4.ip_forward=0`, then ping the server from the client. (Prediction: silence — a host that does not forward drops the packet without an ICMP error. `ip_forward=1` is what makes Linux a router.)
2. Turn forwarding back on and delete the server's return route: `netlab exec … server -- ip route del 198.18.5.0/24`. Capture on the router's `eth1` while pinging. (Requests cross the router; replies never leave the server, which has no route back — asymmetric failure, the most common cloud routing mistake.)
3. Start from a fresh lab (`netlab down`, then `up`, so the client has no cached path MTU). Run `netlab exec … router -- iptables -A OUTPUT -p icmp --icmp-type fragmentation-needed -j DROP` and repeat check 5's ping, then `ip route get 198.19.5.2` on the client. (No "Frag needed" arrives, ping reports plain 100% loss, and the route has no `mtu 1400`: you built a PMTUD black hole.) The rule goes in `OUTPUT`, not `FORWARD`, because the router *generates* that ICMP message itself; it is not forwarding it. A firewall further along the path would drop it in its `FORWARD` chain instead — the result at the client is identical, which is why black holes are hard to locate.

---

### Lab Part 5: IPv6 Configures Itself

[`ipv6.json`](../labs/netlab/ipv6.json) builds the same three-node shape in IPv6. The client gets **no address and no route by hand**; the router runs `dnsmasq` as an RA daemon advertising `fd42:1::/64`, and the server-side link has an MTU of 1280, the IPv6 minimum.

```bash
sudo ./bin/netlab run ../netlab/ipv6.json
```

The checks, in order, and what to look for in each (strings in `code` are the ones the lab asserts):

| Check | What it proves | Evidence to read |
| --- | --- | --- |
| 1 | The client solicits a router and gets an RA | `ra.pcap.txt`: `fe80::… > ff02::2: … router solicitation`, then `router advertisement` with `router lifetime 60s` and the prefix `fd42:1::/64` |
| 2 | The default route came from the RA | `default via fe80::… dev eth0 proto ra` — a link-local next hop |
| 3 | SLAAC built a global address | `inet6 fd42:1::…/64 scope global dynamic` beside `inet6 fe80::…/64 scope link` |
| 4 | Routed IPv6 works with zero manual config | `2 received` from `fd42:2::2` |
| 5 | NDP replaced ARP | `ip -6 neigh`: the router's `fe80::` address with `lladdr` and `router` |
| 6 | Routers do not fragment IPv6 | `ptb.pcap.txt`: `ICMP6, packet too big, mtu 1280` from the router |
| 7 | The sender cached the path MTU | the next oversize ping fails locally |
| 8–10 | Duplicate address detection | `dad.pcap.txt`: an NS from `::` to `ff02::1:ff00:99`, "who has fd42:1::99"; the router's NA "tgt is fd42:1::99"; then `ip -6 addr` shows `dadfailed` |

> **Not run in the course's build environment.** The lab file is validated by the test suite and its commands are a faithful port of the earlier shell fixture, but the environment used to write this module boots with IPv6 disabled, so netlab reports `SKIP: IPv6 is disabled on the kernel command line (ipv6.disable=1)`. On the course VM (stock Ubuntu kernel) it runs. If a check's expected text differs from your tcpdump or iproute2 version, the saved evidence shows the real output — update the lab file and note it in [implementation.md](../implementation.md).

Things to notice beyond the checks:

- The RS is sent from a **link-local** source, and the RA is too. Routers are identified by link-local addresses, which is why an IPv6 default route says `via fe80::…`. If you ever configure one by hand it needs the interface: `ip -6 route add default via fe80::1 dev eth0`.
- The DAD probe's source is `::`, the unspecified address: the client cannot use the address it is testing.
- `ping -6 fe80::…` without `%eth0` fails, because the kernel cannot know which link you mean; with the zone it works. The same link-local address can exist on every link.
- `ip -6 route get fd42:2::2` after check 6 should show `mtu 1280`, the IPv6 version of check 6 in Part 3.

---

### Lab Part 6 (Internet Required): Your Real Path

On a VM with internet access, compare what you built with the real thing:

```bash
traceroute -n 1.1.1.1                       # TTL expiry, hop by hop (Module 7 explains the * * * lines)
tracepath -n 1.1.1.1                        # path MTU along the way
dig +short A example.com; dig +short AAAA example.com
curl -4 -sS -o /dev/null -w '%{remote_ip} %{time_connect}\n' https://example.com
curl -6 -sS -o /dev/null -w '%{remote_ip} %{time_connect}\n' https://example.com
```

If `curl -6` fails with "Couldn't connect to server" while `curl -4` works, your host has no working IPv6 route — normal on many networks. If it *hangs* until a timeout, IPv6 is half-configured (an address and a default route, but traffic is dropped somewhere): exactly the condition that makes dual-stack clients without Happy Eyeballs slow (5.5).

---

## Real-World Troubleshooting Scenario

### Scenario: "Containers in a New Subnet Can't Reach the Database"

**The situation:**

In this synthetic case the application VPC is 10.0.0.0/16. New nodes use
10.0.20.0/24; the RDS database is 10.50.0.20 in a separate, non-overlapping
10.50.0.0/16 peered VPC. Existing nodes can connect, new nodes cannot.

**How IP fundamentals solve this in minutes:**

**Step 1 — Verify the pod can reach the database IP**

```bash
kubectl exec <pod-on-new-node> -- nc -zvw 3 10.50.0.20 5432
# Illustrative result: connect timeout; RDS is not an ICMP ping target
```

This bypasses DNS for the test and establishes a TCP connect failure, not a unique
layer or cause. Verify that this is still the intended database address.

**Step 2 — Check what the pod sees**

```bash
kubectl exec -it <pod-on-new-node> -- ip route show
# Result: default via 10.0.20.1 dev eth0
#         10.0.20.0/24 dev eth0 scope link
```

The OS route only identifies its next hop. The subsequent cloud route and return
path to the observed pod or SNAT source must be inspected separately. (With the
AWS VPC CNI, a pod's table instead reads `default via 169.254.1.1 dev eth0`: a
link-local placeholder gateway, answered by the node, which does the real routing.
Seeing 169.254 there is normal, not a DHCP failure.)

`subnet 10.0.0.0/16 10.50.0.20` confirms the database is *outside* the VPC CIDR,
so the `local` route cannot carry the traffic; some other route must.

**Step 3 — Check the VPC route table for the new subnet**

```bash
aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=subnet-newsubnet-id" \
  --query 'RouteTables[0].Routes'
```

**Configuration evidence in this case:** the effective old table has
`10.0.0.0/16 -> local` and `10.50.0.0/16 -> pcx-...`. The new table lacks the
peering route. If an explicit subnet association query returns no result, inspect
the VPC's main route table, which implicitly serves unassociated subnets. The
database VPC's return route and applicable policies must also permit the flow.

**The IP knowledge that made this fast:**

1. A bounded TCP probe tests the intended destination without a new DNS lookup.
2. Effective cloud routes include explicit or implicit main-table associations.
3. A different subnet alone does not prove a missing route; the absent route to
   the separate peered VPC is the specific configuration difference here.

**The fix:** Associate the new subnet with the correct route table (or add the missing peering route to the new route table).

**Verify:** Repeat the database-aware test from the original pod after the narrow
route correction and retain route, policy, and packet evidence. No diagnosis time
is guaranteed by this synthetic example.

---

## Common Failure Patterns

| Symptom                                | IP-Related Cause                              | How to Confirm                                     | Fix                                                 |
| -------------------------------------- | --------------------------------------------- | -------------------------------------------------- | --------------------------------------------------- |
| `169.254.x.x` as only IPv4 address     | Link-local configuration or DHCP fallback     | Compare intended addressing and DHCP client logs   | Repair DHCP only if dynamic addressing was intended |
| `Network is unreachable`               | No route to destination (nothing was sent)    | `ip route get <IP>`                                | Add the missing route                               |
| `Destination Host Unreachable` from your own IP | On-link destination, no ARP/ND reply | `ip neigh` shows FAILED/INCOMPLETE                 | Fix the address, VLAN, or host (Modules 4, 6)        |
| Silence; requests seen, no replies     | Missing or asymmetric return route            | Capture on both sides of the router                | Add the return route                                |
| Ping works, large transfers hang       | PMTUD black hole — DF set, ICMP blocked       | `ping -s 1472 -M do <IP>`; `ip route get` has no `mtu` | Allow ICMP type 3 code 4 and ICMPv6 type 2, or clamp MSS |
| Slow or hanging connects for some clients | IPv6 broken on the path; client tries AAAA first | `curl -4` vs `curl -6`                        | Fix IPv6; remove the AAAA record only as a stopgap  |
| IPv6 address unusable, `dadfailed`     | Another host already has the address          | `ip -6 addr` shows `dadfailed`                     | Remove the duplicate                                |
| Overlapping IPs after VPN/peering      | Two networks use the same CIDR range          | `ip route show` — same prefix, different next hops | Re-address one of the networks                      |
| Container can't reach external service | NAT not configured for container subnet       | `ip route show` in container, check NAT rules      | Add SNAT/masquerade rule                            |
| Container on AWS can't get IAM credentials | IMDSv2 response hop limit 1: the token reply expires one hop short of the container | SDK error; instance metadata options (`HttpPutResponseHopLimit`) | Raise the hop limit to 2, or use pod/task IAM roles |
| Packets arrive at wrong host           | IP address conflict — duplicate IPs           | `arping -D <IP>` — check for duplicate replies     | Remove the duplicate                                |

---

## Summary

- IP's core jobs are addressing and routing, supported by TTL, the protocol number, and (IPv4) fragmentation. It guarantees nothing about delivery. Reliability, ordering, and flow control are TCP's job; ICMP reports some, not all, failures.
- IPv4 addresses are 32-bit, written in dotted decimal. Immediately classify any address as public, private, loopback, link-local, or multicast. This determines whether NAT is involved and what firewall to check.
- CIDR replaced classful addressing. The `/n` prefix tells you where the network/host boundary is. Everything you need — network address, broadcast, host range — can be calculated from the prefix length.
- The IPv4 header is 20 bytes minimum. TTL prevents routing loops and enables traceroute. The DF bit and fragmentation fields are central to PMTUD. The Protocol field tells the OS whether to hand the payload to TCP, UDP, or ICMP.
- IPv6 uses 128-bit addresses in colon-hex notation, /64 subnets, and a fixed 40-byte header with no checksum and no router fragmentation. Router Advertisements supply the default route (via a link-local address) and, with SLAAC, the address itself. Link-local (fe80::) addresses exist on every interface and carry Neighbor Discovery, which replaces ARP. Blocking ICMPv6 breaks IPv6.
- The errors tell you where to look: "Network is unreachable" is your own routing table; "Destination Host Unreachable" from your own address is ARP/ND on your link; ICMP from a router's address names the router that gave up; silence means a drop somewhere you must find with captures.
- Special addresses provide context, not a unique diagnosis: link-local can be
  intentional; 0.0.0.0 has different socket/route meanings; metadata is provider-specific.
- Subnetting organizes addresses. Routes, neighbor reachability, return paths,
  firewall policy, and listener state jointly determine whether a flow succeeds.

## What's Next

Module 6 covers ARP — Address Resolution Protocol — the bridge between Layer 3 (IP addresses) and Layer 2 (MAC addresses). Now that you understand IP addressing, the next question is: when a host wants to send a packet to an IP address on the same subnet, how does it find the destination's MAC address? ARP is the answer, and understanding it is critical for diagnosing local network failures where hosts cannot communicate even though their IP configuration is correct.

---

## Quick Reference Card

```
MODULE 5: IP — THE INTERNET PROTOCOL — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

WHAT IP DOES
  Addressing    Logical IDs (IP addresses) for every interface
  Routing       Hop-by-hop forwarding   using routing tables

WHAT IP DOES NOT DO
  No reliability, no ordering, no flow control, no connections.
  TCP adds these. UDP does not.

PRIVATE ADDRESS RANGES (RFC 1918)
  10.0.0.0/8        16 million addresses   Cloud VPCs, enterprises
  172.16.0.0/12     1 million addresses    Docker carves its networks from it
  192.168.0.0/16    65K addresses          Home networks, labs

SPECIAL ADDRESSES
  127.0.0.1         Loopback           Never leaves the host
  0.0.0.0           Default route / INADDR_ANY (context-dependent)
  169.254.x.x       Link-local         Check intended mode; metadata is provider-specific
  198.18.0.0/15     Benchmarking       This course's labs
  192.0.2/24, 198.51.100/24, 203.0.113/24   Documentation — never real
  255.255.255.255   Broadcast          All hosts on local network
  224.0.0.0/4       Multicast          One-to-many

IPv4 HEADER KEY FIELDS (20 bytes)
  TTL              Hop limit — prevents loops. Enables traceroute.
  Protocol         6=TCP, 17=UDP, 1=ICMP
  Flags            DF=Don't Fragment, MF=More Fragments ("[+]" in tcpdump)
  Fragment offset  8-byte units in the header; tcpdump prints bytes
  Src/Dst IP       Source and destination addresses

CIDR QUICK MATH
  /24 = 256 addresses, 254 usable hosts
  /20 = 4,096 addresses, 4,094 usable hosts
  /16 = 65,536 addresses, 65,534 usable hosts
  Ordinary subnet: hosts = 2^(32 - prefix) - 2; /31 and /32 are exceptions
  Cloud subnets: AWS and Azure reserve 5, Google Cloud 4 (primary range)

SUBNETTING FORMULA
  Network addr   = IP AND mask        (all host bits = 0)
  Broadcast      = IP OR NOT(mask)    (all host bits = 1)
  First usable   = Network + 1
  Last usable    = Broadcast - 1

DEFAULT TTL VALUES
  64     Linux
  128    Windows
  255    Network equipment

IPv6 KEY FACTS
  128-bit addresses. Colon-hex notation. :: shortens zero runs.
  Link-local (fe80::) auto-configured on every interface.
  No broadcast — multicast instead.
  SLAAC = addresses without DHCP. Default route comes from the RA: via fe80::…
  NDP (ICMPv6 133–137) replaces ARP. Routers send Packet Too Big, never fragment.
  Subnets are /64. Link-local needs a zone: fe80::1%eth0

ESSENTIAL COMMANDS
  ip addr show                All addresses on all interfaces
  ip route show               Full routing table
  ip route get <IP>           Which route would be used
  ip -6 addr show             IPv6 addresses
  ping -c 5 <IP>              Test Layer 3 reachability
  ping -s 1472 -M do <IP>     Test PMTUD (max payload before frag)
  traceroute -n <IP>          Map the path, hop by hop
  tracepath -n <IP>           Path MTU along the path
  subnet <CIDR> [-cloud aws]  Subnet properties (course tool; ipcalc is similar)
  ip -6 neigh / ip -6 route   NDP cache; RA-learned routes (proto ra)
  dig A <domain>              IPv4 address lookup
  dig AAAA <domain>           IPv6 address lookup

WHEN YOU SEE THIS             THINK THIS
  Network is unreachable       No route — check ip route
  169.254.x.x address          Check addressing mode and DHCP evidence
  Large transfers hang          PMTUD black hole — check ICMP/MTU
  TTL=1 in capture             Traceroute probe, or a looping packet about to expire
  Frag needed (mtu = N)        A router with a smaller link; PMTUD is working
  dadfailed                    Duplicate IPv6 address on the link
  Overlapping CIDRs            VPN/peering conflict — re-address one side
  curl -6 fails, -4 works      IPv6 broken — check AAAA, firewall, route
```

---

## References

- RFC 791 — Internet Protocol (IPv4) — the original specification
- RFC 8200 — Internet Protocol, Version 6 (IPv6) Specification
- RFC 1918 — Address Allocation for Private Internets
- RFC 4632 — Classless Inter-domain Routing (CIDR)
- RFC 4862 — IPv6 Stateless Address Autoconfiguration (SLAAC)
- RFC 3927 — Dynamic Configuration of IPv4 Link-Local Addresses (169.254.x.x)
- RFC 6598 — IANA-Reserved IPv4 Prefix for Shared Address Space (100.64.0.0/10)
- RFC 5737 — IPv4 Address Blocks Reserved for Documentation
- RFC 2544 — Benchmarking Methodology (198.18.0.0/15)
- RFC 3021 — Using 31-Bit Prefixes on IPv4 Point-to-Point Links
- RFC 6864 — Updated Specification of the IPv4 ID Field
- RFC 4861 — Neighbor Discovery for IPv6
- RFC 4890 — Recommendations for Filtering ICMPv6 Messages in Firewalls
- RFC 5952 — A Recommendation for IPv6 Address Text Representation
- RFC 7217 — Stable and Opaque Interface Identifiers (SLAAC)
- RFC 8981 — Temporary Address Extensions (IPv6 privacy addresses)
- RFC 8106 — IPv6 Router Advertisement Options for DNS Configuration
- RFC 8305 — Happy Eyeballs Version 2: Better Connectivity Using Concurrency
- `man ip-address` — Linux IP address management
- `man ip-route` — Linux routing table management
- `man ip-netns` — Linux network namespace management
- Cloud subnet reservations: [AWS subnet sizing](https://docs.aws.amazon.com/vpc/latest/userguide/subnet-sizing.html), [Azure virtual network FAQ](https://learn.microsoft.com/azure/virtual-network/virtual-networks-faq), [Google Cloud subnets](https://cloud.google.com/vpc/docs/subnets)
- Course tools: [`cmd/subnet`](../labs/go/cmd/subnet), [`netlab/ip.json`](../labs/netlab/ip.json), [`netlab/ipv6.json`](../labs/netlab/ipv6.json)
