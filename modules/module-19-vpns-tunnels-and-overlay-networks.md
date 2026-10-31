# Module 19: VPNs, Tunnels & Overlay Networks

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 18 — Firewalls, Security & Zero Trust
> **Estimated time:** 4–5 hours

## Overview

A tunnel carries one network's packets inside another network's packets. Support engineers meet tunnels in many places:

- site-to-site VPNs into cloud VPCs;
- the WireGuard or OpenVPN client on a laptop;
- VXLAN under Docker and Kubernetes overlays (Module 17);
- an SSH session someone uses to reach a private database.

Tunnels fail in a small number of recognizable ways, and this module measures each one:

- **Size.** Every tunnel adds bytes, so the inner packet must be smaller. Getting that wrong produces the classic "small things work, big transfers hang" ticket.
- **Keys and identity.** The wrong key, the wrong pre-shared secret, or algorithms the peers don't share.
- **What the tunnel accepts.** WireGuard's AllowedIPs and IPsec's traffic selectors. A tunnel can be up and still refuse a particular subnet.
- **State in the middle.** NAT mappings expire, and a TCP-based tunnel turns packet loss into delay.

Everything here is measured in [`labs/netlab/tunnels.json`](../labs/netlab/tunnels.json). The lab has two sites joined across an emulated internet, a laptop behind a home NAT router, and an SSH bastion.

## What You Will Build Toward

- Identify each tunnel type on the wire and compute its overhead from a capture
- Set tunnel MTUs, recognize a PMTU black hole through a tunnel, and fix it with the MTU or MSS clamping (and know what each fix does not cover)
- Read `wg show`, `swanctl --list-sas` and strongSwan logs; tell a key, proposal, traffic-selector or AllowedIPs problem apart
- Explain NAT keepalives and roaming, and the TCP-over-TCP trade-off
- Use SSH local, dynamic and remote forwarding, and recognize their failure messages

---

## 19.1 What a Tunnel Is

```
inner packet (what the application sent):     [ IP 10.1.0.10 → 10.2.0.10 | TCP | data ]
outer packet (what the internet carries):     [ IP 198.18.190.1 → 198.18.191.1 | UDP 51820 | WireGuard | encrypted inner packet ]
```

Each tunnel endpoint has an address on the **underlay**, the network that actually carries packets. The **inner** packets use the overlay's addresses. Routers in between see only the outer header.

Tunnels differ in three ways:

- **What they carry.** Most carry IP packets (WireGuard, IPsec, GRE). VXLAN and Geneve carry Ethernet frames. SSH forwarding carries byte streams, not packets.
- **Whether they encrypt.** WireGuard, IPsec ESP and OpenVPN encrypt. GRE, VXLAN and Geneve do not.
- **What they ride on.** UDP (WireGuard, VXLAN, OpenVPN-UDP, IPsec with NAT traversal), an IP protocol of their own (ESP is protocol 50, GRE is protocol 47), or TCP (OpenVPN-TCP, SSH).

### Overhead, measured

