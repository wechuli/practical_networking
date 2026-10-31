# Kubernetes Lab Profile

The kernel-level behaviour of Module 17 (pod networking, kube-proxy's Service rules,
NodePort traffic policies, hairpin, DNS search lists, failure signatures) is measured
without a cluster in [`../netlab/k8s.json`](../netlab/k8s.json). This directory is the
**real-cluster** track: NetworkPolicy enforcement by a CNI, an Ingress controller and
mesh identity. It needs images from public registries and was not run in the course's
build environment (which cannot reach them); record your versions and results.

Use a **dedicated disposable cluster**, not the current context by accident. The
[runner](lab.py) requires `--context`, creates a unique namespace, waits for
readiness, makes bounded requests, and deletes only that namespace. It does not
install or remove cluster-wide infrastructure. Readiness and curl failures cause
an error rather than an unconditional success message.

## Cluster Preparation

One suitable local profile is minikube with a supported Kubernetes version and
Calico CNI. Check the installed minikube release's supported versions, record the
choice, and supply it explicitly. This provisions local resources, not cloud VMs:

```bash
minikube start -p networking-course --driver=docker --cni=calico \
  --cpus=4 --memory=6144 --kubernetes-version="$KUBERNETES_VERSION"
kubectl --context networking-course get nodes -o wide
kubectl --context networking-course -n kube-system get pods -o wide
.venv/bin/python -m labs.kubernetes.lab --context networking-course --mode policy
```

Set `KUBERNETES_VERSION` to a currently supported minor matching Calico, the
Ingress controller, and Linkerd's compatibility matrix. Pin the resulting version
in the execution record. The policy runner is the decisive preflight: a CNI that
accepts policy objects but ignores them **fails** the denied-client assertion.
No claim is made that plain kind's default networking enforces NetworkPolicy.

## Ingress

Install a maintained controller in this test cluster, for example Traefik, using
a chart version you explicitly select and record from its supported release list.
The community Ingress NGINX project was retired in March 2026 (no further releases or
security fixes); other NGINX-branded controllers are separate projects. This does not mean Ingress API itself retired.

```bash
helm repo add traefik https://traefik.github.io/charts
helm repo update traefik
helm upgrade --install traefik traefik/traefik --version "$TRAEFIK_CHART_VERSION" \
  --kube-context networking-course --namespace traefik --create-namespace \
  --set service.type=ClusterIP --wait --timeout 5m
.venv/bin/python -m labs.kubernetes.lab --context networking-course --mode ingress
```

The runner uses the actual controller Service via loopback port-forward, sends
Host `course.test`, and verifies HTTP from the selected ready backend. This proves
the controller-to-Ingress-to-Service path, not a cloud LoadBalancer or external
firewall. For another controller, supply its class/namespace/service flags.
Inspect Ingress events and EndpointSlices if requests fail. Test a nonmatching Host
and confirm it does not reach this route before claiming host-based isolation.

## Linkerd mTLS

Install a compatible Linkerd release in the test cluster following its
[getting-started instructions](https://linkerd.io/2-edge/getting-started/), and record
the CLI/control-plane versions. The chart/CLI compatibility must be checked before
installation; do not fetch and execute an unreviewed install script.

```bash
linkerd --context networking-course check --pre
linkerd --context networking-course install --crds | kubectl --context networking-course apply -f -
linkerd --context networking-course install | kubectl --context networking-course apply -f -
linkerd --context networking-course check
.venv/bin/python -m labs.kubernetes.lab --context networking-course --mode mesh
```

The runner injects only its lab workloads, confirms baseline requests, applies a
Server plus MeshTLSAuthentication/AuthorizationPolicy bound to the allowed
ServiceAccount, and verifies the other meshed identity is denied. This is stronger
than just seeing a sidecar: success requires the authorized mesh identity. Read
the saved proxy metrics and TLS identity labels using the exact release's
[metric reference](https://linkerd.io/2-edge/reference/proxy-metrics/).
The [policy reference](https://linkerd.io/2-edge/reference/authorization-policy/)
explains the default-deny selected server port and required authentication.

Capture on a pod's network interface, not only application-to-sidecar loopback,
when checking on-wire encryption. Approved debug containers need NET_RAW/NET_ADMIN
as appropriate and compatible admission policy. CoreDNS images may have neither
a shell nor tcpdump. Retrieve pcaps from the named debug container; do not assume
a Docker runtime on containerd/CRI-O nodes.

All containers in a pod share one network namespace and localhost. A mesh does
not automatically propagate application context between inbound and outbound
requests: run the [observability lab](../netlab/observe.json) to see that explicit work.
Encryption, workload identity, authorization, and certificate rotation are separate
properties to verify.

## Cleanup and Scope

The runner removes its namespace on normal errors and completion. If forcibly
killed, inspect and delete only the printed namespace. When all cluster exercises
are complete, delete the **named test profile**:

```bash
minikube delete -p networking-course
```

NetworkPolicy isolation is directional and additive. A namespaceSelector and
podSelector in one peer entry are ANDed; separate entries are ORed. An egress-isolated
client also needs DNS and destination access, and the destination's ingress policy
must permit the flow. Policy admission alone is not enforcement evidence.

Service VIPs can be implemented by iptables, IPVS (including kube-ipvs0), nftables,
or eBPF; do not assume the VIP never belongs to an interface. LoadBalancer mode
need not use NodePort. Long-lived TCP/HTTP2 connections are often selected once,
not per RPC. Readiness removes endpoints for new selections; gracefully drain
existing connections using application/proxy shutdown and HTTP/2 GOAWAY rather
than assuming endpoint deletion closes every stream. Inspect source preservation
with local/cluster traffic policies and actual backend peer identity.
