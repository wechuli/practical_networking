# Module 4: The OSI Model

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 3 — Networking Fundamentals; the Go toolbox with `netlab` and `labserver` built
> **Estimated time:** 3 hours

## Overview

Every time someone says "it's a Layer 7 issue" or "check Layer 3 first," they are referencing the OSI model — a seven-layer framework that divides the problem space of networking into independent, well-defined responsibilities. It is not an implementation spec. No real network stack implements OSI exactly as drawn. But it is the shared mental model that lets you communicate precisely about where a problem lives, what is responsible for it, and what to look at next.

OSI supplies a vocabulary for hypotheses, not a proof that a whole layer works.
Successful ping tests one protocol, size, address, and path at that time; failed
ping can simply mean ICMP is filtered. Start with the failing application flow and
choose inexpensive discriminating tests at relevant layers. There is no fixed
percentage of debugging eliminated by the model.

This module teaches you the model as a working tool, not a memorization exercise. Each layer is presented with the protocols that live there, the header fields you will see in a packet capture, the tools that test that layer, and the failure signatures that tell you something is wrong at that specific layer and not somewhere else.

## What You Will Build Toward

- Name all 7 OSI layers and their responsibilities without hesitation
- Map real protocols (Ethernet, IP, TCP, UDP, TLS, HTTP, DNS) to the correct OSI layer
- Read a Wireshark packet capture and identify which layers are present and what each header contains
- Explain encapsulation: how application data becomes a segment, then a packet, then a frame, then bits on a wire
- Use the OSI model to isolate a problem to a specific layer before looking at application logs
- Compare the OSI and TCP/IP models and explain when each is the better mental tool
- Produce a failure at each layer on purpose in a namespace lab, and recognise each one in a capture

---

## 4.1 Why the OSI Model Exists

### What This Is

Before the OSI model, networking was a mess of proprietary, vertically integrated systems. IBM's SNA could not talk to DEC's DECnet. Novell's IPX/SPX was incompatible with both. If you bought a network from one vendor, you were locked in. Every vendor implemented everything from the physical cable to the application protocol as a single, tightly coupled stack.

The International Organization for Standardization (ISO) created the OSI (Open Systems Interconnection) model in 1984 to solve this. The insight was simple: if you define clear boundaries between layers, any vendor can implement one layer without caring about the internals of the others. An Ethernet NIC does not need to know whether the packet it carries is TCP or UDP. A TCP implementation does not need to know whether the underlying link is Ethernet, Wi-Fi, or fiber. The OSI protocol suite itself lost to TCP/IP (Module 1), but TCP/IP is built on the same layering principle, and that independence is what lets the internet run over any link and carry any application.

### Why This Matters for You

The OSI model gives you two things:

**A common vocabulary.** When you file a ticket that says "Layer 4 connectivity works but the TLS handshake fails," every network engineer on the planet knows exactly what you mean — TCP connections are succeeding, but something above TCP is broken. Without this vocabulary, the same symptom gets described as "the server is down," "the certificate is wrong," or "the firewall is blocking it" — all of which may be completely wrong.

**A troubleshooting vocabulary.** A flow depends on its lower-layer path, but a
different probe does not validate that path completely. Bottom-up, top-down, and
divide-and-test approaches are all useful when their observations match the source,
destination, protocol, size, and time of the failure.

### Diagram

```
THE PROBLEM BEFORE OSI

  Vendor A Stack              Vendor B Stack
  ┌──────────────┐            ┌──────────────┐
  │  App Protocol │            │  App Protocol │  ← Different
  │  Transport    │            │  Transport    │  ← Incompatible
  │  Network      │            │  Network      │  ← Can't cross over
  │  Physical     │            │  Physical     │  ← Maybe compatible
  └──────────────┘            └──────────────┘
         ✗ Cannot communicate ✗

THE SOLUTION: LAYERED INDEPENDENCE

  Any Application
  ┌──────────────┐
  │  Layer 7     │  HTTP, gRPC, SMTP — swappable
  ├──────────────┤
  │  Layer 4     │  TCP or UDP — swappable
  ├──────────────┤
  │  Layer 3     │  IPv4 or IPv6 — swappable
  ├──────────────┤
  │  Layer 2     │  Ethernet, Wi-Fi, MPLS — swappable
  ├──────────────┤
  │  Layer 1     │  Copper, fiber, wireless — swappable
  └──────────────┘
  Each layer works independently. Change one without breaking others.
```

### Key Concept: Layers as Contracts

Each layer provides a service to the layer above it and consumes a service from the layer below it. The interface between layers is the contract. As long as the contract is met, the implementation can change freely. This is why your HTTPS application works identically over Ethernet, Wi-Fi, 5G, or a VPN tunnel — the upper layers never know or care what the lower layers are.

---

## 4.2 The 7 Layers

### What This Is

The OSI model defines seven layers, numbered 1 (bottom) through 7 (top). Each layer has a specific, non-overlapping responsibility. In practice, Layers 5, 6, and 7 are often collapsed into a single "Application" layer (as they are in the TCP/IP model), but understanding the full seven helps you think more precisely about where a problem lives.

### The Layers

```
 Layer   Name            What It Does                     PDU         Key Protocols
 ─────   ──────────────  ──────────────────────────────   ─────────   ────────────────────────
   7     Application     User-facing services & APIs      Data        HTTP, DNS, SMTP, gRPC,
                                                                      SSH, FTP, SNMP

   6     Presentation    Data format, encoding,           Data        TLS/SSL encryption,
                         encryption, compression                      JPEG, ASCII, UTF-8,
                                                                      gzip compression

   5     Session         Connection management,           Data        NetBIOS, RPC session
                         session state, checkpointing                 management, SOCKS

   4     Transport       End-to-end delivery,             Segment     TCP, UDP, QUIC
                         reliability, flow control        (TCP) /
                                                          Datagram
                                                          (UDP)

   3     Network         Logical addressing and           Packet      IP (v4/v6), ICMP,
                         routing between networks                     IPsec; routing protocols
                                                                      (OSPF; BGP rides on TCP)

   2     Data Link       Physical addressing,             Frame       Ethernet, Wi-Fi
                         local delivery, error                        (802.11), ARP, PPP,
                         detection                                    VLAN (802.1Q)

   1     Physical        Bits on the wire/air             Bits        Ethernet cables (Cat5/6),
                                                                      fiber optic, radio
                                                                      frequencies, connectors
```

> **PDU** = Protocol Data Unit. The name for data at each layer. Using the right term tells the listener which layer you are thinking about.

---

### Layer 1 — Physical

**What it does:** Converts bits (0s and 1s) into physical signals — electrical voltages on copper, light pulses on fiber, radio waves over wireless — and transmits them between directly connected devices.

**What you see:** Cables, NICs, connectors, hubs, repeaters, signal specifications. You do not see Layer 1 in a packet capture because it is below the capture point.

**What fails here:**

