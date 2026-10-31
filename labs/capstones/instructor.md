# Instructor Setup and Solutions

Keep this guide and fault-injection commands separate from the learner's brief.
Use only the lab VM/test cluster. Record a healthy baseline, choose a fault, and
give the learner its symptom, endpoints, configuration location, and authorized
scope without revealing the launch variant. Self-running a guided script does
not count as independent assessment.

## Case 1

Run `sudo ./bin/netlab run ../netlab/capture.json` (from `labs/go`) and supply the
TLS and DNS captures from its evidence directory (`tls.pcap`, `dns.pcap`) with
endpoint/port information. Ask for an explanation first without keys, then with the
restricted `tls-keys.log`.
For HTTP/2, use the web lab (`labs/netlab/web.json`): `weblab get -keylog FILE` writes the client's TLS secrets, and its `h2-mux` checks decrypt the capture with tshark.
Grade actual protocol boundaries and timing, not fixed packet numbers. TLS 1.3
Certificate requires decryption; curl's ssl_verify_result is not the TLS version.

## Cases 2 and 4

The environment is [`labs/netlab/case.json`](../netlab/case.json): client (the application),
resolver 198.18.240.53 (unbound), pubresolver .54, auth .2 and pubauth .3 (BIND, zone `test`),
web .20/.21 (nginx). From `labs/go`:

```bash
sudo ./bin/netlab up ../netlab/case.json
alias lab='sudo ./bin/netlab exec ../netlab/case.json'
E=$(ls -dt /tmp/netlab-case-* | head -1)
lab client -- ./bin/caselab probe -config $E/app/config.json     # baseline: RESULT: OK; also primes the cache
```

Inject one fault (do not show the learner this list):

| Variant | Inject | Repair |
| --- | --- | --- |
| dns resolver | `lab client -- sh -c "printf 'nameserver 198.18.240.99\nsearch shop.test\noptions timeout:2 attempts:2\n' > /etc/resolv.conf"` | restore `nameserver 198.18.240.53` |
| dns search | same, with `search other.test` | restore `search shop.test` |
| dns view | same, with `nameserver 198.18.240.54` | restore `.53` |
| dns cache | after a baseline probe: in auth, `sed -i 's/^api.shop\t300\tA\t.*/api.shop\t300\tA\t198.18.240.21/' /etc/bind/test.db && rndc -s 127.0.0.1 -k /etc/bind/rndc.key reload test`; in `$E/web/site.conf` change `listen 198.18.240.20:443` to `.21`, then `lab web -- nginx -c $E/web/nginx.conf -s reload` | `lab resolver -- unbound-control flush api.shop.test` (or wait out the TTL) |
| tls chain | in `$E/web/site.conf` change `api-chain.pem` to `api.pem`; reload nginx | restore `api-chain.pem` |
| tls trust | `cp $E/pki/inspection-ca.pem $E/app/ca-bundle.pem` | `cp $E/pki/root.pem $E/app/ca-bundle.pem` |
| tls hostname | in `site.conf` use `other-chain.pem` and `other.key`; reload nginx | restore `api-chain.pem`, `api.key` |

What the application reports and what separates the causes (from the self-test run):

| Variant | Application error | Discriminating observation |
| --- | --- | --- |
| resolver | `lookup api on 198.18.240.99:53: … i/o timeout` after 8.0 s (2 names × 2 attempts × 2 s) | `dig @198.18.240.53 api.shop.test` answers |
| search | `no such host` in ~1 ms | `api.other.test` and `api.` are NXDOMAIN, `api.shop.test` NOERROR |
| view | resolves to `192.0.2.80`, then `network is unreachable` | the two resolvers, and the two authorities, give different answers |
| cache | resolves to `.20`, `connection refused` | authority says `.21` with TTL 300; the resolver says `.20` with a TTL counting down |
| chain | `x509: certificate signed by unknown authority` | `openssl s_client -showcerts` shows **1** certificate; with the lab root, `Verify return code: 21 (unable to verify the first certificate)` |
| trust | the same `unknown authority` | **2** certificates are sent, issued up to "Lab Root CA"; the app's bundle is "Corp TLS Inspection CA"; with that bundle, code `20 (unable to get local issuer certificate)` |
| hostname | `x509: certificate is valid for other.test, not api` | the served certificate's SAN is `DNS:other.test`; `-verify_hostname api` gives code `62 (hostname mismatch)` |

