# Module 3: Networking Fundamentals

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Modules 0–2; a Linux VM with root for the namespace labs
> **Estimated time:** 3.5 hours

## Overview

Before you can read a routing table, interpret a packet capture, or explain why two containers cannot reach each other, you need a working vocabulary. Not the kind you memorize for a test — the kind that lets you look at a network and immediately understand what is talking to what, at which layer, and why a packet is or is not arriving where it should.

Most networking problems that get escalated as "the network is broken" turn out to be one of five things: the wrong IP assigned, a missing or incorrect route, a MAC address the switch does not know about, a bandwidth or latency mismatch that is being misread as packet loss, or a MTU problem causing silent drops. All five of these are invisible unless you know what to look for at each layer. This module builds that foundation.

This module is deliberately practical. Every concept is introduced with the tool you use to observe it. By the end you will be able to sit at a Linux machine, examine its network state completely — interfaces, addresses, routes, neighbors — and explain exactly what you are looking at and why it matters.

## What You Will Build Toward

- Read the output of `ip addr`, `ip route`, and `ip neigh` without referring to documentation
- Explain the difference between a packet, a frame, and a segment — and name the layer each belongs to
- Distinguish between bandwidth, latency, and throughput and identify which is the constraint in a given scenario
- Describe the role of switches, routers, and hubs and identify which layer each operates at
- Explain what a MAC address is, where it comes from, and when it matters for troubleshooting
- Say what changes at each hop (MAC addresses, TTL) and what doesn't (IP addresses, unless NAT)
- Explain how a host decides whether to send a frame straight to the destination or to its gateway
- Build a bridge, a VLAN-aware switch, and a looped two-switch network with STP using `netlab`, and read their forwarding state
- Measure latency and throughput between two hosts using `ping` and `iperf3`, and explain why gigabit TCP tops out near 941 Mbit/s
- Recognise when NIC offloads make a capture show "packets" larger than the MTU

---

## 3.1 What Is a Network?

### What This Is

A network is a set of devices connected in a way that allows them to exchange data. That definition is almost useless on its own — the useful part is understanding what "connected" means at different scales, because the problems look different at each scale.

Networks are categorized by geographic scope. These categories are not just taxonomy — they describe who owns and controls the infrastructure, what failure modes are possible, and what tools are relevant for diagnosis.

### How It Works

The categories you will encounter in practice:

**LAN (Local Area Network)** — a network within a single physical location, typically a building or floor. You own and control everything: the switches, the cabling, the addresses. Latency is sub-millisecond. Bandwidth is 1–100 Gbps. Failures are physical: a bad cable, a misconfigured switch port, a VLAN assignment error.

**WAN (Wide Area Network)** — a network spanning multiple locations, connected over leased lines, MPLS circuits, or the public internet. You own the endpoints; a carrier owns the middle. Latency is tens to hundreds of milliseconds. Bandwidth is measured in Mbps to Gbps. Failures are split between your equipment and the carrier's.

**The Internet** — the global network of networks connected via BGP. No single entity owns it. Latency and bandwidth vary enormously depending on path. Failures can come from anywhere along the path, including at peering points you have never heard of.

**Cloud virtual network (VPC, VNet)** — a software-defined private network inside a cloud provider. Behaves like a LAN in many ways but is implemented in software, and the physical layer is invisible to you. Within one availability zone, round trips are typically well under a millisecond; between zones in a region, providers design for low single-digit milliseconds. Cross-region traffic usually rides the provider's own backbone rather than the public internet, with latency set mostly by distance.

### Diagram

```
NETWORK SCOPE

  ┌─────────────────────────────────────────────────────────────┐
  │  LAN                                                        │
  │  ┌──────┐  ┌──────┐  ┌──────┐                              │
  │  │  PC  │  │  PC  │  │Server│  — sub-ms latency            │
  │  └──┬───┘  └──┬───┘  └──┬───┘    1–100 Gbps               │
  │     └─────────┴──────────┘        You own everything        │
  │              │                                              │
  └──────────────┼──────────────────────────────────────────────┘
                 │ Router / Gateway
  ┌──────────────┼──────────────────────────────────────────────┐
  │  WAN         │                                              │
  │    [Your Site] ──── [Carrier Network] ──── [Remote Site]   │
  │                                                             │
  │    You own endpoints. Carrier owns the middle.              │
  │    10ms–100ms latency. Carrier SLA for uptime.              │
  └─────────────────────────────────────────────────────────────┘
                 │
  ┌──────────────┼──────────────────────────────────────────────┐
  │  Internet    │                                              │
  │   Multiple autonomous systems connected via BGP             │
  │   No single owner. Variable latency and reliability.        │
  │   [AS1] ── [IXP] ── [AS2] ── [CDN] ── [AS3: Cloud]        │
  └─────────────────────────────────────────────────────────────┘
                 │
  ┌──────────────┼──────────────────────────────────────────────┐
  │  Cloud VPC   │                                              │
  │   Software-defined. Physical layer invisible to you.        │
  │   ┌─────────────────────────────────────────────────┐      │
  │   │  VPC: 10.0.0.0/16                               │      │
  │   │  ┌──────────────┐    ┌──────────────┐           │      │
  │   │  │ Subnet A     │    │ Subnet B     │           │      │
  │   │  │ 10.0.1.0/24  │    │ 10.0.2.0/24  │           │      │
  │   │  └──────────────┘    └──────────────┘           │      │
  │   └─────────────────────────────────────────────────┘      │
  └─────────────────────────────────────────────────────────────┘
```

### What You Will See in Production

The classification that matters most for troubleshooting is whether you own both ends of the path. If you do, you can capture traffic at both ends. If you do not — because the path crosses the internet or a carrier network — you can only measure from your end and infer what is happening in the middle. `mtr` is the tool for that inference.

---

## 3.2 Network Topologies

### What This Is

Topology describes how nodes in a network are physically or logically connected. The topology determines how failures propagate: a failure at one point in a bus topology takes down every device on the segment; a failure in a star topology takes down only the devices connected to the failed node.

You will not design topologies often. You will diagnose them often. Understanding topology tells you which failure would produce the symptom you are seeing.

### How It Works

**Bus** — all devices share a single cable. Any signal is heard by everyone. A break anywhere splits the network. Used in early Ethernet (coaxial cable). Not used in modern deployments.

**Ring** — each device connects to two neighbors, forming a loop. A single failure breaks the ring unless the protocol supports dual-ring failover (SONET, FDDI). Rarely seen in new deployments.

**Star** — every device connects to a central switch. The switch is a single point of failure but individual device failures are isolated. This is modern Ethernet. Every port is independent.

**Mesh** — every device connects to multiple others. Partial mesh (some redundant paths) is common in data center spine-leaf architecture. Full mesh does not scale.

**The internet** is a loosely connected partial mesh of autonomous systems. There is no single point of failure at the global level, but individual paths can fail.

### Diagram

```
BUS (historical — early Ethernet)
  ───[A]───[B]───[C]───[D]───
  Break here → splits network. Collision domain: everyone.


RING (SONET, FDDI — rare now)
  [A]──[B]
   │    │
  [D]──[C]
  Break in one link → traffic takes reverse path (if dual-ring)


STAR (modern Ethernet — what you use every day)
         [Switch]
       /   |  |   \
     [A]  [B] [C]  [D]

  Switch failure → all devices lose connectivity
  Individual device failure → only that device affected
  Common office LAN pattern; cloud subnets are provider abstractions


SPINE-LEAF (data center — modern)
  Spine 1: connected to Leaf1, Leaf2, Leaf3, Leaf4
  Spine 2: connected to Leaf1, Leaf2, Leaf3, Leaf4
  Leaf1 -> servers   Leaf2 -> servers
  Leaf3 -> servers   Leaf4 -> servers
  No spine-to-spine link is required in this two-tier fabric.

  Same-leaf traffic traverses one switch; cross-leaf traffic traverses
  leaf -> spine -> leaf (three switches, two inter-switch links).
  Lose a spine: traffic re-routes through remaining spine.
  Lose a leaf: only that rack is affected.
```