- A damaged cable — packet loss appears random because bits are corrupted
- A loose connector — intermittent connectivity, link flapping
- Duplex mismatch — one end at full-duplex, the other at half-duplex, causing late collisions and frame errors
- Speed mismatch — autonegotiation failure causing a 1 Gbps NIC to connect at 100 Mbps

**How to check:**

```bash
# Check physical link state (UP/LOWER_UP means physical link is active)
ip link show eth0

# Detailed physical layer info: speed, duplex, link detection
sudo ethtool eth0

# Look for errors at Layer 1
ip -s link show eth0    # Check for RX/TX errors, drops, overruns
```

**Expected output for `ethtool`:**

```
Speed: 1000Mb/s             ← negotiated link speed
Duplex: Full                ← full duplex — both directions simultaneously
Auto-negotiation: on
Link detected: yes          ← physical cable is connected and link is up
```

If `Link detected: no`, the problem is physical — no amount of IP configuration will help.

**Production context:** Physical link faults matter in data centers, but their
prevalence depends on the workload. In cloud VMs, carrier state is a virtual-device
observation, not direct evidence of a defective physical cable or hypervisor.

---

### Layer 2 — Data Link

**What it does:** Delivers frames between devices on the same local network (same broadcast domain). Uses MAC addresses for addressing. Detects (but generally does not correct) bit errors in frames using CRC/FCS. Manages access to the shared medium (historically important with hubs, less so with switches).

**What you see:** Ethernet frames with source and destination MAC addresses, VLAN tags (802.1Q), ARP resolutions.

**Key concepts:**

- **MAC address:** A 48-bit link-layer address, `aa:bb:cc:dd:ee:ff`. Physical NICs ship with a manufacturer-assigned address whose first 3 bytes (OUI) name the vendor, but virtual interfaces, containers, and privacy features use locally administered addresses that say nothing about the manufacturer (Module 3).
- **Switch:** A Layer 2 device that learns MAC addresses and forwards frames only to the correct port (as opposed to a hub which sends to all ports).
- **VLAN:** A logical partition of a switch. Devices in different VLANs cannot communicate without a router, even if they are on the same physical switch.
- **Broadcast domain:** All devices that receive a broadcast frame. A VLAN boundary is a broadcast domain boundary.

**What fails here:**

- MAC address not in the switch's table — the frame is flooded to every port in the VLAN (normal for a moment; a sign of trouble if constant, for example after table overflow). Broadcast *storms* come from loops, not flooding.
- VLAN mismatch — two devices on the same switch but different VLANs cannot communicate
- Spanning Tree Protocol (STP) reconvergence — temporary connectivity loss when the topology changes
- ARP failure — IP is correct but MAC resolution fails (covered in Module 6)
- MTU mismatch — one end sends jumbo frames, the other expects 1500 bytes

**How to check:**

```bash
# Show MAC address and interface state
ip link show

# Show the ARP/neighbor table — mapping IP to MAC
ip neigh show

# Capture Layer 2 traffic — ARP and Ethernet frames
sudo tcpdump -i eth0 -e -n arp
```

**Production context:** In Docker and Kubernetes, the container runtime creates virtual Ethernet devices (veth pairs) and a software bridge — Layer 2 constructs. But when two containers on the same bridge cannot communicate, the cause is often *not* Layer 2: with `br_netfilter`, bridged traffic passes through iptables, so Docker's inter-container isolation or a NetworkPolicy can drop it. Check the bridge membership *and* the filtering rules (Modules 16–18).

---

### Layer 3 — Network

**What it does:** Routes packets between networks using logical addresses (IP addresses). This is the layer that enables communication beyond the local network. Routers operate here, making hop-by-hop forwarding decisions based on the routing table and longest prefix match.

**What you see:** IP addresses (source and destination), TTL, protocol field (TCP=6, UDP=17, ICMP=1), fragmentation flags.

**Key concepts:**

- **IP address:** A logical address assigned to an interface. Unlike MAC addresses, IP addresses are hierarchical and routable.
- **Routing table:** The lookup table a router uses to decide which interface to send a packet out of, and what the next hop should be.
- **TTL (Time to Live):** Decremented at each hop. When it reaches 0, the packet is dropped and an ICMP Time Exceeded is sent back. Prevents routing loops.

**What fails here:**

- No route to destination — `RTNETLINK answers: Network is unreachable`
- Wrong default gateway — packets leave the host but never reach the destination
- Asymmetric routing — outbound path works, return path uses a different route that is blocked by a stateful firewall
- IP address conflict — two hosts with the same IP on the same network
- TTL exceeded — packet is looping or path is too long

**How to check:**

```bash
# View routing table
ip route show

# Test Layer 3 reachability
ping -c 5 <destination_ip>

# Trace the path packet-by-packet
traceroute <destination_ip>

# What route would be used for a specific destination?
ip route get <destination_ip>
```

**Production context:** Inspect effective cloud routes separately from OS routes.
A subnet without an Internet Gateway default route can still use local, peering,
VPN, or NAT routes. Missing a matching route can explain a particular failure,
not every outbound failure. Correlate the destination and return path with policy.

---

### Layer 4 — Transport

**What it does:** Provides end-to-end communication between processes on different hosts. Uses port numbers to multiplex multiple connections over a single IP address. TCP provides reliable, ordered delivery with flow and congestion control. UDP provides fast, connectionless delivery with no guarantees.

**What you see:** Source and destination ports, TCP flags (SYN, ACK, FIN, RST), sequence and acknowledgment numbers, window size.

**Key concepts:**

- **Port number:** A 16-bit number (0–65535) identifying a specific process. Well-known ports: 0–1023. Ephemeral ports: 32768–60999 (Linux default).
- **TCP:** Connection-oriented. Three-way handshake (SYN, SYN-ACK, ACK). Guarantees delivery and ordering through sequence numbers and acknowledgments.
- **UDP:** Connectionless. No handshake, no acknowledgment. Lower overhead, lower latency, no reliability guarantees.
- **Socket:** The combination of IP address + port number + protocol. A socket uniquely identifies one end of a connection.

**What fails here:**

- `Connection refused` — the SYN was answered with an RST, or with an ICMP port-unreachable. Nothing is listening, or a firewall `REJECT` rule or a proxy answered on the server's behalf.
- `Connection timed out` — SYN sent, no answer of any kind. A firewall is silently dropping, a route or return route is missing, or the host is down or overloaded.
- Port exhaustion — all ephemeral ports are in use (common under high connection rates). `EADDRNOTAVAIL`.
- TIME_WAIT accumulation — thousands of sockets stuck in TIME_WAIT after rapid connect/disconnect cycles.
- Window size of zero — receiver's buffer is full, sender is paused. Application is not reading from the socket fast enough.

**How to check:**

```bash
# List all TCP connections with state and process info
ss -tnp

# List all listening TCP sockets
ss -tlnp

# Count connections by state
ss -s

# Test if a specific TCP port is reachable
# (More precise than ping — tests Layer 4 specifically)
nc -zv <host> <port>

# Or with hping3 (sends a TCP SYN):
sudo hping3 -S -p 443 -c 3 <host>
```

