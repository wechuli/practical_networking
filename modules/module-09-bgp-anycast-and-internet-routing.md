# Module 9: BGP, Anycast & Internet Routing

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 8 — Routing
> **Estimated time:** 3–4 hours (the main lab runs offline: FRR routers in network namespaces)

## Overview

Every time you make an HTTPS request to a service hosted in AWS, GCP, or Azure, your packets cross multiple independently operated networks — your ISP, one or more transit providers, possibly an Internet Exchange Point, and finally the cloud provider's network. These networks have no central authority telling them how to reach each other. Instead, they negotiate reachability dynamically using BGP — the Border Gateway Protocol — a path-vector routing protocol that is, without exaggeration, the protocol that holds the internet together.

BGP is how every network on the internet advertises which IP addresses it owns. It is how your ISP knows that traffic destined for `13.107.42.14` (a Microsoft IP) should be sent toward Microsoft's network. It is how Cloudflare advertises `1.1.1.1` from data centers on every continent simultaneously (anycast). And it is how a single misconfiguration can redirect a large share of traffic through a network that was never meant to carry it — as in June 2019, when routes leaked by a small Pennsylvania company's network were accepted and propagated by a large transit provider, disrupting traffic to major services for hours.

You will not configure BGP in most roles. But you need to understand it to diagnose latency spikes caused by sub-optimal BGP path selection, to understand why a cloud region is suddenly unreachable (BGP session dropped), and to recognize when a BGP incident is affecting your infrastructure.

This module covers BGP at the level a support, development, or cloud engineer needs: how routes are advertised and selected, what AS-PATH means, how anycast works, what Internet Exchange Points do, how cloud interconnects (Direct Connect, ExpressRoute) use BGP, and how to use public BGP tools to investigate routing issues.

## What You Will Build Toward

- Explain what an Autonomous System is and why BGP exists
- Describe how BGP advertises routes between networks and how best-path selection works
- Understand anycast: one IP at many locations, routing to the nearest
- Explain what an Internet Exchange Point does and why it matters for latency
- Describe AWS Direct Connect, Azure ExpressRoute, and GCP Cloud Interconnect at a functional level
- Use BGP looking glass tools and traceroute to investigate internet routing paths
- Run BGP yourself (FRR) and watch best-path selection, LOCAL_PREF, prepending, anycast failover, and a prefix hijack happen

---

## 9.1 BGP Basics

### What This Is

BGP — Border Gateway Protocol (RFC 4271, currently version 4) — is the routing protocol used between autonomous systems on the internet. It is the only Exterior Gateway Protocol (EGP) in use today. BGP runs over a TCP connection to port 179, so a BGP session is just a long-lived TCP connection: it fails like one, and a firewall that blocks 179 or an MTU problem on the path breaks it like any other TCP flow. Every ISP, cloud provider, CDN, enterprise with its own IP space, and content provider runs BGP to exchange routing information with their peers.

### Autonomous Systems

An Autonomous System (AS) is a network or collection of networks under a single administrative authority that presents a common routing policy to the internet. Every AS is identified by a unique AS Number (ASN).

```
AUTONOMOUS SYSTEMS — THE BUILDING BLOCKS OF THE INTERNET

  The internet is not one network. In July 2026 about 79,000 autonomous
  systems appeared in the global IPv4 routing table, announcing about
  1.06 million prefixes (APNIC's weekly routing table report).

  Examples of well-known ASNs:
  ASN        Organization
  ─────────  ──────────────────────
  AS15169    Google
  AS16509    Amazon (AWS)
  AS8075     Microsoft (Azure)
  AS13335    Cloudflare
  AS32934    Facebook (Meta)
  AS7018     AT&T
  AS3356     Lumen (Level 3)
  AS174      Cogent Communications

  ASNs are assigned by Regional Internet Registries (RIRs):
  ARIN (North America), RIPE NCC (Europe/Middle East),
  APNIC (Asia-Pacific), LACNIC (Latin America), AFRINIC (Africa).

  Types:
  - 16-bit ASNs: 1 – 65534 (original space, effectively exhausted)
  - 32-bit ASNs: 65536 – 4294967294 (RFC 6793; what new networks get)
  - Private ASNs: 64512–65534 (16-bit) and 4200000000–4294967294 (32-bit)
    Used internally (Kubernetes CNIs, cloud VPN gateways), never on the
    public internet. AWS's default Amazon-side ASN is 64512.
  - Documentation ASNs: 64496–64511 and 65536–65551 (RFC 5398) — the
    examples in this module use them.
  - AS 23456 (AS_TRANS) appears where an old 16-bit-only router stands
    in for a 32-bit ASN.
```

### iBGP vs eBGP

```
iBGP vs eBGP

  eBGP (External BGP)                    iBGP (Internal BGP)
  ────────────────────                   ────────────────────
  Between DIFFERENT ASes                 Within the SAME AS
  Peers are in different organizations   Peers are in the same organization
  TTL = 1 by default (neighbors must     TTL = 255 (can peer across the
    be directly connected)                  internal network — multihop)
  AS-PATH is modified (prepend own ASN)  AS-PATH is NOT modified
  Learned routes are advertised to       Learned routes are NOT re-advertised
    other eBGP and iBGP peers             to other iBGP peers (split horizon)
                                          — requires route reflectors or full mesh

  ┌────────────────────────────────────────────────────────────┐
  │                                                            │
  │  AS 64501                          AS 64502                │
  │  ┌────────────┐   eBGP session    ┌────────────┐         │
  │  │ Router A   │◄─────────────────▶│ Router C   │         │
  │  └─────┬──────┘                    └────────────┘         │
  │        │ iBGP                                             │
  │  ┌─────┴──────┐                                           │
  │  │ Router B   │                                           │
  │  └────────────┘                                           │
  │                                                            │
  │  Router A and Router B: iBGP (same AS)                    │
  │  Router A and Router C: eBGP (different ASes)             │
  └────────────────────────────────────────────────────────────┘
```

### How BGP Advertises Routes

BGP routers exchange **route announcements** (also called route advertisements or UPDATE messages). A route announcement says: "I can reach IP prefix X, and the path to get there through me is [list of ASNs]."

```
BGP ROUTE ADVERTISEMENT

  AS 64501 owns IP prefix 203.0.113.0/24.
  It advertises this prefix to its neighbor AS 64502:

  UPDATE message:
    NLRI (prefix):   203.0.113.0/24
    AS-PATH:         [64501]
    NEXT-HOP:        198.51.100.1 (Router A's interface toward Router C)

  AS 64502 receives this and now knows:
  "To reach 203.0.113.0/24, send traffic to 198.51.100.1.
   The path traverses AS 64501."

  If AS 64502 re-advertises this to AS 64503:
    NLRI:            203.0.113.0/24
    AS-PATH:         [64502, 64501]        ← AS 64502 prepends its own ASN
    NEXT-HOP:        (Router C's interface toward AS 64503)

  AS 64503 now knows:
  "To reach 203.0.113.0/24, the path goes through AS 64502
   then AS 64501. Two hops."
```

