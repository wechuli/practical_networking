# Module 1: The History of Networking

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 0 — Before We Begin
> **Estimated time:** 2 hours

## Overview

Every constraint you will fight in your career has a reason. The 1500-byte MTU that causes silent failures in VPN tunnels. The 16-bit port field, and the much smaller ephemeral port range carved out of it, that runs out under high connection rates. The reason DNS uses UDP by default. The reason nothing in IP proves who sent a packet. None of these are arbitrary — they are the residue of specific engineering decisions made under specific constraints, many of them decades ago.

Engineers who do not know this history treat these constraints as walls. Engineers who do know it treat them as design decisions that can be worked around, tuned, or understood well enough to stop causing unexplained incidents. When you hit a PMTUD black hole, or watch TIME_WAIT connections pile up, or wonder why IPv6 is still not everywhere after three decades, the history is the explanation.

This module is not background reading. It is the answer to "why does it work this way?" — a question you will ask constantly as you go deeper into the following modules.

## What You Will Build Toward

- Explain why packet switching was chosen over circuit switching, and what it costs: queues, loss, and no delivery guarantee
- Explain how ARPANET became an *internet* of networks, and why intelligence sits in the endpoints rather than the network
- Trace specific production constraints to their origin: MTU, port range, IPv4 exhaustion, missing source authentication
- Use `rfcstatus`, a Go tool, to tell whether an RFC is current before you cite it, and what replaced or changed it
- Explain what the cloud changed — boundaries, ownership, and visibility — and what it did not change: the protocols

---

## 1.1 The Problem Networking Solved

### What This Is

Before networks, computers were islands. Data moved between them physically — on magnetic tape, on punch cards, carried by hand. This was not just inconvenient. It made computation impossible to share. If you needed to run a program on a computer in another building, you either moved the data to that machine or moved yourself to it.

The problem networking solved was not "how do we send data faster." It was "how do we make geographically separated computers behave as if they share resources." Speed came later. The original problem was connectivity at all.

### How It Works

The first solutions were point-to-point — a dedicated wire between two specific machines. This worked, but it scaled catastrophically. Connecting N machines requires N×(N-1)/2 dedicated links. Ten machines need 45 links. A hundred machines need 4,950.

The solution was shared infrastructure: a common medium that any machine could use to reach any other, with addressing to distinguish who was talking to whom. This is the principle every network still uses today, from Ethernet in your office to the internet itself.

### Diagram

```
FULL MESH OF DEDICATED LINKS (does not scale)

  [A]───[B]
   │╲   /│
   │  ╲╱  │
   │  ╱╲  │
   │╱   ╲│
  [C]───[D]

  4 machines = 6 dedicated links
  10 machines = 45 links
  100 machines = 4,950 links


SHARED INFRASTRUCTURE (scales)

  [A]   [B]   [C]   [D]
    \    |    |    /
     \   |    |   /
      [shared medium / switch]
          |
         ...
      Add more machines: add one link each
```

### What You Will See in Production

Point-to-point links are common in modern switched Ethernet, including host-to-switch
and switch-to-switch connections. What scales poorly is a dedicated full mesh
between every endpoint, not point-to-point links themselves. A dedicated access
link does not guarantee an uncontended end-to-end service through shared switches,
routers, queues, and provider infrastructure. The same arithmetic returns in the
cloud: a full mesh of VNet or VPC peerings grows as N×(N-1)/2, which is why hub-and-spoke
designs (Azure Virtual WAN, AWS Transit Gateway) exist.

---

## 1.2 Circuit Switching vs Packet Switching

### What This Is

This is the most consequential architectural decision in the history of networking, and its outcome shapes every protocol you will ever use.

In the 1960s there were two competing models for how to move data across a shared network. The telephone system used **circuit switching**: before any call, a dedicated path was reserved end-to-end between the two parties. That path was held open for the duration of the call, whether or not either party was speaking.

**Packet switching** was the alternative: data is broken into chunks (packets), each chunk is addressed independently, and each chunk finds its own path through the network. No path is reserved in advance. If one link is congested or broken, packets take a different route.

The idea arrived from two directions. At RAND, Paul Baran (1960–1964) designed
distributed networks of small message blocks that could survive the loss of many
nodes. At the UK's National Physical Laboratory, Donald Davies (1965) proposed the
same mechanism for a different reason — efficient sharing of lines by bursty
computer traffic — and coined the word *packet*. Leonard Kleinrock's queueing
theory work gave a way to reason about delay in such networks. ARPANET's own
motivation was resource sharing between research computers.

### How It Works

**Circuit switching:**

1. Caller requests a connection to a destination
2. Each telephone switch along a path reserves capacity for the call
3. The path is held exclusively for this call
4. Data flows
5. Call ends, resources are released

**Packet switching:**

