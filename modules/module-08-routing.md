# Module 8: Routing

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 7 — ICMP, Ping, and Traceroute
> **Estimated time:** 3–4 hours (the lab runs offline in network namespaces)

## Overview

Routing is the mechanism that moves packets between networks. Every time a packet leaves a host, the kernel consults its routing table to decide where to send it. Every time that packet arrives at a router, the router consults its own routing table to decide the next hop. This repeats at every hop until the packet reaches the destination network. If any router in the chain does not have a matching route, the packet is dropped and — if ICMP is not filtered — an ICMP Destination Unreachable message is sent back to the sender.

A large share of connectivity incidents reported as "the network is down" are routing problems (most of the rest are filtering: security groups, NACLs, firewalls — Module 10). A missing default gateway, a route pointing at the wrong next hop, a cloud route table that was not updated after a subnet was added, an asymmetric routing path that breaks a stateful firewall — all of these are routing failures. They produce silent packet drops, connection timeouts, or one-way communication. Understanding routing means understanding why packets arrive or do not arrive.

This module covers the Linux routing table in detail — how to read it, how the kernel selects a route using longest prefix match, and how to add static routes. It covers NAT (Network Address Translation), which rewrites source or destination IP addresses and is the mechanism that lets private RFC 1918 addresses reach the public internet. It covers dynamic routing protocols at a conceptual level (detailed BGP coverage is in Module 9). And it covers policy routing — Linux's ability to maintain multiple routing tables and select between them based on source address or other criteria.

## What You Will Build Toward

- Read a Linux routing table and explain every field: destination, gateway, device, scope, metric, protocol
- Explain Longest Prefix Match and predict which route the kernel will select for any given destination
- Add, modify, and delete static routes on a Linux host
- Build a multi-subnet routed network using Linux namespaces and demonstrate packet forwarding
- Explain SNAT, DNAT, and masquerade — what each does, when to use it, and how connection tracking works
- Understand policy routing: multiple routing tables and `ip rule`
- Explain ECMP per-flow hashing, asymmetric paths through stateful devices, and reverse-path filtering
- Measure how long a dynamic routing protocol (OSPF) takes to route around a failure, and why

---

## 8.1 What Routing Does

### What This Is

Routing is the process of forwarding packets from one network to another based on the destination IP address. It is a Layer 3 function. Switches forward frames by MAC address within a single network (Layer 2). Routers forward packets by IP address between networks (Layer 3). A Linux host with IP forwarding enabled is a router.

Every device that participates in IP networking — hosts, routers, firewalls, cloud VPC gateways — has a routing table. The routing table is a list of rules that says: "For packets destined to network X, send them via gateway Y out interface Z."

### The Routing Decision

When the kernel needs to send a packet, it follows this process:

```
ROUTING DECISION — WHAT HAPPENS FOR EVERY PACKET

  1. Application calls send() with data destined for IP address D

  2. Kernel looks up D in the routing table

  3. Kernel finds the best matching route using Longest Prefix Match:
     - Compare D against every route's destination prefix
     - Multiple routes may match (e.g., 0.0.0.0/0 matches everything)
     - Select the route with the LONGEST (most specific) prefix
     - If tied: use the route with the lowest metric
     - A route can have several next hops (ECMP); a hash of the
       packet's addresses (and, if configured, ports) picks one
     - Before any of this, policy rules (8.6) choose WHICH table to
       search; the local table (the host's own addresses) comes first

  4. The matching route tells the kernel:
     - Next hop gateway (or "direct" if on the same subnet)
     - Output interface (eth0, wlan0, tun0, etc.)

  5. If next hop is a gateway:
     - ARP for the gateway's MAC address
     - Encapsulate in an Ethernet frame addressed to the gateway's MAC
     - Send out the interface

  6. If destination is directly connected (on-link):
     - ARP for the destination's MAC address directly
     - Encapsulate in an Ethernet frame addressed to the destination's MAC
     - Send out the interface

  7. If NO route matches:
     - Drop the packet
     - Return error to application: "Network is unreachable"
     A matching route of type blackhole, unreachable, or prohibit also
     refuses the packet, each with its own error (8.3).
```

### The Routing Table Structure

```
ROUTING TABLE FIELDS

  ┌──────────────┬────────────────┬──────────┬────────┬────────┬──────────┐
  │ Destination  │ Gateway        │ Device   │ Scope  │ Metric │ Protocol │
  ├──────────────┼────────────────┼──────────┼────────┼────────┼──────────┤
  │ 0.0.0.0/0    │ 10.0.1.1       │ eth0     │ global │ 100    │ dhcp     │
  │ 10.0.1.0/24  │ (none/direct)  │ eth0     │ link   │ 0      │ kernel   │
  │ 172.17.0.0/16│ (none/direct)  │ docker0  │ link   │ 0      │ kernel   │
  │ 10.0.5.0/24  │ 10.0.1.254     │ eth0     │ global │ 0      │ static   │
  └──────────────┴────────────────┴──────────┴────────┴────────┴──────────┘

  Destination:  The network this route applies to (CIDR notation)
  Gateway:      The IP to forward packets to (next hop).
                "none" or absent means the destination is directly connected.
  Device:       The network interface to send packets out of.
  Scope:        "link" = directly connected, "global" = reachable via gateway,
                "host" = this machine's own addresses (the local table).
  Metric:       Preference when multiple routes exist for the same dest.
                Lower = preferred.
  Protocol:     Who installed the route:
                "kernel" = auto-created for directly connected networks
                "dhcp"   = a DHCP client (Ubuntu's networkd, NetworkManager)
                "static" = a network manager from its configuration
                           (netplan/systemd-networkd routes: entries)
                "boot"   = `ip route add` by hand or a script — the
                           default, so ip hides it; `ip -d route` shows it
                "ra"     = IPv6 Router Advertisement (Module 5)
                "bgp" / "ospf" / "bird" = a routing daemon (FRR, BIRD)
```

### Diagram: Routing at Every Hop

```
PACKET FORWARDING — HOP BY HOP

  Host A (10.0.1.50)                                 Server (10.0.5.20)
  Routing table:                                     Routing table:
    default via 10.0.1.1                               default via 10.0.5.1
    10.0.1.0/24 dev eth0                               10.0.5.0/24 dev eth0

       │                                                     ▲
       │ dst=10.0.5.20                                       │
       │ Best match: default → gw 10.0.1.1                   │
       ▼                                                     │
  [Router 1 — 10.0.1.1 / 10.0.3.1]                         │
  Routing table:                                             │
    10.0.1.0/24 dev eth0                                     │
    10.0.3.0/24 dev eth1                                     │
    10.0.5.0/24 via 10.0.3.2                                │
       │                                                     │
       │ dst=10.0.5.20                                       │
       │ Best match: 10.0.5.0/24 → gw 10.0.3.2              │
       ▼                                                     │
  [Router 2 — 10.0.3.2 / 10.0.5.1]                         │
  Routing table:                                             │
    10.0.3.0/24 dev eth0                                     │
    10.0.5.0/24 dev eth1          ── directly connected ────▶│
       │
       │ dst=10.0.5.20
       │ Best match: 10.0.5.0/24 dev eth1 (direct)
       │ ARP for 10.0.5.20 on eth1
       ▼
  Packet delivered to Server.

  KEY OBSERVATIONS:
  - Each router only knows the NEXT hop, not the full path.
  - The IP destination (10.0.5.20) never changes.
  - The Ethernet destination MAC changes at EVERY hop (ARP at each step).
  - If Router 1 had no route to 10.0.5.0/24, it would drop the packet
    and send ICMP Destination Unreachable back to Host A.
```

---

## 8.2 Reading the Routing Table

### What This Is

The routing table is one of the first things to check for connectivity problems, and the cheapest: one command, no packets sent. If the route does not exist, the packet will never leave the host. Remember that there are several tables (`ip route show table all`), and in the cloud the host's table is only the first of several route lookups — the VPC route table is another, invisible from inside the VM.

### Viewing the Routing Table

```bash
# Modern Linux (recommended — most detail)
ip route show

# Show only IPv6 routes
ip -6 route show

# Every table, including local (your own addresses) and policy tables
ip route show table all

# Legacy commands (net-tools; often not installed any more, and blind to
# policy tables and multipath routes)
route -n
netstat -rn
```

### Reading `ip route show` Output

