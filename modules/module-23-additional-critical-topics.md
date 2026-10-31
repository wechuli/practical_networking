# Module 23: Additional Critical Topics

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 22 — Observability & Troubleshooting Methodology
> **Estimated time:** 4 hours

## Overview

This module covers four things found in every production environment that don't belong to any single earlier module:

- **DHCP:** how hosts get an address, a gateway and DNS servers, and what fails when it breaks.
- **Load balancers:** how traffic is spread across backends, how dead backends are detected, and what clients see during the gap.
- **Time:** how NTP measures a clock's error, why it can be fooled, and what a wrong clock breaks.
- **Proxies:** forward proxies, reverse proxies, and how the client's address survives them: X-Forwarded-For and the PROXY protocol.

Three labs measure each of these on the wire:

- [`labs/netlab/dhcp.json`](../labs/netlab/dhcp.json) runs dnsmasq, busybox udhcpc, a rogue server, a bridge filter and a relay.
- [`labs/netlab/edge.json`](../labs/netlab/edge.json) runs HAProxy 2.8 and nginx 1.24 in front of three backends, plus a forward proxy.
- [`labs/netlab/time.json`](../labs/netlab/time.json) runs five NTP servers, two of them wrong on purpose, then chrony and a TLS client with a shifted clock.

The backends, forward proxy, NTP servers and TLS clock client come from the course's [`edgelab`](../labs/go/cmd/edgelab) tool. Each backend reports what it can see about a request: its TCP peer, the X-Forwarded-For values and the PROXY-protocol client.

## What You Will Build Toward

- Read a DHCP exchange on the wire, and recognize the four main ways it fails: no server, an exhausted pool, a rogue server, and a missing relay
- Explain why a TCP health check can pass while the application fails, and estimate how long clients suffer before a backend is removed
- Know which entries of X-Forwarded-For you can trust, and what happens when PROXY-protocol settings don't match
- Work out NTP offset and delay from the four timestamps, and explain why asymmetric paths and two-source setups mislead
- Configure and debug clients behind a forward proxy (`http_proxy`, `NO_PROXY`, CONNECT)

---

## 23.1 DHCP

### The exchange (DORA)

DHCPv4 (RFC 2131) runs over UDP: clients send from port 68 to port 67, servers send from 67 to 68. Captured in the lab and decoded with `tshark`, a first lease looks like this. The client is c1 and the server is dnsmasq on 198.18.230.1.

```
  0.000s  DISCOVER 0.0.0.0 → 255.255.255.255
  3.060s  OFFER    198.18.230.1 → 198.18.230.101  yiaddr 198.18.230.101  router 198.18.230.1  lease 120s
  3.068s  REQUEST  0.0.0.0 → 255.255.255.255  requested 198.18.230.101
  3.070s  ACK      198.18.230.1 → 198.18.230.101  yiaddr 198.18.230.101  router 198.18.230.1  lease 120s
```

- **DISCOVER** is broadcast from 0.0.0.0, because the client has no address yet.
- **OFFER** carries the proposed address (`yiaddr`) and options: subnet mask, router (option 3), DNS servers (option 6) and lease time (option 51).

  The OFFER was **unicast** to the offered address, sent to the client's MAC. Diagrams often show it as broadcast. A server broadcasts the OFFER only if the client sets the *broadcast flag* because it can't receive unicast before it is configured (RFC 2131 §4.1). udhcpc didn't set the flag.
- **REQUEST** is broadcast again, still from 0.0.0.0. It names the chosen server and address, so every server that made an offer hears which one won.
- **ACK** commits the lease. The client then configures the address and default route itself. In the lab, a small script that udhcpc calls does this; on a normal system, dhclient, systemd-networkd or NetworkManager does it.

**Why the OFFER took 3 seconds.** dnsmasq pings an address before offering it, to avoid handing out one that is already in use. For a new address the OFFER arrived 3.0 s after the DISCOVER in every run. For a client that already held a lease it arrived within 1 ms. Other servers can be configured to do the same "ping before offer" check. The delay matters for a rogue server, as the failure modes below show.

### Lease lifecycle

| Time (default) | Client sends | To |
| --- | --- | --- |
| T1 = 50 % of lease | DHCPREQUEST with `ciaddr` set (renewing) | unicast, to the server that granted the lease |
| T2 = 87.5 % | DHCPREQUEST (rebinding) | broadcast, so any server may answer |
| 100 % | nothing: the client must stop using the address | then DISCOVER again |

Measured:

- **Renewal.** With a 120 s lease, a by-hand run renewed 60.1 s after binding. The renewal was one unicast REQUEST from 198.18.230.101 to 198.18.230.1 and one unicast ACK, with no DISCOVER. The lab forces the renewal early with SIGUSR1 to udhcpc and records the same two packets (`renew.txt`).
- **Release.** SIGUSR2 sent a unicast RELEASE, and the server deleted the lease at once (`release.txt`). Without a release, the address stays reserved until the lease runs out. That is one way pools leak, for example VMs destroyed without a clean shutdown.
- **Same address again.** A client that asks again usually gets the same address back. dnsmasq keys leases on the client identifier, which by default is derived from the MAC. A *reservation* (`dhcp-host=MAC,IP` in dnsmasq, a static mapping elsewhere) makes this permanent.