1. Data is broken into packets, each labeled with source and destination address
2. Each packet is sent into the network independently
3. Each router examines the destination address and forwards the packet toward it
4. Packets may queue, be dropped, take different paths, and arrive out of order
5. The receiver (or a protocol like TCP) puts them back together and recovers losses

Computer traffic is bursty: long silences, then a burst. A reserved circuit sits
idle through the silences. Packet switching lets many bursty senders share one
link, because their bursts rarely all coincide. This is **statistical
multiplexing**. Its price is that when bursts *do* coincide, packets wait in a
queue — and when the queue is full, they are dropped.

### Diagram

```
CIRCUIT SWITCHING
                    [Reserved path — held for duration of call]
  [Caller] ═══════════════════════════════════════════════ [Callee]
              Hop 1       Hop 2       Hop 3
            [Switch A]─[Switch B]─[Switch C]
                │                       │
         Capacity reserved       Capacity reserved
         Can't be used           Can't be used
         by anyone else          by anyone else


PACKET SWITCHING
  [Sender]                                           [Receiver]
     │                                                   ▲
     │  Packet 1 ──▶ [Router A] ──▶ [Router B] ──────────┤
     │  Packet 2 ──▶ [Router A] ──▶ [Router C] ──────────┤
     │  Packet 3 ──▶ [Router D] ──▶ [Router B] ──────────┘
     │
  Each packet forwarded independently.
  Links shared with other traffic; each router has a queue.
  If Router B fails, packets can be routed around it.
```

### What You Will See in Production

Packet switching is why the internet has no single point of failure — in theory. In practice, routing protocols take time to converge, and traffic does not instantly reroute around failures. You will see this when a major provider has a routing incident: traffic is black-holed for minutes while BGP reconverges, even though an alternative path exists.

The other consequences come straight from the design:

- **No delivery guarantee at the IP layer.** Packets can be lost, reordered, or
  duplicated. TCP exists to impose reliability on top. This is one of the most
  important facts in this course.
- **Queues are where latency and loss come from.** A congested link doesn't refuse
  new traffic the way a busy phone line does; it delays it, then drops it. Rising
  latency under load (bufferbloat) and loss that appears only at peak hours are
  both queue behaviour (Module 14).
- **Per-flow paths.** Modern routers usually keep packets of one flow on one path
  (ECMP hashing, Module 8), so one connection can be stuck on a bad path while
  other connections to the same destination are fine.

---

## 1.3 ARPANET

### What This Is

ARPANET, funded by the US Department of Defense's Advanced Research Projects
Agency (ARPA) and switched on in late 1969, was the first wide-area packet-switched
network. It was not the internet — it was one network — but it established
principles the internet still follows.

The design goals were specific and deliberate:

- **Robust communication**: resource sharing and operation despite component failures;
  do not equate ARPANET's research goals with a universal guarantee of survivability
- **Heterogeneity**: different kinds of computers must be able to communicate
- **Decentralization**: no single controlling node whose failure takes down the network
- **Shared resources**: multiple users sharing the same infrastructure

### How It Works

Each site had an **Interface Message Processor (IMP)** — a dedicated minicomputer,
built by BBN, whose only job was moving packets. The IMPs were connected to each
other by 50 kbit/s leased telephone lines and formed the network; each site's host
computer handed messages to its local IMP and did not need to know how the network
worked. The IMP was a packet switch inside a single network — an ancestor of the
router rather than a router in today's sense, which forwards between *different*
networks (1.4). Hosts talked to each other using the Network Control Program (NCP).

The first message on ARPANET was sent on October 29, 1969, from UCLA to the Stanford Research Institute. The message was meant to be "LOGIN"; the system crashed after "LO". The first network transmission was also the first network error.

### Diagram

```
ARPANET, end of 1969 — the first four sites

   [UCLA host]        [SRI host]        [UCSB host]       [Utah host]
        │                  │                  │                  │
      [IMP]              [IMP]              [IMP]              [IMP]
        └──────────── 50 kbit/s leased lines between IMPs ───────┘

  IMP = Interface Message Processor: a packet switch.
  Hosts talked only to their local IMP; IMPs moved packets between sites.
```

This separation of concerns — the host does not run the network, the network does
not run the host — is the same principle behind layering, TCP/IP, and the virtual
NIC in a modern cloud VM.

### What You Will See in Production

The IMP concept is why your application does not need to know how packets are routed. Your code calls `connect()`, and the operating system, network stack, and routers handle everything below that. When that abstraction breaks down — when a routing loop causes a packet to circulate until its TTL expires, or when a misconfigured route sends traffic to the wrong place — it breaks in ways your application cannot detect or explain on its own. That is when you need the tools in this course.

---

## 1.4 From ARPANET to the Internet

### What This Is

