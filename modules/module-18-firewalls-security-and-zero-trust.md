# Module 18: Firewalls, Security & Zero Trust

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 17 — Kubernetes Networking
> **Estimated time:** 4–5 hours

## Overview

Every earlier module met a firewall sooner or later. Docker's published port went straight past one (Module 16), and Kubernetes NetworkPolicy turned out to be a drop rule (Module 17). This module looks at firewalls directly. Support work asks two questions about them:

- **"Is a firewall blocking this?"** The answer is in what the client experiences. Each firewall verdict produces a distinct, measurable symptom:
  - connected;
  - "Connection refused" in a fraction of a millisecond;
  - "No route to host" when routing is fine;
  - a silent timeout.
- **"Why did the firewall allow this?"** The answer is in the rule model:
  - first match wins;
  - stateful rules remember connections, stateless ones don't;
  - a rule written for return traffic can admit traffic that was never a reply.

The second half of the module moves the decision away from addresses:

- **SYN floods**, and what SYN cookies change;
- **rate limits**;
- **WAFs and DDoS protection** at the edge;
- **zero trust**: the request is authorized by a verified identity, wherever it comes from.

All the firewall behaviour here is measured in [`labs/netlab/firewall.json`](../labs/netlab/firewall.json): a client, a firewall routing between three networks, a server and a database.

## What You Will Build Toward

- Recognize ACCEPT, DROP and each REJECT type from the client's side, and know which error message each produces
- Read iptables and nftables rule sets: tables, chains, hooks, first-match order, policies, counters, logging (including why LOG is silent in containers)
- Explain stateful and stateless filtering with AWS security groups and network ACLs as the cloud example, including the hole a stateless return rule opens
- Build tiered segmentation and verify both the allowed and the denied paths
- Explain SYN floods, SYN cookies and connection rate limiting with kernel counters as evidence
- Place WAFs, DDoS protection and zero-trust access in the request path, and authorize by identity with mTLS

---

## 18.1 What a Firewall Verdict Looks Like

The lab gives the server a host firewall with one verdict per port. Here is what a client sees for each (`tcplab dial`, then `nc` for the familiar messages):

| Server rule (INPUT) | Client result (3 runs) | What applications print |
| --- | --- | --- |
| `ACCEPT` (443) | connected in 0.18–0.45 ms | — |
| `REJECT --reject-with tcp-reset` (80) | refused, 0.11–0.36 ms | `Connection refused` |
| `REJECT` (default: ICMP port unreachable) (22) | refused, 0.12–0.30 ms | `Connection refused` |
| `REJECT --reject-with icmp-host-prohibited` (9090) | **unreachable** (EHOSTUNREACH), 0.11–0.28 ms | `No route to host` |
| `REJECT --reject-with icmp-admin-prohibited` (9091) | unreachable, 0.13–0.27 ms | `No route to host` |
| `DROP` (8080) | gave up after the client's 2 s timeout | `timed out` / hangs |

Three lessons:

- **"No route to host" is often a firewall.** The routing was fine; an administratively prohibited ICMP reply produces the same errno as a missing route. firewalld's default zone rejects this way. So "No route to host" on a host that pings fine means look for a REJECT rule.
- **Refused does not mean "nothing is listening".** A REJECT and a closed port look identical to the client. Only the server's counters or a capture tell them apart.
- **DROP looks like a dead host or a lossy path.** Nothing comes back, so the client waits through its SYN retransmissions (Module 11). Firewall designers choose DROP so that scanners learn nothing; support engineers pay for it in timeouts. Inside your own network, REJECT makes failures fast and obvious.

---

## 18.2 iptables and nftables

### The packet path

```
                       ┌──────────── for this host ────────────┐
 in → PREROUTING → routing ─┤                                       ├→ INPUT → local process
      raw, mangle,          └→ FORWARD (mangle, filter) ─┐          │
      nat (DNAT)                                         ↓          │  local process → OUTPUT → routing → POSTROUTING → out
                                                    POSTROUTING ←───┘   (raw, mangle, nat, filter)     (mangle, nat: SNAT)
```

