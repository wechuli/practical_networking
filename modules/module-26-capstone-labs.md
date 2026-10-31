# Module 26: Capstone Labs

> **Audience:** Support Engineers, Developers, Cloud Engineers
> **Prerequisites:** Modules 0–25 and the [lab contract](../labs/README.md)
> **Estimated time:** 6-10 hours; confirm with a learner pilot

## Assessment Format

These six challenges assess diagnosis, not memorization of commands. The instructor
prepares a healthy baseline and selects a fault using the separate
[instructor guide](../labs/capstones/instructor.md). Learners receive the symptom,
permitted endpoints, evidence directory, and access boundaries, not the injection
commands or solution. For self-study, ask a peer to choose an unfamiliar variant.
Running a guided demonstration with its answer visible is preparation, not an
independent mastery assessment.

Incidents and supplied datasets are synthetic unless explicitly sourced. Do not
inject faults on a workstation's management network, production cluster, or cloud
account. A percentage in a case brief describes that dataset, not a guaranteed
local failure rate.

## Required Submission

Use the [incident template](../labs/capstones/submission.md). Include:

1. The failing operation and source/destination/protocol/port context.
2. A healthy comparison and failure with UTC timestamps.
3. Relevant packets, configuration, counters, or spans and observation points.
4. A plausible alternative rejected by a discriminating test.
5. A minimal repair and original-workload before/after evidence.
6. An unauthorized or incorrect case still rejected for security controls.
7. Cleanup, restored settings, and remaining uncertainty.

Do not use fixed packet numbers, heuristic labels, or success echoes as proof.
Saving an output file does not establish it contains the required evidence.

## Capstone 1: Full Stack Trace

**Brief:** Explain a DNS lookup and HTTPS exchange, including success and a TLS
failure. The instructor supplies a controlled capture or endpoint generated with
the [capture lab](../labs/netlab/capture.json) (`netlab up`, then capture DNS, HTTPS with and without a trusted CA, and a key log).

**Tasks:** Identify the DNS question/answer, TCP tuple and sequence progression,
TLS version and authentication result, application protocol/response, and observed
close. Explain what can be established without TLS keys. Calculate phase durations
with the correct timestamp boundaries, overlap, and network contribution to TTFB.
For HTTP/2, identify concurrent streams on one connection.

**Pass conditions:** Resolver traffic is actually captured. TLS 1.3 encrypted
handshake analysis uses matching keys. HTTP/1.1 and HTTP/2 fields match negotiation.
Missing/reused phases are explained. `%{ssl_verify_result}` is not a TLS version.
A reused connection does not require fresh DNS/TCP/TLS per request. Protect and
remove keys after analysis; do not require a prescribed number of packets.

## Capstone 2: Name Resolution Incident

**Brief:** An application on the host `client` calls `https://api/health` and now fails, while other hosts reach the service. The environment is [`labs/netlab/case.json`](../labs/netlab/case.json), a small enterprise:

- the application's stub resolver, configured in `/etc/resolv.conf` with `search shop.test`;
- a caching resolver (unbound, 198.18.240.53) and a second resolver used for the public side (198.18.240.54);
- the internal and public authoritative servers (BIND, 198.18.240.2 and .3);
- the service itself (nginx, 198.18.240.20).

The application is [`caselab probe`](../labs/go/cmd/caselab). It logs each stage — resolve, connect, TLS, HTTP — and the error it got, the way an application log would. It gives no diagnosis.

The instructor injects one fault from the [instructor guide](../labs/capstones/instructor.md) and gives you:

- the symptom;
- access to `client`, plus read-only access to the resolver and the authority;
- the location of the application's configuration.

**Tasks:**

- Establish what the application actually uses: the resolver configuration as the application reads it, the names its search list produces, the server it asks.
- Compare the authority's answer with the answer the application gets.
- Distinguish NXDOMAIN and NODATA, SERVFAIL, timeout, and a correct answer to an unreachable address.
- Apply the minimal repair and repeat the application's own request.

**Pass conditions:**

- Short-name tests use the search list (`dig +search`, or the full names it produces), not a different name.
- A cached answer is shown in the resolver, with its TTL counting down, and compared against the authority (`dig +norec @authority`). It is not inferred from two unrelated lookups.
- "DNS works" is not concluded from a lookup made through a different resolver than the application's.
- The repair is the changed setting, not a hard-coded `/etc/hosts` entry or an IP address in the application.

## Capstone 3: Find the Bottleneck

**Brief:** A download is slower than its healthy baseline. Possible mechanisms
include rate limiting, RTT plus a small receive window, loss, an application slow
reader, and hidden path MTU. The instructor initially injects only one mechanism.

**Tasks:** Identify sender and receiver, measure goodput and RTT on the same path,
inspect effective windows and queues, and make one discriminating change. Compare
complete transfers over equal measurement intervals before and after repair.

**Pass conditions:** The download receiver is the client. Impairment direction is
correct. The rate is shaped or measured, not assumed. `io,stat` is volume, not RTT.
Zero-window and retransmission counts are not votes for a cause. PMTUD uses a
lower-MTU intermediate router while endpoints retain their MTU; feedback is first
observed, then selectively blocked. Fresh destination/PMTU state prevents a prior
test hiding the fault. All processes and owned namespace settings are cleaned up.

