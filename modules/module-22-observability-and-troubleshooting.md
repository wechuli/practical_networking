# Module 22: Observability & Troubleshooting Methodology

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 21 — Packet Capture and Analysis
> **Estimated time:** 4 hours

## Overview

Earlier modules each taught a tool. This one is about choosing among them under pressure: how to go from "it's slow" or "it's failing" to a cause, with the cheapest evidence that can answer each question.

Four kinds of evidence are always available in some form:

- **metrics:** aggregated numbers over time;
- **logs:** one record per event;
- **traces:** one request followed across services;
- **flow records and captures:** what the network carried.

Each answers different questions and hides different things. The lab makes that concrete. [`labs/netlab/observe.json`](../labs/netlab/observe.json) runs a two-service application built with the course's [`obslab`](../labs/go/cmd/obslab) tool. Its instrumentation is written by hand so nothing is hidden:

- W3C trace context and spans;
- Prometheus-format metrics with histograms;
- JSON logs carrying trace IDs;
- a router whose connection tracking serves as flow records.

The lab injects five faults (a slow dependency, a lossy network, errors, a dropped port, a refused port) and records what each kind of evidence shows.

## What You Will Build Toward

- Work a problem with a method that narrows it at each step instead of guessing
- Know what metrics, logs, traces, flow records and captures can and cannot tell you
- Read histograms and percentiles correctly, and compute changes over a window
- Split a slow request's time between services and the network using spans
- Understand trace sampling and what it loses
- Turn error text and timing into a first hypothesis (refused, timeout, reset, DNS)

---

## 22.1 A Method

Good troubleshooting is a loop that shrinks the search space each time:

1. **State the problem precisely.** Get specifics:
   - what fails (operation, endpoints, protocol);
   - since when, and what changed;
   - who is affected and who isn't;
   - how often;
   - the exact error text and timing.

   "Checkout calls from the EU frontend to payments return 504 after 1 s for ~2 % of requests since 14:05" is a problem. "Payments is slow" is not.
2. **Compare with a baseline.** The lab's healthy service had p50 ≈ 2 ms and p99 ≈ 8–10 ms at the client. Without that number, 12 ms means nothing. Keep baselines: dashboards, SLOs, and a capture or trace of a healthy request.
3. **Form a hypothesis from the signature,** not from habit (22.3). Timing, error text and scope usually point at a layer before any tool runs.
4. **Test it with the cheapest discriminating evidence.** A metric you already have beats a capture you must arrange. Choose evidence that can prove you wrong.
5. **Localize.** Split the path: client ↔ frontend ↔ network ↔ backend. Use spans (22.4), captures at several points (Module 21) or tests from intermediate hosts.

   Probing from a different host changes the source address, route and policy, so a test from the middle is evidence about *that* path, not necessarily the user's.
6. **Change one thing, then re-measure with the same measurement** that showed the problem. The lab's last check repeats the load test after the repair.
7. **Write it down:** the cause, the evidence, the fix, the monitoring that would have caught it earlier.

**Two lenses help choose what to look at.**

- **RED, per service:**
  - **R**ate (requests per second);
  - **E**rrors (failed requests);
  - **D**uration (latency distribution).
- **USE, per resource:**
  - **U**tilization;
  - **S**aturation (queueing);
  - **E**rrors.

  For the network this means interface utilization and drops, socket queues and listen overflows, retransmissions, conntrack and NAT port usage.

RED says *that* users suffer. USE often says *why*.

---

## 22.2 What Each Kind of Evidence Shows

| Evidence | Answers | Blind to | Cost |
| --- | --- | --- | --- |
| **Metrics** (counters, histograms) | How much, how often, since when; percentiles; trends | Which request; why; anything not measured; detail inside a bucket | Cheapest; always on |
| **Logs** (one line per event) | What happened to *this* request; exact error text | Time spent elsewhere; requests that never arrived (the backend logged **nothing** when its port was dropped) | Moderate; volume grows with traffic |
| **Traces** (spans joined by trace ID) | Where one request's time went, across services; which call failed | Unsampled requests; uninstrumented hops; the network's internals | Higher; usually sampled |
| **Flow records** (VPC flow logs, conntrack, NetFlow/IPFIX) | Who talked to whom, packets and bytes each way, whether replies came back | Payload; timing within the flow; *when* a long flow broke | Moderate; aggregated over intervals |
| **Captures** | Exactly what crossed one point | Everything elsewhere; encrypted content without keys; volume limits | Highest; arranged per incident |

