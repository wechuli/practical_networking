# Practical Networking

A hands-on networking course for support engineers, developers and cloud engineers:
from Ethernet and IP to TCP, DNS, TLS, HTTP/2–3, containers, Kubernetes, firewalls,
VPNs, packet capture and incident diagnosis.

Every lab runs on one Linux VM, inside network namespaces, with the course's own Go
tools (`labs/go`) and real software (BIND, unbound, nginx, HAProxy, WireGuard,
strongSwan, FRR, chrony and more). The numbers in the notes — timeouts, retransmissions,
throughput, failover windows — are what those labs measured, not textbook figures.

## Modules

| # | Module |
| --- | --- |
| [0](modules/module-00-before-we-begin.md) | Before We Begin |
| [1](modules/module-01-history-of-networking.md) | The History of Networking |
| [2](modules/module-02-quickstart-a-webpage-end-to-end.md) | Quickstart — A Webpage End to End |
| [3](modules/module-03-networking-fundamentals.md) | Networking Fundamentals |
| [4](modules/module-04-the-osi-model.md) | The OSI Model |
| [5](modules/module-05-ip-the-internet-protocol.md) | IP — The Internet Protocol |
| [6](modules/module-06-arp-address-resolution-protocol.md) | ARP — Address Resolution Protocol |
| [7](modules/module-07-icmp-ping-and-traceroute.md) | ICMP, Ping, and Traceroute |
| [8](modules/module-08-routing.md) | Routing |
| [9](modules/module-09-bgp-anycast-and-internet-routing.md) | BGP, Anycast & Internet Routing |
| [10](modules/module-10-udp-user-datagram-protocol.md) | UDP — User Datagram Protocol |
| [11](modules/module-11-tcp-transmission-control-protocol.md) | TCP — Transmission Control Protocol |
| [12](modules/module-12-dns-domain-name-system.md) | DNS — Domain Name System |
| [13](modules/module-13-tls-transport-layer-security.md) | TLS — Transport Layer Security |
| [14](modules/module-14-network-performance.md) | Network Performance |
| [15](modules/module-15-http2-http3-grpc-websockets.md) | HTTP/2, HTTP/3, gRPC & WebSockets |
| [16](modules/module-16-networking-in-docker-and-containers.md) | Networking in Docker & Containers |
| [17](modules/module-17-kubernetes-networking.md) | Kubernetes Networking |
| [18](modules/module-18-firewalls-security-and-zero-trust.md) | Firewalls, Security & Zero Trust |
| [19](modules/module-19-vpns-tunnels-and-overlay-networks.md) | VPNs, Tunnels & Overlay Networks |
| [20](modules/module-20-sockets-and-application-level-networking.md) | Sockets and Application-Level Networking |
| [21](modules/module-21-packet-capture-and-analysis.md) | Packet Capture and Analysis |
| [22](modules/module-22-observability-and-troubleshooting.md) | Observability & Troubleshooting Methodology |
| [23](modules/module-23-additional-critical-topics.md) | Additional Critical Topics |
| 24 | Linux Kernel Networking Internals *(planned)* |
| 25 | Cloud and CI/CD Path Troubleshooting *(planned)* |
| [26](modules/module-26-capstone-labs.md) | Capstone Labs |

The [course outline](networking_course_outline.md) lists every section, and the
[appendices](appendices.md) hold the reference material.

## Running the labs

On a disposable Ubuntu 24.04 VM, as root (see Module 0 for the packages):

```bash
cd labs/go
go build -o bin/ ./cmd/...
sudo ./bin/netlab run ../netlab/tcp.json      # any lab in labs/netlab
```

`node tools/check-course.mjs` checks every document link and runs the Go tools' tests.
The [lab index](labs/index.md) maps each module to its labs.

## How this course was verified

- [improvements.md](improvements.md) — an independent review of the original course
  (2026-09-10): lab-safety, correctness and coverage defects, module by module.
- [implementation.md](implementation.md) — what was changed in each module in response,
  which labs replaced which, and what was executed, on which versions, with what result.

## License

[MIT](LICENSE) © 2026 Paul Wechuli