**Production context:** The difference between `Connection refused` and `Connection timed out` is the single most important diagnostic signal at Layer 4. Refused means *something* answered — but not necessarily the server: this module's lab shows a firewall `REJECT` producing exactly the same curl error as a closed port. A capture shows which IP sent the RST. Timed out means nothing answered — a firewall dropped the SYN, a route is missing, or the host is unreachable or too overloaded to respond. These two symptoms require completely different investigation paths.

---

### Layer 5 — Session

**What it does:** Manages sessions — the establishment, maintenance, and teardown of connections between applications. Handles checkpointing and recovery for long-running data transfers. In practice, this layer is rarely discussed independently because its responsibilities are absorbed into the application and transport layers in the TCP/IP model.

**What lives here:** NetBIOS sessions, RPC session management, SOCKS proxies, TLS session resumption (session IDs and session tickets).

**Production relevance:** The concept of a "session" reappears constantly in production — TLS sessions, HTTP sessions (cookies), TCP session tracking in firewalls (conntrack), database connection pools. When a load balancer loses session affinity and users are bounced between backends losing their state, that is a Layer 5 problem in OSI terms, even though no one calls it that any more.

---

### Layer 6 — Presentation

**What it does:** Handles data representation, encoding, encryption, and compression. Ensures that data from the application layer of one system can be read by the application layer of another system, regardless of internal data representation.

**What lives here:** TLS/SSL encryption, character encoding (ASCII, UTF-8, EBCDIC), data serialization (JSON, XML, Protocol Buffers), compression (gzip, brotli), image formats (JPEG, PNG).

**Production relevance:** Character encoding mismatches (a service sending UTF-8 to a client expecting Latin-1) and serialization failures (a protobuf schema mismatch between client and server) are presentation layer problems. TLS is the most visible protocol at this layer — when an `openssl s_client` connection fails during the handshake, you are debugging a Layer 6 problem.

---

### Layer 7 — Application

**What it does:** The layer closest to the user. Provides the protocols that applications use to exchange data: web pages (HTTP), email (SMTP), file transfer (FTP), name resolution (DNS), remote access (SSH), and API communication (gRPC, REST).

**What you see:** HTTP methods and status codes, DNS queries and responses, SMTP commands, SSH version negotiation.

**What fails here:**

- HTTP 5xx errors — server-side application failure
- HTTP 4xx errors — client request problem (auth, bad path, invalid payload)
- DNS NXDOMAIN — domain does not exist
- DNS SERVFAIL — upstream resolver failure
- TLS certificate errors — expired, wrong hostname, missing intermediate
- Application protocol mismatch — for example a gRPC client (which needs HTTP/2) going through a proxy that only speaks HTTP/1.1, or a client using cleartext HTTP/2 "prior knowledge" against an HTTP/1.1-only server. With TLS, ALPN normally negotiates a version both sides support (Module 2)

**How to check:**

```bash
# Full HTTP request with all protocol details visible
curl -v https://example.com

# DNS query for a specific record type
dig A example.com

# TLS certificate inspection
openssl s_client -connect example.com:443 -servername example.com

# Watch HTTP requests at a specific port
sudo tcpdump -i any -A 'tcp port 80'
```

**Production context:** Most production incidents are ultimately Layer 7 — a misconfigured application, a bad deployment, a wrong endpoint. But a Layer 7 *error* can have a lower-layer cause one hop away: an HTTP 504 is a real HTTP response, from a gateway that could not reach its backend, perhaps because of a routing or firewall problem between them. When you see a 5xx, find out which device generated it, then apply the layers to *that* device's next hop.

---

### Full Stack Summary Diagram

```
 OSI LAYER STACK — WHAT YOU SEE AT EACH LAYER IN A PACKET CAPTURE

 ┌─────────────────────────────────────────────────────────────────────────┐
 │ Layer 7 — Application                                                  │
 │   HTTP: GET /api/v1/users HTTP/2                                       │
 │   DNS:  Query A record for api.example.com                             │
 │   Tool: curl -v, dig, browser DevTools                                 │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 6 — Presentation                                                 │
 │   TLS: ClientHello, ServerHello, Certificate, Encrypted payload        │
 │   Encoding: UTF-8, gzip compression                                    │
 │   Tool: openssl s_client, Wireshark TLS dissector                      │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 5 — Session                                                      │
 │   TLS Session ID / Ticket for resumption                               │
 │   (Rarely visible as a separate layer in captures)                     │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 4 — Transport                                                    │
 │   TCP: SrcPort=52431, DstPort=443, Seq=1001, Ack=5001, Flags=[ACK]    │
 │   Window Size=65535, Options=[MSS 1460, SACK, Timestamps]              │
 │   Tool: ss -tnp, netstat, tcpdump 'tcp port 443'                      │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 3 — Network                                                      │
 │   IPv4: Src=10.0.1.50, Dst=203.0.113.10, TTL=64, Proto=TCP(6)         │
 │   Tool: ping, traceroute, ip route, tcpdump 'host 10.0.1.50'          │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 2 — Data Link                                                    │
 │   Ethernet: Src MAC=aa:bb:cc:dd:ee:ff, Dst MAC=11:22:33:44:55:66      │
 │   Type=0x0800 (IPv4), FCS for error checking                          │
 │   Tool: ip link, ip neigh, tcpdump -e, arp -n                         │
 ├─────────────────────────────────────────────────────────────────────────┤
 │ Layer 1 — Physical                                                     │
 │   1000BASE-T: electrical signals on Cat6 copper at 1 Gbps             │
 │   Not visible in packet captures — below the capture point             │
 │   Tool: ethtool, cable tester, looking at the link LED                 │
 └─────────────────────────────────────────────────────────────────────────┘
```

---

## 4.3 Encapsulation and Decapsulation

### What This Is

Encapsulation is the process by which each layer wraps the data from the layer above it with its own header (and sometimes a trailer), forming a new protocol data unit. When you type a URL into a browser, the HTTP request (Layer 7 data) does not just fly across the wire. It gets wrapped — first by TCP (adding ports and sequence numbers), then by IP (adding source and destination addresses), then by Ethernet (adding MAC addresses and a frame check sequence), and finally converted to electrical signals or light pulses.

Decapsulation is the reverse. When a frame arrives at the destination, each layer strips its header, processes the relevant information, and passes the payload upward to the next layer.

### How It Works

Think of encapsulation like putting a letter in an envelope, putting that envelope in another envelope, and so on. Each envelope has an address relevant to the delivery person at that stage: the mail carrier reads the outer address (Layer 2 — local delivery), the postal service reads the city/state (Layer 3 — routing), and the recipient reads the letter (Layer 7 — application).

### Diagram