### How it fails

All of the following were measured in the lab; the evidence files are in the evidence directory.

- **No server reachable.** The lab took the server's switch port down. c1 sent four DISCOVERs at roughly 1 s intervals, printed `leasefail`, and was left **with no IPv4 address at all**.

  Windows and macOS fall back to an IPv4 link-local address in 169.254.0.0/16 (APIPA). On Linux, that happens only if something is configured to do it, such as avahi-autoipd or NetworkManager's link-local setting. So "no 169.254 address" doesn't mean DHCP worked, and a 169.254 address means DHCP failed *on clients that fall back*. IPv6 link-local addresses (fe80::/10) exist regardless of DHCPv4 and tell you nothing about it.
- **Pool exhausted.** The range had two addresses; c1 and c2 took them. c3's DISCOVERs got **no reply at all**. A DHCP server doesn't send "no addresses left" to the client. The only evidence is in the server's log:

  ```
        3 DHCPDISCOVER(eth0) 56:85:8d:7f:5f:e2 no address available
  ```

  From the client side, an exhausted pool looks exactly like a dead server. Check the server's logs and lease table, not just the client.
- **Rogue server.** A second dnsmasq on the same segment answered too. It was set up the way an attacker's tool would be: no ping before offering, with itself as gateway and DNS. The client took **the first OFFER**. In 12 rounds across three runs, the rogue won 11:

  ```
  udhcpc bound: 198.18.230.150/24 router 198.18.230.66 dns 198.18.230.66 lease 120 server 198.18.230.66
    0.000s  OFFER    198.18.230.66 → 198.18.230.150  …  router 198.18.230.66
    0.000s  OFFER    198.18.230.1 → 198.18.230.100  …  router 198.18.230.1
  ```

  A client that bound to the rogue sends all its traffic and DNS queries to the attacker. Misconfigured devices cause the same symptom: a home router plugged in backwards, or a test VM running a DHCP server.

  When that client later asked the real server for its rogue address, the server answered with **NAKs**. dnsmasq was run with `--dhcp-authoritative`, so it refuses requests for addresses it didn't assign, and the client went back to DISCOVER.
- **DHCP snooping.** Switches defend against rogue servers by trusting server-to-client DHCP (source port 67) only on designated ports. The lab built the same thing by hand: one `nft` rule in the bridge's forward hook drops UDP source port 67 arriving on any port except the real server's and the relay's. The rogue's OFFERs were dropped at the "switch" (the counter showed 1–2 packets), and c3 got only the real server's offer.

### Relay

Broadcasts don't cross routers, so a server on another subnet never hears a DISCOVER directly. A **relay agent** on the router does the forwarding. The router feature is often called `ip helper-address`. The relay works like this:

1. It receives the client's broadcast.
2. It sets `giaddr` to its own address on the client's subnet.
3. It unicasts the message to the server.
4. The server chooses the address range *by giaddr* and replies to the relay, which passes the reply on to the client.

Measured, for the client "remote" on 198.18.231.0/24, behind the relay:

```
  0.000s  DISCOVER 198.18.231.1 → 198.18.230.1  giaddr 198.18.231.1
  3.006s  OFFER    198.18.230.1 → 198.18.231.1  yiaddr 198.18.231.107  giaddr 198.18.231.1  router 198.18.231.1  lease 120s
remote: 198.18.231.107/24 default 198.18.231.1
```

The client received an address from the second range and the router for *its own* subnet. Two details matter in practice:

- **Per-range options.** The router option has to be set per range. In the first attempt only a global router option was set, and the remote client was told to use 198.18.230.1 as its gateway, which isn't on its subnet. The server log shows which range was chosen.
- **The server needs a route back to giaddr.** dnsmasq's relay sent the forwarded packets *from* its client-side address 198.18.231.1, so the server's route back to that subnet had to exist.

### Practical notes

- **Cloud networks.** AWS, Azure and GCP run DHCP for you. You control the options it hands out, not the protocol. In AWS, the VPC's *DHCP options set* sets the domain name, DNS servers and NTP servers. A wrong options set breaks DNS for every new instance.
- **Containers.** Containers on a Docker bridge get addresses from Docker's own address management (IPAM), not from DHCP. Watching a container start is not a DHCP demonstration.
- **Checksum offload.** In the lab, busybox udhcpc **ignored** every OFFER sent from a veth interface with TX checksum offload enabled. Those packets leave with a partial checksum that the kernel normally completes later. dhclient accepted the same packets. The old `labs/linux/dhcp.sh` lab used dhclient and passed three times without any change. The fix was `ethtool -K eth0 tx off` on the server side. If a container- or VM-based DHCP setup gets "DISCOVER, OFFER, DISCOVER, OFFER…", capture at the client and look at the UDP checksum.

---

## 23.2 Load Balancers

### L4 and L7

- **An L4 load balancer** forwards TCP connections or UDP flows. Examples are HAProxy `mode tcp`, AWS NLB and Azure Load Balancer.
  - It never parses HTTP, so it can't route by path, set headers or retry a request.
  - TLS can pass through it to the backend.
  - The backend sees either the client's address (if the LB doesn't proxy the connection) or the LB's address, in which case the client address has to be sent another way (the PROXY protocol, below).