Tables group rules by job:

- `raw`: before conntrack; NOTRACK, and Docker's drops (Module 16);
- `mangle`: marks and header fields;
- `nat`: only the first packet of each connection traverses it;
- `filter`: ACCEPT, DROP, REJECT;
- `security`: SELinux labels.

Within a chain, **rules are evaluated in order and the first terminating match wins**. If none matches, the chain's **policy** applies, which is ACCEPT or DROP for built-in chains.

The lab inserted a DROP for port 443 *above* the existing ACCEPT, and 443 was blocked even though the ACCEPT was still there. `iptables -I` (insert at the top) versus `-A` (append at the end) is the most common way a "correct" rule fails to take effect.

### Counters are the evidence

```
iptables -vnL INPUT
Chain INPUT (policy DROP 2 packets, 120 bytes)
 pkts bytes target  prot opt in out source     destination
    …    …  ACCEPT  6    --  *  *   0.0.0.0/0  0.0.0.0/0   tcp dpt:443
    2  120  REJECT  6    --  *  *   0.0.0.0/0  0.0.0.0/0   tcp dpt:80 reject-with tcp-reset
```

To prove which rule decided a packet, zero the counters (`-Z`), reproduce once, and read them again. The policy's counter is in the chain header.

### iptables is (usually) nftables now

On Ubuntu 22.04 and later, Debian, RHEL 9 and similar, the `iptables` command is **iptables-nft**: it writes nftables rules. `iptables -V` printed `iptables v1.8.10 (nf_tables)`, and `nft list chain ip filter INPUT` showed the same rules in nftables syntax:

```
type filter hook input priority filter; policy drop;
tcp dport 443 counter packets 1 bytes 60 accept
tcp dport 9090 counter packets 2 bytes 120 reject with icmp type host-prohibited
```

Consequences:

- **Use one tool per table.** Tables made by `iptables` (marked "managed by iptables-nft, do not touch") should be changed with `iptables`.
- **Another tool's tables apply too.** Tables created directly with `nft` (firewalld, Docker's nftables backend, your own) are separate, and a packet must pass **all** base chains on a hook, in priority order. A DROP in any one of them wins.
- **Legacy and nft rule sets can coexist** (`iptables-legacy`), which confuses everyone. `iptables-legacy -S` and `nft list ruleset` together show everything.

A native nftables host firewall is the same policy written once for IPv4 and IPv6:

```
table inet host {
  chain input {
    type filter hook input priority 0; policy drop;
    iif lo accept
    ct state established,related accept
    ct state invalid drop
    tcp dport { 22, 443 } accept
    icmp type { echo-request, destination-unreachable, time-exceeded } accept
    icmpv6 type { echo-request, destination-unreachable, packet-too-big, time-exceeded, nd-neighbor-solicit, nd-neighbor-advert, nd-router-advert } accept
    counter reject with icmpx admin-prohibited
  }
}
```

Never drop ICMP wholesale: Module 14's black hole was exactly that. IPv6 also needs neighbour discovery.

### Logging, and why it is silent in containers

```bash
iptables -I INPUT 3 -p tcp --dport 8080 -j LOG --log-prefix "COURSE-DROP "
```

In the lab, a packet that matched this rule produced **no log line** (`new COURSE-DROP log lines: 0`). The kernel ignores LOG in every network namespace except the host's unless `net.netfilter.nf_log_all_netns=1`, which is off by default. That covers every container, pod and lab namespace. With the sysctl set on the host, the same packet logged:

```
COURSE-DROP IN=eth0 OUT= MAC=… SRC=198.18.180.10 DST=198.18.181.10 LEN=60 … PROTO=TCP SPT=… DPT=8080 …
```

Rate-limit LOG rules (`-m limit`) in production, so that an attack cannot flood the kernel log. For high volume, use NFLOG to a userspace logger (ulogd), or nftables `log group`.

---

## 18.3 Stateful and Stateless Filtering

A **stateful** firewall keeps a table of connections (conntrack, Module 11) and can match packets by their place in one:

- **NEW:** the first packet;
- **ESTABLISHED:** later packets in either direction;
- **RELATED:** for example, the ICMP error about a connection (Module 7);
- **INVALID:** fits no known connection.

A **stateless** filter judges each packet on its own fields.

**Stateful, measured.** A client whose INPUT dropped everything but loopback could not complete its *own* outgoing connections: the server's SYN-ACK is just another inbound packet (gave up). One rule, `-m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT`, fixed it. Conntrack then held the connection (`dport=443`), and nothing unsolicited could get in. That rule, near the top, is in every sensible host firewall.

**Stateless, measured, as an AWS network ACL.** The firewall allowed TCP to port 443 inbound, and, mirroring that, TCP *to* port 443 outbound. A capture on the server side shows the problem:

```
198.18.180.10.50428 > 198.18.181.10.443: Flags [S]     ← SYN reaches the server
198.18.181.10.443 > 198.18.180.10.50428: Flags [S.]    ← SYN-ACK leaves the server … and dies at the ACL
198.18.181.10.443 > 198.18.180.10.50428: Flags [S.]    ← retransmitted, dropped again
```

The client timed out. A stateless filter needs the **return rule** explicitly: from port 443 to the client's ephemeral range (`--sport 443 --dport 1024:65535`). With it, the client connected. Linux clients use 32768–60999 (`net.ipv4.ip_local_port_range`), Windows 49152–65535, and NAT gateways may use all of 1024–65535, so ACLs allow the whole range.

**The hole a stateless return rule opens.** That return rule admits *any* packet from port 443 to any high port, including a new connection that was never a reply. From the server side, `nc -s 198.18.181.11 -p 443 198.18.180.10 5000` **succeeded** through the ACL to a service on the client. Replacing the ACL with stateful rules (`ESTABLISHED,RELATED` back, `NEW` only towards 443) kept the client working and closed the hole: the same attempt **timed out**.

### In the cloud

| | AWS security group | AWS network ACL | Azure NSG | GCP VPC firewall rules / policies |
| --- | --- | --- | --- | --- |
| State | Stateful | **Stateless** | Stateful | Stateful |
| Attached to | Network interface | Subnet | Subnet and/or NIC (both apply) | VPC network, targeted by tags or service accounts |
| Rules | Allow only; all rules apply (union) | Allow and deny; numbered, **first match wins** | Allow and deny; priority order (lower first) | Allow and deny; priority order |
| References | Other security groups, prefix lists | CIDRs only | Service tags, application security groups | Tags, service accounts, address groups |
| Default | Deny in, allow out | Default ACL allows all; custom ACLs deny all | Default rules (VNet, load balancer allowed; internet in denied) | Implied deny in, allow out |

Support patterns that follow from the table:

- **Several security groups on one interface combine as a union.** A wide rule in any of them opens the port, whatever the others say.
- **A NACL change that "breaks everything"** has usually lost the ephemeral-port return rule, or put a broad deny at a lower number than the allows.
- **An Azure NSG on both the subnet and the NIC** must allow the flow in both. Read the *effective* rules for the NIC.
- **A security group blocks silently.** Like DROP, it gives a timeout, never "connection refused". A refused connection in the cloud came from the instance itself (nothing listening, or a host firewall REJECT), or from a load balancer.

---

## 18.4 Segmentation

**Segmentation limits what a compromised or misbehaving system can reach.** The lab's firewall routes between three networks:

- **client** (users);
- **web** (the server);
- **data** (the database).

It allows only two kinds of new connections: client→web on 443 (and 8443), and web→database on 5432. Established traffic flows back. Measured:

| Path | Result |
| --- | --- |
| web → db :5432 | connected |
| client → db :5432 | **gave up** (dropped at the firewall) |
| db → web (a compromised database calling out) | **gave up** |

The FORWARD counters (`policy DROP`, per-rule packets on `dpt:443` and `dpt:5432`) show every decision.

Two habits make segmentation trustworthy:

- **Test the denied paths, not just the allowed ones.** A policy that was never seen to block anything may not be enforced: Kubernetes NetworkPolicy on a CNI that ignores it (Module 17), a rule in a chain nothing jumps to.
- **Remember that a block is a timeout to the client.** The client cannot tell "blocked by design" from "the database is down". Document intended blocks, and when debugging, check the firewall's counters before the application's logs.

The same structure appears at every scale:

- separate VPCs or accounts for environments;
- subnets for tiers;
- security groups that reference each other ("db accepts 5432 from sg-app");
- NetworkPolicy per pod;
- mesh authorization per service.

---

## 18.5 SYN Floods, SYN Cookies and Rate Limits

A SYN flood fills a listener's **SYN queue** with half-open connections, the SYN-RECEIVED entries from Module 11, whose final ACK never comes. The lab sent 200 SYNs in 0.4 s (`hping3`) at a listener with a backlog of 4. With the client's own RSTs suppressed, the server's SYN-ACKs went unanswered:

| | SYN cookies **off** | SYN cookies **on** (Linux default `tcp_syncookies=1`) |
| --- | --- | --- |
| Half-open entries | 5: the queue is full | still 5 |
| A real client during the flood | **gave up after 5 s** | **connected in 0.21–0.49 ms** |
| Counters (`nstat`) | `TcpExtTCPReqQFullDrop 200`, `TcpExtListenDrops 200` | `TcpExtSyncookiesSent 196–201`, `TcpExtTCPReqQFullDoCookies 196–201`, `TcpExtSyncookiesRecv 1` |
| Kernel log | — | `Possible SYN flooding on port …:8080. Sending cookies.` |

**What SYN cookies do.** When the queue is full, the server stops storing state for new SYNs. It encodes what it needs (the MSS and a timestamp-protected hash) in its SYN-ACK's sequence number, and rebuilds the connection when the ACK carries that number back. The real client's ACK was the one cookie received. The flood's SYNs cost nothing to store.

Cookies have limits:

- Some TCP options are lost: window scaling and SACK survive only with timestamps.
- They protect the SYN queue, not conntrack tables or bandwidth.
- A flood large enough to fill the link wins anyway.

**`ListenDrops` and `SyncookiesSent` are worth graphing on every server.** `ListenOverflows` is the different problem of a full *accept* queue: an application not calling accept fast enough (Module 11).

**Rate limiting new connections** is the same idea at a different layer. The lab allowed 3 new SSH connections per source per 10 s with the `recent` match:

```bash
iptables -A INPUT -p tcp --dport 22 -m conntrack --ctstate NEW -m recent --name ssh --set
iptables -A INPUT -p tcp --dport 22 -m conntrack --ctstate NEW -m recent --name ssh --rcheck --seconds 10 --hitcount 4 -j DROP
```

Six attempts in a row: **3 connected, 3 gave up**, in all runs. `/proc/net/xt_recent/ssh` holds the source's timestamps. `hashlimit`, nftables `limit`/`meter`, fail2ban (which adds rules from log patterns), and edge rate limits all apply the same principle. They also cause the same support ticket: a legitimate burst, or many users behind one NAT address, gets blocked.

---

## 18.6 WAF and DDoS Protection

A **web application firewall** terminates (or sits behind) TLS and inspects HTTP requests for attack patterns:

- SQL injection, cross-site scripting, path traversal, protocol violations, request smuggling;
- known-bad clients;
- per-key rate limits.

The usual base is the OWASP Core Rule Set or a vendor derivative: AWS WAF managed rule groups, Cloudflare's managed rules, Azure WAF's Microsoft Default Rule Set (DRS 2.x). WAFs score requests ("anomaly scoring") and block above a threshold.

**For support, a WAF is a source of false positives.** Watch for these:

- A legitimate request is blocked because a field contains `'`, `<`, `../`, or SQL-like words: a customer named O'Brien, a code snippet, a search for "select".
- Bodies over the WAF's inspection limit pass uninspected, or are blocked, depending on settings.
- The block arrives as the WAF's own 403 page (often with a request or ray ID), not the application's. That ID finds the matching rule in the WAF log.