Start at the top of the table and move down only when the question demands it.

---

## 22.3 Failure Signatures: Error Text and Timing

These are the signatures measured across the course, now seen from an application:

| What the caller reports | Timing | First hypothesis | Measured in |
| --- | --- | --- | --- |
| `connection refused` (ECONNREFUSED) | ~1 RTT | Nothing listening, or REJECT with tcp-reset or port-unreachable | lab check 10: **40 × 502 in ≤ 5 ms**; Modules 11, 18 |
| Timeout during connect (`dial … i/o timeout`) | Exactly the caller's timeout | SYN or SYN-ACK dropped: firewall, security group, routing, dead host | lab check 8: **40 × 504 at 1.00 s**; Modules 11, 18 |
| Timeout after connecting (`awaiting headers`, read timeout) | The caller's timeout | Server slow or hung, or the path dropped data | lab check 8: pooled connections into a dropped port |
| `No route to host` (EHOSTUNREACH) | ms, or ~3 s (ARP) | No route, failed neighbour resolution, or ICMP-prohibited REJECT | Modules 6, 18 |
| `connection reset by peer` (ECONNRESET) | Mid-connection | Peer closed with unread data, aborted, or a middlebox reset | Modules 11, 20 |
| `cannot assign requested address` (EADDRNOTAVAIL) | Immediate | Client's own source ports exhausted | Module 20 |
| `too many open files` (EMFILE) | Immediate, server side | Descriptor limit; clients connect then hang | Module 20 |
| Name resolution error, NXDOMAIN, SERVFAIL | ms to 5 s × retries | Resolver config, search domains, upstream | Modules 12, 17 |
| Fixed extra latency (1 s, 3 s, …) | Repeated exact values | A retransmission timer (SYN retries, Module 11) | Modules 11, 21 |
| Large transfers hang, small work | — | MTU / PMTUD black hole | Modules 14, 19 |

Error text isn't always precise. In the dropped-port test, Go's client said `Client.Timeout exceeded while awaiting headers` for **36 of 40** requests that were stuck on pooled connections, and also for the 4 that were still dialing (`dial tcp …: i/o timeout (… awaiting headers)`). The flow records (22.6) settled it: 20 connection attempts with no reply.

---

## 22.4 Metrics: Percentiles, Histograms and Windows

**The incident.** The backend was made to delay 10 % of requests by 300 ms. Three measurements of the same 1000 requests (three runs):

| | p50 | p90 | p99 | mean |
| --- | --- | --- | --- | --- |
| Client-measured | 0.9–1.0 ms | 4.2–300.7 ms | 302.2–302.5 ms | — |
| Frontend server spans (exact) | 0.6–0.7 ms | 3.0–300.5 ms | **301.7–301.9 ms** | — |
| Frontend histogram, `histogram_quantile` over the incident window | 2.8 ms | 5.0–272.7 ms | **473.1–477.3 ms** | 28.7–33.8 ms |

Lessons from the table:

- **Histogram percentiles are estimates.** PromQL's `histogram_quantile` finds the bucket holding the requested rank and interpolates linearly inside it.
  - With the default buckets (…, 0.25 s, 0.5 s, …), the true p99 of 302 ms was reported as **≈ 475 ms**.
  - The p50 of 0.6 ms was reported as 2.8 ms, inside the 0–5 ms bucket.
  - The p90 sat exactly on the edge of the slow 10 %, so it swung between runs.

  Choose bucket boundaries around your SLO thresholds, or use native (exponential) histograms in Prometheus 3.