| Tunnel | Inner → outer (measured) | Overhead | Inner MTU on a 1500-byte underlay |
| --- | --- | --- | --- |
| WireGuard (IPv4 underlay) | 1028 → 1100 bytes | 20 IP + 8 UDP + 16 header + 16 tag = 60, plus padding to a multiple of 16 | 1440 is the IPv4 limit; **1420** is the default (leaves room for an IPv6 underlay: 80) |
| IPsec ESP, AES-GCM-256, tunnel mode | 1028 → 1084 bytes | 20 IP + 8 ESP + 8 IV + 2 trailer + padding to 4 + 16 ICV = 56 here | 1446, which is AWS's documented maximum for AES-GCM without NAT-T |
| IPsec with NAT traversal (UDP 4500) | — | +8 UDP | 1438 (AWS's figure for GCM with NAT-T) |
| VXLAN | 1028 → 1078 bytes | 14 inner Ethernet + 8 VXLAN + 8 UDP + 20 IP = 50 | **1450** |
| GRE (no key) | not measured here | 20 IP + 4 GRE = 24 | 1476 |
| OpenVPN over UDP | tun MTU 1500; TCP MSS clamped to 1388 by default | varies with cipher and options | relies on `mssfix` for TCP and on fragmentation for the rest |
| SSH port forwarding | — | not packet-based | no inner MTU: streams are re-segmented |

Cloud VPN gateways publish their own numbers:

- **AWS Site-to-Site VPN:** MTU 1446 and MSS 1406 for AES-GCM without NAT-T, down to 1406 and 1366 for AES-CBC with SHA2-512 and NAT-T.
- **Google Cloud VPN:** a gateway MTU of 1460, with a smaller payload MTU that depends on the cipher.

Both providers recommend fragmenting *before* encryption, or clamping MSS, and Google states that Cloud VPN does not support fragmentation after encapsulation. Use the provider's table for the configured algorithms rather than a remembered number.

---

## 19.2 WireGuard

WireGuard is a small, fixed-cryptography VPN. It uses:

- Curve25519 for key exchange (the Noise IK handshake);
- ChaCha20-Poly1305 for data;
- BLAKE2s for hashing.

There is no cipher negotiation. It is in the Linux kernel (since 5.6), and `wireguard-go` implements the same protocol in userspace. The lab uses the kernel module when the kernel has it, and wireguard-go otherwise; the measurements here came from wireguard-go.

**Configuration** is an interface key plus, for each peer, the peer's public key, an endpoint and **AllowedIPs**:

```bash
wg set wga private-key gwa.key listen-port 51820 \
   peer <gwb public key> endpoint 198.18.191.1:51820 allowed-ips 10.2.0.0/24,10.99.0.2/32
ip addr add 10.99.0.1/24 dev wga; ip link set wga up; ip route add 10.2.0.0/24 dev wga
```

(`wg-quick` and `/etc/wireguard/wg0.conf` wrap these same steps.) AllowedIPs does two jobs, which is why WireGuard calls it *cryptokey routing*:

- **Sending:** a packet for 10.2.0.10 is encrypted to the peer whose AllowedIPs contain 10.2.0.10.
- **Receiving:** a decrypted packet is accepted only if its *source* address is in that peer's AllowedIPs.

A prefix can belong to only one peer. Adding it to a second peer silently removes it from the first. In the lab, adding 10.1.0.0/24 to the laptop's peer on gwb took it away from gwa's peer, and site A's pings stopped.

### On the wire

Before any traffic, `wg show` has no "latest handshake" line: WireGuard is silent until it has something to send. The first ping triggered this exchange (captured on the internet link, three runs identical in size):

```
198.18.190.1.51820 > 198.18.191.1.51820: UDP, length 148    handshake initiation
198.18.191.1.51820 > 198.18.190.1.51820: UDP, length 92     handshake response
198.18.190.1.51820 > 198.18.191.1.51820: UDP, length 1072   data (a 1028-byte ping inside)
```

Nothing of the inner ICMP is visible. `tshark` decodes the message types: Handshake Initiation (1), Handshake Response (2) and Transport Data (4), each with sender and receiver indexes. Sessions rekey every two minutes while in use, and `latest handshake` should never be older than that on an active tunnel.

### Failure signatures

| What is wrong | What the lab showed |
| --- | --- |
| **Wrong public key** for the peer | Handshake initiations (148 bytes) about every 5 s (12.9 s then 18.0 s); **no response at all**. WireGuard does not answer packets it cannot authenticate, so the far end looks like a black hole. `wg show`: no `latest handshake`, `transfer: 0 B received, 296 B sent`. |
| **AllowedIPs** on the receiver lack the sender's source | The handshake works and packets arrive (gwb's received bytes from gwa rose by 384, the three pings), but **gwb drops them**: pings 0 of 3. Nothing is logged by default. |
| **Endpoint unreachable** or UDP 51820 blocked | The same as a wrong key from the sender's side (initiations, no response). Capture at the far end to tell them apart. |
| **Tunnel MTU too large** for the path | See 19.4. |

The quick triage:

- Check `wg show <if> latest-handshakes`. A recent handshake rules out keys and reachability.
- Then compare `wg show <if> transfer` on both ends while pinging. If bytes arrive but nothing is delivered, check AllowedIPs. If bytes don't arrive, check the path or the firewall.

### NAT, roaming and keepalives

The laptop sits behind a home router that masquerades. When it connected:

- gwb recorded its endpoint as **`198.18.192.2:45019`**: the NAT's public address and a NAT-chosen port, never 192.168.50.10.
- WireGuard learns endpoints from authenticated packets, so a peer that moves (new Wi-Fi, a new NAT port) is followed automatically. That is "roaming".

NAT mappings for UDP expire. On Linux the defaults are 30 s, or 120 s once replies have been seen (`nf_conntrack_udp_timeout` and `_stream`); home routers vary. The lab set both to 5 s:

| | LAN B pings the laptop after 8 s of silence |
| --- | --- |
| No keepalive | mapping gone (`0 flow entries`); **0 of 2 received**. gwb kept sending to the stale port. |
| `persistent-keepalive 2` on the laptop | **2 of 2 received**. The mapping was renewed, and gwb's endpoint for the laptop changed to a new port (45019 → 34635), which WireGuard followed. |

Any peer that must be reachable *from* the other side while behind NAT needs `PersistentKeepalive`, usually 25 s. A client that only ever initiates does not.

---

## 19.3 IPsec (IKEv2)

IPsec is the standard for site-to-site VPNs and for every cloud provider's VPN gateway. It has two layers:

- **IKE** (IKEv2, RFC 7296; UDP 500, and 4500 with NAT traversal) authenticates the peers and negotiates keys.
- **ESP** (IP protocol 50, or ESP-in-UDP 4500 when NAT is detected) carries the encrypted packets.

What were "phase 1" and "phase 2" in IKEv1 are, in IKEv2:

| Exchange | Carries | Result |
| --- | --- | --- |
| **IKE_SA_INIT** | algorithm proposals, Diffie-Hellman, nonces, NAT detection | an encrypted IKE channel (not yet authenticated) |
| **IKE_AUTH** | identities, authentication (PSK, certificates, EAP), the first CHILD_SA's proposals and **traffic selectors** | the IKE SA is up, plus the first **CHILD_SA**: two ESP SAs, one per direction, each with its own SPI |
| CREATE_CHILD_SA | more CHILD_SAs, rekeys | |
| INFORMATIONAL | deletes, liveness (DPD) | |

**Measured** with strongSwan between the gateways (PSK; IKE `aes256-sha256-x25519`; ESP `aes256gcm16`; 10.1.1.0/24 ⇄ 10.2.1.0/24):

```
198.18.190.1.500  > 198.18.191.1.500:  isakmp: parent_sa ikev2_init[I]
198.18.191.1.500  > 198.18.190.1.500:  isakmp: parent_sa ikev2_init[R]
198.18.190.1.4500 > 198.18.191.1.4500: NONESP-encap: isakmp: child_sa ikev2_auth[I]
198.18.191.1.4500 > 198.18.190.1.4500: NONESP-encap: isakmp: child_sa ikev2_auth[R]
198.18.190.1 > 198.18.191.1: ESP(spi=0x4541b11a,seq=0x1), length 1064     (proto ESP (50), length 1084)
```

```
[IKE] IKE_SA site-b[1] established between 198.18.190.1[gw-a]...198.18.191.1[gw-b]
[IKE] CHILD_SA lan-b{1} established with SPIs cc6fbf19_i 4541b11a_o and TS 10.1.1.0/24 === 10.2.1.0/24
```

Two details from the capture:

- **IKE moved to port 4500 for IKE_AUTH** even with no NAT. strongSwan switches ports when both peers support MOBIKE. Firewalls must allow UDP 500 *and* 4500, plus protocol 50, or UDP 4500 alone when NAT-T is forced.
- **ESP carried the data as IP protocol 50.** With a NAT in between, it would be ESP inside UDP 4500.

`swanctl --list-sas` shows:

- the IKE SA (`ESTABLISHED, IKEv2`, its algorithms, rekey timer);
- each CHILD_SA (`INSTALLED, TUNNEL, ESP:AES_GCM_16-256`, inbound and outbound SPIs with byte and packet counters, and the negotiated local and remote subnets).

`swanctl --rekey --child` replaced the CHILD_SA with new SPIs; with PFS a fresh key exchange appears (`/CURVE_25519`).

> The lab runs charon with strongSwan's userspace IPsec (`kernel-libipsec`), so it works on kernels without ESP. On a normal Linux gateway the SAs and policies live in the kernel: `ip xfrm state` shows SAs (do not share its output, it can include keys), `ip xfrm policy` shows the selectors, and `ip -s xfrm state` shows counters.

### Failure signatures (initiator's view, then responder's log)

| Misconfiguration | Initiator | Responder's log |
| --- | --- | --- |
| Pre-shared key differs | `received AUTHENTICATION_FAILED notify error` | `tried 1 shared key for 'gw-b' - 'gw-a', but MAC mismatched` |
| No common IKE proposal (responder: `aes128-sha256-modp2048`) | `received NO_PROPOSAL_CHOSEN notify error` | `received proposals: IKE:AES_CBC_256/…/CURVE_25519` · `configured proposals: IKE:AES_CBC_128/…/MODP_2048` · `received proposals unacceptable` |
| Traffic selectors differ (responder expects 10.1.9.0/24) | `IKE_SA site-b[4] established` then `received TS_UNACCEPTABLE notify, no CHILD_SA built` · `failed to establish CHILD_SA, keeping IKE_SA` | `traffic selectors 10.2.1.0/24 === 10.1.1.0/24 unacceptable` |

The third row is the classic "phase 1 up, phase 2 down": IKE is fine, but the two sides describe different subnets.

- **Cloud gateways:** many are policy-based with one CHILD_SA per subnet pair, others route-based with 0.0.0.0/0 selectors. The two ends must use the same model.
- **The responder's log is where the answer is.** The initiator usually only learns the notify type.

Cloud gateways add their own habits:

- **Two tunnels per connection, for redundancy.** AWS and Azure use two; Google HA VPN uses two interfaces.
- **BGP over the tunnels.** Dynamic routing is recommended, and the BGP session itself fails if the tunnel's inside addresses or ASNs disagree (Module 9).
- **DPD and idle timeouts.** Some gateways only bring a tunnel up when the customer side sends traffic.

---

## 19.4 MTU Through Tunnels: the Black Hole and Its Fixes

WireGuard's default inner MTU is 1420. Inside the tunnel, ordinary PMTUD works, because gwa owns the 1420-byte interface:

```
ping -M do -s 1392 10.2.0.10   → 1400 bytes from 10.2.0.10
ping -M do -s 1393 10.2.0.10   → From 10.1.0.1 icmp_seq=1 Frag needed and DF set (mtu = 1420)
```

