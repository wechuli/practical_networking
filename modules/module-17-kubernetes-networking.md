# Module 17: Kubernetes Networking

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 16 — Networking in Docker & Containers
> **Estimated time:** 5–6 hours

## Overview

Kubernetes networking is Module 16's parts arranged under a different contract. Each pod is still a network namespace, attached by a veth to a bridge or to a route on its node. Kubernetes then adds rules that Docker does not have:

- Every pod gets its own address.
- Every pod can reach every other pod, on any node, **without NAT**.
- Every node can reach every pod without NAT.

On top of that flat network come:

- **Services:** a stable virtual IP, or a node port, that is translated to the current set of pod addresses. This is done by kube-proxy's rules or an eBPF replacement.
- **Cluster DNS:** names for those Services.
- **NetworkPolicy:** turns the open flat network into an allow-list.

Every Kubernetes network ticket lands somewhere in that stack, and each layer fails with its own signature:

- **Refused at once:** a Service with no ready endpoints.
- **Hangs until the timeout:** a policy drop.
- **Gets the right answer slowly:** a DNS search list walked to the end.
- **Fails one time in four:** a pod calling itself through its own Service.
- **Shows the node's address instead of the client's:** a NodePort with `externalTrafficPolicy: Cluster`.

This module builds that stack on two namespace "nodes", so every packet and rule can be measured:

- pods on per-node bridges;
- flannel-style VXLAN between the nodes;
- Services programmed by `kproxy`, which writes kube-proxy's iptables-mode rule structure, chain names included;
- a cluster DNS server reached through its Service.

For Ingress, Gateway API, CNI operators and service meshes, which need a real cluster, the module gives the concepts and a runner for a disposable cluster on your VM (Part 4).

## What You Will Build Toward

- Trace pod-to-pod traffic within and between nodes: the bridge, the overlay (VXLAN on UDP 8472), and the MTU it costs
- Read kube-proxy's iptables rules: KUBE-SERVICES → KUBE-SVC → KUBE-SEP, KUBE-NODEPORTS → KUBE-EXT, KUBE-MARK-MASQ, REJECT for Services with no endpoints
- Predict the source address a backend sees (pod, node, bridge, client) for ClusterIP, NodePort with `externalTrafficPolicy` Cluster vs Local, and hairpin traffic
- Tell refused (no endpoints) from timeout (policy drop, or `Local` with no local endpoint) by timing and by rule
- Explain cluster DNS: `resolv.conf`, ndots:5 and the search list, and how many queries each resolver actually sends
- Place Ingress, Gateway API, NetworkPolicy engines and service meshes on the same packet path

---

## 17.1 The Network Model

The Kubernetes network model has three requirements:

1. **Pod to pod without NAT.** A pod on node 1 reaches a pod on node 2 at its own address, and the receiver sees the sender's own address.
2. **Node to pod without NAT.** Agents on a node reach any pod.
3. **A pod sees the same address for itself that others use for it.**

The lab's measurements:

```
client pod (node1) → web2 (node2):  "from 10.244.1.3:… served-by web2"   ← the client pod's own address
node1 → web2:                        "from 10.244.1.0:… served-by web2"   ← node1's flannel.1 address; still no NAT
client pod → 192.168.50.100 (outside the cluster): "from 192.168.50.11"   ← masqueraded to the node
```

"Without NAT" applies inside the cluster only. Traffic leaving the cluster CIDR is masqueraded to the node's address, by the CNI or by the node's own rules. Flannel's rule is `-s 10.244.0.0/16 ! -d 224.0.0.0/4 -j MASQUERADE`, after a RETURN for cluster destinations. With VPC-native CNIs, pods already hold routable VPC addresses, and egress SNAT is a configuration choice.

The model also requires **non-overlapping ranges**: a pod CIDR (10.244.0.0/16 here), a per-node slice of it (10.244.1.0/24, 10.244.2.0/24), and a separate Service CIDR (10.96.0.0/12 here). None of them may collide with networks the pods must reach. This is the same lesson as Module 16's subnet collision, and it is one of the decisions that cannot be changed after a cluster is built.

---

## 17.2 CNI: How Pods Get Connected

The kubelet asks the container runtime for a pod sandbox, and the runtime calls a **CNI plugin** with the pod's network namespace. The plugin creates the interface (usually a veth), assigns an address from IPAM, and sets up routes. A separate agent (the "CNI" product: flannel, Calico, Cilium, a cloud's VPC CNI) arranges reachability between nodes and, for some products, enforces NetworkPolicy.

**How pod traffic crosses nodes:**

| Approach | How a packet to another node's pod travels | Examples | Notes |
| --- | --- | --- | --- |
| Overlay (VXLAN, Geneve, IPIP) | Encapsulated between node addresses | flannel (vxlan), Calico (VXLAN/IPIP), Cilium (VXLAN/Geneve) | Works on any underlay. Costs MTU (VXLAN: 50 bytes) and CPU for encapsulation |
| Direct routing | Routes to each node's pod CIDR via that node | flannel host-gw, Calico BGP, Cilium native routing | No overhead. Needs the underlay to route pod CIDRs (same L2, or BGP with the routers) |
| VPC-native | Pods get addresses from the cloud network itself | AWS VPC CNI, Azure CNI, GKE VPC-native | No overlay. Limited by per-node IP or ENI quotas; subject to cloud security groups |