By the early 1970s there were several packet networks — ARPANET, packet radio,
packet satellite — that could not talk to each other. The problem became
**internetworking**: connecting *networks* rather than computers. The answer
defined the internet's architecture, and its trade-offs explain a large share of
today's incidents.

### How It Works

**Gateways between networks (1974).** Vint Cerf and Bob Kahn's paper *A Protocol
for Packet Network Intercommunication* proposed that each network stay as it was.
**Gateways** — today's routers — would forward packets between networks, and a
common protocol running *in the hosts* would provide end-to-end delivery. The
networks underneath only had to carry packets on a best-effort basis.

**TCP splits into TCP and IP (1978).** The original single protocol was split.
IP carried datagrams between networks with no guarantees, while TCP, running only
at the endpoints, added reliability, ordering, and flow control. This split is
why UDP can exist: an application that doesn't want TCP's guarantees can use IP
almost directly.

**Flag Day (1983).** ARPANET hosts moved from NCP to TCP/IP during 1982, with
1 January 1983 as the deadline after which NCP was no longer supported. That
date is often given as the internet's birthday.

**The end-to-end principle (1981–1984).** Saltzer, Reed, and Clark argued that
functions such as reliability and security can only be completely implemented at
the endpoints, so the network in the middle should stay simple. Routers forward
packets; hosts do everything else.

**The design priorities (1988).** David Clark's *The Design Philosophy of the DARPA
Internet Protocols* listed the goals in priority order. Survivability came first:
communication should continue despite loss of networks or gateways. Support for
multiple types of service and a variety of networks came next, then distributed
management and cost-effectiveness, then low-effort host attachment.
**Accountability for resources came last.**

### Diagram

```
INTERNETWORKING — the design every packet still follows

  [Host A]──[ Network 1 ]──[Gateway]──[ Network 2 ]──[Gateway]──[ Network 3 ]──[Host B]
     │                        │                         │                        │
     │                    forwards IP               forwards IP                  │
     │                    (no per-connection        (no per-connection           │
     │                     state, best effort)       state, best effort)         │
     │                                                                           │
     └────────── TCP: reliability, ordering, flow control — endpoints only ──────┘

  Intelligence at the edges, a simple core.
```

### What You Will See in Production

The priority list reads like a root-cause catalogue for modern incidents:

- **Accountability came last.** Nothing in IP proves who sent a packet. Source
  addresses can be forged, which is what makes reflection and amplification DDoS
  attacks possible (Module 18). Authentication was added later, in layers above:
  TLS, SSH, application credentials.
- **The core keeps no per-connection state, by design.** The devices that *do* keep
  state — NAT gateways, stateful firewalls, load balancers — were added later and
  violate the end-to-end principle. They are exactly where connections break
  mysteriously: idle timeouts, port exhaustion, asymmetric paths a firewall
  never saw the start of (Modules 8, 18, 26).
- **The endpoints do the work.** When throughput collapses or a connection
  stalls, the explanation is often in an endpoint's TCP state rather than in the
  routers (Modules 11, 14, 25).

---

## 1.5 Standards — RFCs and How to Read Their Status

### What This Is

A Request for Comments (RFC) is a numbered document published by the **RFC
Editor**. RFCs come from several streams: the IETF (most protocol standards), the
IRTF (research), the IAB (architecture), the Independent Submission stream, and an
Editorial stream for the series itself. When you need to know exactly how TCP
handles an edge case, or what a DNS SOA record must contain, the relevant
standards-track RFC is the definitive text.

Not every RFC is a standard. Each has a **status**:

| Status | Meaning |
| --- | --- |
| Proposed Standard | A stable, reviewed standard. Most protocols you use are here: TLS 1.3 (RFC 8446), HTTP/2 (RFC 9113), QUIC (RFC 9000) |
| Internet Standard | Proven by wide deployment; also gets an STD number (TCP is STD 7, RFC 9293) |
| Best Current Practice | Operational guidance with a BCP number (BCP 14 defines MUST and SHOULD) |
| Informational, Experimental | Not standards |
| Historic | Kept for the record; no longer recommended |

Since 2011 (RFC 6410) the standards track has two maturity levels, Proposed and
Internet Standard. Older documents may still show the retired Draft Standard
level. An **Internet-Draft** is a work in progress, not an RFC, and should not be
cited as a standard.

### How It Works

Published RFC text is immutable. Instead of editing, later RFCs **update** parts of
an earlier document or **obsolete** it entirely, and errata are tracked
separately. So the behaviour a protocol "has" is the latest base document plus
every RFC that updates it.

For example, RFC 9293 consolidated TCP's base specification and many intervening
updates; TCP did not stay unchanged from 1981 until RFC 793 was replaced in 2022.
TLS 1.3 is specified by RFC 8446, and secure TLS 1.2 deployments remain relevant.
Before citing any RFC in a ticket or a design, check three things: is it obsoleted,
what updates it, and what is its status?

