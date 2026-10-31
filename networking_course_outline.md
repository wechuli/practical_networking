# Networking Fundamentals to Mastery

**A Practical Course for Support Engineers, Developers & Cloud Engineers**

---

## How to Use This Course

This course is built for engineers who need to understand networking at a working level — not to pass a certification, but to diagnose real production issues, understand why things fail, and build systems that communicate reliably. Every module includes theory, practical examples, and hands-on labs.

The practical core is Linux/application networking, with AWS-oriented optional
cloud exercises and qualified Azure/GCP comparisons. Follow the [lab contract](labs/README.md)
for supported profiles, command classes, isolation, and evidence. "Mastery" is a
learning goal assessed by independent capstones, not a certification from reading
the text. Unattributed incident stories and outputs are synthetic illustrations.

Use the [objective-to-exercise index](labs/index.md), [completed appendices](appendices.md),
and [implementation/verification record](implementation.md) alongside the modules.

### Outline Status and Planned Additions

Items marked `[planned]` are in this outline but **not yet implemented** in the module
notes, labs, or appendices. Everything unmarked already has notes. Remove the
marker only when the notes exist **and** the related lab has been executed in its
target profile and recorded in [implementation.md](implementation.md).

The additions target one gap: the course explains how protocols work, but rarely
trains the support workflow of starting from an error string on a failing host and
proving which hop, device, or setting caused it. They also move new labs to **Go**,
on Linux, as the course's programming language.

| # | Addition | Where | Status |
| --- | --- | --- | --- |
| 1 | Go lab toolkit and Go networking behaviour | Lab setup, 0.4, 20.7, Appendix I | Module 0 done; 20.7 and Appendix I `[planned]` |
| 2 | Error catalogue: error string → errno → packet on the wire → first check | 11.10, 20.7, Appendix H | `[planned]` |
| 3 | Linux kernel networking internals and drop points | New Module 24 | `[planned]` |
| 4 | Cloud and CI/CD path troubleshooting (Azure depth, cross-cloud, runners, registries) | New Module 25 | `[planned]` |
| 5 | Reading captures for loss rigorously; quantifying loss (Mathis model) | 14.9, 21.9 | `[planned]` |
| 6 | Runnable routing labs with containerlab + FRR (OSPF, eBGP, ECMP) | 8.7, Labs 8–9 | `[planned]` |
| 7 | Cross-cloud private DNS and Go resolver behaviour | 12.11 | `[planned]` |
| 8 | Per-runtime trust stores and TLS-inspecting proxies | 13.10 | `[planned]` |
| 9 | Docker daemon networking: dockerd proxy, image-pull path, nested MTU | 16.7 | `[planned]` |
| 10 | Kubernetes failure signatures and Cilium/Hubble drop tracing | 17.8 | `[planned]` |
| 11 | Continuous network monitoring and synthetic probes (Prometheus) | 22.7 | `[planned]` |
| 12 | Azure-runner-to-AWS-ECR capstone and `netdoctor` Go capstone | Capstones 7–8 | `[planned]` |

**Open items on existing content** (from [implementation.md](implementation.md)):

- The Linux namespace and packet fixtures, Docker proxy lab, and Kubernetes CNI,
  Ingress, and mesh labs have **not** been executed on Linux. They were authored on
  macOS without a Docker daemon. Run [labs/run-linux.sh](labs/run-linux.sh) and the
  Kubernetes runner in their target profiles and record results before relying on them.
- The labs are Go programs and netlab lab files. Python remains only for the HTTP/3
  client (aioquic) and the Kubernetes runner.

**Study order:** Modules 0–26 in order. The kernel-internals and cloud/CI modules
were added as 24 and 25 and the capstones renumbered to 26 (2026-10-31), so the
assessment comes last.

### Optional Companion: Tailscale

After Module 19, the [Tailscale minicourse](labs/tailscale.md) applies WireGuard,
DNS, routing, and access-policy concepts to a working private network. Its five
lessons cover tailnet setup, direct and relayed connections, MagicDNS and grants,
subnet routers versus exit nodes, and an evidence-based troubleshooting capstone.
Plan for 3-4 hours plus setup. An isolated Tailscale account and two disposable
Linux VMs are required; this companion is not a prerequisite for the remaining
modules or part of the local-only lab suite.

## Who This Course Is For

- Technical Support Engineers diagnosing connectivity and performance issues
- Software Developers who need to understand what happens below the HTTP layer
- Cloud Engineers building and operating infrastructure on AWS, GCP, or Azure
- DevOps / Platform Engineers managing Docker, Kubernetes, and service meshes

## Lab Environment Setup

- Disposable Ubuntu 24.04 LTS VM with root access and required kernel capabilities
- Running rootful Docker Engine and Docker Compose v2
- Wireshark, tshark, and tcpdump
- kubectl and a dedicated local cluster with a policy-enforcing CNI
- WireGuard tools (wireguard-tools package)
- Python 3.12/3.13 in a virtual environment, only for labs not yet ported to Go
- Tools: dig, nslookup, mtr, iperf3, hping3, openssl, curl, ss, ip
- Go 1.24+ toolchain for all labs ([labs/go](labs/go/README.md)); third-party packages
  (`golang.org/x/net`, `github.com/google/gopacket`, `github.com/miekg/dns`,
  `github.com/vishvananda/netlink`) arrive with the modules that need them `[planned]`
- containerlab and FRR images for multi-router OSPF/BGP labs `[planned]`
- bpftrace, bcc-tools, and dropwatch for kernel drop tracing `[planned]`
- Cilium CLI and Hubble on the local cluster for drop visibility `[planned]`
- Optional cloud sandboxes in **Azure and AWS**, built and destroyed with Terraform
  per session, for cross-cloud labs `[planned]`

---

## Module 0 — Before We Begin

_Mental models, environment setup, the Go toolbox, and the big picture_

### 0.1 How to Use This Course

- Each module builds on the previous one: concept, mechanism, diagram, production
  context, lab, failure patterns, write-up
- Course map: Modules 0–26 in order, ending with the capstones
- The write-up habit: every drill ends with symptom → evidence → root cause → fix

### 0.2 The Single Most Important Exercise

- `curl -v https://example.com`: a fresh request as DNS → TCP → TLS 1.3 → HTTP → teardown
- Caveats: caching, connection reuse, proxies (CONNECT), QUIC, plaintext
- Each step costs at least one round trip; the TLS 1.3 certificate is encrypted
- "Who answered?": every refusal, reset, or error status came from some device; a timeout means nothing answered

### 0.3 Lab Environment Setup

- Capture, path, DNS, HTTP/TLS, and performance tools on Ubuntu 24.04
- Go 1.24+ from the official release, checksum-verified
- Capture permissions and their security cost; Docker and its daemon-level proxy

### 0.4 Your Go Lab Toolbox

- One Go module, [labs/go](labs/go/README.md), one command per tool, shared conventions
  (stdlib first, exit codes, `-json`, explanations of what happened on the wire)
- Why Go: one static binary you can copy onto a failing host or CI runner
- **`phases`**: times DNS, connect, proxy tunnel, TLS, request, server wait, and
  transfer for one request, and names the failed step
- Exercises: a healthy request, skipping DNS with `-resolve`, four deliberate
  failures (refused, timeout, NXDOMAIN, untrusted certificate) verified in
  tcpdump, and a proxy refusing a CONNECT tunnel
- `netlab`, the namespace topology CLI, arrives with the first namespace lab in
  Module 3; fault injection (`tc netem`, DROP vs REJECT, MTU) is added with the
  modules that need it `[planned]`

### 0.5 Vocabulary You Will Use Every Day

- Frame, packet, segment and datagram, protocol, port, socket and socket address,
  5-tuple, latency and RTT, throughput, encapsulation

---

## Module 1 — History of Networking

_From ARPANET to the cloud-native era — and the constraints each step left behind_

### 1.1 The Problem Networking Solved

- Isolated computers and the need to share data; 'sneakernet'
- Why a full mesh of dedicated links doesn't scale (N×(N-1)/2), then and in cloud peering

### 1.2 Circuit Switching vs Packet Switching

- Baran, Davies, and Kleinrock; why packets won: statistical multiplexing of bursty traffic
- The costs: queues, loss, reordering, no delivery guarantee — and per-flow paths

### 1.3 ARPANET

- The first message (October 29, 1969, UCLA → SRI)
- IMPs as packet switches within one network; NCP; separation of host and network

### 1.4 From ARPANET to the Internet

- Cerf and Kahn's gateways (1974); TCP split into TCP and IP (1978); Flag Day (1983)
- The end-to-end principle and Clark's design priorities (1988)
- Why accountability came last: spoofable sources, and stateful middleboxes added later

### 1.5 Standards — RFCs and How to Read Their Status

- The RFC Editor, streams, and statuses; two maturity levels since RFC 6410
- Internet-Drafts vs RFCs; updates vs obsoletes; errata; MUST/SHOULD (BCP 14)
- Checking an RFC before citing it

### 1.6 Key Milestones and Why They Matter

- Ethernet and the 1500-byte MTU; IPv4, TCP, DNS; the 1986 congestion collapse and
  TCP congestion control; CIDR, NAT, IPv6; SSL/TLS through TLS 1.3 and RFC 8996
- Incidents that shaped practice: Morris worm, Kaminsky (2008), IANA IPv4
  exhaustion (2011), Mirai/Dyn (2016), Facebook BGP withdrawal (2021)
- The cloud and container era: EC2 (2006), Docker, Kubernetes, Let's Encrypt, QUIC, HTTP/3

### 1.7 The TCP/IP Model and Why Two Models Exist

- OSI as vocabulary, TCP/IP as implementation; protocols that straddle layers