```bash
ip route show
```

```
default via 10.0.1.1 dev eth0 proto dhcp src 10.0.1.50 metric 100
10.0.1.0/24 dev eth0 proto kernel scope link src 10.0.1.50 metric 100
172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown

FIELD BY FIELD:

default via 10.0.1.1 dev eth0 proto dhcp src 10.0.1.50 metric 100
│       │             │        │          │              │
│       │             │        │          │              └─ Preference (lower=better)
│       │             │        │          └─ Use this as source IP for packets
│       │             │        └─ Route learned from DHCP
│       │             └─ Send packets out this interface
│       └─ Forward to this gateway (next hop)
└─ 0.0.0.0/0 — matches ANY destination not matched by a more specific route

10.0.1.0/24 dev eth0 proto kernel scope link src 10.0.1.50 metric 100
│            │        │            │          │
│            │        │            │          └─ Source IP for this network
│            │        │            └─ "link" scope = directly connected
│            │        └─ Auto-created when IP was assigned to eth0
│            └─ Packets go directly out eth0 (no gateway — direct delivery)
└─ Destination network: 10.0.1.0 through 10.0.1.255

172.17.0.0/16 dev docker0 proto kernel scope link src 172.17.0.1 linkdown
                                                                  │
                                                                  └─ Interface is DOWN
                                                                     (no containers running)
```

### Longest Prefix Match (LPM)

Within a routing-table lookup, the longest matching prefix is selected. Policy
rules determine which tables are consulted; compare rule priority before applying
longest-prefix reasoning across entries in different tables.

```
LONGEST PREFIX MATCH — HOW THE KERNEL CHOOSES

  Routing table:
    0.0.0.0/0      via 10.0.1.1     (default route — matches everything)
    10.0.0.0/8     via 10.0.1.254   (matches all 10.x.x.x)
    10.0.5.0/24    via 10.0.1.100   (matches 10.0.5.0 – 10.0.5.255)
    10.0.5.20/32   via 10.0.1.200   (matches ONLY 10.0.5.20)

  Packet destined for 10.0.5.20:
    ✓ Matches 0.0.0.0/0      prefix length = 0  bits
    ✓ Matches 10.0.0.0/8     prefix length = 8  bits
    ✓ Matches 10.0.5.0/24    prefix length = 24 bits
    ✓ Matches 10.0.5.20/32   prefix length = 32 bits  ← WINNER (longest match)

  Route selected: 10.0.5.20/32 via 10.0.1.200

  Packet destined for 10.0.5.99:
    ✓ Matches 0.0.0.0/0      prefix length = 0  bits
    ✓ Matches 10.0.0.0/8     prefix length = 8  bits
    ✓ Matches 10.0.5.0/24    prefix length = 24 bits  ← WINNER
    ✗ Does NOT match 10.0.5.20/32

  Route selected: 10.0.5.0/24 via 10.0.1.100

  Packet destined for 10.0.99.1:
    ✓ Matches 0.0.0.0/0      prefix length = 0  bits
    ✓ Matches 10.0.0.0/8     prefix length = 8  bits  ← WINNER
    ✗ Does NOT match 10.0.5.0/24
    ✗ Does NOT match 10.0.5.20/32

  Route selected: 10.0.0.0/8 via 10.0.1.254

  Packet destined for 8.8.8.8:
    ✓ Matches 0.0.0.0/0      prefix length = 0  bits  ← WINNER (only match)

  Route selected: default via 10.0.1.1
```

### Equal-Cost Multipath (ECMP)

When a route has several next hops, Linux picks one per flow by hashing packet fields, controlled by `net.ipv4.fib_multipath_hash_policy`:

| Policy | Hash input | Effect |
| --- | --- | --- |
| 0 (default) | source and destination IP | all traffic between two hosts takes one path |
| 1 | the 5-tuple (addresses, protocol, ports) | each connection can take a different path |
| 2, 3 | inner headers of tunnels; custom field set | for overlays and special cases |

Routers and cloud load balancers do the same thing in hardware. Two consequences for troubleshooting: a fault on one of N paths breaks roughly 1/N of *connections* (not 1/N of packets), and retries that open a new connection may succeed. Traceroute, which uses a new flow for every probe, can show two routers at the same hop — the lab shows both effects.

### Querying the Routing Table for a Specific Destination

```bash
# "What route would the kernel use for this destination?"
ip route get 8.8.8.8
```

```
8.8.8.8 via 10.0.1.1 dev eth0 src 10.0.1.50 uid 1000
    cache
```

This tells you exactly which route was selected, which gateway will be used, which interface the packet leaves from, and what source IP will be used. This is the single most useful routing diagnostic command.

```bash
# Check route for a host on a directly connected network
ip route get 10.0.1.20
```

```
10.0.1.20 dev eth0 src 10.0.1.50 uid 1000
    cache
```

No "via" — means direct delivery. The kernel will ARP for 10.0.1.20 directly.

`ip route get` accepts the rest of the flow, which matters as soon as policy routing or ECMP is involved: `ip route get 8.8.8.8 from 10.0.1.60` (a different source address), `… mark 0x1` (a firewall mark), `… iif eth0` (as if forwarding a packet that arrived on eth0), `… ipproto tcp sport 40000 dport 443` (the ports an ECMP hash would use). The lab uses all of these.

---

## 8.3 Static Routing

### What This Is

A static route is a route manually added to the routing table by an administrator. Static routes do not change unless an administrator changes them. They are simple, predictable, and appropriate for small networks or specific overrides. They do not adapt to network failures — if the next-hop gateway goes down, the static route still points at it and packets are black-holed.

### Adding and Removing Static Routes

```bash
# Add a route: packets for 10.0.5.0/24 go via gateway 10.0.1.254
sudo ip route add 10.0.5.0/24 via 10.0.1.254 dev eth0

# Add a route for a single host (/32 — host route)
sudo ip route add 10.0.5.20/32 via 10.0.1.200

# Add a default route
sudo ip route add default via 10.0.1.1

# Delete a route
sudo ip route del 10.0.5.0/24

# Replace a route (update in place — no need to delete first)
sudo ip route replace 10.0.5.0/24 via 10.0.1.253 dev eth0

# Routes that refuse instead of forwarding (no packet is sent):
sudo ip route add blackhole 203.0.113.0/24     # silently discard; apps get EINVAL
sudo ip route add unreachable 203.0.113.0/24   # apps get EHOSTUNREACH ("No route to host")
sudo ip route add prohibit 203.0.113.0/24      # apps get EACCES ("Permission denied")

# Equal-cost multipath: one prefix, two next hops; the kernel hashes each flow
sudo ip route add 10.0.9.0/24 nexthop via 10.0.1.254 nexthop via 10.0.1.253

# Add a route with a metric (preference)
sudo ip route add 10.0.5.0/24 via 10.0.1.254 metric 100
sudo ip route add 10.0.5.0/24 via 10.0.1.253 metric 200
# Both routes exist. Metric 100 is preferred. If it is DELETED (or its
# interface goes down), metric 200 takes over. Linux does not notice that
# the 10.0.1.254 router died: a static route to a dead next hop keeps
# black-holing traffic. Failover needs a routing protocol, a health-
# checking daemon, or a cloud service doing it for you.
```

### Making Routes Persistent

Routes added with `ip route add` are lost on reboot. To make them persistent:

```bash
# Ubuntu with Netplan (default on Ubuntu 18.04+):
# Edit /etc/netplan/01-netcfg.yaml
network:
  version: 2
  ethernets:
    eth0:
      routes:
        - to: 10.0.5.0/24
          via: 10.0.1.254

# Apply:
sudo netplan apply

# RHEL/CentOS with nmcli:
sudo nmcli connection modify eth0 +ipv4.routes "10.0.5.0/24 10.0.1.254"
sudo nmcli connection up eth0
```

### IP Forwarding — Turning a Linux Host into a Router

By default, a Linux host drops packets that are not addressed to it. To forward packets between interfaces (act as a router), IP forwarding must be enabled:

```bash
# Check current state
sysctl net.ipv4.ip_forward
# 0 = forwarding disabled (default on most hosts)
# 1 = forwarding enabled (required for routing, NAT, containers)

# Enable temporarily (lost on reboot)
sudo sysctl -w net.ipv4.ip_forward=1

# Enable permanently
echo "net.ipv4.ip_forward = 1" | sudo tee -a /etc/sysctl.d/99-routing.conf
sudo sysctl -p /etc/sysctl.d/99-routing.conf
```