### BGP Best-Path Selection

When a BGP router receives multiple routes to the same prefix from different peers, it must choose the best one. BGP uses a well-defined decision process:

```
BGP BEST-PATH SELECTION ALGORITHM (simplified)

  Given multiple routes to the same prefix, evaluate in order:

  Step  Attribute          Prefer              Controlled By
  ────  ─────────────────  ──────────────────  ──────────────────────
   1    LOCAL-PREF         Highest value       Local network admin
                                                (iBGP only, not sent via eBGP)
                                                Use: prefer one ISP over another

   2    AS-PATH length     Shortest path       Originating AS
                           (fewest ASes)        Use: natural distance metric
                                                Can be manipulated by AS-PATH prepending:
                                                advertising [64501 64501 64501] makes
                                                your route look 3 hops long

   3    ORIGIN type        IGP > EGP > ?       Originating router
                                                (Rarely a deciding factor)

   4    MED                Lowest value        Neighboring AS
        (Multi-Exit Disc)                       Use: tell a neighbor "prefer this
                                                peering link over that one"
                                                Only compared between routes from
                                                the same neighbor AS

   5    eBGP over iBGP     eBGP preferred      Topology
                                                (prefer externally learned routes)

   6    IGP metric         Lowest metric to    Internal routing (OSPF/IS-IS)
        to NEXT-HOP        reach the BGP       Use: closest exit (hot-potato routing)
                           next-hop

   7    Oldest eBGP path   Most stable         Avoids flapping between
                                                equal paths (FRR, Cisco)

   8    Router ID          Lowest              Tiebreaker
        (or peer IP)

  Before step 1, many implementations check reachability of the NEXT-HOP,
  a vendor "weight" (Cisco, local only), and prefer locally originated
  routes. In practice, most path decisions are made at steps 1-2:
  LOCAL-PREF (admin preference) and AS-PATH length (shortest path).

  BGP chooses among routes for the SAME prefix. A more specific prefix is
  a different route, and the forwarding table's longest prefix match
  prefers it before any of these attributes are compared — the reason a
  hijacked /25 beats a legitimate /24 (the lab demonstrates it). FRR
  prints the deciding step next to the winner: "best (AS Path)",
  "best (Local Pref)", "best (Older Path)".
```

### Diagram: BGP Path Selection

```
EXAMPLE: CHOOSING BETWEEN TWO PATHS

  Your AS (64500) peers with two transit providers:

                    AS 3356 (Lumen)
                   ┌──────────────────┐
  [Your Router] ──▶│ Route to 8.8.8.0/24:  │
  AS 64500         │ AS-PATH: [3356, 15169] │  ← 2 AS hops
                   └──────────────────┘

                    AS 174 (Cogent)
                   ┌──────────────────────┐
  [Your Router] ──▶│ Route to 8.8.8.0/24:  │
                   │ AS-PATH: [174, 1299,  │  ← 3 AS hops
                   │           15169]       │
                   └──────────────────────┘

  Both routes reach 8.8.8.0/24 (Google DNS).
  Assuming LOCAL-PREF is the same for both:

  Winner: AS 3356 path — shorter AS-PATH (2 hops vs 3 hops).
  Traffic to 8.8.8.8 goes via Lumen, not Cogent.

  If you WANT to prefer Cogent, set LOCAL-PREF higher for routes
  from Cogent: LOCAL-PREF 200 (Cogent) vs 100 (Lumen).
  Now Cogent wins at Step 1 regardless of AS-PATH length.
```

### BGP Convergence

When a BGP route is withdrawn (a link fails, a network goes down), all BGP routers that used that route must find an alternative. This process — convergence — is slow compared to IGPs like OSPF. BGP timers are conservative:

```
BGP TIMERS AND CONVERGENCE

  Common defaults: hold 180 s / keepalive 60 s (Cisco IOS, FRR's
  traditional profile), hold 90 s / keepalive 30 s (Junos, and the RFC's
  suggestion). The session uses the LOWER of the two peers' hold times.
  Inspect the negotiated values on the actual peer.

  If no KEEPALIVE is received within the Hold Timer, the BGP session
  is declared dead and all routes from that peer are withdrawn.

  Convergence time:
  - Detection of failure: up to the hold time (the lab: hold 3 s → 2–2.7 s
    of lost traffic; a clean withdrawal → one lost probe at 20/s)
  - Propagation of withdrawal: minutes to tens of minutes
    (every AS must process the withdrawal and recalculate)
  - No universal Internet convergence interval; policy, topology and failure
    detection determine what each observer sees.

  Faster detection:
  - BFD (Bidirectional Forwarding Detection): sub-second failure detection
  - Reduced KEEPALIVE/Hold timers: 3s/10s (common in cloud peering)
  - Graceful Restart: allows a router to restart BGP without
    withdrawing routes during the restart

  Why convergence matters to you:
  During convergence, traffic to affected prefixes may be:
  - Black-holed (no alternative route yet)
  - Routed sub-optimally (using a longer path)
  - Flapping (alternating between paths)
```

### Route Leaks and Hijacks

```
BGP ROUTE LEAK

  A route leak occurs when a network advertises routes it received from
  one peer to another peer in violation of the intended routing policy.

  Normal operation:
  AS 64501 (customer) receives full internet routes from AS 3356 (transit provider).
  AS 64501 should only advertise its OWN prefixes to its other peers.

  Route leak:
  AS 64501 accidentally re-advertises ALL routes from AS 3356 to AS 174.
  Now AS 174 thinks AS 64501 is a path to the entire internet.
  AS 174 typically prefers it — not because of AS-PATH length, but
  because providers give routes learned from CUSTOMERS a higher
  LOCAL_PREF than routes from peers (customers pay; peers don't).
  Internet traffic floods through AS 64501's small network. Their
  routers overload. Traffic is dropped. Outage affects everyone routing
  through AS 174. Leaks that carry more-specific prefixes (for example
  from a route optimizer that splits prefixes) are worse: they win by
  longest prefix match everywhere they are accepted.

  Historical case studies must use the operator's dated postmortem and
  distinguish leaks from withdrawals. The October 2021 Facebook outage
  included route withdrawals; do not label every BGP outage a route leak.
  The route diagrams here are synthetic, not evidence of an operator's policy.


BGP ROUTE HIJACK

  A route hijack occurs when a network advertises IP prefixes it does
  not own. BGP has no built-in way to verify prefix ownership.

  Example:
  AS 12345 (legitimate owner) advertises 203.0.113.0/24.
  AS 99999 (attacker or misconfigured) also advertises 203.0.113.0/24
    - A /25 is more specific where accepted; many Internet networks filter
      IPv4 prefixes longer than /24, so it does not win everywhere.

  Result: traffic intended for AS 12345 goes to AS 99999.

  Defenses:
  - RPKI (Resource Public Key Infrastructure): prefix holders publish
    signed ROAs saying which AS may originate a prefix, and up to what
    prefix length. Routers doing Route Origin Validation (ROV) drop
    "invalid" announcements. A ROA with maxLength /24 makes any /25
    announcement invalid, whoever originates it. What ROV cannot catch is
    a forged-origin hijack: announcing the same /24 with the real origin
    AS placed at the end of the path. Deployment is widespread among large networks but
    not universal; check a prefix's status on bgp.tools or RIPEstat.
    ASPA (an IETF draft) extends validation to the path.
  - IRR (Internet Routing Registry): prefix-to-AS mapping database.
    Used to build prefix filters. Less secure than RPKI (no crypto).
  - Prefix filtering: ASes configure maximum prefix limits and filters
    based on IRR or RPKI data.
```

