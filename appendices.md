# Course Reference Appendices

These references complement the owning modules. Configuration examples are not
commands to apply indiscriminately to a production host; use the [lab contract](labs/README.md).

## A. Essential Commands

| Question                 | Inspection                                                                                                  | Interpretation limit                                               |
| ------------------------ | ----------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------ |
| Which interface/address? | `ip -br link`; `ip -br address`                                                                             | Administrative/carrier state is not end-to-end reachability        |
| Which OS path?           | `ip route get DEST from SOURCE`; `ip rule`                                                                  | Does not expose a subsequent cloud route decision                  |
| Which neighbor?          | `ip neigh`; `ip -6 neigh`                                                                                   | STALE is usable, not necessarily failed                            |
| Who is listening?        | `ss -lntup`; `ss -tin`                                                                                      | Permissions affect process visibility; VIPs are dataplane-specific |
| What DNS view?           | `dig @RESOLVER NAME TYPE`; `dig +search NAME`                                                               | dig does not use the hosts file or validate DNSSEC by itself       |
| Does TLS authenticate?   | `openssl s_client -connect HOST:443 -servername HOST -verify_hostname HOST -verify_return_error </dev/null` | Use the intended trust store; SNI alone is not validation          |
| What did HTTP do?        | `curl --max-time 10 -v URL`                                                                                 | Record proxy, selected address, protocol and status                |
| What rate?               | `iperf3 -c OWNED_SERVER -t 10 -J`                                                                           | Application-byte rate; match direction and workload                |
| What probe path?         | `mtr --report -c 20 OWNED_SERVER`                                                                           | Reply statistics include return paths and control-plane policy     |

## B. Capture Filters

| BPF capture expression                   | Meaning                                                  |
| ---------------------------------------- | -------------------------------------------------------- |
| `host 192.0.2.10 and tcp port 443`       | Either direction of this IPv4 host/port traffic          |
| `src net 198.18.0.0/24 and dst port 443` | Selected source subnet and destination port              |
| `udp port 53 or tcp port 53`             | Traditional DNS transports, not DoH/DoT/DoQ              |
| `arp or icmp or icmp6`                   | Neighbor/control traffic appropriate to the capture link |
| `tcp[tcpflags] & tcp-syn != 0`           | SYN and SYN-ACK, not the final ACK                       |
| `tcp[tcpflags] & tcp-rst != 0`           | TCP resets                                               |
| `tcp[tcpflags] & tcp-fin != 0`           | TCP FIN in either direction                              |

Use an actual Ethernet interface for Ethernet II headers. Linux `any` normally
uses cooked capture. A capture filter cannot reliably recognize every fragmented
or TCP-reassembled TLS ClientHello; use a dissector after capture. Capture feedback
protocols when testing their absence. Size-based `-C -W` wraps; time-based `-G -W`
exits at the file count. Restrict directories and retain capture-drop counters.

## C. Wireshark Filters

| Display filter                             | Use                                                    |
| ------------------------------------------ | ------------------------------------------------------ | -------------- | -------------------------------------------------- |
| `tcp.flags.syn == 1 && tcp.flags.ack == 0` | Initial SYN/retries                                    |
| `tcp.stream == 0`                          | One observed TCP stream; choose the actual index       |
| `tcp.analysis.retransmission`              | Heuristic retransmission classification                |
| `tcp.analysis.ack_rtt`                     | RTT samples where the analyzer can infer them          |
| `dns.flags.response == 0`                  | DNS questions; correlate replies by tuple/ID/name/type |
| `tls.handshake.type == 1`                  | Decoded ClientHello                                    |
| `http.request                              |                                                        | http.response` | HTTP/1.x, including decrypted TLS where keys match |
| `http2.streamid == 1`                      | One HTTP/2 stream, not QUIC stream numbering           |
| `icmp.type == 3 && icmp.code == 4`         | IPv4 Fragmentation Needed                              |
| `icmpv6.type == 2`                         | IPv6 Packet Too Big                                    |

Analyzer flags are observations/inferences, not independent root-cause proof.
Checksum and segmentation offloads, missing packets, snaplen, clocks, and encryption
must be considered before attributing a fault.