Docker and Kubernetes enable IP forwarding automatically, so on many hosts `ip_forward = 1` simply means containers. The reverse matters more in tickets: a node where something reset `ip_forward` to 0 (a hardening script, a sysctl file applied at boot) silently stops forwarding pod and container traffic — no ICMP error, just drops (Module 5's "break it" exercise).

### When Static Routing Is Appropriate

```
USE STATIC ROUTES WHEN:
  ✓ Small network with few routes that rarely change
  ✓ Specific overrides (e.g., "send traffic for this prefix via the VPN")
  ✓ Cloud VPC route tables (AWS, GCP, Azure use static routes internally)
  ✓ Default gateway configuration
  ✓ Lab environments and testing

DO NOT USE STATIC ROUTES WHEN:
  ✗ Many routes that change frequently
  ✗ Redundant paths where automatic failover is needed
  ✗ ISP-level or internet routing (use BGP — Module 9)
  ✗ Large enterprise networks (use OSPF or IS-IS)
```

---

## 8.4 Dynamic Routing Protocols (Conceptual)

### What This Is

Static routing does not scale. If you have 500 routers and a link fails, someone must manually update routes on every affected router. Dynamic routing protocols solve this by having routers automatically discover neighbors, exchange routes, and adapt when the network topology changes.

This module covers the concepts. Module 9 covers BGP in detail.

### Two Families of Routing Protocols

```
DISTANCE VECTOR vs LINK-STATE

  DISTANCE VECTOR                           LINK-STATE
  ─────────────────────────────────         ─────────────────────────────────
  Each router tells neighbors:              Each router tells ALL routers:
  "I can reach network X in N hops"         "Here are my directly connected links"

  Router knows:                             Router knows:
  - Distance (hops) to each destination     - Complete topology of the network
  - Direction (which neighbor to forward to) - Computes best path itself (Dijkstra)

  Simple but slow to converge.              Complex but fast convergence.
  Loops possible during convergence.       Transient forwarding loops also possible.

  Examples:                                 Examples:
  - RIP  (Routing Info Protocol)            - OSPF (Open Shortest Path First)
    Max 15 hops. Obsolete for most use.       The standard for enterprise networks.
  - EIGRP (Enhanced Interior Gateway RP)    - IS-IS (Intermediate System to IS)
    Cisco-developed, "advanced distance       Common in ISP networks and large DCs.
    vector"; published as RFC 7868 (2016).

  When to use:                              When to use:
  Small, simple networks (home routers).    Enterprise, data center, ISP networks.
  Rarely chosen for new deployments.        The default choice for internal routing.


  BGP (Border Gateway Protocol):
  A path-vector protocol — a distinct category.
  Routes between autonomous systems (organizations).
  The protocol that runs the internet.
  Covered in detail in Module 9.


INTERIOR vs EXTERIOR ROUTING

  IGP (Interior Gateway Protocol)           EGP (Exterior Gateway Protocol)
  ─────────────────────────────             ─────────────────────────────
  Routing WITHIN an organization            Routing BETWEEN organizations
  - OSPF                                    - BGP
  - IS-IS
  - EIGRP
  - RIP

  Inside your company / cloud VPC           Between your network and the internet
```

### How OSPF Converges, in Numbers

The lab runs OSPF (FRR) on four Linux routers and measures what this section describes:

```
  OSPF STEP                          WHAT HAPPENS                      LAB TIMING (hello 1 s, dead 4 s)
  ─────────────────────────────────  ────────────────────────────────  ─────────────────────────────────
  Neighbours discover each other      hellos on each link               adjacencies Full in ~2.4 s
  Exchange link-state databases       every router learns every link
  Run SPF (Dijkstra), install routes  equal costs → ECMP next hops     all routes in the kernel in ~10 s
  A neighbour dies SILENTLY           nothing until hellos stop         3.2–3.4 s of lost traffic (≤ dead interval)
  A link goes DOWN (carrier loss)     the router notices at once        0–1 lost probe at 20 per second
```

Two lessons carry over to every routing protocol, BGP included. **Failure detection dominates convergence**: recomputing paths takes milliseconds, noticing a failure takes as long as the timers say — and the defaults are far longer than the lab's (OSPF hello 10 s, dead 40 s; BGP hold time 90–180 s), which is why production networks add BFD (Bidirectional Forwarding Detection, sub-second). **A dead box is worse than a dead cable**: a link that drops carrier is noticed instantly, while a router that hangs, or a firewall that silently blocks the protocol, is noticed only by timeout.

### What You Need to Know as an Engineer (Not a Network Architect)

```
PRACTICAL KNOWLEDGE FOR SUPPORT/DEV/CLOUD ENGINEERS:

  1. You will rarely configure OSPF or BGP directly.
     But you need to recognize when a routing protocol failure is the cause
     of a connectivity issue.

  2. Signs of a dynamic routing failure:
     - Routes suddenly disappear from the routing table
     - Traceroute shows different paths for the same destination at different times
     - Connectivity flaps (works, then fails, then works again)
     - "Destination unreachable" for networks that were working minutes ago

  3. What to check:
     - Is the routing daemon running? (systemctl status bird / frr)
     - Are neighbors established? (birdc show protocols / vtysh -c "show ip ospf neighbor")
     - Did a specific route withdraw? (ip route show — is the route present?)

  4. In cloud environments:
     - AWS VPC route tables are static but managed automatically by services
       like Transit Gateway, VPN connections, and peering.
     - Route propagation can be enabled to let VPN/Direct Connect routes
       automatically appear in VPC route tables.
     - GCP uses custom routes + Cloud Router (which speaks BGP to your on-prem).
     - Azure has system routes, user-defined routes (UDRs), and routes
       learned by BGP from VPN/ExpressRoute gateways; "effective routes"
       on a NIC shows the result.
     - Kubernetes CNIs often run BGP themselves: Calico (BIRD) and
       Cilium can advertise pod CIDRs to the network; a broken BGP
       session there looks like "pods on one node are unreachable".
```

---

## 8.5 NAT — Network Address Translation

### What This Is

NAT modifies IP addresses (and often port numbers) in packet headers as packets pass through a router or firewall. It is the reason billions of devices with private RFC 1918 addresses can communicate on the internet — a NAT device translates their private addresses to public addresses for outbound traffic, and translates back for return traffic.

NAT operates on the routing device, not on the endpoints. The endpoints are unaware that NAT is happening.

### Types of NAT

```
THREE TYPES OF NAT

  SNAT (Source NAT)
  ─────────────────
  Changes the SOURCE IP address of outbound packets.
  Return traffic has its DESTINATION IP translated back.

  Use case: Allow private hosts (10.x.x.x) to reach the internet.

  Before SNAT:
  [Host 10.0.1.50] ──src=10.0.1.50, dst=198.51.100.80──▶ [NAT Gateway]
  After SNAT:
  [NAT Gateway] ──src=203.0.113.5, dst=198.51.100.80──▶ [Internet]
                    ↑
              Public IP of the NAT gateway replaces private IP


  DNAT (Destination NAT)
  ──────────────────────
  Changes the DESTINATION IP address of inbound packets.
  Return traffic has its SOURCE IP translated back.
  The backend sees the client's real source address (only the
  destination changed), so it must route replies back THROUGH the NAT
  device; if it replies directly, the client receives a reply from an
  address it never contacted and drops it.

  Use case: Port forwarding. Route incoming traffic on a public IP
  to a private backend server.

  Before DNAT:
  [Internet] ──src=198.51.100.1, dst=203.0.113.5:443──▶ [NAT Gateway]
  After DNAT:
  [NAT Gateway] ──src=198.51.100.1, dst=10.0.1.50:8080──▶ [Backend]
                                          ↑
                    Private IP of the backend replaces public IP


  MASQUERADE
  ──────────
  A variant of SNAT where the source IP is automatically set to the
  outgoing interface's current IP address. Used when the public IP is
  dynamic (DHCP on the WAN interface).

  Linux stateful NAT selects a mapping on the first packet and reuses it.
  MASQUERADE derives the source from the egress interface when creating
  the mapping and handles address changes; it is not a fresh lookup per packet.
```

### Diagram: NAT in a Typical Cloud Architecture

```
NAT IN A CLOUD VPC

  Private Subnet (10.0.1.0/24)          Public Internet
  ┌────────────────────────────┐
  │ App Server  10.0.1.50      │
  │ App Server  10.0.1.51      │─────▶ NAT Gateway ─────▶ [Internet]
  │ App Server  10.0.1.52      │       (SNAT)
  └────────────────────────────┘       Public IP: 52.1.2.3

  Outbound packet from 10.0.1.50 to 198.51.100.80:
  ┌─────────────────────────────────────────────────────────┐
  │ Before NAT: src=10.0.1.50:54321  dst=198.51.100.80:443 │
  │ After NAT:  src=52.1.2.3:40001   dst=198.51.100.80:443 │
  └─────────────────────────────────────────────────────────┘

  Return packet from 198.51.100.80:
  ┌─────────────────────────────────────────────────────────┐
  │ Arrives:    src=198.51.100.80:443 dst=52.1.2.3:40001   │
  │ After NAT:  src=198.51.100.80:443 dst=10.0.1.50:54321  │
  └─────────────────────────────────────────────────────────┘

  The internet sees 52.1.2.3. It never sees 10.0.1.50.
  The app server sees 198.51.100.80. It never knows NAT happened.
```

### Connection Tracking (conntrack)

The Linux stateful NAT used here requires conntrack to associate original and reply
tuples. Not every possible address-translation design is stateful; stateless
algorithmic translation is a different mechanism.

```
CONNECTION TRACKING TABLE (conntrack)

  The kernel's conntrack table stores the state of every connection it
  tracks — NAT'd or not, as soon as any rule uses NAT or connection state:

  ┌──────────┬──────────────────────────────┬──────────────────────────────┐
  │  Proto   │  Original (internal)         │  Reply (translated)          │
  ├──────────┼──────────────────────────────┼──────────────────────────────┤
  │  tcp     │  10.0.1.50:54321 →           │  198.51.100.80:443 →         │
  │          │  198.51.100.80:443            │  52.1.2.3:40001              │
  ├──────────┼──────────────────────────────┼──────────────────────────────┤
  │  tcp     │  10.0.1.51:55000 →           │  198.51.100.80:443 →         │
  │          │  198.51.100.80:443            │  52.1.2.3:40002              │
  └──────────┴──────────────────────────────┴──────────────────────────────┘

  View the conntrack table on Linux:
  sudo conntrack -L
  sudo conntrack -C          # Count entries
  cat /proc/sys/net/netfilter/nf_conntrack_max   # Read the actual configured limit
```

### Conntrack Table Exhaustion

```
CONNTRACK EXHAUSTION — A COMMON PRODUCTION FAILURE

  Problem: Every NAT'd connection uses one conntrack entry.
  The table has a maximum size (nf_conntrack_max).
  When the table is full, NEW connections are DROPPED.

  Symptoms:
  - Intermittent connection failures under high load
  - "nf_conntrack: table full, dropping packet" in dmesg/syslog
  - Connections that were already established continue working
  - Only NEW connections fail (SYN packets are dropped)

  Diagnosis:
  sudo dmesg | grep conntrack            # Check for "table full" messages
  sudo conntrack -C                       # Current entry count
  cat /proc/sys/net/netfilter/nf_conntrack_max  # Maximum entries

  Mitigation decisions:
  Correlate count/limit, drop-counter deltas, logs, and failing flows.
  Pooling and correcting leaks may reduce pressure. Increasing limits costs
  memory; reducing conntrack lifetimes can break valid idle flows. Preserve
  original values and a rollback plan before any production change.
  Conntrack TIME_WAIT lifetime is separate from endpoint TCP TIME_WAIT.
```

### NAT with iptables

```bash
# SNAT: Change source IP for outbound traffic
# All traffic from 10.0.1.0/24 leaving via eth0 gets source IP 203.0.113.5
sudo iptables -t nat -A POSTROUTING -s 10.0.1.0/24 -o eth0 -j SNAT --to-source 203.0.113.5

# MASQUERADE: Like SNAT but uses the interface's current IP (for dynamic IPs)
sudo iptables -t nat -A POSTROUTING -s 10.0.1.0/24 -o eth0 -j MASQUERADE

# DNAT: Port forwarding. Incoming traffic on port 8080 goes to internal host on port 80
sudo iptables -t nat -A PREROUTING -i eth0 -p tcp --dport 8080 -j DNAT --to-destination 10.0.1.50:80

# View NAT rules
sudo iptables -t nat -L -n -v
```

### NAT in Cloud

```
CLOUD NAT SERVICES

  AWS NAT Gateway
  ────────────────
  Managed SNAT service. Placed in a public subnet.
  Private subnets route 0.0.0.0/0 to the NAT Gateway.
  Up to 55,000 simultaneous connections to each unique destination
  (IP, port, protocol) per assigned IP address; more IPs raise it.
  Idle TCP connections are dropped after 350 s — long-lived idle
  connections need keepalives shorter than that.
  Costs: per-hour + per-GB processed (often a surprise on the bill).

  GCP Cloud NAT
  ─────────────
  Managed SNAT service at the VPC level.
  Does not require a separate instance — operates on the network tier.
  Automatic NAT IP allocation or manual assignment.
  Ports are allocated per VM: by default a minimum of 64 per VM,
  so one busy VM talking to one destination runs out early; dynamic
  port allocation raises the ceiling.

  Azure NAT Gateway
  ─────────────────
  Managed SNAT for outbound connectivity.
  Associates with subnets. Uses public IP or prefix.
  64,512 SNAT ports per public IP; idle timeout 4 minutes by default
  (configurable up to 120).

  Common to all:
  - You cannot SSH into a NAT gateway instance (it's managed)
  - SNAT port exhaustion is the #1 failure mode
  - Monitor: CloudWatch ErrorPortAllocation (AWS), Cloud Monitoring
    dropped-ports metrics (GCP), Azure Monitor SNAT metrics
  - Ports are per destination: thousands of connections to one API
    endpoint exhaust ports; the same number spread across many
    destinations does not
  - The fix is usually fewer, reused connections (HTTP keep-alive,
    connection pools) before more IPs
  - When SNAT ports are exhausted: new connections fail, existing survive
```

---

## 8.6 Policy Routing and Multiple Tables

### What This Is

Linux evaluates policy rules in priority order to select lookups, then uses longest
prefix match within a selected table. Rules can match source, mark, or interfaces.
Table identifiers support 32-bit values, not only 255 tables; some IDs are reserved.

### Why Policy Routing Exists

**Use case 1: Multi-homed servers.** A server has two network interfaces connected to two different ISPs. Without policy routing, all traffic leaves via one interface (the default route). With policy routing, traffic that arrives on ISP-A's interface leaves via ISP-A, and traffic that arrives on ISP-B's interface leaves via ISP-B.

**Use case 2: split tunneling.** A destination-only corporate prefix and a default
route can coexist in one table. Multiple tables are needed when selection depends
on additional policy such as source identity, not inherently for every split tunnel.

**Use case 3: Container networking.** Several Kubernetes CNIs install policy rules: the AWS VPC CNI adds `ip rule`s so pods using a secondary ENI's addresses leave through that ENI; Cilium and Calico use rules and marks for some modes. `ip rule` on a node is the first place to look when pod traffic leaves by an unexpected interface. (Docker's default bridge does not need them.)