- **The mean describes no request.** 29–34 ms was the average of 90 % fast and 10 % slow requests. Alert on percentiles or on the fraction of requests over a threshold (an SLO), not on means.
- **Counters only grow.** Over the frontend's whole life (baseline plus incident), the same histogram gave p99 ≈ 446–455 ms. An incident needs the change over its window: PromQL's `increase()`/`rate()`, or `obslab metrics -since` with an earlier scrape.

---

## 22.5 Traces: Where the Time Went

A trace is a tree of spans sharing one trace ID. The ID travels between services in the W3C `traceparent` header:

```
traceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01
             version-trace id (16 bytes)-parent span id (8 bytes)-flags (01 = sampled)
```

Each service creates a **server span** for the request it handles and a **client span** for each call it makes, and passes a new `traceparent` with its client span as the parent. With spans at both ends of a call, the gaps can be computed:

```
client → frontend network   = load client span − frontend server span
frontend's own work         = frontend server span − its client span
network + queues to backend = frontend client span − backend server span
backend time                = backend server span
```

In the lab, every run kept 4 spans per request and **0 spans with a missing parent**: propagation worked end to end. Two incidents, decomposed:

| Incident | Slowest traces |
| --- | --- |
| Backend delays 10 % by 300 ms | `backend 300.1–301.1 ms`, network + queue 2–5 ms: the **backend** |
| 5 % loss + 5 ms each way between frontend and backend | `backend 0.0 ms`, network + queue **443–961 ms**: the **network** (retransmissions) |

In the lossy case the client's p50 rose from about 2 ms to 11.7–12.0 ms, which is the added 10 ms RTT. p99 was 262–291 ms and the maximum 517–1001 ms (one request in one run hit the frontend's 1 s timeout and got a 504). The backend's own spans stayed under 1 ms.

No trace says "packet loss", but the frontend host's counters did: `TcpRetransSegs 82–88`, `TcpExtTCPTimeouts 4–9`, `TcpExtTCPLossProbes 73–81` during the run. That is the USE lens: a host metric that points at the network without any tracing.

**Logs join traces through the trace ID.** The slowest trace's ID found one log line in each service (`duration_ms 305.1` at the frontend, `300.1` at the backend). Put the trace ID in every log line.

### Sampling

Tracing every request is expensive, so most systems sample. With the backend failing 2 % of requests:

| Policy | Traces kept | Error traces available |
| --- | --- | --- |
| Keep all (100 %) | 2000 | **26–39 of 26–39** |
| Tail rule over the full set: keep errors and traces ≥ 250 ms | 26–39 (1.3–1.9 %) | **all of them** |
| **Head sampling 10 %**, decided before the outcome was known | 197–202 | **4–5**, while the frontend log recorded **30–41** 503s |

Head sampling (the `01` flag, set by the first service) is cheap and keeps whole traces, but it keeps a random tenth of the problems. Tail sampling decides after the trace finishes, for example in the OpenTelemetry Collector's tail-sampling processor. It keeps the interesting traces, but it must buffer whole traces before deciding.

During an incident, check whether the request you care about was sampled before concluding that "there is no trace".

---

## 22.6 Flow Records

Flow records summarize connections:

- 5-tuple;
- packets and bytes each way;
- start and end;
- for cloud flow logs, ACCEPT/REJECT per security group or ACL.

The lab used the router's connection tracking (`nf_conntrack_acct=1`), rendered by `obslab flows`, while the backend's firewall dropped its port:

```
prot source                destination           state        pkts/bytes out  pkts/bytes in  verdict
tcp  198.18.221.10:49134   198.18.222.10:8081    SYN_SENT               1/60            0/0  NO REPLY
tcp  198.18.221.10:37058   198.18.222.10:8081    ESTABLISHED         18/2722         8/2320  two-way
```

What the records said, and what they couldn't:

- **20 connection attempts** passed the router and got **no reply**: "dropped beyond this point, or nothing there".
- **Pooled connections** that broke during the incident still read "two-way". Flow counters cover the whole life of a flow, so they can't say *when* it stopped working.