```
ENCAPSULATION — SENDING SIDE (top-down)

  Application Layer (Layer 7)
  ┌───────────────────────────────────┐
  │  HTTP GET /index.html             │  ← Application data
  └───────────────────┬───────────────┘
                      │ Pass down
                      ▼
  Transport Layer (Layer 4)
  ┌──────────┬────────────────────────────────────┐
  │ TCP Hdr  │  HTTP GET /index.html               │  ← Segment
  │ SrcPort  │                                     │
  │ DstPort  │                                     │
  │ Seq/Ack  │                                     │
  └──────────┴──────────────────┬─────────────────-┘
                                │ Pass down
                                ▼
  Network Layer (Layer 3)
  ┌──────────┬──────────┬──────────────────────────────────────┐
  │  IP Hdr  │ TCP Hdr  │  HTTP GET /index.html                 │  ← Packet
  │  SrcIP   │          │                                       │
  │  DstIP   │          │                                       │
  │  TTL     │          │                                       │
  └──────────┴──────────┴────────────────┬─────────────────────┘
                                         │ Pass down
                                         ▼
  Data Link Layer (Layer 2)
  ┌──────────┬──────────┬──────────┬─────────────────────────────────┬─────┐
  │  Eth Hdr │  IP Hdr  │ TCP Hdr  │  HTTP GET /index.html           │ FCS │ ← Frame
  │  SrcMAC  │          │          │                                  │     │
  │  DstMAC  │          │          │                                  │     │
  │  Type    │          │          │                                  │     │
  └──────────┴──────────┴──────────┴─────────────────────────────────┴─────┘
                                         │ Pass down
                                         ▼
  Physical Layer (Layer 1)
  ░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░  ← Bits
  Electrical signals / light pulses / radio waves on the medium


DECAPSULATION — RECEIVING SIDE (bottom-up)

  The exact reverse: each layer strips its header, processes it,
  and hands the payload to the layer above.

  Bits → Frame → Packet → Segment → Application Data
```

### Header Sizes and Overhead

```
HEADER SIZES, MEASURED (this module's lab: Linux client and server, MTU 1500)

  Captured frame                     Length   Ethernet + IP + TCP     Payload
  ─────────────────────────────────  ──────   ─────────────────────   ───────
  SYN                                  74     14 + 20 + 40 (options)      0
  ACK, data segments                   66+    14 + 20 + 32 (timestamps)  0+
  GET / HTTP/1.1                      144     14 + 20 + 32               78
  RST from a closed port               54     14 + 20 + 20                0

  Minimum overhead:   54 bytes (Ethernet 14 + IPv4 20 + TCP 20), no options
  MSS advertised:   1460 = 1500 MTU − 20 IP − 20 TCP (the SYN shows "mss 1460")
  Payload per full segment on Linux: 1448, because every segment after the SYN
  carries a 12-byte timestamp option (Module 3)

  Extra layers:
  TLS 1.3 AES-GCM record:  +22 bytes (5 header + 1 inner type + 16 tag)
  802.1Q VLAN tag:          +4 bytes
  GRE over IPv4:           +24 bytes (outer IP 20 + GRE 4)
  VXLAN over IPv4:         +50 bytes (outer IP 20 + UDP 8 + VXLAN 8 + inner Ethernet 14)
  WireGuard:               +60 bytes over IPv4, +80 over IPv6
  IPsec ESP:               variable, roughly 50–80 bytes depending on mode and cipher
```

### What Wireshark Shows You

When you capture a packet in Wireshark, you see encapsulation directly. In the packet detail pane, Wireshark displays each layer's header as an expandable tree:

```
▶ Frame 1: 215 bytes on wire                  <- Capture metadata, not Layer 1 signals
▶ Ethernet II, Src: aa:bb:cc:dd:ee:ff ...     ← Layer 2 (Data Link)
▶ Internet Protocol Version 4, Src: 10.0.1.50 ← Layer 3 (Network)
▶ Transmission Control Protocol, Src Port: ... ← Layer 4 (Transport)
▶ Hypertext Transfer Protocol                  ← Layer 7 (Application)
```

Each arrow you expand shows the fields in that layer's header. The hex dump at the bottom shows the raw bytes, and you can correlate each highlighted byte to a specific field in a specific layer.

### Production Relevance

Understanding encapsulation is critical for MTU and tunneling problems. Every encapsulation layer adds header bytes, reducing the space available for payload. A packet that fits in 1500 bytes on a normal Ethernet link does not fit when you add VXLAN (50 bytes of overhead) or IPsec (variable, often 50–70 bytes). If the path does not handle this, you get silent drops. The packet is too large, the Don't Fragment bit is set, and if ICMP is blocked, the sender never learns the packet was dropped — a classic PMTUD black hole.

---

## 4.4 OSI vs TCP/IP Model

### What This Is

The TCP/IP model (also called the Internet model or DoD model) is the model that the actual internet is built on. It has four layers instead of seven, and it maps to reality more directly than OSI. In practice, engineers use both models depending on context: OSI when they need precision about where in the stack a problem lies, and TCP/IP when they are describing how real protocols interact.

### The Mapping

```
  OSI Model (7 Layers)              TCP/IP Model (4 Layers)
  ════════════════════              ═══════════════════════

  ┌──────────────────┐
  │ 7  Application   │
  ├──────────────────┤              ┌──────────────────┐
  │ 6  Presentation  │  ──────────▶ │   Application    │
  ├──────────────────┤              │                  │
  │ 5  Session       │              │  HTTP, DNS, TLS, │
  └──────────────────┘              │  SSH, SMTP, gRPC │
                                    └──────────────────┘

  ┌──────────────────┐              ┌──────────────────┐
  │ 4  Transport     │  ──────────▶ │   Transport      │
  │                  │              │  TCP, UDP, QUIC   │
  └──────────────────┘              └──────────────────┘

  ┌──────────────────┐              ┌──────────────────┐
  │ 3  Network       │  ──────────▶ │   Internet       │
  │                  │              │  IP, ICMP, IPsec  │
  └──────────────────┘              └──────────────────┘

  ┌──────────────────┐
  │ 2  Data Link     │              ┌──────────────────┐
  ├──────────────────┤  ──────────▶ │   Link           │
  │ 1  Physical      │              │  Ethernet, Wi-Fi,│
  └──────────────────┘              │  ARP, PPP        │
                                    └──────────────────┘
```

### When to Use Which

**Use OSI when:**

- Troubleshooting — the seven layers give you finer-grained isolation. "Is this a Layer 2 problem (ARP, VLAN) or a Layer 3 problem (routing, IP)?" The TCP/IP model lumps both into "Link" and "Internet," which is less precise.
- Communicating with network engineers — the industry uses OSI layer numbers universally. "L4 load balancer," "L7 firewall," "L2 switch" all reference OSI.
- Discussing vendor documentation — switch and firewall vendors describe their products using OSI layers.

**Use TCP/IP when:**

- Describing protocol interactions — TCP/IP maps directly to real protocol implementations. There is no meaningful "Layer 5" in actual TCP/IP traffic.
- Writing code — the socket API maps to the TCP/IP model: you work with the transport layer (TCP/UDP) and the application layer (HTTP). Sessions and presentation are absorbed into the application.
- Discussing the Linux kernel networking stack — it is implemented as the TCP/IP model: link layer (device drivers, Ethernet), internet layer (IP stack), transport layer (TCP/UDP), application layer (socket interface to userspace).