Capitalised **MUST**, **SHOULD**, and **MAY** have defined meanings (RFC 2119 and
RFC 8174, together BCP 14). An implementation that ignores a SHOULD may be
conforming; one that ignores a MUST is not.

The IETF is deliberately informal — no membership, no voting, no binding authority.
Protocols succeed because implementations adopt them, not because a standards body
mandates them. Hence its maxim: "rough consensus and running code."

### Diagram

```
HOW A PROTOCOL BECOMES AN RFC, AND HOW IT CHANGES

  Internet-Draft ──▶ RFC: Proposed Standard ──▶ RFC: Internet Standard (+ STD number)
  (work in progress,        │                          │
   expires, not citable)    │  later RFCs may...       │
                            ▼                          ▼
                  UPDATE it (both stay valid)   OBSOLETE it (read the new one)

KEY RFCs YOU WILL ENCOUNTER IN THIS COURSE

  RFC 768   ──  UDP                                     (1980)
  RFC 791   ──  IPv4                                    (1981)
  RFC 1034  ──  DNS concepts                            (1987)
  RFC 1035  ──  DNS implementation                      (1987)
  RFC 1122  ──  Requirements for Internet hosts         (1989)
  RFC 4271  ──  BGP-4                                   (2006)
  RFC 8200  ──  IPv6 (obsoletes RFC 2460)               (2017)
  RFC 8446  ──  TLS 1.3                                 (2018)
  RFC 9000  ──  QUIC                                    (2021)
  RFC 9110  ──  HTTP semantics                          (2022)
  RFC 9113  ──  HTTP/2 (obsoletes RFC 7540)             (2022)
  RFC 9114  ──  HTTP/3                                  (2022)
  RFC 9293  ──  TCP (obsoletes RFC 793)                 (2022)

  This list was current when written. Check before citing — that is the lab.
```

### What You Will See in Production

When a vendor's implementation does something unexpected, the RFC is how you confirm whether it is a bug or a deliberate choice. When you are debugging a TLS negotiation failure and need to know exactly what goes in a ClientHello, RFC 8446 Section 4.1.2 tells you. You do not need to memorize RFCs — you need to know they exist, how to find the current one, and how to read MUST and SHOULD. Citing an obsoleted RFC in an escalation ("RFC 793 says…") is an easy way to lose an argument you were right about.

---

## 1.6 Key Milestones and Why They Matter

### What This Is

These are not dates to memorize. They are the moments when a specific constraint you will encounter in production was created or resolved.

### The Timeline

```
NETWORKING TIMELINE — production-relevant milestones

1969 ── ARPANET first message (UCLA → SRI)
        └─ Packet switching becomes the model

1973 ── Ethernet invented at Xerox PARC (Metcalfe)

1974 ── Cerf and Kahn: gateways connecting separate networks
        └─ The internetworking design every packet still follows

1980 ── DIX Ethernet II specification; IEEE 802.3 follows (1983)
        └─ Maximum payload of 1500 bytes. RFC 894 (1984) carries IP over it:
           the 1500-byte IP MTU you will meet for your entire career

1981 ── IPv4 (RFC 791) and TCP (RFC 793) specified
        └─ 32-bit addresses: about 4.3 billion. Seemed enormous. Was not.

1983 ── ARPANET's NCP-to-TCP/IP transition deadline (1 January)
1983 ── First DNS specifications (RFC 882/883); base specs RFC 1034/1035 in 1987
        └─ Replaces one shared hosts.txt file
           The hosts file still exists on your machine and still overrides DNS

1986 ── Congestion collapse: throughput between LBL and UC Berkeley drops
        from 32 kbit/s to 40 bit/s
1988 ── Jacobson's congestion avoidance and control added to TCP
        └─ Slow start, congestion window, retransmit backoff — why TCP slows
           down when it sees loss (Modules 11, 14)

1988 ── Morris worm (sendmail, fingerd, rsh trust, weak passwords)
        └─ Security was not in the original design; CERT/CC is founded

1989 ── BGP (RFC 1105); BGP-4 in 1994, current spec RFC 4271 (2006)
        └─ Policy routing between independent networks

1991 ── The World Wide Web goes public (Berners-Lee, CERN)
        └─ HTTP on top of TCP; networking becomes mainstream infrastructure

1993 ── CIDR (RFC 1518/1519) replaces class-based addressing
        └─ Buys IPv4 decades and makes route aggregation possible

1994 ── NAT described (RFC 1631)
        └─ Many hosts share one public IP. Breaks end-to-end reachability;
           NAT state is behind many idle-timeout and port-exhaustion incidents

1995 ── NSFNET backbone retired; the commercial internet takes over
1995 ── First IPv6 specification (RFC 1883); current spec RFC 8200 (2017)
        └─ 128-bit addresses — and three decades of partial adoption

1995 ── SSL 2.0 (Netscape); SSL 3.0 in 1996
1999 ── TLS 1.0 (RFC 2246)

2006 ── Amazon EC2 launches
        └─ Networks become API objects: VPCs, security groups, route tables

2008 ── Kaminsky DNS cache-poisoning attack
        └─ Resolvers randomise source ports as well as query IDs
           (you will watch this in the lab)

2011 ── IANA allocates its last free IPv4 blocks to the regional registries
        └─ IPv4 becomes something you buy; CGNAT and paid cloud public IPs follow

2013 ── Docker; 2014 ── Kubernetes
        └─ Network namespaces, veth pairs, and overlays become everyday tools

2015 ── HTTP/2 (RFC 7540; now RFC 9113)
        └─ Fixes HTTP head-of-line blocking, not TCP head-of-line blocking
2015 ── Let's Encrypt opens to the public
        └─ Free automated certificates; HTTPS becomes the default

2016 ── Mirai botnet floods Dyn's DNS service
        └─ Many large sites unreachable for hours because their DNS provider was

2018 ── TLS 1.3 (RFC 8446)
        └─ 1-RTT handshake, encrypted certificate, forward secrecy required

2021 ── TLS 1.0 and 1.1 formally deprecated (RFC 8996)
2021 ── Facebook withdraws its own BGP routes
        └─ Its DNS servers become unreachable, so its names stop resolving
           worldwide — and its internal tools fail with them

2021 ── QUIC (RFC 9000); 2022 ── HTTP/3 (RFC 9114)
        └─ Transport over UDP, encrypted by design; no TCP head-of-line blocking

2022 ── TCP consolidated in RFC 9293
        └─ Folds in decades of updates; wire format and handshake unchanged
```