### What You Will See in Production

Spine-leaf fabrics are common in data centers, but provider topology and failure
domains vary. An AZ impairment announcement alone does not identify a leaf switch
or rack. Use measured affected paths and the provider's incident evidence to scope
impact and decide whether to fail over.

---

## 3.3 Bandwidth, Latency, and Throughput

### What This Is

These three terms are used interchangeably in incident reports and they should not be. They measure different things, they fail independently, and they require different tools to diagnose. Confusing them leads to wrong fixes — adding bandwidth when latency is the problem, or optimizing for latency when the real constraint is throughput.

### How It Works

**Bandwidth** is the capacity of a link — how much data it can carry per unit of time. A 1 Gbps Ethernet link has 1 Gbps of bandwidth regardless of whether it is carrying 1 byte or 1 gigabyte. Bandwidth is a property of the link, not of a specific flow.

**Latency** is the time data takes to get from one point to another. Tools almost always report the **round-trip time (RTT)**, because one-way delay needs synchronised clocks to measure. Latency does not depend on how much data you send: a path through a geostationary satellite has an RTT of about 600 ms whatever its capacity, simply because the satellite is 36,000 km up. A local Ethernet connection has sub-millisecond RTT. Latency comes from distance (light in fibre covers about 200 km per millisecond), the time to put bits on each link, processing, and queuing at each hop.

**Throughput** is what you actually measure between two hosts — how much data was transferred per second over a real transfer. Throughput is always less than or equal to bandwidth. It is constrained by the slowest link in the path (bandwidth bottleneck), by round-trip latency combined with TCP window size (the bandwidth-delay product problem), and by packet loss causing retransmissions.

**Goodput** is throughput minus overhead — only the application payload bytes, excluding headers and retransmitted segments. This is the number your application actually experiences, and even on a perfect link it is well below the link speed. Each full-size Ethernet frame costs 1,538 bytes of wire time (8 preamble, 14 header, 1,500 payload, 4 FCS, 12 inter-frame gap) to carry 1,448 bytes of TCP payload (1,500 minus 20 IP and 32 TCP bytes with the timestamp option Linux uses). So the best possible single-flow TCP goodput on 1 Gbit/s Ethernet is 1,448 / 1,538 × 1,000 ≈ **941 Mbit/s** — exactly the figure `iperf3` reports on a healthy gigabit link. A result of 941 is a perfect link, not a 6% problem.

### Diagram

```
BANDWIDTH vs LATENCY vs THROUGHPUT

  Link capacity:
  ══════════════════════════════════════════ 1,000 Mbit/s (bandwidth)

  Best possible single TCP flow, perfect link:
  ═══════════════════════════════════════    941 Mbit/s goodput
                                        ↑
                          Ethernet framing, IP and TCP headers:
                          1,448 payload bytes per 1,538 on the wire

  What you measure on a real path is lower again when:
  - the TCP window is smaller than bandwidth × RTT (BDP, below)
  - packets are lost, so TCP retransmits and slows down
  - another flow shares the bottleneck
  - the application, CPU, or disk can't keep up


BANDWIDTH-DELAY PRODUCT — why high latency limits throughput

  Data in flight needed to fill a path at the chosen rate and RTT:

    BDP = Bandwidth × Round-Trip Time

  Example:
    Link: 1 Gbps = 125 MB/s
    RTT: 100ms = 0.1s
    Target in-flight: 125 MB/s × 0.1s = 12.5 MB = 12,500 KB

  If TCP window size is smaller than 12.5 MB,
  TCP must pause and wait for ACKs before sending more.
  The link sits idle. Throughput is limited by window, not bandwidth.

  The cap works the other way too: maximum throughput = window / RTT.
  A 4 MB window over a 100 ms path caps one flow at
    4 MB × 8 / 0.1 s = 320 Mbit/s
  however fast the link is. The same window over a 2 ms path allows
  16 Gbit/s. Distance, not link speed, sets the ceiling.
```

### What You Will See in Production

A transfer can be limited by receive window, congestion window, application pacing,
CPU, storage, policy, or path capacity. Compare the actual in-flight window and RTT
to the required BDP before changing buffers. BDP is not a universal ceiling on bytes
in flight; excess data can accumulate in queues.

A small transfer can spend much of its lifetime in setup or slow start, but its
size alone does not prove that explanation. Compare repeated longer transfers,
RTT, congestion state, server work, and loss on the same path.

### Commands and Tools

#### `ping`

**Purpose:** Measure round-trip latency to a host using ICMP Echo
**When to use it:** First tool when a connection is slow or unreachable — confirms whether the host is reachable and measures baseline RTT

```bash
# Basic latency measurement — sends 10 packets
ping -c 10 8.8.8.8

# Larger packets — tests whether MTU issues appear under load
ping -c 10 -s 1400 8.8.8.8

# Keep stress experiments bounded and inside the isolated lab topology.

# Set packet interval to 0.2s instead of default 1s
ping -c 20 -i 0.2 8.8.8.8
```

**Reading the output:**

```
PING 8.8.8.8 (<HOSTNAME>): 56 data bytes

64 bytes from <IP>: icmp_seq=<N> ttl=<TTL> time=<RTT> ms

--- <HOST> ping statistics ---
<N> packets transmitted, <N> received, <LOSS>% packet loss
rtt min/avg/max/mdev = <MIN>/<AVG>/<MAX>/<MDEV> ms
```

- `ttl` — hops remaining when the packet arrived; starts at 64 or 128 depending on OS; low values mean many hops
- `time` — round-trip time in milliseconds; this is latency
- `mdev` is the standard deviation of RTT samples; it describes variability,
  not one-way latency or the application's complete jitter distribution
- `packet loss` — any sustained loss warrants investigation. TCP reacts to loss by slowing down, so even 1% matters: on a 100 ms path, 1% loss limits one TCP flow to roughly 1.4 Mbit/s whatever the link speed (the Mathis model, Module 14)

**Gotchas:**

- ICMP is often rate-limited or deprioritized by routers — a high `ping` RTT does not always mean TCP traffic is slow
- Some hosts block ICMP entirely — `ping` timeout does not mean the host is down
- `ping` measures round-trip time; it does not measure the time for a TCP connection to be established

#### `iperf3`

**Purpose:** Measure actual throughput between two hosts
**When to use it:** When you need to know the real transfer rate of the network path, independent of application behavior

```bash
# On the server (the machine you are testing toward):
iperf3 -s

# On the client — run a 10-second TCP throughput test:
iperf3 -c <server_ip> -t 10

# UDP test — sends at exactly the rate you ask for (here 100 Mbit/s) and reports
# loss and jitter at that rate. It does not discover capacity; raise -b step by
# step and watch where loss starts:
iperf3 -c <server_ip> -u -b 100M -t 10

# Reverse test — server sends to client (tests download path):
iperf3 -c <server_ip> -R -t 10

# Parallel streams — simulates multiple simultaneous connections:
iperf3 -c <server_ip> -P 4 -t 10
```

**Reading the output:**

```
Connecting to host <SERVER_IP>, port 5201
[  5] local <CLIENT_IP> port <PORT> connected to <SERVER_IP> port 5201
[ ID] Interval        Transfer     Bitrate
[  5]  0.00-1.00  sec  <MB> MBytes  <Mbps> Mbits/sec
[  5]  1.00-2.00  sec  <MB> MBytes  <Mbps> Mbits/sec
...
- - - - - - - - - - - - - - - - - - - - -
[  5]  0.00-10.00 sec  <MB> MBytes  <Mbps> Mbits/sec   sender
[  5]  0.00-10.00 sec  <MB> MBytes  <Mbps> Mbits/sec   receiver
```

- `Bitrate` per interval — watch this for consistency; drops mid-transfer indicate congestion or packet loss
- TCP sender/receiver summaries can use different measurement intervals or include
  bytes still in flight. They do not alone prove loss; inspect retransmission and
  capture evidence. iperf3 counts application bytes, not all on-wire headers.
- For UDP tests, also check `Lost/Total Datagrams` and `Jitter`

**Gotchas:**