### Where TLS Fits (The Classic Debate)

TLS is famously hard to place in the OSI model:

```
  Where does TLS live?

  OSI says:        Layer 6 (Presentation) — it handles encryption and encoding
  But also:        Layer 5 (Session) — TLS sessions have state, resumption, IDs
  In TCP/IP:       Application layer — TLS sits between the socket API and HTTP
  In practice:     Between Layer 4 (TCP) and Layer 7 (HTTP)

  ┌──────────────────┐
  │  HTTP (Layer 7)  │
  ├──────────────────┤
  │  TLS (Layer 6/5) │  ← Operates on top of TCP, below HTTP
  ├──────────────────┤
  │  TCP  (Layer 4)  │
  ├──────────────────┤
  │  IP   (Layer 3)  │
  └──────────────────┘

  TLS is often discussed alongside session/presentation functions;
  "Layer 6.5" is not a standard. BGP controls routing but is carried over TCP.
```

The honest answer: TLS does not fit cleanly into OSI. It spans Session and Presentation in function. In the TCP/IP model, it is simply part of the Application layer. Do not spend time trying to force a clean mapping — understand what TLS does and where it sits in the packet, and you will never be confused.

---

## 4.5 Troubleshooting by Layer

### What This Is

Use this checklist to organize evidence, not as mandatory ping-gated control flow.
An application error can be the cheapest useful starting point. Confirm that each
test matches the original flow; a different source or ICMP probe may see different
routes and policy. Capture metadata is not physical Layer 1 signaling, and not
every OSI abstraction appears as a distinct header.

### The Layer-by-Layer Checklist

```
TROUBLESHOOTING CHECKLIST — CHOOSE TESTS FOR THE FAILING FLOW

  ┌──────────────────────────────────────────────────────────────────────┐
  │  Layer 1 — Physical                                                 │
  │  ✓ Is the link up?            ip link show eth0                     │
  │  ✓ Is LOWER_UP present?       (no = physical link down)            │
  │  ✓ Speed/duplex correct?      ethtool eth0                         │
  │  ✓ Any interface errors?      ip -s link show eth0                 │
  │  If Layer 1 fails → stop. Fix the physical before proceeding.       │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Layer 2 — Data Link                                                │
  │  ✓ Is ARP resolving?          ip neigh show                        │
  │  ✓ Can you reach the gateway? arping -I eth0 <gateway>             │
  │  ✓ Correct VLAN?              ip -d link show (tagged sub-ifaces);  │
  │                               access VLANs live on the switch port  │
  │  If Layer 2 fails → check switch port, VLAN config, cabling.       │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Layer 3 — Network                                                  │
  │  ✓ Do you have an IP address? ip addr show                         │
  │  ✓ Is the route present?      ip route show                        │
  │  ✓ Can you ping the gateway?  ping -c 3 <gateway>                  │
  │  ✓ Can you ping the target?   ping -c 3 <target_ip>               │
  │  ✓ What does the path look    traceroute <target_ip>               │
  │    like?                                                            │
  │  If Layer 3 fails → check routes, NACLs, security groups, NAT.     │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Layer 4 — Transport                                                │
  │  ✓ Is the port open?          ss -tlnp | grep <port>               │
  │  ✓ Can you TCP connect?       nc -zv <host> <port>                 │
  │  ✓ Connection refused or      (refused = an RST came back: closed   │
  │    timed out?                  port, REJECT rule, or proxy)          │
  │                               (timeout = nothing came back)         │
  │  ✓ Any socket state issues?   ss -s (check TIME_WAIT, CLOSE_WAIT)  │
  │  If Layer 4 fails → check firewall rules, process status, ports.   │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Layer 5/6 — Session / Presentation                                │
  │  ✓ Does TLS handshake         openssl s_client -connect host:443   │
  │    complete?                                                        │
  │  ✓ Certificate valid?         Check dates, SAN, chain              │
  │  ✓ Cipher suite negotiated?   Look for "Cipher" in output          │
  │  If TLS fails → check certs, expiry, SNI, cipher compatibility.    │
  ├──────────────────────────────────────────────────────────────────────┤
  │  Layer 7 — Application                                              │
  │  ✓ Does the app respond?      curl -v https://host/endpoint        │
  │  ✓ HTTP status code?          200 OK, 502, 504, etc.               │
  │  ✓ Response body correct?     Check for error messages in body      │
  │  ✓ DNS resolving correctly?   dig A hostname                        │
  │  If Layer 7 fails → now it's time to look at application logs.     │
  └──────────────────────────────────────────────────────────────────────┘
```

### Failure Isolation Examples

```
EXAMPLE 1: "The website is down"

  Layer 1: ip link show → eth0 is UP, LOWER_UP         ✓
  Layer 2: ip neigh → gateway MAC resolved              ✓
  Layer 3: ping 203.0.113.10 → replies received         ✓
  Layer 4: nc -zv 203.0.113.10 443 → Connection refused  ✗

  Hypothesis: a closed port or active rejection returned a reset.
  Check listener state, destination identity, and filtering evidence
  before restarting anything. The source address alone does not identify
  which device generated a reset.


EXAMPLE 2: "API calls are timing out"

  Layer 1: ip link show → eth0 is UP, LOWER_UP         ✓
  Layer 2: ip neigh → gateway MAC resolved              ✓
  Layer 3: ping 10.0.2.50 → 100% packet loss           ✗

  Observation: no ICMP replies. Test the application's TCP port and
  inspect routes/policy; this does not yet prove an application outage.


EXAMPLE 3: "HTTPS returns 'certificate error'"

  Layer 1: ✓  Layer 2: ✓  Layer 3: ✓
  Layer 4: nc -zv host 443 → succeeded                  ✓
  Layer 5/6: openssl s_client → "verify error:
    unable to get local issuer certificate"              ✗

  Hypothesis: chain building failed. Compare the served certificates,
  intended hostname, and client trust store. A missing intermediate and
  an untrusted private root can produce similar errors.
```

### Common Failure Signatures by Layer

