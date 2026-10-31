# Switching Exercises

The VLAN and STP labs run with [`netlab`](go/cmd/netlab) (Module 3):

```bash
cd labs/go && go build -o bin/ ./cmd/netlab
sudo ./bin/netlab run ../netlab/vlan.json
sudo ./bin/netlab run ../netlab/stp.json
```

The WireGuard, IPsec, VXLAN, SSH and OpenVPN exercises are in Module 19's
[`tunnels` lab](netlab/tunnels.json); VXLAN as a Kubernetes overlay is in Module 17's
[`k8s` lab](netlab/k8s.json).

## VLAN and STP

[The VLAN lab](netlab/vlan.json) connects a tagged VLAN-10 endpoint to an untagged
access port with PVID 10. A third access port uses PVID 20. The same IPv4 subnet
configured across those VLANs still cannot communicate without a routed path;
inspect `bridge vlan show` and the VLAN-scoped forwarding database.

[The STP lab](netlab/stp.json) enables STP **before** attaching two redundant links
between two isolated software bridges. It waits for the converged state — five
ports forwarding and one blocking — proves hosts on both switches can talk, then
breaks the forwarding link and measures failover. With the lab's 4 s forward delay
both take about 8–10 s; the default 15 s makes them about 30 s.
Never build this loop on a management network. Production switches add features
such as RSTP, edge-port handling, BPDU guard, and storm control; learn the selected
vendor's behavior before changing those settings.

For physical-link diagnosis, collect `ip -s link`, `ethtool INTERFACE`, and
`ethtool -S INTERFACE`; compare error/drop counter deltas under the same workload.
Driver counters and negotiated duplex/speed are not interchangeable with packet
loss at a remote destination. Virtual-link labs cannot reproduce defective optics.
