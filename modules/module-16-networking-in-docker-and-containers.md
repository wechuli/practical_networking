# Module 16: Networking in Docker & Containers

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 15 — HTTP/2, HTTP/3, gRPC & WebSockets
> **Estimated time:** 4–5 hours

## Overview

A container's network is not a special kind of network. It is the kernel features from earlier modules, put together by a daemon:

- a **network namespace** (Module 0's isolation);
- a **veth pair** into a **bridge** (Module 3's switch);
- the bridge address as the **default gateway** (Module 8);
- **NAT** in iptables for egress and for published ports (Modules 11 and 18);
- a **DNS server** that knows container names (Module 12).

Each layer has its own failure, and each failure has its own symptom:

- No masquerade: egress dies silently.
- No forwarding rule: a published port times out.
- A host firewall rule: a published port stays open anyway.
- The wrong network: container names don't resolve.
- An overlapping subnet: a corporate host becomes unreachable from containers only.

This module first builds a container network by hand in namespaces, so every rule is one you typed. It then inspects what Docker actually creates (Docker Engine 29.9.2, iptables backend), from the host, with the same tools. Every output below is measured.

## What You Will Build Toward

- Find a container's namespace, interfaces and veth peer from the host, and enter it with `nsenter`
- Explain bridge switching between containers and routing plus MASQUERADE to everything else, and show what fails without each
- Read the NAT, filter and raw rules behind `-p`: DNAT in PREROUTING and OUTPUT, docker-proxy for loopback and hairpin, Docker 28's protection of unpublished ports
- Explain why a host firewall (INPUT, ufw) does not protect a published port, and protect it with DOCKER-USER
- Diagnose container DNS: the embedded server at 127.0.0.11, how it is wired, user-defined vs default bridge, and where upstream queries come from
- Choose and recognise network modes (bridge, user-defined, internal, host, none, macvlan/ipvlan, overlay), and set an MTU
- Recognise SNAT port exhaustion and subnet collisions with corporate networks

---

## 16.1 Network Namespaces

A network namespace is a separate copy of the network stack: its own interfaces, addresses, routes, neighbour table, iptables rules, conntrack table, sockets and `net.*` sysctls. A container gets its own namespace, unless it runs with `--network host`. Processes in it see only its interfaces.

There are two ways to name a namespace:

- **`ip netns`** names namespaces by bind-mounting them under `/var/run/netns/`. That is what `netlab` uses (`nl-<lab>-<node>`).
- **Docker** keeps each container's namespace as a file under `/var/run/docker/netns/`. It is shown as `SandboxKey` in `docker inspect`, and `ip netns list` does not show it. From the lab:

  ```
  SandboxKey: /var/run/docker/netns/8142becbe2f1
  in ip netns list: 0
  nsenter --net=/var/run/docker/netns/8142becbe2f1 ip -4 -o addr show eth0
  2: eth0    inet 10.89.0.3/24 brd 10.89.0.255 scope global eth0
  ```

**Entering a container's network namespace from the host** lets you use the host's tools (`ss`, `tcpdump`, `iptables`, `ip`) on it, even when the image has no shell. Use either:

- `nsenter -t "$(docker inspect -f '{{.State.Pid}}' C)" -n CMD`
- `nsenter --net="$SANDBOXKEY" CMD`

`-n` enters only the network namespace: the filesystem, and so the binaries, are still the host's.

Some kernel state is shared or global across namespaces: the kernel itself, loaded modules, and some sysctls. One per-namespace subtlety matters below: `net.bridge.bridge-nf-call-iptables` (br_netfilter) is set per namespace. When the lab changed it in its own "host" namespace, the real host's value stayed at 1, the value Docker sets.

---

## 16.2 veth Pairs and the Bridge

A **veth pair** is two interfaces joined like a cable: what enters one end leaves the other. Docker puts one end in the container as `eth0` and attaches the other to the network's bridge as `vethXXXX`. The name `eth0@ifN` gives the peer's interface index in the other namespace. From the Docker lab:

```
docker exec nl-dk-cli ip addr show eth0
2: eth0@if119: <BROADCAST,MULTICAST,UP,LOWER_UP,M-DOWN> mtu 1500
    inet 10.89.0.3/24 brd 10.89.0.255 scope global eth0
host side, ifindex 119:
veth50fc562@if2 <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc noqueue master nl-dk-br0
```

To find a container's host-side veth, read `/sys/class/net/eth0/iflink` inside the container and look up that index on the host with `ip -o link | grep "^N:"`.

Be careful with `bridge link show` here. It prints the peers as `veth50fc562@ifb0`: it resolved the peer's index (2, in the *container's* namespace) to the name of the host's interface number 2, which in this VM is `ifb0`. That is a display coincidence, not a connection to `ifb0`.

**The bridge** (`docker0` for the default network, `br-<id>` for user-defined ones, or a name chosen with `-o com.docker.network.bridge.name=`) is a software switch. Its own address, 10.89.0.1/24 in the lab, is every container's default gateway.

- **Between containers on the same bridge**, traffic is switched, not routed. The hand-built lab shows `ttl=64` between containers (no hop) and the peer's MAC in the neighbour table. Same-network traffic does not go through the host's routing or NAT.
- **To anything else**, traffic is routed by the host, so it needs `ip_forward=1` and source NAT.

---

## 16.3 Egress: Routing and MASQUERADE

A container's address is private, and nothing outside has a route back to it. The hand-built lab shows both halves:

```
without source NAT (capture on the internet side):
  IP 10.88.0.2 > 198.18.140.10: ICMP echo request         ← arrives…
  (no reply)                                              ← …and the reply has nowhere to go
internet$ ip route get 10.88.0.2
  RTNETLINK answers: Network is unreachable

iptables -t nat -A POSTROUTING -s 10.88.0.0/24 ! -o cbr0 -j MASQUERADE
  IP 198.18.140.1 > 198.18.140.10: ICMP echo request      ← the host's address
  IP 198.18.140.10 > 198.18.140.1: ICMP echo reply
conntrack: src=10.88.0.2 dst=198.18.140.10 … src=198.18.140.10 dst=198.18.140.1
```

Docker's rule is the same, one per network: `-A POSTROUTING -s 10.89.0.0/24 ! -o nl-dk-br0 -j MASQUERADE`. That is, rewrite traffic from the network's subnet unless it is going back out the same bridge. The Docker lab's capture on the host's uplink showed a container's ping leaving as `198.51.100.1 > 198.51.100.10`, never from 10.89.0.3.

**Turning NAT off is not enough to route containers.** A network created with `com.docker.network.bridge.enable_ip_masquerade=false` sent its packets out with the container's own source (10.93.0.2), and the outside host replied. The ping still got **0 replies**: Docker 28 and later drop, in the raw table, anything addressed to a container's IP that arrives on any interface other than its bridge (`-d 10.93.0.2/32 ! -i br-… -j DROP`, 2 packets counted). Docker's supported way to route without NAT is `-o com.docker.network.bridge.gateway_mode_ipv4=routed`. With it, the same ping worked, and the network has no MASQUERADE rule. The outside network then needs a route to the container subnet.

---

## 16.4 Publishing Ports

`-p 18080:80` creates a destination NAT, plus the rules that let the translated traffic through. The hand-built lab adds Docker's rules one at a time:

| Step | From outside | From the host, to its own address | From the host, to 127.0.0.1 | Container to host:port (hairpin) |
| --- | --- | --- | --- | --- |
| DNAT in PREROUTING (`--dst-type LOCAL`) | works; container sees **198.18.140.10** | fails (exit 7): local packets skip PREROUTING | fails | — |
| + the same DNAT in OUTPUT (`! -d 127.0.0.0/8`) | works | works (source 198.18.140.1) | still fails | — |
| + `route_localnet` and SNAT away from 127/8 | | | works (source 10.88.0.1) | — |
| br_netfilter off (`bridge-nf-call-iptables=0`) | | | | **fails** (timeout) |
| br_netfilter on | | | | works; container sees the caller (10.88.0.3) |

What the table shows:

- **DNAT keeps the client's address.** The container logs the real external source, unlike a proxy.
- **The host's own requests need the OUTPUT rule.**
- **127.0.0.1 is hard to do in the kernel.** Docker does not do it by default: it runs **docker-proxy**, a userland process listening on the published port. The Docker lab showed `docker-proxy` in `ss` on port 18080, and the container saw requests to the host's 127.0.0.1 arrive from the bridge gateway (10.89.0.1), because docker-proxy opened a new connection.
- **Hairpin is the subtle one.** Container B calls the host's published port, DNAT sends the request to container A on the same bridge, and A replies to B directly over the bridge. B never sent anything to A's address, so it drops the reply, unless the bridge's frames pass through conntrack (br_netfilter, which Docker loads and turns on) or the traffic is also source-NATed. Docker's DNAT rules exclude traffic arriving from the container's own bridge (`! -i nl-dk-br0`), so in the Docker lab a container calling its own published port is answered by docker-proxy.

Docker's actual rules, from the Docker lab:

```
nat:   -A PREROUTING -m addrtype --dst-type LOCAL -j DOCKER
       -A OUTPUT ! -d 127.0.0.0/8 -m addrtype --dst-type LOCAL -j DOCKER
       -A POSTROUTING -s 10.89.0.0/24 ! -o nl-dk-br0 -j MASQUERADE
       -A DOCKER ! -i nl-dk-br0 -p tcp -m tcp --dport 18080 -j DNAT --to-destination 10.89.0.2:80
       -A DOCKER -d 127.0.0.1/32 ! -i nl-dk-br0 -p tcp -m tcp --dport 18081 -j DNAT --to-destination 10.89.0.2:80   ← -p 127.0.0.1:18081:80
filter: -P FORWARD DROP
       -A FORWARD -j DOCKER-USER
       -A FORWARD -j DOCKER-FORWARD               (→ DOCKER-CT established, DOCKER-INTERNAL, DOCKER-BRIDGE → DOCKER)
       -A DOCKER -d 10.89.0.2/32 ! -i nl-dk-br0 -o nl-dk-br0 -p tcp -m tcp --dport 80 -j ACCEPT
       -A DOCKER ! -i nl-dk-br0 -o nl-dk-br0 -j DROP
raw:   -A PREROUTING -d 10.89.0.2/32 ! -i nl-dk-br0 -j DROP      (one per container)
```

Chain names change between releases. Docker 28.0.0 began blocking direct routed access to unpublished container ports, and added the raw-table rules behind it (28.0.2 added `DOCKER_INSECURE_NO_IPTABLES_RAW=1` to opt out). 28.0.1 moved most of Docker's rules out of FORWARD itself, into chains of its own such as DOCKER-FORWARD; releases before 28 used DOCKER-ISOLATION-STAGE-1/2 chains instead. Read the rules on the machine in front of you (`iptables-save | grep -i docker`) rather than from a blog post.

**A published port bypasses the host firewall.**

- **INPUT rules don't apply.** After DNAT, the packet's destination is the container, so it is *forwarded* and never reaches INPUT. In the lab, an `iptables -I INPUT … --dport 18080 -j DROP` on the uplink changed nothing: the outside client still got its response. ufw is built on INPUT, which is why "ufw denies 8080 but the port is open" is such a common ticket. Docker's documentation says the same.
- **DOCKER-USER is the place for your filter rules.** By the time a packet reaches it, the destination port is already 80, so match the original port through conntrack. `iptables -I DOCKER-USER -i nl-dk-out -p tcp -m conntrack --ctorigdstport 18080 -j DROP` made the outside client time out (exit 28). Removing it restored access.
- **Bind instead of filtering when you can.** `-p 127.0.0.1:18081:80` answered the host and refused the outside (exit 7).
- **Under the nftables backend there is no DOCKER-USER.** The backend is experimental in Docker 29 (`"firewall-backend": "nftables"`). You add your own table with base chains at the right hooks and priorities.

---

## 16.5 Network Modes and Isolation

| Mode | What the container gets | Measured / notes |
| --- | --- | --- |
| default `bridge` (`docker0`) | Private address, NAT, published ports | No name resolution between containers (16.6) |
| user-defined bridge | The same, plus the embedded DNS server, aliases, and isolation from other networks | The default for Compose projects |
| `--internal` | A bridge with no gateway route and no MASQUERADE | `10.91.0.0/24 dev eth0` only; ping out → `Network is unreachable` |
| `--network none` | Loopback only | `1: lo:` and nothing else |
| `--network host` | The host's namespace itself | Saw the host's `nl-dk-out` and `nl-dk-br0`; `-p` is ignored; ports conflict with the host's |
| macvlan / ipvlan | An address on the physical LAN (macvlan: its own MAC) | No NAT. The parent interface cannot reach its macvlan children by design — add a macvlan sub-interface on the host. Switches and Wi-Fi may refuse extra MACs (ipvlan shares one) |
| overlay | A VXLAN network across hosts (Swarm) | UDP 4789 between hosts, plus 7946 (gossip) and 2377 (manager). VXLAN costs 50 bytes of MTU (Module 19) |

**Isolation between networks.**

- **Blocked:** a container on the default bridge could not ping a container on `nl-dk-app` (100% loss). The DOCKER chain's `! -i nl-dk-br0 -o nl-dk-br0 -j DROP` and the raw rule stop direct traffic between bridges.
- **Allowed:** it could reach `nl-dk-app`'s published port through the host's address, and arrived from 10.89.0.1. Its packet left through `nl-dk-br0` after DNAT, so the default network's MASQUERADE rule (`-s 172.17.0.0/16 ! -o docker0`) rewrote it to the bridge's address.
- **The fix is joining the network:** `docker network connect nl-dk-app nl-dk-def` gave it a second interface (`eth1 10.89.0.5/24`), and then `ping nl-dk-cli` by name worked.

**MTU.** A container's interface gets the network's MTU: `-o com.docker.network.driver.mtu=1400` gave `eth0 … mtu 1400`. The daemon-wide default is `"mtu"` in `daemon.json`. Set it when the host's uplink is smaller than 1500 (a VPN, a VXLAN or WireGuard underlay, a cloud with 1460). Otherwise large transfers from containers can hang where the host's own transfers do not, as in Module 14's black hole, typically in Docker-in-Docker CI builds that hang pushing or pulling layers.

---

## 16.6 Container DNS

### The embedded server

On user-defined networks, Docker writes `nameserver 127.0.0.11` and `ndots:0` into each container's `/etc/resolv.conf`. dockerd answers with container names, `--network-alias` aliases and Compose service names, and forwards everything else. The default bridge gets a copy of the host's resolvers instead, so container names don't resolve there:

```
user-defined network                          default bridge
nameserver 127.0.0.11                         nameserver 8.8.8.8
# ExtServers: [host(8.8.8.8) host(8.8.4.4)]   nameserver 8.8.4.4
nslookup nl-dk-web → Address: 10.89.0.2       nslookup nl-dk-web → ** server can't find nl-dk-web: NXDOMAIN
```

Two containers sharing `--network-alias api` both came back for `nslookup api` (10.89.0.5 and 10.89.0.6). That is DNS round robin, with the client-side caching caveats from Module 12.

**How 127.0.0.11 works.** The address is not an interface. Docker installs NAT rules in the **container's** namespace that redirect port 53 to sockets dockerd holds open inside that namespace (seen through `nsenter`):

```
-A DOCKER_OUTPUT -d 127.0.0.11/32 -p udp -m udp --dport 53 -j DNAT --to-destination 127.0.0.11:54851
-A DOCKER_OUTPUT -d 127.0.0.11/32 -p tcp -m tcp --dport 53 -j DNAT --to-destination 127.0.0.11:44907
UNCONN 0 0 127.0.0.11:54851 0.0.0.0:* users:(("dockerd",pid=201,fd=48))
```

A container that flushes its own nat table (for example, a tool run with `--cap-add NET_ADMIN` that resets iptables) breaks its own DNS.

**Where upstream queries come from** depends on how the server was configured. The lab measured both cases:

- **Servers from the host's resolv.conf** are marked `host(…)` and are queried from the **host's** namespace. The capture showed `192.0.2.2 > 8.8.8.8.53` on the host's uplink, and nothing on the bridge. So host-only resolvers, such as a VPN's or 127.0.0.53, work.
- **Servers given with `--dns`** (on user-defined networks, still behind 127.0.0.11: `ExtServers: [198.51.100.10]`) are queried from the **container's** namespace. The capture on the bridge showed `10.89.0.4 > 198.51.100.10.53: A? app.corp.test`, and the upstream server's log showed the query `from 198.51.100.1`, after MASQUERADE. So container egress rules, network isolation and `--internal` apply to those queries.

**Troubleshooting order.**

1. `cat /etc/resolv.conf` in the container: is it 127.0.0.11, and which ExtServers are listed?
2. `nslookup NAME 127.0.0.11` from inside: does dockerd answer?
3. Is the target on the **same** network? `docker network inspect`.
4. For external names, the upstream path: the host's resolver, or `--dns` reached from the container's namespace.
5. `nsenter … tcpdump -i any port 53` shows the stub query on lo and the upstream query on eth0.

---

## 16.7 The Daemon's Own Networking

- **`docker pull` is made by dockerd (and containerd), not by your shell.** `HTTPS_PROXY` in your session has no effect. Configure the daemon: `"proxies": {"http-proxy": …, "https-proxy": …, "no-proxy": …}` in `daemon.json` (Docker 23+), or `Environment=` in a systemd drop-in, then restart. `docker info` shows the daemon's proxy settings.
- **A pull talks to several endpoints.** It contacts the registry API (`registry-1.docker.io`), an auth/token endpoint, and often a separate blob store or CDN for layers. On ECR, layers come from S3, so a proxy or firewall allowlist must cover all of them. This lab's VM could not pull at all (`Forbidden` from the egress proxy), which is why the lab builds its own image from a static busybox and the course tools with `docker import`.
- **BuildKit `RUN` steps** use their own network setup (bridge by default; `--network=host` must be allowed). They can therefore differ from `docker run` in DNS and proxy settings: build args `HTTP_PROXY`/`HTTPS_PROXY` are passed through, but daemon settings are not inherited by build containers.
- **Address pools.** Docker takes 172.17.0.0/16 for `docker0` and allocates user-defined networks from `default-address-pools` (by default 172.17–172.31 in /16s, then 192.168.0.0/16 in /20s). These ranges often collide with corporate networks and VPNs (16.9). Set `bip` and `default-address-pools` in `daemon.json` before creating networks; existing networks keep their subnets until they are recreated.

---

## 16.8 SNAT Port Exhaustion

Every connection that MASQUERADE or SNAT translates needs a unique tuple. Towards one destination IP and port, that means a unique translated source port. With few addresses and many connections to the same destination, the ports run out. Typical cases are a busy container host, a Kubernetes node, a cloud NAT gateway, or one service calling one API. The hand-built lab limits SNAT to four ports and shows exactly what happens:

```
SNAT --to-source 198.18.140.1:40000-40003 for connections to 198.18.140.10:9000
c1: four held connections                        conntrack: reply dport=40000, 40001, 40002, 40003
c2: a fifth connection                           connect 1: gave up after 3.00s (timeout — no error, no RST)
conntrack -S                                     insert_failed=3 drop=3     (every run)
widen the range to 128 ports                     connect 1: connected after 0.68 ms
```

The fifth connection's SYNs (the original and two retransmissions, Module 11) found no free tuple and were dropped. To the application, it looks like packet loss. The evidence is in **conntrack's statistics** (`insert_failed`, `drop`), in cloud NAT gateway metrics (for example AWS NAT Gateway's `ErrorPortAllocation`), and in the pattern: connections to *one* destination fail while others work. The fixes are more SNAT addresses, connection reuse (pools, keep-alive), or spreading load over more destination addresses.

---

## Hands-On Lab: Container Networking from the Kernel Up

### Lab Goals

- Build a container network by hand and watch each rule change what works
- Inspect Docker's namespaces, veths, bridge, NAT, filter and raw rules, embedded DNS, docker-proxy and modes from the host
- Show that a host INPUT rule does not protect a published port, and that DOCKER-USER does
- Reproduce SNAT port exhaustion and a subnet collision

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- For Part 1: `iproute2`, `iptables`, `conntrack`, `tcpdump`, `curl`
- For Part 2: Docker Engine 28 or later with the iptables backend ([install](https://docs.docker.com/engine/install/ubuntu/)), `busybox-static`, Go (the image is built locally from a static busybox and the course tools; nothing is pulled), and `dnsmasq-base`

Part 2 changes the real host: it creates Docker networks, containers, a veth called `nl-dk-out`, a route for 10.90.0.0/24 and, briefly, two iptables rules. Teardown removes them all, but run it only on the lab VM.

---

### Lab Part 1: A Container Network by Hand

[`containers.json`](../labs/netlab/containers.json): two "containers" (c1 runs a web server) on bridge `cbr0` in a "host" namespace, whose uplink reaches an "internet" namespace. The checks add Docker's rules one at a time:

```
  c1 10.88.0.2 ─┐
                ├─ cbr0 10.88.0.1 [host] eth1 198.18.140.1 ──── internet 198.18.140.10 (web :8080, hold :9000)
  c2 10.88.0.3 ─┘
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/containers.json     # about 30 seconds
```

The checks show:

1. Switching between containers.
2. Egress without, then with, MASQUERADE, with captures and conntrack.
3. The publishing table in 16.4, row by row.
4. Hairpin with br_netfilter off and on.
5. SNAT exhaustion with conntrack's counters.

The `snat-*.txt`, `publish-*.txt` and `hairpin-*.txt` evidence files and the `no-masq.pcap`/`masq.pcap` captures are from three runs; all three gave identical results.

### Lab Part 2: What Docker Builds

[`docker.json`](../labs/netlab/docker.json) builds the `course/netshell:1` image ([`labs/docker/build-image.sh`](../labs/docker/build-image.sh)) and creates the following:

- a user-defined network `nl-dk-app` (10.89.0.0/24, bridge `nl-dk-br0`) with:
  - `nl-dk-web`: a web server, published on 18080 and on 127.0.0.1:18081;
  - `nl-dk-cli`;
  - `nl-dk-ext` (`--dns 198.51.100.10`);
- `nl-dk-def` on the default bridge;
- an "outside" namespace attached to the host by `nl-dk-out` (198.51.100.0/24), running a DNS server for `corp.test` and holding the "corporate" address 10.90.0.20.

```bash
sudo ./bin/netlab run ../netlab/docker.json         # about a minute, most of it building the image
```

The 33 checks cover:

- the veth pairing and SandboxKey;
- the bridge, and both resolv.conf variants;
- name resolution on each network;
- the embedded DNS redirect, and the upstream query's source;
- the NAT, filter and raw rules;
- publishing from outside, the INPUT rule that doesn't help and the DOCKER-USER rule that does;
- loopback publishing and docker-proxy;
- egress masquerade;
- isolation and `network connect`;
- `--internal`, `none`, `host`, and a 1400-byte MTU;
- the subnet collision (16.9).

The outputs quoted in 16.1–16.6 are from these runs; three runs were identical apart from interface indexes and port numbers.

---

### Lab Part 3: By Hand

```bash
sudo ./bin/netlab up ../netlab/containers.json
alias lab='sudo ./bin/netlab exec ../netlab/containers.json'
```

1. **Follow one packet through the hand-built network.** Add the MASQUERADE rule (`lab host -- iptables -t nat -A POSTROUTING -s 10.88.0.0/24 ! -o cbr0 -j MASQUERADE`), then capture on three interfaces while c1 pings out:
   ```bash
   lab c1 -- timeout 3 tcpdump -l -nn -i eth0 icmp & lab host -- timeout 3 tcpdump -l -nn -i veth-c1 icmp &
   lab host -- timeout 3 tcpdump -l -nn -i eth1 icmp & sleep 1; lab c1 -- ping -c1 198.18.140.10; wait
   ```
   The source is 10.88.0.2 on c1's eth0 and on veth-c1 (the same cable), and 198.18.140.1 on eth1. The rewrite happens between the bridge and the uplink, in POSTROUTING.
2. **Without Docker's help.** Remove the rule again (`-D` instead of `-A`) and look at `lab internet -- ip route get 10.88.0.2`: the internet has no way back. This is every "container can't reach X" ticket where X is reached through a router that doesn't know the container subnet.

`sudo ./bin/netlab down ../netlab/containers.json`. Then, with the Docker lab up (`sudo ./bin/netlab up ../netlab/docker.json`):

3. **Unmasqueraded egress hits Docker 28's protection.** `docker network create -o com.docker.network.bridge.enable_ip_masquerade=false --subnet 10.93.0.0/24 nl-dk-nomasq`, run a container on it, and ping 198.51.100.10 (0 replies). `iptables -t raw -L PREROUTING -n -v | grep 10.93` shows the replies counted by the DROP rule (2 packets). Repeat with `-o com.docker.network.bridge.gateway_mode_ipv4=routed` on a new network: the ping works, and there is no MASQUERADE rule for it.
4. **DNS round robin by alias.** Start two containers with `--network nl-dk-app --network-alias api`, then `docker exec nl-dk-cli nslookup api`: both addresses (10.89.0.5 and 10.89.0.6 here).
5. **The translation for a published port.** `ip netns exec nl-docker-outside curl -s --noproxy '*' http://198.51.100.1:18080/`, then `conntrack -L -p tcp --orig-src 198.51.100.10`. The original tuple is `dst=198.51.100.1 dport=18080`, and the reply tuple is `src=10.89.0.2 sport=80 dst=198.51.100.10`. Only the destination changed.
6. **The `@ifb0` illusion.** Compare `bridge link show master nl-dk-br0` with `docker exec nl-dk-cli cat /sys/class/net/eth0/iflink` and `ip -o link`. Work out why `bridge` names the peer `ifb0`.

Remove anything you created by hand (`docker rm -f …; docker network rm …`), then run `sudo ./bin/netlab down ../netlab/docker.json`.

---

### Lab Part 4: Your Own Machine

```bash
docker network ls; docker network inspect bridge -f '{{json .IPAM.Config}}'
ip route | grep -E 'docker0|br-'                      # do any Docker subnets overlap your VPN or LAN routes?
sudo iptables-save | grep -c -i docker; sudo iptables -S DOCKER-USER
docker run --rm busybox cat /etc/resolv.conf          # default bridge: your resolvers
docker network create t && docker run --rm --network t busybox cat /etc/resolv.conf; docker network rm t
docker info | grep -iE 'proxy|firewall|mtu'
```

On Docker Desktop (macOS, Windows), all of this runs inside a hidden Linux VM. The host's `ip` and `iptables` show none of it, and published ports are forwarded by Desktop's own proxy.

---

## Real-World Troubleshooting Scenario

### Scenario: "CI Builds Can't Reach the Internal Artifact Server"

**The situation.**

- After a CI runner host was rebuilt, container-based builds fail to download from `artifacts.corp`, which resolves to 10.90.0.20, an address reached over the company network.
- From the runner host itself, `curl` to the server works.
- Other internet downloads in the builds work.

**Step 1 — Which layer?** Name resolution works (the build log shows the right address). So compare the path inside and outside:

```bash
ip route get 10.90.0.20                       # host: 10.90.0.20 via 198.51.100.10 dev nl-dk-out  ← correct
docker exec build ip route                    # container: 10.90.0.0/24 dev eth0 scope link      ← "on my own link"
docker exec build ping -c1 -W2 10.90.0.20     # 100% packet loss
```

**Step 2 — Why does the container think 10.90.0.20 is local?** Check `docker network inspect`. The CI system's network was created with subnet 10.90.0.0/24, the same range as the corporate network. The container ARPs for 10.90.0.20 on the bridge, and nothing answers. The host keeps working only because its route via the corporate router was added first: `ip route show 10.90.0.0/24` lists both the static route and the bridge's `dev br-… proto kernel` route. Whichever wins on another machine (a VPN connecting after Docker starts, a different metric), the result is fragile.

This is exactly the lab's last check: the host reached 10.90.0.20 (0% loss), the container on the colliding network did not (100% loss), and both routes were present.

**The fix.**

1. Recreate the network from a range nobody uses, and stop Docker choosing colliding ranges: `"default-address-pools": [{"base": "10.200.0.0/16", "size": 24}]` and a `"bip"` for docker0 in `daemon.json`.
2. Restart the daemon.
3. Recreate the networks. Existing networks keep their old subnets.
4. Ask the network team which ranges are free, and document the choice; VPN clients on laptops have the same problem with 172.17–172.31.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| Container names don't resolve | Containers on the default bridge, or on different networks | `resolv.conf` (no 127.0.0.11); `docker network inspect` | User-defined network; `docker network connect` |
| Names resolve, connections time out between networks | Network isolation (DOCKER chain, raw drop) | Ping by IP fails; same-network works | Put both on one network, or publish |
| Container egress fails, host works | No MASQUERADE / `ip_forward=0` / FORWARD policy | Capture on uplink: private source, no reply | Restore Docker's rules (restart daemon); check sysctls |
| Unmasqueraded or routed setup gets no replies | Docker 28+ raw-table drop to container IPs | `iptables -t raw -L PREROUTING -v` counters | `gateway_mode_ipv4=routed`, upstream route to the subnet |
| Published port open despite ufw/INPUT rule | DNAT before INPUT; traffic is forwarded | Rule counters stay 0; client still connects | DOCKER-USER with `--ctorigdstport`, or bind to 127.0.0.1 |
| Published port unreachable from outside | Bound to 127.0.0.1, wrong host port, or DOCKER-USER drop | `iptables -t nat -S DOCKER`; `ss -ltnp` | Correct `-p`; review DOCKER-USER |
| Container calling host:published-port fails | Hairpin without br_netfilter or docker-proxy | Works from outside, not from a container on the same bridge | Keep userland proxy or br_netfilter; call the service by name |
| App sees 172.x/10.x gateway as client IP | docker-proxy (loopback, hairpin) or masquerade between bridges | Source in app logs; `ss` shows docker-proxy | Publish on an external address; use proxy protocol / X-Forwarded-For at a real proxy |
| Container DNS broken after a tool ran | Container's own nat table flushed (DOCKER_OUTPUT gone) | `nsenter -n iptables -t nat -S` | Restart the container; don't flush iptables in containers |
| `--dns` server unreachable from containers only | Upstream queried from the container's namespace | Capture on the bridge | Allow egress, or use a host resolver (host(…)) |
| Corporate/VPN hosts unreachable from containers only | Docker subnet overlaps the corporate range | `docker exec C ip route`; `ip route show RANGE` | `default-address-pools`, `bip`; recreate networks |
| Large transfers hang in containers, not on host | Container MTU 1500 over a smaller uplink without working PMTUD | DF pings; `ip link` in container | Network or daemon `mtu` |
| New outbound connections time out under load | SNAT port exhaustion | `conntrack -S` insert_failed/drop; NAT gateway metrics | More SNAT IPs; connection reuse |
| `docker pull` fails behind a proxy while `curl` works | Daemon has no proxy configuration | `docker info` proxy fields | `daemon.json` `proxies` or systemd drop-in |
| Host can't reach its macvlan containers | macvlan isolates parent from children | Ping from host fails, LAN works | macvlan sub-interface on the host |

---

## Summary

- A container's network is a namespace with a veth into a bridge; the bridge address is its gateway. Docker's namespaces live under `/var/run/docker/netns` (not `ip netns`); enter them with `nsenter`. `eth0@ifN` names the host-side peer.
- Containers on one bridge are switched (ttl 64); everything else is routed by the host and MASQUERADEd (`-s subnet ! -o bridge`). Without NAT, replies have no way back; with NAT off, Docker 28+'s raw-table drop also discards them unless the network is `routed`.
- `-p` is DNAT in PREROUTING and OUTPUT, plus ACCEPT rules; the client address is preserved. 127.0.0.1 and hairpin go through docker-proxy (or need route_localnet, br_netfilter or extra SNAT). Published ports bypass INPUT and ufw; DOCKER-USER with `--ctorigdstport` filters them.
- User-defined networks get the embedded DNS server at 127.0.0.11, implemented by DNAT in the container's namespace to dockerd's sockets. The default bridge copies the host's resolvers. `host(…)` upstreams are queried from the host's namespace, `--dns` upstreams from the container's.
- Networks are isolated from each other; `--internal` has no way out; `none` has only lo; `host` shares the host's stack. Set the MTU when the uplink is smaller than 1500.
- SNAT port exhaustion looks like loss; conntrack's `insert_failed`/`drop` prove it. Docker subnets colliding with corporate ranges break containers only — choose `default-address-pools` deliberately.

## What's Next

Module 17 covers Kubernetes networking: pods built from the same namespaces and veths, but connected by a CNI plugin, Services implemented with the NAT you just read (or eBPF), and network policies in place of Docker's isolation rules.

---

## Quick Reference Card

```
MODULE 16: NETWORKING IN DOCKER & CONTAINERS — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

FIND AND ENTER
  docker inspect -f '{{.State.Pid}} {{.NetworkSettings.SandboxKey}}' C
  nsenter -t PID -n CMD  ·  nsenter --net=/var/run/docker/netns/ID CMD   (host tools, container's network)
  docker exec C cat /sys/class/net/eth0/iflink   →   ip -o link | grep "^N:"   (host-side veth)
  docker network inspect NET -f '{{range .Containers}}{{.Name}} {{.IPv4Address}} {{end}}'

RULES (iptables backend, Docker 28+)
  nat     PREROUTING/OUTPUT --dst-type LOCAL → DOCKER: DNAT --dport HOSTPORT → CIP:PORT
          POSTROUTING -s SUBNET ! -o BRIDGE -j MASQUERADE
  filter  FORWARD → DOCKER-USER (yours) → DOCKER-FORWARD → DOCKER-CT / DOCKER-INTERNAL / DOCKER-BRIDGE → DOCKER
  raw     PREROUTING -d CIP ! -i BRIDGE -j DROP    (unpublished/direct access protection)
  yours   iptables -I DOCKER-USER -i EXTIF -p tcp -m conntrack --ctorigdstport HOSTPORT -j DROP
  nftables backend (experimental, 29+): tables ip/ip6 docker-bridges, no DOCKER-USER

DNS
  user-defined: nameserver 127.0.0.11, ndots:0; names, --network-alias, Compose services
  default bridge: host's resolvers copied; no container names
  host(…) upstreams from the host's namespace · --dns upstreams from the container's namespace
  nsenter -t PID -n iptables -t nat -S DOCKER_OUTPUT    (the 127.0.0.11 redirect)

NETWORK OPTIONS (-o on docker network create)
  com.docker.network.bridge.name=NAME · com.docker.network.driver.mtu=1400
  com.docker.network.bridge.enable_ip_masquerade=false · com.docker.network.bridge.gateway_mode_ipv4=nat|routed|nat-unprotected
  --internal · --subnet · --network host|none · macvlan/ipvlan -o parent=IF

DAEMON (daemon.json)
  "bip", "default-address-pools", "mtu", "proxies": {"http-proxy","https-proxy","no-proxy"},
  "userland-proxy", "iptables", "firewall-backend": "nftables", "ip-forward-no-drop"

EVIDENCE
  conntrack -L -p tcp --orig-src IP · conntrack -S (insert_failed, drop) · iptables -L -v counters
  tcpdump on container eth0 (nsenter), host veth, bridge, uplink — where does the address change?
```

---

## References

- Docker documentation — Networking overview, Bridge network driver, Packet filtering and firewalls (iptables), nftables (experimental), Daemon proxy configuration: https://docs.docker.com/engine/network/
- Docker blog — "Docker Engine v28: Hardening Container Networking by Default" (2025); "Docker Engine v29" (nftables, 2025)
- `man ip-netns`, `man ip-link` (veth), `man bridge`, `man nsenter`, `man namespaces(7)`, `man network_namespaces(7)`
- `man iptables`, `man iptables-extensions` (DNAT, SNAT, MASQUERADE, conntrack, addrtype), `man conntrack`
- Linux `Documentation/networking/ip-sysctl.rst` (ip_forward, route_localnet) and the br_netfilter sysctls
- RFC 1918 — Address Allocation for Private Internets; RFC 7348 — VXLAN
- AWS — NAT gateway CloudWatch metrics (ErrorPortAllocation)