| Layer | Symptom                           | Likely Cause                                        | Tool                                 |
| ----- | --------------------------------- | --------------------------------------------------- | ------------------------------------ |
| 1     | Link flapping (UP/DOWN)           | Bad cable, bad SFP, speed mismatch                  | `ethtool`, `dmesg`, `ip -s link`     |
| 2     | ARP FAILED for gateway            | VLAN mismatch, cable in wrong port                  | `ip neigh`, `arping`, `tcpdump arp`  |
| 2     | High error count on interface     | CRC errors from bad cable, duplex mismatch          | `ip -s link show`                    |
| 3     | `Network is unreachable`          | No route to destination                             | `ip route show`, `ip route get`      |
| 3     | `Destination Host Unreachable`    | From *your own* IP: ARP for an on-link address got no reply. From a router's IP: that router can't deliver | `ip neigh`, `tcpdump arp`, `traceroute` |
| 3     | Ping works, some traffic does not | Asymmetric routing + stateful firewall              | `traceroute` both directions         |
| 4     | `Connection refused`              | Nothing listening, or a REJECT rule or proxy answered | capture: who sent the RST?; `ss -tlnp` on the server |
| 4     | `Connection timed out`            | SYN dropped by firewall, host unreachable           | `tcpdump 'tcp[tcpflags] == tcp-syn'` |
| 4     | Thousands of TIME_WAIT            | Rapid open/close cycles (e.g., bad connection pool) | `ss -s`                              |
| 5/6   | TLS handshake failure             | Cert expired, SNI mismatch, cipher mismatch         | `openssl s_client`                   |
| 7     | HTTP 502 Bad Gateway              | Reverse proxy can't reach backend                   | `curl -v`, check proxy logs          |
| 7     | HTTP 504 Gateway Timeout          | Backend exists but takes too long to respond        | `curl -v`, check backend health      |
| 7     | DNS NXDOMAIN                      | Domain doesn't exist or resolver is wrong           | `dig`, `dig +trace`                  |

---

## Hands-On Lab: Every Layer, Observed and Broken

### Lab Goals

- Watch one request pass through Layers 2–7 in a capture with real Ethernet headers
- Measure the header overhead at each layer
- Fail Layers 2, 4, and 5/6 on purpose and recognise each failure in the error *and* in the packets
- Apply the layer checklist to your own machine

### Prerequisites

- Linux VM (Ubuntu 24.04) with root for Part 1
- `tcpdump`, `curl`, `openssl`, `iptables`, `iproute2`, `iputils-ping`
- The Go toolbox built: `cd labs/go && go build -o bin/ ./cmd/...`
- Wireshark on the VM or your workstation for Part 3

---

### Part 1: The OSI Lab in `netlab`

The [osi lab](../labs/netlab/osi.json) builds two namespaces joined by a veth pair:
`client` (198.18.4.1) and `server` (198.18.4.2). The server runs `labserver`, the
course's local web server, on plain HTTP (8080) and HTTPS (8443). Its firewall
drops TCP 8081 and rejects TCP 8082 with a reset. Because the capture runs on a
real Ethernet interface (not Linux's `any` pseudo-interface), every frame shows its
MAC addresses and EtherType.

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/osi.json
```

It runs ten checks and keeps every capture (`.pcap` plus a decoded `.txt`) in the
evidence directory it prints. All output below is *captured* from this lab.

**Layers 2–7 in one plain-HTTP request** (`http.pcap`; timestamps and TCP options
trimmed):

```
d6:ea:be:e8:50:7b > 92:1d:77:2f:4e:b3, ethertype IPv4 (0x0800), length 74:
    198.18.4.1.40392 > 198.18.4.2.8080: Flags [S], seq 2856002906, win 64240 [mss 1460,...], length 0
92:1d:77:2f:4e:b3 > d6:ea:be:e8:50:7b, ethertype IPv4 (0x0800), length 74:
    198.18.4.2.8080 > 198.18.4.1.40392: Flags [S.], seq 2355583608, ack 2856002907, ..., length 0
d6:ea:be:e8:50:7b > 92:1d:77:2f:4e:b3, ethertype IPv4 (0x0800), length 66:
    198.18.4.1.40392 > 198.18.4.2.8080: Flags [.], ack 1, ..., length 0
d6:ea:be:e8:50:7b > 92:1d:77:2f:4e:b3, ethertype IPv4 (0x0800), length 144:
    198.18.4.1.40392 > 198.18.4.2.8080: Flags [P.], seq 1:79, ..., length 78: HTTP: GET / HTTP/1.1
92:1d:77:2f:4e:b3 > d6:ea:be:e8:50:7b, ethertype IPv4 (0x0800), length 507:
    198.18.4.2.8080 > 198.18.4.1.40392: Flags [P.], seq 1:442, ..., length 441: HTTP: HTTP/1.1 200 OK
... ACK, then FIN from each side, then a final ACK
```

Label every part of one line with its layer:

| Part of the line | Layer | Header field |
| --- | --- | --- |
| `d6:ea:… > 92:1d:…` | 2 | Source and destination MAC |
| `ethertype IPv4 (0x0800)` | 2 | EtherType: what the frame carries |
| `length 74` | — | Captured frame length (Ethernet header onward; no FCS, no preamble) |
| `198.18.4.1.40392 > 198.18.4.2.8080` | 3 and 4 | IP addresses, then ports |
| `Flags [S]`, `seq`, `win`, `mss 1460` | 4 | TCP flags, sequence number, window, options |
| `GET / HTTP/1.1` | 7 | Application data, readable because this is plain HTTP |

**Verify:**

- [ ] The SYN and SYN-ACK are 74 bytes and the later ACKs 66. Account for every byte using the table in 4.3.
- [ ] The SYN advertises `mss 1460`. Why does the 144-byte GET carry only 78 bytes of payload, and why will a full-size segment on this link carry 1,448, not 1,460?
- [ ] Every frame from the client has the same source MAC. Which device's MAC is the destination? (There is no router on this link, so it is the server's.)

**Layer 2 — the ARP entry the request needed.** Check 2 shows the client's
neighbour table: `198.18.4.2 lladdr <server MAC> REACHABLE`. The first frame of
the HTTP request could only be addressed once that existed.

**Layers 5–7 — the same server, HTTPS, two ways.** Check 4 trusts the lab CA and
gets `HTTP 200 over HTTPS`. Check 5 runs `openssl s_client` without the CA:

```
CONNECTED(00000003)                                         ← Layer 4 worked
depth=1 O = practical networking course, CN = practical networking labserver CA
verify error:num=19:self-signed certificate in certificate chain
...
New, TLSv1.3, Cipher is TLS_AES_128_GCM_SHA256
Verify return code: 19 (self-signed certificate in certificate chain)
```

TCP connected and the TLS handshake ran — ciphers were even negotiated — but the
client refused the certificate chain. That is a Layer 5/6 failure with Layers 1–4
fully healthy.

**Layer 4 — refused, twice, by different devices.** Check 6 connects to port 9, where
nothing listens:

```
198.18.4.1.57158 > 198.18.4.2.9: Flags [S], ..., length 0
198.18.4.2.9 > 198.18.4.1.57158: Flags [R.], seq 0, ack 1896253167, win 0, length 0
```

Check 7 connects to port 8082, where a `REJECT --reject-with tcp-reset` rule answers
instead. curl prints the identical error for both:

```
curl: (7) Failed to connect to 198.18.4.2 port 9 after 0 ms: Couldn't connect to server
curl: (7) Failed to connect to 198.18.4.2 port 8082 after 0 ms: Couldn't connect to server
```

**Layer 3/4 — dropped.** Check 8 connects to port 8081, where the firewall drops:

```
curl: (28) Connection timed out after 3003 milliseconds

198.18.4.1.36066 > 198.18.4.2.8081: Flags [S], seq 2117626499, ...   t = 0
198.18.4.1.36066 > 198.18.4.2.8081: Flags [S], seq 2117626499, ...   t ≈ 1.0 s
198.18.4.1.36066 > 198.18.4.2.8081: Flags [S], seq 2117626499, ...   t ≈ 2.0 s
```

The same SYN — same sequence number — retransmitted, and nothing back. The check
asserts that no RST and no SYN-ACK appear.

**Layer 2 failure behind a Layer 3 message.** Check 9 pings 198.18.4.99, an address in
the client's own subnet that no host owns:

```
From 198.18.4.1 icmp_seq=1 Destination Host Unreachable      ← from the client itself

d6:ea:… > ff:ff:ff:ff:ff:ff, ethertype ARP (0x0806), length 42: Request who-has 198.18.4.99 tell 198.18.4.1
d6:ea:… > ff:ff:ff:ff:ff:ff, ethertype ARP (0x0806), length 42: Request who-has 198.18.4.99 tell 198.18.4.1
d6:ea:… > ff:ff:ff:ff:ff:ff, ethertype ARP (0x0806), length 42: Request who-has 198.18.4.99 tell 198.18.4.1
```

No ICMP left the client at all. The "Destination Host Unreachable" was generated
locally after three unanswered ARP broadcasts: a Layer 2 failure reported as a
Layer 3 one. When this message names *your own* address, look at ARP, not routing.

**Verify by experiment:** run `sudo ./bin/netlab up ../netlab/osi.json`, then
`sudo ./bin/netlab exec ../netlab/osi.json server -- iptables -L INPUT -v -n` before and
after a curl to port 8081 and 8082 from the client
(`sudo ./bin/netlab exec ../netlab/osi.json client -- curl -sS --noproxy '*' --max-time 3 http://198.18.4.2:8081/`).
The packet counters on the DROP and REJECT rules prove *which* rule answered — the
evidence curl's error message cannot give you. *Captured* after one curl to each port:

```
 pkts bytes target     prot opt in     out     source               destination
    3   180 DROP       6    --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:8081
    1    60 REJECT     6    --  *      *       0.0.0.0/0            0.0.0.0/0            tcp dpt:8082 reject-with tcp-reset
```

Three SYNs dropped (the original and two retransmissions in 3 s), one rejected.
Finish with `sudo ./bin/netlab down ../netlab/osi.json`.

> **A capture pitfall found while building this lab.** The first version captured
> nothing for the fast requests (the refused connection finished in under a
> millisecond), yet worked for the 3-second timeout. tcpdump on Linux receives
> packets from a kernel ring buffer in blocks, and a capture stopped right after a
> short burst can exit before the block is handed over. `tcpdump --immediate-mode`
> delivers each packet as it arrives. Use it whenever you script a capture around a
> short-lived command.

---

### Part 2: The Checklist on Your Own Machine

Run the layer checklist from 4.5 against your real network. Expected *shapes* are
shown; your names and addresses differ, and some networks block ICMP or outbound
DNS — record that as a finding, not a failure.

```bash
# Layer 1: link state and errors
ip link show; ip -s link show <iface>; sudo ethtool <iface> | grep -E 'Speed|Duplex|Link detected'

# Layer 2: the gateway's MAC is resolved
GW=$(ip route show default | awk '{print $3; exit}'); echo "gateway $GW"
ip neigh show "$GW"                   # expect: lladdr <mac> REACHABLE or STALE

# Layer 3: route, gateway, a distant address
ip route get 1.1.1.1
ping -c 3 "$GW"; ping -c 3 1.1.1.1

# Layer 4: a TCP handshake to a known service
nc -zv 1.1.1.1 53                     # "succeeded" means SYN, SYN-ACK, ACK completed

# Layer 5/6: TLS handshake and certificate verification
openssl s_client -connect example.com:443 -servername example.com </dev/null 2>&1 \
  | grep -E 'Verify return code|^New, |Cipher is'

# Layer 7: an HTTP status from the real application
curl -sS -o /dev/null -w '%{http_code}\n' https://example.com/
```

**Verify:** `Verify return code: 0 (ok)` at Layer 5/6, and a 2xx or 3xx status at
Layer 7. If a step fails, the steps above it usually fail too; record the lowest
failing step and the exact message, then stop and investigate there.

---

### Part 3: Encapsulation in Wireshark

Open `http.pcap` from Part 1's evidence directory in Wireshark (copy it off the VM
with `scp` if needed). Select the GET packet:

```
▶ Frame 4: 144 bytes on wire                       ← capture metadata, not Layer 1
▶ Ethernet II, Src: d6:ea:be:e8:50:7b, Dst: ...    ← Layer 2, 14 bytes
▶ Internet Protocol Version 4, Src: 198.18.4.1     ← Layer 3, 20 bytes
▶ Transmission Control Protocol, Src Port: 40392   ← Layer 4, 32 bytes
▶ Hypertext Transfer Protocol                      ← Layer 7, 78 bytes
    GET / HTTP/1.1\r\n
```

Click each header in turn and watch the highlighted bytes in the hex pane move
inward: Ethernet's 14 bytes first, then IP's 20, then TCP's 32, then the HTTP text.
That is encapsulation, byte for byte. Then open `refused.pcap` and find the RST's
54 bytes: Ethernet 14, IP 20, TCP 20 — an RST carries no options.

---

## Real-World Troubleshooting Scenario

### Scenario: "The Microservice Can't Reach the Database — It Must Be a Network Issue"

**The situation:**

A development team deploys a new version of a microservice to a Kubernetes cluster. The service immediately starts logging `connection timed out` errors when trying to connect to a PostgreSQL database running on an RDS instance at `db.internal.example.com:5432`. The team files a ticket: "Network team — our database is unreachable. Please fix your firewall."

**How layer-by-layer troubleshooting solves this in minutes:**

**Step 1 — Layer 3: Can we reach the database IP?**

```bash
# Exec into the failing pod
kubectl exec -it <pod-name> -- /bin/sh

# Resolve the database hostname
dig A db.internal.example.com
# Result: 10.0.5.20

# Inspect the pod's route; this does not expose subsequent cloud route tables
ip route get 10.0.5.20
```

RDS does not accept ICMP, so ping is not a valid RDS availability check. The route
lookup only establishes the pod's chosen next hop, not end-to-end delivery.

**Step 2 — Layer 4: Can we connect to port 5432?**

```bash
nc -zv 10.0.5.20 5432
# Result: Connection timed out
```

The TCP connect timed out. Candidate causes include dropped requests or replies,
wrong routing/address, filtering, or an unresponsive endpoint. Capture the failing
flow and compare source identity, effective routes, NACLs, and SG rules.

**Step 3 — Narrow the cause:**

In this synthetic case, the confirmed post-NAT source is from the new pod subnet.
Inspect the database SG and route/NACL evidence; do not infer the policy from ping:

```bash
# From your workstation with cloud CLI access
aws ec2 describe-security-groups --group-ids sg-0abc12345 \
  --query 'SecurityGroups[0].IpPermissions'
```