### How It Works

```
POLICY ROUTING — TWO-STEP LOOKUP

  Step 1: ip rule — which routing table to use?
  ┌──────────────────────────────────────────────────────────┐
  │  Priority 0:     from all lookup local                   │
  │  Priority 100:   from 10.0.1.0/24 lookup custom_table    │
  │  Priority 32766: from all lookup main                    │
  │  Priority 32767: from all lookup default                 │
  └──────────────────────────────────────────────────────────┘

  Rules are evaluated in priority order (lowest number = first).
  "from 10.0.1.0/24" means: if the SOURCE IP is in this range.
  "lookup custom_table" means: use this routing table.

  Step 2: ip route show table <name> — the selected routing table
  Each table is a normal routing table with its own routes.

  Example: traffic from 10.0.1.0/24 uses custom_table, which has a
  different default gateway than the main table.
```

### Commands

```bash
# View all routing rules
ip rule show

# Default output:
# 0:       from all lookup local        ← system routes (broadcast, loopback)
# 32766:   from all lookup main         ← the normal routing table
# 32767:   from all lookup default      ← fallback (usually empty)

# Add a rule: traffic from 10.0.1.0/24 uses table 100
sudo ip rule add from 10.0.1.0/24 lookup 100 priority 100

# Configuration template: eth0 must actually own an address in 10.0.1.0/24
# Add the connected route before its reachable next hop
sudo ip route add 10.0.1.0/24 dev eth0 table 100
sudo ip route add default via 10.0.1.1 dev eth0 table 100
ip route get 198.51.100.10 from 10.0.1.50

# View a specific routing table
ip route show table 100

# Name tables for readability (optional)
echo "100 custom_table" | sudo tee -a /etc/iproute2/rt_tables
ip route show table custom_table

# Delete a rule
sudo ip rule del from 10.0.1.0/24 lookup 100
sudo ip route del default via 10.0.1.1 dev eth0 table 100
sudo ip route del 10.0.1.0/24 dev eth0 table 100
```