### 1.8 How the Cloud Changed Networking

- Boundaries, ownership, and visibility move; protocols do not
- Guest routes vs platform routes; stateful vs stateless filtering by provider;
  reserved subnet addresses; emulated Layer 2; platform addresses (169.254.169.254, 168.63.129.16)

> **Lab 1**
>
> - **Go lab `rfcstatus`**: check whether RFCs are current, follow obsoleted-by
>   chains to the current document, and list updates, from the RFC Editor's index
> - Watch DNS source-port and query-ID randomisation (the 2008 Kaminsky fix) in tcpdump
> - Write-up: map four production symptoms to the historical decision behind each

---

## Module 2 — Quickstart: A Webpage End to End

_Watch a full request-response cycle before diving into fundamentals_

### 2.1 The 10,000-Foot View

- DNS → TCP → TLS → HTTP → close, for a fresh HTTPS request over TCP
- What each step solves, and where each protocol layer enters

### 2.2 Step-by-Step Walkthrough

- DNS: application → C library and NSS → stub resolver → recursive → authoritative; A and AAAA
- TCP 3-way handshake; TLS 1.3 with SNI and ALPN; HTTP/2 frames vs HTTP/1.1 text
- Closing: close_notify, FIN — and why a healthy connection can end with an RST
- Round trips before the first byte

### 2.3 Seeing It Yourself

- `curl -v`, annotated (real curl 8.x output); `curl -w`, whose times are cumulative
- `phases` per-step timings; browser DevTools timing rows
- tcpdump and tshark, with and without a TLS key log

### 2.4 What Changes the Sequence

- Cached DNS, connection reuse, session resumption, Happy Eyeballs, forward proxies
  (CONNECT), TLS-terminating CDNs and inspecting proxies, redirects, HTTP/3 via Alt-Svc

### 2.5 Which Step Failed?

- Real error messages from curl (exit codes), Go, and Chrome for DNS, refused,
  dropped, proxy, TLS trust, TLS name, and HTTP-level failures

> **Lab 2**
>
> - **Go lab `labserver`**: a local HTTPS server with a throwaway CA, chosen body size, and response delay
> - Annotate `curl -v`; compare HTTP/2 and HTTP/1.1 through ALPN
> - Separate steps with `curl -w` and `phases`; find a 300 ms server delay in each
> - Connection reuse with `curl` and `phases -count`
> - Capture with tcpdump; read it with tshark with and without keys; find the closing RST
> - Five failures, including DROP vs REJECT in a throwaway network namespace

---

## Module 3 — Networking Fundamentals

_The vocabulary every network conversation depends on — and the Layer 2 machinery under it_

### 3.1 What Is a Network?

- Nodes, links, and topology definitions; LAN, WAN, the internet, cloud virtual networks
- Whether you own both ends of the path decides what you can measure

### 3.2 Network Topologies

- Star, bus, ring, mesh, spine-leaf — and how each fails

### 3.3 Bandwidth, Latency, and Throughput

- Bandwidth vs latency (RTT) vs throughput vs goodput
- Ethernet overhead: 1,448 payload bytes per 1,538 on the wire, so ≈ 941 Mbit/s TCP on gigabit
- Bandwidth-delay product and the window / RTT ceiling; `ping` and `iperf3`

### 3.4 Packets, Frames, and Segments

- The right term at each layer; frame sizes with and without an 802.1Q tag
- Retransmissions prove loss somewhere; per-layer counters locate it

### 3.5 Network Interfaces and MAC Addresses

- MAC structure, the I/G and U/L bits, OUIs; `ip link`, `ip addr`
- Learning switches, flooding, and ageing

### 3.6 Switches, Routers, and Hubs

- Forwarding by MAC vs by IP; Layer 3 switches
- The on-link vs via-gateway decision; what changes at each hop (MACs, TTL) and what doesn't

### 3.7 VLANs, Trunks, Loops, and STP

- 802.1Q tags, access and trunk ports, PVID; per-VLAN forwarding databases
- Why loops are fatal at Layer 2; STP election, port states, and timers; RSTP

### 3.8 NIC and Link Realities

- NIC offloads (TSO, GSO, GRO, checksum offload) and what they do to captures
- Interface counters: `ip -s link`, `ethtool -S`; default MTUs by platform

### 3.9 The Importance of MTU

- Fragmentation, DF, and Path MTU Discovery; PMTUD black holes

> **Lab 3**
>
> - Part A: read a machine's interfaces, addresses, routes, and neighbours; measure RTT and throughput
> - Part B: **Go lab `netlab`** — declarative namespace labs with checks:
>   [bridge](labs/netlab/bridge.json) (MAC learning), [vlan](labs/netlab/vlan.json)
>   (tagged, access, isolated VLANs), [stp](labs/netlab/stp.json) (loop blocked, failover timed)
> - Part C: measure what TSO/GSO do to captured segment sizes and throughput

---

## Module 4 — The OSI Model

_The 7-layer framework every network conversation references_

### 4.1 Why the OSI Model Exists

