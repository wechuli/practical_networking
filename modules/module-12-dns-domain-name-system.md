# Module 12: DNS — Domain Name System

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 11 — TCP — Transmission Control Protocol
> **Estimated time:** 4–5 hours

## Overview

DNS turns names into addresses (and much else) through a hierarchical, delegated, cached database. Most applications depend on it for every new connection, which is why "it's always DNS" became a joke — and why it is a bad diagnostic method. An application that already holds an address, a connection pool, or a cached answer does not query DNS at all, and many outages blamed on DNS are something else. Test DNS when the failing operation actually resolves a name, and test it the way the application does: with the application's resolver, from the application's network.

What makes DNS confusing is not the protocol, which is small, but the number of places an answer can come from and the number of independent caches between a change and a client:

- **Several resolvers with different views.** The same name can legitimately resolve differently from a laptop, a VM, a container and a pod, because each may use a different resolver, search list, `/etc/hosts`, or split-horizon view.
- **Caches at every layer.** The application or its runtime, the operating system's stub resolver (or none), a node-local cache, the recursive resolver — each holds answers, including negative ones, for the TTL it was given, or for its own idea of a TTL.
- **Failures that look alike.** A dead authoritative server, a broken DNSSEC signature, a lame delegation and a firewall dropping port 53 can all end as "could not resolve host". The response code, the Extended DNS Error, and the time it took tell them apart.

This module covers DNS at the depth a support engineer needs: the path from stub to root to authority, the record types, reading `dig` output, TTLs and negative caching measured through real caches, transport (UDP, TCP, truncation, encrypted DNS), DNSSEC as it fails in practice, split-horizon and cloud private DNS, the stub resolver's search list and timeouts, and the failure signatures. The lab builds a complete private DNS — root, TLD, authoritative servers and two validating resolvers — in network namespaces and measures all of it.

## What You Will Build Toward

- Follow a resolution from the stub resolver through a recursive resolver to the root, TLD and authoritative servers — by hand, with `dig +norecurse`, and with a trace tool
- Read `dig` output fully: status, flags (`qr aa tc rd ra ad cd`), sections, TTLs, EDNS, Extended DNS Errors
- Predict how long a changed record and a newly created name take to appear through a cache — and measure it
- Tell NXDOMAIN, NODATA, SERVFAIL, REFUSED and a timeout apart, and know which layer produced each
- Diagnose DNSSEC failures with `+cd` and EDE codes without disabling validation
- Explain split-horizon DNS and cloud private zones, and why the same name resolves differently inside and outside
- Predict the queries a stub sends for a name, given its search list, `ndots`, `/etc/hosts` and nameserver timeouts

---

## 12.1 Why DNS Exists

Until the mid-1980s every host on the ARPANET downloaded one file, `HOSTS.TXT`, maintained by the SRI Network Information Center. By then the file changed daily, names collided, and one organisation could not keep up. DNS (RFC 1034 and RFC 1035, 1987) replaced it with a delegated hierarchy: the root delegates top-level domains to their operators, who delegate second-level domains to their owners, and so on. No server knows every name; each knows its own zone and where to send questions about zones below it.

The hosts file survived, and on Linux it is still consulted **before** DNS, as configured in `/etc/nsswitch.conf`:

```
# /etc/nsswitch.conf (typical Ubuntu)
hosts:  files mdns4_minimal [NOTFOUND=return] dns

  files            /etc/hosts — if the name is here, DNS is never asked
  mdns4_minimal    multicast DNS for .local names (if avahi is installed)
  dns              the resolvers in /etc/resolv.conf
```

That ordering is why a forgotten line in `/etc/hosts` (a test entry, or one written by a VPN client or a container platform) makes a host ignore every DNS change — and why `dig` and `nslookup` can disagree with the application: they talk DNS directly and never read `/etc/hosts`. The lab shows it: `getent hosts legacy.corp.test` returns the hosts-file address while `dig` gets NXDOMAIN. Use `getent hosts NAME` (or `getent ahosts`) to see what an application using the system resolver will get.

---

## 12.2 DNS Architecture

```
DNS RESOLUTION — WHO TALKS TO WHOM

  application ── getaddrinfo() ──▶ STUB RESOLVER (in the OS or the runtime)
                                    reads /etc/hosts, /etc/resolv.conf;
                                    sends ONE recursive query (RD=1)
                                            │
                                            ▼
                                   RECURSIVE RESOLVER (caching, often validating)
                                    systemd-resolved → corporate/ISP/cloud resolver,
                                    8.8.8.8, 1.1.1.1, 9.9.9.9 …
                                    answers from cache, or asks iteratively (RD=0):
              ┌─────────────────────────────┼─────────────────────────────┐
              ▼                             ▼                             ▼
         ROOT SERVERS                   TLD SERVERS                 AUTHORITATIVE
         "test. is served by           "shop.test. is served       "app.shop.test is
          ns.tld.test, here is          by ns.shop.test, here       198.18.70.10, TTL 10"
          its address (glue)"           is its address (glue)"      (aa: authoritative answer)
```