Inspect `net.ipv4.conf.all.rp_filter` and the relevant interface setting when a
multihomed return path differs. Strict reverse-path filtering can reject traffic
whose reverse lookup uses another interface; asymmetry itself is not necessarily
a fault. Compare source-qualified lookups and captures before changing policy.

### Diagram: Policy Routing for Multi-Homed Server

```
MULTI-HOMED SERVER WITH POLICY ROUTING

  ISP A (198.51.100.0/24)                ISP B (203.0.113.0/24)
       │                                      │
       │ eth0: 198.51.100.10                   │ eth1: 203.0.113.10
       └──────────┐                 ┌──────────┘
                  │                 │
               ┌──┴─────────────────┴──┐
               │       Server          │
               │                       │
               │  ip rule:             │
               │  from 198.51.100.10   │
               │    → lookup table_a   │
               │  from 203.0.113.10    │
               │    → lookup table_b   │
               │                       │
               │  table_a:             │
               │    default via        │
               │    198.51.100.1       │
               │                       │
               │  table_b:             │
               │    default via        │
               │    203.0.113.1        │
               └───────────────────────┘

  Without policy routing:
  All outbound traffic uses one default gateway.
  Traffic arriving on eth1 might reply via eth0.
  ISP A sees return traffic for connections it didn't initiate.
  ISP A drops it (anti-spoofing: that source address is ISP B's), or a
  stateful firewall on that path drops it because it never saw the
  connection start — the lab reproduces exactly this.

  With policy routing:
  Traffic from eth0's IP → table_a → ISP A gateway
  Traffic from eth1's IP → table_b → ISP B gateway
  Return traffic always goes back the way it came.
```

---

## 8.7 Asymmetric Routing and Reverse-Path Filtering

### What This Is

A path is **asymmetric** when a connection's two directions take different routes. On the internet that is normal and harmless (BGP chooses each direction independently, Module 9). It becomes a failure when something on one of the paths keeps per-connection state:

- **Stateful firewalls and security groups** see only half the conversation. A SYN-ACK with no SYN is `INVALID` and dropped (the lab shows exactly this).
- **NAT devices** see replies they have no mapping for.
- **Cloud network appliances** (firewall VMs, NVAs, Gateway Load Balancer targets) inserted with route tables in one direction only.

The signature in a capture: the SYN arrives at the server, the server sends SYN-ACKs (and retransmits them), and the client never receives one.

### Reverse-Path Filtering

Linux can drop asymmetric traffic itself. With `rp_filter`, the kernel checks each incoming packet's **source** address: would my route back to that source leave through the interface this packet arrived on?

| `net.ipv4.conf.<if>.rp_filter` | Behaviour |
| --- | --- |
| 0 | No check |
| 1 (strict) | Drop unless the best route back to the source uses the arrival interface |
| 2 (loose) | Drop only if there is no route back to the source at all |

The value used is the **maximum** of `conf.all.rp_filter` and the interface's own value. Ubuntu sets `2` (loose) on the host by default (`/etc/sysctl.d/10-network-security.conf`); other distributions and some hardening guides set `1`; new network namespaces start at 0. Strict mode protects single-homed hosts from spoofed packets and breaks multi-homed hosts and policy routing. Drops are silent except for a counter: `nstat -az TcpExtIPReversePathFilter`. The lab's Part 2 exercise 4 reproduces a strict-mode drop.

### Proving It

```bash
ip route get <client-ip> from <server-ip>               # the reply's route on the server
ip route get <server-ip> from <client-ip> iif eth1      # would rp_filter accept a packet arriving on eth1?
tcpdump -ni any 'host <client-ip> and tcp[tcpflags] & (tcp-syn) != 0'   # SYNs in, SYN-ACKs out — which interfaces?
```

`ip route get … iif <dev>` simulates a packet arriving on that interface, reverse-path check included: in the lab, with strict mode on the server, `ip route get 198.18.14.10 from 198.18.10.20 iif eth1` answers `RTNETLINK answers: Invalid argument`; with loose mode it returns the normal `local … dev lo` result.

---

## Hands-On Lab: Routing, Policy, and NAT

### Lab Goals

- Read the routing tables and the kernel's decision for any flow with `ip route get`
- See longest prefix match cause a one-host outage, and route types refuse traffic with distinct errors
- Watch ECMP split flows across two routers, and trace one flow consistently
- Build source-based policy routing, break a stateful firewall with the asymmetric path it creates, and fix it with SNAT
- Use DNAT and read the conntrack entry it creates
- Exhaust a SNAT port range and find the evidence on the NAT device
- Run OSPF on four routers and measure convergence after a silent failure and after a link-down

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iproute2`, `iputils-ping`, `traceroute`, `tcpdump`, `iptables`, `conntrack`, `curl`
- `frr` for Part 2 (`sudo apt-get install frr`; the package also starts an idle system `frr` service, which the lab does not use — `sudo systemctl disable --now frr` if you prefer)
- Parts 1–2 are offline; Part 3 reads your own machine.

---

### Lab Part 1: Run the Routing Lab

[`routing.json`](../labs/netlab/routing.json) builds this network:

```
                       LAN 198.18.10.0/24 (bridge)
     client .10 (+ .20) ──────┬───────────── r9 .2 ──── 198.18.14.0/24 ──── server eth1 .10
                              │
                            r1 .1  (iptables: drop INVALID conntrack state)
                ┌─────────────┼───────────────┐
         198.18.11.0/24  198.18.12.0/24  198.18.13.0/24
                │             │               │
         server eth0 .10     r2 .2          r3 .2
         (default via r1)     └──── 198.19.12.0/24 (bridge) ──── far .10
                                     r2 .2   r3 .3
```

r1 reaches `far` over two equal paths (r2 and r3) and hashes flows by port. The server has two interfaces but only one default route, via r1. The server runs labserver on 8080 (fast) and 8090 (replies after 60 s).

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/routing.json      # about 40 s; 28 checks
```

The excerpts below are from a real run.

**The table and the decision.**

```
check 1/28: the client's table: …
  | default via 198.18.10.1 dev eth0
  | 198.18.10.0/24 dev eth0 proto kernel scope link src 198.18.10.10
  | unicast default via 198.18.10.1 dev eth0 proto boot scope global
check 2/28: the kernel's decision for the server: next hop, interface, source
  | 198.18.11.10 via 198.18.10.1 dev eth0 src 198.18.10.10 uid 0
check 3/28: and for a host on its own subnet: no 'via' — deliver directly
  | 198.18.10.2 dev eth0 src 198.18.10.10 uid 0
```

The default route was added with `ip route add` (by netlab), so its protocol is `boot`, which `ip` hides unless you ask with `-d`. Note `src`: the kernel also picks the source address, from the route or the interface. A host with several addresses can send from the "wrong" one, which a remote allowlist then rejects — `ip route get` shows it before you capture anything.

**Longest prefix match causes a one-host outage.**

```
check 6/28: the /32 wins for 198.18.11.10 only; its neighbour .11 still uses the default route
  | 198.18.11.10 via 198.18.10.99 dev eth0 src 198.18.10.10 uid 0
  | 198.18.11.11 via 198.18.10.1 dev eth0 src 198.18.10.10 uid 0
check 7/28: one host is now unreachable while the rest of the subnet works: the classic stale-host-route outage
  | From 198.18.10.10 icmp_seq=1 Destination Host Unreachable
```