**The overlay, measured** (flannel-style VXLAN, VNI 1, port 8472, as flannel uses on Linux; the IANA port is 4789):

```
node1 eth0:  IP 192.168.50.11.45745 > 192.168.50.12.8472: VXLAN, flags [I] (0x08), vni 1
             IP 10.244.1.3.45148 > 10.244.2.2.8080: Flags [S] … options [mss 1410,…]   ← the pod's packet inside

node1:  10.244.2.0/24 via 10.244.2.0 dev flannel.1 onlink     ← route to node2's pods via node2's VTEP address
        10.244.2.0 lladdr 0a:00:00:00:02:01 PERMANENT          ← the VTEP's MAC, installed by the agent
        0a:00:00:00:02:01 dst 192.168.50.12 self permanent     ← which node owns that MAC (the forwarding entry)
```

Flannel's agent writes these entries from each node's annotations. It does not rely on VXLAN learning or multicast. If they are missing or stale (an agent crash, a node replaced with the same name), cross-node traffic fails while same-node traffic works.

**MTU.** VXLAN's 50 bytes come off the pod MTU: 1450 on a 1500-byte underlay. The pods' SYNs advertised MSS 1410. A 1422-byte DF ping crossed nodes; a 1423-byte one failed inside the pod (`message too long, mtu=1450`). On clouds with 1460 or less, or inside a VPN, the CNI's MTU must be lowered again. When it isn't, the result is Module 14's black hole: small requests work, large responses stall.

