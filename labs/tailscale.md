# Tailscale Minicourse: Build and Troubleshoot a Private Network

> **Audience:** Support engineers, developers, and platform engineers
> **Placement:** Optional companion after [Module 19](../modules/module-19-vpns-tunnels-and-overlay-networks.md)
> **Prerequisites:** IP addressing, routing, DNS, host firewalls, and the [Module 19 tunnels lab](netlab/tunnels.json) (WireGuard)
> **Estimated time:** 3 hours 40 minutes, plus account and VM setup; unvalidated until a learner pilot
> **Profile:** Guided multi-device lab with an external Tailscale account; not part of the local-only regression suite

## Outcomes and Route Through the Course

You will connect two devices, explain their actual packet path, restrict access to
a service, reach a device without Tailscale through a subnet router, and distinguish
that from using an exit node. The final assessment is a broken-service diagnosis,
not an installation checklist.

| Lesson | Time | Evidence of understanding |
| --- | --- | --- |
| [1. From WireGuard to a Tailnet](#lesson-1-from-wireguard-to-a-tailnet) | 35 min | Successful HTTP request and a control-plane/data-plane diagram |
| [2. How Peers Connect](#lesson-2-how-peers-connect) | 30 min | Connection type, network checks, and inner/outer observations |
| [3. Names and Access Policy](#lesson-3-names-and-access-policy) | 45 min | DNS comparison and allowed/denied/recovered TCP connections |
| [4. Subnet Routers and Exit Nodes](#lesson-4-subnet-routers-and-exit-nodes) | 70 min | Route approval, policy, forwarding, and egress evidence |
| [5. Troubleshooting Capstone](#lesson-5-troubleshooting-capstone) | 40 min | An evidence-backed diagnosis, minimal repair, and cleanup |

Follow the [lab contract](README.md). Commands below are **guided configuration
templates and inspection steps**, not an unattended fixture. Run only the block
for the named device and lesson. Stop when an expected result is missing; do not
continue through failed setup. All addresses, names, and timings must be verified
against your own environment. No illustrative result is a measured run.

## Prepare an Isolated Lab

Use a new test tailnet that contains only your lab devices. You need permission to
edit its policy, assign tags, approve devices/routes, and remove them afterward.
Check the current [plans and limits](https://tailscale.com/pricing) before starting;
this course does not promise a particular free-tier entitlement. Do not replace
an employer's tailnet policy or enroll a production server for these exercises.

| Role | Environment | Purpose |
| --- | --- | --- |
| A: `ts-client` | Disposable Ubuntu 24.04 VM | Initiates requests; later consumes subnet routes and an exit node |
| B: `ts-server` | Disposable Ubuntu 24.04 VM | Hosts HTTP services; later becomes the subnet router and exit node |
| C: `ts-private` | Network namespace created inside B in Lesson 4 | HTTP endpoint with no Tailscale client |

Two VMs on one computer work for the core exercises. Separate owned internet
connections make NAT traversal and exit-node comparisons more informative, but
neither a direct path nor a DERP-only path is guaranteed. A and B need outbound
internet access. C will use an isolated veth link, not your home or office LAN.

Use hypervisor consoles or another management path independent of Tailscale.
Keep them open: policy changes, exit-node selection, and logout can terminate
remote sessions. Do not expose application ports on a public router. Do not flush
firewalls, enable broad forwarding exceptions, or disable security software to
make a test pass. Review any firewall change as a separate, narrowly scoped step.

Install the Linux tools from the lab contract, including `curl`, `jq`, `tcpdump`,
`iproute2`, and Python 3. Use the official
[Linux installation instructions](https://tailscale.com/download/linux) for the
current stable Tailscale package on both VMs. Inspect installation instructions
before executing privileged commands; installation is not performed by this guide.
Do not clone a VM after enrollment, which would also copy its Tailscale state.

On each VM, record these read-only checks before changing networking:

```bash
uname -a
tailscale version
tailscale set --help
tailscale ping --help
python3 --version
curl --version
ip -brief address
ip rule show
ip route show table all
sysctl net.ipv4.ip_forward net.ipv6.conf.all.forwarding
sudo nft list ruleset
```

Keep a private lab notebook with timestamps, versions, original settings, policy
snapshots, and results. Use restricted permissions for saved logs and captures.
Never commit auth keys, login URLs, daemon state, private keys, real user emails,
or unredacted packet captures. Browser login avoids placing an auth key in shell
history. No paid logging service is required.

**macOS note:** A Mac can be an optional client for Lessons 1-3 using the
[documented CLI integration](https://tailscale.com/kb/1080/cli). Linux interface,
namespace, firewall, and resolver commands do not apply to macOS. Use the two
Ubuntu VMs for the complete assessed path; Docker Desktop is not a substitute for
the Linux host topology used here.

## Lesson 1: From WireGuard to a Tailnet

### Mental Model

WireGuard encrypts packets between peers identified by public keys. A manually
configured tunnel still leaves you to distribute public keys, choose addresses,
track changing endpoints, and decide which peers may communicate. Tailscale adds
coordination, identity integration, policy distribution, DNS integration, and
connectivity negotiation around WireGuard-based encrypted networking.

A **tailnet** is your Tailscale network of enrolled devices and access policy.
Joining it authenticates a device; it does not mean every application on every
device should be reachable.

```text
                     Identity provider
                            |
                  Tailscale coordination
                    /                 \
             identity, keys, endpoints, policy
                  /                     \
       A: ts-client ===== encrypted ===== B: ts-server
                     direct or relayed

       Coordination is not the ordinary application packet path.
       A relay forwards encrypted packets; it is not their decrypting endpoint.
```

The control plane remains a trust and availability dependency. An established
data path may survive a temporary coordination outage, but new enrollment, policy
updates, and endpoint changes can be affected. Do not generalize that into
"Tailscale works indefinitely without its control plane."

Tailscale normally assigns IPv4 addresses from `100.64.0.0/10`, shared address
space rather than RFC 1918 space, and also supplies IPv6 addresses. Use addresses
reported by the client, not invented `100.x` values. Existing VPNs or provider
networks using the same range can create conflicts. A tailnet is a routed Layer-3
overlay, not a VXLAN broadcast domain.

### Enroll and Identify the Peers

On A, authenticate in the browser to your **test** tailnet:

```bash
sudo tailscale up --hostname=ts-client
tailscale status
tailscale ip -4
```

On B:

```bash
sudo tailscale up --hostname=ts-server
tailscale status
tailscale ip -4
```

Complete device approval if the tailnet requires it. In the admin console, confirm
both devices, their names, ownership, and key-expiry state. Record A's and B's
actual addresses. On A, set `SERVER_IP` to B's reported IPv4 address before using
commands that reference it; do not use B's LAN or public address instead.

### Establish a Narrow Starting Policy

Back up the test tailnet's original policy privately. In its Access controls
editor, replace that policy with this complete lab policy. This intentionally
removes the broad initial access policy. **Never paste it into a shared tailnet.**

```json
{
  "tagOwners": {
    "tag:lab-client": ["autogroup:admin"],
    "tag:lab-server": ["autogroup:admin"]
  },
  "grants": [
    {
      "src": ["tag:lab-client"],
      "dst": ["tag:lab-server"],
      "ip": ["tcp:8080"]
    }
  ],
  "tests": [
    {
      "src": "tag:lab-client",
      "proto": "tcp",
      "accept": ["tag:lab-server:8080"],
      "deny": ["tag:lab-server:8081", "tag:lab-server:22"]
    }
  ]
}
```

Require the editor's policy tests to pass. In the Machines page, assign only
`tag:lab-client` to A and only `tag:lab-server` to B. Inspect the resulting identities.
Defining `tagOwners` does not assign tags or grant network access by itself.
Tagged devices use a service identity instead of their enrolling user's identity;
these disposable VMs deliberately model a workload client and a server. Human
laptops would usually remain user-owned and receive access through users/groups.
Tagging can also change key-expiry defaults; record the displayed setting.

### Serve One Harmless Resource

On B, in a dedicated terminal, create a new directory containing only lab content
and run a foreground server bound to B's Tailscale IPv4 address:

```bash
SERVER_IP=$(tailscale ip -4)
umask 077
LAB_DIR=$(mktemp -d /tmp/ts-course.XXXXXX)
printf 'tailscale-lab-ok\n' > "$LAB_DIR/index.html"
printf 'Lab directory: %s\n' "$LAB_DIR"
python3 -m http.server 8080 --bind "$SERVER_IP" --directory "$LAB_DIR"
```

Keep this terminal open. Record `LAB_DIR` for later terminals; shell variables are
not shared between terminal windows. Never serve your repository or home directory.
The unprivileged HTTP server is a lab tool, not a production deployment pattern.

On A, after setting `SERVER_IP` to B's address:

```bash
body=$(curl --noproxy '*' --fail --silent --show-error --connect-timeout 3 --max-time 8 "http://${SERVER_IP}:8080/") &&
  [[ "$body" == 'tailscale-lab-ok' ]] && printf 'PASS: expected response\n'
```

The expected body must match; a ping or a login success is not enough. If the
request fails, inspect B's `ss -lntp`, tags, policy, and host firewall before changing
anything. Do not broaden the grant to all ports. `--noproxy '*'` ensures these
checks test the network path rather than an environment-configured HTTP proxy.

**Checkpoint:** Draw the enrollment path and the HTTP data path separately.
Explain why HTTP is encrypted between these two Tailscale endpoints even without
HTTPS, and why that does not replace application authentication or justify dropping
TLS in production. Encryption ends at the Tailscale endpoint, not inside the app.

## Lesson 2: How Peers Connect

### Direct, DERP, and Peer Relay Paths

NAT maps private source addresses and ports to external ones. Tailscale discovers
candidate endpoints and tries UDP connectivity between peers. NAT traversal is a
negotiation, not a guarantee that every firewall can be crossed.

DERP provides a relay path when peers cannot communicate directly, and also helps
with connection establishment. Connections can start relayed and then become
direct. Current Tailscale versions can also use a configured **peer relay** in the
tailnet. This course does not deploy one; recognize `peer-relay` if you encounter it.
All three paths retain WireGuard end-to-end encryption between A and B. A relay
can observe traffic metadata, but cannot decrypt the inner application payload.

On A, run the HTTP request from Lesson 1, then inspect:

```bash
tailscale ping --c=5 --timeout=3s --until-direct=false "$SERVER_IP"
tailscale status
tailscale netcheck
ip -details link show tailscale0
```

Record the observed connection type and endpoint or relay identifier, UDP
availability, and measured delays. `netcheck` describes network conditions; it
does not prove which path one peer's current request used. An idle DERP connection
or relay metadata alone does not establish that HTTP traffic was relayed. Use
active-peer status and observations taken during the actual workload.

`tailscale ping` is a Tailscale diagnostic, not a TCP port check. Its default
discovery probes are distinct from ordinary ICMP and application traffic. A
successful response does not establish that the access policy allows TCP 8080,
that the OS accepts it, or that a process is listening there.

### Compare Inner and Outer Observations

On B, identify the underlay interface with `ip -brief address` and routing
inspection. Set `UNDERLAY_IF` to that interface's actual name. In separate B
terminals, start these bounded captures, then make three fresh HTTP requests from A:

```bash
sudo timeout 15 tcpdump -ni tailscale0 -c 20 'tcp port 8080'
```

```bash
sudo timeout 15 tcpdump -ni "$UNDERLAY_IF" -c 40 'udp or tcp port 443'
```

The broad outer filter is suitable only for these otherwise idle lab VMs. Correlate
addresses and timestamps with peer status; unrelated HTTPS is not evidence of DERP.
A capture reaching its time limit may return status 124, which is not itself a
connectivity failure. No packets before the limit means insufficient evidence,
not automatically a broken tunnel. Interface names and capture visibility differ
on other platforms and in userspace-networking mode.

On `tailscale0`, look for A/B overlay addresses and the TCP handshake. On the
underlay, look for direct encrypted UDP or traffic to the observed relay. Do not
assume the raw WireGuard lab's UDP port 51820 is Tailscale's port. Do not assume
the raw tunnel's MTU is the Tailscale interface MTU: record the interface value.

**Optional comparison:** Move A to another owned network with console access,
repeat the same bounded workload, and compare the path. Do not deliberately block
all UDP on a workstation or weaken a router firewall. If you cannot observe a
second connection type, record "not observed" and explain the expected difference
using the [connection-types reference](https://tailscale.com/kb/1257/connection-types).
Never submit an expected relay result as a measured one.

**Checkpoint:** Does a relay necessarily indicate a broken connection? No. It may
explain a performance change, but prove that with same-workload measurements.
Neither encryption nor an empty capture alone proves the application is healthy.

## Lesson 3: Names and Access Policy

### Separate Name Resolution from Reachability

Enable MagicDNS in the test tailnet's DNS settings if necessary. On A, accept the
tailnet DNS configuration and copy B's full DNS name from the Machines page into
`SERVER_NAME`. Do not invent the tailnet's `.ts.net` suffix.

```bash
sudo tailscale set --accept-dns=true
getent ahostsv4 "$SERVER_NAME"
resolvectl status
curl --noproxy '*' --fail --show-error --connect-timeout 3 --max-time 8 "http://${SERVER_NAME}:8080/"
```

Compare the resolved IPv4 address with B's address and repeat the numeric-address
request. `getent` uses Linux's system lookup path; on a system without
`systemd-resolved`, inspect that system's resolver instead of requiring `resolvectl`.
For this IPv4-bound service, use `curl -4` if an IPv6 selection obscures the exercise.
On macOS, `host`, `dig`, and `nslookup` can bypass the resolver behavior used by
applications; their failure alone is not proof that MagicDNS is broken.

MagicDNS names enrolled devices. It does not automatically publish names for all
hosts behind a subnet router. Those may need internal DNS and split-DNS configuration.
DNS supplies an address, not authorization to connect to it.

### Prove a Policy Denial Against a Working Listener

On B, open a second terminal, set `SERVER_IP` and `LAB_DIR` to the values from
Lesson 1, and run a second foreground listener:

```bash
python3 -m http.server 8081 --bind "$SERVER_IP" --directory "$LAB_DIR"
```

From another B terminal, verify both listeners with `ss -lntp` and a local request
to B's own Tailscale address on port 8081. This checks the listener, not remote
access policy. On A, the same port must be unreachable under the starting policy.

To establish an unambiguous baseline/failure/recovery sequence:

1. In the test policy, temporarily change the grant's `ip` list to
   `["tcp:8080", "tcp:8081"]`. Move `tag:lab-server:8081` from the test's `deny`
   list to its `accept` list, leaving port 22 in `deny`. Save only if tests pass.
2. On A, request port 8081 with the same bounded `curl` options. Require the
   `tailscale-lab-ok` body. If it does not succeed, resolve the listener/firewall
   problem before calling the next result a policy denial.
3. Restore the exact Lesson 1 policy and wait for it to propagate. Use a new
   `curl` process for every attempt. Require 8080 to keep working while 8081 fails.
4. Temporarily reapply the two-port policy and prove 8081 recovers without changing
   the application. Finally restore the one-port policy and verify denial again.

Use this check for the denial step on A:

```bash
if curl --noproxy '*' --silent --show-error --connect-timeout 3 --max-time 8 "http://${SERVER_IP}:8081/"; then
  printf 'FAIL: denied port returned an HTTP response\n'
else
  printf 'Request failed; correlate its error with the working baseline and policy change\n'
fi
```

A timeout alone cannot identify the cause. Preserve the curl error, the policy
test result, the still-working 8080 response, B's listening socket, and the 8081
recovery. Do not treat an HTTP error response as a network-policy denial: receiving
HTTP proves an application connection was established.

### Why the Policy Works

Grants permit matching traffic and are additive. Legacy ACLs and grants can
coexist; leaving a broad allow rule in either can invalidate your intended
restriction. There is no ordered "deny overrides allow" rule in this example.
The `deny` entries are **test assertions**, not firewall rules. A failing assertion
prevents saving the policy; it does not silently install a deny.

A permitted client connection also permits its response traffic; you need not
grant B permission to initiate a separate connection to A. Network grants do not
configure HTTP authentication. Tailscale SSH is a separate optional feature with
its own SSH policy in addition to network access; this lab does not enable it.

**Checkpoint:** Explain why a correct DNS answer, a successful diagnostic ping,
and an allowed TCP connection are three different claims. Predict what happens
if A is assigned only `tag:lab-server`: it no longer matches the client source
selector, even if the same person originally enrolled both VMs.

## Lesson 4: Subnet Routers and Exit Nodes

### Two Different Routing Jobs

```text
Private service:
A -- WireGuard --> B -- isolated veth --> C:192.0.2.2:8080
                    subnet router          no Tailscale client

Internet egress:
A -- WireGuard --> B -- ordinary internet route --> public HTTPS service
                    exit node
```

Subnet routing makes a specific destination prefix reachable. An exit node
provides internet egress using default-route behavior, subject to more-specific
routes and platform exceptions. Advertising either is not sufficient: approval,
client configuration, access policy, host forwarding, and a working destination
are separate requirements.

WireGuard encryption ends at B in both drawings. B-to-C HTTP is plaintext on the
isolated link. For internet HTTPS, application TLS continues beyond B. An exit
node is neither an anonymity guarantee nor a replacement for TLS.

### Build C on B Only

Check B's namespaces, interfaces, and all routing tables. Continue only if
`ts-private`, `ts-lab-host`, `ts-lab-peer`, and `192.0.2.0/24` are unused. Check A
for a conflicting route too. The documentation prefix is used solely on an
isolated link; never advertise it into a real organization's network. If there
is a conflict, choose another unused lab prefix and update every dependent example.

On B, from a console-backed terminal:

```bash
ip netns list
ip -brief link
ip route show table all
sudo ip netns add ts-private
sudo ip link add ts-lab-host type veth peer name ts-lab-peer
sudo ip link set ts-lab-peer netns ts-private
sudo ip address add 192.0.2.1/24 dev ts-lab-host
sudo ip link set ts-lab-host up
sudo ip -n ts-private address add 192.0.2.2/24 dev ts-lab-peer
sudo ip -n ts-private link set lo up
sudo ip -n ts-private link set ts-lab-peer up
```

C intentionally has no default route. With Tailscale's default subnet SNAT,
requests arriving from A appear to C as B's `192.0.2.1` address, and C can return
them over its connected link. Disabling SNAT would require a return route for A's
original address; do not toggle it during this exercise.

On B, using the same recorded `LAB_DIR`, run C's server in a dedicated terminal:

```bash
sudo ip netns exec ts-private python3 -m http.server 8080 --bind 192.0.2.2 --directory "$LAB_DIR"
```

This process runs as root only inside the disposable lab VM's namespace workflow;
it is not a recommended production service account. From B, require a successful
bounded request to `http://192.0.2.2:8080/` before continuing. From A, record that
the same request is not yet reachable. A failure here is the expected pre-routing
baseline, not yet evidence about Tailscale policy.

### Enable and Authorize the Subnet Path

On B, retain these original values in the same shell for cleanup and also record
them in your notebook. Inspect the VM's forwarding firewall first: it should
deny unintended forwarding while allowing the Tailscale-managed path. Leave
Tailscale's normal netfilter integration enabled; never flush its chains.

```bash
ORIGINAL_V4_FORWARD=$(sysctl -n net.ipv4.ip_forward)
ORIGINAL_V6_FORWARD=$(sysctl -n net.ipv6.conf.all.forwarding)
sudo sysctl -w net.ipv4.ip_forward=1
sudo tailscale set --advertise-routes=192.0.2.0/24
```

In the admin console, approve **only** B's `192.0.2.0/24` advertisement. Leave
automatic route approval unconfigured so that this step remains visible.

Add this object to the existing `grants` array, retaining the original 8080 grant:

```json
{
  "src": ["tag:lab-client"],
  "dst": ["192.0.2.2/32"],
  "ip": ["tcp:8080"]
}
```

Add `192.0.2.2:8080` to the existing test's `accept` list and `192.0.2.2:8081`
to its `deny` list. Require passing policy tests. The route exposes a prefix to
routing, but this grant authorizes just one destination and port. Access to
`tag:lab-server` alone does not authorize destinations behind B.

On Linux client A:

```bash
sudo tailscale set --accept-routes=true
ip rule show
ip route show table all
ip route get 192.0.2.2
body=$(curl --noproxy '*' --fail --silent --show-error --connect-timeout 3 --max-time 8 http://192.0.2.2:8080/) &&
  [[ "$body" == 'tailscale-lab-ok' ]] && printf 'PASS: subnet service\n'
```

Linux does not accept advertised subnet routes by default. Other clients often
do. Inspect policy routing and all tables rather than assuming the main table
contains the route. A route lookup is useful evidence, but the HTTP result proves
the workload actually worked.

On B, capture while A repeats the request:

```bash
sudo timeout 15 tcpdump -ni ts-lab-host -c 20 'host 192.0.2.2 and tcp port 8080'
```

Require the observed source address on this link to agree with the SNAT explanation.
Compare it with the original client source on `tailscale0`. If they disagree,
inspect actual SNAT and forwarding settings rather than rewriting the observation.

**Fault and recovery:** On A, set `--accept-routes=false`, repeat the numeric C
request and record failure, then set `--accept-routes=true` and require recovery.
Keep the B:8080 request working throughout. This distinguishes route consumption
from DNS and application failure. Recheck the lack of another route to C if the
failure does not occur.

### Contrast Internet Egress

Exit-node selection changes A's internet path and can affect DNS and LAN access.
Perform this only on the disposable A console, never through its sole management
connection. Record the original preference values. A must not already be using
another exit node.

Choose an approved HTTPS endpoint that reports the request's public source IPv4
address and set `EGRESS_CHECK_URL` to its URL on A and B. This intentionally sends
one request per check to that operator; do not attach credentials or use a service
whose terms disallow it. Run on both VMs before changing the path:

```bash
curl -4 --noproxy '*' --fail --show-error --connect-timeout 3 --max-time 8 "$EGRESS_CHECK_URL"
```

On B, using the forwarding baseline already recorded above:

```bash
sudo sysctl -w net.ipv6.conf.all.forwarding=1
sudo tailscale set --advertise-exit-node=true
```

B should remain connected from Lesson 1; reconnect with `sudo tailscale up` if
it is disconnected. Approve B as an exit node in its admin-console route settings.
Retain subnet SNAT. Add the following object to the lab policy's `grants` array:

```json
{
  "src": ["tag:lab-client"],
  "dst": ["autogroup:internet"],
  "ip": ["*"]
}
```

This deliberately authorizes A's internet egress through available exit nodes;
it is broader than the private-service grants. It does not mean "allow access
only to B's own ports." Keep it temporary and keep all existing tests passing.

On A, with `SERVER_IP` still set to B's Tailscale IPv4 address:

```bash
sudo tailscale set --exit-node="$SERVER_IP" --exit-node-allow-lan-access=false
tailscale status
ip rule show
ip route show table all
curl -4 --noproxy '*' --fail --show-error --connect-timeout 3 --max-time 8 "$EGRESS_CHECK_URL"
```

The reported IPv4 egress should now match B's egress. If A and B already share an
upstream NAT, an unchanged public address cannot prove exit-node use. Correlate
the selected exit node, route changes, and B's bounded underlay capture during
A's request, or repeat on separate owned networks. This IPv4 test does not prove
IPv6 egress; test an IPv6-capable endpoint separately when both networks support it.

Do not assume A's local LAN remains accessible: local-network access is disabled
by the selected flag. Verify that the explicitly routed private service still
works, then restore ordinary egress on A and require the HTTPS request to succeed:

```bash
sudo tailscale set --exit-node=
```

On B, stop advertising exit-node service and revoke its exit-node approval in
the console. Remove only the internet grant, retaining the two private-service
grants for the capstone:

```bash
sudo tailscale set --advertise-exit-node=false
```

**Checkpoint:** List the independent gates between A and C. Explain why approving
a route does not authorize traffic, why SNAT simplifies C's return path, and why
an exit node is unnecessary merely to reach C.

## Lesson 5: Troubleshooting Capstone

### Scenario and Fault Selection

"The device appears online, but the private HTTP service is unreachable."
First require successful A-to-B and A-to-C requests under the final Lesson 4
policy. Have a partner choose one fault below without telling the learner which
one. For a solo run, select a fault, record the injection separately, and justify
each diagnostic step rather than jumping directly to the reversal.

| Fault to inject in this lab only | Discriminating observation | Minimal recovery |
| --- | --- | --- |
| On A, `sudo tailscale set --accept-routes=false` | B:8080 works, numeric C:8080 fails, C route is no longer consumed | Restore `--accept-routes=true` |
| Stop B's 8080 server and restart the same command with `--bind 127.0.0.1` | Local loopback HTTP works, `ss` shows no tailnet-address listener | Stop it and rebind to B's Tailscale address |
| Assign A only `tag:lab-server` in the console | Listener and routes are healthy, but A no longer matches `tag:lab-client` | Restore only A's original client tag |
| On A, `sudo tailscale set --accept-dns=false` | Name request may fail while numeric B:8080 still works | Restore `--accept-dns=true` |

For the DNS variant, cached or independently configured resolution can mask the
fault. Inspect the system resolver and actual lookup result. If it still resolves,
mark the injection inconclusive and choose another fault; do not claim a failure
you did not observe. Do not delete resolver files to force the scenario.

### Diagnostic Order

1. Write the exact source identity, destination name/address, protocol, and port.
   Repeat a bounded request and preserve its error, not just "does not work."
2. Compare name and numeric requests. A DNS failure precedes TCP; an HTTP response
   means TCP worked even if the application rejected the request.
3. Inspect Tailscale enrollment/key state and active path. A relay may explain
   latency; it does not automatically explain a denied port.
4. Check A's actual tags against the policy, including additive broad rules.
   Passing policy tests describe configured permissions, not live host health.
5. For C, verify advertisement, approval, client route acceptance, forwarding,
   SNAT/return path, and the destination grant separately.
6. On B or C, inspect listening addresses and host firewall state. Capture at the
   boundary where competing hypotheses predict different packets. A missing SYN
   at a host capture is not alone proof of a Tailscale policy drop.
7. Make one minimal repair, repeat the original workload, and retest 8081 denial.

Use [Module 22](../modules/module-22-observability-and-troubleshooting.md) for the
broader hypothesis-and-evidence approach. Do not use "disable all firewalls" or
"allow all tailnet traffic" as the repair.

### Submission and Rubric

Submit a redacted notebook with these sections; the existing
[capstone submission template](capstones/submission.md) can supply the structure.

| Criterion | Points | Required evidence |
| --- | --- | --- |
| Topology and identity | 2 | A/B/C, addresses, tags, and the encryption termination point |
| Working baseline and failing flow | 2 | Timestamped bounded HTTP results before and after injection |
| Diagnosis | 2 | Relevant resolver/route/policy/socket/capture evidence and one rejected alternative |
| Repair and regression | 2 | Minimal change, original workload recovery, continued 8081 denial |
| Cleanup | 2 | Removed advertisements/approvals/devices, stopped listeners, restored settings |

Pass at 8/10 or above, with working recovery, negative-access verification, and
cleanup mandatory regardless of score. Explain control plane versus data plane,
direct versus relay, and subnet router versus exit node in your own words.
Do not award a live-check point for a static policy test or a predicted result.

## Cleanup and Interrupted Runs

Use the independent VM consoles and undo only resources owned by this lab.
These commands assume the fresh-device baseline: no exit node, no advertised
routes, and Linux route acceptance disabled. Restore recorded values instead if
you deliberately chose a different baseline.

On A, clear exit-node use before withdrawing B:

```bash
sudo tailscale set --exit-node= --exit-node-allow-lan-access=false --accept-routes=false --accept-dns=true
```

On B:

```bash
sudo tailscale set --advertise-routes= --advertise-exit-node=false
```

Revoke the lab subnet and exit-node approvals in the admin console. Stop all
three foreground HTTP servers with Ctrl+C in their respective terminals. Verify
the B listeners with `ss -lntp`; inspect C's remaining processes before deletion:

```bash
sudo ip netns pids ts-private
```

Require no remaining C processes. If a terminal was lost, inspect each returned
PID with `ps -fp PID` and stop only the confirmed lab server, then check again.
Deleting a namespace name without stopping its processes does not fully clean it up.
Remove these names only if created by this run:

```bash
sudo ip link delete ts-lab-host
sudo ip netns delete ts-private
sudo sysctl -w "net.ipv4.ip_forward=${ORIGINAL_V4_FORWARD:?use the recorded baseline}"
sudo sysctl -w "net.ipv6.conf.all.forwarding=${ORIGINAL_V6_FORWARD:?use the recorded baseline}"
```

If the shell was lost, recover the forwarding values from your notebook; do not
guess or replace an empty value with zero. Inspect actual resources after an
interruption and skip deletion of already-absent names. Remove only the recorded
temporary content directory after checking its path and contents.

On each lab VM, log out, then remove the corresponding device in the admin console:

```bash
sudo tailscale logout
```

Logout is not the same as deleting a persistent device record. Remove unused lab
policy tags/grants or restore the backed-up test policy after removing the devices.
Revoke any enrollment keys you independently created. Restore original DNS
preferences if they differed, then discard the disposable VMs. Compare routes,
forwarding settings, namespaces, and listener state with the initial baseline
before disposal; preserve only redacted assessment evidence.

## Review Questions

1. Why can diagnostic ping succeed while TCP 8081 is denied?
2. Why does adding a narrow grant not cancel an existing broad ACL?
3. Why can B reach C while A cannot, even though A can reach B?
4. Which source address should C observe with default subnet SNAT, and why?
5. Where does WireGuard encryption end for a non-Tailscale subnet destination?
6. What can an unchanged public egress IP prove when A and B share a NAT?

**Answer guide:** Different probes test different paths and permissions; grants
are additive; B's local route does not establish A's approval/acceptance/policy
or B's forwarding; C sees B's link address because of SNAT; encryption ends at
the subnet router; a shared public address alone cannot establish exit-node use.

## References and Validation Status

Official documentation consulted on 2026-09-19. Client behavior and admin-console
labels can change; record the installed versions and consult `--help` for them.

- [Tailscale CLI](https://tailscale.com/kb/1080/cli)
- [Connection types and relay behavior](https://tailscale.com/kb/1257/connection-types)
- [MagicDNS](https://tailscale.com/kb/1081/magicdns)
- [Grants](https://tailscale.com/kb/1324/grants)
- [Policy syntax and policy tests](https://tailscale.com/kb/1337/acl-syntax)
- [Subnet routers](https://tailscale.com/kb/1019/subnets)
- [Exit nodes](https://tailscale.com/kb/1103/exit-nodes)

Static course validation checks document links and code-fence structure; it does
not authenticate to Tailscale or execute this multi-device lab. Policy acceptance,
actual connection paths, positive/negative HTTP checks, interruption cleanup, and
a new-learner capstone pilot remain target-environment gates. No live tailnet run
is claimed by this document.

Kubernetes integration, Headscale, peer-relay deployment, Tailscale SSH, Serve,
and public exposure through Funnel are possible advanced follow-ons, not required
for this minicourse. Return to [Module 20](../modules/module-20-sockets-and-application-level-networking.md)
for the main course's application-networking track.