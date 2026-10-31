# Objective-to-Exercise Index

Core commands run from the repository directory. Follow [README.md](README.md)
for prerequisites and evidence requirements. Conceptual history does not need an
artificial packet lab. Existing satisfactory diagrams and reference sections remain
the owning explanations; the fixtures supplement them.

| Module | Operational outcome                                    | Exercise or assessment evidence                                                            |
| ------ | ------------------------------------------------------ | ------------------------------------------------------------------------------------------ |
| 00     | Identify phases and prepare tools                      | Environment profile, `curl -V`, `go/cmd/phases` healthy, `-resolve`, and four deliberate failures verified in tcpdump |
| 01     | Distinguish historical decisions and current standards | `go/cmd/rfcstatus` status/obsoleted-by chains/updates, DNS source-port randomisation capture, constraint write-up |
| 02     | Trace one actual request                               | `go/cmd/labserver` + `curl -v`/`-w`, `phases -count`, keyed and unkeyed tshark capture, DROP vs REJECT in a namespace, DevTools timing, Capstone 1 |
| 03     | Interpret interfaces, neighbors, MTU, and rates        | `go/cmd/netlab` with `netlab/bridge.json`, `vlan.json`, `stp.json`; offload capture; same-path measurements |
| 04     | Use layers as hypotheses, not ping gates               | `netlab/osi.json`: per-layer capture and failures (TLS, refused, REJECT, DROP, ARP), iptables counters, Capstone 1 |
| 05     | Subnet and operate dual-stack paths                    | CIDR worked problems, `netlab/ip.json` (TTL, DF, fragments, routes), `netlab/ipv6.json` (RS/RA, SLAAC, ND, DAD, PTB) |
| 06     | Observe next-hop ARP and conflict detection            | `netlab/arp.json`: resolution, NUD states, FAILED, VIP failover with/without gratuitous ARP (measured), conflicts, static entries, proxy ARP |
| 07     | Interpret ICMP/traceroute within observation limits    | `netlab/icmp.json`: traceroute modes, silent hop, ICMP rate limiting, every unreachable code with its errno, redirect, PMTUD black hole and MSS clamp; `go/cmd/gotrace` |
| 08     | Diagnose forwarding and return paths                   | `netlab/routing.json`: LPM outage, route types and errnos, ECMP, policy routing and an asymmetric path through a stateful firewall, DNAT/conntrack, SNAT port exhaustion; `netlab/ospf.json` (FRR OSPF, convergence measured) |
| 09     | Explain routing policy and escalation evidence         | `netlab/bgp.json` (FRR: best path, LOCAL_PREF, prepending, anycast failover, hijack and filter); [routing.md](routing.md) synthetic collector observations |
| 10     | Compare datagram behavior under equal impairment       | `netlab/udp.json`: DNS UDP vs TCP, TC retry, ICMP errors by socket type, MSG_TRUNC, loss vs retries measured, conntrack UDP timeout; `go/cmd/udplab` |
| 11     | Explain TCP sequences, windows, states, and recovery   | `netlab/tcp.json`: lifecycle, TSO and MSS clamp, refused/forged RST/unreachable/timed out, SYN schedule, accept-queue overflow, CLOSE_WAIT, FIN_WAIT_2, RST vs FIN, zero window, 1% loss, CUBIC vs BBR bufferbloat, outage vs RTO backoff; `go/cmd/tcplab` |
| 12     | Distinguish cache, view, search, and transport         | `netlab/dns.json`: a private root/TLD/authoritative hierarchy with two validating Unbound resolvers — referrals and glue, QNAME minimisation, a change and a negative answer timed through caches, NODATA, truncation, lame and dead delegations, DNSSEC secure/insecure/bogus with EDE, REFUSED, split-horizon views, glibc search/ndots/timeouts/hosts, Go's resolver; `go/cmd/dnslab` |
| 13     | Verify chain, hostname, and client identity            | `netlab/tls.json`: cleartext vs TLS captures, TLS 1.2/1.3 handshakes decoded and decrypted with a key log, round trips measured through a delay relay (resumption, HelloRetryRequest), ClientHello sizes, eleven certificate failures in Go and curl, no SNI, protocol version, mTLS, a TLS-inspecting proxy across runtimes; `go/cmd/tlslab` |
| 14     | Measure rather than infer the bottleneck               | `netlab/perf.json` on `go/cmd/pathlab` (a userspace delay/rate/queue/loss path): curl timing breakdown, slow start vs reuse, window ÷ RTT vs BDP, bufferbloat, UDP overload, loss vs the Mathis model (CUBIC vs BBR), goodput vs throughput, PMTUD black hole, PLPMTUD, MSS clamping; Capstone 3 |
| 15     | Observe multiplexing and RPC semantics                 | `netlab/web.json` on `go/cmd/weblab` and `http3/h3lab.py` (aioquic), across `pathlab`: ALPN and h2c, a page over 1 vs 6 HTTP/1.1 connections vs HTTP/2 vs HTTP/3, HTTP/1.1 head-of-line blocking, multiplexed streams decrypted, HPACK, QUIC 1-RTT/resumption/0-RTT, one lost packet in HTTP/2 vs HTTP/3 (and QPACK), UDP blocked, gRPC on the wire (trailers, deadlines, streaming, an HTTP/1.1 proxy), WebSocket handshake/frames/idle timeout, SSE through a buffering proxy |
| 16     | Trace namespace/bridge/NAT/DNS ownership               | `netlab/containers.json` (a container network by hand: veth/bridge, MASQUERADE with and without, DNAT publishing from outside/host/127.0.0.1, hairpin with and without br_netfilter, SNAT port exhaustion with conntrack counters); `netlab/docker.json` (real Docker from the host: veths, SandboxKey, embedded DNS and its redirect, upstream source namespace, NAT/filter/raw rules, INPUT vs DOCKER-USER, docker-proxy, isolation, internal/none/host, MTU, subnet collision; image built by `docker/build-image.sh`, nothing pulled) |
| 17     | Verify Service, policy, Ingress, and mesh              | `netlab/k8s.json` on `go/cmd/kproxy` (two namespace nodes: pods on bridges, flannel-style VXLAN and MTU, egress masquerade, kube-proxy iptables rules, spread, conntrack, unpingable ClusterIP, hairpin off/on, NodePort Cluster vs Local, refused vs timeout, DNS ndots:5 query counts for glibc and Go, DNS failure timings); on a real cluster: `kubernetes/lab.py` policy/ingress/mesh modes (not run in the course build environment), Capstone 5 |
| 18     | Enforce and test access policy safely                  | `netlab/firewall.json`: ACCEPT/DROP/REJECT types as clients see them, first match, counters, iptables-nft, LOG from a namespace (nf_log_all_netns), stateful vs NACL-style stateless rules and the stateless hole, tiers, SYN flood with and without SYN cookies, rate limiting, identity-based authorization with mTLS (`tlslab serve -authz`) |
| 19     | Inspect real encapsulation and forwarding              | `netlab/tunnels.json`: WireGuard site-to-site and a client behind NAT (handshake and data on the wire, overhead, MTU, PMTU black hole with MSS-clamp and MTU fixes, wrong key, AllowedIPs, NAT expiry and PersistentKeepalive), IPsec IKEv2 with strongSwan (IKE/ESP on the wire, SAs, PSK, proposal and traffic-selector failures), VXLAN overhead and outer fragmentation, SSH -L/-D/-R and refused/prohibited forwards, OpenVPN UDP vs TCP under loss |
| 20     | Handle stream framing and backpressure                 | `netlab/sockets.json` with `socklab`: writes vs reads, ephemeral-port exhaustion per destination and TIME_WAIT by closer, tcp_tw_reuse, HTTP client reuse (Go's idle limit, unread bodies), thread-per-connection vs epoll, EMFILE, SO_REUSEADDR/SO_REUSEPORT, relay backpressure, UDP receive-buffer drops |
| 21     | Capture/decrypt with known limits                      | `netlab/capture.json`: --immediate-mode, kernel drops, snaplen, -i any, a drop localized from four capture points, ring buffers, network vs server time and one-way delay (mergecap), loss as sender and receiver captures report it, zero window in Expert Info, unanswered DNS, TLS without and with a key log |
| 22     | Correlate packets, traces, logs, and metrics           | `netlab/observe.json` with `obslab`: baseline, slow dependency, lossy network, error burst with head vs tail sampling, dropped and refused ports — metrics (histogram quantiles over a window), spans (where the time went), logs joined by trace ID, host counters, conntrack flow records; Capstone 6 dataset in `observability/` |
| 23     | Diagnose DHCP, proxies, and time                       | `netlab/dhcp.json` (DORA, renew, release, pool exhaustion, no server, rogue server and a bridge filter, relay/giaddr); `netlab/edge.json` with `edgelab` (HAProxy and nginx: round robin, HTTP vs TCP health checks, failover window with and without redispatch, X-Forwarded-For, PROXY protocol and mismatches, stickiness, forward-proxy variables and CONNECT); `netlab/time.json` (NTP offset/delay, a wrong and an asymmetric server, chrony falseticker and no-majority, TLS with a skewed clock); NTS procedure |
| 26     | Independently diagnose unfamiliar faults               | `netlab/case.json` with `go/cmd/caselab` (Capstones 2 and 4: seven DNS and TLS faults with their repairs, as an instructor self-test); `caselab case6` (Capstone 6 dataset key); [instructor.md](capstones/instructor.md), [submission.md](capstones/submission.md), rubric |

## Optional Tailscale Companion

Take the [Tailscale minicourse](tailscale.md) after Module 19. This guided track
requires a separate test tailnet and two disposable Ubuntu VMs, with an isolated
namespace for the non-Tailscale destination. Allow 3-4 hours plus setup.

| Lesson | Operational outcome | Assessment evidence |
| --- | --- | --- |
| 1 | Distinguish coordination, identity, and encrypted traffic | Two enrolled/tagged peers and an asserted HTTP response |
| 2 | Identify direct, DERP, or peer-relay paths | Active connection status and correlated inner/outer observations |
| 3 | Separate DNS, access policy, and listener health | Passing policy tests plus live allowed/denied/recovered requests |
| 4 | Separate subnet routing from internet egress | Route and policy gates, observed SNAT, exit-node selection and egress checks |
| 5 | Diagnose an unfamiliar private-service failure | Baseline, fault, rejected alternative, minimal repair, negative retest, cleanup |

The course checker includes the guide's static document checks. Live enrollment,
policy validation in the admin console, multi-device traffic, interrupted cleanup,
and a learner pilot must be verified separately; they are not portable regressions.

## Portable Regression Suite

```bash
node tools/check-course.mjs     # documents, syntax, and gofmt/vet/test of every Go tool (loopback only)
(cd labs/go && go build -o bin/ ./cmd/... && ./bin/caselab case6 -flows ../observability/flows.csv -metrics ../observability/metrics.json)
```

Python remains only for Module 15's HTTP/3 client (`http3/h3lab.py`, aioquic) and the
Kubernetes runner (`kubernetes/lab.py`); create the virtual environment for those:
`python3 -m venv .venv && .venv/bin/python -m pip install -r labs/requirements.txt`.

## Target-Environment Gates

On the disposable Ubuntu VM, run [run-linux.sh](run-linux.sh). Do not substitute
the macOS host. Docker requires a running daemon for `netlab/docker.json`; Kubernetes
requires the explicit-context [profile](kubernetes/README.md). Cloud workflows are
optional inspection of an owned sandbox, described in [cloud-private-access.md](cloud-private-access.md).

Test interruption after resource creation, verify owned processes/namespaces are
gone, and compare host routes/firewall/sysctls with the baseline. A static parser
does not establish these behaviors. Record skip reasons, not a false PASS. A new
learner's capstone pilot remains a separate human assessment gate.