- **Stub resolver.** The DNS client library: glibc's resolver, musl's (Alpine), systemd-resolved behind its 127.0.0.53 stub listener on Ubuntu, Go's built-in resolver, the JVM's. It sends a recursive query and waits; glibc and musl cache nothing.
- **Recursive (caching) resolver.** Does the work: walks the hierarchy, caches every answer it gets (positive, negative and referrals) for the TTL, and — if validating — checks DNSSEC signatures. In clouds this is the platform resolver (AWS VPC base + 2 / 169.254.169.253, Azure 168.63.129.16, GCP's metadata server); in Kubernetes it is CoreDNS forwarding to the node's resolver.
- **Root servers.** Thirteen named identities, `a.root-servers.net` to `m.root-servers.net`, each an anycast service (Module 9) with many instances worldwide; see [root-servers.org](https://root-servers.org/). They only refer: they know who serves each TLD. Resolvers know their addresses from a built-in **root hints** file.
- **TLD servers.** Run by registries (Verisign for `.com` and `.net`, others for the rest); they refer to each domain's name servers.
- **Authoritative servers.** Hold the zone's data and answer with `aa` set. The zone's owner runs them, or a DNS provider does.

A **referral** is an answer with no answer section, NS records in the authority section, and — when the name servers are inside the zone being delegated — their addresses in the additional section. Those addresses are **glue**: without them, finding `ns.shop.test` would require resolving `shop.test`, whose servers are the ones being looked for. The lab's three `dig +norecurse` checks show the chain exactly:

```
dig +norecurse @198.18.50.2 app.shop.test     (a root server)
  flags: qr;  ANSWER: 0                        ← no aa, no answer: a referral
  ;; AUTHORITY SECTION:   test.            172800  IN  NS  ns.tld.test.
  ;; ADDITIONAL SECTION:  ns.tld.test.     172800  IN  A   198.18.51.2      ← glue
dig +norecurse @198.18.51.2 app.shop.test     (the test. server)
  ;; AUTHORITY SECTION:   shop.test.       3600    IN  NS  ns.shop.test.
  ;; ADDITIONAL SECTION:  ns.shop.test.    3600    IN  A   198.18.54.2
dig +norecurse @198.18.54.2 app.shop.test     (shop.test.'s server)
  flags: qr aa;                                ← authoritative
  ;; ANSWER SECTION:      app.shop.test.   10      IN  A   198.18.70.10
```

A real validating resolver does not ask exactly these questions. The lab captured what Unbound sent upstream on a cold cache:

```
198.18.61.10 > 198.18.60.2: A? app.shop.test.            ← the office client's one question
198.18.60.2 > 198.18.50.2:  NS? .                        ← prime the root (check the hints are current)
198.18.60.2 > 198.18.50.2:  A? test.                     ← QNAME minimisation: tell the root only "test."
198.18.60.2 > 198.18.51.2:  A? shop.test.                ← tell the TLD only "shop.test."
198.18.60.2 > 198.18.54.2:  A? app.shop.test.            ← only the authority hears the full name
198.18.60.2 > 198.18.50.2:  DNSKEY? .                    ← DNSSEC: fetch keys to validate the chain
198.18.60.2 > 198.18.50.2:  A? _ta-c380.                 ← RFC 8145: report which trust anchor it uses
198.18.60.2 > 198.18.51.2:  DNSKEY? test.
198.18.60.2 > 198.18.61.10: 1/0/1 A 198.18.70.10         ← the client's answer
```

**QNAME minimisation** (RFC 9156, on by default in Unbound, BIND and the large public resolvers) sends each server only as much of the name as it needs, for privacy. In tcpdump's notation, `%` marks the CD bit — the resolver validates itself, so it asks servers not to — and `-` marks a response without RA. The same question asked again two seconds later produced no upstream packets at all: it came from the cache.

---

## 12.3 DNS Record Types

```
RECORD  WHAT IT HOLDS                                EXAMPLE
──────  ───────────────────────────────────────────  ──────────────────────────────────────────────
A       IPv4 address                                 app.shop.test.     10  A      198.18.70.10
AAAA    IPv6 address                                 app.example.       300 AAAA   2001:db8::10
CNAME   "this name is an alias for that one"         www.shop.test.     10  CNAME  edge.shop.test.
        A name with a CNAME may have NO other data (RFC 1034/2181) — so not at a zone apex,
        which must have SOA and NS. Providers offer ALIAS/ANAME/"CNAME flattening" there instead:
        the provider resolves the target and serves A/AAAA records.
        Each CNAME hop can be another lookup, possibly in another zone (12.5).
MX      Mail exchanger, with preference              shop.test.         MX     10 mx1.shop.test.
        (lower is preferred); must point at a name with A/AAAA, never a CNAME (RFC 2181)
NS      The zone's (or a delegated child's) servers  shop.test.         NS     ns.shop.test.
SOA     Zone metadata: primary server, contact,      shop.test. 20 SOA  ns.shop.test. hostmaster.shop.test.
        serial, refresh, retry, expire, MINIMUM         3 3600 900 604800 3600
        (MINIMUM is now only the negative-caching cap; see 12.4)
PTR     Reverse lookup: address → name, under        10.70.18.198.in-addr.arpa. PTR app.shop.test.
        in-addr.arpa / ip6.arpa; set by whoever controls the address block (often your cloud/ISP)
TXT     Free text: SPF, DKIM, DMARC, domain          _dmarc.shop.test.  TXT    "v=DMARC1; p=reject"
        verification, ACME challenges (_acme-challenge)
SRV     Service location: priority weight port      _sip._tcp.shop.test. SRV 10 60 5060 sip1.shop.test.
        target — used by Kubernetes (_port._proto.svc.ns.svc.cluster.local), AD, SIP, XMPP
CAA     Which certificate authorities may issue     shop.test.         CAA    0 issue "letsencrypt.org"
        for the name (CAs must check, RFC 8659)
HTTPS / Endpoint and protocol hints for a service:  shop.test. HTTPS 1 . alpn="h2,h3"
SVCB    ALPN (h2, h3), ports, address hints, ECH keys (RFC 9460); browsers query them alongside A/AAAA
DS      In the PARENT zone: hash of the child's key  secure.test.       DS     40728 13 2 …
DNSKEY  In the zone: its public keys                 secure.test.       DNSKEY 257 3 13 …
RRSIG   A signature over one record set              www.secure.test.   RRSIG  A 13 3 300 …
NSEC/   Signed proof that a name or type does not exist (NSEC3 hashes the names)
NSEC3
```

```bash
dig app.shop.test                 # A by default
dig app.shop.test AAAA            # one type
dig shop.test SOA +noall +answer  # just the answer section
dig -x 198.18.70.10               # reverse (PTR) lookup
dig shop.test ANY                 # many servers answer ANY minimally (RFC 8482): ask for types explicitly
```

---

## 12.4 TTL, Caching and Negative Caching

Every record has a **TTL**: how many seconds a resolver may keep it. A cache returns the *remaining* time, so a TTL in `dig` output that is lower than the zone's value, and falling, means the answer came from a cache. An answer with `aa` came from an authoritative server; the absence of `aa` does not by itself prove a cache hit.

### Where answers are cached

```
LAYER                        CACHES?                                   FLUSH / INSPECT
───────────────────────────  ────────────────────────────────────────  ─────────────────────────────────
Application / runtime        JVM: positive 30 s, negative 10 s by      restart, or the runtime's settings
                             default (networkaddress.cache.ttl);       (Java security properties, etc.)
                             browsers keep their own short-lived cache; chrome://net-internals/#dns
                             many HTTP clients reuse connections and
                             never re-resolve while they are open
Stub resolver                glibc, musl, Go: NO cache                 —
OS caching service           systemd-resolved (Ubuntu), nscd,           resolvectl flush-caches /
                             Windows DNS Client, macOS mDNSResponder    statistics; ipconfig /flushdns;
                                                                        sudo dscacheutil -flushcache
Node-local / sidecar cache   NodeLocal DNSCache, dnsmasq, CoreDNS       per product
Recursive resolver           yes — positive, negative, referrals,       only its operator can flush
                             failures (RFC 9520), sometimes stale       (public resolvers have purge pages)
                             data (serve-stale, RFC 8767)
```

### A change, followed through the caches

The lab changes `app.shop.test` (TTL 10 s) from 198.18.70.10 to 198.18.70.20 on the authoritative server, after the office and public resolvers have both cached the old value, and then asks all three every half second (`dnslab watch`):

```
   0.0s  198.18.54.2     198.18.70.20      ttl 10     ← the authority has the new value at once
   0.0s  198.18.60.2     198.18.70.10      ttl 7      ← office resolver: 7 s left on its copy
   0.0s  198.18.62.2     198.18.70.10      ttl 10     ← public resolver cached it 3 s later
   8.0s  198.18.60.2     198.18.70.20      ttl 10
  11.0s  198.18.62.2     198.18.70.20      ttl 10
```

Each cache switches when **its own** copy expires — never later than one TTL after the change, but at different moments. Clients behind different resolvers therefore see the change at different times, and a client that resolved once and kept a connection open sees it only when it reconnects. With a TTL of 3600 the same picture lasts up to an hour.

### Negative caching

"This name does not exist" (NXDOMAIN) and "this name has no record of that type" (NODATA: NOERROR with an empty answer) are cached too. The authoritative server includes its zone's SOA record in the authority section, and the negative answer may be cached for **the smaller of the SOA record's own TTL and its MINIMUM field** (RFC 2308). The lab's `shop.test` SOA has TTL 20 and MINIMUM 3600:

```
dnslab query -s 198.18.54.2 -norec new.shop.test
  NXDOMAIN: the name does not exist
  negative answer cacheable for 20 s = min(SOA TTL 20, MINIMUM 3600)
```

Then the lab creates `new.shop.test` and watches:

```
   0.0s  198.18.54.2     198.18.70.60      ttl 10     ← exists now
   0.0s  198.18.60.2     NXDOMAIN          ttl 20     ← the resolver's negative entry
  21.0s  198.18.60.2     198.18.70.60      ttl 10     ← 20 s later (sampled every 0.5 s)
```

The textbook shortcut "MINIMUM is the negative TTL" would have predicted an hour. The classic incident this explains: a deploy pipeline queries a name *before* creating it (a health check, a certificate validation, a typo), the resolver caches NXDOMAIN, and the freshly created record "doesn't work" for the negative TTL. Avoid querying names before they exist, and keep the SOA's TTL (or MINIMUM) modest.

NXDOMAIN applies to the name and every type; NODATA is cached per type. The lab's `dig app.shop.test AAAA` returns `status: NOERROR`, `ANSWER: 0` and the SOA: the name exists, it just has no IPv6 address. Applications often log both as "not found".

### Changing records safely

```
MIGRATION WITH CACHES IN MIND
  T−(old TTL + margin)  Lower the record's TTL (say 3600 → 60). Caches holding the old
                        record keep the OLD TTL until it expires, so wait at least the old
                        TTL before relying on the new one.
  T                     Change the record. Keep the old endpoint serving: some caches,
                        long-lived connections and misbehaving clients will still use it.
  T + new TTL           Most clients have moved. Verify from several resolvers and networks
                        (dnslab watch, dig @…), and watch the old endpoint's traffic drain.
  Later                 Raise the TTL again.
  Mistake to avoid      Changing the address and lowering the TTL at the same time: caches
                        already holding the record ignore the new TTL until theirs expires.
  Also remember         Resolvers may clamp TTLs (cache-min-ttl, cache-max-ttl), serve stale
                        data when the authority is unreachable (RFC 8767), and a JVM caches
                        independently. Low TTLs cost queries; they do not guarantee speed.
```

---

## 12.5 Resolution, Step by Step

`dig +trace` performs its own iterative walk from the root, ignoring the system resolver's cache (it uses the system resolver only to find the root servers). The lab's `dnslab trace` does the same and explains each step:

```
$ dnslab trace app.shop.test
step  1  198.18.50.2     (root hint)        app.shop.test. A  3.0 ms
          ← referral to test. [qr]
          test. is delegated to ns.tld.test. (glue 198.18.51.2)
step  2  198.18.51.2     (ns.tld.test.)     app.shop.test. A  0.6 ms
          ← referral to shop.test. [qr]
          shop.test. is delegated to ns.shop.test. (glue 198.18.54.2)
step  3  198.18.54.2     (ns.shop.test.)    app.shop.test. A  0.8 ms
          ← answer (1 records) [qr aa]
          app.shop.test.	10	IN	A	198.18.70.10
result: NOERROR after 3 queries in 4.5 ms
```

A **CNAME into another zone** restarts the walk for the target:

```
$ dnslab trace api.shop.test
step  3  198.18.54.2     (ns.shop.test.)    api.shop.test. A  0.1 ms
          ← answer (1 records) [qr aa]
          api.shop.test.	10	IN	CNAME	api.corp.test.
          api.shop.test. is an alias (CNAME) for api.corp.test.: start again from the root
step  4  198.18.50.2     (root hint)        api.corp.test. A  …
step  6  198.18.52.2     (ns1.corp.test.)   api.corp.test. A  0.2 ms
          api.corp.test.	300	IN	A	192.0.2.10
result: NOERROR after 6 queries in 1.3 ms
```

Note the answer: 192.0.2.10, the **external** view of `corp.test` (12.8), because the trace's queries came from the office client itself, not from the internal resolver. A trace from your laptop proves what the public internet sees, not what your resolver sees.

Three things a trace shows that a recursive query hides: **which server failed** (the step where the answers stop making sense), **lame delegations** (a server named in a referral that does not serve the zone — the lab's `broken.test` server answers REFUSED), and **glue and TTL mismatches between parent and child** (the parent's NS set and the child's own NS set should agree; when they differ, resolvers behave inconsistently).

---

## 12.6 DNS over UDP, TCP and Encrypted Transports

```
DNS TRANSPORT
  UDP 53      The default: one datagram each way, no handshake.
              Without EDNS a UDP answer is limited to 512 bytes (RFC 1035).
              EDNS(0) (RFC 6891) lets the client advertise a bigger buffer. The DNS Flag Day
              2020 recommendation is 1232 bytes — small enough to avoid IP fragmentation on
              almost any path — and it is now the default in BIND, Unbound, Knot and PowerDNS
              (every response in the lab shows "udp: 1232").
  TCP 53      Mandatory to support (RFC 7766), not a fallback for the odd case:
              - a UDP answer that does not fit comes back truncated (TC flag) and the client
                repeats the question over TCP — the lab's 1662-byte TXT record does this;
              - zone transfers (AXFR, and large IXFR) use TCP;
              - resolvers may switch to TCP when they suspect spoofing.
              Firewalls that allow UDP 53 but not TCP 53 break large answers (DNSSEC, many
              TXT/MX records, long CNAME chains) and nothing else — a confusing partial outage.
  DoT  TCP 853  DNS over TLS (RFC 7858): encrypted stub ↔ resolver.
  DoH  TCP 443  DNS over HTTPS (RFC 8484): DNS messages in HTTPS requests to a URL
              (https://dns.google/dns-query). Indistinguishable from web traffic, which is the
              point for privacy and the problem for enterprise policy and split-horizon names:
              a browser using its own DoH resolver bypasses the corporate resolver.
  DoQ  UDP 853  DNS over QUIC (RFC 9250).
  Encryption between stub and resolver says nothing about resolver ↔ authoritative traffic,
  and nothing about whether the answer is genuine — that is DNSSEC's job.
```

Module 10's lab measured UDP vs TCP for the same lookup (2 packets vs 10) and the TC retry; this module's lab repeats the truncation through a recursive resolver: `UDP truncated (tc), then TCP in 1.9 ms, 1662 bytes`.

---

## 12.7 DNSSEC in Practice

DNSSEC (RFC 4033–4035) lets a resolver prove that an answer came from the zone's owner, unaltered. Each zone signs its record sets (RRSIG records) with its keys (DNSKEY); the parent publishes a hash of the child's key (DS); the resolver starts from a **trust anchor** for the root and validates the chain of DS → DNSKEY → RRSIG down to the answer. Signed non-existence proofs (NSEC/NSEC3) cover NXDOMAIN and NODATA.

What a support engineer sees is one of three outcomes. The lab has all three:

| Zone | State | What the client gets |
| --- | --- | --- |
| `secure.test` (signed, DS in `test.`) | **Secure** | The answer with the `ad` (authenticated data) flag: `flags: qr rd ra ad` |
| `shop.test` (unsigned, no DS) | **Insecure** | The answer without `ad`. This is normal: most of the DNS is unsigned. No `ad` is not an error. |
| `expired.test` (signed, signatures expired yesterday) | **Bogus** | `status: SERVFAIL` and `EDE: 7 (Signature Expired): validation failure <www.expired.test. A IN>: signature expired from 198.18.52.2 for key expired.test. while building chain of trust` |

A validating resolver **refuses to return bogus data**: the client cannot tell this SERVFAIL from an unreachable server unless the resolver attaches an Extended DNS Error (RFC 8914; Unbound with `ede: yes`, BIND 9.18+, and the big public resolvers do). The diagnostic step is to ask the same resolver with **CD (checking disabled)**:

```
dig +cd www.expired.test         → status: NOERROR, flags: qr rd ra cd, 198.18.70.50
```

If `+cd` works and the normal query fails, the problem is validation — expired signatures (a signer that stopped running), a DS in the parent that no longer matches the child's keys (a botched key rollover or a DNS provider migration that did not move the DS), or a clock far off on the resolver. `+cd` is a diagnostic, not a fix; turning validation off hides exactly the attack DNSSEC exists to stop. `dig +dnssec` only *asks* for the signatures (`flags: do` in the OPT record; the answer then includes RRSIGs); it does not validate anything itself. `delv` (from BIND) validates locally, and online analysers (DNSViz) draw the whole chain.

---

## 12.8 Split-Horizon and Private DNS

Split-horizon DNS returns different answers depending on who asks — typically a private address to clients inside a network and a public one (or nothing) outside. The lab's `corp.test` server has two BIND **views**, matched on the resolver's source address:

```
office (internal resolver 198.18.60.2)          cloud (public resolver 198.18.62.2)
  api.corp.test → 10.20.0.10                      api.corp.test → 192.0.2.10
  git.corp.test → 10.20.0.20                      git.corp.test → NXDOMAIN (internal only)
```

The answer depends on **which resolver asked the authoritative server**, not where the client is — which is why a VPN that does not change the client's resolver, or a container that uses a different resolver than its host, gets the "wrong" view. The office resolver also only serves its own subnet: the cloud client asking it directly gets `status: REFUSED` with `EDE: 18 (Prohibited)` — an access-control decision, not a missing name.

Cloud platforms implement the same idea as **private zones attached to networks**:

```
AWS    Route 53 private hosted zone, associated with specific VPCs; answered by the VPC
       resolver (VPC base + 2, or 169.254.169.253). A VPC not associated with the zone falls
       through to public DNS — NXDOMAIN or the public record.
Azure  Private DNS zone with virtual network links; the platform resolver 168.63.129.16
       answers for linked VNets only.
GCP    Cloud DNS private zone authorised for VPC networks; the metadata server
       (169.254.169.254) resolves for instances.
```

Debug split horizon by asking each resolver explicitly, from each network: `dig @10.0.0.2 api.internal.example` from inside the VPC, `dig @8.8.8.8 …` from outside, and compare. If you need private answers from outside (a laptop on a VPN, another cloud), the resolver your client uses must forward that zone to a resolver that can see it — see 12.11.

---

## 12.9 DNS-Based Load Balancing and Failover

```
DNS TRAFFIC STEERING
  Multiple records   Several A/AAAA records; resolvers rotate or shuffle their order (Unbound's
                     rrset-roundrobin is on by default). The client usually tries the first address after
                     sorting by RFC 6724 rules — which can undo the rotation, so the spread
                     is often uneven.
  Weighted           The provider returns record sets in proportion to weights (canaries,
                     gradual migrations).
  Latency / geo      The provider answers based on the RESOLVER's location — or the client
                     subnet the resolver passes on (ECS, RFC 7871). A client using a distant
                     public resolver can be sent to a distant region.
  Health-checked     Unhealthy endpoints are withdrawn from answers.
  Failover time ≈ health-check detection + record TTL + client-side caching and connection
  reuse. Clients that resolve once at start-up never fail over at all.
  DNS does not see load, does not keep sessions together, and cannot move an open
  connection. Use it for coarse placement and failover; use load balancers (Module 23)
  for balancing.
```

---

## 12.10 The Stub Resolver: resolv.conf, search lists and timeouts

Most DNS surprises on a host come from the stub resolver's configuration, not from DNS servers. On glibc systems it is `/etc/resolv.conf`:

```
nameserver 10.0.0.2          # up to 3; tried in order (or round-robin with "options rotate")
search corp.test shop.test   # suffixes to try for "short" names
options ndots:5 timeout:2 attempts:2
  ndots:N    a name with FEWER than N dots is tried with each search suffix FIRST, then as is;
             a name with N or more dots is tried as is first (default 1)
  timeout:S  seconds to wait for one server (default 5)
  attempts:N rounds through the server list (default 2)
  rotate, single-request, single-request-reopen, use-vc (always TCP), edns0, trust-ad
```

**The search list multiplies queries.** In the lab, the office client has `search corp.test shop.test` and Kubernetes's `ndots:5`. Resolving `app.shop.test` (two dots, fewer than five) sends:

```
A? app.shop.test.corp.test.    → NXDomain
A? app.shop.test.shop.test.    → NXDomain
A? app.shop.test.              → A 198.18.70.20
```

With a trailing dot (`app.shop.test.`) the name is absolute and there is one query. A Kubernetes pod has three search suffixes (`<namespace>.svc.cluster.local svc.cluster.local cluster.local`, plus any from the node), so every lookup of an external name with fewer than five dots costs three NXDOMAIN round trips first — and twice that when the stub asks for A and AAAA in parallel, as glibc does on hosts with IPv6 (the lab's namespaces have none, so only A queries appear). Fixes: use fully qualified names with a trailing dot, lower `ndots` in the pod's `dnsConfig`, or a node-local cache that absorbs the NXDOMAINs.

**A dead nameserver costs time on every lookup.** With `nameserver 198.18.53.2` (unreachable) listed before a working one and `timeout:2`, every `getent ahosts app.shop.test` in the lab took 2006 ms — the first server's timeout, then an answer from the second. glibc remembers nothing between lookups, so the next one waits again. With the defaults (`timeout:5`) that is 5 s per lookup; with only dead servers, `timeout × attempts × servers` before the application gets an error (4.0 s in the lab, `timeout:2 attempts:2`). systemd-resolved, a node-local cache, or a resolver library that tracks server health avoids this; the stub on its own does not.

**Different stubs behave differently.** systemd-resolved (Ubuntu's default: `/etc/resolv.conf` points at its 127.0.0.53 stub; `resolvectl status` shows the real upstream servers per interface, and edits to `/etc/resolv.conf` are overwritten — configure netplan/NetworkManager/systemd-resolved instead) caches and has per-link DNS settings and routing domains. musl (Alpine images) sends queries to all nameservers in parallel and takes the first answer, and gained TCP fallback only in version 1.2.4 (2023). Go's resolver (12.11) reads `resolv.conf` and `/etc/hosts` itself. Docker's embedded resolver (127.0.0.11 in containers on user-defined networks) forwards to the host's resolvers.

---

## 12.11 Cross-Cloud Private DNS and Resolver Behaviour

### Private endpoints and the `privatelink` pattern

Managed services reached privately (Azure Private Link, AWS PrivateLink interface endpoints, GCP Private Service Connect) keep their **public names** and rely on DNS to return a private address inside the network. Azure's pattern is typical: `myaccount.blob.core.windows.net` is a CNAME to `myaccount.privatelink.blob.core.windows.net`; a private DNS zone `privatelink.blob.core.windows.net`, linked to the VNet, answers that name with the private endpoint's address; everyone else follows the public chain to the public address. So "the private endpoint doesn't work" very often means the client resolved through a resolver that cannot see the private zone, got the public address, and was blocked by the service's firewall. Check with `dig` from the client's network and look at the CNAME chain: a `privatelink` name followed by a public address means the private zone was not consulted.

### Hybrid and cross-cloud resolution

To resolve private names from somewhere else — on-premises, another VPC, another cloud — the client's resolver must **conditionally forward** the private zone to a resolver inside the network that owns it:

```
on-prem / other cloud resolver   ── forward "internal.example" ──▶  inbound endpoint in the VPC/VNet
                                                                       AWS: Route 53 Resolver inbound endpoint
                                                                       Azure: DNS Private Resolver inbound endpoint
                                                                       (GCP: inbound server policy)
cloud workloads needing on-prem names ── outbound endpoint + forwarding rule ──▶ on-prem DNS servers
```

The platform resolvers (VPC base + 2, 168.63.129.16, 169.254.169.254) are reachable only from inside their own network, which is why the inbound endpoints exist. Two frequent mistakes: setting a VNet or VPC to use **custom DNS servers** that do not forward to the platform resolver (private zones and private endpoints stop resolving), and forwarding loops between two resolvers that each forward the zone to the other (SERVFAIL after a timeout).

### Go's resolvers

Go has two: a **pure Go** resolver that reads `/etc/resolv.conf`, `/etc/hosts` and `/etc/nsswitch.conf` itself and sends its own queries, and the **cgo** resolver that calls the C library's `getaddrinfo`. On Linux the pure Go resolver is used unless something forces cgo (an `nsswitch.conf` source Go does not understand, certain resolv.conf options, or a build setting). `GODEBUG=netdns=go+1` (or `cgo+1`) forces one and prints which was chosen; the lab's check shows `go package net: GODEBUG setting forcing use of the Go resolver` followed by an answer from `/etc/hosts`. Neither caches. A custom `net.Resolver` with its own `Dial` function can send queries to a chosen server — useful for tools, dangerous when it silently bypasses the system's split-horizon configuration.

---

## 12.12 DNS Troubleshooting Tools

```bash
dig NAME [TYPE]                      # the system's first nameserver; full output
dig @SERVER NAME                     # a specific resolver or authoritative server
dig +norecurse @AUTH NAME            # ask an authoritative server; expect aa
dig +trace NAME                      # iterate from the root (own walk, no cache)
dig +short NAME                      # just the data
dig +noall +answer NAME              # just the answer section (+authority, +additional, +comments)
dig +tcp / +notcp NAME               # force a transport
dig +dnssec NAME                     # request signatures (DO); look for "ad" in the flags
dig +cd NAME                         # checking disabled: does validation cause the failure?
dig +bufsize=512 / +noedns NAME      # test small-buffer and no-EDNS behaviour
dig +tries=1 +time=2 NAME            # control dig's own retries (default 3 tries × 5 s)
getent ahosts NAME                   # what applications using glibc get (hosts file, search list…)
resolvectl status | query NAME | statistics | flush-caches     # systemd-resolved
sudo tcpdump -ni any port 53         # what actually leaves the host
dnslab trace / watch / query         # the course tool: explained trace, change tracking
```

### Reading dig output

```
;; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN, id: 16249
;; flags: qr rd ra; QUERY: 1, ANSWER: 0, AUTHORITY: 1, ADDITIONAL: 1
     qr  this is a response              rd  recursion desired (we asked for it)
     aa  authoritative answer            ra  recursion available (the server recurses)
     tc  truncated (retry over TCP)      ad  authenticated data (DNSSEC-validated)
     cd  checking disabled (we asked the resolver not to validate)
;; OPT PSEUDOSECTION:
; EDNS: version: 0, flags:; udp: 1232      ← the server's EDNS buffer; "flags: do" when DNSSEC was asked
; EDE: 7 (Signature Expired): (…)          ← Extended DNS Error: WHY it failed
; COOKIE: … (good)                         ← DNS cookies (RFC 7873), anti-spoofing
;; AUTHORITY SECTION:
shop.test.   20  IN  SOA  ns.shop.test. hostmaster.shop.test. 2 3600 900 604800 3600
             ↑ remaining TTL           serial refresh retry expire MINIMUM
;; Query time: 0 msec                      ← 0–1 ms usually means a cache hit nearby
;; SERVER: 198.18.60.2#53(198.18.60.2) (UDP)    ← WHO answered, and over what
;; MSG SIZE  rcvd: 92
";; communications error to 198.18.60.2#53: timed out"   ← no reply at all: no status to read
```

`dig` sets the AD bit in its queries by default (asking "tell me if you validated"); that is why an AD flag can appear on odd responses such as REFUSED. `nslookup` and `host` are fine for quick checks but hide flags and EDNS, and `nslookup` has its own resolver logic that can disagree with the system's.

---

## 12.13 Common DNS Failure Patterns

### NXDOMAIN — the name does not exist (here)

The authority (or a cached negative answer) says the name has no records of any type. Causes: a typo; a deleted record; a domain whose registration expired; the wrong view or the wrong resolver (split horizon); a search-list expansion that never reached the intended name; a cached negative answer from before the record was created. Confirm by asking the authoritative server directly (`dig +norecurse @NS name`): if it answers, a cache is stale — wait out the negative TTL (`min(SOA TTL, MINIMUM)`) or flush it.

### NODATA — NOERROR with an empty answer

The name exists, but not with this type: an IPv6-only lookup of an IPv4-only name, an MX query for a name without mail. Often reported by applications as "not found".

### SERVFAIL — the resolver could not produce an answer

Not "the resolver is down" — the resolver is answering. It failed to get a usable answer: authoritative servers unreachable or timing out; a lame delegation (the servers refused or did not serve the zone — the lab's `broken.test`, with Unbound logging `from 198.18.52.2 got REFUSED`); DNSSEC validation failure (bogus — check the EDE and try `+cd`); a forwarding loop. Resolvers cache SERVFAILs briefly (RFC 9520 requires at least 1 s, at most 5 minutes) to protect broken servers from retry storms.

### Timeout — no response at all

Either the resolver itself is unreachable (wrong address in `resolv.conf`, UDP/53 blocked, resolver down) or **the resolver is still working** on a slow or dead upstream and the client gave up first. The lab's `slow.test` delegation points at a black-holed address. The client's first attempts got no reply at all (`;; communications error … timed out`, dig exit status 9): Unbound was still retrying the dead server, and it deliberately drops client requests that have waited longer than about 2 s (`discard-timeout`) instead of answering late. In all three lab runs it had given up 12 s later (18–29 s in by-hand experiments with other query patterns) on that server and from then on answered SERVFAIL in under a millisecond. So "DNS timeouts" can be an authoritative problem showing through a healthy resolver. Separate the two with `dig @resolver` for a name you know works: a fast answer means the resolver is fine and the problem is upstream of it.

### REFUSED — policy

The server will not answer this client or this question: a resolver's access control (the lab's `EDE: 18 (Prohibited)`), an authoritative server asked about a zone it does not serve (lame delegation, seen from the resolver), or rate limiting. Ask another resolver to confirm the name is fine.

### The search list, ndots and `/etc/hosts`

Unexpected names in a capture (`api.example.com.default.svc.cluster.local`), three or more NXDOMAINs before each success, and hosts-file overrides are stub problems (12.10). In Kubernetes, the conntrack race that drops one of the parallel A/AAAA queries produces the classic **5-second delay** (glibc's default timeout) — Module 10 covers it.

### `/etc/resolv.conf` keeps changing

On Ubuntu it is a symlink managed by systemd-resolved (`ls -l /etc/resolv.conf`); DHCP, netplan and VPN clients rewrite the configuration behind it. Configure DNS where it is managed (netplan, NetworkManager, `resolvectl` for runtime tests) and check the real servers with `resolvectl status`.

---

## Hands-On Lab: A Private DNS, From the Root Down

### Lab Goals

- Walk the hierarchy with `dig +norecurse` and `dnslab trace`, including a CNAME into another zone
- Capture a validating resolver's upstream queries (QNAME minimisation, DNSKEY fetches) and its cache
- Change a record and create a name, and measure when each cache sees it
- Produce NODATA, truncation and TCP retry, a lame delegation, a dead delegation, REFUSED, and DNSSEC secure, insecure and bogus answers
- See split-horizon views from two networks
- Measure glibc's search list, ndots, nameserver timeouts and `/etc/hosts` precedence, and Go's resolver choice

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `bind9`, `bind9-utils` (dnssec-keygen, dnssec-signzone, rndc), `unbound`, `bind9-dnsutils` (dig), `tcpdump`
- Ubuntu starts the packaged `named` and `unbound` services on the host when installed; they do not interfere with the lab's namespaces. If AppArmor blocks the lab's `named` (look for `apparmor="DENIED"` in `dmesg`), put the profile in complain mode with `sudo aa-complain /usr/sbin/named` while you work.
- Part 3 uses your own machine and the internet

The lab uses [`dnslab`](../labs/go/cmd/dnslab), written for this module with the Go standard library only — its DNS message encoder and decoder ([`msg.go`](../labs/go/cmd/dnslab/msg.go)) is short enough to read. `dnslab trace` resolves iteratively from the root, explaining each referral, glue record, CNAME restart, truncation and lame server; `dnslab watch` polls several servers and prints the moment each one's answer changes; `dnslab query` prints a single exchange with its negative-caching TTL; `dnslab lookup` uses Go's own resolver.

The lab's namespaces get their own `/etc/resolv.conf`, `/etc/hosts` and BIND/Unbound configuration through `netlab`'s per-node files, which are written to `/etc/netns/<namespace>/` — the standard iproute2 mechanism that `ip netns exec` overlays on `/etc` — and removed at teardown.

---

### Lab Part 1: Run the DNS Lab

[`dns.json`](../labs/netlab/dns.json):

```
                                 ┌──────── net (router) ────────┐
  root     198.18.50.2  "."       │                              │   resolver 198.18.60.2  Unbound, validating,
  tld      198.18.51.2  "test."   │                              │            serves 198.18.61.0/24 only
  auth1    198.18.52.2/.3         │   198.18.53.0/24 is          │   public   198.18.62.2  Unbound, serves anyone
           corp.test (2 views),   │   black-holed (slow.test)    │   office   198.18.61.10 → resolver
           secure.test, expired.test                             │   cloud    198.18.63.10 → public
  auth2    198.18.54.2  shop.test │                              │
                                 └──────────────────────────────┘
  test. delegates: corp (auth1), shop (auth2), secure + expired (auth1, signed, with DS),
                   broken (→ auth1, which does not serve it: lame), slow (→ 198.18.53.2: dead)
  ".", "test.", secure.test and expired.test are signed at setup (expired.test with signatures
  that expired yesterday); both resolvers trust the lab's own root key.
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/dns.json        # about 1.5 minutes: it waits out real TTLs
```

Excerpts from real runs (the full outputs are in the evidence directory):

**The hierarchy and the resolver** — checks 1–8 produce the referrals, traces and capture shown in 12.2 and 12.5. **Caching** — checks 9–15 produce the change and negative-caching timelines in 12.4. Three runs:

| Run | Office resolver switched to .20 | Public resolver switched | `new.shop.test` visible via resolver after creation |
| --- | --- | --- | --- |
| 1 | 8.5 s | 10.5 s | 20.5 s |
| 2 | 8.0 s | 11.0 s | 21.0 s |
| 3 | 7.5 s | 10.5 s | 20.5 s |

**Transport and failures** — checks 16–19:

```
check 16/32: a 1.6 KB TXT answer does not fit the 1232-byte EDNS buffer: truncated over UDP, repeated over TCP
  | NOERROR from 198.18.60.2:53 over UDP truncated (tc), then TCP in 1.9 ms, 1662 bytes
check 17/32: a lame delegation: broken.test. points at a server that does not serve it …
  | ;; ->>HEADER<<- opcode: QUERY, status: SERVFAIL
  | … SERVFAIL <x.broken.test. A IN>: all servers for this domain failed, at zone broken.test. from 198.18.52.2 got REFUSED
check 18/32: a dead delegation: slow.test.'s only server is unreachable; the client times out before the resolver gives up
  | ;; communications error to 198.18.60.2#53: timed out
  | ;; no servers could be reached
  | exit 9 after 4031 ms
check 19/32: keep asking …
  | 2s: timed out
  | 5s: timed out
  | 8s: timed out
  | 11s: timed out
  | 12s: status: SERVFAIL
  | exit 0 after 28 ms
```

**DNSSEC, policy and views** — checks 20–26 produce the secure/insecure/bogus table in 12.7, `REFUSED` with `EDE: 18 (Prohibited)`, and the two views in 12.8.

**The stub** — checks 27–32 produce the search-list capture, the 2006 ms dead-nameserver penalty, the hosts-file override and Go's resolver line shown in 12.10–12.11.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/dns.json
alias lab='sudo ./bin/netlab exec ../netlab/dns.json'
```

1. **Look inside the resolver's cache.** `lab office -- dig app.shop.test`, then `lab resolver -- unbound-control dump_cache | grep shop.test`: the A record with its remaining TTL, the NS set and glue from the referral (TTL 3600), and the signed NSEC record from `test.` that proved `shop.test` has no DS — the reason the answer is insecure rather than bogus. `lab resolver -- unbound-control flush app.shop.test` removes one name; the next query goes upstream again.
2. **Serve stale.** `lab auth2 -- rndc stop` kills shop.test's only server. Wait 12 s for the cached copy to expire: `lab office -- dig +tries=1 +time=3 app.shop.test` now times out (the resolver is retrying a dead server, as with `slow.test`). Then `lab resolver -- unbound-control set_option serve-expired: yes` and ask again: the expired answer comes back at once, `app.shop.test. 30 IN A 198.18.70.10` — RFC 8767's trade of freshness for availability, with a 30 s TTL so clients come back soon.
3. **A trace does not validate.** `lab office -- ./bin/dnslab trace www.expired.test` returns 198.18.70.50 without complaint: iterating is not validating. Only the validating resolver turns the expired signatures into SERVFAIL.
4. **Force TCP from the stub.** `lab office -- sh -c 'printf "nameserver 198.18.60.2\noptions use-vc\n" > /etc/resolv.conf'`, then capture on the office's `eth0` while running `getent ahosts www.secure.test`: ten packets — handshake, query, ACK, answer, ACK, and a four-packet close — for one name, where UDP needs two.
5. **Two stable answers.** `lab office -- ./bin/dnslab watch -s 198.18.60.2 -for 3s api.corp.test` reports `no change: 10.20.0.10`; the same from `cloud` against 198.18.62.2 reports `no change: 192.0.2.10`. Neither is wrong: split horizon is a deliberate, consistent difference, unlike a cache in transition.

`sudo ./bin/netlab down ../netlab/dns.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
cat /etc/resolv.conf; resolvectl status          # which resolvers, per interface
getent ahosts example.com; dig example.com       # the system's view vs a direct query
dig +trace example.com                           # the real hierarchy: root → com → example.com
dig example.com +dnssec | grep -E 'flags|RRSIG'  # ad? (depends on whether your resolver validates)
dig @1.1.1.1 dnssec-failed.org; dig @1.1.1.1 +cd dnssec-failed.org    # a deliberately broken zone
resolvectl statistics                            # systemd-resolved's cache hits and misses
sudo tcpdump -ni any port 53 & curl -sI https://example.com >/dev/null   # what one request resolves
```

With Docker, the Kubernetes search-list behaviour can be reproduced against the internet: `docker run --rm --dns-search=default.svc.cluster.local --dns-search=svc.cluster.local --dns-search=cluster.local --dns-option=ndots:5 alpine nslookup example.com` while capturing on the host shows the suffixed queries first. (Not run here: the lab environment has no Docker daemon; the netlab check shows the same behaviour with glibc.)

---

## Real-World Troubleshooting Scenario

### Scenario: "The Application Works From The Office But Fails From The Cloud"

**The situation.** A team deploys a service to AWS ECS. It must call an internal API, `api.internal.example.com`. From developer laptops on the corporate network, `curl https://api.internal.example.com` works. From the ECS tasks it fails with `Could not resolve host: api.internal.example.com`. "The record is in Route 53," the team says. "The VPC is right."

**Step 1 — Resolve the way the application does, where it runs.**

```bash
aws ecs execute-command --cluster prod --task <task-id> --container app --interactive --command "/bin/sh"
getent hosts api.internal.example.com; echo "exit $?"     # nothing, exit 2
dig api.internal.example.com
;; ->>HEADER<<- opcode: QUERY, status: NXDOMAIN
```

The name really does not resolve from here. It is not a network or TLS problem: the failure happens before any connection is attempted.

**Step 2 — Which resolver, and what search list?**

```bash
cat /etc/resolv.conf
nameserver 10.0.0.2
search us-east-1.compute.internal
```

10.0.0.2 is the VPC resolver (base + 2): correct, and it is the only resolver that can see private hosted zones.

**Step 3 — Compare with the authoritative data.** The record exists in a Route 53 **private** hosted zone. Private zones answer only for the VPCs they are associated with:

```bash
aws route53 get-hosted-zone --id Z1234567 --query 'VPCs'
[ { "VPCRegion": "us-east-1", "VPCId": "vpc-0staging" } ]
```

The zone is associated with the staging VPC, not the production VPC the tasks run in. For the production VPC, the resolver falls through to public DNS, where `internal.example.com` has no such name: NXDOMAIN.

**Step 4 — Why it works from the office.** The corporate DNS servers hold their own copy of `internal.example.com` (or forward it to a Route 53 inbound endpoint in the staging VPC). The laptops are in a different view entirely — and may well be reaching a different instance of the API.

**The fix.** Associate the zone with the production VPC (`aws route53 associate-vpc-with-hosted-zone --hosted-zone-id Z1234567 --vpc VPCRegion=us-east-1,VPCId=vpc-0prod`), wait out the negative TTL the resolver may have cached, and test again from inside the task. Then fix the process: create the association in the same Terraform/CloudFormation change as the VPC, and decide deliberately which view the office should see.

**What made it fast:** resolving from inside the workload; reading the status (NXDOMAIN, not SERVFAIL or a timeout, so the resolver worked and the name was absent in its view); knowing private zones are scoped to networks; and remembering that the office's success proves nothing about the cloud's view.

---

## Common Failure Patterns

| Symptom | DNS cause | How to confirm | Fix |
| --- | --- | --- | --- |
| NXDOMAIN for a name that exists | Wrong view/resolver; cached negative answer; search-list expansion | `dig +norecurse @AUTH name`; `dig @OTHER_RESOLVER`; check SOA TTL/MINIMUM | Use the right resolver/view; wait out min(SOA TTL, MINIMUM); FQDN with trailing dot |
| New record "not working" for minutes | Name was queried before creation (negative cache) | Resolver shows NXDOMAIN with a counting-down SOA TTL | Wait; avoid pre-creation lookups; keep the SOA TTL modest |
| Old address after a change | Positive caches (resolver, JVM, client connection reuse) | TTL in `dig` counting down; `dnslab watch` | Lower TTL in advance; keep the old endpoint up; restart clients that resolve once |
| SERVFAIL for one zone only | Authoritative unreachable, lame delegation, or DNSSEC bogus | `dig +cd`; EDE code; `dig +trace` / `dnslab trace`; resolver logs | Fix the servers/delegation; fix signatures or the DS |
| SERVFAIL fixed by `+cd` | DNSSEC: expired RRSIG, DS/DNSKEY mismatch, resolver clock | EDE 6/7/9/10; DNSViz; `delv` | Re-sign; correct the DS at the registrar; fix NTP |
| Client times out, other names fine | Resolver waiting on a dead authority (it drops stale requests) | `dig @resolver known-good-name` is fast; trace the failing zone | Fix the authority; client retries will get SERVFAIL soon |
| All lookups time out | Resolver unreachable, port 53 blocked | `dig @resolver`; tcpdump port 53 on both sides | Fix `resolv.conf`/firewall/resolver |
| Every lookup is slow by exactly N s | First nameserver dead (stub tries it every time) | Time `getent ahosts`; tcpdump shows queries to the dead server first | Remove it; use a caching stub (systemd-resolved, node-local cache) |
| REFUSED | Resolver access control, or asking an authority about a zone it does not serve | EDE 18; `dig @other-resolver` | Use an allowed resolver; fix the delegation |
| Large answers fail, small work | TCP/53 blocked, or fragments dropped | `dig +tcp`; `dig +bufsize=512` vs default | Allow TCP/53; EDNS 1232 |
| Works outside the VPC, not inside (or vice versa) | Private zone not associated / public record missing / custom DNS not forwarding | `dig @platform-resolver` from each network | Associate/link the zone; forward to the platform resolver |
| Private endpoint resolves to a public IP | Client's resolver cannot see the `privatelink` zone | CNAME chain shows `privatelink…` then a public address | Link the private zone; conditional forwarding to an inbound endpoint |
| Lookups 3–5× slower in pods | ndots:5 search-list expansion | tcpdump shows `.svc.cluster.local` suffixes | Trailing dots; lower ndots; NodeLocal DNSCache |
| 5-second stalls in pods | Parallel A/AAAA lost to a conntrack race (Module 10) | tcpdump: a query with no answer, retried after 5 s | `single-request-reopen`, NodeLocal DNSCache, newer kernels |
| `dig` and the application disagree | `/etc/hosts`, nsswitch order, a different resolver library | `getent hosts name` vs `dig` | Fix the hosts entry; test with the application's resolver |
| `/etc/resolv.conf` edits disappear | Managed by systemd-resolved / DHCP / netplan | `ls -l /etc/resolv.conf` (a symlink) | Configure the manager, not the file |

---

## Summary

- A stub sends one recursive query to a recursive resolver, which iterates from the root using referrals and glue to the authoritative servers, validates (if configured), and caches everything it learns. Modern resolvers minimise the names they reveal (QNAME minimisation).
- `aa` marks an authoritative answer; a lower-than-configured, falling TTL marks a cached one. `ad` marks a DNSSEC-validated answer; its absence on an unsigned zone is normal.
- Each cache holds a changed record until its own copy expires: the lab's two resolvers switched 8 s and 11 s after a change to a 10 s record. Negative answers are cached for min(SOA TTL, MINIMUM) — 20 s in the lab, not the hour MINIMUM alone suggests.
- NXDOMAIN (no such name), NODATA (no such type), SERVFAIL (the resolver could not get a usable answer), REFUSED (policy), and timeouts (no answer — possibly a resolver still busy with a dead authority) point at different layers. Extended DNS Errors say why; `+cd` separates DNSSEC failures from the rest.
- DNS uses UDP with EDNS (1232 bytes), and TCP for anything larger; blocking TCP/53 breaks large answers only.
- Split horizon and cloud private zones answer according to which resolver asks; private endpoints depend on the client's resolver seeing the `privatelink` zone; cross-network resolution needs conditional forwarding to inbound endpoints.
- The stub's search list and `ndots` multiply queries, a dead first nameserver adds its timeout to every lookup (glibc caches nothing), and `/etc/hosts` overrides DNS for applications but not for `dig`.

## What's Next

Module 13 covers TLS: the handshake that follows the TCP handshake, certificate validation — where the name you resolved here must match the certificate — and the failures that look like network problems but are not.

---

## Quick Reference Card

```
MODULE 12: DNS — QUICK REFERENCE
════════════════════════════════

PATH        app → stub (glibc/musl/Go/systemd-resolved) → recursive resolver → root → TLD → authoritative
            referral = no answer + NS in authority (+ glue in additional); answer from authority has aa

DIG         dig @SERVER NAME TYPE   +norecurse  +trace  +short  +noall +answer  +tcp  +dnssec  +cd
            +bufsize=N  +noedns  +tries=1 +time=2   (exit 9 = no reply)
            getent ahosts NAME = what glibc applications get
FLAGS       qr response · aa authoritative · tc truncated · rd/ra recursion desired/available
            ad validated · cd checking disabled · EDNS "udp: 1232" · "flags: do" = DNSSEC asked
STATUS      NOERROR (+ empty answer = NODATA) · NXDOMAIN · SERVFAIL · REFUSED · (timeout: no status)
EDE         6 DNSSEC Bogus · 7 Signature Expired · 9 DNSKEY Missing · 10 RRSIGs Missing
            15 Blocked · 18 Prohibited · 22 No Reachable Authority · 23 Network Error

CACHING     cached TTL counts down; each cache expires independently
            negative TTL = min(SOA record TTL, SOA MINIMUM)        (RFC 2308)
            SERVFAIL cached 1 s – 5 min (RFC 9520); serve-stale (RFC 8767)
            glibc/musl/Go cache nothing; JVM 30 s / 10 s negative; systemd-resolved caches
MIGRATION   lower TTL ≥ old TTL ahead · change · keep old endpoint · verify · raise TTL

TRANSPORT   UDP 53 (EDNS 1232) · TCP 53 for tc/AXFR (must be allowed) · DoT 853 · DoH 443 · DoQ 853/udp

DNSSEC      DS (parent) → DNSKEY → RRSIG; secure = ad; insecure = no ad (normal); bogus = SERVFAIL
            diagnose: dig +cd, EDE, delv, DNSViz — never "turn validation off"

STUB        /etc/nsswitch.conf  hosts: files … dns       (/etc/hosts first)
            /etc/resolv.conf    nameserver (≤3) · search · options ndots:N timeout:S attempts:N
                                rotate · use-vc · single-request(-reopen) · edns0 · trust-ad
            names with < ndots dots: search suffixes first; trailing dot = absolute
            dead first nameserver: + timeout on EVERY lookup
            Ubuntu: 127.0.0.53 = systemd-resolved; resolvectl status/query/statistics/flush-caches
            containers: Docker 127.0.0.11; Kubernetes ndots:5 + 3 search suffixes
            Go: GODEBUG=netdns=go+1 | cgo+1

CLOUD       AWS VPC resolver base+2 / 169.254.169.253 · Azure 168.63.129.16 · GCP 169.254.169.254
            private zones scoped to VPCs/VNets; privatelink CNAME pattern
            hybrid: conditional forwarding → Route 53 Resolver / Azure DNS Private Resolver inbound endpoints

RESERVED    test. invalid. localhost. example. (RFC 6761, 2606) · home.arpa (RFC 8375)
            .internal (reserved by ICANN for private use)
```

---

## References

- RFC 1034 / RFC 1035 — Domain Names: Concepts and Facilities; Implementation and Specification
- RFC 2181 — Clarifications to the DNS Specification
- RFC 2308 — Negative Caching of DNS Queries
- RFC 6891 — Extension Mechanisms for DNS (EDNS(0)); DNS Flag Day 2020 (dnsflagday.net)
- RFC 7766 — DNS Transport over TCP
- RFC 7858 / RFC 8484 / RFC 9250 — DNS over TLS / HTTPS / QUIC
- RFC 4033, 4034, 4035 — DNSSEC
- RFC 8145 — Signaling Trust Anchor Knowledge in DNSSEC
- RFC 8767 — Serving Stale Data to Improve DNS Resiliency
- RFC 8914 — Extended DNS Errors
- RFC 9156 — DNS Query Name Minimisation
- RFC 9520 — Negative Caching of DNS Resolution Failures
- RFC 8482 — Minimal Responses to ANY Queries
- RFC 9460 — SVCB and HTTPS Resource Records
- RFC 6761 — Special-Use Domain Names; RFC 2606 — Reserved Top Level DNS Names
- RFC 7871 — Client Subnet in DNS Queries
- `man 5 resolv.conf`, `man 5 nsswitch.conf`, `man 1 dig`, `man 1 resolvectl`, `man 5 unbound.conf`, BIND 9 Administrator Reference Manual
- Kubernetes: DNS for Services and Pods — https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/
- AWS Route 53 private hosted zones and Resolver endpoints; Azure Private DNS zones, Private Endpoint DNS configuration and DNS Private Resolver; Google Cloud DNS private zones and server policies
