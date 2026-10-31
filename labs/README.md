# Lab Contract

Run commands from the networking repository directory unless a lesson explicitly
names another location. This course's practical core is Linux and application
networking. AWS exercises are optional; Azure and GCP comparisons are not a claim
that the same commands or dataplanes work on all three providers.

## Environments

| Profile    | Baseline                                                                                             | Requirements                                                                                 |
| ---------- | ---------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------- |
| Portable   | Go 1.24+ for [go/](go/README.md); Python 3.12 or 3.13 and Node.js 22 or 24 for labs not yet ported; OpenSSL 3.x | Loopback sockets; no root or cloud account                                                   |
| Linux      | Disposable Ubuntu 24.04 LTS VM, Linux 6.8 or a recorded supported HWE kernel, Bash 5.x, iproute2 6.x | Root inside that VM, namespaces/veth/netfilter enabled                                       |
| Containers | Rootful Docker Engine 28.x or a recorded newer version, Compose v2                                   | Running daemon; internet access for initial image pulls                                      |
| Kubernetes | Dedicated local cluster with a NetworkPolicy-enforcing CNI                                           | kubectl, Helm, enough RAM for the specified controllers; never a production context          |
| Cloud      | An owned sandbox and explicit account/region/resource identifiers                                    | Read permissions for inspection; separately approved provisioning, budget, and deletion plan |

These are target profiles, not a claim that every OS/version combination was
executed during authoring. Record `uname -a`, `python3 --version`, `openssl version`,
`curl -V`, `docker version`, `docker compose version`, and cluster/CNI versions with
results, plus `go version` for Go labs. HTTP/2, HTTP/3, TLS key logging, and NetworkPolicy require feature checks,
not just a minimum version number. Docker Desktop's hidden Linux VM is not the
host network shown by rootful Linux bridge diagrams.

## Linux Setup

On the disposable VM, install tools without enabling a DHCP or RA server on its
management network:

```bash
sudo apt-get update
sudo apt-get install -y iproute2 iputils-ping iputils-arping iputils-tracepath traceroute mtr-tiny \
  dnsutils dnsmasq-base bind9 bind9-utils unbound tcpdump tshark iptables nftables conntrack frr \
  ethtool netcat-openbsd curl openssl iperf3 hping3 nmap wireguard-tools openvpn openssh-client openssh-server \
  strongswan-swanctl strongswan-charon libcharon-extra-plugins libstrongswan-standard-plugins \
  python3 python3-venv python3-pip jq nghttp2-client chrony busybox haproxy nginx-light
```

Configure Docker using its [Ubuntu installation instructions](https://docs.docker.com/engine/install/ubuntu/)
and verify the daemon before container labs. Docker group membership grants
effective root access. Capture privileges also expose sensitive traffic: use the
distribution's dumpcap/wireshark-group setup or scoped root commands in the lab
VM, not unrestricted packet-capture capabilities for every executable/user.

## Command Classes

- **Inspection:** read-only commands such as `ip route`, `ss`, and saved-pcap analysis.
- **Runnable lab:** an explicit invocation of a checked-in fixture, with its own
  preflight, bounded requests, baseline/failure/recovery assertions, and cleanup.
- **Configuration template:** commands in explanatory sections with resource names,
  addresses, or placeholders. Read and adapt these; they are not a script to run
  sequentially on a workstation. Rules in separate examples are alternatives.

Failure injection belongs in owned namespaces, containers, or a disposable VM.
Do not change the SSH interface, flush host firewall rules, or exhaust shared
host resources. Network namespaces isolate many network settings, but not every
kernel resource: in particular, do not lower global conntrack limits as a shortcut.

The Linux fixtures create unique namespaces with no external route, trap exits and
signals, and preserve restricted evidence directories. A SIGKILL or VM crash cannot
run a trap; use `ip netns list` and `ip netns pids NAME` to inspect the exact printed
lab-owned names before cleanup, or discard the VM. Never delete unrelated namespaces.
Temporary listeners serve only a dedicated directory, not the whole of `/tmp`.

## Evidence and Assessment

Protocol fields and successful/failed assertions are invariants. Public IPs,
certificate chains, timings, packet numbers, segmentation, routes, and counters
are environment-dependent. Unattributed packet/log transcripts and incident
stories in the lessons are **synthetic illustrations**, not measured output or
claims about a named provider's incident. Record what you actually observed.

For each fault, submit the affected flow, observation point, baseline, failure,
relevant packets/counters, a rejected alternative, the minimal fix, the original
workload retest, and cleanup evidence. A printed success message without an
assertion is not verification. Compare captures from both ends when necessary;
do not infer absence from a filter that excludes the protocol of interest.

## Checks

The labs are Go programs and netlab lab files. A Python environment is needed only
for Module 15's HTTP/3 client (aioquic) and the Kubernetes runner:

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -r labs/requirements.txt
```

See the complete [exercise index](index.md) for portable, Linux, Docker, Kubernetes,
and cloud tracks. The checked-in requirements pin the tested direct dependencies;
record resolved transitive versions with each run.

```bash
node tools/check-course.mjs
```

Linux-only fixtures, run separately in the VM: `sudo bash labs/run-linux.sh` builds
the Go tools and runs every netlab lab (including `sockets.json` for Module 20) and the
remaining shell fixtures.

An interrupted-run cleanup check and a new-learner capstone pilot remain release
gates even when static checks and automated portable tests pass. Estimated lesson
times are planning estimates until a pilot learner measures them.