### What You Will See in Production

Ethernet's conventional 1500-byte IP MTU, documented in RFC 894, is one constraint
when adding a tunnel. A lower path MTU does not inevitably fail: correct sizing,
fragmentation where permitted, or functioning PMTUD can accommodate it. NAT and
L7 proxying are different mechanisms; X-Forwarded-For is a proxy request header,
not a universal consequence of NAT. Historical context helps explain these choices,
but current observations establish the cause of a particular incident.

The 2016 and 2021 outages share a lesson worth keeping: **DNS is a dependency of
almost everything, including your recovery tools.** When names stop resolving,
check DNS before anything else, and know how to reach critical systems by IP.

---

## 1.7 The TCP/IP Model and Why Two Models Exist

### What This Is

You will hear about two models: the OSI model (7 layers) and the TCP/IP model (4 layers). Module 4 covers OSI in depth. Here you need to understand why both exist and which one maps to reality.

### How It Works

The OSI reference model was developed by ISO from the late 1970s and published as
ISO 7498 in 1984, as a vendor-neutral framework for interoperability. A full OSI
protocol suite was built too, but it lost to TCP/IP in deployment. The model
survived as vocabulary.

TCP/IP was developed independently through DARPA research and evolved through
practice ("running code"). It was implemented before OSI and does not map cleanly
onto it. The internet runs on TCP/IP. RFC 1122 describes its layers as link,
internet, transport, and application.

```
OSI MODEL (7 layers)         TCP/IP MODEL (4 layers)     WHAT RUNS HERE

7  Application  ────────────┐
6  Presentation ────────────┼──  Application  ─────────  HTTP, DNS, SSH,
5  Session      ────────────┘                             SMTP, gRPC
                                                          (TLS sits between
                                                           TCP and the app)

4  Transport    ────────────────  Transport   ─────────  TCP, UDP, QUIC (on UDP)

3  Network      ────────────────  Internet    ─────────  IP, ICMP

2  Data Link    ────────────┐
1  Physical     ────────────┴──  Link         ─────────  Ethernet, Wi-Fi, ARP

OSI is the vocabulary.
TCP/IP is the implementation.
You need to know both because:
  - Engineers say "Layer 3" and "Layer 7" using OSI numbering
  - The protocols you capture follow the TCP/IP structure
  - Some protocols straddle layers (ARP maps Layer 3 addresses to Layer 2 ones;
    TLS and QUIC don't fit one layer) — don't let the model argue with the packets
```

### What You Will See in Production

When someone says "this is a Layer 4 issue," they mean TCP or UDP — transport layer. When they say "Layer 7 load balancer," they mean it makes routing decisions based on HTTP headers, not just IP and port. When they say "that's a Layer 2 problem," they mean switching, MAC addresses, VLANs. The OSI numbers are the lingua franca. Know them without having to count on your fingers.

---