Cloud flow logs behave the same way:

- **AWS VPC flow logs:** `ACCEPT`/`REJECT` from security groups and NACLs, aggregated over 1- or 10-minute intervals. `NODATA` means no traffic; `SKIPDATA` means records were dropped and that interval is incomplete.
- **GCP VPC flow logs** sample.
- **Azure** is moving from NSG flow logs to virtual network flow logs: new NSG flow logs can't be created, and existing ones retire on 30 September 2027.

In all of them, ACCEPT means a rule allowed the packet. It does not prove delivery.

**Path analysis without traffic.** AWS Reachability Analyzer, Azure Network Watcher (IP flow verify, next hop, connection troubleshoot) and GCP Connectivity Tests evaluate routes, security groups, ACLs and gateways for a hypothetical flow. They are good for "is it *configured* to work". They say nothing about loss, latency or the application.

---

## 22.7 Playbooks

Each playbook is a sequence of discriminating questions, cheapest first.

**"It fails" (connect errors)**
1. **Read the error and its timing** (22.3). Refused in milliseconds, or timeout at exactly the timeout value?
2. **Refused:**
   - `ss -ltnp` on the target: is it listening, and on which address (`127.0.0.1` vs `0.0.0.0`)?
   - Then look for a REJECT rule (counters), a load balancer with no healthy backends, or a Kubernetes Service with no endpoints (Module 17).
3. **Timeout:**
   - Is the SYN leaving? Is it arriving? Capture at both ends (Module 21), or check flow records for no reply.
   - Then check the dropping layer: security group, NACL, host firewall, route.
4. **Name errors:** `dig` against the configured resolver, then the authority. Check search domains (Module 12).

**"It's slow"**
1. **Phase timings:** `curl -w` or the course's `phases` tool, for DNS, connect, TLS, first byte and transfer (Modules 0, 15).
   - A slow connect points at the network or the accept queue.
   - Slow first byte with fast connect points at the server (confirm with its spans or logs).
   - Slow transfer points at throughput: loss, window, MTU (Module 14).
2. **Traces:** which span holds the time? Is it inside a service, or between client and server spans?
3. **Host counters on the suspect side:**
   - `nstat` for retransmissions, timeouts and listen drops;
   - `ss -ti` for RTT, cwnd and retransmits per connection.
4. **Capture** only to settle what the cheaper evidence cannot.

**"Some requests fail"**
1. **Scope:** which instances, availability zones, destinations or clients? Compare with a healthy peer.
2. **Correlate:** error spikes against deploys, scaling events, connection counts, NAT port allocation errors and conntrack insert failures (Modules 16, 20).
3. **Check the traces you have,** remembering sampling.

---

## Hands-On Lab: Five Incidents, Five Kinds of Evidence

### Lab Goals

- Establish a baseline, then diagnose a slow dependency, a lossy network, an error burst and a blocked port
- Compare exact span percentiles with histogram estimates; compute metrics over a window
- Split latency between services and network using spans
- See what head sampling loses and what logs and flow records add

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), the Go toolbox (`cd labs/go && go build -o bin/ ./cmd/...`)
- `conntrack`, `iproute2` (nstat), `curl`

### Lab Part 1: Run the Lab

```
client 198.18.220.10 ── frontend :8080 ── router (pathlab · conntrack flows) ── backend :8081
                         spans + logs       delay/loss on demand                  faults: slow / fail
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/observe.json      # about 15 seconds
```

