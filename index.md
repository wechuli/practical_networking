---
layout: home

hero:
  name: Practical Networking
  text: How packets really move, and how to find out why they don't
  tagline: A hands-on course for support engineers, developers and cloud engineers, from Ethernet and IP to TCP, DNS, TLS, HTTP/2–3, containers, Kubernetes, firewalls, VPNs, packet capture and incident diagnosis.
  actions:
    - theme: brand
      text: Start here
      link: /modules/module-00-before-we-begin
    - theme: alt
      text: A webpage end to end
      link: /modules/module-02-quickstart-a-webpage-end-to-end
    - theme: alt
      text: All labs
      link: /labs/

features:
  - title: One Linux VM
    details: Every lab runs on one disposable Linux VM, inside network namespaces. Nothing touches your real network.
  - title: Real software
    details: The labs use BIND, unbound, nginx, HAProxy, WireGuard, strongSwan, FRR, chrony and more, plus the course's own Go tools.
  - title: Measured numbers
    details: Timeouts, retransmissions, throughput and failover windows in the notes are what the labs measured, not textbook figures.
  - title: Built for diagnosis
    details: Every module ends with failure patterns and a write-up, and the capstones are incidents you diagnose from evidence.
---

## The course at a glance

| Part | Modules |
| --- | --- |
| **Foundations** | [0 Before we begin](modules/module-00-before-we-begin.md) · [1 History](modules/module-01-history-of-networking.md) · [2 A webpage end to end](modules/module-02-quickstart-a-webpage-end-to-end.md) · [3 Fundamentals](modules/module-03-networking-fundamentals.md) · [4 The OSI model](modules/module-04-the-osi-model.md) |
| **Addressing and paths** | [5 IP](modules/module-05-ip-the-internet-protocol.md) · [6 ARP](modules/module-06-arp-address-resolution-protocol.md) · [7 ICMP, ping and traceroute](modules/module-07-icmp-ping-and-traceroute.md) · [8 Routing](modules/module-08-routing.md) · [9 BGP and anycast](modules/module-09-bgp-anycast-and-internet-routing.md) |
| **Transport and applications** | [10 UDP](modules/module-10-udp-user-datagram-protocol.md) · [11 TCP](modules/module-11-tcp-transmission-control-protocol.md) · [12 DNS](modules/module-12-dns-domain-name-system.md) · [13 TLS](modules/module-13-tls-transport-layer-security.md) · [14 Performance](modules/module-14-network-performance.md) · [15 HTTP/2, HTTP/3, gRPC, WebSockets](modules/module-15-http2-http3-grpc-websockets.md) |
| **Production** | [16 Containers](modules/module-16-networking-in-docker-and-containers.md) · [17 Kubernetes](modules/module-17-kubernetes-networking.md) · [18 Firewalls and zero trust](modules/module-18-firewalls-security-and-zero-trust.md) · [19 VPNs and overlays](modules/module-19-vpns-tunnels-and-overlay-networks.md) · [20 Sockets](modules/module-20-sockets-and-application-level-networking.md) · [21 Packet capture](modules/module-21-packet-capture-and-analysis.md) · [22 Observability](modules/module-22-observability-and-troubleshooting.md) · [23 Additional topics](modules/module-23-additional-critical-topics.md) |
| **Going further** | 24 Linux kernel internals *(planned)* · 25 Cloud and CI/CD paths *(planned)* · [26 Capstone labs](modules/module-26-capstone-labs.md) |

New to the course? Read [Module 0](modules/module-00-before-we-begin.md) first. It sets up the lab VM and the Go toolbox you'll use for everything else. The [introduction](README.md) explains how the course was verified, the [lab index](labs/index.md) maps each module to its labs, and the [appendices](appendices.md) hold the reference material.