The learner may read and edit the application config, `/etc/resolv.conf` on client, and nginx's
`site.conf`; query any DNS server; and inspect certificates. They may not read `case.json`.
After a TLS repair, require that a wrong name (`https://www.shop.test/health` with the same
config) and an unrelated CA (`$E/pki/rogue-root.pem`) are still rejected. `netlab down` removes
the namespaces; the lab's teardown deletes the generated keys and certificates.

`sudo ./bin/netlab run ../netlab/case.json` runs every variant with its repair (the self-test).

## Case 3

Use the [perf lab](../netlab/perf.json) (`netlab up`, then `pathlab set` on the
router to change delay, rate, queue or loss) or the [sockets lab](../netlab/sockets.json) (a relay with and without backpressure, a slow UDP consumer)
to establish controlled evidence. Give resulting pcaps/JSON without the guided
diagnostic output. For a live variant, keep the lab up and change one router
rate/delay/queue/loss setting, client receive window, or PMTUD-feedback rule. Keep the endpoints' MTU unchanged for PMTUD.
Have the learner propose one change before rerunning. Compare full transfers and
equal intervals, not a vote of retransmission versus zero-window counts. Never
change host-wide rmem or conntrack limits for this exercise.

## Case 5

First run policy mode of [the Kubernetes runner](../kubernetes/lab.py) to prove CNI
enforcement. In a separate unique namespace apply
[resources.yaml](../kubernetes/resources.yaml) and [policy.yaml](../kubernetes/policy.yaml),
wait for readiness, and verify permitted/denied clients. Save the baseline objects.
Choose one change while preserving unrelated access policy:

- Service selector: change `spec.selector.app` to a nonmatching label.
- Service port: change `spec.ports[0].targetPort` to 81; listener remains 80.
- DNS: recreate the allowed Pod with a search list omitting its namespace, or
  dnsPolicy None with an unused lab-owned resolver address.
- Policy: change only the allowed peer selector to another label.
- Cross-node: use two distinct test nodes and verified placement, then block only
  the documented overlay port on the disposable node path. Skip this variant on
  a single-node cluster; do not modify a production node.

The reference fix restores only the changed field. Require EndpointSlice/readiness,
DNS, or policy evidence and original-client success with unauthorized-client denial.
Deleting all policies fails. Remove only the owned namespace; restore node-path
changes before ending a cross-node variant.

## Case 6

Give the flow/metric dataset first. Then compare the learner's reading with the answer key,
`./bin/caselab case6 -flows ../observability/flows.csv -metrics ../observability/metrics.json`,
which also fails if the dataset is edited so that it no longer supports this reading:

- The REJECT is for 198.51.100.90, not the failing 203.0.113.80:443 tuple.
- The three-packet ACCEPT flows (180 bytes) are consistent with SYN retries that got no reply. ACCEPT is not delivery.
- ErrorPortAllocation, together with demand on that one destination (55,000 connections through one NAT address), supports SNAT pressure. ActiveConnectionCount (61,000, all destinations) alone does not.
- SKIPDATA marks an interval with lost records.
- The request timeout rate is 3/20, not a share of flow records.

Run the [containers lab](../netlab/containers.json) (`netlab run labs/netlab/containers.json`) for actual
translated tuples: four held flows fill four SNAT ports, another times out while
`conntrack -S` counts `insert_failed`/`drop`, and widening that lab range repairs a new flow. Compare the [sockets lab](../netlab/sockets.json) for local EADDRNOTAVAIL.
A trace from [the observability lab](../netlab/observe.json) is a separate observation, not
fabricated proof that its unrelated HTTP 503 was caused by NAT.

## Pilot Gate

Record a new learner's duration, hints, score, and cleanup for every case. Execute
interrupted-run checks on the target Ubuntu and Kubernetes profiles. Static checks
cannot replace these gates. Preserve sanitized artifacts, remove keys, and record
which variants actually ran.