**Direct routing, by hand.** In Part 2, replacing the route with `10.244.2.0/24 via 192.168.50.12 dev eth0` (flannel's host-gw) made the same SYN appear on the underlay unencapsulated: `IP 10.244.1.3.53466 > 10.244.2.2.8080`. This works only because both nodes share an L2 segment. Across routers, the routers need routes to the pod CIDRs, which is what Calico's BGP peering provides.

**Choosing a CNI** is mostly about operating constraints:

- Does it enforce NetworkPolicy? Flannel alone does not; k3s pairs it with kube-router's policy controller.
- Does it fit the underlay: overlay, BGP, or the cloud's native routing?
- Observability (Cilium's Hubble).
- Whether to replace kube-proxy (Cilium's eBPF datapath, Calico's eBPF mode).
- What the managed platform supports.

Weave Net, still in many tutorials, is no longer maintained since Weaveworks closed in 2024.

---

## 17.3 Services and kube-proxy

A **Service** is a stable name and virtual IP for a changing set of pods. The control plane turns its selector into **EndpointSlices**: the ready pod addresses and ports. On every node, a dataplane programs the translation. That dataplane is kube-proxy in iptables mode (the default), nftables mode (GA since Kubernetes 1.33 and the intended successor) or IPVS mode (deprecated since 1.35), or an eBPF replacement (Cilium, Calico eBPF).

### The iptables rules, chain by chain

What kube-proxy programs for `default/web` (ClusterIP 10.96.0.20:80 → pods :8080, NodePort 30080), as written by `kproxy` in the lab:

```
-A KUBE-SERVICES -d 10.96.0.20/32 -p tcp -m comment --comment "default/web:http cluster IP" -m tcp --dport 80 -j KUBE-SVC-CDGGSHYLG3RE2FKL
-A KUBE-SVC-CDGGSHYLG3RE2FKL ! -s 10.244.0.0/16 -d 10.96.0.20/32 … -j KUBE-MARK-MASQ        ← traffic from outside the pod CIDR gets SNAT
-A KUBE-SVC-CDGGSHYLG3RE2FKL -m comment --comment "default/web:http -> 10.244.1.2:8080" -m statistic --mode random --probability 0.50000000000 -j KUBE-SEP-UIQK3OSOSTRHRPBX
-A KUBE-SVC-CDGGSHYLG3RE2FKL -m comment --comment "default/web:http -> 10.244.2.2:8080" -j KUBE-SEP-4EX2N4PG4ASNZOBF
-A KUBE-SEP-UIQK3OSOSTRHRPBX -s 10.244.1.2/32 … -j KUBE-MARK-MASQ                          ← hairpin: the endpoint calling itself
-A KUBE-SEP-UIQK3OSOSTRHRPBX -p tcp … -j DNAT --to-destination 10.244.1.2:8080
-A KUBE-NODEPORTS -p tcp … --dport 30080 -j KUBE-EXT-CDGGSHYLG3RE2FKL
-A KUBE-SERVICES … -m addrtype --dst-type LOCAL -j KUBE-NODEPORTS                          ← "must be the last rule in this chain"
-A KUBE-POSTROUTING -m mark ! --mark 0x4000/0x4000 -j RETURN
-A KUBE-POSTROUTING -j MARK --set-xmark 0x4000/0x0
-A KUBE-POSTROUTING -m comment --comment "kubernetes service traffic requiring SNAT" -j MASQUERADE --random-fully
```

How the pieces work:

- **Load balancing** is random per *connection*. With n endpoints, the first rule matches with probability 1/n, the next with 1/(n−1), and so on, so each endpoint gets an equal share. Forty requests from the client split 19/21, 21/19 and 16/24 over three runs. There is no notion of load, and a long-lived connection (HTTP/2, gRPC, a database pool) stays on its first choice (Module 15).
- **Chain names** are the first 16 characters of the base32 SHA-256 of `namespace/name:port` plus the lowercase protocol. They are stable across nodes and clusters: `KUBE-SVC-TCOU7JCQXEZGVUNU` is `kube-system/kube-dns:dns` UDP on every cluster, and `kproxy`'s test checks exactly that. That stability is what lets you grep a real node for a given Service.
- **SNAT is marked, then applied.** `KUBE-MARK-MASQ` sets 0x4000 and `KUBE-POSTROUTING` masquerades marked packets on the way out.
- **conntrack holds each decision.** On node1: `dst=10.96.0.20 dport=80`, with the reply from the chosen pod's `sport=8080`. Changing the endpoints affects new connections only.

### What a ClusterIP is not

The address 10.96.0.20 is on no interface, and only TCP port 80 to it is translated. In the lab, `ping 10.96.0.20` from a pod gave **100% loss**, because nothing answers ICMP. "I can't ping the Service" is expected behaviour and says nothing about whether the Service works. Test the port.

### NodePort and externalTrafficPolicy

A NodePort is a port on every node's own addresses that leads to the Service. `externalTrafficPolicy` decides what happens to traffic from outside:

| | `Cluster` (default) | `Local` |
| --- | --- | --- |
| Which endpoints | Any, on any node | Only those on the node that received it |
| Source the pod sees | SNATed: the node's bridge (10.244.1.1) for local pods, its flannel.1 address (10.244.1.0) for remote ones | **The real client**, 192.168.50.100, from both nodes in all runs |
| Node with no local endpoint | Forwards elsewhere | **Drops** (`"default/web:http has no local endpoints" … -j DROP` in KUBE-EXTERNAL-SERVICES): the client timed out after 3009–3011 ms while node2 still answered |
| Balance | Even across endpoints | Even across *nodes* that the load balancer sends to |

`Local` keeps client addresses, which access logs, rate limits and geo rules need, and avoids a second hop. Its cost is that a load balancer must stop sending to nodes without endpoints. Cloud load balancers do this through `healthCheckNodePort`, which kube-proxy answers per node with the local endpoint count. When the health check is misconfigured, a share of new connections time out after every scale-down or rollout.

### Hairpin: a pod calling its own Service

When the random choice sends a pod's connection to the pod itself, the packet must go back out of the same bridge port it arrived on. Bridges don't do that unless the port has **hairpin mode**, which the kubelet sets (`--hairpin-mode=hairpin-veth`). With hairpin off, 30 calls from web1 to its own Service gave:

| Run | Fast | Slow (~1 s) | Failed |
| --- | --- | --- | --- |
| 1 | 16 | 8 | 6 |
| 2 | 13 | 10 | 7 |
| 3 | 18 | 6 | 6 |

The *slow* outcome is the interesting one. A SYN that was dropped before its conntrack entry was confirmed leaves no entry, so the retransmitted SYN, a second later, goes through the random choice again. Half of those land on the other pod and succeed. So the symptom of missing hairpin is a mixture of 1-second delays and failures, roughly 1 in 4 calls each with two endpoints, not a clean failure.

With hairpin on, every call worked. Calls to itself arrived from 10.244.1.1 (the bridge, because of the `-s <endpoint> -j KUBE-MARK-MASQ` rule); calls to web2 arrived from web1's own address.

### Refused vs timeout: reading the failure

| Cause | What the rules do | Client sees (measured) |
| --- | --- | --- |
| Service with **no ready endpoints** (scaled to zero, selector matches nothing, all pods failing readiness) | `REJECT --reject-with icmp-port-unreachable` ("… has no endpoints") | `refused after 0.19–0.29 ms` |
| **NetworkPolicy** denies (enforced as a drop on the destination's node) | DROP | `gave up after 3.00s` (the client's timeout) |
| `externalTrafficPolicy: Local`, node without local endpoint | DROP | timeout |
| Pod not ready | Its endpoint leaves the EndpointSlice; rules exclude it | Traffic goes only to the others (marking web2 not ready: 20/20 requests to web1) |

"Connection refused" from a Service means **look at the endpoints** (`kubectl get endpointslices -l kubernetes.io/service-name=web`, labels, readiness). "Timed out" means **look for something dropping**: policy, a security group, `Local` routing, or a dead node.

### LoadBalancer, ExternalName, headless

- **LoadBalancer** asks a controller (the cloud's, MetalLB, kube-vip) for an external address. Most send to NodePorts. Some target pod IPs directly (AWS Load Balancer Controller in IP mode, GKE container-native load balancing), and some set `allocateLoadBalancerNodePorts: false`.
- **ExternalName** is only a DNS CNAME; no proxying, no ports.
- **Headless** (`clusterIP: None`) has no virtual IP: DNS returns the pod addresses themselves, which is what StatefulSets and client-side load balancers (gRPC, Module 15) use.

---

## 17.4 Cluster DNS

Each pod's `/etc/resolv.conf`, written by the kubelet for `dnsPolicy: ClusterFirst`, is exactly the lab's:

```
search default.svc.cluster.local svc.cluster.local cluster.local
nameserver 10.96.0.10            ← the kube-dns Service; CoreDNS pods behind it
options ndots:5
```

Names are `service.namespace.svc.cluster.local`. From the same namespace, `web` is enough; from another namespace, `web.default`. CoreDNS answers cluster names from the API and forwards everything else to the node's resolvers (`forward . /etc/resolv.conf`). In the lab, the upstream saw only `api.example.com`, from node2's address, and never a `cluster.local` name.

### ndots:5 and the search list, measured

A name with fewer than five dots is tried with each search domain *before* being tried as-is. The number of queries depends on the resolver:

| Lookup from the pod | Queries CoreDNS received (all runs) |
| --- | --- |
| `getent ahosts web` (glibc) | **1**: `web.default.svc.cluster.local`, answered on the first try |
| `getent ahosts api.example.com` (glibc, IPv4-only pod) | **4**: A for `…default.svc.cluster.local`, `…svc.cluster.local`, `…cluster.local`, then `api.example.com` |
| `dnslab lookup api.example.com` (Go's resolver) | **8**: A *and* AAAA for each of the four names |
| `getent ahosts api.example.com.` (trailing dot) | **1** |

glibc sent no AAAA queries because the pod has no IPv6 address (`AI_ADDRCONFIG`). Go's resolver, and many others, ask for both regardless. So "8 queries per external lookup" is true for some applications and not others, and the only way to know is to count at the server.

The fixes trade cost between kinds of names:

- **A trailing dot** in configuration (`api.example.com.`) makes a name absolute.
- **`dnsConfig.options: ndots: "1"`** (or 2) makes names with dots go out as-is first. In the lab, `api.example.com` then cost 1 query, `web` still resolved on the first search domain, but `web.default` now cost an extra query, because it is tried as an absolute name before the search list.
- **NodeLocal DNSCache** puts a caching resolver on each node, so the amplified queries are answered locally.
- **Autopath** (a CoreDNS plugin) answers the search walk server-side.

### DNS failures have the same two signatures

| Cause | Measured from `getent hosts web` |
| --- | --- |
| kube-dns Service with **no endpoints** (CoreDNS pods all down or not ready) | REJECT → ICMP port unreachable → `exit 2 after 2 ms` |
| CoreDNS **dropped** (a NetworkPolicy without a DNS egress rule, a node firewall) | `exit 2 after 40040–40048 ms`: glibc's 5 s timeout × 2 attempts, for each of the 4 names on the search list |

A default-deny **egress** policy that forgets DNS is one of the most common Kubernetes outages. Every name lookup then takes 40 seconds to fail, so it looks like "the application hangs on startup", not like a network error. Allow UDP and TCP 53 to the kube-dns pods, by namespace and pod selector, not by the Service IP: policy is evaluated after the Service translation.

The intermittent **5-second DNS delay** seen on some clusters has a different cause. A conntrack race between parallel A and AAAA queries sent from one UDP socket and translated by the DNS Service can drop one of them. The resolver's retry after its 5 s timeout then succeeds. Kernel fixes reduced the race (clash resolution), and NodeLocal DNSCache, `single-request-reopen`, or TCP avoid it. It was not reproduced in this lab. Confirm it with captures and `conntrack -S` (`insert_failed`, `clash_resolve`) before blaming it.

---

## 17.5 NetworkPolicy

Without any policy, every pod accepts traffic from everywhere. A NetworkPolicy *selects* pods, and for each direction it lists (`policyTypes`), those pods accept only what some policy explicitly allows. Policies are additive allow-lists: there is no "deny" rule in the core API.

Things that trip people up:

- **Isolation is per direction.** An ingress policy on the server does not restrict the client's egress, and vice versa. A connection needs both sides to allow it, when both are isolated.
- **Selectors are ANDed within one peer entry and ORed across entries.** `- namespaceSelector: … podSelector: …` in one item means "these pods in those namespaces"; as two items, it means "either".
- **Policies apply to pod addresses after Service translation.** Allow the backend pods, not the ClusterIP. `ipBlock` is for traffic outside the cluster, and is affected by SNAT (an `externalTrafficPolicy: Cluster` NodePort hides the client).
- **Default-deny egress also blocks DNS** (17.4).
- **Enforcement is the CNI's job.** The API server accepts NetworkPolicy objects even when nothing enforces them (flannel alone). Test with a connection that should be denied; seeing the object does not prove it works.

**The enforcement point is a drop.** The lab installs what policy engines do: a rule on the destination's node, in the forwarding path, before the pod.

```
iptables -I FORWARD -d 10.244.2.2/32 -p tcp --dport 8080 -m conntrack --ctstate NEW -j DROP
client → web2:  connect 1: gave up after 3.00s
```

Real engines use per-pod chains (Calico `cali-*`, kube-router `KUBE-POD-FW-*`), ipsets, or eBPF maps (Cilium, where `hubble observe --verdict DROPPED` shows each drop with the policy that caused it). The signature is the same: the SYN disappears, and the client waits for its own timeout. The *AdminNetworkPolicy* API, being developed by SIG Network's policy subproject, adds cluster-scoped, ordered rules, including real deny, for platform teams; check its status before relying on it.

---

## 17.6 Ingress and Gateway API

**Ingress** routes HTTP(S) by host and path to Services. An **Ingress controller** (a proxy running in the cluster, usually behind a LoadBalancer Service) does the work. Annotations carry everything the API does not cover, so they are controller-specific.

**Ingress NGINX**, the community controller in most tutorials, was retired: best-effort maintenance ended in March 2026, with no further releases or security fixes after that. Existing installations keep running. The Kubernetes project recommends moving to Gateway API or another maintained controller. The Ingress API itself is not deprecated.

**Gateway API** splits the job by role:

- `GatewayClass` (infrastructure);
- `Gateway` (listeners, addresses, TLS: the platform team);
- routes attached to it by application teams: `HTTPRoute`, `GRPCRoute`, `TLSRoute` (Standard since v1.5, 2026), and `TCPRoute`/`UDPRoute` (still experimental in v1.5).

Cross-namespace references need an explicit `ReferenceGrant`. Header matching, traffic splitting and mirroring are in the API, not in annotations.

**Where things go wrong on this path:**

1. **The load balancer to the controller.** Health checks, `externalTrafficPolicy`, security groups.
2. **The controller to the route.** No matching host or path gives the controller's own 404. A Gateway or route that is not attached shows in `status.conditions` (`Accepted`, `ResolvedRefs`, `Programmed`).
3. **The route to the Service and endpoints.** No endpoints gives the controller's 503. The wrong `targetPort` gives 502 or a connection refused from the pod.
4. **TLS.** Module 13's failures, now with a certificate in a Secret: the wrong Secret, a missing intermediate, SNI routing.

Read the response's origin: a 404 from the controller differs from a 404 from the application (headers and body differ).

---

## 17.7 Service Mesh

A mesh puts a proxy on the path of each workload's traffic. That proxy is either a sidecar per pod (Istio's classic mode, Linkerd), or a per-node proxy for L4 with optional per-namespace L7 proxies (Istio **ambient** mode, GA since Istio 1.24; Cilium's mesh). It provides:

- workload identity (certificates issued per ServiceAccount, usually SPIFFE IDs);
- **mTLS** between proxies;
- authorization by identity;
- retries, timeouts and traffic splitting;
- per-request metrics.

**What to verify rather than assume:**

- **Coverage.** Only enrolled pods and ports are meshed. Traffic to non-meshed pods, to the outside, or on excluded ports is plain unless policy forbids it. "Permissive" modes accept both.
- **On-wire encryption.** Capture on the pod's interface (or the node's veth), not inside the pod where the application talks plain text to its sidecar.
- **Identity is not authorization.** A policy must say which identities may call which servers (Linkerd `AuthorizationPolicy`/`MeshTLSAuthentication`, Istio `AuthorizationPolicy`), and a denied call must be tested.
- **Tracing needs the application.** The proxies propagate headers only if the application copies the incoming trace context onto its outgoing calls.

**How meshes intercept traffic:**

- **Sidecars** redirect pod traffic to the proxy with iptables rules in the pod's namespace (installed by an init container or a CNI plugin), the same technique as Docker's 127.0.0.11 (Module 16).
- **Ambient mode** redirects at the node instead.
- **Debugging:** the mesh's own tooling first (`istioctl proxy-status` / `linkerd diagnostics`), then the proxy's admin endpoints, then captures outside the pod.

---

## 17.8 Debugging from the Node

When `kubectl exec` is not enough (a distroless image, no shell, no tools), go to the node.

**Find the pod's network namespace.**

```bash
crictl pods --name web -q
crictl inspectp ID | jq '.info.runtimeSpec.linux.namespaces[] | select(.type=="network").path'
```

or the PID of any container in the pod, then `nsenter -t PID -n …` (Module 16).

**Find its veth.** Read `/sys/class/net/eth0/iflink` inside the pod, then `ip -o link | grep "^N:"` on the node. Capture there:

```bash
tcpdump -i vethXXXX
```

That is outside the pod, so it is where mesh encryption is visible.

**Ephemeral debug containers.**

```bash
kubectl debug -it POD --image=… --target=CONTAINER
```

This adds a container to a running pod that shares its network namespace, with the tools the image lacks. The `NET_ADMIN`/`NET_RAW` capabilities it needs for captures may be blocked by admission policy.

**Images are pulled by the node, not the pod.** Pulls use the node's DNS, routes, proxy and registry credentials, not the pod's. An `ImagePullBackOff` on one node only is a node problem: its DNS, its egress, its proxy configuration (Module 16's daemon proxy, here for containerd), its disk. Pod networking is not involved.

**Rule and drop inspection.**

```bash
iptables-save | grep -E 'KUBE-(SVC|SEP)-.*default/web'       # a Service's rules on that node
nft list ruleset | grep -A3 web                                # kube-proxy nftables mode
cilium-dbg service list; hubble observe --verdict DROPPED      # Cilium
conntrack -L -d CLUSTER_IP; conntrack -S                       # decisions and insert failures
```

---

## Hands-On Lab: The Kubernetes Network on Two Nodes

### Lab Goals

- Trace pod traffic within and between nodes: bridge, VXLAN, MTU, egress masquerade
- Read kube-proxy's Service rules and see their effect: spread, conntrack, hairpin, NodePort with both traffic policies
- Reproduce the failure signatures: refused vs timeout for Services, 2 ms vs 40 s for DNS
- Count DNS queries per lookup under ndots:5 for glibc and Go

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- `iproute2`, `iptables`, `conntrack`, `tcpdump`, `curl`, `dnsmasq-base`, and the `br_netfilter` kernel module

**What is real and what is a stand-in.** The kernel datapath is real:

- namespaces, veths, bridges, the VXLAN device and its forwarding entries;
- iptables, conntrack, br_netfilter and hairpin;
- glibc's and Go's resolvers.

The Kubernetes components are replaced by their effects:

- **`kproxy`** ([`labs/go/cmd/kproxy`](../labs/go/cmd/kproxy)) writes kube-proxy's iptables-mode rules (same chains, names, comments and order) from [`labs/kubernetes/services.json`](../labs/kubernetes/services.json) instead of the API.
- **The VXLAN entries** are what flannel's agent would install.
- **The cluster DNS** is dnsmasq answering `cluster.local` names and forwarding the rest, as CoreDNS does.
- **The policy** is the drop a policy engine installs.

Part 4 runs the real components on a disposable cluster.

### Lab Part 1: Run the Lab

[`k8s.json`](../labs/netlab/k8s.json):

```
                       lan 192.168.50.0/24 (the underlay)
    ┌──────────────────────────────┬──────────────────────────────┬──────────────────┐
  node1 192.168.50.11            node2 192.168.50.12            external 192.168.50.100
  cni0 10.244.1.1 · flannel.1    cni0 10.244.2.1 · flannel.1    (upstream DNS, a web server,
   ├ web1   10.244.1.2 :8080      ├ web2    10.244.2.2 :8080     and the nodes' default gateway)
   └ client 10.244.1.3            └ coredns 10.244.2.3 :53
  Services (kproxy on both nodes): web 10.96.0.20:80 → :8080, NodePort 30080 · kube-dns 10.96.0.10:53
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/k8s.json        # about 2 minutes, most of it the 40-second DNS check
```

The 50 checks produce the measurements in 17.1–17.5 (three runs). The evidence directory includes:

- the capture `vxlan.pcap` and its decode, and `flannel.txt`;
- `kube-proxy-rules.txt`, `spread.txt`, `conntrack.txt`;
- `hairpin-off.txt` and `hairpin-on.txt`;
- `nodeport-*.txt`, `no-endpoints.txt`, `policy-drop.txt`;
- `dns-*.txt`, and the DNS servers' query logs (`coredns.log`, `upstream-dns.log`).

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/k8s.json
alias lab='sudo ./bin/netlab exec ../netlab/k8s.json'
S=../kubernetes/services.json
```

1. **Read the rules before installing them.** `./bin/kproxy show -f $S -node node1` prints the iptables-restore input. Make a copy with `"externalTrafficPolicy": "Local"` and compare `show` for node1 and node2: a `KUBE-SVL-…` chain listing only that node's endpoint. Remove node1's endpoint and node1 gets the `has no local endpoints … DROP` rule instead.
2. **Readiness.** Copy the file, add `"ready": false` to web2's endpoint, and `lab node1 -- ./bin/kproxy sync -f COPY -node node1` (and node2). Twenty requests from the client then all went to web1 (`20 served-by web1`). Sync the original file to restore.
3. **Overlay vs direct routing.** `lab node1 -- ip route replace 10.244.2.0/24 via 192.168.50.12 dev eth0` and the reverse on node2. A capture on node1's eth0 now shows the pod packets themselves (`10.244.1.3.53466 > 10.244.2.2.8080`), no VXLAN. Put the flannel routes back with `ip route replace 10.244.2.0/24 via 10.244.2.0 dev flannel.1 onlink` (and the reverse).
4. **Change ndots for one pod.** The pod's resolv.conf is a bind-mounted file, so `sed -i` cannot replace it. Rewrite it in place:
   ```bash
   lab client -- sh -c "sed 's/ndots:5/ndots:1/' /etc/resolv.conf > /tmp/rc && cat /tmp/rc > /etc/resolv.conf"
   ```
   Then repeat the counts from the coredns log: `api.example.com` 1 query, `web` 1 query, but `web.default` now tries `web.default` as-is first (forwarded upstream), then the search list. Restore ndots:5 the same way.
5. **Debug from the node.** `lab node1 -- tcpdump -nn -i veth-client` while the client calls the Service: the packets leave the pod to 10.96.0.20 and come back from 10.96.0.20. The translation happens in the node, between this veth and wherever the endpoint is.

`sudo ./bin/netlab down ../netlab/k8s.json`.

### Lab Part 3: Compare with a Real Node

On any cluster you can read (a kind, minikube or k3s cluster on your VM, or a managed one where you have node access):

```bash
sudo iptables-save | grep -c KUBE-SVC                     # one chain per service port with endpoints (iptables mode)
sudo iptables-save | grep 'kube-dns:dns cluster IP'       # → -j KUBE-SVC-TCOU7JCQXEZGVUNU on every cluster
kubectl -n kube-system get cm kube-proxy -o yaml | grep -i 'mode:'  # kubeadm clusters; empty = iptables
kubectl get endpointslices -A | head
kubectl run t --rm -it --image=busybox:1.36 --restart=Never -- cat /etc/resolv.conf
```

### Lab Part 4: A Real Cluster (on Your VM)

[`labs/kubernetes`](../labs/kubernetes) has a runner (`lab.py`) and manifests for a disposable cluster: NetworkPolicy enforcement with a denied client, an Ingress through a maintained controller, and mesh identity with Linkerd. Its [README](../labs/kubernetes/README.md) gives the cluster preparation. These need cluster images from public registries, which the course's build environment could not reach, so this part has not been run for this edition of the notes. Record versions and results when you run it.

---

## Real-World Troubleshooting Scenario

### Scenario: "One Request in Four Is Slow or Fails — Only for the Report Service"

**The situation.**

- The report service calls its own public API through its Service name (`http://reports/…`) to render sub-reports.
- After a move to a self-managed cluster, about a quarter of those internal calls take just over a second, and a similar share time out.
- Calls from other services to `reports` are fine.
- Running the same image on the old cluster shows no problem.

**Step 1 — Who is affected?** Only calls *from* a `reports` pod *to* the `reports` Service. Two replicas. Timeouts and 1-second delays in roughly equal numbers.

**Step 2 — Rule out the obvious.** Endpoints are both ready (`kubectl get endpointslices -l kubernetes.io/service-name=reports`). DNS resolves on the first query. No NetworkPolicy selects the pods.

**Step 3 — The pattern.** A pod calling its own Service lands on itself with probability ½ per SYN, because kube-proxy chooses randomly per connection. The ~1 s delays are retransmitted SYNs that were re-balanced after the first one was dropped. On the node:

```bash
bridge -d link show dev vethXXXX | grep -o 'hairpin [a-z]*'     # hairpin off
```

In the lab, 30 such calls gave 13–18 fast, 6–10 slow and 6–7 failed.

**The fix.** Enable hairpin on the pod bridge ports. That is the kubelet's `--hairpin-mode=hairpin-veth` (or `promiscuous-bridge`), or the CNI's equivalent. The `bridge` CNI plugin has `"hairpinMode": true`, which the new cluster's hand-written CNI configuration had left out. Afterwards, all calls succeeded; self-calls arrive from the bridge address because of kube-proxy's `KUBE-MARK-MASQ` for an endpoint calling itself. Calling `localhost` for self-requests, where the application allows it, avoids the Service path entirely.

**What made it findable:** the failure was conditional on caller and callee being the same pod, and its rate matched the endpoint count. Intermittent errors with a clean ratio usually come from a random choice somewhere: a load balancer, a DNS answer order, a hash.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| `connection refused` to a Service, immediately | No ready endpoints (selector, labels, readiness, scaled to zero) | `kubectl get endpointslices -l kubernetes.io/service-name=S`; REJECT "has no endpoints" in KUBE-SERVICES | Fix labels/selector/readiness |
| Connections to a Service or pod time out | NetworkPolicy drop, cloud security group, dead node, CNI down | Same call by pod IP; policy listing; drops (`hubble observe`, counters) | Allow the flow on both sides; fix the node/CNI |
| NodePort/LoadBalancer times out for some connections | `externalTrafficPolicy: Local` and traffic sent to nodes without endpoints | `has no local endpoints` DROP; LB health checks | Use `healthCheckNodePort` correctly, or `Cluster` |
| Backend logs show node or bridge IPs, not clients | `externalTrafficPolicy: Cluster` (SNAT) | Source in logs; KUBE-EXT MASQ rule | `Local`, or proxy protocol / X-Forwarded-For at the LB |
| Pod calling its own Service: ~1 s delays and failures | Hairpin mode off on the pod's bridge port | `bridge -d link show` hairpin off; failures only for self-calls | kubelet/CNI hairpin mode |
| "Can't ping the Service IP" | Expected: only service ports are translated | Test the port instead | — |
| Long-lived clients all hit one pod | Connection-level balancing (HTTP/2, gRPC, pools) | Per-pod request counts | L7 balancing, client-side LB with headless Service, max connection age |
| Every lookup takes ~40 s then fails | DNS dropped (egress policy without DNS, node firewall) | `time getent hosts NAME`; capture shows queries without replies | Allow UDP/TCP 53 to CoreDNS pods |
| Lookups fail instantly | kube-dns has no endpoints (CoreDNS down) | Endpoints of kube-dns; CoreDNS pods | Fix CoreDNS (resources, crash loops, Corefile) |
| External names slow, CoreDNS overloaded | ndots:5 search walk (4–8 queries per lookup) | Count queries per lookup at CoreDNS | Trailing dots, lower ndots, NodeLocal DNSCache, autopath |
| Intermittent exactly-5 s DNS delays | Lost query (conntrack race, overload, packet loss) | Captures; `conntrack -S`; CoreDNS metrics | NodeLocal DNSCache, single-request-reopen, TCP |
| Same-node pod traffic works, cross-node fails | Overlay broken (VXLAN port blocked, missing fdb/neigh entries, CNI agent down) or MTU | Capture on underlay (UDP 8472/4789); `bridge fdb show dev flannel.1` | Open the overlay port between nodes; restart agent; fix MTU |
| Small requests work, large responses stall across nodes | Overlay MTU too large for the underlay | DF pings between pods at 1450/1422 | Lower the CNI MTU |
| `ImagePullBackOff` on one node | Node's DNS, egress, proxy, credentials or disk | Pull with `crictl pull` on that node | Fix the node, not the pod |
| Policy objects exist but nothing is blocked | CNI does not enforce NetworkPolicy | Denied-path test succeeds | Policy-capable CNI or policy controller |

---

## Summary

- A pod is a namespace with a veth to its node; pods reach each other and nodes reach pods without NAT, by an overlay (VXLAN on UDP 8472 for flannel, 50 bytes of MTU: pods at 1450) or by direct routes; traffic leaving the cluster is masqueraded to the node.
- kube-proxy's iptables mode is a chain per Service port and per endpoint: random choice per connection (statistic probabilities 1/n…), DNAT in KUBE-SEP, SNAT marked with 0x4000. Names are hashes of `namespace/name:port` + protocol and are the same on every cluster. nftables mode is GA (1.33) and IPVS is deprecated (1.35).
- A ClusterIP is not pingable. NodePort with `externalTrafficPolicy: Cluster` hides the client (SNAT to the node's bridge or VTEP address); `Local` keeps it and drops on nodes without endpoints. A pod calling its own Service needs hairpin mode, and without it fails or stalls about one call in four.
- No endpoints → REJECT → refused in under a millisecond; policy drop → timeout. In DNS: 2 ms for a kube-dns without endpoints, 40 s for dropped DNS with glibc defaults and ndots:5.
- ndots:5 makes external names walk the search list: 4 queries from glibc on an IPv4-only pod, 8 from Go's resolver, 1 with a trailing dot.
- NetworkPolicy is per-direction, additive, applied after Service translation, and enforced only by a capable CNI — test a denied path. Ingress NGINX is retired; Gateway API is the successor for L7 routing. A mesh adds identity and mTLS for enrolled traffic; verify coverage, encryption on the wire and authorization separately.

## What's Next

Module 18 covers firewalls, security groups and zero trust — the drop rules this module kept meeting, from host firewalls and cloud security groups to policy engines and identity-based access.

---

## Quick Reference Card

```
MODULE 17: KUBERNETES NETWORKING — QUICK REFERENCE
═══════════════════════════════════════════════════

MODEL        pod↔pod and node↔pod without NAT · pod CIDR / node slices / Service CIDR must not overlap anything
OVERLAY      VXLAN: UDP 8472 (flannel, Linux) or 4789 · −50 bytes MTU (1450) · fdb + neigh entries per remote node
SERVICES     iptables (default) / nftables (GA 1.33) / IPVS (deprecated 1.35) / eBPF
             KUBE-SERVICES → KUBE-SVC-<hash> → (statistic random) → KUBE-SEP-<hash> → DNAT
             KUBE-NODEPORTS → KUBE-EXT-<hash> (→ KUBE-SVL for Local) · KUBE-MARK-MASQ 0x4000 → KUBE-POSTROUTING
             no endpoints: filter KUBE-SERVICES REJECT ("has no endpoints") → refused
             Local without local endpoints: KUBE-EXTERNAL-SERVICES DROP → timeout
             hash = base32(sha256("ns/name:port" + "tcp"|"udp"))[:16]   e.g. kube-dns:dns udp = TCOU7JCQXEZGVUNU
SOURCE IP    ClusterIP from pod: pod IP · NodePort Cluster: node (SNAT) · NodePort Local: client · self-call: bridge
DNS          nameserver = kube-dns ClusterIP · search <ns>.svc.cluster.local svc.cluster.local cluster.local · ndots:5
             svc.ns.svc.cluster.local · headless → pod IPs · ExternalName → CNAME
             trailing dot · dnsConfig ndots · NodeLocal DNSCache · count queries at the server
POLICY       per direction · additive allow · ANDed within a peer, ORed across · after Service DNAT · allow DNS egress
             enforcement = CNI (flannel alone: none) · test a DENIED path
FAILURES     refused → endpoints · timeout → drops (policy, SG, Local, dead node) · 1-in-N → random choice (hairpin, LB)

COMMANDS     kubectl get svc,endpointslices -A · kubectl get netpol -A · kubectl debug -it POD --image=… --target=C
             kubectl exec POD -- cat /etc/resolv.conf · kubectl -n kube-system logs -l k8s-app=kube-dns
             node: iptables-save | grep KUBE-SVC · nft list ruleset · conntrack -L -d VIP · conntrack -S
                   crictl inspectp · nsenter -t PID -n · tcpdump -i vethXXXX · bridge -d link show (hairpin)
             cilium-dbg service list · hubble observe --verdict DROPPED
COURSE TOOLS kproxy show|sync -f services.json -node N · netlab run ../netlab/k8s.json
```

---

## References

- Kubernetes documentation — Services, EndpointSlices, Virtual IPs and Service Proxies (kube-proxy modes), DNS for Services and Pods, Network Policies, Ingress, Gateway API: https://kubernetes.io/docs/concepts/services-networking/
- Kubernetes source — `pkg/proxy/iptables/proxier.go` (chain layout and naming)
- KEP-3866 (nftables kube-proxy), KEP-5495 (deprecate IPVS mode)
- Kubernetes blog — "Ingress NGINX Retirement: What You Need to Know" (2025); "Gateway API v1.5: Moving features to Stable" (2026)
- CNI specification — https://www.cni.dev/docs/spec/; flannel backends (vxlan, host-gw); Calico and Cilium documentation
- RFC 7348 — VXLAN
- CoreDNS manual (kubernetes, forward, cache, autopath plugins); NodeLocal DNSCache
- Istio (ambient mode), Linkerd (authorization policy) documentation
- `man resolv.conf` (ndots, search, timeout, attempts, single-request-reopen), `man getaddrinfo` (AI_ADDRCONFIG)