---

## 9.2 Anycast

### What This Is

Anycast advertises the same service address from multiple locations. Routing policy
selects a path; BGP does not ordinarily measure latency, and the selected location
need not be geographically closest or have the fewest AS hops. The client uses
the same destination address despite potentially different serving instances.

### How Anycast Works

```
ANYCAST — ONE IP, MANY LOCATIONS

  Illustrative deployment: one resolver address advertised from several locations.

  London DC:               Tokyo DC:                São Paulo DC:
  BGP: "1.1.1.0/24         BGP: "1.1.1.0/24         BGP: "1.1.1.0/24
   via AS 13335"            via AS 13335"             via AS 13335"
       │                        │                         │
       ▼                        ▼                         ▼
  ┌──────────┐            ┌──────────┐            ┌──────────┐
  │ CF London│            │ CF Tokyo │            │CF São Paulo│
  │ 1.1.1.1  │            │ 1.1.1.1  │            │ 1.1.1.1   │
  └──────────┘            └──────────┘            └──────────┘

  A user in Paris queries 1.1.1.1:
  → Every site announces the same prefix from the same AS, so the
    AS-PATHs are usually the same length
  → The user's ISP picks by its own policy and tie-breakers: which
    peering link it learned the route on, LOCAL_PREF, the nearest exit
    in its own network (hot potato), even which session is older
  → The query might land in Paris — or in London, or Frankfurt
    (the lab's two-site anycast was decided by "best (Older Path)")

  A user in Osaka queries 1.1.1.1:
  → BGP routes to Tokyo DC
  → Response: sub-3ms latency

  If the London DC goes offline:
  → Cloudflare withdraws the BGP advertisement from London
  → BGP converges: Paris users now route to the next closest DC
    (maybe Frankfurt or Amsterdam)
  → No client configuration change needed — BGP handles failover
```

### Anycast vs Other Addressing Types

```
ADDRESSING TYPES

  Unicast:    One IP → One host.
              Standard addressing. 10.0.1.50 identifies exactly one machine.
              Most traffic on the internet is unicast.

  Broadcast:  One IP → Every host on the network.
              255.255.255.255 or subnet broadcast (e.g., 10.0.1.255).
              Layer 2 only. Not routed. IPv6 has no broadcast.

  Multicast:  One IP → A group of interested hosts.
              224.0.0.0/4 (IPv4) or ff00::/8 (IPv6).
              Subscribers join the group. Used for IPTV, OSPF, PIM.
              Rarely crosses the public internet.

  Anycast:    One IP → The closest (by routing) of many hosts.
              Same IP advertised from multiple locations via BGP.
              Used for DNS resolvers (1.1.1.1, 8.8.8.8), CDNs,
              DDoS mitigation, and globally distributed services.

KEY PROPERTIES OF ANYCAST:
  Routing-policy distribution, not guaranteed geographic or equal load balance
  Failover requires a health-to-route withdrawal mechanism and convergence
  Distributed capacity can help, but a particular site/path can still overload
  ✓ Works well for stateless protocols (DNS over UDP)
  ~ TCP works in practice — large CDNs serve HTTPS over anycast —
    because routes are stable for far longer than most connections. A
    routing change mid-connection sends the flow to a site that has no
    state for it, which answers RST; operators mitigate with stable
    routing and short connections
  ✗ No guarantee of session stickiness across routing changes
```

### Anycast and DNS

The two most widely used public DNS resolvers are both anycast:

- **1.1.1.1** (Cloudflare, AS 13335): see the provider's current deployment map
- **8.8.8.8** (Google, AS 15169): see the provider's current deployment map

For `dig @1.1.1.1 example.com`, measure the observed response and path. Anycast
often helps latency but does not guarantee a nearby site or a particular RTT.
RPKI validates route origin authorization, not the entire AS path or actual routing.

### Anycast for CDNs and DDoS Mitigation

```
ANYCAST FOR DDoS ABSORPTION

  Without anycast:
  Attack traffic (100 Gbps) → [Single Origin Server] → overwhelmed, down

  With anycast:
  Attack traffic distributed across all anycast locations:

  30 Gbps → [London node, 400 Gbps capacity]    ← handles it easily
  25 Gbps → [Frankfurt node, 300 Gbps capacity] ← handles it easily
  20 Gbps → [NYC node, 500 Gbps capacity]       ← handles it easily
  15 Gbps → [Tokyo node, 200 Gbps capacity]     ← handles it easily
  10 Gbps → [São Paulo node, 150 Gbps capacity] ← handles it easily

  Each node absorbs only its local share of the attack.
  Total capacity across all nodes >> attack volume.
  Service remains available.
```

---

## 9.3 Internet Exchange Points (IXPs)

### What This Is

An Internet Exchange Point (IXP) is a physical location — typically a data center or a set of interconnected data centers — where multiple networks (ISPs, CDNs, cloud providers, enterprises) connect to exchange traffic directly. Instead of sending traffic through expensive transit providers, networks peer at an IXP and exchange traffic for free or at a fraction of the transit cost.

### Why IXPs Matter