Roll out new rules in count or log-only mode, review what they would block, then enforce. Prefer narrow exclusions for one rule, one field and one path over disabling a rule group.

**DDoS** comes in three layers, each with its own defence:

- **Volumetric** floods (UDP reflection and amplification via DNS, NTP, memcached or CLDAP; botnets) fill links. Only networks bigger than the attack absorb them. That means anycast edges (Module 9) and scrubbing (Cloudflare, AWS Shield, Akamai, cloud DDoS protection). Attacks above 1 Tbps were routine in Cloudflare's 2026 reports.
- **Protocol** attacks exhaust state: SYN floods (18.5), conntrack tables, firewall session tables. Defences are SYN cookies, stateless filtering at the edge, and keeping stateful devices out of the high-volume path.
- **Application** attacks look like real requests: HTTP floods, expensive endpoints, Slowloris-style slow requests. Defences are rate limits per key, caching, bot detection, and request timeouts.

Two support corollaries:

- **The origin must accept traffic only from the protection layer.** Use security groups limited to the CDN's ranges, or authenticated origin pulls. Otherwise attackers who find the origin address go around it.
- **Edge protections fail closed for some users.** A challenge page breaks API clients, and a geo rule blocks a traveling user. Check the edge's logs before the application's.

---

## 18.7 Zero Trust: Identity Instead of Location