- `iperf3` measures the network path, not your application's performance — application overhead is separate
- Run the server on the machine you are testing toward, not toward your own machine, to test the path you care about
- Cloud security groups and firewalls must allow port 5201 TCP (and UDP if testing UDP)

---

## 3.4 Packets, Frames, and Segments

### What This Is

These three words are used interchangeably in casual conversation and that causes real confusion during incidents. Each word refers to a unit of data at a specific layer of the network stack. Using the right word tells you immediately which layer you are talking about and which headers are relevant.

### How It Works

**Frame** — Layer 2 (Data Link). The unit that Ethernet switches forward. Contains a destination MAC address, source MAC address, an EtherType indicating what's inside (0x0800 IPv4, 0x86DD IPv6, 0x0806 ARP, 0x8100 a VLAN tag), and the payload, followed by a 4-byte frame check sequence (FCS, a CRC). Maximum size on standard Ethernet: 1,518 bytes (14 header + 1,500 payload + 4 FCS), or 1,522 with an 802.1Q VLAN tag. On the wire each frame also costs an 8-byte preamble and a 12-byte inter-frame gap. Jumbo frames carry up to about 9,000 bytes of payload where every device on the path supports them.

**Packet** — Layer 3 (Network). The unit that routers forward. Contains a source IP address, destination IP address, protocol number (TCP=6, UDP=17, ICMP=1), TTL, and the payload. Maximum size: 65,535 bytes theoretically, but fragmentation kicks in above the path MTU (usually 1500 bytes).

**Segment** — Layer 4 (Transport, TCP specifically). Contains source and destination ports, sequence and acknowledgment numbers, TCP flags, window size, and the payload. A segment is what TCP uses to track reliable delivery. The equivalent UDP unit is called a **datagram**.

**Message or Request** — Layer 7 (Application). An HTTP request, a DNS query, a TLS record. This is what your application actually sends and receives.

### Diagram

```
THE SAME DATA AT EACH LAYER

Application sends:  GET /index.html HTTP/1.1\r\nHost: example.com\r\n\r\n

Layer 7 — Message:
┌──────────────────────────────────────────────────────┐
│  HTTP Request: GET /index.html HTTP/1.1              │
│  Host: example.com                                   │
└──────────────────────────────────────────────────────┘

Layer 4 — Segment (TCP wraps the HTTP message):
┌─────────────────────┬────────────────────────────────┐
│  TCP Header         │  HTTP Request                  │
│  Src: 54321         │  GET /index.html HTTP/1.1      │
│  Dst: 443           │  Host: example.com             │
│  Seq: 1001  Ack: 1  │                                │
└─────────────────────┴────────────────────────────────┘

Layer 3 — Packet (IP wraps the TCP segment):
┌────────────────┬─────────────────────┬────────────────┐
│  IP Header     │  TCP Header         │  HTTP Request  │
│  Src: 10.0.0.5 │  Src: 54321         │  GET /...      │
│  Dst: 203.0.113│  Dst: 443           │                │
│  TTL: 64       │  Seq: 1001          │                │
│  Proto: 6(TCP) │                     │                │
└────────────────┴─────────────────────┴────────────────┘

Layer 2 — Frame (Ethernet wraps the IP packet):
┌──────────────┬────────────┬─────────────────────┬────────────────┬──────┐
│  Eth Header  │  IP Header │  TCP Header         │  HTTP Request  │  CRC │
│  Src MAC     │  Src IP    │  Src Port           │  GET /...      │      │
│  Dst MAC     │  Dst IP    │  Dst Port           │                │      │
│  Type: 0x0800│            │                     │                │      │
└──────────────┴────────────┴─────────────────────┴────────────────┴──────┘

                                                       Sent on the wire →
```

### What You Will See in Production

When Wireshark shows you "TCP Retransmission," it is working at the segment level — it has noticed that a segment with a specific sequence number was sent twice. When a router drops a packet because the TTL reached zero, it is working at the packet level. When a switch forwards traffic, it reads only the frame — it has no idea what IP addresses or TCP ports are inside.

Understanding the layer tells you which counters and devices to check — but be careful about the direction of inference. A **retransmission** says a segment was lost (or delayed) *somewhere* on the path: a NIC ring buffer, a switch queue, a router, a firewall, or the receiving host. TCP sees the loss end to end; it cannot say where. To locate it, compare per-layer evidence: interface and switch-port counters for frames (`ip -s link`, `ethtool -S`), router and firewall counters for packets, and captures from both ends of the path (Module 21).

---

## 3.5 Network Interfaces and MAC Addresses

### What This Is

Ethernet interfaces use MAC addresses; loopback, point-to-point, and some tunnel
interfaces do not have an Ethernet MAC. Physical adapters may have a factory address,
while virtual or privacy-sensitive interfaces often use locally administered or
randomized addresses. An address can change and is not authenticated identity.

### How It Works

A conventional Ethernet MAC is 48 bits, for example `00:1a:2b:3c:4d:5e`. Two bits
of the **first octet** tell you what kind of address it is:

- **Bit 0 (I/G):** 1 means a group address — multicast or broadcast. `01:00:5e:…`
  (IPv4 multicast), `33:33:…` (IPv6 multicast), and `ff:ff:ff:ff:ff:ff` (broadcast)
  all have it set. A frame can only ever be *sent from* an individual address.
- **Bit 1 (U/L):** 1 means **locally administered** — chosen by software, not
  burned in by a manufacturer. Any first octet ending in 2, 6, A, or E in hex has
  it set, such as `02:42:…`, `52:54:…`, or `ea:51:…`. Every veth in this module's labs
  has one, as do Docker containers, many VMs, and phones using MAC randomisation.

Globally administered addresses (bit 1 clear) start with a vendor prefix (OUI)
assigned by the IEEE. A locally administered address says nothing about the
manufacturer, and any address can be duplicated or spoofed.

A learning switch records each incoming frame's **source** MAC and ingress port in
its forwarding database (often implemented in CAM), scoped to the VLAN. It forwards
known unicast to the destination's port; unknown unicast is normally flooded to
eligible ports in that VLAN except ingress. A response is not required for learning.

### Diagram

```
MAC ADDRESS STRUCTURE

  00:1a:2b : 3c:4d:5e
  ─────────   ─────────
  OUI         Device ID
  (Manufacturer)  (Unique per device)

  Prefixes you will meet:
  00:0c:29, 00:50:56   VMware (IEEE OUIs)
  00:15:5d             Microsoft Hyper-V (IEEE OUI)
  52:54:00             QEMU/KVM default — locally administered (bit 1 set)
  02:42:…              common on older Docker — locally administered
  ff:ff:ff:ff:ff:ff    broadcast (bit 0 set)


SWITCH FORWARDING — how a switch uses MAC addresses

  CAM Table (Content Addressable Memory):
  ┌──────────────────┬──────┬──────────┐
  │ MAC Address      │ Port │ Age      │
  ├──────────────────┼──────┼──────────┤
  │ 00:1a:2b:3c:4d:5e│  1   │  120s    │
  │ 00:1a:2b:3c:4d:6f│  2   │   45s    │
  │ 00:1a:2b:3c:4d:7a│  4   │    5s    │
  └──────────────────┴──────┴──────────┘

  Frame arrives destined for 00:1a:2b:3c:4d:6f
  → Switch looks up MAC in CAM table
  → Found on Port 2
  → Forward ONLY to Port 2

  Frame arrives destined for 00:1a:2b:3c:4d:99 (unknown)
  → Not in CAM table
  → Flood to ALL ports except the one it arrived on
  → Learn source MACs on arriving frames, including any later response

  Entries expire if the MAC isn't seen again: Linux bridges default to
  300 s (ageing_time). A moved device is unreachable at its old port
  until it sends a frame from the new one, or the entry ages out.
```

### What You Will See in Production

In cloud and virtualized environments, MAC addresses matter less than in physical networks because Layer 2 is often abstracted away. But you will encounter MAC addresses in these specific scenarios:

- **ARP tables** showing a MAC address that is wrong — a stale entry from a moved VM, or an ARP spoofing attack
- **Docker networking**: inspect the actual interface; MAC allocation varies by version and driver
- **DHCP reservations** — fixed IP assignments based on MAC address
- **Gratuitous ARP** — when a VM migrates to a new host in a hypervisor cluster, it sends a gratuitous ARP to update every switch and host's ARP cache with the new MAC-to-IP mapping

### Commands

#### `ip link`

**Purpose:** Show network interfaces and their Layer 2 properties including MAC address and MTU
**When to use it:** When you need to see all interfaces on a machine, their state (UP/DOWN), and their MAC addresses

```bash
# Show all interfaces
ip link show

# Show a specific interface
ip link show eth0

# Show only interfaces that are UP
ip link show up
```

**Reading the output:**

```
<INDEX>: <IFNAME>: <FLAGS> mtu <MTU> ...
    link/<TYPE> <MAC_ADDRESS> brd <BROADCAST_MAC>

2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP
    link/ether <MAC> brd ff:ff:ff:ff:ff:ff
```

- `UP` in the flags — interface is administratively enabled
- `LOWER_UP` — physical link is up (cable connected, signal detected)
- `mtu 1500` — maximum transmission unit; 1500 is standard Ethernet; lower values indicate a tunnel or configuration change
- `state UP/DOWN/UNKNOWN` — operational state
- `link/ether` — MAC address follows

**Gotchas:**

- An interface can be `UP` administratively but have no `LOWER_UP` if the cable is unplugged — `ip link` shows both states separately
- Virtual interface states depend on driver and link state; UNKNOWN can be normal,
  but bridges and veth interfaces can also report UP or DOWN

#### `ip addr`

**Purpose:** Show IP addresses assigned to interfaces
**When to use it:** When you need to see what IPs are assigned, in what subnet, and on which interface

```bash
# Show all interfaces and their addresses
ip addr show

# Show addresses on a specific interface
ip addr show eth0

# Show only IPv4 addresses
ip -4 addr show

# Show only IPv6 addresses
ip -6 addr show
```

**Reading the output:**

```
<INDEX>: <IFNAME>: <FLAGS> mtu <MTU>
    link/ether <MAC> brd <BROADCAST>
    inet <IP>/<PREFIX> brd <BROADCAST> scope global <IFNAME>
       valid_lft <TIME> preferred_lft <TIME>
    inet6 <IPv6>/<PREFIX> scope link
```

- `inet` — IPv4 address with CIDR prefix
- `inet6` — IPv6 address
- `scope global` is address scope, not proof of a valid route or external reachability
- `scope link` — link-local address; only valid on the local segment (`169.254.x.x` for IPv4, `fe80::` for IPv6)
- `scope host` — loopback only (`127.0.0.1`)
- `dynamic` — address assigned by DHCP
- `valid_lft forever` — address is permanent (static)

**Gotchas:**

- A machine can have multiple IP addresses on the same interface — this is intentional for virtual hosting and should not alarm you unless unexpected
- Link-local or absent IPv4 addresses can be intentional, including IPv6-only or
  unnumbered interfaces. Check the intended configuration and DHCP/RA logs before
  diagnosing failure; IPv6 link-local addresses do not indicate failed DHCP.

---

## 3.6 Switches, Routers, and Hubs

### What This Is

These three devices are frequently confused in conversations about network problems, and using the wrong word sends the investigation in the wrong direction. Each operates at a different layer, makes different forwarding decisions, and fails in different ways.

### How It Works

**Hub — Layer 1 (Physical)**
A hub has no intelligence. Every bit that arrives on any port is immediately copied and sent out every other port. Every device on a hub is in the same collision domain — only one device can transmit at a time. Hubs are effectively extinct in production. You will not encounter them except in legacy environments or in network simulation tools. If you see one, replace it.

**Switch — Layer 2 (Data Link)**
A switch reads the destination MAC address from every frame and forwards it only to the port where that MAC is known to live. Each port is its own collision domain. Switches learn MAC-to-port mappings from traffic (the CAM table). They do not understand IP addresses. A switch cannot forward traffic between two different subnets — that requires a router.

**Router — Layer 3 (Network)**
A router reads the destination IP address from every packet and makes a forwarding decision based on its routing table. It connects different networks (different subnets, different IP ranges). It decrements the TTL on every packet it forwards. When a packet arrives at a router with TTL=1, the router drops it and sends an ICMP Time Exceeded message back to the source — this is how `traceroute` works.

**Layer 3 Switch**
A switch with routing capability built in. Common in data center environments where you want both fast L2 switching and L3 routing in one device. Operates as a switch for traffic within a VLAN and as a router for traffic between VLANs.

### Diagram

```
HUB — Layer 1, no intelligence

  [A sends a frame to B]

       [A]──────[HUB]──────[B]
                  │
                 [C]  ← Also receives the frame. Cannot send simultaneously.
                  │
                 [D]  ← Also receives the frame.

  Collision domain = every device on the hub.


SWITCH — Layer 2, MAC-based forwarding

  [A: MAC aa:aa] sends frame to [B: MAC bb:bb]

       [A]──[Port 1]                [Port 2]──[B]
                   ╲              ╱
                    [   SWITCH   ]
                   ╱              ╲
       [C]──[Port 3]                [Port 4]──[D]

  Switch looks up bb:bb → Port 2
  Forwards ONLY to Port 2
  C and D see nothing.

  CAM Table: aa:aa→Port1, bb:bb→Port2, ...


ROUTER — Layer 3, IP-based forwarding

  Network A: 10.0.1.0/24        Network B: 10.0.2.0/24

  [10.0.1.5] ──── [Switch A] ──── [ROUTER] ──── [Switch B] ──── [10.0.2.7]
                                     │
                              Routing Table:
                              10.0.1.0/24 → eth0
                              10.0.2.0/24 → eth1
                              0.0.0.0/0   → 203.0.113.1 (internet)

  Packet from 10.0.1.5 to 10.0.2.7:
  1. Arrives at router's eth0
  2. Router reads destination: 10.0.2.7
  3. Routing table: 10.0.2.0/24 → eth1
  4. Router decrements TTL
  5. Router forwards out eth1 toward 10.0.2.7
```

### What You Will See in Production

The most common confusion: "packets are not reaching the other subnet." The machine has the right IP. Ping fails. The culprit is usually the **default gateway** — the router entry in the routing table. Either it is not configured, configured incorrectly, or the router itself is dropping the traffic (firewall rule, missing return route).

In cloud environments, the "router" is the VPC virtual router — you configure it through routing tables in the console, not by SSHing into a device. The mental model is identical; the tooling is different.

### On-Link or Via the Gateway?

Before sending any packet, a host makes one decision with its routing table:

1. **Destination inside a directly connected prefix** (`10.0.1.0/24 dev eth0 proto kernel
   scope link`): the destination is *on-link*. The host ARPs for the **destination's**
   IP and sends the frame straight to that MAC.
2. **Anything else**: the packet goes to a gateway. The host ARPs for the
   **gateway's** IP and sends the frame to the gateway's MAC, while the IP header
   still carries the final destination's address.

`ip route get <destination>` shows you which case applies: on-link destinations
show only `dev eth0`, others show `via <gateway>`. A wrong prefix length breaks this
decision silently. With a /16 instead of a /24, the host thinks a remote subnet is
on-link and ARPs for addresses that will never answer. With a /28 instead of a /24,
it sends neighbours to the gateway.

### What Changes at Each Hop

A packet crossing two routers keeps its IP addresses, but the Ethernet header is
rebuilt on every link:

```
HOST A (10.0.1.5) → ROUTER → HOST B (10.0.2.7), as seen in captures on each link

  Link 1: A → router eth0             Link 2: router eth1 → B
  ─────────────────────────────       ─────────────────────────────
  Eth src: A's MAC                    Eth src: router eth1's MAC    ← rewritten
  Eth dst: router eth0's MAC          Eth dst: B's MAC              ← rewritten
  IP  src: 10.0.1.5                   IP  src: 10.0.1.5             ← unchanged
  IP  dst: 10.0.2.7                   IP  dst: 10.0.2.7             ← unchanged
  TTL:     64                         TTL:     63                   ← decremented
```