The error comes from the client's own address after ~3 s: the /32's next hop 198.18.10.99 never answered ARP (Module 6). Leftover /32 routes from VPN clients, old failover scripts, and hand-made fixes cause exactly this "only one host is broken" pattern.

**Routes that refuse.**

```
check 10/28: what an application sees for each: EINVAL, EHOSTUNREACH, EACCES — instantly, nothing is sent
  | error:   … dial tcp 203.0.113.5:80: connect: invalid argument        (blackhole)
  | error:   … dial tcp 203.0.113.70:80: connect: no route to host       (unreachable)
  | error:   … dial tcp 203.0.113.130:80: connect: permission denied     (prohibit)
  | ping: Do you want to ping broadcast? Then -b. If not, check your local firewall rules
```

Three more meanings for errors you will meet in tickets. "Permission denied" on a connect usually means a local firewall rule — but a `prohibit` route produces it too, and ping's advice about broadcasts is simply wrong here. `ip route get` on the destination settles it in one line: `RTNETLINK answers: Permission denied` and the route type.

**ECMP.**

```
check 11/28: ECMP: r1 has two equal next hops … (fib_multipath_hash_policy=1)
  | 198.19.12.0/24
  | 	nexthop via 198.18.12.2 dev eth2 weight 1
  | 	nexthop via 198.18.13.2 dev eth3 weight 1
  |       4 198.19.12.10 from 198.18.10.10 via 198.18.12.2 dev eth2
  |       6 198.19.12.10 from 198.18.10.10 via 198.18.13.2 dev eth3
check 12/28: traceroute uses a new flow for every probe, so hop 2 shows both routers
  |  2  198.18.13.2  0.078 ms 198.18.12.2  0.021 ms 198.18.13.2  0.009 ms … 198.18.12.2  0.102 ms 198.18.13.2  0.018 ms
check 13/28: gotrace -fixed sends every probe in one flow, so it sees one consistent path
  |  1  198.18.10.1  0.052 ms
  |  2  198.18.12.2  0.018 ms
  |  3  198.19.12.10  0.051 ms
  | distinct hop-2 routers: 1
```

Ten simulated flows (source ports 1000–1009) split 4/6 between the two next hops. Traceroute's hop 2 lists both routers, which is not a routing problem — it is ECMP seen by a tool that changes flows. [`gotrace -fixed`](../labs/go/cmd/gotrace) keeps every probe in one flow, so all six probes at hop 2 saw the same router.

Building this check exposed a detail worth knowing: in this lab, probes with *identical* ports sent from *different sockets* still took different paths. With hash policy 1, Linux can reuse a hash already attached to the packet instead of recomputing it from the headers, and on veth links that is the sending socket's random flow hash. `gotrace -fixed` therefore sends every probe from one socket. On physical NICs the receive hash is computed from the headers, so a fixed 5-tuple is enough there.

**Policy routing, an asymmetric path, and a stateful firewall.**

```
check 14/28: policy routing: traffic FROM 198.18.10.20 uses table 100, whose default route is r9
  | 0:	from all lookup local
  | 100:	from 198.18.10.20 lookup 100
  | 32766:	from all lookup main
  | 32767:	from all lookup default
  | 198.18.14.10 from 198.18.10.20 via 198.18.10.2 dev eth0 table 100 uid 0
  | 198.18.14.10 via 198.18.10.1 dev eth0 src 198.18.10.10 uid 0
check 15/28: asymmetric path: the SYN goes via r9 to the server's eth1, the SYN-ACK leaves eth0 toward r1 …
  | curl: (28) Connection timed out after 3002 milliseconds
  | IP 198.18.14.10.8080 > 198.18.10.20.44454: Flags [S.], seq 639037372, ack 2590055210, …
  | IP 198.18.14.10.8080 > 198.18.10.20.44454: Flags [S.], seq 639037372, ack 2590055210, …   (retransmitted)
check 16/28: … where r1's stateful firewall has never seen the SYN, calls the SYN-ACK INVALID, and drops it
  |        4      240 DROP       0    --  *      *       0.0.0.0/0            0.0.0.0/0            ctstate INVALID
check 18/28: the same request from 198.18.10.20 now works
  | 200 in 0.001136s
```