```
WITHOUT IXP — TRANSIT PATH

  [Your ISP AS 100]                              [Content Provider AS 200]
        │                                               │
        │ $$ pays transit $$                             │ $$ pays transit $$
        ▼                                               ▼
  [Transit Provider AS 3356]      long path      [Transit Provider AS 174]
        │─────────────────────────────────────────────│

  Packets cross multiple networks. Each transit hop adds latency
  and cost. Transit providers charge per-Mbps or per-Gbps.


WITH IXP — DIRECT PEERING

  [Your ISP AS 100]                              [Content Provider AS 200]
        │                                               │
        └─────────────▶ [IXP Switch] ◀─────────────────┘
                        (Layer 2 fabric)

  Packets cross ONE switch. Sub-millisecond latency.
  Cost: IXP membership fee (flat, typically much less than transit).
  Each network has a port on the IXP's Ethernet switch fabric.
  They establish BGP sessions directly across the IXP.


MAJOR IXPs (examples; each publishes live traffic and member counts):
  DE-CIX Frankfurt, AMS-IX Amsterdam, LINX London, IX.br São Paulo,
  Equinix IX (many cities), JPNAP Tokyo; in East Africa, KIXP in
  Nairobi and Mombasa. The largest carry peaks in the tens of Tbps and
  connect around a thousand or more networks. PeeringDB
  (peeringdb.com) lists which networks are present at which IXP —
  the first place to check whether your ISP and a provider can peer
  directly.
```

### How Peering Works at an IXP

```
PEERING SESSION AT AN IXP

  Both networks connect a router to the IXP's switching fabric.
  Each gets an IP from the IXP's peering LAN (e.g., 80.249.208.0/21 for AMS-IX).

  AS 100 router: 80.249.208.10
  AS 200 router: 80.249.208.20

  They establish a BGP session between each other:

  [AS 100 Router]  ──BGP session──  [AS 200 Router]
  80.249.208.10                     80.249.208.20

  AS 200 advertises its prefixes to AS 100 over this session.
  AS 100 can now send traffic for AS 200's IPs directly across the IXP.
  No transit provider involved. Latency: sub-1ms (same building).

  Types of peering:
  - Bilateral: two networks negotiate a private BGP session
  - Multilateral / Route Server: the IXP runs a route server that
    collects and distributes routes from all participants.
    Networks peer with the route server instead of each individual network.
```

---

## 9.4 Cloud Interconnects

### What This Is

A cloud interconnect is a dedicated, private network connection between your on-premises data center and a cloud provider's network. It bypasses the public internet, providing more consistent latency, higher bandwidth, and stronger security guarantees (traffic never traverses public routers).

All three major cloud providers offer this, and all of them use BGP to exchange routes between your network and the cloud VPC.

### Comparison

```
CLOUD INTERCONNECT OPTIONS

  Service              Provider   Connection Type          BGP Used?
  ───────────────────  ────────   ────────────────────     ─────────
  Direct Connect       AWS        Dedicated fiber (1/10/   Yes — BGP between
                                  100 Gbps) or hosted      your router and
                                  via partner               AWS's DX router

  ExpressRoute         Azure      Dedicated fiber (50      Yes — BGP via
                                  Mbps – 100 Gbps) or      Microsoft Enterprise
                                  via exchange provider     Edge (MSEE) routers

  Cloud Interconnect   GCP        Dedicated (10/100 Gbps)  Yes — BGP via
                                  or Partner (50 Mbps –     Cloud Router
                                  50 Gbps)


WHEN TO USE WHICH:

  Public Internet + VPN:
  ✓ Quick to set up (minutes)
  ✓ Encrypted (IPsec)
  ✗ Variable latency and throughput
  ✗ Subject to internet congestion
  ✗ Bandwidth limited per tunnel (AWS Site-to-Site VPN: up to
    1.25 Gbps per tunnel; more with ECMP across tunnels via a
    Transit Gateway)
  Best for: Dev/test, small workloads, budget-conscious

  Cloud Interconnect:
  ✓ Consistent, low latency
  ✓ High bandwidth (up to 100+ Gbps)
  ✓ Traffic stays on a private circuit — which is not encryption:
    use MACsec (where offered) or IPsec over the circuit if required
  ✗ Expensive (monthly port fees + data transfer)
  ✗ Takes weeks to provision (physical fiber)
  ✗ Requires BGP configuration
  Best for: Production, large data transfer, latency-sensitive,
            compliance (data must not traverse public internet)
```

### BGP over Cloud Interconnects

```
AWS DIRECT CONNECT — BGP IN ACTION

  Your Data Center                              AWS VPC
  ┌───────────────┐                            ┌──────────────────┐
  │               │   Direct Connect           │ VPC: 10.0.0.0/16 │
  │ Your Router   │◄─────────────────────────▶│ Virtual Private   │
  │ ASN: 65001    │   (dedicated fiber)        │ Gateway           │
  │               │                            │ ASN: 64512        │
  │ Advertises:   │                            │                   │
  │ 172.16.0.0/12 │   BGP session              │ Advertises:       │
  │ (your on-prem)│◄─────────────────────────▶│ 10.0.0.0/16       │
  │               │                            │ (your VPC CIDR)   │
  └───────────────┘                            └──────────────────┘

  Your router advertises 172.16.0.0/12 to AWS via BGP.
  AWS advertises 10.0.0.0/16 to your router via BGP.

  Result:
  - AWS EC2 instances can reach 172.16.x.x (your on-prem servers)
  - Your on-prem servers can reach 10.0.x.x (AWS instances)
  - Traffic flows over the Direct Connect fiber, not the internet.

  If the Direct Connect link fails:
  - BGP session drops within the hold time, or within about a second
    with BFD (AWS supports BFD on Direct Connect; enable it on your side)
  - Routes are withdrawn
  - If you have a VPN backup, traffic fails over to the VPN
  - Convergence: seconds to minutes depending on BGP timers
```

---

## 9.5 Internet Routing in Practice

### What This Is

This section covers the practical tools you use to investigate how the internet is routing traffic to and from your infrastructure. You will not configure BGP routers, but you will need to read BGP data to diagnose routing issues.

### Tools for Investigating Internet Routing

```
TOOL                      WHAT IT DOES                              ACCESS
────────────────────      ──────────────────────────────────────    ────────────
traceroute / mtr          Maps the hop-by-hop path from your host   Local CLI
                          to a destination. Shows intermediate
                          routers and their ASes.

BGP Looking Glass         Query a remote router's BGP table.         Web-based
                          See how a specific AS sees your prefix.
                          Examples:
                          - route-views.oregon-ix.net
                          - lg.he.net (Hurricane Electric)
                          - bgp.tools

Cloudflare Radar          Route leak and origin-hijack detection,    Web-based
(radar.cloudflare.com)    per-AS and per-prefix routing views.

RIPE RIS Live / bgp.tools Live and historical BGP updates seen by    Web / API
                          public route collectors; RPKI status.

RIPEstat                  Detailed analysis of any IP prefix or AS.  Web-based
(stat.ripe.net)           Shows: routing history, visibility,
                          AS-PATH, RPKI status.

RIPE Atlas                Distributed measurement network.           Web-based
                          Run traceroute, ping, DNS from thousands   (free/paid)
                          of probes worldwide.

whois                     Look up IP prefix ownership and ASN.       CLI
                          whois -h whois.radb.net <prefix>
```