A perimeter model trusts what is "inside". Once an attacker or a misconfigured service is inside (a VPN account, a compromised host, a flat network), it can reach everything the network allows. **Zero trust** (NIST SP 800-207; Google's BeyondCorp) moves the decision to each request:

- **Who** is asking: a user identity from SSO, or a workload identity from a certificate (a SPIFFE ID, a Kubernetes ServiceAccount).
- **From what:** device posture.
- **For what:** this resource, this action.

Network location becomes one signal among several, not a pass.

The lab's mTLS service authorizes by the client certificate's identity (`tlslab serve -clientca … -authz orders-service=/orders`):

| Request | Result |
| --- | --- |
| identity `orders-service`, `GET /orders/42`, from the client network | **HTTP 200**: "client orders-service" |
| same identity, `GET /admin` | **HTTP 403**: "orders-service may not access /admin" (authenticated, not authorized) |
| no client certificate | TLS fails: `alert certificate required` |
| certificate from another CA (same name) | TLS fails: `alert unknown ca` |
| identity `orders-service`, from the **database network** | **HTTP 200**: the source address never entered the decision |

The service's log records each decision with identity and source. That is the audit trail a network ACL cannot give: an ACL knows addresses, not who held them.

Zero trust in practice has several forms:

- **Identity-aware proxies** for people (Cloudflare Access, Google IAP, Zscaler Private Access, Tailscale ACLs and similar) replace "on the VPN, so allowed" with SSO plus device checks per application.
- **Service meshes and SPIFFE** issue short-lived workload certificates and enforce mTLS and authorization between services (Module 17).
- **Segmentation stays** as a second layer, to limit blast radius and stop what identity cannot (scanning, exploits against the TLS stack itself).

Verify each property separately: that identity is authenticated, that the authorization policy denies (test a denial), that certificates rotate, and that every path is covered (direct pod IPs, health ports, admin ports).

---

## Hands-On Lab: Firewalls Measured

### Lab Goals

- Measure every firewall verdict from the client's side and prove which rule decided it
- Compare stateful and NACL-style stateless rules, including the stateless hole
- Segment three tiers and verify allowed and denied paths
- Flood a listener with and without SYN cookies, and rate-limit new connections
- Authorize requests by mTLS identity and show that location does not matter

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iptables` (nft backend), `nftables`, `conntrack`, `hping3`, `netcat-openbsd`, `curl`, `tcpdump`

The lab changes one host setting: `net.netfilter.nf_log_all_netns`. It saves the original value and restores it at teardown.

### Lab Part 1: Run the Lab

```
  client 198.18.180.10 ── fw ── server 198.18.181.10 (.11, .12)   tcp 22 80 443 8080(backlog 4) 9090 9091 · mTLS 8443
         (tcp 5000)         └── db 198.18.182.10 (tcp 5432)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/firewall.json     # about 40 seconds
```

The 52 checks produce every table in this module. Three runs gave identical verdicts and counters, apart from the SYN-cookie count, which was 196–201.

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/firewall.json
alias lab='sudo ./bin/netlab exec ../netlab/firewall.json'
```

1. **Which rule matched?** Recreate the server policy from check 2. Then `lab server -- iptables -Z INPUT`, run one `nc -zv` from the client, and `lab server -- iptables -vnL INPUT`: exactly one counter moved.
2. **Write it in nftables.** In the server, `iptables -F INPUT; iptables -P INPUT ACCEPT`, save the `table inet host` example from 18.2 to a file and load it with `nft -f`. Measured: 22 and 443 connect, 8080 fails with `No route to host` (icmpx admin-prohibited), ping still works. `nft list ruleset` shows two tables — the (now empty) iptables-nft `ip filter` and yours.
3. **NOTRACK breaks stateful rules.** On the firewall, set the stateful FORWARD policy (DROP, `ESTABLISHED,RELATED` accept, NEW to 443 accept); 443 connects. Then `iptables -t raw -A PREROUTING -p tcp --dport 443 -j NOTRACK`: the next attempt **timed out**. The SYN is now UNTRACKED — neither NEW nor ESTABLISHED — so it falls to the DROP policy. High-volume servers use NOTRACK on purpose (to spare the conntrack table), and must then write stateless rules for that traffic.
4. **Find the flood in counters alone.** Repeat the SYN flood while watching `lab server -- nstat -az TcpExtSyncookiesSent TcpExtListenDrops TcpExtTCPReqQFullDrop` and `lab server -- ss -s`. These are the counters to alert on.

`sudo ./bin/netlab down ../netlab/firewall.json`.

### Lab Part 3: Your Own Machine and Cloud

```bash
sudo iptables -V; sudo nft list ruleset | head -40; sudo iptables-legacy -S 2>/dev/null | head
sudo ufw status verbose 2>/dev/null; sudo firewall-cmd --list-all 2>/dev/null
nstat -az TcpExtSyncookiesSent TcpExtListenDrops TcpExtListenOverflows
sysctl net.ipv4.tcp_syncookies net.ipv4.ip_local_port_range net.netfilter.nf_log_all_netns
```

In AWS, `aws ec2 describe-security-groups` and `describe-network-acls` for an instance's interface and subnet. VPC Reachability Analyzer evaluates the rules for a path without sending traffic. Azure's "Effective security rules" and Network Watcher's IP flow verify do the same, and so does GCP's Connectivity Tests.

---

## Real-World Troubleshooting Scenario

### Scenario: "Works in Staging, Times Out in Production — and the Firewall Team Says It's Open"

**The situation.**

- A new service runs in a private subnet in production and calls a partner API on 443.
- Staging works.
- In production, every call times out after 30 seconds.
- The firewall team shows a security group rule allowing outbound 443.

**Step 1 — Name the symptom precisely.**

```bash
curl -v --max-time 10 https://partner.example/api
# * Trying 203.0.113.40:443...   then nothing until the timeout
```

It is a timeout, not refused and not "No route to host". So something is *dropping*, not rejecting. The candidates are a security group, an ACL, a missing route or NAT, or the far end.

**Step 2 — Walk the path, both directions.**

- The security group allows outbound 443, and, being stateful, the replies.
- The subnet's **network ACL**, added by a recent hardening change, allows outbound 443 and inbound 443. That is the mirrored mistake from the lab. The partner's SYN-ACK comes back *from* port 443 *to* an ephemeral port, and no inbound rule allows that. Exactly the capture in 18.3: the SYN leaves, the SYN-ACK arrives at the subnet edge, and the ACL drops it.
- **VPC Flow Logs** confirm it: `ACCEPT` for the outbound SYN, `REJECT` for the inbound SYN-ACK on the subnet's interfaces.

**The fix.** Add an inbound ACL rule allowing TCP from the destination (or 0.0.0.0/0) with destination ports 1024–65535, at a number below any broad deny. Better, keep NACLs coarse and do fine-grained control in stateful security groups, where return traffic is implicit.

Staging worked because it used the default ACL, which allows everything.

**What made it fast:** the symptom class (timeout means drop), the knowledge that only one layer in the path is stateless, and flow logs that show the decision per direction.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| "No route to host" but routing and ping are fine | REJECT with icmp-host/admin-prohibited (firewalld default) | `nc -zv`; capture shows ICMP type 3 code 10/13 | Allow the port; check `firewall-cmd --list-all` |
| "Connection refused" though the service runs | REJECT (tcp-reset or port unreachable) in a host firewall | Server counters; capture for RST or ICMP | Allow the port |
| Connection hangs, then times out | DROP: host firewall, security group, ACL, policy | Capture at both ends; counters; flow logs | Find the dropping layer; allow the flow |
| A new ACCEPT rule has no effect | An earlier rule matches first (DROP/REJECT above it) | `iptables -vnL --line-numbers`; counters | Insert above, or remove the earlier rule |
| Outbound connections from a host fail after a "harden" | INPUT DROP without an ESTABLISHED,RELATED rule | SYN-ACK arrives and is dropped | Add the conntrack rule near the top |
| Works with NACL default, fails with custom NACL | Missing ephemeral return rule, or deny ordered before allow | Flow logs show REJECT on the return | Return rule for 1024–65535; renumber |
| A published Docker port ignores ufw | DNAT before INPUT (Module 16) | INPUT counters stay 0 | DOCKER-USER, or bind to 127.0.0.1 |
| LOG rule in a container/pod logs nothing | `nf_log_all_netns=0` | Same rule logs on the host | Set the sysctl on the host, or log with NFLOG to userspace |
| Rules look right but traffic is blocked | Another table or tool (nft tables, legacy iptables, firewalld) also on the hook | `nft list ruleset`; `iptables-legacy -S` | Consolidate; remember every base chain must accept |
| Clients fail during a traffic spike, server idle | SYN queue full (flood, or burst with cookies off) | `TcpExtListenDrops`, `ReqQFullDrop`; kernel "Possible SYN flooding" | Keep `tcp_syncookies=1`; larger backlog; edge protection |
| Legitimate users blocked in bursts | Rate limit per source with many users behind one NAT | Rate-limit counters; `/proc/net/xt_recent` | Raise limits or key by something other than IP |
| Some requests get a 403 HTML page from "the firewall" | WAF false positive | WAF log by request/ray ID | Narrow exclusion; count mode before enforcing |
| mTLS clients fail with "certificate required"/"unknown ca" | No client certificate sent / wrong CA trusted | Server's handshake errors | Configure client cert; distribute the right CA |
| Authenticated client gets 403 | Authorization policy lacks the identity or path | Service's authz log | Grant the identity explicitly |

---

## Summary

- A client can tell firewall verdicts apart: ACCEPT connects; REJECT with tcp-reset or port unreachable is "refused" in under a millisecond; REJECT with host/admin-prohibited is "No route to host"; DROP is a timeout. "No route to host" on a reachable host usually means a REJECT rule.
- iptables rules are evaluated in order and the first match wins; policies apply when nothing matches; counters prove which rule decided. On modern distributions iptables writes nftables, and every base chain on a hook must accept. LOG is silent outside the host namespace unless `nf_log_all_netns=1`.
- Stateful rules need one ESTABLISHED,RELATED rule and then only describe new connections. Stateless filters (AWS NACLs) need explicit return rules for ephemeral ports, and those rules admit new traffic from the "return" port. A missing return rule shows as a SYN-ACK that leaves the server and never arrives.
- Segment by tier with explicit allowed paths and test the denied ones; blocked paths look like timeouts to clients.
- SYN cookies kept a real client connecting through a flood that otherwise timed it out; `SyncookiesSent`, `ListenDrops` and `ReqQFullDrop` are the evidence. Rate limits block bursts — including legitimate ones.
- WAFs and DDoS services protect the edge and create their own failure modes (false positives, challenges, origin bypass). Zero trust authorizes each request by verified identity: the lab's service allowed `orders-service` to `/orders` from any network, refused `/admin`, and rejected missing or foreign certificates at the TLS layer.

## What's Next

Module 19 covers VPNs, tunnels and overlay networks — WireGuard, IPsec, VXLAN, GRE and SSH tunnels: how traffic is carried across networks you don't control, and what that costs in MTU and debugging.

---

## Quick Reference Card

```
MODULE 18: FIREWALLS, SECURITY & ZERO TRUST — QUICK REFERENCE
══════════════════════════════════════════════════════════════

VERDICTS AS CLIENTS SEE THEM
  ACCEPT                         connected
  REJECT tcp-reset / port-unreach "Connection refused" (ECONNREFUSED), < 1 ms
  REJECT host/admin-prohibited   "No route to host" (EHOSTUNREACH), < 1 ms   ← firewalld default
  DROP                           timeout (SYN retransmissions until the client gives up)

IPTABLES / NFTABLES
  iptables -vnL CHAIN --line-numbers · -Z (zero counters) · -I (insert first) vs -A (append) · -P CHAIN DROP
  iptables-save · iptables -V (nf_tables = writes nftables) · nft list ruleset · iptables-legacy -S
  -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT   (first, in every stateful policy)
  -j LOG --log-prefix X  (+ -m limit) · non-host namespaces need sysctl net.netfilter.nf_log_all_netns=1
  never drop all ICMP/ICMPv6 (PMTUD, ND)

STATELESS (AWS NACL)
  return rule: --sport SVC --dport 1024:65535 (Linux clients 32768–60999)  ← also admits new traffic from SVC port
  numbered, first match wins · default ACL allows all, custom denies all

CLOUD
  AWS SG stateful, allow-only, union · NACL stateless, ordered · Azure NSG subnet+NIC both apply · GCP rules/policies by priority
  tools: VPC Reachability Analyzer · flow logs · Azure effective rules / IP flow verify · GCP Connectivity Tests

SYN FLOOD / RATE LIMIT
  sysctl net.ipv4.tcp_syncookies=1 (default) · nstat: TcpExtSyncookiesSent TcpExtListenDrops TcpExtTCPReqQFullDrop
  ss -s · kernel: "Possible SYN flooding on port … Sending cookies."
  -m recent --set / --rcheck --seconds S --hitcount N -j DROP · hashlimit · nft limit/meter

EDGE
  WAF: OWASP CRS / vendor rules, anomaly scoring, count mode first, narrow exclusions, find the request ID
  DDoS: volumetric → anycast/scrubbing · protocol → cookies, stateless edge · application → rate limits, caching
  lock the origin to the edge (SG to CDN ranges, authenticated origin pulls)

ZERO TRUST
  identity (SSO / SPIFFE / ServiceAccount certs) + device + context, per request · NIST SP 800-207
  test: authn required · authz denies · rotation · every path covered · segmentation still limits blast radius
  COURSE: tlslab serve -clientca CA -authz IDENTITY=/prefix
```

---

## References

- `man iptables`, `man iptables-extensions` (conntrack, recent, hashlimit, LOG, REJECT), `man nft`, `man conntrack`
- Linux `Documentation/networking/ip-sysctl.rst` (tcp_syncookies, ip_local_port_range) and `nf_conntrack-sysctl.rst`; `net.netfilter.nf_log_all_netns`
- RFC 4987 — TCP SYN Flooding Attacks and Common Mitigations
- AWS — Security groups; Network ACLs; VPC Flow Logs; VPC Reachability Analyzer; AWS WAF; AWS Shield
- Azure — Network security groups (effective rules); Web Application Firewall rule sets (DRS)
- Google Cloud — VPC firewall rules and firewall policies; Connectivity Tests
- OWASP Core Rule Set — https://coreruleset.org/; OWASP Top 10
- NIST SP 800-207 — Zero Trust Architecture; Google BeyondCorp papers; SPIFFE — https://spiffe.io/
- Cloudflare DDoS threat reports (2026)