The same client, the same server: from 198.18.10.20 the request goes out via r9, but the server answers using its own default route, via r1. r1's firewall sees a SYN-ACK for a connection it never saw start, classifies it `INVALID`, and drops it; the server keeps retransmitting the SYN-ACK into the void. This is the most common way asymmetric routing breaks things in real networks — a firewall, a NAT gateway, or a cloud security appliance sits on only one of the two paths. Check 17's fix is SNAT on r9: the server now replies to r9, so both directions take one path. The alternative fix is policy routing on the server (reply from eth1's address via eth1), which keeps the client's address visible.

**DNAT and conntrack.**

```
check 20/28: the client asks r1; the server receives a SYN addressed to itself, still from the client's real address
  | IP 198.18.10.10.34370 > 198.18.11.10.8080: Flags [S], …
check 21/28: conntrack holds both tuples: what the client sent, and what the reply must look like
  | tcp 6 119 TIME_WAIT src=198.18.10.10 dst=198.18.10.1 sport=34370 dport=8080 src=198.18.11.10 dst=198.18.10.10 sport=8080 dport=34370 [ASSURED]
```

Read a conntrack line as two tuples. The first is the packet as it arrived (client → r1:8080); the second is the reply r1 expects (server → client). The difference between "dst of the first" and "src of the second" is the translation: 198.18.10.1 became 198.18.11.10. `TIME_WAIT` here is conntrack's own timer (120 s), separate from the endpoints' TCP TIME_WAIT.

**SNAT port exhaustion.**

```
check 24/28: conntrack shows the four translations: server replies to 198.18.11.1:40000–40003
  | tcp 6 431998 ESTABLISHED src=198.18.10.10 dst=198.18.11.10 sport=60770 dport=8090 src=198.18.11.10 dst=198.18.11.1 sport=8090 dport=40001 [ASSURED]
  |   … dport=40000 …  dport=40002 …  dport=40003 …
check 25/28: a fifth connection cannot get a port: its SYN is dropped and the connect times out
check 26/28: the evidence on the NAT box: conntrack insert failures (summed across CPUs)
  | cpu=0   	found=0 invalid=6 insert=0 insert_failed=1 drop=1 …
  | cpu=1   	found=0 invalid=0 insert=0 insert_failed=2 drop=2 …
  | insert_failed total: 3
check 28/28: the next connection succeeds at once
```

This is NAT gateway port exhaustion in miniature: four ports, four long-lived connections, and the fifth connection's SYN — sent three times in 3 s — is dropped each time (`insert_failed=3`). The client only sees a connect timeout; the evidence is on the NAT device (here `conntrack -S`; on AWS the `ErrorPortAllocation` metric). Real NAT gateways have tens of thousands of ports, but they are counted *per destination*, so a service that opens a new connection for every request to one API endpoint gets there. While building the lab, starting the four holding connections at the same instant made one of them lose a port race (`insert_failed=1` before the test began), so the lab starts them 0.3 s apart.

---

### Lab Part 2: OSPF and Convergence

[`ospf.json`](../labs/netlab/ospf.json) builds two equal-cost paths between two hosts and runs FRR's `zebra` and `ospfd` on each router (each in its own namespace, configured from `/etc/frr/<router>/ospfd.conf`, inspected with `vtysh -N <router>`):

```
                 ┌── r2 ──┐
  ha ── r1 ──────┤        ├────── r4 ── hb
  .21.10         └── r3 ──┘           .26.10
          all router links: OSPF point-to-point, hello 1 s, dead 4 s
```

```bash
sudo ./bin/netlab run ../netlab/ospf.json        # about 45 s; 11 checks
```

```
check 1/11: OSPF adjacencies come up on both router links (hello 1 s, dead 4 s, point-to-point)
  | Neighbor ID     Pri State           Up Time         Dead Time Address         Interface
  | 10.0.0.2          1 Full/-          0.334s             3.671s 198.18.22.2     eth1:198.18.22.1
  | 10.0.0.3          1 Full/-          0.335s             3.686s 198.18.23.2     eth2:198.18.23.1
  | full adjacencies: 2
  PASS after 2.37s
check 2/11: r1 learned hb's network from OSPF, with two equal-cost next hops
  | Routing entry for 198.18.26.0/24
  |   Known via "ospf", distance 110, metric 30, best
  |   * 198.18.22.2, via eth1, weight 1
  |   * 198.18.23.2, via eth2, weight 1
  | 198.18.26.0/24 nhid 21 proto ospf metric 20
  | 	nexthop via 198.18.23.2 dev eth2 weight 1
  | 	nexthop via 198.18.22.2 dev eth1 weight 1
  PASS after 9.77s
check 6/11: silent failure: that router keeps its links up but stops forwarding and stops OSPF; …
  | lost 66 of 200 probes = about 3.30 s without connectivity
check 11/11: loud failure: that router's link to r1 goes DOWN; r1 sees the carrier loss and reroutes at once
  | lost 0 of 100 probes = about 0.00 s without connectivity
```

The metric 30 is the sum of three interface costs of 10; both paths cost the same, so OSPF installs both and the kernel's ECMP does the rest (check 2's `nexthop` lines; `nhid` is the kernel's nexthop-object ID). Check 5 finds which router this host pair's traffic uses — with the default hash policy, all of it uses one — and checks 6 and 11 break *that* router two ways while 20 pings a second measure the outage. Across four runs the silent failure cost 3.15–3.40 s of traffic, always under the 4 s dead interval (the last hello arrived somewhere in the second before the failure), and the link-down cost 0–1 probe. With default timers (dead 40 s), the silent case would be ten times longer.

---

### Lab Part 3: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/routing.json
alias lab='sudo ./bin/netlab exec ../netlab/routing.json'
```

1. **Metric failover that is not failover.** `lab client -- ip route add default via 198.18.10.2 metric 200`; `ip route get 198.18.11.10` still uses r1. Now make r1 "die" without the client noticing: `lab r1 -- ip link set eth0 down`. The client keeps using the metric-0 route and fails (`ip route get` still says via 198.18.10.1). Only deleting that route moves traffic to r9.
2. **Hash policy 0.** `lab r1 -- sysctl -w net.ipv4.fib_multipath_hash_policy=0` and repeat check 11: all ten flows take one next hop, because only the addresses are hashed.
3. **The other asymmetry fix.** Remove r9's MASQUERADE rule and instead give the server a rule for its eth1 address: `lab server -- ip rule add from 198.18.14.10 lookup 200; lab server -- ip route add default via 198.18.14.2 table 200`. The request from 198.18.10.20 works again, and the server sees the client's real address.
4. **Reverse-path filtering.** On the server, `sysctl -w net.ipv4.conf.all.rp_filter=1 net.ipv4.conf.eth1.rp_filter=1`, remove the rule from exercise 3, and capture on eth1: the SYN from 198.18.10.20 now arrives and is dropped before any reply, because the server's route back to that source leaves via eth0. `nstat -az TcpExtIPReversePathFilter` counts these drops.
5. **Conntrack's view of everything.** `lab r1 -- conntrack -E` in one terminal while you run curls: NEW, UPDATE, DESTROY events as they happen.

`sudo ./bin/netlab down ../netlab/routing.json` removes the lab. For OSPF: `sudo ./bin/netlab up ../netlab/ospf.json`, then `sudo ip netns exec nl-ospf-r1 vtysh -N r1` gives you FRR's router shell — try `show ip ospf neighbor`, `show ip ospf database`, and `conf t` → `interface eth1` → `ip ospf cost 50` to watch ECMP collapse to one path.

---

### Lab Part 4: Your Own Machine

```bash
ip route show table all | head -30      # main, local, and any policy tables
ip rule                                  # more than three rules? something added policy routing
ip -d route show default                 # who installed your default route (proto dhcp/static/ra)
ip route get 1.1.1.1                     # next hop and source for internet traffic
sysctl net.ipv4.ip_forward net.ipv4.fib_multipath_hash_policy
sudo conntrack -C 2>/dev/null; cat /proc/sys/net/netfilter/nf_conntrack_max 2>/dev/null
```

On a Kubernetes node, `ip rule` and `ip route show table all` reveal how the CNI routes pods; on a laptop with a VPN client, the VPN's routes and rules appear and disappear as it connects.

---

## Real-World Troubleshooting Scenario

### Scenario: "Application Containers Can Reach the Internet But Cannot Reach the On-Prem Database"

**The situation:**

A company runs an application on ECS (AWS Fargate) in a VPC with CIDR `10.0.0.0/16`. App containers are in private subnets `10.0.10.0/24` and `10.0.11.0/24`. The VPC has a NAT Gateway in a public subnet, and the private subnet route table has `0.0.0.0/0 → NAT Gateway`. Everything works: containers can reach the internet, pull Docker images, call external APIs.

The company adds a Site-to-Site VPN to connect the VPC to their on-premises data center at `10.100.0.0/16`. The on-prem database is at `10.100.5.20`. After the VPN is configured, the containers cannot reach the database. The VPN tunnel status shows "UP" in the AWS console. The on-prem team confirms the VPN is established and they can see routes to `10.0.0.0/16`.

**The diagnosis — step by step:**

**Step 1 — Check the VPC route table:**

```bash
aws ec2 describe-route-tables --filters \
  "Name=association.subnet-id,Values=subnet-private-a" \
  --query 'RouteTables[0].Routes[*].[DestinationCidrBlock,GatewayId,NatGatewayId,State]' \
  --output table
```

```
|   0.0.0.0/0    |  None  |  nat-0abc123  |  active  |
|  10.0.0.0/16   |  local |  None         |  active  |
```

**Root cause found:** There is no route for `10.100.0.0/16`. The VPN connection was configured, and the Virtual Private Gateway was attached to the VPC — but **route propagation was not enabled** on the private subnet's route table. The VPN-learned routes are not appearing.

**Step 2 — Verify with `ip route get` from inside a container:**

```bash
# Exec into the container task (Fargate supports ECS Exec)
aws ecs execute-command --cluster prod --task <task-id> --container app \
  --command "/bin/sh" --interactive

# Inside the container:
ip route get 10.100.5.20
# Result: 10.100.5.20 via 10.0.10.1 dev eth0 src 10.0.10.50
# The container's own table only knows its subnet gateway. The VPC
# route table decides next: 10.100.5.20 matches only 0.0.0.0/0, so the
# packet goes to the NAT gateway, which has nowhere useful to send a
# private destination. (Minimal images may lack `ip`; `cat
# /proc/net/route` shows the same table in hex.)
```

**Step 3 — The fix:**

Option A: Enable route propagation on the private subnet's route table so VPN-learned routes appear automatically.

Option B: Add a static route manually:

```bash
aws ec2 create-route --route-table-id rtb-privatesubnet \
  --destination-cidr-block 10.100.0.0/16 \
  --gateway-id vgw-vpngateway
```

**After the fix:**

```
Route table now shows:
|   0.0.0.0/0    |  None         |  nat-0abc123  |  active  |
|  10.0.0.0/16   |  local        |  None         |  active  |
|  10.100.0.0/16 |  vgw-0def456  |  None         |  active  |  ← NEW
```

**Why routing knowledge was essential:**

1. Knowing that the route table determines where packets go — not just "the VPN is UP" — pointed directly at the route table as the first thing to check.
2. Understanding Longest Prefix Match explained why internet traffic worked (matched `0.0.0.0/0 → NAT Gateway`) but VPN traffic did not (also matched `0.0.0.0/0` instead of a specific VPN route that did not exist).
3. The container lookup established only its OS next hop. The effective cloud
   subnet route table established NAT versus VPN selection. If no explicit subnet
   association exists, inspect the VPC main table; also verify the return route.

---

## Common Failure Patterns

| Symptom                                         | Routing-Related Cause                                       | How to Confirm                                        | Fix                                             |
| ----------------------------------------------- | ----------------------------------------------------------- | ----------------------------------------------------- | ----------------------------------------------- |
| "Network is unreachable"                        | No matching route in the routing table                      | `ip route get <dest>` returns error                   | Add the missing route                           |
| Ping works one way only                         | Return route is missing (asymmetric failure)                | tcpdump on both ends — requests arrive, replies don't | Add return route on the remote host             |
| New subnet unreachable via VPN                  | VPN route not propagated to VPC route table                 | Check cloud route table for the destination prefix    | Enable route propagation or add static route    |
| Internet works, VPN destination doesn't         | Default route sends VPN traffic to NAT GW instead of VPN    | `ip route get <vpn-dest>` shows NAT GW as next hop    | Add specific route for VPN CIDR via VPN gateway |
| Intermittent new connection failures            | Conntrack table full — NAT can't track new flows            | `dmesg \| grep conntrack` for "table full"            | Find the flow source first; then size nf_conntrack_max |
| New connections to one API time out under load, others fine | SNAT port exhaustion (per destination)     | NAT metrics (`ErrorPortAllocation`), `conntrack -S` insert_failed | Reuse connections; add NAT IPs                  |
| Works from one source address, times out from another | Asymmetric path through a stateful firewall or NAT   | Capture both sides: SYN arrives, SYN-ACK leaves by another path; INVALID drop counters | Policy-route replies, or SNAT so both directions share a path |
| "Permission denied" / "Invalid argument" on connect | A prohibit / blackhole route (or a local firewall)      | `ip route get <dest>` shows the route type            | Remove or narrow the route                      |
| Remote allowlist rejects you after adding an address | Kernel picked a different source address             | `ip route get <dest>` shows `src`                     | Set `src` on the route, or bind the application |
| Some connections fail, retries succeed          | One of several ECMP paths is broken                         | Per-flow tests (`gotrace -fixed`, different source ports); compare paths | Remove the bad path from the ECMP group         |
| Specific host unreachable, rest of subnet works | Stale /32 host route overriding the correct route (LPM)     | `ip route show \| grep <host>` — look for /32 entries | Delete the stale /32 route                      |
| Container cannot reach host network             | Container's default route missing or incorrect              | `ip route show` inside container                      | Check Docker/K8s CNI configuration              |
| Two networks with overlapping CIDRs             | The same addresses exist on both sides; cloud peering refuses it, VPNs route only one side | Route tables on both sides; LPM decides which wins | Re-address, or NAT between them as a stopgap    |

---

## Summary

- Routing is the process of forwarding packets between networks using the routing table. Every packet is forwarded hop by hop — no router knows the full path, only the next step.
- The routing table maps destination prefixes to next-hop gateways and output interfaces. The kernel selects the best route using **Longest Prefix Match** — the most specific matching route always wins.
- `ip route get <dest>` is the single most useful routing diagnostic command. It tells you exactly which route, gateway, interface, and source IP the kernel will use for a given destination.
- Static routes are simple and predictable but do not adapt to failures. Dynamic routing protocols (OSPF, BGP) enable automatic failover and scaling.
- Linux stateful NAT uses conntrack; SNAT changes source, DNAT changes destination,
  and MASQUERADE derives a source from the egress interface when making a mapping.
- Conntrack table exhaustion is a common production failure: the NAT device's connection table fills up and new connections are silently dropped while existing connections continue to work.
- Policy routing allows multiple routing tables selected by source address or other criteria. It is essential for multi-homed hosts, and it creates asymmetric paths when only one side uses it — which stateful firewalls and NATs on one of the paths then break (the lab drops a SYN-ACK as INVALID).
- ECMP hashes flows, not packets: one broken path breaks some connections. Linux hashes addresses only by default (`fib_multipath_hash_policy=0`); traceroute shows several routers at one hop because each probe is a new flow.
- Route types and errors: no route → `Network is unreachable`; `unreachable` route → `No route to host`; `prohibit` → `Permission denied`; `blackhole` → `Invalid argument`. `ip route get` names the cause in one line.
- NAT gateways run out of ports per destination; the client sees only connect timeouts, the evidence is on the NAT device.
- A Linux host with `net.ipv4.ip_forward=1` is a router. Docker and Kubernetes enable this automatically.

## What's Next

Module 9 covers BGP, Anycast, and Internet Routing — how traffic finds its way between autonomous systems, how the internet's routing table is built through BGP route advertisements, and how anycast enables services like 1.1.1.1 and 8.8.8.8 to be served from the nearest location worldwide.

---

## Quick Reference Card

```
MODULE 8: ROUTING — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

ROUTING TABLE COMMANDS
  ip route show                       View all routes
  ip route show table all             All tables including policy routing
  ip route get <dest>                 Which route would be used (THE key command)
  ip route add <net> via <gw>         Add a static route
  ip route add default via <gw>       Set default gateway
  ip route del <net>                  Delete a route
  ip route replace <net> via <gw>     Update a route in place
  ip route flush cache                Clear cached exceptions (PMTU, redirects)
  ip route get <d> from <src>         Decision for another source (policy routing)
  ip route get <d> iif eth0 ipproto tcp sport N dport M   As forwarded; ECMP choice
  ip -d route                         Shows hidden fields (proto boot, type unicast)
  ip route add blackhole|unreachable|prohibit <net>       Refusing routes

LONGEST PREFIX MATCH
  /32 beats /24 beats /16 beats /8 beats /0
  Most specific match always wins — even if its next hop is dead.
  0.0.0.0/0 (default) matches everything — lowest priority.
  Tie on prefix length → lowest metric. Several next hops → ECMP hash.

IP FORWARDING
  sysctl net.ipv4.ip_forward                   Check state
  sudo sysctl -w net.ipv4.ip_forward=1         Enable (temporary)
  echo "net.ipv4.ip_forward = 1" >> /etc/sysctl.d/99-routing.conf  (permanent)

NAT WITH IPTABLES
  # SNAT (static public IP):
  iptables -t nat -A POSTROUTING -s 10.0.1.0/24 -o eth0 -j SNAT --to-source 203.0.113.5

  # MASQUERADE (dynamic public IP):
  iptables -t nat -A POSTROUTING -s 10.0.1.0/24 -o eth0 -j MASQUERADE

  # DNAT (port forwarding):
  iptables -t nat -A PREROUTING -i eth0 -p tcp --dport 8080 -j DNAT --to-destination 10.0.1.50:80

  # View NAT rules:
  iptables -t nat -L -n -v

CONNTRACK
  conntrack -L                     List all tracked connections
  conntrack -C                     Count entries
  cat /proc/sys/net/netfilter/nf_conntrack_max    Configured table limit
  dmesg | grep conntrack           Check for "table full" errors
  conntrack -S                     Per-CPU counters: insert_failed, drop
  conntrack -E                     Live events (NEW / UPDATE / DESTROY)
  sysctl -w net.netfilter.nf_conntrack_max=524288    Increase max (memory cost)

POLICY ROUTING
  ip rule show                     View all routing rules
  ip rule add from <src> lookup <table> priority <N>
  ip route show table <table>      View a specific table
  ip route add <net> via <gw> table <table>

TROUBLESHOOTING SIGNALS
  "Network is unreachable"         No route. Check ip route show.
  Ping works one way only          Return route missing (asymmetric).
  New connections fail, old work   Conntrack table full.
  VPN up but destination           Route for VPN CIDR missing from
  unreachable                      route table (propagation not enabled).
  /32 route overriding             Stale host route. Delete it.
  Works from one source, not other Asymmetric path + stateful device.
  "Permission denied" on connect   prohibit route or local firewall.

CLOUD NAT FAILURE MODES
  SNAT port exhaustion             Too many connections per destination.
  Conntrack overflow               Same as Linux — table full.
  Route propagation disabled       VPN routes don't appear in VPC route table.
```

---

## References

- RFC 791 — Internet Protocol — routing and forwarding
- RFC 1812 — Requirements for IP Version 4 Routers
- RFC 2328 — OSPF Version 2
- RFC 4271 — A Border Gateway Protocol 4 (BGP-4) — covered in Module 9
- `man ip-route` — Linux routing table management
- `man ip-rule` — Linux policy routing
- `man iptables` — Linux netfilter firewall and NAT
- `man conntrack` — Connection tracking tool
- Linux kernel documentation: `Documentation/networking/ip-sysctl.rst` — routing sysctls
- AWS VPC Route Tables documentation; AWS NAT gateway quotas and the `ErrorPortAllocation` metric
- RFC 7868 — Cisco's Enhanced Interior Gateway Routing Protocol (EIGRP)
- RFC 2992 — Analysis of an Equal-Cost Multi-Path Algorithm
- `man ip-route` sections on `nexthop`, route types, and `ip route get` flow selectors
- Course lab: [`netlab/routing.json`](../labs/netlab/routing.json); [`gotrace`](../labs/go/cmd/gotrace) `-fixed` for single-flow traces