- **An L7 load balancer** ends the client's connection, parses each request, and opens its own connections to the backends. Examples are HAProxy `mode http`, nginx, Envoy, AWS ALB, Azure Application Gateway and Google's HTTP(S) load balancers.
  - It can route by host, path or header, terminate TLS, add headers, retry, and reuse backend connections.
  - The backend always sees the LB as the peer. In every L7 check in the lab, `peer=198.18.233.1:…`, and the client appeared only in X-Forwarded-For.

### Spreading the load

**Round robin** sent 30 new connections 10/10/10 across three backends. Other algorithms:

- **least connections** suits long or uneven requests;
- **weighted** variants serve backends of different sizes;
- **hashing** (by source address, URI or a header) keeps the same key on the same backend.

Hashing algorithms are where "stickiness" comes from (below).

### Health checks

The health-checks check measured a backend whose application fails while its port stays open. b2's `/health` and its pages returned 500.

| Check type | b2 marked | Traffic to b2 (30 requests) |
| --- | --- | --- |
| HTTP: `option httpchk GET /health`, expects 200 | **DOWN** within ~1 s: `Layer7 wrong status, code: 500` | 0. b1 and b3 got 15 each |
| TCP: `check`, connect only | **UP** | 10 requests, all **HTTP 500** |

A TCP check only proves that something accepted a connection. Make the HTTP health endpoint exercise what the application needs to serve a request. Keep it cheap, though, and don't let a shared dependency fail every backend at once: if the database is down and every check fails, the load balancer has nothing left to send traffic to.

**How long until a dead backend leaves the pool.** Detection takes about `fall × inter`, plus a check timeout if the host is silent. The lab used `inter 500ms fall 2 rise 2`. In a by-hand run, a backend switched to returning 503 showed `UP 1/2` after about 0.6 s and `DOWN` after about 1 s. It came back after `rise 2` passing checks. Cloud defaults are much slower: AWS target groups default to a 30 s interval and an unhealthy threshold of 2.

What clients experience during that window depends on how the backend failed and on retry settings. The failover checks blocked b3 at 1.5 s into a run of sequential requests, 50 ms apart; the results were identical in all three runs.

| b3 fails by | HAProxy defaults (`retries 3`) | With `option redispatch 1` |
| --- | --- | --- |
| **DROP** (host silent) | 1 request waited **4.0 s** (4 × `timeout connect 1s`), then **503** | 2 requests took **1.0 s** each (one connect timeout, then another server); 0 failures |
| **RST** (process gone) | 1 request waited **3.0 s**, then **503** | no slow requests, 0 failures |

- **Why RST still costs 3 s.** HAProxy waits `min(timeout connect, 1 s)` before each retry, so that a server that is restarting has time to come back. Three retries cost 3 s even though each refusal was instant.
- **Reading the log line.** HAProxy's log line for the 503 explains it:

  ```
  http_rr app_http/b3 0/0/-1/-1/4003 503 217 - - sC-- 1/1/0/0/3 …
  ```

  - `Tc = -1`: the connection never completed.
  - `sC`: the server side failed while connecting.
  - The final `3`: three retries.
- **Concurrency multiplies the damage.** With one sequential client, only one request was hurt. With many concurrent clients, every request sent to b3 in that window suffers the same way.
- **What is safe to retry.** Retrying on another server is safe for *connection* failures, because the request never reached a backend. Retrying after a request was sent, for example on a timeout or a 5xx, can execute a non-idempotent request twice.

### Keep-alive timeouts (why 502s appear)

An L7 load balancer reuses idle backend connections. If the **backend** closes an idle connection at the moment the LB sends a new request on it, the LB gets a reset or EOF instead of a response and returns 502.

The rule is to make the backend's keep-alive idle timeout **longer** than the load balancer's. AWS documents this for ALB: its default idle timeout is 60 s, and AWS recommends a longer timeout in the application. The race is timing-dependent, so the lab doesn't try to reproduce it. The scenario at the end of this module walks through how it shows up.

### Stickiness

The stickiness check showed three behaviors:

- **On :80 (round robin)**, a client that keeps cookies still went 4/4/4 across the backends. Cookies don't create stickiness by themselves.
- **On :82**, HAProxy inserted `set-cookie: SRV=b2; path=/`, and every later request with that cookie went to the same backend: 12 × b1 in the measured run. With `cookie … insert indirect`, HAProxy **removes** its own cookie before forwarding, so the backend never saw it (`cookie=-`).
- **On :83** (`balance source`), one client address always landed on one backend (12 of 12), with or without cookies.

Costs:

- Load becomes uneven.
- All of a user's state is lost if their backend dies.
- Source hashing groups everyone behind one NAT onto one backend.

Prefer stateless backends with shared session storage, and keep stickiness for cases that need it, such as WebSocket or upload affinity.

---

## 23.3 Time

### Why it matters