### Reading AS-PATH in Traceroute

```bash
# Use mtr with AS lookup to see which AS each hop belongs to
mtr -n -z -c 30 --report 8.8.8.8
```

```
                                    AS       Loss%  Snt   Avg
 1. 10.0.1.1                       AS???     0.0%   30    0.5
 2. 203.0.113.1                    AS64500   0.0%   30    2.1
 3. 198.51.100.5                   AS3356    0.0%   30    5.4
 4. 72.14.236.109                  AS15169   0.0%   30   10.8
 5. 8.8.8.8                        AS15169   0.0%   30   12.1

Reading this:
  Hop 1: Your local gateway (private IP, no AS)
  Hop 2: Your ISP (AS 64500)
  Hop 3: Transit provider Lumen (AS 3356)
  Hop 4-5: Google (AS 15169) — the destination network

  The path: Your ISP → Lumen (transit) → Google
  A 20 ms jump between hop 3 and hop 4 is consistent with a long link
  between them, but per Module 7 it is not proof: the reply from hop 4
  may return by a different path, and routers answer ICMP slowly.
  The AS of each hop comes from a lookup of its address (Team Cymru),
  so interfaces numbered from another AS's space — common on
  interconnect links — can be labelled with the wrong AS.
```

---

## Hands-On Lab: BGP You Can Break, Then the Real Internet

### Lab Goals

- Bring up eBGP sessions between five autonomous systems and read the BGP table
- Watch best-path selection choose by AS_PATH, then override it with LOCAL_PREF and with prepending
- Measure anycast failover: a clean withdrawal versus a site that silently dies
- Hijack a prefix with a more specific announcement, see the hijacker serve the traffic, and stop it with a filter
- Then map real internet paths, ASNs, and anycast from your own machine

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`) and `frr` installed (`sudo apt-get install frr`; see Module 8 about the idle system service)
- Part 3 needs internet access and `mtr`, `traceroute`, `dig`, `curl`, `whois`

---

### Lab Part 1: Run the BGP Lab

[`bgp.json`](../labs/netlab/bgp.json) builds a small internet with documentation ASNs and addresses, every router running FRR's `zebra` and `bgpd` in its own namespace (configs in `/etc/frr/<router>/bgpd.conf`; keepalive 1 s, hold 3 s so failures show quickly):

```
                                        ┌── t1 AS64510 ──── origin AS64530 ── server 203.0.113.10
  user ── you AS64500 ──┤                  │                   │ announces 203.0.113.0/24
  198.18.30.10          │                  └── anya AS64550    │
                        │                      (anycast site A) │
                        └── t2 AS64520 ──── t3 AS64540 ─────────┘
                               ├── anyb AS64550 (anycast site B)     anycast: 198.51.100.0/24,
                               └── rogue AS64566                     service 198.51.100.53
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/bgp.json        # about 60 s; 26 checks
```

Excerpts from a real run (version numbers and `Last update` lines trimmed):

**Sessions and the table.**

```
check 1/26: eBGP sessions to both transit providers are Established (keepalive 1 s, hold 3 s)
  | Neighbor        V         AS   MsgRcvd   MsgSent   TblVer  InQ OutQ  Up/Down State/PfxRcd   PfxSnt Desc
  | 198.18.31.2     4      64510         2         2        0    0    0 00:00:00            0        0 t1
  | 198.18.32.2     4      64520         2         2        0    0    0 00:00:00            0        0 t2
