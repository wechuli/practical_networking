# Private Service Access Walkthrough

This optional exercise inspects an **owned existing sandbox**. It provisions
nothing. Use read-only permissions, record account/region/resource IDs, and do not
copy example identifiers into production changes. The core course is Linux/AWS;
Azure and GCP mappings below are comparisons, not identical command paths.

## Peered Private Service

Synthetic topology: application VPC 10.0.0.0/16, app subnet 10.0.20.0/24, service
VPC 10.50.0.0/16, HTTPS service 10.50.1.20. The application resolves
`api.internal.example` in a private DNS view. Two non-overlapping networks and
an active peer connection are necessary but not sufficient.

1. From the affected application namespace, record its resolver, queried name,
   answer, chosen address family, and source. Compare the authoritative/private
   view. VPN access does not automatically configure DNS forwarding.
2. Inspect the OS route to 10.50.1.20. Then inspect the effective cloud subnet
   route table, including implicit main-table association. App VPC local routes
   cover 10.0/16, not 10.50/16; the peering route must cover 10.50/16.
3. Check the service VPC's route back to the **observed** source (pod IP or SNAT
   address). Peering is not transitive routing through another peer/VPN by default.
   Overlapping CIDRs need a deliberate addressing/translation design.
4. Inspect SG, subnet NACL, host/container policy, and listener. SGs are stateful;
   NACL return rules must cover client ephemeral destination ports, including
   SYN-ACK. Use a bounded TCP/TLS request, not RDS ping.
5. Compare the failing and healthy subnet's actual effective objects. Propose one
   route/DNS/policy correction, obtain change approval, then verify the original
   request and an unauthorized source. Keep rollback identifiers and evidence.

Read-only AWS templates:

```bash
aws ec2 describe-route-tables --filters "Name=association.subnet-id,Values=$SUBNET_ID"
aws ec2 describe-route-tables --filters "Name=vpc-id,Values=$VPC_ID" "Name=association.main,Values=true"
aws ec2 describe-vpc-peering-connections --vpc-peering-connection-ids "$PEERING_ID"
aws ec2 describe-security-groups --group-ids "$SERVICE_SG_ID"
aws ec2 describe-network-acls --filters "Name=association.subnet-id,Values=$SUBNET_ID"
```

Resolve the effective table by explicit association **or** the main-table fallback,
not by assuming an empty explicit query means no routing. API permission failure
is not a networking diagnosis. Record flow-log limitations and actual timestamps.

## Private Endpoints Are Different

For AWS interface endpoints/PrivateLink, the consumer connects to endpoint ENIs in
its own network, with private DNS, endpoint policies/SGs, and provider service
acceptance as applicable. It does not simply route to the provider's VPC CIDR as
in peering. Inspect the CNAME/A chain, selected endpoint ENI, endpoint state, SG,
and service policy. A public DNS answer can silently take a different path.

Azure Private Endpoint/private DNS zone links and GCP Private Service Connect have
their own DNS, approval, route, source, and policy semantics. In each case inspect
the effective NIC/VNet/VPC policy and actual endpoint, not a generic "VPN up"
signal. Use Azure virtual network flow logs for new setups; NSG flow logs are on
their published retirement path. Private connectivity alone does not prove TLS
authentication, application authorization, encryption, or multi-location redundancy.

## Long-Lived Service Connections

An L4 selection normally lasts for the connection; HTTP/2/gRPC requests on that
connection can keep using one backend. Observe backend identity and source IP
before/after readiness removal. Graceful draining should stop new selections and
allow existing work to finish under a deadline; HTTP/2 GOAWAY helps clients open a
replacement connection. Test stream completion and idempotent retry behavior,
not just a new curl command. Do not assume a LoadBalancer always traverses NodePort
or preserves source IP under every traffic policy.