- **TLS.** Certificates are valid only between `notBefore` and `notAfter`. The time lab's server certificate was valid from one hour ago for 30 days. A client clock shifted by **−2 h** or **+31 days** rejected it with Go's `x509: certificate has expired or is not yet valid: current time … is before …` (or `… is after …`). Shifts of −30 min and +29 days still worked. Browsers show the same failure as "Your connection is not private" (NET::ERR_CERT_DATE_INVALID). One device with a wrong clock produces a ticket that looks like a certificate problem.
- **Kerberos / Active Directory.** By default, authentication fails with "clock skew too great" when the two clocks differ by more than 5 minutes.
- **Distributed systems.** Leases, token expiry (JWT `exp`/`nbf`), cache TTLs, "last write wins" databases and consensus timeouts all assume roughly agreed clocks.
- **Logs and traces.** Ordering events across hosts (Module 22) is only as good as their clocks agree.

### How NTP measures a clock

A client sends a request and gets back four timestamps:

```
t1  client sends        (client clock)
t2  server receives     (server clock)
t3  server sends        (server clock)
t4  client receives     (client clock)

offset = ((t2 − t1) + (t3 − t4)) / 2      how far the server's clock is ahead of the client's
delay  = (t4 − t1) − (t3 − t2)            round trip, minus time spent in the server
```

The lab adds 10 ms each way and measures with `edgelab ntp query`, five samples per server; the results were the same in three runs.

| Server | Truth | Measured offset | Measured delay |
| --- | --- | --- | --- |
| ntp1: same clock, 10 ms in / 10 ms out | 0 | **+0.0 ms** | 20.9–21.1 ms |
| bad: clock +5 min, symmetric | +300 s | **+300000.0 ms** | 20.9 ms |
| asym: same clock, 10 ms in / **50 ms out** | 0 | **−20.0 ms** | 61.0 ms |

The last row is the key limitation. The formula assumes both directions take equally long. When they don't, the error is half the difference between the two directions: (10 − 50) / 2 = −20 ms. The error can never exceed delay / 2. NTP can't detect this from timestamps alone, and authentication (NTS) doesn't remove it either.

Real causes of asymmetry are:

- different routes in each direction;
- a congested uplink;
- queueing in one direction only, for example on a link that is busy uploading.

### Choosing between sources

chrony doesn't trust a single answer. It collects samples from every configured source, computes an error interval for each, and keeps the largest group whose intervals overlap. A source outside that group is a **falseticker**. Measured with `chronyd -x`, which runs the full algorithm but never touches the clock:

```
^* 198.18.234.11     2   0   377     0    +40us[  +43us] +/-   10ms
^+ 198.18.234.12     2   0   377     0    -75us[  -73us] +/-   11ms
^x 198.18.234.66     2   0   377     0  -300.0s[-300.0s] +/-   10ms
Detected falseticker 198.18.234.66
```

The selected source is marked `*`, an acceptable one `+`, and the falseticker `x`.

**The sign is flipped.** `chronyc sources` reports the *local* clock relative to the source. The server that is 5 minutes ahead shows as `−300.0s`, while the RFC formula above gives +300 s.

With **two** sources that disagree, there is no majority: both were marked `x`, the log said `Can't synchronise: no majority`, and the tracking showed `Reference ID : 00000000`. So use **at least three** independent sources, and preferably four, so that one can fail and still be out-voted.

With a **single** source, whatever it says is believed. `chronyd -Q` against the bad server alone reported `System clock wrong by 300.000192 seconds`.

The asymmetric server, shown alongside as a `noselect` source, read `+20ms ± 30ms`. Its error interval still included the truth. That interval is what chrony uses to judge whether sources agree.

**Stratum** counts the steps from a reference clock: 1 means the server is directly attached to a reference clock (GPS or atomic), 2 means it syncs to a stratum-1 server, and so on; 16 means unsynchronized. Stratum is a distance, **not an accuracy guarantee**. A nearby stratum-3 server can be more accurate than a distant stratum-1.

### Operating it

```bash
timedatectl                      # "System clock synchronized: yes", "NTP service: active"
chronyc tracking                 # selected source, current offset ("System time"), frequency error
chronyc sources -v               # every source with its state (*, +, -, x, ?) and last sample
chronyc sourcestats              # per-source statistics
chronyd -Q 'server time.example iburst'   # measure against a server, change nothing
chronyc makestep                 # step the clock now: use with care
```

**Step or slew.** chrony normally *slews* the clock: it makes it run slightly faster or slower until the error is gone. A *step* jumps the clock instantly, and some software doesn't expect time to jump, especially backwards. `makestep 1 3` in chrony.conf allows steps only during the first three updates after start. Don't step a production host's clock to test something. In the lab, every namespace shares one kernel clock, so the lab never set it. It measured instead, with `-Q` and `-x`.

**Cloud time sources.** Each cloud provides a local source:

- **AWS:** the Amazon Time Sync Service at `169.254.169.123`, or `fd00:ec2::123` on Nitro instances. On supported instances there is also a PTP hardware clock, `refclock PHC /dev/ptp_ena`.
- **Azure:** the host clock is exposed as a PTP device. Current images use `refclock PHC /dev/ptp_hyperv` in chrony.
- **GCP:** the metadata server, `metadata.google.internal`.