## 1.8 How the Cloud Changed Networking

### What This Is

Cloud computing did not change the underlying protocols. TCP/IP works the same in Azure, AWS, and GCP as it does on bare metal. What the cloud changed is where the boundaries are, who controls what, and which failure modes are visible to you.

### How It Works

On bare metal in your own data center, you control everything: the physical switch, the router, the firewall, the cable. When something breaks, you can inspect everything.

In the cloud, software-defined networking exposes virtual networks, subnets, and
firewall policies. A cloud subnet is not necessarily an ordinary Ethernet broadcast
domain; providers can proxy neighbor resolution and restrict broadcast/multicast.
The underlying physical devices are managed by the provider.

```
TRADITIONAL DATA CENTER

  Your Application
        │
  Your OS Network Stack
        │
  Physical NIC ──────── Physical Cable
                               │
                        Physical Switch
                               │
                        Physical Router
                               │
                        Physical Firewall
                               │
                           Internet

  You control: everything above the internet boundary
  You can inspect: everything


CLOUD (Azure VNet / AWS VPC / GCP VPC)

  Your Application
        │
  Your OS Network Stack
        │
  Virtual NIC (appears physical to your OS)
        │
  [─────────────────────────────────────────]
  [  Provider's software-defined network    ]  ← you configure it,
  [  Virtual routing, NSGs / security       ]    you cannot inspect it
  [  groups / firewall rules, NAT gateways, ]
  [  route tables, private DNS — software   ]
  [─────────────────────────────────────────]
        │
  [Physical provider network — not visible to you]
        │
    Internet

  You control: network, subnet, filtering, route-table, and NAT configuration
  You can inspect: flow logs (Layer 3/4 metadata), effective-rule and
                   reachability tools, captures on your own instances
  You cannot inspect: the physical network, the hypervisor's packet path,
                      or the root cause of provider-side latency
```

### What You Will See in Production

The most common cloud networking mistake is applying bare-metal mental models to
virtual networks:

- **The OS view is not the network view.** The guest's default route points at the
  subnet's first host address, a virtual router you do not control. Whether a
  packet then reaches the internet is decided by the platform's route table, NAT,
  and filtering, none of which `ip route` inside the VM shows.
- **Filtering differs by provider and layer.** AWS security groups are stateful,
  while AWS network ACLs are stateless. Azure NSGs are stateful and can apply at
  both the subnet and the NIC. GCP firewall rules apply to the whole VPC.
- **Addresses you cannot use.** AWS and Azure reserve five addresses in every
  subnet, and GCP reserves four. An Azure /29 (8 addresses) leaves three usable;
  AWS's smallest subnet, a /28, leaves eleven.
- **Layer 2 is emulated.** The platform answers ARP and drops most broadcast and
  multicast, so Layer 2 tools such as `arping` behave differently or not at all.
- **Platform services live at special addresses.** Instance metadata is at
  169.254.169.254 on all three providers. Azure's platform DNS and health
  probes come from 168.63.129.16.

Modules 16–18 and 22 cover cloud filtering and diagnostics, and Module 25 works
through cross-cloud paths end to end.

---

## Lab: History You Can Observe

> **Profile:** Portable for part 1 (any machine with Go). Linux with sudo for part 2.
> **Time:** 30–40 minutes.

### Part 1 — Is This RFC Current? (`rfcstatus`)

[`rfcstatus`](../labs/go/cmd/rfcstatus) reads the RFC Editor's official index and
says, for each RFC: its status, whether it was obsoleted and by what (following
the chain to the current document), what updates it, and its STD number. It
stream-parses the index with `encoding/xml`, so it never holds the raw XML in
memory.

```bash
cd labs/go
go test ./cmd/rfcstatus                    # uses a trimmed sample index; offline
curl -fsSLO https://www.rfc-editor.org/rfc-index.xml   # download once
go run ./cmd/rfcstatus -index rfc-index.xml 793 2460 7540 768
```

Against the test fixture, the tool prints (the real index has longer lists):

```
RFC 793  Transmission Control Protocol (September 1981)
  status      INTERNET STANDARD; Legacy stream
  verdict     OBSOLETED — read RFC 9293 instead
  chain       RFC 793 → RFC 9293
  updated by  RFC 1122, RFC 3168, RFC 6093
  errata      https://www.rfc-editor.org/errata/rfc793
```

**Exercises:**

1. Run it on 793, 2460, and 7540. **Verify:** each verdict is `OBSOLETED`, pointing
   to 9293, 8200, and 9113 respectively.
2. Run it on 2246 (TLS 1.0). Follow the chain to its end and write down every step.
   Is the last RFC in the chain still RFC 8446 on the day you run it? Standards keep
   moving; that is why you check rather than remember.