This is how to read captures taken at different points of a path. Matching MACs
tell you which device sent a frame onto *this* link; matching IPs, ports, and TCP
sequence numbers tell you it is the same packet. When IP addresses *do* change
between two captures, a NAT device sits between them (Module 8).

---

## 3.7 VLANs, Trunks, Loops, and STP

### What This Is

A switch forwards within one **broadcast domain**: every host on it receives
broadcasts such as ARP requests. **VLANs** (IEEE 802.1Q) split one physical switch
into several isolated broadcast domains, and **STP** (IEEE 802.1D) stops the
switching loops that redundant cabling would otherwise create. Both are usually
invisible until they are misconfigured — and then they cause some of the most
confusing outages there are.

### How It Works

**VLAN tags.** An 802.1Q tag is 4 bytes inserted after the source MAC: EtherType
0x8100, then a 12-bit VLAN ID (1–4094 usable). A switch port has a role:

- **Access port:** belongs to one VLAN. Frames leave untagged; untagged frames
  arriving are assigned to the port's VLAN (its **PVID**). Hosts plug in here and
  never see a tag.
- **Trunk port:** carries several VLANs, each frame tagged with its VLAN ID.
  Switch-to-switch links, hypervisor uplinks, and routers serving several VLANs
  ("router on a stick") use trunks.

The forwarding database is per VLAN: the same MAC can be learned in two VLANs on
different ports. Hosts in different VLANs cannot exchange frames even if their IP
addresses are in the same subnet — traffic between VLANs needs a router.

**Loops.** Ethernet frames have no TTL. If two switches are joined by two links,
a broadcast leaves on one link, comes back on the other, and is flooded again —
forever, multiplying at each pass. Within seconds the loop saturates the links (a
**broadcast storm**), and MAC tables flap as the same source appears on different
ports.

**STP** prevents that. The switches elect a **root bridge** (lowest bridge ID:
priority, then MAC), each other switch picks its best path to the root (its
**root port**), and every redundant port is put into **blocking**: it stays up but
forwards nothing. A new or recovering port passes through **listening** and
**learning** for one *forward delay* each (15 s by default) before **forwarding**,
so classic STP takes about 30 s to start forwarding after a change. RSTP (802.1w)
replaced those timers with an explicit handshake and converges in about a second;
the Linux kernel bridge implements classic STP, while physical switches and
userspace daemons implement RSTP and MSTP.

### Diagram

```
VLAN-AWARE SWITCH (this module's vlan lab)

               ┌───────────── br0 (vlan_filtering) ─────────────┐
               │  port "tagged"     port "access"    port "other"│
               │  VLAN 10 tagged    VLAN 10 PVID     VLAN 20 PVID │
               └──────┬──────────────────┬────────────────┬───────┘
                      │ 802.1Q tag 10    │ untagged       │ untagged
                 [eth0.10]            [eth0]           [eth0]
                10.42.10.1          10.42.10.2       10.42.10.3
                      └──── same VLAN: OK ┘           └ same subnet, other VLAN:
                                                        unreachable

TWO SWITCHES, TWO LINKS (this module's stp lab)

  [hosta]──[left br0]══path1══[right br0]──[hostb]
                 ║                ║
                 ╚═════path2══════╝   one port on path2 BLOCKING: no loop

  Break path1 → after listening + learning, path2 forwards
```

### What You Will See in Production

- **Wrong VLAN on a port** looks like a host with a correct IP and gateway that can't
  ARP anything: `FAILED` neighbour entries, no replies, no errors.
- **Native VLAN mismatch on a trunk** (each side treats untagged frames as a
  different VLAN) leaks traffic between VLANs or black-holes it.
- **A loop** shows up as a sudden surge in broadcast traffic, CPU on every switch,
  MAC addresses flapping between ports, and the whole segment going down at once.
  Physical switches use BPDU guard on host ports and storm control to contain it.
- **STP reconvergence** looks like a 30-second outage after an unrelated link change,
  or a newly connected server that can't reach anything for 30 seconds (which is
  why host-facing ports are configured as *edge* or *PortFast* ports).
- **In the cloud you don't operate VLANs or STP.** The provider's network isolates
  tenants in its own way and prevents loops. Hypervisor uplinks, on-premises
  switches, and container hosts still use VLAN tags, and a VLAN-tagged trunk
  reaching a VM or a Kubernetes node is a common source of confusion.

---

## 3.8 NIC and Link Realities

### What This Is

A network interface is more than a MAC address. It splits and merges packets on
the host's behalf, counts errors and drops, and has an MTU that differs by platform.
Each of these changes what you see in a capture or a counter.

### How It Works

**Offloads.** To save CPU, the kernel hands the NIC large chunks of data and lets it
cut them into MTU-sized segments (TSO, or GSO in software), and merges incoming
segments before the stack sees them (GRO). It also lets the NIC fill in checksums.
tcpdump captures *before* segmentation on the way out and *after* merging on the way
in, so on the host doing the work it shows:

- outbound "segments" far larger than the MTU;
- outbound packets with a bad or zero checksum (`cksum incorrect` in verbose
  output, a red checksum in Wireshark), because the NIC hasn't computed it yet.

Neither is a fault. Capture on the far side, or on a switch port or mirror, to see
what actually crossed the wire.

**Counters.** `ip -s link show eth0` shows packets, bytes, errors, and drops per
interface; `ethtool -S eth0` shows the driver's detailed counters (ring-buffer
overruns, CRC errors, per-queue drops). Compare *deltas* over the same period, not
totals since boot. Interface drops and remote packet loss are different things.

**Default MTUs differ.** Plan tunnels and overlays from the platform's real MTU, not
from 1500:

| Platform | Default MTU |
| --- | --- |
| Standard Ethernet | 1500 |
| Azure VNet | 1500 |
| AWS, within a VPC | 9001 (jumbo); 1500 to the internet and over most VPN and peering paths outside the region |
| GCP VPC | 1460 by default (configurable) |
| Typical VXLAN overlay on a 1500 underlay | 1450 |
| WireGuard default | 1420 |

### What You Will See in Production

*Captured* in a namespace lab while writing this module: an `iperf3` transfer
between two veth interfaces with MTU 1500, with tcpdump on the sender.

```
                            segment sizes tcpdump saw        throughput
  offloads on (default)     286 × 65,160 bytes, a few small   14.3 Gbit/s
  ethtool -K v1 tso off     395 × 1,448 bytes                  6.8 Gbit/s
              gso off
```

With offloads on, the sender's capture showed segments 43 times larger than the
MTU, and nothing was wrong. With them off, every full segment was exactly 1,448
bytes — the MSS for a 1500-byte MTU with TCP timestamps — and the CPU had to do the
segmentation, halving throughput. When a ticket includes a capture full of
65,000-byte packets, check where it was taken before suspecting jumbo frames.

---

## 3.9 The Importance of MTU

### What This Is

IP MTU is the largest IP packet that fits a link, excluding Ethernet header and FCS.
A conventional Ethernet IP MTU is 1500 bytes. Encapsulation can reduce the path's
available inner MTU; failed sizing or feedback can cause drops. There is no universal
ranking establishing MTU as the most common cause of silent loss.

This topic is covered in full in Module 14 (Network Performance). It is introduced here because you need to know it exists and why, before you encounter it in every subsequent module.

### How It Works

When a packet is larger than the MTU of the next link, one of two things happens:

1. **Fragmentation**: the IP layer splits the packet into fragments, each small enough to fit. The destination reassembles them. This adds overhead and can cause problems if fragments arrive out of order or are lost.