These are close, reliable and free. Use them, or several independent sources, rather than one public server. Don't mix sources that *smear* leap seconds with ones that don't: they disagree by up to half a second around a leap second.

**Security.** Plain NTP is unauthenticated UDP, so an on-path attacker can shift your clock, and with it certificate validity. Keep these in mind:

- **NTS** (RFC 8915) adds a TLS-based key exchange and authenticated packets. In chrony, use `server … nts` and check with `chronyc -N authdata`.
- Restrict who can query your servers and reach their management (UDP 123, chronyc).
- Don't run an open public NTP server.

---

## 23.4 Proxies

### Forward, reverse and transparent

| | Configured by | The other side sees | Examples |
| --- | --- | --- | --- |
| **Forward proxy** | the client (settings, environment variables, PAC file) | the proxy's address | Squid, corporate egress proxies |
| **Reverse proxy** | the server operator | the client sees one endpoint; backends see the proxy | nginx, HAProxy, Envoy, CDNs, cloud LBs |
| **Transparent / intercepting** | the network: traffic is redirected without client configuration | depends; TLS can only be inspected with a CA the client trusts | ISP caches, firewalls |

### Forward proxies and environment variables

A client sends plain HTTP to a forward proxy with an absolute URI: `GET http://host/path HTTP/1.1`. The proxy sees and can log the whole request.

For HTTPS, the client sends `CONNECT host:443`. The proxy answers 200 and from then on relays bytes it can't read. TLS, including certificate checks, runs end to end inside that tunnel. The lab's proxy log shows both:

```
GET-style GET http://198.18.233.11:8080/ from 198.18.232.10:… (plain HTTP: the proxy sees the whole request)
CONNECT 198.18.233.12:8080 from 198.18.232.10:… (the proxy sees only the destination; TLS runs inside the tunnel)
```

Most tools pick up a proxy from environment variables. Implementations disagree, and the forward-proxy check measured how:

| Environment (proxy = 198.18.232.1:3128) | Backend's peer | Result |
| --- | --- | --- |
| curl, `http_proxy` | 198.18.233.1 | via the proxy |
| curl, **`HTTP_PROXY`** | 198.18.232.10 | **direct**: curl ignores upper-case `HTTP_PROXY` (CGI "httpoxy" protection) but accepts `HTTPS_PROXY` |
| Go (`http.ProxyFromEnvironment`), `HTTP_PROXY` | 198.18.233.1 | via the proxy: Go accepts both cases (except under CGI) |
| curl, `http_proxy` + `NO_PROXY=198.18.233.11` | 198.18.232.10 | direct |
| curl, `no_proxy=other.example` **and** `NO_PROXY=198.18.233.11` | 198.18.233.1 | **via the proxy**: curl read the lower-case `no_proxy` and never looked at `NO_PROXY` |

The last row first happened by accident. The lab shell had inherited a lower-case `no_proxy`, and it silently overrode the `NO_PROXY` the check set. The checks now run with `env -i`.

When "the proxy settings are ignored", follow these steps:

1. Print the environment, in both cases: `env | grep -i proxy`.
2. Know which variable *this* client reads.
3. Remember that `NO_PROXY` syntax varies: suffix match, leading dots, and CIDR support differ by tool and version.
4. Check the backend's view of the peer, or the proxy's log, to see which way the request actually went.

Other gotchas:

- Go never proxies requests to `localhost`.
- Java ignores these variables entirely and uses `-Dhttps.proxyHost` and related system properties instead.

### Preserving the client's address

Behind any proxy, the backend's TCP peer is the proxy. There are two ways to pass on the original client's address.

**X-Forwarded-For (L7).** Each proxy can add the address it received the connection from. The lab's client sent a forged `X-Forwarded-For: 6.6.6.6`.

| Proxy and setting | What the backend received |
| --- | --- |
| HAProxy `option forwardfor` | two header lines: `6.6.6.6` and `198.18.232.10` (`xff=6.6.6.6 \| 198.18.232.10`) |
| nginx `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for` | one line: `6.6.6.6, 198.18.232.10` |
| nginx `proxy_set_header X-Forwarded-For $remote_addr` | `198.18.232.10`: the forged value is gone |

The two forms in the first two rows are equivalent: repeated header lines mean the same as one comma-joined line. A by-hand test with `X-Forwarded-For: 1.1.1.1, 2.2.2.2` arrived as `1.1.1.1, 2.2.2.2, 198.18.232.10`.

**The leftmost entry is whatever the client chose to send.** The only entries you can trust are the ones added by proxies you run, counted from the right. So:

- At the edge that faces the internet, either overwrite the header (as nginx's `$remote_addr` does), or append and tell the application how many trusted hops to strip from the right. Examples are nginx `real_ip_header` with `set_real_ip_from`, and the trusted-proxies setting in application frameworks.
- Rate limiting, allow-lists or audit logs based on the leftmost value can be bypassed by anyone.
- The standard header is `Forwarded: for=…;proto=…` (RFC 7239). The same trust rules apply.

**PROXY protocol (L4).** A TCP proxy can't add HTTP headers. Instead, the load balancer writes one line before the client's first byte:

```
PROXY TCP4 <client-ip> <dest-ip> <client-port> <dest-port>\r\n
```

That is version 1. Version 2 is a binary header, and it is what AWS NLB sends. In the lab, through HAProxy `mode tcp` with `send-proxy`, the backend reported `peer=198.18.233.1 xff=- proxy-protocol-client=198.18.232.10:…`. There was no HTTP header, but the client's address and port arrived.

Both sides must agree, and both ways of disagreeing were measured:

- **The LB sends PROXY to a backend that doesn't expect it.** The backend parses `PROXY TCP4 …` as an HTTP request line and answers `HTTP/1.1 400 Bad Request`.
- **A client connects to a PROXY-expecting backend without the header.** The backend logs `sent "GET / HTTP/1.1" instead of a PROXY header — closing`, and the client sees the connection closed with no reply.

  The same happens to a load balancer's HTTP health check if it doesn't send the header. A health check that never succeeds can be the first sign of the mismatch.

**Restrict who can reach the backend.** A backend that accepts PROXY headers believes them. In a by-hand test, `nc` sent `PROXY TCP4 9.9.9.9 …` directly to the backend, and it reported `proxy-protocol-client=9.9.9.9:1234`. Only the load balancer should be able to reach that port: use security groups, firewalls, or the backend's own trusted-source list.

---

## Hands-On Lab: DHCP, Load Balancing, Time and Proxies

### Lab Goals

- Capture DORA, a renewal, a release, an exhausted pool, a silent network, a rogue server and a relay
- Compare HTTP and TCP health checks and measure the failover window with and without redispatch
- See X-Forwarded-For appended, overwritten and forged, and the PROXY protocol working and mismatched
- Work out NTP offset and delay by hand, and watch chrony reject a falseticker
- Break a TLS connection with nothing but a wrong clock

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), and the Go toolbox: `cd labs/go && go build -o bin/ ./cmd/...`
- `apt-get install dnsmasq-base busybox tcpdump tshark nftables ethtool haproxy nginx-light curl chrony`

### Lab Part 1: Run the Labs

```
dhcp:   c1 c2 c3 ── switch (br0) ── server dnsmasq 198.18.230.1 (pool .100–.101)
                        │  └─ rogue dnsmasq 198.18.230.66
                        └─ relay 198.18.230.2 / 198.18.231.1 ── remote

edge:   client 198.18.232.10 ── lb 198.18.232.1 (HAProxy :80–:86, nginx :8000/:8001, proxy :3128)
                                  └─ 198.18.233.1 ── b1 b2 b3 (:8080 HTTP, :9090 PROXY protocol)

time:   client ── switch ── ntp1 ntp2 ntp3 (correct) · bad (+5 min) · asym (10 ms in / 50 ms out)
                         └─ web (TLS, certificate valid from now−1 h to now+30 days)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/dhcp.json    # 9 checks, about 50 s
sudo ./bin/netlab run ../netlab/edge.json    # 9 checks, about 35 s
sudo ./bin/netlab run ../netlab/time.json    # 7 checks, about 25 s
```

All 25 checks passed in three runs of each lab. The captures, logs and HAProxy statistics are kept in each run's evidence directory.

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/dhcp.json
alias lab='sudo ./bin/netlab exec ../netlab/dhcp.json'
E=$(ls -dt /tmp/netlab-dhcp-* | head -1)
```

1. **Watch T1 happen on its own.** Run these two commands in two terminals:

   ```bash
   lab server -- tcpdump -ni eth0 --immediate-mode 'udp port 67 or udp port 68'
   lab c1 -- busybox udhcpc -f -i eth0 -s $E/udhcpc.script
   ```

   Wait just over a minute. Half of the 120 s lease later, you should see one unicast `198.18.230.101.68 > 198.18.230.1.67` request and its reply. Then predict when the next one comes.
2. **Read every option.** Re-read `$E/dora.pcap` with `tshark -r $E/dora.pcap -V -Y dhcp | grep -E 'Option|Your'`. Find the lease time, T1 and T2 if present, the router, the DNS server and the server identifier.

Clean up with `sudo ./bin/netlab down ../netlab/dhcp.json`. Then, with `sudo ./bin/netlab up ../netlab/edge.json` and `lab` pointing at `edge.json`:

3. **Watch a backend leave and return.** Run `lab b2 -- curl -s 'localhost:8080/admin/health?code=503'`, then repeat `lab client -- curl -s 'http://198.18.232.1:8404/stats;csv' | grep '^app_http,b2' | cut -d, -f18` every 0.3 s. Expect `UP`, then `UP 1/2`, then `DOWN` within about a second. Set the code back to 200 and watch `DOWN 1/2` and then `UP`.
4. **Who can you trust?** Run `lab client -- curl -s -H 'X-Forwarded-For: 1.1.1.1, 2.2.2.2' http://198.18.232.1:8001/` and the same against `:8000` and `:80`. Then answer: if your application took the leftmost value, what address would an attacker get to choose?
5. **Forge a PROXY header.** Run:

   ```bash
   lab client -- sh -c "printf 'PROXY TCP4 9.9.9.9 198.18.233.11 1234 9090\r\nGET / HTTP/1.0\r\n\r\n' | nc -q1 198.18.233.11 9090"
   ```

   Then write the firewall rule on b1 that would have stopped it.