3. Run it on 1122, then on 9293. Note how 9293 *updates* 1122 rather than
   obsoleting it: both stay in force, and you need to read both.
4. Pick an RFC you have seen cited in a ticket, a vendor document, or an error
   message. Is the citation current?
5. Read [`index.go`](../labs/go/cmd/rfcstatus/index.go). Why does `Successors`
   return a *list* of chains rather than one RFC? (Hint: a document can be split
   into several replacements.)

If the download fails, run `./bin/phases https://www.rfc-editor.org/rfc-index.xml`
(built in Module 0) to see which step fails: DNS, connect, proxy, or TLS.

### Part 2 — See the 2008 DNS Fix on Your Machine

After the Kaminsky attack, resolvers made forged answers much harder to accept by
randomising the UDP **source port** of each query as well as its 16-bit query ID.
You can watch both:

```bash
# Terminal 1
sudo tcpdump -ni any 'udp port 53 and host 1.1.1.1'

# Terminal 2 — query a public resolver directly, bypassing any local stub
for i in 1 2 3 4 5; do dig +short @1.1.1.1 example.com A >/dev/null; done
```

**Verify:** each query line looks like
`IP <your-ip>.<port> > 1.1.1.1.53: <id>+ [1au] A? example.com. (<len>)`, where `[1au]`
is dig's EDNS record. Both the source `<port>` and the query `<id>` change
unpredictably between queries.

Captured while writing this module, with the queries sent to a loopback address
where no server listens (so there are no replies):

```
IP 127.0.0.1.35200 > 127.0.0.1.53: 36652+ [1au] A? example.com. (52)
IP 127.0.0.1.55287 > 127.0.0.1.53: 4912+ [1au] A? example.com. (52)
IP 127.0.0.1.49440 > 127.0.0.1.53: 38672+ [1au] A? example.com. (52)
```

Three queries, three unrelated source ports and query IDs. An off-path attacker
must now guess both, about 2^16 × 2^14 combinations with a typical port range,
rather than 2^16.
Before 2008, many resolvers used one fixed source port, so an attacker only had to
guess the 16-bit ID.

If your network blocks outbound DNS to public resolvers, you will see queries but no
replies, and `dig` times out. That is also a finding: write down which resolver your
network expects you to use (`resolvectl status`). You can still see the randomisation
by repeating the capture on `lo` with `dig +tries=1 +time=1 @127.0.0.1 example.com`,
exactly as above.

### Write-Up

For each of these, write one line naming the historical decision behind it:

- a VPN that breaks large transfers but not small ones;
- a DDoS that uses other people's DNS servers to hit you;
- an idle database connection that dies after a few minutes;
- an Azure /29 subnet that can only fit three VMs.

---

## Common Failure Patterns

| Symptom | Likely Cause | How to Confirm | Fix |
| --- | --- | --- | --- |
| Packets silently dropped in a VPN tunnel | MTU mismatch — 1500 byte Ethernet MTU minus tunnel overhead exceeds path MTU | `ping -M do -s 1400 <remote_ip>` — reduce size until ping succeeds | Set MTU on tunnel interface, or clamp MSS with iptables |
| High connection rate causes `EADDRNOTAVAIL` | Possible local address/ephemeral-port exhaustion | Check configured range, reserved ports, source address, destination tuples, and errors | Correct leaks or pool churn; measure before tuning |
| IPv4 `169.254.x.x` is the only address | Link-local configuration, possibly fallback after DHCP failure | Check intended address mode and client logs | Repair DHCP if it was intended; IPv6 link-local is a separate mechanism |
| Cloud VM cannot reach the internet, but `ip route` shows a default route | The platform has no outbound path: no NAT gateway or public IP, a route table sending 0.0.0.0/0 elsewhere, or an outbound filter rule | Provider tools: effective routes and NSG rules (Azure), Reachability Analyzer (AWS), Connectivity Tests (GCP) | Fix the platform route, NAT, or rule; the guest OS is usually not the problem |
| dig returns NXDOMAIN but a record is expected | Wrong resolver/view, authoritative data, or negative cache | Compare explicit resolver and authoritative answers | Repair the demonstrated cause; dig does not consult the hosts file |
| A ticket or vendor cites an RFC that "proves" a behaviour | The RFC may be obsoleted or updated | `rfcstatus <number>` | Re-check the claim against the current RFC and its updates |

---

## Summary

- Packet switching shares links efficiently through statistical multiplexing; its costs are queues, loss, and no delivery guarantee at the IP layer, which is why TCP exists.
- ARPANET was one network. The internet came from gateways connecting different networks, with reliability pushed to the endpoints (the end-to-end principle).
- The original priorities put survivability first and accountability last: IP has no source authentication, and stateful middleboxes — NAT, firewalls, load balancers — were bolted on later and are where many connections break.
- RFC 894 documents Ethernet's conventional 1500-byte IP MTU. Tunnel overhead and
  failed feedback can create PMTUD black holes; the MTU value alone is not a fault.