check 2/26: two paths to the origin's 203.0.113.0/24: via t1 (2 ASes) and via t2 (3 ASes); the shorter AS_PATH is best
  | Paths: (2 available, best #2, table default)
  |   64520 64540 64530
  |     198.18.32.2 from 198.18.32.2 (10.0.1.20)
  |       Origin IGP, valid, external
  |   64510 64530
  |     198.18.31.2 from 198.18.31.2 (10.0.1.10)
  |       Origin IGP, valid, external, best (AS Path)
check 3/26: the best path is what reaches the kernel
  | 203.0.113.0/24 nhid 8 via 198.18.31.2 dev eth1 proto bgp metric 20
check 4/26: the user's traffic crosses t1
  |  1  198.18.30.1  0.083 ms
  |  2  198.18.31.2  0.014 ms
  |  3  198.18.33.2  0.015 ms
  |  4  203.0.113.10  0.032 ms
```

Read a BGP path entry top to bottom: the AS_PATH (`64510 64530`: t1, then the origin, which is always the **last** AS), the next hop and the peer it came from, the attributes, and FRR's reason for the winner. Only the best path goes into the kernel (`proto bgp`), which is all the forwarding plane ever sees. `State/PfxRcd 0` in the summary simply means the snapshot was taken the moment the sessions came up, before any routes arrived.

**Policy beats path length.** Check 5 adds a route map on `you` that sets `local-preference 200` on everything from t2:

```
check 6/26: now the longer path via t2 is best
  |   64520 64540 64530
  |     198.18.32.2 from 198.18.32.2 (10.0.1.20)
  |       Origin IGP, localpref 200, valid, external, best (Local Pref)
  | 203.0.113.0/24 nhid 10 via 198.18.32.2 dev eth2 proto bgp metric 20
```

That is how a network decides its own *outbound* traffic — "use our cheaper provider" — and why public tools cannot tell you why another network routes the way it does: LOCAL_PREF never leaves the AS that set it.

**Prepending: influencing other networks' choices.** The origin cannot set `you`'s LOCAL_PREF, but it can make one path look worse:

```
check 9/26: inbound traffic engineering worked: you now prefer t2 (3 ASes) over t1 (4 ASes)
  |   64520 64540 64530
  |       Origin IGP, valid, external, best (AS Path)
  |   64510 64530 64530 64530
  |       Origin IGP, valid, external
```

The repeated `64530 64530 64530` is the origin prepending its own ASN toward t1. It works only where AS_PATH length decides — any network whose LOCAL_PREF favours t1 ignores it.

**Anycast, and how its failover really behaves.**

```
check 11/26: anycast: 198.51.100.0/24 is announced by two sites of AS64550; you sees two equal-length paths
  |   64520 64550
  |       Origin IGP, valid, external, best (Older Path)
  |   64510 64550
  |       Origin IGP, valid, external
check 12/26: the user reaches whichever site its path leads to
  | X-Served-By: site-B
check 14/26: planned maintenance: the serving site withdraws its announcement; measure the gap
  | lost 1 of 100 probes = about 0.05 s without the service
check 18/26: unplanned failure: that site's router hangs (BGP silent, packets dropped); only the 3 s hold timer notices
  | lost 41 of 200 probes = about 2.05 s without the service
```

Both sites are one AS hop away, so the choice fell to a tie-breaker — the older path — not to distance or load. Across four runs, a withdrawal cost exactly one probe at 20 per second every time; a silent failure cost 1.95–2.70 s, always under the 3 s hold time. With a 180 s default hold time, the same silent failure is a three-minute outage for everyone routed to that site — which is why anycast operators withdraw routes from health checks rather than waiting for BGP to notice.

**A hijack, and the filter that stops it.**

```
check 22/26: you now has both; longest prefix match picks the /25 before BGP attributes matter
  | 203.0.113.0/24 nhid 8 via 198.18.31.2 dev eth1 proto bgp metric 20
  | 203.0.113.0/25 nhid 10 via 198.18.32.2 dev eth2 proto bgp metric 20
  | 203.0.113.10 via 198.18.32.2 dev eth2 src 198.18.32.1 uid 0
check 23/26: the user's request is answered by the hijacker, and nothing looks wrong to the application
  | X-Served-By: ROGUE
check 25/26: the /25 is gone everywhere downstream of t2
  | 203.0.113.10 via 198.18.31.2 dev eth1 src 198.18.31.1 uid 0
check 26/26: and the real server answers again
```

AS64566 announced 203.0.113.0/25 — half of someone else's prefix — and configured 203.0.113.10 on itself. Every router that accepted the /25 sent traffic for 203.0.113.0–127 to it; the request succeeded with a normal HTTP 200, just from the wrong server. Over HTTPS the client would at least see a certificate error, unless the attacker could also obtain a certificate. The fix in check 24 is what transit providers are supposed to do for every customer session: accept only the prefixes the customer is registered to originate (built from IRR records or RPKI ROAs). One filter at t2 protected everyone behind it.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/bgp.json
v() { r=$1; shift; sudo ip netns exec nl-bgp-$r vtysh -N $r "$@"; }   # v you -c 'show bgp summary'
```

1. **Read a whole table.** `v you -c 'show bgp ipv4 unicast'`: `*>` marks the best path, `*` other valid paths. Compare with `v t2 -c 'show bgp ipv4 unicast'` — every AS sees a different internet.
2. **A route leak, built in.** The lab's routers export everything to everyone (`no bgp ebgp-requires-policy`), so `origin` — a customer of t1 and t3 that should only announce 203.0.113.0/24 — passes t1's routes on to t3. Look: `v t3 -c 'show bgp ipv4 unicast 198.51.100.0/24'` lists a second path `64530 64510 64550` through origin. It loses on AS_PATH length. Now give t3 the policy real providers use, preferring customer routes: `v t3 -c 'configure terminal' -c 'route-map CUSTOMER permit 10' -c 'set local-preference 200' -c 'exit' -c 'router bgp 64540' -c 'address-family ipv4 unicast' -c 'neighbor 198.18.35.2 route-map CUSTOMER in' -c 'end' -c 'clear bgp ipv4 unicast 198.18.35.2 soft in'`. The leaked path becomes `best (Local Pref)`, and t3 sends its anycast traffic through origin's small network. That is the June 2019 mechanism in miniature. Fix it where it started, with an export filter on origin that announces only its own prefix (`ip prefix-list OWN seq 5 permit 203.0.113.0/24`, a route map matching it, `neighbor … route-map OWN-ONLY out` toward both providers): t3 is left with one path. FRR enables `bgp ebgp-requires-policy` by default (RFC 8212: no routes are exchanged with an eBGP peer until a policy is configured) precisely to prevent this; the lab turns it off to stay short.
3. **MED.** On origin, `route-map MED-LOW permit 10` / `set metric 50` toward t1 only, and check that it changes nothing at `you`: t1 receives MED 50 (`v t1 -c 'show bgp ipv4 unicast 203.0.113.0/24'`), but MED is not passed beyond the neighbouring AS, and it is compared only between paths from the same neighbouring AS anyway.
4. **Timers are negotiated.** Set `timers bgp 10 30` on `you` only and clear the sessions: `v you -c 'show bgp neighbors 198.18.31.2' | grep -i 'hold time'` prints `Hold time is 3 seconds` under `Configured hold time is 30 seconds` — the session uses the lower of the two peers' values. Set it on t1 and t2 as well and repeat check 18: the silent failure now costs up to 30 s.
5. **What the session is.** `sudo ip netns exec nl-bgp-you ss -tn 'sport = :179 or dport = :179'` — BGP is two TCP connections on port 179, one per neighbour.

`sudo ./bin/netlab down ../netlab/bgp.json` removes the lab.

---

### Lab Part 3 (Internet Required): Investigating Real Routing

**Paths and ASNs.**

```bash
sudo mtr -n -z -c 50 --report 8.8.8.8     # -z: AS of each hop (via Team Cymru DNS lookups)
sudo mtr -n -z -c 50 --report 1.1.1.1
whois -h whois.cymru.com " -v 8.8.8.8"    # IP → origin AS, BGP prefix, registry
whois -h whois.cymru.com " -v $(curl -s https://ifconfig.me)"   # your own public prefix and ISP AS
```

Read the AS column for transitions (your ISP → transit or direct peer → destination AS). Remember the caveats from 9.5: hop AS labels come from address lookups, interconnect interfaces can carry the neighbour's addresses, and RTT jumps are probe evidence, not link measurements.

**How the world sees a prefix.** Look up `8.8.8.0/24` (or your own prefix from the whois above) on `https://bgp.tools/`, `https://bgp.he.net/`, and `https://stat.ripe.net/`: origin AS, visibility across route collectors, AS paths from many vantage points, and RPKI status. Check `https://radar.cloudflare.com/routing` for recent leaks and hijacks.

**Anycast in action.**

```bash
curl -s https://1.1.1.1/cdn-cgi/trace | grep -E '^(colo|ip|loc)='   # colo = the Cloudflare site that answered
dig @1.1.1.1 example.com +stats | grep "Query time"
dig @8.8.8.8 example.com +stats | grep "Query time"
```

`colo` is an airport-style code for the site that served you; compare it with your location and with a friend's, or through a VPN. A distant `colo` is not a fault by itself — it is your ISP's routing policy, the same tie-breaking you saw in check 11.

**DNS-based steering is different.** `dig +short www.google.com @8.8.8.8` and `@1.1.1.1` may return different addresses: that is the authoritative DNS choosing an answer by resolver location, not anycast.

---

## Real-World Troubleshooting Scenario

Complete the [saved routing-observation exercise](../labs/routing.md) before using
a live looking glass. It has a dated, explicitly synthetic dataset and reference
answers, so the learning outcome does not depend on current public routes.

### Scenario: "Our API Latency to US-East Users Doubled Overnight"

**The situation:**

In this synthetic case (documentation ASNs and addresses stand in for the networks involved), a SaaS company hosts its API on AWS in `us-east-1`. Monitoring shows that API response times for customers in the eastern United States increased from ~15ms to ~80ms starting at 2:00 AM. Nothing changed in the application — no deploys, no config changes. The AWS Health Dashboard shows no issues. Customers in Europe and Asia are unaffected. The spike is consistent, not intermittent.

**The BGP diagnosis:**

**Step 1 — Verify from the customer's perspective:**

A customer in New York runs an mtr report:

```bash
sudo mtr -n -z -c 100 --report <api-endpoint-ip>
```

```
HOST:                           AS       Loss%   Avg
 1. 10.0.0.1                    AS???     0.0%    0.4
 2. 198.51.100.1                AS64501   0.0%    3.2  ← the customer's ISP ("ISP-A")
 3. 198.51.100.65               AS64501   0.0%    5.1  ← ISP-A backbone
 4. 192.0.2.57                  AS64502   0.0%   45.8  ← a transit AS ("Transit-B")  ← BIG JUMP
 5. 192.0.2.14                  AS64502   0.0%   47.2  ← Transit-B backbone
 6. 205.251.244.71              AS16509   0.0%   78.5  ← AWS
 7. 52.94.76.1                  AS16509   0.0%   79.2  ← API server
```

**Observation:** This is a synthetic transcript, not a sourced incident. A change
in probe RTTs and interface-AS mapping suggests a path-change hypothesis, but does
not measure the handoff's one-way delay or establish physical geography. Retain
dated before/after traces from the same source and application endpoint.

**Step 2 — Check BGP routing changes:**

The network team checks the RIPE RIS routing history (RIPEstat's BGPlay) and bgp.tools for the API prefix.

Public collectors can reveal announcements and selected paths at their observation
points. They cannot establish ISP-A's private LOCAL_PREF change: that attribute
is not normally exported over eBGP. Confirmation requires the affected operator's
own route/policy history. Do not invent internal configuration from public traces.

**Step 3 — Confirm with a BGP looking glass:**

```
# On the HE looking glass (https://lg.he.net/):
# Query: "show ip bgp <api-prefix>" from a router in New York

# Synthetic observations from this looking glass's own AS:
  Before: AS-PATH 16509
  After:  AS-PATH 64502 16509
# This is not ISP-A's table and does not expose ISP-A's LOCAL_PREF.
```

The changed view is supporting evidence, not proof that an extra AS hop caused
65 ms. Send the operator UTC timestamps, source/destination prefixes, affected and
unaffected paths, application timing, and relevant collector observations.

**Step 4 — Resolution:**

This is not something the SaaS company can fix directly. They open a ticket with AWS and with ISP-A. Options:

- ISP-A reverts the BGP policy change (if it was accidental).
- AWS and ISP-A restore direct peering if the old session or link was decommissioned.
- Global Accelerator can change ingress and backbone use, but ISP routing still
  determines how a client reaches an AWS edge. Measure improvement; do not assume
  a geographically nearest edge or complete bypass of ISP policy.

**Why BGP knowledge mattered:**

- The `mtr -z` output with AS numbers immediately showed the traffic was routing through an unexpected transit AS (AS64502).
- A correlation at an AS transition supports escalation, not precise fault attribution.
- Without understanding BGP, the team could spend days investigating application code, database performance, or AWS infrastructure — none of which had changed. The lab's check 6 is this incident's mechanism: one LOCAL_PREF change, a longer path, nothing visible to the application.

---

## Common Failure Patterns

| Symptom                                                   | BGP/Routing Cause                                  | How to Confirm                                             | Fix / Escalation                                    |
| --------------------------------------------------------- | -------------------------------------------------- | ---------------------------------------------------------- | --------------------------------------------------- |
| Sudden latency increase for specific users                | ISP changed BGP path selection (new transit)       | `mtr -z` shows new AS in path, higher RTT                  | Contact ISP with dated before/after evidence; consider an edge service (Global Accelerator, CDN) |
| Traffic to your prefix answered by someone else           | Hijack: a more specific or forged-origin announcement | bgp.tools / RIPEstat show an unexpected origin or a more specific prefix; Cloudflare Radar alerts | Contact upstreams; publish ROAs with tight maxLength; announce more specifics as a stopgap |
| Anycast service down for one region for minutes           | A site died without withdrawing; waiting for hold timers | Path still points at the dead site until the session drops   | Health-checked withdrawal, BFD, shorter hold times  |
| Region completely unreachable                             | BGP route withdrawal (link down, session drop)     | Prefix disappears from BGP looking glass                   | Wait for convergence; check cloud health dashboards |
| Intermittent unreachability                               | BGP route flapping (session oscillating)           | RIPEstat / RIS Live show rapid announce/withdraw cycles    | Provider must stabilize the peering session         |
| Traffic from one ISP works, another doesn't               | ISP not receiving BGP announcement for your prefix | Check prefix visibility via `stat.ripe.net`                | Verify your prefix is being advertised; check RPKI  |
| Massive latency — traffic routing through another country | BGP route leak or suboptimal path                  | `mtr -z` shows unexpected AS path; looking glass confirms  | Contact transit provider; check Cloudflare Radar for leaks |
| Cloud interconnect (Direct Connect) failover not working  | BGP session not established on backup link         | Check BGP session state on your router                     | Verify BGP config, timers, ASN on backup connection |
| 1.1.1.1 or 8.8.8.8 unreachable                            | Anycast node withdrawn or ISP lost peering         | `traceroute 1.1.1.1` — check if path reaches Cloudflare AS | Switch to different resolver; check ISP peering     |

---

## Summary

- BGP is the single protocol that connects all autonomous systems on the internet. It is a path-vector protocol that exchanges route advertisements between networks. Every IP prefix you reach on the internet is reachable because of BGP.
- An Autonomous System (AS) is a network under one administrative authority, identified by an ASN. About 79,000 ASes appear in the global routing table (July 2026). BGP runs between them (eBGP) and within them (iBGP).
- BGP selects the best path using LOCAL-PREF (admin preference), AS-PATH length (shortest path), MED (neighbor preference), and several tiebreakers. In practice, LOCAL-PREF and AS-PATH decide most path selections.
- BGP convergence is dominated by failure detection: a withdrawal propagates in well under a second in the lab, a silent failure waits for the hold time (default 90–180 s). Across the internet, propagation and route-flap damping add minutes. During convergence, traffic may be black-holed or take suboptimal paths.
- BGP decides among routes for the same prefix; longest prefix match decides first. That is why a more specific announcement (a hijack, a leaked more-specific, or your own traffic engineering) wins everywhere it is accepted.
- Route leaks (accidentally advertising received routes) and route hijacks (advertising prefixes you do not own) are real threats. RPKI provides cryptographic verification of route origins but is not yet universally deployed.
- Anycast advertises the same IP from multiple locations via BGP. Each network's policy and tie-breakers, not distance, decide which site a user reaches. It provides distribution, fault tolerance, and DDoS resilience, and fails over as fast as a site withdraws its route. DNS resolvers (1.1.1.1, 8.8.8.8) and CDNs are the primary users.
- IXPs are physical facilities where networks peer directly, reducing latency and cost. Traffic between two networks at the same IXP crosses one switch instead of multiple transit providers.
- Cloud interconnects (Direct Connect, ExpressRoute, Cloud Interconnect) use BGP to exchange routes between your data center and the cloud. They provide lower latency and more consistent performance than the public internet.
- `mtr -z` (with AS lookup), BGP looking glasses and collectors (bgp.tools, bgp.he.net, RIPEstat), and Cloudflare Radar are your primary tools for diagnosing internet routing problems — remembering that each shows the view from where it sits.

## What's Next

Module 10 covers UDP — the User Datagram Protocol — the simplest transport protocol. Now that you understand how IP delivers packets across the internet (via the routing and BGP mechanisms covered in Modules 8 and 9), the next two modules cover how those packets are used: UDP for fast, connectionless delivery (DNS, DHCP, QUIC) and TCP for reliable, ordered delivery.

---

## Quick Reference Card

```
MODULE 9: BGP, ANYCAST & INTERNET ROUTING — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

BGP BASICS
  Protocol:       Path-vector routing protocol (RFC 4271)
  Port:           TCP 179
  Purpose:        Exchange routing information between autonomous systems
  eBGP:           Between different ASes (TTL=1 default)
  iBGP:           Within the same AS (TTL=255)
  Hold Timer:     90 s or 180 s by platform; the session uses the lower
                  of the two peers' values. Dead if nothing arrives in time.
  Keepalive:      a third of the hold time (30 s or 60 s).
  BFD:            sub-second failure detection alongside BGP.

BGP BEST-PATH SELECTION (in order)
  1. Highest LOCAL-PREF        (admin preference, iBGP only)
  2. Shortest AS-PATH          (fewest AS hops)
  3. Lowest ORIGIN type        (IGP > EGP > Incomplete)
  4. Lowest MED                (neighbor preference, same AS only)
  5. eBGP over iBGP            (prefer external routes)
  6. Lowest IGP metric to NH   (closest exit — hot-potato routing)
  7. Oldest eBGP path          (stability)
  8. Lowest Router ID          (tiebreaker)
  Longest prefix match happens BEFORE all of this: a /25 beats a /24.

WELL-KNOWN ASNs
  AS15169   Google         AS13335   Cloudflare      AS3356   Lumen
  AS16509   Amazon/AWS     AS32934   Meta/Facebook   AS174    Cogent
  AS8075    Microsoft      AS7018    AT&T

ANYCAST
  Same IP advertised from multiple BGP locations.
  Traffic goes to whichever site each network's BGP policy selects
  (often near, never guaranteed).
  Used by: 1.1.1.1 (Cloudflare), 8.8.8.8 (Google), CDNs
  Strength: geo-distribution, DDoS absorption, fault tolerance
  Weakness: TCP sessions can break during BGP changes; a dead site that
            does not withdraw keeps attracting traffic until hold timers expire

CLOUD INTERCONNECTS
  AWS Direct Connect       Dedicated fiber, BGP, 1/10/100 Gbps
  Azure ExpressRoute       Dedicated fiber, BGP, 50 Mbps – 100 Gbps
  GCP Cloud Interconnect   Dedicated or Partner, BGP

INVESTIGATION TOOLS
  mtr -n -z -c 50 --report <host>    Path + AS + loss statistics
  traceroute -n <host>                Hop-by-hop path
  whois -h whois.cymru.com " -v <IP>"  IP → ASN + prefix + owner
  curl https://1.1.1.1/cdn-cgi/trace  Which Cloudflare DC you hit
  dig @1.1.1.1 <domain> +stats        DNS query time (anycast test)
  https://bgp.he.net/                  BGP looking glass (web)
  https://stat.ripe.net/               Routing analysis (web)
  https://bgp.tools/                   Prefix/AS views, RPKI status
  https://radar.cloudflare.com/routing Leaks and hijacks
  https://www.peeringdb.com/           Who peers where (IXPs, facilities)

THREATS
  Route leak:    Accidentally redistributing received routes. Causes
                 traffic floods through unprepared networks.
  Route hijack:  Advertising prefixes you don't own. Redirects traffic.
  Defense:       RPKI ROV (origin + maxLength), IRR-based customer
                 filters, max-prefix limits, RFC 8212 default-deny eBGP.

TROUBLESHOOTING SIGNALS
  mtr shows new AS in path          ISP changed BGP path selection
  Prefix missing from looking glass Route withdrawn — link or session down
  RIS: rapid announce/withdraw      Route flapping — unstable session
  Latency spike for one ISP only    BGP path change at that ISP
  Cloud interconnect failover slow  BGP timers too long — tune Hold/KA
```

---

## References

- RFC 4271 — A Border Gateway Protocol 4 (BGP-4) — the core BGP specification
- RFC 7908 — Problem Definition and Classification of BGP Route Leaks
- RFC 6480 — An Infrastructure to Support Secure Internet Routing (RPKI)
- RFC 4786 — Operation of Anycast Services
- RFC 7947 — Internet Exchange BGP Route Server
- `man mtr` — network diagnostic tool with AS lookup
- `man traceroute` — path tracing
- `man whois` — WHOIS lookup tool
- Hurricane Electric BGP Toolkit — https://bgp.he.net/
- RIPE RIS / RIPEstat — https://stat.ripe.net/
- Team Cymru WHOIS — https://www.team-cymru.com/ip-asn-mapping
- RFC 8212 — Default External BGP (EBGP) Route Propagation Behavior without Policies
- RFC 6811 — BGP Prefix Origin Validation (RPKI ROV)
- RFC 6793 — BGP Support for Four-Octet AS Number Space; RFC 5398 — AS Numbers for Documentation
- RFC 5880 — Bidirectional Forwarding Detection (BFD)
- APNIC weekly routing table report (posted to NANOG and other lists) — table size and AS counts
- PeeringDB — https://www.peeringdb.com/
- FRR documentation — BGP (`bgpd`), route maps, prefix lists
- Course lab: [`netlab/bgp.json`](../labs/netlab/bgp.json)
- Cloudflare Radar — https://radar.cloudflare.com/