A 2 MB TCP transfer used MSS 1368 (1420 − 40 − 12 for timestamps) and completed.

**The black hole.** The lab then made one internet hop silently drop packets over 1400 bytes: no ICMP, the way a mis-sized link with filtered errors behaves. The outer packets carrying 1420-byte inner packets were now 1480 bytes.

| Test | Result |
| --- | --- |
| ping 1300 bytes | received |
| ping 1350 bytes | **lost** |
| TCP 2 MB | **connected in 0.8 ms, then stalled**: `wrote 76608 of 2000000 bytes`, `cwnd 1`, `backoff 4`. The SYN and ACKs are small; full-size data segments are not. |

That is the support ticket: "SSH works, the web page half-loads, `scp` hangs". Two fixes were measured:

| Fix | TCP 2 MB | ping 1350 | Note |
| --- | --- | --- | --- |
| **MSS clamp** on gwa, both directions, `--set-mss 1280` | complete (mss 1268) | still lost | Only TCP SYNs carry an MSS: UDP, ICMP and anything else still black-holes. With WireGuard the clamp must leave room for **padding to 16 bytes**: a first try at 1300 still stalled, because 1340-byte inner packets pad to 1344 and become 1404-byte outer packets. |
| **Tunnel MTU 1340** on both ends | complete (mss 1288) | a 1313-byte DF ping is told `mtu=1340` | Fixes every protocol: the gateway now reports the real limit to senders. |

```bash
# MSS clamping (both directions through the tunnel interface)
iptables -t mangle -A FORWARD -o wg0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1280
iptables -t mangle -A FORWARD -i wg0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1280
# or: --clamp-mss-to-pmtu (uses the outgoing interface's MTU — right when that MTU is right)
```

Where DF ends up varies by implementation:

- **Kernel WireGuard** sends its outer IPv4 packets without DF (`ignore_df`, `df = 0` in `drivers/net/wireguard/socket.c`), so a router may fragment them.
- **wireguard-go** uses an ordinary socket, and its outer packets carried DF in the capture.
- **IPsec gateways** usually copy or set DF and send ICMP to the inner sender.

Either way, fragments are often dropped. Set the tunnel MTU from the real path, and keep ICMP "fragmentation needed" flowing (Module 14).

---

## 19.5 VXLAN, Geneve and GRE

**VXLAN** (RFC 7348) carries *Ethernet frames* in UDP 4789, tagged with a 24-bit VNI: about 16 million segments. It is the overlay under flannel, Docker swarm, many CNIs and data-center fabrics (EVPN). It does not encrypt.

Measured between the gateways (VNI 42):