- RFCs are updated and obsoleted, never edited. Check a document's status and successors before citing it.
- TCP has changed a lot since 1981 — congestion control (1988), options, security fixes — even though its wire format and handshake have not.
- The OSI model provides the vocabulary; TCP/IP is what runs.
- The cloud virtualises the network but not the protocols: it moves boundaries, and it hides the provider's layers from your tools.

## What's Next

Module 2 walks through the full lifecycle of a web request end to end — DNS, TCP, TLS, HTTP, and teardown — using the tools you installed in Module 0 and `phases`. Every step maps directly to a later module; this is the map before the terrain.

---

## Quick Reference Card

```
MODULE 1: HISTORY OF NETWORKING — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

KEY CONCEPTS
  Circuit switching      Reserved path, guaranteed capacity, idle when silent
  Packet switching       Shared links, queues, loss, no delivery guarantee
  Statistical mux        Many bursty senders share one link
  IMP                    ARPANET's packet switch (1969) — one network only
  Gateway / router       Forwards between networks (Cerf–Kahn, 1974)
  End-to-end principle   Simple core; reliability and security at the hosts
  RFC                    Immutable document; updated or obsoleted, never edited
  CIDR                   Classless addressing (1993)
  NAT                    Many hosts share one IP; keeps state (1994)

ORIGIN OF CONSTRAINTS YOU WILL HIT IN PRODUCTION
  1500 byte IP MTU       Ethernet payload limit (DIX 1980; RFC 894, 1984)
  Ports 0–65535          16-bit field; the ephemeral range is a configured subset
  IPv4 exhaustion        32-bit addresses (RFC 791, 1981); IANA pool empty 2011
  No delivery guarantee  IP is best effort by design — TCP adds reliability
  Spoofable sources      Accountability was the lowest design priority
  TCP backs off on loss  Congestion control, added 1988 after the 1986 collapse

KEY RFCs (check with rfcstatus before citing)
  RFC 791   IPv4              RFC 8446  TLS 1.3
  RFC 8200  IPv6              RFC 9000  QUIC
  RFC 9293  TCP               RFC 9113  HTTP/2
  RFC 768   UDP               RFC 9114  HTTP/3
  RFC 1034  DNS               RFC 4271  BGP-4
  RFC 1122  Host requirements RFC 2119 + 8174  MUST / SHOULD

OSI LAYER NUMBERS (used as vocabulary throughout this course)
  Layer 1   Physical          Layer 5   Session
  Layer 2   Data Link         Layer 6   Presentation
  Layer 3   Network (IP)      Layer 7   Application
  Layer 4   Transport (TCP)

WHEN YOU SEE THIS            THINK THIS
  Silent packet drop in VPN    MTU — check the path MTU
  EADDRNOTAVAIL at scale       Source address and tuple/range capacity
  169.254.x.x on interface     IPv4 link-local; compare intended configuration
  Cloud VM default route OK    Check the platform route table, NAT, and rules
  but no internet
  "Layer 7 load balancer"      Routes by HTTP content, not just IP+port
  "Layer 4 issue"              TCP or UDP — transport layer
  "RFC NNNN says…"             rfcstatus NNNN — is it still current?

VERIFY WITH
  ip route show        Guest routing table (not the platform's)
  ip addr show         Interface addresses including link-local
  ss -s                Socket state counts including TIME_WAIT
  cat /etc/hosts       Local overrides that win over DNS for most programs
  rfcstatus N          RFC status, successors, and updates
```

---

## References

- Cerf, V., Kahn, R. — *A Protocol for Packet Network Intercommunication*, IEEE Transactions on Communications, 1974
- Saltzer, J., Reed, D., Clark, D. — *End-to-End Arguments in System Design*, ACM Transactions on Computer Systems, 1984
- Clark, D. — *The Design Philosophy of the DARPA Internet Protocols*, ACM SIGCOMM, 1988
- Jacobson, V. — *Congestion Avoidance and Control*, ACM SIGCOMM, 1988
- RFC Editor — RFC index, statuses, and streams — <https://www.rfc-editor.org/>
- RFC 6410 — Reducing the Standards Track to Two Maturity Levels
- RFC 2119 and RFC 8174 (BCP 14) — Key words for use in RFCs
- RFC 894 — Transmission of IP Datagrams over Ethernet Networks
- RFC 1122 — Requirements for Internet Hosts — Communication Layers
- RFC 8996 — Deprecating TLS 1.0 and TLS 1.1
- RFC 9293 — Transmission Control Protocol (TCP)
- `man ip`, `man ping` (the `-M do` flag for MTU probing), `man dig`