The 11 checks passed in three runs. Spans, logs, metric snapshots and flow tables are kept in the evidence directory.

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/observe.json
alias lab='sudo ./bin/netlab exec ../netlab/observe.json'
E=$(ls -dt /tmp/netlab-observe-* | head -1)
```

1. **Your own incident.** Have a partner set a fault you don't see, for example:
   ```bash
   lab backend -- curl -s --noproxy '*' 'http://198.18.222.10:8081/admin/faults?slow=0.05&delay=800ms'
   ```
   or `lab router -- ./bin/pathlab set loss=0.03`. Then work it with the method in 22.1, using metrics first (`obslab metrics -save` and `-since`), then spans, then counters.
2. **Bucket choice.** Read `lab client -- curl -s --noproxy '*' http://198.18.220.20:8080/metrics`. Work out by hand what `histogram_quantile(0.99)` must return for a fleet where 2 % of requests take 300 ms.
3. **Follow one request.** Take a trace ID from `obslab spans -slowest 1 $E/spans-*.jsonl`, then `grep` it in both logs and in the span files. Draw the span tree.
4. **Flows over time.** While `obslab load -n 5000` runs, watch `lab router -- conntrack -L -o extended -p tcp --dport 8081 | ./bin/obslab flows` a few times. Notice how pooled connections accumulate counts.

`sudo ./bin/netlab down ../netlab/observe.json`.

### Lab Part 3: Your Environment

- For one service you support, list the RED metrics, where its traces go and at what sampling rate, whether logs carry trace IDs, and where its flow logs live and with what delay.
- Find one histogram whose buckets don't bracket the SLO threshold, and propose better ones.

---

## Real-World Troubleshooting Scenario

### Scenario: "Checkout p99 Doubled; Payments Says Its Latency Is Flat"

**The situation.**

- After a network change, the checkout service's p99 to payments rose from 120 ms to 260 ms.
- The payments team shows its server-side p99 unchanged at 80 ms.
- Both teams are right about their own numbers.

**Step 1 — Compare like with like.**
- Checkout's p99 comes from a client-side histogram with buckets at 0.1 s and 0.25 s, and the new value sits in the 0.25–0.5 s bucket. The real p99 could be anywhere above 250 ms.
- The exact client spans give 231 ms.

**Step 2 — Decompose with traces.** For the slowest 1 % of sampled traces:
- checkout's client span ≈ 230 ms;
- payments' server span ≈ 75 ms;
- the gap is ≈ 155 ms, **between** the services. That's the lab's lossy-network shape, not its slow-backend shape.

**Step 3 — Ask the network with host counters.** `nstat` on checkout nodes shows `TcpRetransSegs` up about 30× since the change, and `ss -ti` to payments shows `retrans` on many connections. Flow records show the traffic now leaves through a new inspection appliance.

**Step 4 — Localize.** Captures before and after the appliance (Module 21) show about 1 % of segments entering it and never leaving. Its interface statistics (`ethtool -S`) show `rx_missed_errors` climbing during traffic bursts: its receive rings overflow, so it can't keep up at peak.

**The fix:**
- scale the appliance, or route internal service traffic around it;
- then re-measure with the same client spans and histogram: p99 back to about 120 ms;
- add a bucket at 0.15 s and an alert on retransmission rate.

**What made it fast:** comparing the same quantity on both sides, using spans to locate the time between the services, and a host counter that named the mechanism.

---

## Common Failure Patterns

| Symptom | Likely cause | Evidence that confirms | Next step |
| --- | --- | --- | --- |
| Client latency up, server latency flat | Network or queueing between them | Client span − server span; retransmission counters | Captures at both ends; path changes |
| p99 jumps but dashboards "look fine" | Mean or wide buckets hide the tail | Exact spans vs histogram buckets | Percentile/SLO alerts; better buckets |
| "There is no trace of the failing request" | Not sampled (head sampling) | Request in logs, not in trace store | Tail sampling for errors; log trace IDs |
| Spans with missing parents | Context not propagated (proxy, async hop, new SDK) | Orphan count; traceparent at each hop | Fix propagation |
| 504 at exactly the timeout | Dependency dropped or hung | Flow records with no reply; no server span | Firewall/route/health of dependency |
| 502 in milliseconds | Dependency refused | `connection refused` in logs | Listener, REJECT, empty backend pool |
| Errors only on some instances or AZs | Local resource limit or path | Per-instance metrics; NAT/conntrack counters | Compare with healthy peer |
| Flow logs show ACCEPT but the app times out | ACCEPT is a rule decision, not delivery | Capture beyond the allowing point | Look past the security layer |
| Incident numbers mixed with history | Counters read without a window | `increase()` / `-since` | Always scope to the incident window |