**Root cause discovered:** The RDS security group allows inbound on port 5432 from CIDR `10.0.3.0/24` (the old pod subnet). The new deployment moved pods to subnet `10.0.7.0/24`, which is not in the security group's allow list. The new pods' source IPs are not permitted.

**The fix:** Add `10.0.7.0/24` to the RDS security group's inbound rules for port 5432.

**Verification:** Retest a bounded connection and a database-aware health query from
the original pod, and confirm an unauthorized source remains denied. The example
does not establish a guaranteed time to diagnosis.

**The lesson:** A timeout is evidence of an unfinished operation, not a unique
firewall diagnosis. Configuration evidence, the observed source, and a narrow
before/after repair establish the cause for this case.

---

## Common Failure Patterns

| Symptom                              | OSI Layer | Likely Cause                            | Diagnostic Tool                          |
| ------------------------------------ | --------- | --------------------------------------- | ---------------------------------------- |
| `Link detected: no` in ethtool       | Layer 1   | Bad cable, disconnected, bad SFP        | `ethtool`, physical inspection           |
| Interface UP but not LOWER_UP        | Layer 1   | Physical link down                      | `ip link show`                           |
| ARP state FAILED for gateway         | Layer 2   | VLAN mismatch, wrong switch port        | `ip neigh`, `arping`, `tcpdump arp`      |
| Broadcast storm / high CPU on switch | Layer 2   | STP loop, missing BPDU guard            | `tcpdump -e`, switch logs                |
| `Network is unreachable`             | Layer 3   | No route to destination                 | `ip route show`, `ip route get`          |
| Ping works, app doesn't              | Layer 4+  | Port blocked, service down, TLS issue   | `nc -zv`, `ss -tlnp`, `openssl s_client` |
| Connection refused                   | Layer 4   | Nothing listening on port               | `ss -tlnp \| grep <port>`                |
| Connection timed out                 | Layer 3/4 | Firewall dropping packets silently      | `tcpdump`, security group check          |
| TLS handshake failure                | Layer 5/6 | Expired cert, wrong SNI, bad chain      | `openssl s_client -connect host:443`     |
| HTTP 502 Bad Gateway                 | Layer 7   | Proxy cannot reach backend              | `curl -v`, proxy logs, check backend     |
| DNS SERVFAIL                         | Layer 7   | Upstream resolver failure, DNSSEC issue | `dig +trace`, `dig @<authoritative>`     |

---

## Summary

- The OSI model is a diagnostic framework, not an implementation. No real stack implements it exactly, but every engineer uses it to communicate about and isolate problems.
- Seven layers, each with a specific responsibility: Physical → Data Link → Network → Transport → Session → Presentation → Application.
- Encapsulation wraps data with headers at each layer going down the stack. Decapsulation strips them going up. Each header carries the addressing and control information needed at that layer.
- The TCP/IP model (4 layers) maps to reality more directly. Use OSI for precision in troubleshooting, TCP/IP for protocol implementation discussions.
- Use OSI to organize hypotheses; choose tests matching the failing flow instead
  of requiring successful ping before investigating TCP, TLS, or application logs.
- The RST vs timeout distinction at Layer 4 is the most important diagnostic signal: RST means *something* answered — the server, a firewall, or a proxy, and only a capture or rule counters tell you which — while a timeout means nothing answered.
- "Destination Host Unreachable" from your own address is a Layer 2 failure: ARP for an on-link address got no reply.
- Every tunnel, overlay, and VPN adds encapsulation overhead that reduces effective MTU — understanding encapsulation is understanding why packets get silently dropped in complex network paths.

## What's Next

Module 5 covers IP — the Internet Protocol — in depth: IPv4 and IPv6 addressing, subnetting, CIDR notation, the IP header fields you will encounter in every packet capture, and the addressing system that makes Layer 3 routing possible. This is where the abstract concept of "Layer 3" becomes concrete with real addresses you will configure, subnet, and troubleshoot.

---

## Quick Reference Card

```
MODULE 4: THE OSI MODEL — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

THE 7 LAYERS (bottom to top)
  1  Physical        Bits on wire/air        ethtool, ip link
  2  Data Link       Frames, MAC addresses   ip neigh, tcpdump -e, arp
  3  Network         Packets, IP addresses   ping, traceroute, ip route
  4  Transport       Segments, ports          ss, nc, hping3, tcpdump
  5  Session         Connection management   (absorbed into app layer)
  6  Presentation    Encoding, encryption    openssl s_client
  7  Application     HTTP, DNS, SSH, gRPC    curl -v, dig, ssh

PROTOCOL DATA UNITS (PDU)
  Layer 1    Bits
  Layer 2    Frames
  Layer 3    Packets
  Layer 4    Segments (TCP) / Datagrams (UDP)
  Layer 7    Data / Messages

ENCAPSULATION (sending)
  App Data → [TCP Hdr + Data] → [IP Hdr + Segment] → [Eth Hdr + Packet + FCS] → Bits

DECAPSULATION (receiving)
  Bits → Frame → Packet → Segment → App Data

TROUBLESHOOTING CHECKLIST (choose relevant observations, not a mandatory order)
  L1:  ip link show / ethtool              ← Is the link up?
  L2:  ip neigh show                       ← Is ARP resolving?
  L3:  ping <target>                       ← Is the host reachable?
  L4:  nc -zv <host> <port>               ← Is the port open?
  L5/6: openssl s_client -connect h:443    ← Does TLS handshake complete?
  L7:  curl -v https://host/              ← Does the app respond?

KEY DIAGNOSTIC SIGNALS
  Connection refused   Layer 4   RST (or ICMP) came back — closed port, REJECT, or proxy
  Connection timed out Layer 3/4 SYN dropped  — firewall or unreachable
  TLS handshake error  Layer 5/6 Cert/cipher  — check openssl output
  HTTP 502/504         Layer 7   Proxy/backend — check upstream health

OSI vs TCP/IP MAPPING
  OSI 7,6,5 ──▶ TCP/IP Application
  OSI 4     ──▶ TCP/IP Transport
  OSI 3     ──▶ TCP/IP Internet
  OSI 2,1   ──▶ TCP/IP Link

OVERHEAD PER LAYER (typical)
  Ethernet header:  14 bytes
  IP header:        20 bytes (no options)
  TCP header:       20 bytes (no options)
  Total minimum:    54 bytes before any application data
  MSS advertised:   1460 bytes (1500 − 20 − 20)
  Linux full segment: 1448 bytes of payload (TCP timestamps use 12)
```

---

## References

- ISO/IEC 7498-1:1994 — The OSI Basic Reference Model
- RFC 1122 — Requirements for Internet Hosts — Communication Layers
- RFC 791 — Internet Protocol (IPv4)
- RFC 9293 — Transmission Control Protocol
- RFC 768 — User Datagram Protocol
- `man tcpdump` — packet capture and BPF filter syntax
- `man ss` — socket statistics utility
- `man ethtool` — display or change Ethernet device settings
- `man ip` — show / manipulate routing, network devices, interfaces
- Wireshark User Guide — https://www.wireshark.org/docs/wsug_html/