- ARP and ICMP crossed the internet inside UDP 4789 in clear text (`VXLAN, flags [I] (0x08), vni 42`). The inner frames are fully readable in a capture.
- A 1028-byte inner IP packet became a **1078-byte** outer one: 50 bytes = 14 *inner* Ethernet + 8 VXLAN + 8 UDP + 20 IP. The outer Ethernet header isn't counted, because it is part of the underlay link, not of the IP MTU.
- Created with `dev eth1`, Linux set the VXLAN MTU to **1450** and refused 1500 (`RTNETLINK answers: Invalid argument`).
- **Created without `dev`, it kept MTU 1500.** A full-size 1500-byte inner packet became 1550 bytes, and the sender fragmented the outer packet (VXLAN doesn't set DF): `flags [+] … length 1500` plus `offset 1480 … length 70`. Linux's VXLAN driver then recorded a 1450 path MTU for that destination: DF senders were told `mtu=1450`, and non-DF packets were fragmented *before* encapsulation (1444 + 76 inside two whole outer packets). Don't count on other VTEPs (switches, cloud fabrics) being that forgiving. Set the overlay MTU to underlay − 50, or run jumbo frames on the underlay. Some clouds support jumbo frames inside a VPC: AWS 9001 bytes, Google Cloud up to 8896.

**Geneve** (RFC 8926, UDP 6081) is VXLAN with extensible options. It is used by OVN, by AWS Gateway Load Balancer and by some CNIs, and its overhead is 50 bytes plus options.

**GRE** (RFC 2784, IP protocol 47) carries IP (or Ethernet, as gretap) with a 4-byte header and no encryption. It is common between routers and in "GRE over IPsec". It needs protocol 47 through firewalls, and through NAT only with helpers. This lab's build kernel had no GRE module, so GRE is a Part 3 exercise on your VM.

---

## 19.6 OpenVPN and TCP-over-TCP

OpenVPN uses TLS for its control channel and its own encrypted data channel, over UDP (1194) or TCP. The lab's tunnels negotiated:

- TLS 1.3 for control;
- AES-256-GCM for data (`P2P mode NCP negotiation result … cipher=AES-256-GCM`);
- the default tun MTU of 1500, with OpenVPN's default `mssfix` lowering TCP's MSS (1388 was measured) so that encapsulated packets fit.

OpenVPN's data path was traditionally userspace. Linux 6.16 merged the `ovpn` data-channel-offload driver, used by OpenVPN 2.6+ when available; the lab disables it to measure the same thing everywhere.

**TCP-over-TCP.** A tunnel over TCP gives every packet inside it TCP's reliable, in-order delivery. Measured over a 20 ms path with 2 % random loss in each direction (3 runs):

| Tunnel | Bulk TCP throughput, 0 % loss | Bulk TCP throughput, 2 % loss | 100 pings at 2 % loss |
| --- | --- | --- | --- |
| none (direct) | 957–990 Mbit/s | 3.4–3.6 Mbit/s | — |
| WireGuard (userspace) | 152–246 Mbit/s | 3.0–3.1 Mbit/s | 4–6 % lost, max RTT 22–26 ms |
| OpenVPN over UDP | 148–210 Mbit/s | 3.6–3.7 Mbit/s | 5–6 % lost, max RTT 22–24 ms |
| OpenVPN over **TCP** | 141–147 Mbit/s | **45.6–82.9 Mbit/s**, 0 inner retransmissions | **0 % lost**, avg 38–75 ms, **max 267–459 ms** |

The common claim is that TCP-over-TCP "collapses under any loss". Two things actually happened here:

- **The outer TCP repaired the loss hop by hop**, so the inner TCP saw no loss at all and a single bulk transfer ran *faster* than over UDP. Under random loss, the TCP tunnel behaved like a reliable link layer.
- **That reliability arrives as delay.** Every packet behind a lost segment waits for its retransmission (head-of-line blocking). Pings that would have been lost arrived up to 0.46 s late. At 5 % loss and 100 ms RTT (a Part 2 exercise), the maximum was 686 ms.

The delay is what hurts:

- voice and video, where late equals lost;
- DNS and other UDP request-response with short timeouts;
- interactive sessions;
- many flows sharing one tunnel, all stalled together;
- inner TCP flows whose timers fire during long outer stalls. That is the "meltdown" case (Titz, 2001), and it shows up with burst loss and long outages rather than light random loss.

Prefer UDP transports. Use TCP/443 when it is the only thing a network allows.

**SSH port forwarding is not TCP-over-TCP.** It terminates the TCP connection at each end and carries the byte stream, so there are no inner retransmissions. Many channels still share one TCP connection and its head-of-line blocking.

---

## 19.7 SSH Tunnels

| Option | Listens where | Connects from | Use |
| --- | --- | --- | --- |
| `-L [bind:]lport:host:hport` | client (default 127.0.0.1) | the SSH **server** to host:hport | reach a private service through a bastion |
| `-D [bind:]port` | client; a SOCKS4/5 proxy | the SSH server, to any destination the client asks for | browse or call many internal hosts |
| `-R [bind:]rport:host:hport` | the SSH **server** (loopback unless `GatewayPorts`) | the client to host:hport | expose a local service to the server's side |

Measured from the laptop (behind NAT) through the bastion gwb to the private web service on lanb:

| Test | Result |
| --- | --- |
| `-L 127.0.0.1:18080:10.2.0.10:8080` | `served-by lanb … from 10.2.0.1:54662`: the service sees the **bastion** as its client, so its logs and allow lists show the bastion's address |
| `-L` to a port with no listener | the **local connect succeeds**, then `curl: (56) Recv failure: Connection reset by peer`; ssh logs `channel 2: open failed: connect failed: Connection refused` |
| `-D 127.0.0.1:1080` with `curl --socks5-hostname` | works: the bastion resolved `db.internal` from its own `/etc/hosts` |
| … with `curl --socks5` | `Could not resolve host: db.internal`: the laptop resolved it, and couldn't |
| `-R 0.0.0.0:9000:127.0.0.1:3000` | the bastion listened on **127.0.0.1:9000** anyway (`GatewayPorts no`); `curl` on the bastion reached the laptop's app, while lanb got `Failed to connect to 10.2.0.1 port 9000` |
| a bastion with `AllowTcpForwarding no` | login works; `-L` connects locally then resets; ssh logs `open failed: administratively prohibited: open failed`, and sshd logs `refused local port forward: originator … target 10.2.0.10 port 8080` |

The pattern behind most "the tunnel is up but the app fails" tickets: **the local port always accepts**. The real error is in ssh's own output (`-v`, or `-E logfile`) or the server's log, never in the application's connect.

Some habits that help:

- **`-N`** runs no remote command, only forwards.
- **`-f`** puts ssh in the background once the forwards are up.
- **`-o ExitOnForwardFailure=yes`** fails fast if a port is taken.
- **`ServerAliveInterval 30`** detects dead paths, including NAT mappings that expired as in 19.2.
- **`ProxyJump bastion`** (`-J`) reaches hosts behind a bastion without exposing agent keys on it.
- **Cloud alternatives that avoid open SSH ports:** AWS SSM Session Manager port forwarding, GCP IAP TCP forwarding, Azure Bastion.

---

## 19.8 Site-to-Site, Client VPNs and Overlays as Products

- **Site-to-site.** Gateways connect networks: IPsec to cloud VPN gateways, or WireGuard between Linux routers. The usual checks: routes on both sides for the remote prefixes, overlapping address space (it breaks everything; NAT or renumber), BGP sessions over the tunnels, and both tunnels' health.
- **Client VPN.** OpenVPN- or IPsec-based (AWS Client VPN uses OpenVPN; corporate clients such as GlobalProtect and AnyConnect use IPsec or TLS), or WireGuard. **Full tunnel** sends all traffic through the VPN. **Split tunnel** sends only listed prefixes, and DNS must follow the same split.
- **Mesh VPNs** such as Tailscale and other WireGuard-based products add a coordination server, NAT traversal and relays (DERP) to WireGuard. Their failures are often "relayed instead of direct", which costs latency, or ACLs. See the optional [Tailscale minicourse](../labs/tailscale.md).

---

## Hands-On Lab: Tunnels Measured

### Lab Goals

- See WireGuard, IKEv2/ESP, VXLAN and OpenVPN on the wire and compute their overhead
- Reproduce a tunnel PMTU black hole and fix it two ways
- Produce and read each failure signature: wrong key, AllowedIPs, PSK, proposals, traffic selectors, NAT expiry, SSH forwarding errors
- Measure TCP-over-TCP under loss

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) and the Go toolbox (`cd labs/go && go build -o bin/ ./cmd/...`)
- These packages:
  ```bash
  sudo apt-get install wireguard-tools openvpn strongswan-swanctl strongswan-charon \
      libcharon-extra-plugins libstrongswan-standard-plugins openssh-client openssh-server \
      iperf3 conntrack curl tshark
  ```
  Also install `wireguard-go` if your kernel has no WireGuard.