2. **Drop with ICMP Fragmentation Needed**: if the packet has the DF (Don't Fragment) bit set, the router drops it and sends back an ICMP Type 3 Code 4 message telling the sender to use a smaller packet size. This is how **Path MTU Discovery (PMTUD)** works.

The problem: if the ICMP Fragmentation Needed message is blocked by a firewall (as it often is), the sender never knows to reduce its packet size. TCP connections that work for small transfers fail silently for large ones. This is a **PMTUD black hole**.

### Diagram

```
MTU AND FRAGMENTATION

Standard Ethernet: MTU = 1500 bytes

  [Sender] ──── [Ethernet: MTU 1500] ──── [Router] ──── [VPN Tunnel: MTU 1450] ──── [Receiver]

  Sender sends a 1500-byte packet with DF bit set.

  ┌────────────────────────────────────────────┐
  │ IP Packet: 1500 bytes, DF=1                │
  └────────────────────────────────────────────┘
                  │
          Arrives at router.
          Next link MTU = 1450.
          Packet > MTU. DF bit is set.
          Router DROPS the packet.
          Router sends ICMP Type 3 Code 4 back to sender:
          "Fragmentation needed. Next hop MTU: 1450"
                  │
                  ▼
  [Sender receives ICMP] → reduces packet size to 1450 → retransmits

  ✓ This works correctly.


PMTUD BLACK HOLE — when ICMP is filtered

  [Sender] ──── [Router] ──── [Firewall: blocks ICMP] ──── [VPN] ──── [Receiver]

  Sender sends 1500-byte packet with DF=1
  Router drops it (too big for VPN tunnel)
  Router sends ICMP Fragmentation Needed
  Firewall drops the ICMP message
  Sender never learns to reduce packet size
  Sender retransmits the same 1500-byte packet
  Connection appears to hang for large transfers
  Small transfers work (small packets fit in tunnel)

  Symptom: SSH connects fine. Large file transfers hang.
           Web pages load headers but not body.
```

### What You Will See in Production

"SSH works but `scp` hangs." "The API works for small payloads but times out for large ones." "Connecting to the VPN succeeds but nothing loads after." These are all PMTUD black hole symptoms. The fix is either to allow ICMP Type 3 Code 4 through firewalls, or to clamp TCP MSS on the tunnel interface so packets are never sent at a size that will be dropped.

---

## Lab Part A: Reading a Machine's Complete Network State

**Objective:** Given any Linux machine, produce a complete picture of its network state — interfaces, addresses, routes, and neighbors — and explain what each piece means
**Environment:** Linux (Ubuntu 24.04); root for Parts B and C
**Tools needed:** `iproute2`, `ping`, `iperf3`, `ethtool`, `tcpdump`, Go toolbox (`netlab`)
**Estimated time:** 1.5 hours

### Lab Topology

```
Your Lab Machine
┌──────────────────────────────────────────────────────┐
│                                                      │
│  lo: 127.0.0.1/8                                    │
│  eth0 (or ens3): <your LAN IP>/<prefix>             │
│  docker0: 172.17.0.1/16 (if Docker is installed)    │
│                                                      │
│  You will read and interpret each of these.          │
└──────────────────────────────────────────────────────┘
                    │
                    │ eth0
                    ▼
             [Default Gateway]
                    │
                    ▼
               [Internet]
```

### Setup

```bash
# Verify iproute2 is installed
ip --version

# Install iperf3 if not already present
sudo apt-get install -y iperf3
```

---

### Exercise 1: Read Every Interface

**Goal:** Identify every network interface on the machine, its state, MTU, and MAC address

```bash
# Step 1: List all interfaces with Layer 2 details
ip link show

# Step 2: List all interfaces with IP addresses
ip addr show

# Step 3: Show only active (UP) interfaces with addresses
ip -brief addr show
```

**What to observe:**

- Every interface has a name (`lo`, `eth0`, `ens3`, `docker0`), a state (`UP`, `DOWN`, `UNKNOWN`), an MTU, and a MAC address
- The loopback (`lo`) has the address `127.0.0.1/8` — it always exists and is always up
- `ip -brief addr show` gives a compact one-line-per-interface summary — useful when a machine has many interfaces

**What this shows you:**
Compare interface state, addresses, and MTU with the intended configuration.
No IPv4 address does not necessarily indicate DHCP failure, and MTU 1450 does not
prove a VPN exists. Verify interface type, routes, and the configured addressing mode.

**If it fails:**
If `ip` is not found, install `iproute2`: `sudo apt-get install iproute2`. The older `ifconfig` command from `net-tools` is an alternative but is deprecated — learn `ip` instead.

---

### Exercise 2: Read the Routing Table

**Goal:** Identify the default gateway and all known routes, and explain how a packet to a given destination would be forwarded

```bash
# Step 1: Show the full routing table
ip route show

# Step 2: Ask the kernel which route would be used for a specific destination
ip route get 8.8.8.8

# Step 3: Ask for a route to an address in your local subnet
ip route get <your_gateway_ip>
```

**What to observe:**

- A line starting with `default via` is the default gateway — the router that handles all traffic with no more specific route
- Lines like `10.0.0.0/24 dev eth0 proto kernel scope link` are directly connected networks — the machine can reach these without going through a router
- `ip route get` shows exactly which interface and gateway would be used for a specific destination — this is what the kernel would actually do

**What this shows you:**
Inspect the route for the actual destination. Without a default route, more-specific
off-link routes can still work. A configured next hop is not proof that downstream
routing or the return path is correct; verify neighbor reachability and both paths.

**If it fails:**
If there is no `default` route and there should be one, add it for the current boot with `sudo ip route add default via <gateway_ip>`. It does not persist across reboots; on Ubuntu the permanent configuration lives in `/etc/netplan/`. On a cloud VM, don't change routes by hand: the guest gets them from the platform's DHCP, and the real routing decisions happen in the platform's route tables.

---

### Exercise 3: Read the ARP / Neighbor Table

**Goal:** See which IP-to-MAC mappings the machine has learned, and understand what a missing entry means

```bash
# Step 1: Show the ARP/neighbor table
ip neigh show

# Step 2: Ping your default gateway to populate the ARP table if it is empty
ping -c 3 <your_gateway_ip>

# Step 3: Show the neighbor table again
ip neigh show
```

**What to observe:**

- Each entry shows an IP address, the interface it was learned on, the MAC address, and a state (`REACHABLE`, `STALE`, `FAILED`)
- `REACHABLE` — the entry was confirmed recently and is considered valid
- `STALE`: usable immediately; sending can start reachability confirmation through DELAY/PROBE
- `FAILED` — the kernel tried to resolve this IP and got no ARP reply; the host may be down or unreachable at Layer 2

**What this shows you:**
The ARP table is the bridge between Layer 3 (IP) and Layer 2 (MAC). When a machine can ping its gateway's IP, it must first resolve that IP to a MAC via ARP. A `FAILED` entry for the gateway means the machine cannot communicate on the local network — even though it has an IP address. This is often caused by a VLAN misconfiguration, a wrong subnet mask, or a firewall blocking ARP.

---

### Exercise 4: Measure Latency and Throughput

**Goal:** Measure baseline RTT latency and throughput to a known host, and understand the relationship between them

```bash
# Step 1: Measure latency to your gateway, then to a public DNS server (20 packets each)
# (Some networks block ICMP to the internet; that is a finding, not a failure.)
ping -c 20 <your_gateway_ip>
ping -c 20 8.8.8.8

# Step 2: Note the avg RTT and mdev (jitter) values

# Step 3: Start an iperf3 server on localhost in the background
iperf3 -s -B 127.0.0.1 &
IPERF_PID=$!
trap 'kill "$IPERF_PID" 2>/dev/null || true' EXIT INT TERM

# Step 4: Run a throughput test to localhost
iperf3 -c 127.0.0.1 -t 10

# Step 5: Run against a remote host if one is available
# (The remote host must have iperf3 -s running)
iperf3 -c <remote_ip> -t 10

# Step 6: Kill the local iperf3 server
kill "$IPERF_PID"
wait "$IPERF_PID" 2>/dev/null || true
trap - EXIT INT TERM
```

**What to observe:**

- The loopback `iperf3` test will show very high throughput (often tens of Gbit/s) because no real network traversal occurs. Offloads are part of why: data moves in 64 KB chunks (3.8)
- A remote `iperf3` test will show the real path throughput, which will be significantly lower
- Measure RTT and throughput to the same endpoint and inspect `ss -tin` window,
  congestion, and retransmission evidence. Ping to one host and iperf to another
  cannot confirm a BDP bottleneck; loopback only checks the local tool setup.

**What this shows you:**
Running `iperf3` against localhost shows you the machine's network stack overhead without any network path involved. Running it to a remote host adds the actual network. The difference between the two is what the network path costs. If the remote test gives throughput far below what the link should support, the constraint is either latency (check RTT × window size), packet loss (watch for retransmissions in `iperf3` output), or an intermediate bottleneck.

### Lab Topology (actual)

```
iperf3 loopback test:

  [Your Machine]
      │
      ├── iperf3 client ──▶ lo (127.0.0.1) ──▶ iperf3 server
      │                     ↑
      │              No real network.
      │              Tests stack overhead only.
      │
      └── ping ──▶ eth0 ──▶ [Gateway] ──▶ [8.8.8.8]
                              ↑
                    Real network path.
                    RTT reflects actual latency to 8.8.8.8.
```

### Key Takeaways

- `ip addr`, `ip link`, `ip route`, and `ip neigh` give you the complete Layer 2 and Layer 3 state of a machine — run all four before declaring a network problem
- A machine can have a valid IP address and still not route traffic if the default gateway is missing or wrong
- A `FAILED` ARP entry means the host cannot communicate at Layer 2 — the problem is on the local network, not the internet
- Latency and throughput are independent constraints — a slow transfer may be caused by high RTT with a small TCP window, not by insufficient bandwidth
- The loopback interface (`lo`) always exists, always has `127.0.0.1`, and is always `UP` — if it is not, something is seriously wrong with the network stack

---

## Lab Part B: Build Switches with `netlab`

`netlab` builds a lab from a JSON file inside network namespaces, so nothing on
your host changes. It prints every `ip` and `bridge` command it runs, runs the
lab's checks, saves evidence, and deletes everything afterwards, even if you press
Ctrl-C. Read the [lab files](../labs/netlab) alongside the output: each one is a
topology plus the checks that prove what it teaches.

```bash
cd labs/go
go build -o bin/ ./cmd/netlab
./bin/netlab plan ../netlab/bridge.json        # the commands it will run; no root needed
sudo ./bin/netlab run ../netlab/bridge.json
```

### Exercise 5: A Bridge Learns

The bridge lab connects two hosts (`peer1`, `peer2`) to a Linux bridge in a third
namespace (`switch`). *Captured* checks:

```
check 1/6: forwarding database before any traffic: only the bridge's own (permanent) entries
  | 8a:0e:18:93:f4:5d dev port1 master br0 permanent
  | a2:6d:5f:4d:a7:50 dev port2 master br0 permanent
  ...
check 2/6: ping from peer1 to 198.18.0.2 is reachable
  PASS after 1.01s: reachable (2/2 replies)
check 3/6: br0 on switch learns peer1:eth0's MAC on port port1
  PASS after 10ms: 52:bf:d7:72:34:c9 learned on port1
check 4/6: br0 on switch learns peer2:eth0's MAC on port port2
  PASS after 10ms: 0e:9d:1c:9c:5b:25 learned on port2
check 6/6: peer1's neighbour (ARP) table now maps 198.18.0.2 to peer2's MAC
  | 198.18.0.2 lladdr 0e:9d:1c:9c:5b:25 REACHABLE
```

**Verify and explain:**

- [ ] Before traffic, the only entries are `permanent` (the bridge's own ports and multicast).
- [ ] One ping later, each peer's MAC is learned on its own port. Which frame taught the bridge peer1's MAC? (The ARP request peer1 broadcast before the first ping.)
- [ ] Every MAC in the output is locally administered. Check the second hex digit of each.

### Exercise 6: VLANs Isolate the Same Subnet

```bash
sudo ./bin/netlab run ../netlab/vlan.json
```

Three hosts share the subnet 10.42.10.0/24, but `other` is on VLAN 20. **Verify:**
the tagged host reaches the access host, the ping to `other` is `unreachable`, and
`bridge vlan show` (saved as `vlans.txt`) lists VLAN 10 as tagged on `tagged` and
`PVID Egress Untagged` on `access`. If netlab prints `SKIP: this kernel …`, your
kernel lacks 802.1Q or VLAN filtering; stock Ubuntu kernels have both.

Then make one change at a time to `vlan.json` and predict the result before you run
it: give `other` VID 10, or remove `"untagged": true` from `access`.

### Exercise 7: A Loop, Blocked — and a Failover, Timed

```bash
sudo ./bin/netlab run ../netlab/stp.json
```

*Captured*, with the lab's 4-second forward delay:

```
check 1/7: STP converges: one of the two parallel links is blocked, everything else forwards
  PASS after 9.32s: left: path1=forwarding path2=blocking host=forwarding;
                    right: path1=forwarding path2=forwarding host=forwarding
check 5/7: break link path1 (both ends lose carrier)
check 6/7: STP fails over: path2 forwards; nothing is blocked any more
  PASS after 8.27s: left: path1=disabled path2=forwarding host=forwarding; ...
check 7/7: hosts reach each other again over path2
```

**Verify and explain:**

- [ ] Exactly one of the six bridge ports is `blocking`, on the switch that is *not* the root.
- [ ] Failover took about 2 × forward delay. Scale it: with the default 15 s, how long is the outage?
- [ ] Run `sudo ./bin/netlab up ../netlab/stp.json`, then
      `sudo ./bin/netlab exec ../netlab/stp.json left -- ip -d link show br0` to see `root_id`,
      `bridge_id`, `root_port`, and `root_path_cost`. Is `left` the root? Repeat for `right`.
      Remove the lab with `sudo ./bin/netlab down ../netlab/stp.json`.

## Lab Part C: Offloads in a Capture

Reproduce 3.8's measurement in two throwaway namespaces:

```bash
sudo ip netns add o1; sudo ip netns add o2
sudo ip -n o1 link add v1 type veth peer name v2 netns o2
sudo ip -n o1 addr add 198.18.6.1/24 dev v1; sudo ip -n o2 addr add 198.18.6.2/24 dev v2
sudo ip -n o1 link set v1 up; sudo ip -n o2 link set v2 up
sudo ip netns exec o2 iperf3 -s -D

# Capture the sender while it sends
sudo ip netns exec o1 timeout 6 tcpdump -nni v1 -c 400 'tcp dst port 5201 and greater 200' > /tmp/on.txt &
sudo ip netns exec o1 iperf3 -c 198.18.6.2 -t 2; wait
awk '{for(i=1;i<=NF;i++) if($i=="length") print $(i+1)}' /tmp/on.txt | sort -n | uniq -c | sort -rn | head -3

# Turn segmentation offload off and repeat
sudo ip netns exec o1 ethtool -K v1 tso off gso off
sudo ip netns exec o1 timeout 6 tcpdump -nni v1 -c 400 'tcp dst port 5201 and greater 200' > /tmp/off.txt &
sudo ip netns exec o1 iperf3 -c 198.18.6.2 -t 2; wait
awk '{for(i=1;i<=NF;i++) if($i=="length") print $(i+1)}' /tmp/off.txt | sort -n | uniq -c | sort -rn | head -3

# Clean up: stop iperf3 first — a process inside a namespace keeps running
# (and keeps the namespace alive) after `ip netns del`
sudo ip netns pids o2 | xargs -r sudo kill
sudo ip netns del o1; sudo ip netns del o2
```

**Verify:** with offloads on, the most common length is tens of kilobytes; with them
off, it is 1448. Explain both numbers, and the throughput difference. If tcpdump
reports `packets dropped by kernel`, the capture itself fell behind at these speeds;
that is not network loss, and it is why captures under load need care (Module 21).

---

## Common Failure Patterns

| Symptom                                                                    | Likely Cause                                                             | How to Confirm                                             | Fix                                                    |
| -------------------------------------------------------------------------- | ------------------------------------------------------------------------ | ---------------------------------------------------------- | ------------------------------------------------------ |
| Interface has `169.254.x.x` as its only IPv4 address                       | Often DHCP failure with link-local fallback, but can be intended         | `ip addr show`; DHCP client logs; intended configuration   | Fix DHCP if it was meant to run, or assign the intended address |
| `ip route get 8.8.8.8` returns `RTNETLINK answers: Network is unreachable` | No default gateway configured                                            | `ip route show` — no `default via` line                    | `sudo ip route add default via <gateway>`              |
| Ping to gateway succeeds but ping to internet fails                        | Missing or wrong NAT on the gateway, or gateway has no route to internet | `traceroute 8.8.8.8` — see where it stops                  | Check gateway's routing and NAT configuration          |
| Large file transfer hangs, small transfers work                            | PMTUD black hole — ICMP Type 3 Code 4 is being filtered                  | `ping -M do -s 1400 <remote>` — try decreasing sizes       | Allow ICMP unreachable through firewalls, or clamp MSS |
| `ip neigh show` shows `FAILED` for gateway IP                              | ARP not resolving — Layer 2 connectivity problem                         | `arping -I eth0 <gateway_ip>`                              | Check VLAN config, cable, switch port state            |
| iperf3 throughput far below expected link speed                            | Packet loss causing retransmissions, or TCP window too small for RTT     | Check iperf3 output for retransmit count; check `ping` RTT | Tune TCP buffers, investigate packet loss source       |
| Interface shows `UP` but not `LOWER_UP`                                    | Cable disconnected or physical link down                                 | `ethtool eth0` — check `Link detected`                     | Check physical cable, switch port                      |
| Correct IP and gateway, every neighbour `FAILED`                           | Port in the wrong VLAN, or tagged/untagged mismatch                      | Switch port config; capture: ARP requests leave, none come back | Put the port in the right VLAN with the right tagging |
| Whole segment down at once, broadcast traffic and switch CPU surge         | Layer 2 loop without STP, or STP disabled                               | Interface counters climbing on many ports; MAC flapping logs | Break the loop; enable STP/BPDU guard and storm control |
| New host or link change → about 30 s with no connectivity                  | Classic STP listening and learning on a non-edge port                   | Switch port STP state over time                            | Configure host-facing ports as edge/PortFast; use RSTP |
| Capture shows packets of tens of KB on a 1500-MTU link, or bad checksums  | NIC offloads (TSO/GSO, checksum offload) on the capturing host          | `ethtool -k <if>`; capture on the receiver instead          | Nothing to fix; read captures from the right vantage point |
| iperf3 reports ~941 Mbit/s on gigabit Ethernet                             | A perfect link: that is the TCP goodput ceiling                          | 1,448 / 1,538 × 1,000                                       | Nothing to fix                                         |

---

## Summary

- A LAN gives you full control and sub-millisecond latency; a WAN and the internet mean shared infrastructure you do not own, with variable latency and failure modes you cannot directly observe
- Bandwidth is a link property; latency is a path property; throughput is what you measure between two hosts — they fail independently and require different tools to diagnose
- A frame is Layer 2 (Ethernet, MAC addresses), a packet is Layer 3 (IP), a segment is Layer 4 (TCP) — the word tells you which header is relevant and which device is responsible
- Switches forward by MAC address (Layer 2); routers forward by IP address (Layer 3) — a packet that cannot cross a subnet boundary is missing a route, not a switch configuration
- At each routed hop the MAC addresses are rewritten and the TTL drops by one; the IP addresses stay the same unless NAT is involved
- VLANs split a switch into isolated broadcast domains; STP blocks redundant links to prevent loops, and classic STP takes about 2 × forward delay (30 s by default) to recover
- Offloads make a sender's capture show oversized, unchecksummed segments; best-case TCP goodput on gigabit Ethernet is about 941 Mbit/s
- The MTU of 1500 bytes is a hard constraint on standard Ethernet — any tunnel, VPN, or overlay adds overhead that reduces the effective MTU and can silently drop packets if not accounted for
- `ip addr`, `ip route`, and `ip neigh` together give you the complete network state of a Linux machine — run all three before escalating any connectivity issue

## What's Next

Module 4 covers the OSI model in depth — the 7-layer framework that gives every engineer a common vocabulary for describing where in the stack a problem lives, and how to use layer-by-layer elimination to narrow a problem from "the network is broken" to "the VLAN tag is missing on port 14 of the switch."

---

## Quick Reference Card

```
MODULE 3: NETWORKING FUNDAMENTALS — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

KEY TERMS
  Frame        Layer 2 unit — Ethernet, has MAC src/dst
  Packet       Layer 3 unit — IP, has IP src/dst, TTL
  Segment      Layer 4 unit — TCP, has ports, seq/ack numbers
  Datagram     Layer 4 unit — UDP equivalent of a segment
  IP MTU       Max IP packet size; conventional Ethernet value 1500 bytes
  BDP          Bandwidth × RTT = in-flight data needed to fill the path

BANDWIDTH vs LATENCY vs THROUGHPUT
  Bandwidth    Link capacity (1 Gbps, 100 Mbps)         — link property
  Latency      Delay src→dst; tools report RTT (ms)      — path property
  Throughput   Measured transfer rate between two hosts  — flow property
  Goodput      App-layer useful bytes per second         — app property

ESSENTIAL COMMANDS
  ip link show              All interfaces — state, MTU, MAC
  ip addr show              All interfaces — IP addresses
  ip -brief addr show       Compact one-line-per-interface view
  ip route show             Routing table — gateway and routes
  ip route get <IP>         Which route would be used for this destination
  ip neigh show             ARP/neighbor table — IP to MAC mappings
  ping -c 20 <host>         Measure RTT latency and packet loss
  iperf3 -s                 Start throughput test server
  iperf3 -c <host> -t 10    Measure throughput to server

LAYER 2
  bridge fdb show br br0    Learned MACs per port (and VLAN)
  bridge vlan show          VLAN membership, PVID, tagging per port
  bridge link show          STP state per port
  ethtool -k <if>           Offloads; ethtool -S <if> for driver counters
  MAC first octet           bit 0 = group (multicast/broadcast); bit 1 = local
  Max frame                 1518 bytes, 1522 with an 802.1Q tag
  TCP goodput, 1 Gb/s       ≈ 941 Mbit/s (1448 / 1538)

NETLAB
  netlab plan|run|up|down|exec LAB.json    (run/up/down/exec need root)

ARP NEIGHBOR STATES
  REACHABLE    Recently confirmed — valid
  STALE        Usable now; reachability confirmation can follow traffic
  FAILED       ARP got no reply — host unreachable at Layer 2

WHEN YOU SEE THIS          THINK THIS
  169.254.x.x only          IPv4 link-local: DHCP failed, or intended?
  No default route           Specific off-link routes can still work
  iperf3 << expected speed   Check RTT × window size (BDP) or packet loss
  FAILED in ip neigh         Layer 2 problem — VLAN, cable, switch port
  Large transfers hang        PMTUD black hole — check ICMP filtering
  UP but not LOWER_UP        Physical link down — check cable

VERIFY WITH
  ip addr show               Interface has IP and is UP/LOWER_UP
  ip route show              Default gateway is present
  ip neigh show              ARP resolved for gateway
  ping -c 5 <gateway>        Layer 3 reachable to first hop
  ping -c 5 8.8.8.8          Internet reachable
```

---

## References

- `man ip` — complete reference for the `ip` command and all subcommands
- `man ip-link` — interface management
- `man ip-address` — address management
- `man ip-route` — routing table management
- `man ip-neighbour` — ARP/neighbor table management
- `man iperf3` — throughput testing tool
- `man ping` — ICMP echo tool including MTU probing flags
- RFC 791 — Internet Protocol (IPv4) — packet structure and fragmentation
- RFC 826 — An Ethernet Address Resolution Protocol (ARP)
- IEEE 802.1Q — Bridges and Bridged Networks (VLANs; includes the spanning tree protocols)
- RFC 9542 — IANA Considerations and IETF Protocol and Documentation Usage for IEEE 802 Parameters (MAC address bits)
- Linux bridge documentation — <https://docs.kernel.org/networking/bridge.html>