Clean up with `sudo ./bin/netlab down ../netlab/edge.json`. Then, with `time.json`:

6. **Do the arithmetic.** Run `lab client -- ./bin/edgelab ntp query -server 198.18.234.40:123 -n 1`. Given 10 ms in and 50 ms out, work out t2 − t1 and t3 − t4 by hand. Check that your offset and delay match the tool's.

### Lab Part 3: Your Environment

- **DHCP.** On a laptop, find your lease and its servers: `nmcli -f DHCP4 device show <if>`, `ipconfig /all` or `ipconfig getpacket en0`. Work out when it renews. In your cloud account, find the DHCP options set or its equivalent and the DNS and NTP servers it hands out.
- **Load balancer.** For one load balancer you support, write down its health-check path, interval and thresholds; its retry policy; its idle timeout; and the backends' keep-alive timeout. Calculate the worst-case time before a dead backend stops receiving traffic.
- **Time.** On a server, run `chronyc sources -v` and `chronyc tracking`. How many sources are there, and how many are independent?

---

## Real-World Troubleshooting Scenario

### Scenario: "Health Checks Pass But Users Get 502 Errors"

This scenario is constructed. It follows AWS's documented guidance, but the numbers below are invented to illustrate it.

**The situation:** an application behind an AWS ALB returns 502 to a few percent of requests. All targets show healthy, and the application's logs show no errors.

1. **What a 502 from the load balancer means.** The ALB generated the 502 because it got no valid response from the target. The application never produced it, so the application's logs being clean is expected. The ALB's access log shows the ALB answered 502 (`elb_status_code`) without a target status code.
2. **Look at the timing.** The failures are spread across all targets, and they happen on connections that had been idle for a while. They don't happen under a steady load test.
3. **Compare the timeouts.** The ALB idle timeout is 60 s, but the application server's keep-alive timeout is 30 s. After 30 s of idleness the backend closes the connection. If the ALB sends the next request at the same moment, that request lands on a closing connection.
4. **Confirm.** Capture on a target. A FIN (or RST) from the target that crosses an incoming request on the same connection is the proof. A count of TIME_WAIT sockets is not.

**The fix:** make the backend's idle timeout longer than the ALB's, for example `keepalive_timeout 75s;` in nginx or the equivalent setting in your application server. Alternatively, lower the ALB's idle timeout below the backend's. Then measure again with an idle-then-burst pattern, which is the traffic that triggered the problem.

**Lesson:** a passing health check proves that a new connection to `/health` works. It says nothing about reused connections, timeouts, or requests that never reached the application.

---

## Common Failure Patterns

| Symptom | Likely cause | How to confirm | Fix |
| --- | --- | --- | --- |
| No IPv4 address (Linux) or 169.254.x.x (Windows/macOS) | DHCP server unreachable, relay missing, or pool exhausted | Capture at the client: DISCOVERs with no OFFER. Then read the **server's** log ("no address available") | Fix reachability or the relay; enlarge the pool, shorten leases, release on shutdown |
| Host gets an address but the wrong gateway or DNS | Rogue or misconfigured DHCP server; per-range option missing behind a relay | Capture shows OFFERs from two servers; check the server identifier in the lease | DHCP snooping or port filtering; set options per range |
| OFFERs arrive but the client ignores them | Bad UDP checksum (offload on veth/VM), wrong subnet | Capture at the client; check the UDP checksum | Disable TX checksum offload on the server side; fix the configuration |
| Load balancer keeps sending to a broken backend | TCP check only, or a health path that doesn't test the application | HTTP 500s from one backend while the stats show it UP | Use an HTTP check that exercises the application |
| A few slow requests or 503s whenever a backend dies | Detection window (`fall × inter`) plus connect retries | LB log: `sC--`, Tc = −1, retry count | Shorter detection; `option redispatch`; proper connect timeouts |
| Intermittent 502s on reused connections | Backend idle timeout shorter than the LB's | Capture: backend FIN crossing a new request | Backend idle timeout > LB idle timeout |
| Application logs show the LB's address as the client | X-Forwarded-For or PROXY protocol not configured, or not read | Backend sees `peer=` the LB and no XFF | Configure the LB to send it and the application to trust it |
| Rate limits or allow-lists bypassed | Application trusts the leftmost XFF entry | Send a forged XFF and see it in the logs | Overwrite at the edge, or count trusted hops from the right |
| 400 Bad Request with "PROXY TCP4 …" in logs, or connections dropped with no reply | PROXY protocol on at one end only | Backend log; `nc` test with and without a header | Enable or disable it at both ends together |
| TLS "not yet valid" / "expired" on one machine only | That machine's clock is wrong | `date -u`; `chronyc tracking` | Fix time sync (three or more sources); step once if necessary |
| Clock off by minutes despite NTP | Two disagreeing sources, or one wrong source | `chronyc sources`: `x` marks, "no majority" | Add independent sources |
| Proxy settings "ignored" by one tool | Variable case (`HTTP_PROXY` vs `http_proxy`), a lower-case `no_proxy` overriding, or the tool ignores the environment (Java) | `env \| grep -i proxy`; the proxy's log | Set the variable the tool reads; use explicit flags |