### Lab Part 1: Run the Lab

```
 lana 10.1.0.10 ─ gwa ─(198.18.190.1)── wan (pathlab: delay, loss) ──(198.18.191.1)─ gwb ─ lanb 10.2.0.10
   10.1.1.10      │  WireGuard 10.99.0.0/24 · IPsec 10.1.1/24⇄10.2.1/24 ·     │      10.2.1.10, web :8080
                  │  VXLAN 10.98.0.0/24 · OpenVPN udp 10.97/24, tcp 10.96/24 │  sshd :22 (:2222 no forwarding)
                                         │
                      home NAT 198.18.192.2 ─ laptop 192.168.50.10 (WireGuard client 10.99.0.3)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/tunnels.json      # about two and a half minutes
```

The 42 checks produce every measurement in this module. Sizes, messages and failure signatures were identical across runs; throughput and latency vary, as shown in the 19.6 table.

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/tunnels.json
alias lab='sudo ./bin/netlab exec ../netlab/tunnels.json'
E=$(ls -dt /tmp/netlab-tunnels-* | head -1)        # the lab's keys and configs
```

1. **Decode a handshake.** Capture on wan while lana pings 10.2.0.10:
   ```bash
   lab wan -- timeout 4 tcpdump -ni eth0 -w /tmp/wg.pcap udp port 51820 &
   lab lana -- ping -c2 10.2.0.10
   tshark -r /tmp/wg.pcap -O wg | grep -E 'Type|Sender|Receiver'
   ```
   Initiation (1) carries the sender index, the response (2) echoes it as the receiver, and data (4) carries only the receiver index.
2. **AllowedIPs belong to one peer.** On gwb, add `10.1.0.0/24` to the laptop's peer (`wg set wgb peer $(cat $E/wg/laptop.pub) allowed-ips 10.99.0.3/32,10.1.0.0/24`). Then `wg show wgb allowed-ips`: gwa's peer lost the prefix, and lana's pings fail. Put it back on gwa's peer.
3. **Rekey IPsec.** Load both configs as check 23 does, bring the tunnel up with `swanctl --initiate --child lan-b`, then run `swanctl --rekey --child lan-b`. In `swanctl --list-sas` the old CHILD_SA is `DELETED` and a new one is `INSTALLED` with new SPIs and `/CURVE_25519` (a PFS key exchange).
4. **Make TCP-over-TCP worse.** Run `lab wan -- ./bin/pathlab set delay=50ms loss=0.05`, then ping 10.97.0.2 and 10.96.0.2 from gwa (`-i 0.1 -c 50 -W 5`). Measured: UDP lost 12 % with a steady 101 ms; TCP lost 0 % with an average of 228 ms and a maximum of 686 ms.

`sudo ./bin/netlab down ../netlab/tunnels.json`.

### Lab Part 3: Your VM, Your VPNs

- **Kernel IPsec.** Repeat the IPsec part with charon's default kernel backend (drop `kernel-libipsec` from the `load` line) and inspect `ip xfrm policy` and `ip -s xfrm state`.
- **GRE.** Run `ip link add gre1 type gre local A remote B ttl 64` between two namespaces, capture protocol 47, and confirm the 24-byte overhead.
- **Your own VPN client.** `ip link` / `ip route` / `resolvectl` before and after connecting: which interface appeared, its MTU, which prefixes now route through it (full or split tunnel), and which DNS servers are used.
- **A cloud VPN.** Read the gateway's tunnel status and logs (AWS: tunnel state and VPN logs in CloudWatch; Azure: VPN diagnostics; GCP: Cloud VPN logs). Check the configured algorithms against the provider's MTU/MSS table.

---

## Real-World Troubleshooting Scenario

### Scenario: "The VPN Is Up, SSH Works, but `scp` and Large Queries Hang"

**The situation.**

- An office connects to an AWS VPC over a Site-to-Site VPN with AES-CBC/SHA2-256, behind the office's NAT.
- SSH to 10.0.1.50 works, small SQL queries work, and the tunnel shows UP.
- Copying a file stalls at 0 %, and large query results never arrive.

**Step 1 — Name the symptom.** Small packets work and large ones don't, on a tunnel. That is the MTU class until proven otherwise. A DF ping finds the boundary:

```bash
ping -M do -s 1400 10.0.1.50   # replies
ping -M do -s 1420 10.0.1.50   # no reply and no "Frag needed" — a silent limit
```

**Step 2 — Find what the limit should be.** AWS's table for AES-CBC with SHA2-256 and NAT-T enabled gives **MTU 1422, MSS 1382**. The office firewall's tunnel interface is set to 1500, and it does not clamp MSS. TCP from the office offers MSS 1460, and the server sends full 1500-byte segments that cannot fit in the tunnel. The gateway that drops them sends no ICMP, or the office firewall blocks it.

**Step 3 — Confirm with a capture** on the office side: the handshake completes, then full-size segments from 10.0.1.50 never arrive, and the sender retransmits them (the lab's `cwnd 1, backoff 4` picture).

**The fix.**

- Set the tunnel interface MTU to 1422 and clamp TCP MSS to 1382 on the office gateway, in both directions.
- Allow ICMP type 3 code 4 to and from the tunnel.
- Then repeat the DF pings and the copy.

Clamping alone would have fixed `scp` but not large UDP (for example DNS over UDP with big responses). The MTU setting fixes both.

**What made it fast:** recognizing "small OK, large hangs" as size, measuring the boundary with DF pings, and taking the number from the provider's table for the actual algorithms instead of guessing "1400".

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| Small requests work, large transfers hang through the tunnel | Tunnel MTU larger than the path allows; ICMP not delivered | DF ping boundary; capture shows full-size segments retransmitted | Correct tunnel MTU; MSS clamp (TCP only); allow ICMP 3/4 |
| WireGuard: no `latest handshake`, initiations every 5 s | Wrong public key, endpoint unreachable, UDP blocked | Capture at both ends; keys in `wg show` | Fix key/endpoint/firewall |
| WireGuard: handshake fine, traffic one-way or none | AllowedIPs lack the source (or a prefix moved to another peer); missing route | `wg show transfer` grows on receiver; `wg show allowed-ips` | Fix AllowedIPs and routes |
| Peer behind NAT unreachable after idle | NAT mapping expired | Conntrack/NAT table; works right after the peer sends | `PersistentKeepalive = 25` |
| IKE: `AUTHENTICATION_FAILED` | PSK or certificate/ID mismatch | Responder log: `MAC mismatched` / ID lines | Match PSK, IDs, certificates |
| IKE: `NO_PROPOSAL_CHOSEN` | No common algorithms (IKE or ESP) | Responder log: received vs configured proposals | Align proposals (and DH groups, PFS) |
| IKE up, `TS_UNACCEPTABLE`, no CHILD_SA | Subnets (traffic selectors) differ; policy- vs route-based mismatch | Responder log: `traffic selectors … unacceptable` | Same subnets on both sides |
| Tunnel up, some subnets fail | Missing selector/route for that prefix; overlapping address space | `swanctl --list-sas` / `ip xfrm policy`; routes both sides | Add selectors/routes; renumber or NAT |
| VXLAN works for small packets only | Overlay MTU 1500 on a 1500 underlay; fragments dropped | `ip link` MTU; capture shows outer fragments | MTU underlay − 50, or jumbo underlay |
| Laggy VPN, no packet loss reported | TCP transport turning loss into delay | Ping RTT spikes; outer retransmissions | Use UDP transport |
| `ssh -L` port accepts, then resets | Target refused, or forwarding not allowed | ssh `-v`/`-E`: `connect failed` / `administratively prohibited` | Fix target; `AllowTcpForwarding` |
| `ssh -R` port not reachable from others | `GatewayPorts no` binds loopback | `ss -ltn` on the server | `GatewayPorts clientspecified` (deliberately) |
| SOCKS proxy: internal names fail | Client resolving locally | `--socks5` vs `--socks5-hostname` | Remote resolution (socks5h) |

---

## Summary

- Tunnels wrap inner packets in outer ones. The overheads measured were:
  - WireGuard: 60 bytes plus padding (MTU 1420 by default);
  - IPsec ESP with AES-GCM: 56 bytes (plus 8 with NAT-T);
  - VXLAN: 50 bytes, including the inner Ethernet header (MTU 1450).
  
  Provider VPN gateways publish MTU/MSS tables per algorithm; use them.
- WireGuard is silent until there is traffic and never answers what it cannot authenticate. A wrong key shows as initiations with no response. AllowedIPs both route and filter, and a prefix belongs to one peer. Endpoints follow roaming peers, and peers behind NAT need keepalives to stay reachable.
- IKEv2 is IKE_SA_INIT, then IKE_AUTH with the first CHILD_SA. strongSwan moved to UDP 4500 even without NAT, and ESP used protocol 50. `AUTHENTICATION_FAILED`, `NO_PROPOSAL_CHOSEN` and `TS_UNACCEPTABLE` name the three common misconfigurations, and the responder's log says exactly what differed.
- A tunnel MTU larger than the path is a black hole: connections open and then stall. Lowering the tunnel MTU fixes every protocol. MSS clamping fixes TCP only, and with WireGuard it must leave room for padding.
- TCP-based tunnels hide loss by turning it into delay. In the lab a bulk transfer ran faster, but pings arrived up to 0.46 s late. Prefer UDP transports.
- SSH `-L`, `-D` and `-R` carry streams through a bastion. The local port always accepts, and the real error is in ssh's log.

## What's Next

Module 20 covers sockets and application-level networking: the sockets API, blocking and non-blocking I/O, epoll, connection pooling and ephemeral-port exhaustion. That is where the connections in this module come from and how applications see their failures.

---

## Quick Reference Card

```
MODULE 19: VPNs, TUNNELS & OVERLAYS — QUICK REFERENCE
═══════════════════════════════════════════════════════