## Capstone 4: TLS Trust Incident

**Brief:** In the same environment, the application now fails at TLS after a change on either the server or the client. The instructor chooses the change.

**Tasks:**

- Inspect what the server sends (the full chain), the name the application verifies, the CA bundle it trusts, the validity dates, and the exact error.
- Note that the application reports the same error text for more than one cause. Find the observation that separates them.
- Distinguish SNI (what the client asks for) from hostname verification (what it accepts).
- Repair without disabling verification or trusting an individual leaf certificate.
- Retest the application, then show that a wrong name and an unrelated CA are still rejected.

**Pass conditions:**

- The served chain is observed (`openssl s_client -showcerts`, or `tlslab inspect`), not assumed from the files on disk.
- A missing intermediate is fixed on the server, by sending leaf + intermediate. It is not fixed by adding the intermediate, or the leaf, to the client's trust.
- The HTTP response completes after the repair; a handshake alone is not enough.
- `curl -k`, `InsecureSkipVerify`, or trusting the presented certificate fails the security criterion.

## Capstone 5: Kubernetes Connectivity Maze

**Brief:** A client cannot reach an API Service. The instructor selects a Service
selector, targetPort, client DNS, NetworkPolicy, or two-node path variant. Use only
the explicit test context and namespace names supplied with the case.

**Tasks:** Trace application DNS, Service selection, ready EndpointSlices, Pod
reachability, ingress/egress policy, and the relevant node path. Compare allowed
and denied clients. Identify whether only the VIP, a backend, DNS, or cross-node
traffic is affected.

**Pass conditions:** Policy enforcement is proven, readiness precedes testing,
and empty endpoints are distinguished from CNI failure. DNS failure is not
automatically CoreDNS failure. The repair preserves unauthorized-client denial.
Client-side curl's Connected-to IP is not treated as backend identity. Cross-node
variants require distinct nodes and verified placement, not a single-node cluster.

## Capstone 6: Outbound Timeout Incident

**Brief:** A synthetic cloud API sample has 3 timeouts in 20 attempts, scoped to one
source subnet. The instructor supplies [flow records](../labs/observability/flows.csv),
[metrics](../labs/observability/metrics.json), and a packet or trace evidence set
from the [containers lab](../labs/netlab/containers.json)'s SNAT section.
Determine which observations support which causes and identify missing evidence.

**Tasks:** Distinguish firewall rejection, local ephemeral-port exhaustion,
conntrack-table capacity, and SNAT translation-port allocation. Correlate the
destination tuple, time window, counters, and healthy comparison. Verify a scoped
mitigation on the instructor's controlled topology.

**Pass conditions:** REJECT is not NAT-exhaustion proof, ACCEPT is not delivery
proof, and SKIPDATA is acknowledged. The original destination remains the remote
service, not the NAT gateway. AWS's 55,000 figure is per NAT IPv4 address and unique
destination IP/port/protocol, not a total ActiveConnectionCount ceiling. No host
conntrack limit is lowered; the capacity demonstration uses bounded SNAT ports on
a real routed path.

## Running the Cases

All cases run on the course lab VM in namespaces, with no internet access and no change to the host's own network:

| Case | Environment | Instructor tools |
| --- | --- | --- |
| 1 | [`capture.json`](../labs/netlab/capture.json), [`web.json`](../labs/netlab/web.json) | captures and key logs from the lab's evidence directory |
| 2, 4 | [`case.json`](../labs/netlab/case.json) | one fault from the instructor guide, injected after `netlab up` |
| 3 | [`perf.json`](../labs/netlab/perf.json), [`sockets.json`](../labs/netlab/sockets.json) | one `pathlab set` or socket setting |
| 5 | a disposable test cluster | [Kubernetes profile](../labs/kubernetes/README.md) |
| 6 | the dataset, plus [`containers.json`](../labs/netlab/containers.json) | `caselab case6` (the answer key) |

`sudo ./bin/netlab run ../netlab/case.json` is the instructor's self-test: it injects every Case 2 and Case 4 fault, records the application's error and a discriminating observation, repairs it and re-runs the application. It passed 10 of 10 checks in three runs, in about 18 s. Because it contains the answers, learners attempt the cases **before** reading `case.json` or its evidence.

## Rubric

| Criterion               | Points | Evidence                                            |
| ----------------------- | ------ | --------------------------------------------------- |
| Scope and baseline      | 15     | Correct flow, context, healthy comparison           |
| Observations            | 25     | Relevant packet/configuration/counter evidence      |
| Discrimination          | 20     | A plausible alternative rejected by a targeted test |
| Minimal repair          | 20     | Original workload recovers; intended denial remains |
| Explanation             | 10     | Correct mechanism, no unsupported certainty         |
| Cleanup and limitations | 10     | Owned resources removed and limits stated           |

A suggested pass is 80/100 **and** all safety, trust, and unauthorized-access checks
pass. Deleting all security policy or using `curl -k` fails the security criterion.
"Insufficient evidence" with a precise next test is better than an invented cause.

## Completion

Reading and guided labs prepare the learner. Independent diagnosis demonstrates
the assessed skills, not universal networking mastery. Repeat unfamiliar variants,
retain sanitized evidence, and capture production traffic only with authorization
and an explicit retention plan.