- Interoperability problems before standards
- Layers as independent contracts (changing one doesn't break others)

### 4.2 The 7 Layers

- **Layer 1 — Physical:** cables, signals, bits, fiber, wireless
- **Layer 2 — Data Link:** frames, MAC addresses, switches, VLANs
- **Layer 3 — Network:** packets, IP addresses, routers
- **Layer 4 — Transport:** TCP/UDP, ports, reliability
- **Layer 5 — Session:** connection management, checkpointing
- **Layer 6 — Presentation:** encoding, encryption, compression
- **Layer 7 — Application:** HTTP, DNS, TLS, gRPC

### 4.3 Encapsulation and Decapsulation

- How data gets wrapped and unwrapped layer by layer
- Reading a captured packet: matching headers to layers
- Header sizes measured from a capture; MSS 1460 vs 1448-byte segments; tunnel overheads

### 4.4 OSI vs TCP/IP Model

- The 4-layer TCP/IP model (Link, Internet, Transport, Application)
- Mapping between OSI and TCP/IP
- Which model engineers actually use and when

### 4.5 Troubleshooting by Layer

- The layer checklist as a way to organise evidence, not a mandatory order
- Common failure signatures at each layer; "who answered?" for refusals and 5xx errors
- "Destination Host Unreachable" from your own address is an ARP failure

> **Lab 4**
>
> - **`netlab` osi lab**: a client and a `labserver` in namespaces; one plain-HTTP request
>   captured with real Ethernet headers; header sizes measured (74/66/54 bytes)
> - Fail each layer on purpose and assert it: untrusted certificate (5/6), refused by a
>   closed port and by a REJECT rule (4), dropped by a DROP rule (3/4), ARP failure behind
>   "Destination Host Unreachable" (2)
> - Prove which firewall rule answered with iptables counters; the checklist on your own machine; Wireshark byte highlighting

---

## Module 5 — IP — The Internet Protocol

_Addressing, routing, and the foundation of the internet_

### 5.1 What IP Does (and Doesn't Do)

- Best-effort, connectionless delivery — no guarantees; ICMP reports some failures, not all
- IP's core jobs: addressing and routing, plus TTL, protocol number, and fragmentation
- ECMP per-flow hashing: why "some connections fail" points at one of several paths
- `ip route get` for your own decision; ping and traceroute as evidence, not a gate

### 5.2 IPv4 Addressing

- 32-bit addresses and dotted decimal notation
- Classes A/B/C (historical context, why CIDR replaced them)
- Public vs private address space (RFC 1918), CGNAT, link-local, documentation and benchmarking ranges

### 5.3 Subnetting and CIDR

- What is a subnet and why subnetting exists
- CIDR notation: /24, /16, /8 and what the prefix means
- Calculating: network address, broadcast, host range, usable IPs; /31 (RFC 3021) and /32
- Cloud reservations and size limits: AWS, Azure, Google Cloud
- Practical: subnet a VPC for a multi-tier app

### 5.4 IPv4 Packet Header Deep Dive

- Every field as tcpdump -v prints it (tos, ttl, id, offset in bytes, flags [DF]/[+])
- TTL: loops, traceroute, hop estimates, and IMDSv2's hop limit as an access control
- Protocol field: how IP knows to hand off to TCP vs UDP vs ICMP
- Fragmentation: offsets, ID, port-less later fragments; DF and PMTUD; black holes
- DSCP / ECN: what survives in the cloud is path-specific

### 5.5 IPv6

- Why IPv4 ran out: NAT as a band-aid, IPv6 as the cure
- 128-bit addressing, canonical text form (RFC 5952), /64 subnets
- The 40-byte header: hop limit, next header, flow label; no checksum, no router fragmentation, Packet Too Big
- Neighbor Discovery (RS/RA/NS/NA, solicited-node multicast) replaces ARP
- SLAAC, DAD, stable and temporary interface identifiers; default route via fe80::
- Dual-stack, Happy Eyeballs timings, and broken-IPv6 symptoms; never block all ICMPv6

### 5.6 Special and Reserved Addresses

- Loopback: 127.0.0.1 / ::1
- Link-local: 169.254.x.x / fe80:: — metadata endpoints, VPC CNI gateways, DHCP fallback
- 0.0.0.0: default route, INADDR_ANY; IPv4-mapped ::ffff:a.b.c.d in logs
- Multicast ranges: 224.0.0.0/4, ff00::/8; documentation and benchmarking ranges

> **Lab 5**
>
> - **`subnet` Go tool**: check hand subnetting, cloud reservations, on-link membership, address kinds
> - **`netlab` ip lab** (offline): route decision, TTL 63 at the server, Time Exceeded from the router,
>   Frag Needed with `mtu 1400` cached, fragments with `[+]` and byte offsets, "Network is unreachable",
>   on-link routes between two /25s; break it (forwarding off, missing return route, PMTUD black hole)
> - **`netlab` ipv6 lab** (offline): RS/RA, SLAAC, RA default route via fe80::, NDP, Packet Too Big
>   on a 1280 link, DAD conflict → `dadfailed` — *not yet run: build environment has IPv6 disabled*
> - Your real path: traceroute, tracepath, `curl -4` vs `curl -6`

---

## Module 6 — ARP — Address Resolution Protocol

_Bridging Layer 3 addresses to Layer 2 hardware_

### 6.1 The Problem ARP Solves

- You have a destination IP. But frames need a MAC address. Why?
- Layer 2 vs Layer 3: the boundary; MACs rewritten per hop, IP kept
- The symptoms: "Destination Host Unreachable" from your own IP, EHOSTUNREACH after ~3 s, silent loss from stale mappings

### 6.2 How ARP Works

- ARP request: broadcast — 'Who has 192.168.1.1?'; ARP reply: unicast; the target learns the sender from the request
- Reading ARP in tcpdump -e: broadcast requests vs unicast probes; 42-byte frames
- The neighbor table: REACHABLE, STALE, DELAY, PROBE, FAILED, INCOMPLETE, PERMANENT
- The timers behind them (15–45 s, 5 s, 3 × 1 s) and what they mean for outages; IPv6 shares the table (NDP)

### 6.3 Gratuitous ARP

- What it is: announcing your own IP/MAC pair unsolicited; request and reply forms
- Linux updates existing entries, does not create new ones (`arp_accept`); does not announce on its own (`arp_notify`)
- Use cases: failover, VIP takeover, keepalived, MetalLB Layer 2 — and why none of it works in a cloud VPC

### 6.4 ARP Spoofing

- Poisoning a neighbor's ARP cache; IP conflicts as accidental spoofing
- Man-in-the-middle via ARP; what TLS does and does not protect
- Detection and defense: arping conflict test, arpwatch, dynamic ARP inspection, static entries

### 6.5 Proxy ARP and ARP in Cloud

- Proxy ARP: when Linux answers (route via another interface), `proxy_delay`
- Calico's 169.254.1.1 pod gateway built on proxy ARP
- How cloud providers answer ARP (AWS, Azure's single MAC, Google Cloud's /32 netmask); API-based failover
- Overlay networks where ARP is suppressed or pre-populated

> **Lab 6**
>
> - **`netlab` arp lab** (offline, 33 checks): resolution and learning from requests; STALE → DELAY →
>   unicast PROBE; FAILED after three broadcasts; VIP failover without gratuitous ARP (measured
>   12–21 s) and with one (no outage); `arp_accept`; IP conflict with two answers; `arping -D`;
>   poisoning and a PERMANENT entry; Calico-style proxy-ARP pod gateway, then broken
> - By hand: `ip -s neigh` ages, `arp_accept=1`, `arp_notify=1`, `proxy_delay=0`
> - Your own machine and cloud VMs; ARP between Docker containers

---

## Module 7 — ICMP, Ping, and Traceroute

_The diagnostics built into IP itself_

### 7.1 What Is ICMP?

- A control protocol, not a data protocol; rides inside IP (Protocol 1)
- Errors quote the offending packet (at least 8 bytes; Linux quotes up to 576) and go to its source
- Stateful firewalls treat ICMP errors as RELATED; never block Type 3 (or ICMPv6 1–4 and ND)
- How ICMP errors become the errno an application prints (refused, no route to host, network unreachable)

### 7.2 Ping

- Echo Request (Type 8) / Echo Reply (Type 0)
- Reading ping output: RTT (with propagation arithmetic), TTL, packet loss, mdev
- Options: -c, -i, -s, -M do, -t, -O, -D
- What ping can and cannot tell you (ICMP filtered; success ≠ port open or large packets fine)

### 7.3 ICMP Types in Depth

- Type 3: Destination Unreachable — codes 0, 1, 3, 4, 9, 10, 13; firewall REJECT defaults
- Type 11: Time Exceeded — the backbone of traceroute
- Type 3 Code 4 and PMTUD black holes: which side must hear it, MSS follows the cached PMTU, MSS clamping
- Type 5: Redirect — Linux defaults, `shared_media`, route exceptions
- ICMPv6 types

### 7.4 Traceroute and Path Analysis

- How TTL expiry is used to map hops; UDP port per probe; probe bursts (-N)
- UDP vs ICMP vs TCP mode traceroute; tracepath; mtr
- Asterisks: filtering, rate limiting, cloud virtual routers — not always a problem
- ICMP rate limiting as phantom loss (measured)
- Asymmetric paths: forward and return routes may differ

> **Lab 7**
>
> - **`netlab` icmp lab** (offline, 32 checks): traceroute in UDP/ICMP/TCP modes captured; a silent hop;
>   mtr at 1/s vs 10/s (0% vs 82% phantom loss); host/net/port unreachable, firewall REJECT as
>   port-unreachable and admin-prohibited, each with the application's errno and timing; redirect;
>   PMTUD working, black-holed (small OK, large hangs), fixed by MSS clamping
> - **Go lab `gotrace`**: UDP and TCP SYN probes with increasing TTL, ICMP read on a raw socket
>   (standard library only), replies matched by the quoted ports and explained; `-fixed` keeps one flow
>   for ECMP (Paris-traceroute idea)
> - By hand: default traceroute bursts, redirects on/off, MSS following the PMTU cache, ping-size MTU search
> - Your real path (three traceroute modes, tracepath, mtr); ICMP between Docker containers

---

## Module 8 — Routing

_How packets find their way across networks_

### 8.1 What Routing Does

- Moving packets between networks by making hop-by-hop forwarding decisions
- The routing decision: policy rules → table → longest prefix → metric → ECMP hash
- The routing table: destination, gateway, interface, scope, metric, protocol (kernel, boot, dhcp, static, ra, ospf/bgp)

### 8.2 Reading the Routing Table

- `ip route`, `ip route show table all`, `ip -d route`; net-tools' limits
- Default gateway: the 0.0.0.0/0 route
- Longest Prefix Match (LPM): how the router picks the best route
- ECMP and `fib_multipath_hash_policy`: per-flow hashing, why 1/N of connections fail
- `ip route get` with `from`, `iif`, `mark`, `ipproto/sport/dport`

### 8.3 Static Routing

- Adding and removing static routes: `ip route add`; persistence (netplan, nmcli)
- Route types: blackhole, unreachable, prohibit — and the errno each produces
- Metrics are not failover: a static route to a dead next hop black-holes
- IP forwarding

### 8.4 Dynamic Routing Protocols (Conceptual)

- Why static routing doesn't scale
- Distance vector (RIP, EIGRP) vs link-state (OSPF, IS-IS); BGP as path vector (Module 9)
- How OSPF converges, measured: detection dominates; silent failures vs link-down; BFD
- Cloud and CNI routing: AWS propagation, Azure effective routes, GCP Cloud Router, Calico/Cilium BGP

### 8.5 NAT — Network Address Translation

- SNAT, DNAT (backend must reply through the NAT), MASQUERADE
- Connection tracking: reading the two tuples; table exhaustion
- NAT in cloud: AWS NAT Gateway, GCP Cloud NAT, Azure NAT Gateway — per-destination port limits, idle timeouts, metrics

### 8.6 Policy Routing and Multiple Tables

- `ip rule` and multiple routing tables in Linux; source-based routing
- Use cases: multi-homed servers, VPN split tunneling, CNIs with secondary interfaces

### 8.7 Asymmetric Routing and Reverse Path Filtering

- Asymmetric routing: the SYN arrives, the SYN-ACK leaves by another path and is
  dropped by a stateful device that never saw the SYN
- Linux `rp_filter` (strict vs loose), the max(all, interface) rule, and the counter that proves a drop
- `ip route get <dst> from <src> iif <dev>` to test the decision the kernel will make

> **Lab 8**
>
> - **`netlab` routing lab** (offline, 28 checks): the decision and `src`; a stale /32 outage; blackhole/
>   unreachable/prohibit errors; ECMP split and traceroute showing two routers vs `gotrace -fixed`;
>   policy routing creating an asymmetric path that a stateful firewall drops, fixed with SNAT; DNAT and
>   conntrack tuples; SNAT port exhaustion with `conntrack -S` evidence
> - **`netlab` ospf lab** (FRR, 11 checks): four routers, OSPF adjacencies, ECMP from equal costs;
>   convergence measured after a silent failure (~3.3 s with dead 4 s) and after a link-down (~0 s)
> - By hand: metrics vs dead next hops, hash policy 0, policy-routing fix on the server, strict `rp_filter`
>   drop and counter, conntrack events, FRR `vtysh` and OSPF cost changes
>
> **Lab 8b — containerlab** `[planned]`: the same OSPF topology on containerlab for learners who prefer
> it; netlab already covers the OSPF, ECMP, return-route, and `rp_filter` exercises.

---

## Module 9 — BGP, Anycast & Internet Routing

_How traffic finds the cloud — and how the internet holds itself together_

### 9.1 BGP Basics

- Autonomous Systems (AS): what they are and how they're numbered (16/32-bit, private, documentation)
- BGP as a TCP session on port 179; iBGP vs eBGP
- Route announcements: advertising IP prefixes to peers
- Best-path selection: LOCAL_PREF, AS_PATH, origin, MED, eBGP over iBGP, IGP metric, oldest path, router ID — and longest prefix match first
- BGP convergence: hold times, BFD, withdrawals vs silent failures
- Route leaks (customer LOCAL_PREF, RFC 8212) and hijacks (more-specifics, forged origins); RPKI ROV and maxLength, ASPA

### 9.2 Anycast

- One IP address, many sites — each network's policy picks one, not "the closest"
- How CDNs and DNS resolvers use anycast; TCP over anycast in practice
- Anycast vs unicast vs multicast vs broadcast
- Failure behavior: clean withdrawal vs a site that dies without withdrawing

### 9.3 Internet Exchange Points (IXPs)

- What an IXP is: a physical location where networks interconnect; route servers
- Why IXPs reduce latency and cost; PeeringDB
- Notable IXPs, including KIXP (Nairobi, Mombasa)

### 9.4 Cloud Interconnects

- AWS Direct Connect, Azure ExpressRoute, GCP Cloud Interconnect
- When to use a cloud interconnect vs a VPN vs the public internet; private ≠ encrypted
- BGP over cloud interconnects: advertising routes between your DC and cloud; BFD

### 9.5 Internet Routing in Practice

- traceroute/mtr with AS lookups and their caveats
- Looking glasses and collectors: bgp.tools, bgp.he.net, RIPEstat, Cloudflare Radar
- Identifying if a BGP leak or hijack is affecting your traffic; what evidence to escalate

> **Lab 9**
>
> - **`netlab` bgp lab** (FRR, 26 checks): five ASes; sessions and the BGP table; AS_PATH best path;
>   LOCAL_PREF override; prepending; two-site anycast decided by a tie-breaker, failover measured
>   (withdrawal ~0.05 s vs silent failure ~2–2.7 s with hold 3 s); a /25 hijack answered by the
>   rogue server, stopped by a customer prefix filter
> - By hand: a route leak caused by customer LOCAL_PREF and fixed by an export filter; MED's reach;
>   negotiated hold times; BGP as TCP sessions
> - Saved synthetic collector observations ([routing.md](labs/routing.md)); then the real internet:
>   mtr -z, Team Cymru whois, bgp.tools/RIPEstat, anycast `colo`
> - **Cloud-style BGP** `[planned]`: emulate a VPN/interconnect gateway peering with BGP, primary and
>   backup paths, and show what traffic does when the primary withdraws

---

## Module 10 — UDP — User Datagram Protocol

_Fast, simple, connectionless transport_

### 10.1 What UDP Is

- Connectionless: no handshake, no acknowledgment, no ordering; message boundaries preserved
- UDP header: Source Port, Destination Port, Length, Checksum (8 bytes total); size limits and fragmentation
- Error reporting: ICMP port unreachable — delivered only to connected sockets
- Stateful firewalls and NATs: UDP idle timeouts (Linux 30 s / 120 s) and keepalives

### 10.2 When UDP Is the Right Choice

- Speed over reliability: DNS, DHCP, VoIP, live video, gaming
- The application owns timeouts, retries, backoff, congestion response (RFC 8085)
- QUIC: building reliability on top of UDP; what it changes for troubleshooting (UDP 443, fallback, Alt-Svc)

### 10.3 Common Protocols Using UDP

- DNS (port 53) — truncation and TCP fallback, EDNS 1232, DoT/DoH/DoQ
- DHCP (ports 67/68), NTP (port 123), SNMP, syslog
- Tunnels: WireGuard, VXLAN (4789 vs 8472), IPsec NAT-T, Geneve
- QUIC (port 443/UDP) — the transport for HTTP/3

> **Lab 10**
>
> - **`netlab` udp lab** (offline, 23 checks): DNS over UDP (2 packets) vs TCP (10); a truncated answer
>   retried over TCP; no handshake; ICMP port unreachable seen by a connected socket, not by an
>   unconnected one; MSG_TRUNC; 30% loss each way measured against 0.7² and against 2 retries;
>   DNS under 50% loss, UDP vs TCP; a stateful firewall's UDP timeout dropping a late reply
> - **Go lab `udplab`**: echo server and numbered-datagram sender with loss, retry, reordering,
>   truncation, and socket-error reporting
> - By hand: conntrack UNREPLIED, `UdpNoPorts`, EMSGSIZE and fragments; your own machine's UDP counters

---

## Module 11 — TCP — Transmission Control Protocol

_Reliable, ordered delivery — and all the complexity that comes with it_

### 11.1 What TCP Provides

- Reliable, ordered byte stream or an error; an ACK is not application processing
- The price: handshake latency, header overhead, state everywhere, head-of-line blocking, slow start

### 11.2 The TCP Header

- Ports, sequence and acknowledgment numbers, data offset, eight flags (NS is historic), window, checksum, options
- tcpdump's flag letters and relative sequence numbers (`-S` for absolute)

### 11.3 The Three-Way Handshake

- SYN → SYN-ACK → ACK; ISNs; options negotiated in the SYNs only
- How connects fail: refused (RST or ICMP port unreachable), forged RSTs and their TTL, no route to host (ICMP prohibited, ARP), timed out (silence)
- The SYN retransmission schedule, classic and Linux 6.5+ (`tcp_syn_linear_timeouts`), measured
- Lost SYN-ACK or final ACK; server-speaks-first protocols
- Accept queue overflow: silent SYN drops, `ss -ltn` Recv-Q vs Send-Q, `ListenOverflows`

### 11.4 Connection Teardown

- FIN exchange and states; active vs passive closer; half-close
- RST: when hosts send it, ECONNRESET/EPIPE, RST bursts after an abort
- TIME_WAIT: fixed 60 s on Linux, what it costs, port exhaustion to one destination, `tcp_tw_reuse` (and why not `tcp_tw_recycle`)
- CLOSE_WAIT (no timeout) vs orphaned FIN_WAIT_2 (`tcp_fin_timeout`)

### 11.5 Sequence Numbers, Acknowledgments and Retransmission

- Cumulative and delayed ACKs; duplicate ACKs, SACK, RACK-TLP
- RTO: initial 1 s, minimum 200 ms, backoff, `tcp_retries2`; how backoff stretches an outage (measured)

### 11.6 Flow Control

- rwnd, zero window, persist probes; reading Recv-Q/Send-Q and `rwnd_limited`
- Buffer autotuning (`tcp_rmem`) and how SO_RCVBUF disables it

### 11.7 Congestion Control

- cwnd, the bandwidth-delay product, initial window 10, slow start and HyStart
- Reno and CUBIC (loss-based), the Mathis bound, bufferbloat; BBR (model-based); measured CUBIC vs BBR
- Selecting algorithms per system, route and socket; ECN

### 11.8 TCP States

- The state machine; states that point at problems
- `ss` filters, timers and `-i` fields; `nstat` counters; `ss -s` pitfalls (CurrEstab includes CLOSE_WAIT; host-wide totals in containers)

### 11.9 TCP Options in Practice

- MSS, timestamps' 12 bytes, MSS clamping (measured)
- TSO/GSO/GRO: why captures show segments larger than the MTU
- SACK and D-SACK, window scaling (and middleboxes that break it), timestamps, keepalive

### 11.10 How TCP Events Surface as Application Errors

- Refused, no route, kernel vs application timeout, reset, broken pipe, clean close — as errno, Go, Python, Node.js, Java and curl report them (all produced in the lab)
- Full catalogue across protocols in Appendix H `[planned]`

> **Lab 11**
>
> - **`netlab` tcp lab** (offline, 35 checks): a whole connection captured; TSO super-packets vs real
>   segments; MSS clamped in transit; refused, forged RST, ICMP port unreachable, admin prohibited and
>   timed-out connects with their errnos; the SYN schedule timed; an accept queue overflowing; CLOSE_WAIT,
>   orphaned FIN_WAIT_2, RST abort, FIN without an answer; zero window and persist probes; 1% loss repaired
>   by fast retransmit; CUBIC vs BBR on a deep-buffered 20 Mbit/s bottleneck; a 3 s outage stretched by
>   RTO backoff
> - **Go lab `tcplab`**: misbehaving server modes and a client that classifies connect failures and
>   reports `tcp_info` (this also covers the error-classification goal once planned as `errlab`)
> - By hand: TIME_WAIT expiry, the SYN schedule's sysctls, finding a stuck reader from the sender,
>   application connect timeouts against a full queue, per-socket congestion control; your own machine,
>   and delay plus loss with `netem` where the kernel has it

---

## Module 12 — DNS — Domain Name System

_A common application dependency: resolver behavior, views, caching, and evidence_

### 12.1 Why DNS Exists

- HOSTS.TXT and why it could not scale; delegation
- `/etc/hosts` and `nsswitch.conf` still come first — and `dig` ignores them

### 12.2 DNS Architecture

- Stub, recursive (caching, validating), root, TLD and authoritative servers; cloud and cluster resolvers
- Referrals and glue, seen with `dig +norecurse`
- What a real resolver sends: QNAME minimisation, DNSKEY fetches, trust-anchor signalling (captured)

### 12.3 DNS Record Types

- A, AAAA, CNAME (and its rules), MX, NS, SOA, PTR, TXT, SRV, CAA, HTTPS/SVCB, DS/DNSKEY/RRSIG/NSEC

### 12.4 TTL, Caching and Negative Caching

- Caching layers, from the runtime to the resolver; who caches nothing
- A record change followed through two resolvers (measured)
- Negative caching: min(SOA TTL, MINIMUM), measured; NXDOMAIN vs NODATA
- Migrations with caches in mind

### 12.5 Resolution, Step by Step

- Iterative resolution with `dig +trace` and `dnslab trace`; CNAME restarts across zones
- Lame delegations and parent/child mismatches

### 12.6 DNS over UDP, TCP and Encrypted Transports

- EDNS and the 1232-byte buffer; truncation and TCP (mandatory, RFC 7766)
- DoT, DoH, DoQ — what they protect and what they bypass

### 12.7 DNSSEC in Practice

- Secure, insecure and bogus — the `ad` flag, SERVFAIL, Extended DNS Errors; `+cd` as a diagnostic

### 12.8 Split-Horizon and Private DNS

- Views chosen by the asking resolver (BIND views in the lab); resolver access control (REFUSED)
- Route 53 private hosted zones, Azure Private DNS, GCP private zones

### 12.9 DNS-Based Load Balancing and Failover

- Multiple records, weighted, latency/geo (and ECS), health checks; what failover time really is

### 12.10 The Stub Resolver

- `resolv.conf`: search, ndots, timeout, attempts, rotate, use-vc; measured query expansion and dead-server penalty
- systemd-resolved, musl, Docker's embedded resolver, Kubernetes pods

### 12.11 Cross-Cloud Private DNS and Resolver Behaviour

- Platform resolvers; the `privatelink` CNAME pattern and why the wrong resolver returns the public IP
- Hybrid resolution: conditional forwarding to Route 53 Resolver / Azure DNS Private Resolver inbound endpoints
- Custom DNS servers that do not forward to the platform resolver; forwarding loops
- Go's resolvers: pure Go vs cgo (`GODEBUG=netdns=go+1` / `cgo+1`)

### 12.12 DNS Troubleshooting Tools

- dig options and reading every part of its output; getent; resolvectl; tcpdump; dnslab

### 12.13 Common DNS Failure Patterns

- NXDOMAIN, NODATA, SERVFAIL (and its causes), timeouts (including a resolver busy with a dead authority), REFUSED
- Search lists, ndots, `/etc/hosts`, the 5-second pod stall, managed `resolv.conf`

> **Lab 12**
>
> - **`netlab` dns lab** (offline, 32 checks): a private root, TLD and two authoritative BIND servers (signed
>   zones, split-horizon views, a lame and a dead delegation) and two validating Unbound resolvers;
>   referrals by hand and traced; a cold vs warm resolver captured; a record change and a new name timed
>   through the caches; NODATA; truncation and TCP; SERVFAIL, timeouts and REFUSED with EDE; DNSSEC
>   secure/insecure/bogus and `+cd`; glibc search list, ndots, a dead nameserver, `/etc/hosts`; Go's resolver
> - **Go lab `dnslab`**: a standard-library DNS message codec, an iterative `trace` (the `resolve` lab once
>   planned), `watch` to time changes across servers (covers the planned `dnsdiff`), `query`, `lookup`
> - By hand: the resolver's cache, serve-stale, why a trace does not validate, stub over TCP, stable view
>   differences; your own machine and the real root
> - **Cloud lab** `[planned]`: resolve an AWS private hosted zone from an Azure VM over a VPN
>   using a Route 53 inbound endpoint and a conditional forwarder

---

## Module 13 — TLS — Transport Layer Security

_Encryption, certificates, and the trust that holds the web together_

### 13.1 Why TLS Exists

- Cleartext credentials captured; what TLS hides and what it does not (SNI, endpoints, sizes, timing)

### 13.2 Certificates and PKI

- X.509 fields that matter: SAN, validity, issuer, constraints, EKU, key
- Chains and trust stores; self-signed vs private CA
- Public certificates today: ACME, lifetimes falling to 47 days (SC-081), Certificate Transparency

### 13.3 The TLS 1.2 Handshake

- Messages as captured; ECDHE and forward secrecy vs RSA key exchange; reading TLS 1.2 cipher suites

### 13.4 TLS 1.3

- One round trip; what is encrypted (captured and decrypted); suites vs groups; resumption and 0-RTT
- Post-quantum hybrid key exchange (X25519MLKEM768) and larger ClientHellos (measured)

### 13.5 What a Handshake Costs

- Round trips measured through a delay relay: TLS 1.3, TLS 1.2, resumption, HelloRetryRequest

### 13.6 SNI, ALPN and ECH

- SNI and the default certificate; ALPN and HTTP/2; ECH (RFC 9849)

### 13.7 Certificate Validation and Its Failures

- Eleven failures in Go's and OpenSSL's words; protocol-version refusals; revocation today

### 13.8 Mutual TLS

- CertificateRequest and acceptable CAs; TLS 1.3 post-handshake rejection; clients that send nothing
- Public CAs no longer issue clientAuth certificates

### 13.9 TLS in Practice

- openssl s_client/x509/verify, curl timing, key logs and decryption, termination vs re-encryption vs passthrough

### 13.10 Trust Stores per Runtime and TLS-Inspecting Proxies

- curl/OpenSSL, Python ssl and requests, Go, Node, Java, Docker, the OS store
- Inspection: how it fails in each runtime, proving it (issuer inside vs outside), fixing trust without disabling verification
- Writing key logs from Go with `tls.Config.KeyLogWriter`

> **Lab 13**
>
> - **`netlab` tls lab** (offline, 30 checks): cleartext vs TLS captures; TLS 1.2 and 1.3 handshakes decoded
>   with tshark and decrypted with a key log; round trips through a 50 ms relay (1.3, 1.2, resumption,
>   HelloRetryRequest); ClientHello sizes (Go's post-quantum share); eleven certificate failures in Go and
>   curl; no SNI; TLS 1.0/1.1 refusal; mTLS with no, good and foreign client certificates; a transparent
>   TLS-inspecting proxy breaking and then fixed in curl, Go and Python
> - **Go lab `tlslab`** (the planned `tlsinspect`, plus a PKI generator, server, relay and intercepting proxy)
> - By hand: `openssl s_client`/`verify`, Node and Java under inspection, the server's view of a bad client
>   certificate, forcing versions and groups; your own machine

---

## Module 14 — Network Performance

_Latency, throughput, windows, queues, loss, MTU — and why connections feel slow_

### 14.1 Latency: Where the Time Goes

- Propagation (~5 µs/km in fibre), transmission, queueing, processing; ping measures the round trip
- How one HTTPS request spends round trips: `curl -w` phases (DNS, TCP, TLS, server, transfer)
- The first transfer on a connection is the slowest: handshakes plus slow start vs a reused connection

### 14.2 Bandwidth, Throughput and Goodput

- Capacity vs bits carried at a named layer vs useful bytes per second
- Measuring goodput from a capture — at the sender, since a receiver never sees lost originals
- Short transfers do not reach steady state; iperf3 to separate network from application

### 14.3 The Bandwidth-Delay Product and the Window Ceiling

- throughput ≤ window ÷ RTT; BDP = bandwidth × RTT; worked examples
- Linux autotuning (tcp_rmem/tcp_wmem), fixed SO_RCVBUF disabling it, rwnd_limited/sndbuf_limited in `ss -ti`

### 14.4 Queues and Bufferbloat

- A loss-based sender fills a deep queue: same throughput, many times the delay
- AQM (fq_codel, CAKE), shaping below the bottleneck, delay-aware senders
- UDP has no congestion control: overload becomes loss

### 14.5 The Cost of Loss

- The Mathis model: throughput ≈ (MSS / RTT) × (1.22 / √p), independent of bandwidth
- Measured CUBIC vs the model; BBR under random loss; fix the loss first
- Choosing congestion control per system, route or socket (BBR no longer requires fq)

### 14.6 MTU, MSS and Path MTU Discovery

- Sizes per layer; common smaller MTUs (PPPoE, WireGuard, IPsec, VXLAN, GRE, cloud VPCs); jumbo frames
- PMTUD and ICMP fragmentation-needed / Packet Too Big; finding a path MTU with DF pings and tracepath
- The black hole; restoring ICMP vs PLPMTUD (tcp_mtu_probing) vs MSS clamping — measured

### 14.7 Diagnosing a Slow Connection — Measure, Then Localise

- Which phase is slow (curl -w) → window-, loss-, queue- or size-limited (ss -ti) → is it the network (iperf3) → where on the path (mtr, tracepath)

> **Lab 14** (`labs/netlab/perf.json`, `pathlab`)
>
> - Timing breakdown and cold vs reused connections across a 50 ms path
> - Window ÷ RTT against the BDP with three window sizes
> - Bufferbloat with deep and short queues; UDP overload
> - Single-flow throughput under loss vs the Mathis model, CUBIC and BBR
> - Goodput vs throughput from a sender-side capture at 1% loss
> - Path MTU discovery, a PMTUD black hole, PLPMTUD recovery and MSS clamping

---

## Module 15 — HTTP/2, HTTP/3, gRPC & WebSockets

_Modern application protocols your team uses every day_

### 15.1 HTTP/1.1 — The Baseline

- One request at a time per connection; keep-alive; pipelining never deployed; six connections per host
- Measured: a page of 20 resources on 1 vs 6 HTTP/1.1 connections vs HTTP/2 vs HTTP/3; application head-of-line blocking

### 15.2 HTTP/2 — Streams on One TCP Connection

- Binary framing (9-byte header, frame types, flags), streams, pseudo-headers, SETTINGS, flow control (WINDOW_UPDATE)
- ALPN vs h2c prior knowledge; HPACK measured; push removed → 103 Early Hints; RFC 9218 priorities
- TCP head-of-line blocking measured with one dropped packet; GOAWAY, coalescing, MAX_CONCURRENT_STREAMS

### 15.3 HTTP/3 and QUIC

- QUIC over UDP with TLS 1.3; handshake, resumption and 0-RTT measured; replay and RFC 8470
- Stream independence measured — and QPACK's dynamic table re-creating head-of-line blocking
- Connection IDs and migration; Alt-Svc and HTTPS DNS records; UDP blocked and fallback; debugging with key logs and qlog

### 15.4 gRPC

- On the wire: POST /pkg.Service/Method, length-prefixed protobuf, status in trailers, trailers-only errors, HTTP 200 ≠ success
- Status codes and the HTTP-to-gRPC mapping; deadlines (grpc-timeout) and cancellation; streaming; reflection and health
- Proxies and load balancers: HTTP/1.1 hops (505, ALB 464), connection-level balancing of long-lived HTTP/2, gRPC-Web

### 15.5 WebSockets

- The upgrade handshake and Sec-WebSocket-Accept; frame format and masking (what it is for); ping/pong and close
- Over HTTP/2 and HTTP/3 (RFC 8441, 9220); idle timeouts measured vs pings

### 15.6 Server-Sent Events and Polling

- text/event-stream, ids and Last-Event-ID resumption; buffering proxies measured; long polling, polling, WebTransport; choosing

> **Lab 15** (`labs/netlab/web.json`, `weblab`, `h3lab.py`, `pathlab`)
>
> - ALPN and h2c; page load per protocol; HTTP/1.1 head-of-line blocking; multiplexed streams in a decrypted capture; HPACK
> - QUIC handshake, resumption and 0-RTT, with the 0-RTT flight captured; one lost packet in HTTP/2 vs HTTP/3 (with and without QPACK's dynamic table); UDP blocked
> - gRPC on the wire, error status in HTTP 200, deadlines, streaming, no reflection, an HTTP/1.1 proxy vs h2c
> - WebSocket handshake and frames, an idle-timeout relay with and without pings; SSE with resumption and through a buffering proxy

---

## Module 16 — Networking in Docker & Containers

_How containers get network access and talk to each other_

### 16.1 Network Namespaces

- What a namespace isolates; `ip netns` names vs Docker's `/var/run/docker/netns` (SandboxKey); `nsenter`
- Per-namespace sysctls (br_netfilter) vs global kernel state

### 16.2 veth Pairs and the Bridge

- `eth0@ifN` and finding the host-side peer; the `bridge link` @ifb0 display trap
- The bridge as switch and gateway: same-bridge traffic is switched (ttl 64), everything else routed

### 16.3 Egress: Routing and MASQUERADE

- Measured without and with MASQUERADE (captures, conntrack); Docker's per-network rule
- NAT off vs Docker 28+'s raw-table protection; `gateway_mode_ipv4=routed`

### 16.4 Publishing Ports

- DNAT in PREROUTING and OUTPUT; client address preserved; 127.0.0.1 and docker-proxy; hairpin and br_netfilter
- Docker's actual nat/filter/raw rules (28+ chain layout); published ports bypass INPUT/ufw; DOCKER-USER with conntrack's original port; loopback binding; nftables backend

### 16.5 Network Modes and Isolation

- Default vs user-defined bridge, internal, none, host, macvlan/ipvlan, overlay; isolation between networks and `network connect`
- MTU per network and per daemon

### 16.6 Container DNS

- The embedded server at 127.0.0.11, its iptables redirect in the container's namespace, aliases
- Default bridge vs user-defined; host(…) upstreams from the host's namespace vs `--dns` from the container's; troubleshooting order

### 16.7 The Daemon's Own Networking

- Pulls by dockerd: daemon proxy configuration; registry, auth and blob endpoints
- BuildKit RUN networking; address pools (`bip`, `default-address-pools`) and collisions

### 16.8 SNAT Port Exhaustion

- Tuple uniqueness, the fifth connection's silent timeout, conntrack `insert_failed`/`drop`, NAT gateway metrics, fixes

> **Lab 16** (`labs/netlab/containers.json`, `labs/netlab/docker.json`)
>
> - Build a container network by hand: bridge, veths, MASQUERADE with and without, DNAT from outside, the host and 127.0.0.1, hairpin with and without br_netfilter, SNAT port exhaustion
> - Inspect real Docker from the host: veth pairing, SandboxKey and nsenter, resolv.conf per network, the 127.0.0.11 redirect, where upstream DNS queries come from, NAT/filter/raw rules
> - A host INPUT rule that does not protect a published port and a DOCKER-USER rule that does; loopback publishing and docker-proxy; egress masquerade
> - Network isolation and `network connect`; internal, none and host networks; a 1400-byte MTU; a Docker subnet colliding with a corporate route

---

## Module 17 — Kubernetes Networking

_Pod networking, services, DNS, network policy, ingress and service mesh_

### 17.1 The Network Model

- Pod↔pod and node↔pod without NAT (measured), egress masquerade, non-overlapping pod/Service CIDRs

### 17.2 CNI: How Pods Get Connected

- Plugin vs agent; overlay vs direct routing vs VPC-native; VXLAN on UDP 8472 with its fdb/neigh entries (captured); MTU 1450 (measured); host-gw by hand; choosing a CNI (Weave discontinued)

### 17.3 Services and kube-proxy

- iptables chain structure and hashed names; random per-connection choice (measured spread); conntrack; iptables/nftables (GA 1.33)/IPVS (deprecated 1.35)/eBPF
- ClusterIP is not pingable; NodePort with externalTrafficPolicy Cluster vs Local (source IP, drops, health checks); hairpin (measured, with re-balanced retransmissions)
- Refused (no endpoints) vs timeout (drops); LoadBalancer, ExternalName, headless

### 17.4 Cluster DNS

- resolv.conf, names, CoreDNS forwarding; ndots:5 query counts for glibc and Go (measured); fixes and their trade-offs
- DNS failure signatures (2 ms vs 40 s, measured); the 5-second conntrack race (not reproduced; how to confirm)

### 17.5 NetworkPolicy

- Per-direction, additive, selector logic, after Service DNAT, DNS egress; enforcement is the CNI's (a drop, measured); AdminNetworkPolicy

### 17.6 Ingress and Gateway API

- Ingress controllers (Ingress NGINX retired March 2026); Gateway API roles and resources (v1.5 Standard features); where L7 paths fail

### 17.7 Service Mesh

- Sidecar and ambient models; identity, mTLS, authorization, coverage; interception; what to verify

### 17.8 Debugging from the Node

- crictl/nsenter, the pod's veth, ephemeral debug containers; image pulls are the node's; rule/drop/conntrack inspection

> **Lab 17** (`labs/netlab/k8s.json`, `kproxy`; real-cluster track in `labs/kubernetes`)
>
> - Two namespace nodes with pods on bridges and flannel-style VXLAN: no-NAT checks, VXLAN capture, MTU, egress masquerade
> - Services from kube-proxy's iptables rules: spread, conntrack, unpingable ClusterIP, hairpin off/on, NodePort Cluster vs Local
> - Failure signatures: no endpoints (refused) vs policy drop (timeout); DNS with no endpoints (2 ms) vs dropped (40 s)
> - DNS query counts under ndots:5 for glibc and Go; trailing dots; ndots:1 by hand
> - On a real cluster (your VM): NetworkPolicy enforcement, Ingress, Linkerd identity — `labs/kubernetes/lab.py`

---

## Module 18 — Firewalls, Security & Zero Trust

_What firewall verdicts look like to clients, how rules decide, and authorizing by identity instead of address_

### 18.1 What a Firewall Verdict Looks Like

- ACCEPT, REJECT (tcp-reset, port unreachable → refused; host/admin-prohibited → "No route to host"), DROP (timeout), measured from the client

### 18.2 iptables and nftables

- Hooks, tables (raw, mangle, nat, filter, security), chains; first match wins; policies; counters as evidence
- iptables-nft: one rule set, two views; every base chain on a hook must accept; legacy coexistence
- An nftables inet host firewall; never drop all ICMP/ICMPv6; LOG silent outside the host namespace (`nf_log_all_netns`)

### 18.3 Stateful and Stateless Filtering

- Conntrack states; the ESTABLISHED,RELATED rule; NACL-style return rules for ephemeral ports and the hole they open (measured)
- AWS security groups vs NACLs, Azure NSGs, GCP firewall rules and policies; cloud path-analysis tools

### 18.4 Segmentation

- Tiers with explicit allowed paths; testing denied paths; blocked paths look like timeouts

### 18.5 SYN Floods, SYN Cookies and Rate Limits

- SYN queue exhaustion and cookies, with nstat counters; connection rate limits (recent, hashlimit, nft limit) and NAT side effects

### 18.6 WAF and DDoS Protection

- OWASP CRS / vendor rule sets, anomaly scoring, false positives and request IDs; volumetric, protocol and application attacks; locking the origin to the edge

### 18.7 Zero Trust

- NIST SP 800-207, BeyondCorp; identity-aware proxies; workload identity and mTLS authorization; segmentation as a second layer; what to verify

> **Lab 18** (`labs/netlab/firewall.json`, `tlslab serve -authz`)
>
> - Every verdict from the client side, with nc messages and counters; first-match order; iptables vs nft views; LOG with and without `nf_log_all_netns`
> - Stateful client firewall; a NACL-style stateless filter: dropped SYN-ACK capture, the return rule, the hole, the stateful fix
> - Three-tier segmentation with allowed and denied paths
> - SYN flood with cookies off and on; per-source connection rate limit
> - mTLS identity authorization: 200, 403, missing and foreign certificates, and the same identity from another network

---

## Module 19 — VPNs, Tunnels & Overlay Networks

_How traffic is encapsulated, encrypted and carried across networks you don't control — and how tunnels fail_

### 19.1 What a Tunnel Is

- Underlay and overlay, inner and outer headers; what each tunnel carries, encrypts and rides on
- Overhead measured per tunnel; provider VPN MTU/MSS tables (AWS, Google Cloud)

### 19.2 WireGuard

- Keys, peers, endpoints; AllowedIPs as cryptokey routing (one prefix, one peer)
- Handshake and data on the wire; failure signatures (wrong key, AllowedIPs, unreachable endpoint)
- NAT, roaming and PersistentKeepalive (mapping expiry measured)

### 19.3 IPsec (IKEv2)

- IKE_SA_INIT, IKE_AUTH, CHILD_SAs, traffic selectors; UDP 500/4500 and ESP; MOBIKE and NAT-T
- swanctl and kernel xfrm views; AUTHENTICATION_FAILED, NO_PROPOSAL_CHOSEN, TS_UNACCEPTABLE; cloud gateway habits

### 19.4 MTU Through Tunnels

- PMTUD inside a tunnel; the black hole when the outer path is smaller; MSS clamping vs tunnel MTU (and WireGuard padding); DF behaviour by implementation

### 19.5 VXLAN, Geneve and GRE

- VXLAN overhead and MTU (with and without a bound device), outer fragmentation and Linux's PMTU handling; Geneve; GRE

### 19.6 OpenVPN and TCP-over-TCP

- TLS control channel, data ciphers, mssfix, DCO; TCP transports turn loss into delay (measured)

### 19.7 SSH Tunnels

- -L, -D, -R; what the target sees; refused and prohibited forwards; GatewayPorts; socks5 vs socks5h; ProxyJump and cloud alternatives

### 19.8 Site-to-Site, Client VPNs and Overlays as Products

- Routes, overlaps, BGP over tunnels; full vs split tunnel and DNS; mesh VPNs (Tailscale minicourse)

> **Lab 19** (`labs/netlab/tunnels.json`)
>
> - WireGuard site-to-site and a client behind NAT: handshake capture, overhead, MTU, wrong key, AllowedIPs, NAT expiry and keepalives
> - A PMTU black hole through the tunnel, fixed by MSS clamping (TCP only) and by the tunnel MTU
> - IPsec IKEv2 with strongSwan: IKE and ESP on the wire, SAs, PSK, proposal and traffic-selector failures
> - VXLAN overhead, MTU and outer fragmentation; SSH -L/-D/-R and their failure messages; OpenVPN UDP vs TCP under loss

---

## Module 20 — Sockets and Application-Level Networking

_Where applications meet the network: socket calls, their errors, and the limits applications hit_

### 20.1 The Socket API and Its Errors

- socket/bind/listen/accept/connect/read/write/close and the errno each raises; non-blocking connect in runtimes (strace)

### 20.2 TCP Is a Byte Stream

- Writes vs reads (measured), framing; TCP_NODELAY, Nagle, autocorking

### 20.3 Ephemeral Ports and TIME_WAIT

- EADDRNOTAVAIL per destination tuple; who closes first holds TIME_WAIT; tcp_tw_reuse rates; IP_BIND_ADDRESS_NO_PORT

### 20.4 Connection Reuse in HTTP Clients

- Keep-alive cost measured; Go's MaxIdleConnsPerHost = 2; unread bodies; pools in other clients; HTTP/2 multiplexing

### 20.5 Thread per Connection vs an Event Loop

- Threads and memory for 2000 idle connections; select/poll/epoll/io_uring

### 20.6 File Descriptor Limits

- EMFILE with connected-but-unanswered clients; limits in shells, systemd, containers; Go's own rlimit raise

### 20.7 Binding: SO_REUSEADDR and SO_REUSEPORT

- Restart over TIME_WAIT, real conflicts, REUSEPORT groups and their spread

### 20.8 Backpressure

- Bounded vs unbounded relays (measured); UDP receive-buffer drops, truesize, rmem_max caps

> **Lab 20** (`labs/netlab/sockets.json`, `socklab`)
>
> - Writes vs reads and segments; ephemeral-port exhaustion per destination, TIME_WAIT by closer, tcp_tw_reuse
> - HTTP client reuse: no keep-alive, Go's default idle limit, a right-sized pool, unread bodies
> - Thread-per-connection vs epoll with 2000 idle connections; EMFILE; Go's rlimit raise
> - SO_REUSEADDR and SO_REUSEPORT; a relay with and without backpressure; UDP receive-buffer drops

---

## Module 21 — Packet Capture and Analysis

_Capturing the right packets at the right place, knowing what a capture can miss, and turning pcaps into answers_

### 21.1 What a Capture Can and Cannot Tell You

- AF_PACKET capture points, offloads, NAT, encryption; stating where a capture was taken

### 21.2 tcpdump: Capturing Without Missing Things

- Options and BPF filters; --immediate-mode (measured); kernel drops; -i any (LINUX_SLL2); snaplen; ring buffers; production safety

### 21.3 Localizing Loss

- Captures at several points (measured); NAT and matching packets across points; cloud mirroring

### 21.4 tshark: From Packets to Answers

- Display vs capture filters, two-pass filters, fields, conv/expert/io/follow statistics, editcap/mergecap/capinfos
- Network vs server time from two captures; one-way delay and clocks

### 21.5 TCP Problems in tshark

- The same loss at sender and receiver; zero window (persist probes as keep-alives); other signatures

### 21.6 DNS in a Capture

- rcode, dns.time, unanswered queries

### 21.7 TLS Without and With Keys

- SNI/ALPN, certificates in TLS 1.2 vs 1.3, alerts; SSLKEYLOGFILE and its equivalents

### 21.8 Containers, Kubernetes and the Cloud

- nsenter, kubectl debug, node interfaces, remote streaming, mirroring

> **Lab 21** (`labs/netlab/capture.json`)
>
> - --immediate-mode, kernel drops, snaplen, -i any, ring buffers
> - A drop localized from four capture points
> - Network vs server time and one-way delay from two captures
> - Loss at sender and receiver, zero window in Expert Info, unanswered DNS, TLS with and without a key log

---

## Module 22 — Observability & Troubleshooting Methodology

_A method for going from symptom to cause, and what each kind of evidence can and cannot show_

### 22.1 A Method

- Define, baseline, hypothesize from the signature, cheapest discriminating evidence, localize, change one thing, re-measure; RED and USE

### 22.2 What Each Kind of Evidence Shows

- Metrics, logs, traces, flow records, captures: answers, blind spots, cost

### 22.3 Failure Signatures

- Error text and timing → first hypothesis (refused, timeouts, no route, reset, EADDRNOTAVAIL, EMFILE, DNS, fixed timers, MTU); imprecise error strings

### 22.4 Metrics

- Histogram quantile interpolation vs exact percentiles (measured); means; counters over a window

### 22.5 Traces

- W3C traceparent; server and client spans; service vs network time (measured); logs joined by trace ID; head vs tail sampling (measured)

### 22.6 Flow Records

- conntrack as flow records; cloud flow logs (ACCEPT/REJECT, NODATA/SKIPDATA, Azure VNet flow logs); path analyzers

### 22.7 Playbooks

- "It fails", "it's slow", "some requests fail"

> **Lab 22** (`labs/netlab/observe.json`, `obslab`)
>
> - Baseline; a slow dependency in spans, histograms and means; logs joined to traces
> - A lossy network located between client and server spans, confirmed by host counters
> - An error burst: metrics, tail vs head sampling, logs
> - A dropped port (504 at the timeout, no backend span, unreplied flows) vs a refused port (502 in ms); repair verified

---

## Module 23 — Additional Critical Topics

_DHCP, load balancers, time and proxies, measured on the wire_

### 23.1 DHCP

- DORA on the wire (OFFER/ACK unicast unless the broadcast flag is set; ping-before-offer delay); options; lease lifecycle (T1 unicast renew, T2 rebind, release); reservations
- Failure modes measured: no server (no 169.254 on Linux), pool exhaustion (silence; server log), rogue server (first OFFER wins), snooping by bridge filter; relay and giaddr; checksum offload and udhcpc

### 23.2 Load Balancers

- L4 vs L7 and what the backend sees; algorithms
- HTTP vs TCP health checks (measured); detection window; retries and redispatch for silent vs refusing backends (measured); keep-alive vs idle timeout (502s)
- Stickiness: inserted cookie vs source hash (measured) and their costs

### 23.3 Time

- Why clocks matter (TLS validity measured, Kerberos 5 min, expiry, logs)
- NTP offset/delay from four timestamps (measured); asymmetric delay error; source selection, falseticker, "no majority" with two sources (measured with chrony); stratum; step vs slew; cloud sources; NTS

### 23.4 Proxies

- Forward, reverse, transparent; CONNECT vs absolute-URI; proxy environment variables in curl and Go (measured, including `no_proxy` vs `NO_PROXY`)
- X-Forwarded-For appended, overwritten and forged (HAProxy, nginx); Forwarded (RFC 7239); PROXY protocol v1/v2 and both mismatches (measured); forged PROXY headers

> **Lab 23** (`labs/netlab/dhcp.json`, `labs/netlab/edge.json`, `labs/netlab/time.json`, `edgelab`)
>
> - DHCP: DORA, renew, release, exhaustion, no server, rogue server and a bridge filter, relay
> - HAProxy and nginx: round robin, HTTP vs TCP checks, failover window (DROP vs RST, with and without redispatch), XFF, PROXY protocol and mismatches, stickiness, forward proxy variables and CONNECT
> - Time: offset and delay, a wrong server, asymmetric delay, chrony falseticker and no-majority, TLS with a skewed clock

---

## Module 24 — Linux Kernel Networking Internals `[planned]`

_Where packets go missing between the NIC and your application_

Builds on Modules 18–22; this module explains the drops the earlier modules only name.

### 25.1 The Receive and Send Paths

- NIC → ring buffer → NAPI → GRO → netfilter PREROUTING → routing → INPUT →
  socket buffer → application; the send path through the qdisc
- Where each stage keeps a counter, and which tool reads it

### 25.2 Netfilter and Connection Tracking

- Hooks and where iptables/nftables chains attach
- conntrack table limits (`nf_conntrack_max`), timeouts, and drop evidence
  (`conntrack -S`, kernel log)

### 25.3 Socket and Queue Limits

- Listen backlog: SYN queue, accept queue, `somaxconn`, SYN cookies
- Socket buffers (`rmem`, `wmem`) and when to tune them
- `softnet_stat` and per-CPU backlog drops

### 25.4 Reverse Path Filtering and Routing Decisions

- `rp_filter` modes and asymmetric drops
- Policy routing interactions

### 25.5 Finding the Drop with eBPF

- `kfree_skb` and kernel drop reasons
- `bpftrace`, `dropwatch`, `perf`; `pwru` for per-packet tracing
- XDP and tc-BPF: how Cilium-style datapaths change where drops happen

### 25.6 Counters Reference

- `nstat`, `/proc/net/snmp`, `/proc/net/netstat`, `ethtool -S`, `tc -s qdisc`

> **Lab 24** `[planned]`
>
> - Set `nf_conntrack_max` low, run load, find the drops
> - Create asymmetric routing with strict `rp_filter` and find the drop with `kfree_skb` tracing
> - Overflow a listen backlog and find it in `nstat`
> - **Go lab `netstat-exporter`**: expose retransmits, listen overflows, conntrack
>   usage, and drops as Prometheus metrics

---

## Module 25 — Cloud and CI/CD Path Troubleshooting `[planned]`

_From a failing CI job to the exact hop, rule, or setting that broke it_

Builds on Modules 16–24; this module applies the whole course to multi-cloud and CI paths.

### 26.1 Cloud Network Concepts Side by Side

- Azure VNet / AWS VPC / GCP VPC equivalents: filtering, routing, NAT, private
  access, private DNS, peering and hubs, site-to-site, and flow evidence

### 26.2 Evaluation Order

- AWS: route table → NACL (both directions) → security group
- Azure: NSG at subnet and NIC → UDR → Azure Firewall or NVA
- Which layer logs what, and the tool that shows the effective result

### 26.3 Egress Identity

- Which public IP your traffic actually leaves from: NAT gateway, firewall, load
  balancer outbound rules, or none
- Newer Azure subnets may have no default outbound internet access; check explicitly
- Destination allow-lists and why "the IP is allowed" is often the wrong IP

### 26.4 Private Access Across Clouds

- Private Endpoint / PrivateLink / Private Service Connect and their DNS
- Azure ↔ AWS site-to-site IPsec with BGP; overlapping CIDRs; MTU across the tunnel

### 26.5 CI Runners

- GitHub-hosted runners (dynamic IPs, larger runners with static IPs, Azure
  private networking) vs self-hosted runners in a VNet or VPC
- The outbound endpoints runners need, and the GitHub meta API for IP ranges
- Proxy configuration for the runner service, job steps, and Docker daemon

### 26.6 Container Registries

- ECR: API endpoint, registry endpoint, and S3 for layers; auth tokens and
  repository policies with IP or VPC conditions
- ACR data endpoints; GHCR and `pkg-containers.githubusercontent.com`
- Pull failures by stage: DNS, connect, TLS, auth, manifest, layer download

### 26.7 Long-Lived Connections

- Idle timeouts on load balancers, NAT gateways, and firewalls
- SNAT port exhaustion: symptoms (`EADDRNOTAVAIL`, intermittent timeouts) and
  metrics on each cloud

> **Lab 25** `[planned]` — Terraform; destroy after each session
>
> - AWS: private subnet without NAT → timeouts; add NAT; break a NACL return rule
> - Azure: NSG outbound deny vs a UDR to a missing firewall — compare symptoms and
>   Network Watcher output
> - Azure ↔ AWS VPN with BGP and cross-cloud private DNS
> - **Go lab `cloudpath`**: given a source NIC and destination IP:port, read effective
>   routes and NSG/SG rules through the Azure and AWS SDKs and report the deciding rule

---

## Module 26 — Capstone Labs

_Bring everything together in realistic, high-pressure scenarios_

### Capstone Lab 1 — Full Stack Trace

From `curl https://example.com` to rendered response — capture and annotate every step: DNS, TCP, TLS, HTTP/2, and teardown. Identify each protocol layer in the Wireshark capture.

### Capstone Lab 2 — Break and Fix DNS

An application cannot reach its backend by its short name. Using only CLI tools, diagnose and fix the DNS failure. The instructor injects one of: wrong resolver, wrong search domain, split-horizon (the public view), or a stale cache after a migration (`labs/netlab/case.json`, `caselab probe`).

### Capstone Lab 3 — Find the Bottleneck

Given a packet capture of a slow file transfer, identify the root cause. The capture contains: high RTT latency, TCP retransmissions, a small receive window, and a PMTUD black hole. Which one is the actual bottleneck?
Use the corrected [capstone assessment](modules/module-26-capstone-labs.md): begin
with one controlled variant and compare a healthy baseline. Counts of symptoms
alone do not establish a bottleneck, and multiple constraints can coexist.

### Capstone Lab 4 — Certificate Chain Failure

An HTTPS service returns a TLS error. Walk through the diagnosis from the error message, through openssl s_client, to the cause — a missing intermediate, a replaced trust bundle, or the wrong certificate (the first two produce the same application error) — and deploy a fix that keeps verification on (`labs/netlab/case.json`).

### Capstone Lab 5 — Kubernetes Networking Maze

Two pods in different namespaces cannot communicate. The failure could be at the CNI level, a Network Policy, a Service misconfiguration, or a CoreDNS issue. Trace through each layer to find the root cause.

### Capstone Lab 6 — Production Incident Simulation

A cloud-hosted API starts timing out for 15% of users. Metrics show elevated error rates in one region. Use VPC Flow Logs, NAT gateway metrics, and a packet capture to decide which cause the evidence supports — the dataset points to SNAT port allocation on one destination, and the containers lab reproduces it (`caselab case6` is the instructor's key).
The supplied dataset is synthetic (3 timeouts in 20 attempts). Learners must
distinguish filtering, local source ports, conntrack state, and SNAT capacity rather
than assume the cause from the brief. Instructor setup and solutions are separate.

### Capstone Lab 7 — Azure Runner Cannot Reach AWS ECR `[planned]`

A self-hosted Actions runner in an Azure VNet behind Azure Firewall fails to pull
from ECR. Faults are injected one at a time: VNet DNS that cannot resolve the
registry, an NSG outbound deny, a UDR to a dropping firewall, a TLS-inspecting
proxy, an ECR policy pinned to the wrong egress IP, S3 missing from the firewall
allow-list, and a firewall idle timeout during a large push. Diagnose each from the
runner's error and prove it with evidence.

### Capstone Lab 8 — Build `netdoctor` `[planned]`

Combine the course's Go labs into one CLI that runs on the failing host and reports
the first failing layer with evidence: DNS across resolvers, route lookup, TCP
connect with classified errors, TLS chain, HTTP timing, proxy detection, path MTU,
TCP-based loss, and a bounded capture with automatic analysis. Output text and
JSON so it can run as a CI step. Validate it against Capstone 7's faults.

---

## Appendices

The complete references are in [appendices.md](appendices.md). The sections below
are an index, not a replacement for those explanations and qualification notes.

### Appendix A — Essential Commands Reference

- ip addr, ip route, ip link, ip neigh
- ss -tnp, ss -unp, ss -s
- dig, dig +trace, dig @resolver
- tcpdump one-liners for every common scenario
- openssl s_client -connect host:443
- curl -v, curl --http3, curl --resolve
- iperf3 -s / iperf3 -c host -t 30
- mtr --report host

### Appendix B — tcpdump Filter Cheat Sheet

- By host, port, protocol, network, direction
- Combining filters with and/or/not
- TCP flag filters: SYN, RST, FIN
- Capture only DNS, ARP, ICMP, TLS ClientHello

### Appendix C — Wireshark Display Filter Cheat Sheet

- `tcp.flags.syn==1 and tcp.flags.ack==0`
- `dns.flags.response == 0`
- `tcp.analysis.retransmission`
- `tls.handshake.type == 1` (ClientHello)
- `http2.streamid`, `http.request`

### Appendix D — TCP Flags and States Reference

- All TCP flags explained
- TCP state machine diagram
- Common stuck states and remediation

### Appendix E — Common Port Numbers

- Application ports every engineer must know
- Ephemeral port range

### Appendix F — Recommended RFCs

- RFC 791 — IP
- RFC 9293 — TCP (RFC 793 is the historical base)
- RFC 768 — UDP
- RFC 1034/1035 — DNS
- RFC 8446 — TLS 1.3
- RFC 9000 — QUIC
- RFC 9114 — HTTP/3
- RFC 9113 — HTTP/2 (replaces RFC 7540)
- RFC 1122 — Host requirements `[planned]`
- RFC 5681 — TCP congestion control `[planned]`
- RFC 6298 — TCP retransmission timer `[planned]`
- RFC 4271 — BGP-4 `[planned]`
- RFC 8899 — Packetization Layer PMTUD `[planned]`

### Appendix G — Lab Environment Setup Guide

- Installing VirtualBox / UTM / Lima for local VMs
- Installing kind or minikube for Kubernetes labs
- Installing Wireshark, tshark, tcpdump
- Installing Python socket dependencies
- Installing WireGuard tools
- Installing Go, containerlab, FRR images, bpftrace, and Hubble `[planned]`

### Appendix H — Error Catalogue `[planned]`

- One row per failure: error as reported by Go, Node, curl, and Python → errno or
  status → what happened on the wire → usual causes → first command to run
- Covers connect, DNS, TLS, proxy, HTTP-from-middlebox, and MTU signatures

### Appendix I — Go Networking Reference `[planned]`

- `net`, `net/http`, `crypto/tls`, `net/http/httptrace` patterns used in the labs
- Timeouts, error matching, resolver selection, key logging, raw sockets