---

## Summary

- **DHCP:** DISCOVER and REQUEST are broadcast. OFFER and ACK go to the client, usually by unicast unless it sets the broadcast flag. Renewals at T1 are a single unicast REQUEST/ACK pair.
  - Failures look alike from the client: silence. Read the server's logs.
  - The first OFFER wins, which is why rogue servers work and why snooping exists.
  - A relay adds `giaddr`, and the server picks the range by it.
- **Load balancers:** an L7 balancer is always the backend's peer. A TCP health check only proves the port is open.
  - Clients feel the detection window. Measured: 4 s and a 503 for a silent host with default retries; 1 s and no failure with redispatch.
  - Make the backend's keep-alive timeout longer than the LB's idle timeout.
- **Time:** offset = ((t2 − t1) + (t3 − t4)) / 2. Asymmetric paths cause an error of half the difference, and nothing in the protocol detects it.
  - Use three or more independent sources; two disagreeing sources mean no synchronization.
  - Wrong clocks break TLS, Kerberos and anything with expiry times.
- **Proxies:**
  - Trust only the X-Forwarded-For entries your own proxies added, counted from the right.
  - The PROXY protocol must be enabled at both ends, and its port must be reachable only from the load balancer.
  - Know which proxy variables each client reads.

## What's Next

Module 24 goes inside the Linux kernel to find where packets are dropped, Module 25 applies the course to cloud and CI/CD paths, and Module 26 is the Capstone Labs.

---

## Quick Reference Card

```
MODULE 23 — QUICK REFERENCE
═══════════════════════════

DHCP (UDP 67 server, 68 client)
  DISCOVER bcast → OFFER (unicast to yiaddr unless broadcast flag) → REQUEST bcast → ACK
  T1 50 %: unicast REQUEST to the granting server · T2 87.5 %: broadcast · expiry: stop using
  capture:  tcpdump -ni IF --immediate-mode 'udp port 67 or udp port 68'
  decode:   tshark -r F -Y dhcp -T fields -e dhcp.option.dhcp -e ip.src -e dhcp.ip.your -e dhcp.option.router
  no OFFER: dead server, missing relay, OR exhausted pool (check the server log)
  relay:    giaddr = relay's address on the client subnet; server picks the range by it

LOAD BALANCERS
  detection ≈ fall × inter (+ check timeout) · removal ≠ instant
  HAProxy: retries 3, ~1 s between retries; option redispatch to move to another server
  log: Tq/Tw/Tc/Tr/Ta  status  …  termination state (sC = server side, connecting)
  stats: curl 'http://LB:8404/stats;csv' | cut -d, -f1,2,18
  backend keep-alive timeout > LB idle timeout

TIME
  offset = ((t2−t1)+(t3−t4))/2   delay = (t4−t1)−(t3−t2)   error ≤ delay/2
  chronyc tracking | sources -v | sourcestats   ·  chronyd -Q 'server X iburst' (measure only)
  sources: * selected  + combined  - not combined  x falseticker  ? unusable
  ≥ 3 independent sources · NTS: 'server X nts' · Kerberos skew limit 5 min

PROXIES
  forward: absolute-URI GET (proxy sees all) · CONNECT host:443 (proxy sees host:port)
  curl: http_proxy (lower case only), https_proxy/HTTPS_PROXY, no_proxy beats NO_PROXY
  XFF: trust from the right, only your own hops · Forwarded: RFC 7239
  PROXY v1: "PROXY TCP4 src dst sport dport\r\n" · v2 binary (AWS NLB) · both ends or neither
```

---

## References

- RFC 2131 — Dynamic Host Configuration Protocol; RFC 3046 — DHCP Relay Agent Information Option
- dnsmasq manual (`--dhcp-range` tags, `--dhcp-relay`, `--no-ping`, `--dhcp-authoritative`) — https://thekelleys.org.uk/dnsmasq/docs/dnsmasq-man.html
- RFC 5905 — Network Time Protocol Version 4; RFC 8915 — Network Time Security
- chrony documentation — https://chrony-project.org/documentation.html
- Amazon Time Sync Service — https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/configure-ec2-ntp.html
- Azure Linux VM time sync — https://learn.microsoft.com/azure/virtual-machines/linux/time-sync
- HAProxy 2.8 configuration manual (`retries`, `option redispatch`, `option forwardfor`, `send-proxy`, log format) — https://docs.haproxy.org/2.8/configuration.html
- PROXY protocol specification — https://www.haproxy.org/download/2.8/doc/proxy-protocol.txt
- nginx `ngx_http_proxy_module` and `ngx_http_realip_module` — https://nginx.org/en/docs/
- RFC 7239 — Forwarded HTTP Extension
- AWS ALB troubleshooting (HTTP 502) and connection idle timeout — https://docs.aws.amazon.com/elasticloadbalancing/latest/application/load-balancer-troubleshooting.html
- curl environment variables — https://everything.curl.dev/usingcurl/proxies/env.html
- Go `net/http.ProxyFromEnvironment` — https://pkg.go.dev/net/http#ProxyFromEnvironment