OVERHEAD (IPv4 underlay, 1500)
  WireGuard 60 + pad16 → MTU 1420 default (1440 max IPv4) · ESP AES-GCM tunnel 56 (+8 NAT-T) → 1446/1438
  VXLAN 50 (inner Ethernet included) → 1450 · Geneve 50+opts · GRE 24 → 1476 · OpenVPN: tun 1500 + mssfix

WIREGUARD
  wg show [if] · wg show if latest-handshakes | transfer | endpoints | allowed-ips
  no handshake + initiations every 5 s, no response → key / endpoint / UDP blocked
  handshake OK, rx bytes grow, nothing delivered → AllowedIPs (source) · one prefix = one peer
  behind NAT and must be reachable → PersistentKeepalive = 25 · UDP 51820 · tshark -O wg

IPSEC / IKEv2 (strongSwan)
  swanctl --load-all · --initiate --child X · --list-sas · --rekey --child X · logs on the RESPONDER
  UDP 500 (IKE_SA_INIT) · UDP 4500 (NAT-T / MOBIKE) · ESP = IP proto 50
  AUTHENTICATION_FAILED → PSK/ID · NO_PROPOSAL_CHOSEN → algorithms · TS_UNACCEPTABLE → subnets
  kernel: ip xfrm policy · ip -s xfrm state (don't share: keys)

MTU
  ping -M do -s N (N + 28 = packet) · ip route get DST (cached mtu)
  fix: tunnel MTU (all protocols) · TCPMSS --set-mss / --clamp-mss-to-pmtu (TCP only) · allow ICMP 3/4

VXLAN
  ip link add vx type vxlan id VNI local A remote B dstport 4789 dev ETH  (dev → MTU 1450)
  tcpdump 'udp port 4789' · unencrypted · 24-bit VNI

TCP-over-TCP: loss → delay (head-of-line); prefer UDP transports

SSH
  -L [b:]lp:host:hp (server connects) · -D [b:]port SOCKS (curl --socks5-hostname) · -R [b:]rp:host:hp (loopback unless GatewayPorts)
  -N -f -o ExitOnForwardFailure=yes · -J bastion · ServerAliveInterval 30 · ssh -v / -E log
  "open failed: connect failed" = target · "administratively prohibited" = AllowTcpForwarding
```

---

## References

- WireGuard: whitepaper (https://www.wireguard.com/papers/wireguard.pdf), https://www.wireguard.com/; `man wg`; Linux `drivers/net/wireguard/socket.c`
- RFC 7296 — IKEv2; RFC 4303 — ESP; RFC 3948 — UDP encapsulation of ESP; RFC 4555 — MOBIKE; strongSwan documentation (swanctl.conf, kernel-libipsec)
- RFC 7348 — VXLAN; RFC 8926 — Geneve; RFC 2784 — GRE
- OpenVPN 2.6 manual (`--mssfix`, `--peer-fingerprint`, DCO); Linux 6.16 networking changes (ovpn driver)
- O. Titz, "Why TCP Over TCP Is A Bad Idea" (2001)
- `man ssh`, `man sshd_config` (AllowTcpForwarding, GatewayPorts), `man ssh_config` (ProxyJump, ServerAliveInterval)
- AWS — Best practices for a Site-to-Site VPN customer gateway device (MTU/MSS table)
- Google Cloud — Cloud VPN MTU considerations