## D. TCP Flags and States

SYN synchronizes sequence state; ACK acknowledges received sequence space; FIN
ends one sending direction; RST aborts; PSH is not an application message delimiter.
See the full state diagram and window examples in
[modules/module-11-tcp-transmission-control-protocol.md](modules/module-11-tcp-transmission-control-protocol.md).

| State       | Interpretation                                                      |
| ----------- | ------------------------------------------------------------------- |
| SYN_SENT    | Connect attempt not yet completed; compare requests and replies     |
| SYN_RECV    | Half-open handshake state; not the accept queue                     |
| ESTABLISHED | TCP state, not proof of application health or active traffic        |
| CLOSE_WAIT  | Remote sent FIN; local side remains open, possibly valid half-close |
| FIN_WAIT_2  | Local FIN acknowledged; peer has not ended its sending side         |
| TIME_WAIT   | Normal active-closer state; capacity depends on tuples and policy   |

Linux `tcp_fin_timeout` concerns orphaned FIN_WAIT_2, not TCP TIME_WAIT. Conntrack
lifetimes are a separate state machine. Count plus context/counter deltas matters;
neither 10,000 sockets nor nonzero listen Recv-Q proves a leak or queue overflow.

## E. Common Ports

| Service                      | Conventional transport/port                      |
| ---------------------------- | ------------------------------------------------ |
| SSH                          | TCP 22                                           |
| DNS                          | UDP/TCP 53                                       |
| DHCPv4                       | UDP 67 server, 68 client                         |
| DHCPv6                       | UDP 546 client, 547 server                       |
| HTTP                         | TCP 80                                           |
| HTTPS                        | TCP 443; HTTP/3 commonly UDP 443                 |
| NTP / NTS-KE                 | UDP 123 / TCP 4460                               |
| IKE / IPsec NAT-T            | UDP 500 / UDP 4500; native ESP is IP protocol 50 |
| DNS over TLS / DNS over QUIC | TCP 853 / UDP 853                                |
| PostgreSQL                   | TCP 5432                                         |
| iperf3                       | TCP 5201 control and TCP/UDP data as configured  |
| VXLAN                        | UDP 4789; Linux Flannel commonly uses 8472       |
| WireGuard                    | Configurable UDP port, commonly 51820            |

Ports are conventions, not proof of the application or encryption. The 16-bit
space has 65,536 values (0-65535); usable configured ephemeral ranges, reservations,
binding rules, and destination tuples determine connection capacity.

## F. Standards

- IPv4: RFC 791; IPv6: RFC 8200; special-purpose addresses: current IANA registries.
- ARP: RFC 826; IPv4 conflict detection: RFC 5227; IPv6 ND/SLAAC: RFC 4861/4862.
- PMTUD: RFC 1191 and RFC 8201; packetization-layer discovery: RFC 4821/8899.
- TCP: RFC 9293; CUBIC: RFC 9438; UDP: RFC 768 and application guidance RFC 8085.
- DNS: RFC 1034/1035; negative caching RFC 2308; HTTPS/SVCB RFC 9460; DoQ RFC 9250.
- TLS 1.3: RFC 8446; HTTP semantics/1.1/2/3: RFC 9110/9112/9113/9114.
- QUIC: RFC 9000/9001; Early Hints: RFC 8297; WebSocket: RFC 6455.
- DHCPv4: RFC 2131; NTPv4: RFC 5905; NTS: RFC 8915; IKEv2: RFC 7296.

Check status, updates, and errata at [RFC Editor](https://www.rfc-editor.org/).
RFC 8895 is ALTO incremental updates using SSE; RFC 7950 is YANG 1.1. Neither
is a reference for HTTP/2 server push or NTPv4. Product defaults need separate
dated implementation documentation.

## G. Environment and Outcomes

Start with [labs/README.md](labs/README.md) and the
[Kubernetes profile](labs/kubernetes/README.md). The
[exercise index](labs/index.md) maps all modules to evidence and assessment.
Portable tests, Linux packet labs, Docker workflows, Kubernetes controllers, and
cloud inspection have different prerequisites; a version number alone is not a
feature check. Record actual executions in [implementation.md](implementation.md).