---

## Summary

- Work problems as a loop: define precisely, compare with a baseline, hypothesize from the signature, test with the cheapest discriminating evidence, localize, change one thing, re-measure the same way.
- Metrics are cheap and always on, but aggregate. Histogram percentiles are interpolated within buckets: a true p99 of 302 ms read as about 475 ms. Means describe no request, and counters must be read over a window.
- Traces locate time. Client span minus server span is the network and queues between them: 443–961 ms in the lossy case, against a backend under 1 ms. Logs join traces through the trace ID. Head sampling at 10 % kept 4–5 of 30–41 error traces, while tail rules keep them all.
- Flow records show who talked to whom and whether replies came back (20 attempts, 1 packet out, nothing back), but not when a long flow broke. Path analyzers check configuration, not delivery.
- Error text plus timing gives the first hypothesis: refused in milliseconds, timeout at exactly the timeout. Confirm with a second kind of evidence, because error strings can be imprecise.

## What's Next

Module 23 covers the supporting infrastructure every environment depends on: DHCP, load balancers, NTP and proxies, along with their failure modes and how they look from the evidence sources in this module.

---

## Quick Reference Card

```
MODULE 22: OBSERVABILITY & TROUBLESHOOTING — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

METHOD
  define precisely → baseline → hypothesis from signature → cheapest discriminating evidence
  → localize → change ONE thing → re-measure the same way → write it down
  RED per service (rate, errors, duration) · USE per resource (utilization, saturation, errors)

EVIDENCE
  metrics: how much/when (aggregate) · logs: this event (exact text) · traces: where the time went
  flows: who/what/replies? · captures: exactly what crossed one point

SIGNATURES
  refused (ms) · timeout (= timeout value) · no route · reset · EADDRNOTAVAIL · EMFILE · NXDOMAIN/SERVFAIL
  fixed 1 s/3 s extras = retransmission timers · large-only hangs = MTU

METRICS
  histogram_quantile interpolates within a bucket → set buckets around SLOs; native histograms
  rate()/increase() over the incident window · alert on percentiles / SLO burn, not means
  COURSE: obslab metrics -url …/metrics [-save F] [-since F]

TRACES
  traceparent: 00-<trace 32 hex>-<parent span 16 hex>-<flags, 01 = sampled>
  network+queue = client span − server span · put trace_id in every log line
  head sampling: cheap, random · tail sampling: keeps errors/slow, buffers whole traces
  COURSE: obslab spans [-slowest N] [-tail-slow D] FILES…

HOST COUNTERS
  nstat TcpRetransSegs TcpExtTCPTimeouts TcpExtListenDrops UdpRcvbufErrors · ss -ti · ss -s
  conntrack -S (insert_failed, drop) · ip -s link (drops)

FLOWS
  conntrack -L -o extended (nf_conntrack_acct=1) · [UNREPLIED] = no reply seen
  AWS: ACCEPT/REJECT, NODATA, SKIPDATA, 1–10 min aggregation · ACCEPT ≠ delivered
  path analysis (config only): Reachability Analyzer · Network Watcher · Connectivity Tests
```

---

## References

- W3C Trace Context — https://www.w3.org/TR/trace-context/
- OpenTelemetry — traces, sampling, the Collector's tail-sampling processor — https://opentelemetry.io/docs/
- Prometheus — histograms and summaries, `histogram_quantile`, native histograms — https://prometheus.io/docs/practices/histograms/
- B. Gregg, "The USE Method"; T. Wilkie, "The RED Method"
- Google SRE Book — "Monitoring Distributed Systems" (the four golden signals) and "Effective Troubleshooting"
- AWS VPC Flow Logs; AWS Reachability Analyzer; Azure virtual network flow logs and Network Watcher; Google Cloud VPC Flow Logs and Connectivity Tests
- `man conntrack`, `man nstat`, `man ss`
