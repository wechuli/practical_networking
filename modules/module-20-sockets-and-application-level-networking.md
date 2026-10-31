# Module 20: Sockets and Application-Level Networking

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 19 — VPNs, Tunnels & Overlay Networks (and Modules 10–11 for UDP and TCP)
> **Estimated time:** 4 hours

## Overview

Applications don't send packets; they call `connect()`, `write()`, `read()` and `close()` on sockets, and the kernel turns those calls into the TCP and UDP behaviour of Modules 10 and 11. Many production incidents sit exactly at this boundary, where the network is fine and the *application's use of it* is not:

- a client that runs out of local ports (`cannot assign requested address`);
- a server that runs out of file descriptors and stops accepting, while clients still "connect";
- an HTTP client that opens hundreds of connections it could have reused;
- a protocol parser that assumes one `read()` is one message;
- a proxy that buffers everything in memory because nothing pushes back;
- a UDP collector that silently drops most of its input.

This module measures each of these in [`labs/netlab/sockets.json`](../labs/netlab/sockets.json) with the course's [`socklab`](../labs/go/cmd/socklab) tool. The setup is a client and a server separated by a router that can add delay.

## What You Will Build Toward

- Map socket calls to what happens on the wire, and know which side holds TIME_WAIT
- Recognize and explain EADDRNOTAVAIL, EADDRINUSE, EMFILE and the other errnos applications report
- Measure connection reuse in an HTTP client and find the configuration that defeats it
- Explain thread-per-connection vs event loops (epoll) with real thread and memory costs
- Recognize missing backpressure in a proxy and receive-buffer drops in a UDP service

---

## 20.1 The Socket API and Its Errors

```
server: socket() → bind(addr:port) → listen(backlog) → accept() … read()/write() … close()
client: socket() → [bind()] → connect(addr:port) … write()/read() … close()
```

| Call | What the kernel does | Errors you will see |
| --- | --- | --- |
| `socket()` | allocates a descriptor | `EMFILE` (process limit), `ENFILE` (system limit) |
| `bind()` | claims a local address and port | `EADDRINUSE`, `EACCES` (port < 1024 without privilege) |
| `listen(backlog)` | starts completing handshakes into the accept queue (Module 11) | — |
| `accept()` | takes the next completed connection from the queue and creates a descriptor for it | `EMFILE`/`ENFILE` (the connection stays queued) |
| `connect()` | picks a source port if not bound, sends SYN | `EADDRNOTAVAIL` (no free source port), `ECONNREFUSED`, `ETIMEDOUT`, `EHOSTUNREACH`, `ENETUNREACH` |
| `read()` | copies from the receive buffer; 0 means the peer closed (EOF) | `ECONNRESET` |
| `write()` | copies into the send buffer, blocks or returns `EAGAIN` when it is full | `EPIPE`, `ECONNRESET` |
| `close()` | sends FIN, or **RST if unread data remains** | — |

Earlier modules measured `ECONNREFUSED`, `ETIMEDOUT` and `EHOSTUNREACH` (Modules 11 and 18). This module produces the rest.

Language runtimes wrap these calls without changing them. The `strace` in Part 2 shows Go's `connect()`:

1. a **non-blocking** socket (`SOCK_NONBLOCK`);
2. `connect()` returning `EINPROGRESS`;
3. registration with epoll;
4. `getsockopt(SO_ERROR)` to learn the result.

Go programs look blocking, but every Go socket is non-blocking underneath.

Two defaults in Go matter later in this module:

- **`TCP_NODELAY` is on** for every connection, and Nagle's algorithm is off.
- **`SO_REUSEADDR` is set** on every listener.

---

## 20.2 TCP Is a Byte Stream

The client made 100 `write()` calls of 10 bytes each on one connection, then repeated it with Nagle's algorithm on. The server recorded what each `read()` returned (four runs):

| Client | Data segments on the wire | Server `read()`s |
| --- | --- | --- |
| `TCP_NODELAY` (Go's default) | 11, 11, 11, 25 | 2 (100 + 900), 7, 19, 2 |
| Nagle's algorithm | 2 | 1 or 2 (10 + 990, or all 1000) |

Three lessons:

- **Writes are not messages.** The receiver saw 1000 bytes in 1, 2, 7 or 19 pieces. Which split you get depends on timing, not on the sender's calls. A parser that treats one `read()` as one message works in testing and fails under load.
- **`TCP_NODELAY` doesn't mean one segment per write.** Linux's autocorking (`tcp_autocorking=1`) merged 100 writes into 11–25 segments while earlier segments were still queued.
- **Nagle holds small writes while data is unacknowledged.** It produced 2 segments. That's efficient for bulk data, but it adds a delay of up to one RTT for request-response protocols. It is also the reason most RPC libraries set `TCP_NODELAY`.

Every TCP protocol therefore frames its own messages:

- a length prefix (gRPC, Kafka, PostgreSQL);
- a delimiter (lines, HTTP headers);
- or a self-describing format.

The reader loops until it has a whole message. UDP is different: each `recvfrom()` returns exactly one datagram, truncated if the buffer is too small (Module 10).

---

## 20.3 Ephemeral Ports and TIME_WAIT

Every TCP connection is a 4-tuple `(source IP, source port, destination IP, destination port)`. A client's source port comes from `net.ipv4.ip_local_port_range`, which is **32768–60999 by default (28,232 ports)**. The lab shrank the client's range to 32 ports so that exhaustion takes milliseconds instead of minutes:

| Experiment | Result (identical in all runs) |
| --- | --- |
| Hold connections open to server port 7002 | 32 open; attempt 33: `connect: cannot assign requested address` (**EADDRNOTAVAIL**) |
| …and at the same time to port 7003 of the same server | another 32 open: **ports run out per destination**, not per host (64 connections from 32 ports) |
| Close those (the client closes first) | the client now holds 64 TIME_WAITs, 32 per destination, for 60 s |
| 300 connect/close cycles where the **server** closes first | **300 of 300** succeeded with 32 ports; client TIME_WAIT 0, server TIME_WAIT 16 |
| 100 cycles where the **client** closes first | **32 of 100**, then 68 × EADDRNOTAVAIL; client TIME_WAIT 32 |
| `tcp_tw_reuse=1`, 1.2 s later, 100 more cycles | **32 of 100** again: connect() may reuse a TIME_WAIT port once it is older than 1 s |

What this means in practice:

- **TIME_WAIT belongs to whoever closes first** (Module 11). A client that closes first keeps every finished connection's port for 60 s. With the full default range, that caps it at about 28,232 ÷ 60 ≈ **470 new connections per second to one destination**.
- **More destinations means more tuples.** More destination IPs or ports, or more source IPs, multiply the tuples available. NAT gateways have the same arithmetic per public IP (Module 16).
- **`tcp_tw_reuse` doesn't make ports unlimited.** It makes them reusable after 1 s: about 28,000 per second with the full range. It requires TCP timestamps.
  - The default, `2`, enables it for loopback only.
  - Setting `1` is safe for outgoing connections, but it treats the symptom.
  - `tcp_tw_recycle` was removed (Linux 4.12) because it broke clients behind NAT.
- **Bound to a source address, the limit changes.** An application that calls `bind()` to a source address before `connect()` reserves a port for any destination, so it exhausts faster unless it sets `IP_BIND_ADDRESS_NO_PORT`.
- **The fix is fewer new connections:** pooling and keep-alive (20.4).

**How to confirm EADDRNOTAVAIL:**

- The error comes from the *client's* `connect()`, never the server. The server never sees the attempt.
- `ss -tan state time-wait dst IP:PORT | wc -l` and `ss -tan dst IP:PORT | wc -l` against the range size.
- `sysctl net.ipv4.ip_local_port_range` and `net.ipv4.ip_local_reserved_ports`.

---

## 20.4 Connection Reuse in HTTP Clients

A new connection costs one round trip for TCP (and one more for TLS), plus slow start. Reusing one costs nothing. Measured with 1000 requests from 20 workers over a 10 ms RTT path (Go client, three runs):

| Client configuration | TCP connections dialed | p50 latency | New TIME_WAIT (client / server) |
| --- | --- | --- | --- |
| Keep-alive off | 1000 | **23.8–24.6 ms** (2 RTT) | 50–99 / **1000** |
| Keep-alive, Go default `MaxIdleConnsPerHost = 2` | **336–345** | 13.5–13.6 ms | **313–336** / 0 |
| Keep-alive, `MaxIdleConnsPerHost = 20` | **20** | 13.6–13.8 ms (1 RTT) | 0 / 0 |
| `MaxIdleConnsPerHost = 20` but the body is never read | 1000 | 23.9–24.0 ms | 0 / 0 |

Each row is a real incident pattern:

- **No keep-alive:**
  - Every request pays the handshake, so latency roughly doubles at this RTT.
  - The server closed first (HTTP/1.1 `Connection: close`), so the 1000 TIME_WAITs landed on the server.
  - A client that closes first would pile them up on its own side instead, and with them its EADDRNOTAVAIL risk.
- **Go's default idle limit of 2 per host:**
  - With 20 concurrent workers, only 2 connections may stay idle. When a burst finishes, the other 18 are closed, and the next burst dials again.
  - Only 20 of 1000 requests *waited* for a new connection, so latency looked fine, but **hundreds of connections were dialed and thrown away**. Each one left a client TIME_WAIT.
  - At production rates this produces "connection churn", port exhaustion and load on the server's accept path. The fix is `MaxIdleConnsPerHost` ≥ the concurrency per host.
- **Unread bodies:**
  - Go returns a connection to the pool only after the body has been **read to the end and closed**. Not draining it forced 1000 new connections.
  - Closing with unread data sends **RST** instead of FIN (`close()` above), so not even a TIME_WAIT appeared, and the server's writes failed with ECONNRESET.

Other clients have their own defaults:

- Python `requests` / urllib3 pools 10 connections per host;
- Node.js's global agent keeps connections alive since Node 19;
- database drivers expose `max_open`/`max_idle`/`max_lifetime`.

Size pools from measured concurrency, and budget the server side: replicas × max connections must fit under the database's limit. HTTP/2 and gRPC multiplex many requests over one connection, which removes most of this problem (Module 15).

---

## 20.5 Thread per Connection vs an Event Loop

A blocking `read()` parks the calling thread until data arrives. There are two ways to serve many connections:

- **Thread per connection:** each connection gets a thread that blocks. Simple, but every idle connection costs a thread: kernel scheduling state plus a stack.
- **Event loop:** non-blocking sockets plus a readiness API. Linux's `epoll` reports which of thousands of sockets are ready, so a few threads serve all of them.

Measured with 2000 idle connections to two `socklab` servers (three runs, identical within 1 %):

| Server | Threads | Resident memory | epoll registrations |
| --- | --- | --- | --- |
| Go `net` (goroutines on the runtime's epoll netpoller) | 7–8 | **27 MB** (≈ 10 KB per connection) | **2002** (every socket) |
| Thread per connection (a blocking `read()` on a pinned OS thread each) | **2006** | **183 MB** (≈ 88 KB per connection) | 1 (the runtime's own, unused) |

Event loops won because idle connections are nearly free for them. Every high-connection server works this way: nginx, HAProxy, Envoy, Node.js, Redis and Go's runtime.

- **`select()`** scans every descriptor on each call, and its `fd_set` is limited to descriptors below 1024.
- **`poll()`** removes the limit, but still scans.
- **`epoll`** keeps an interest list in the kernel and returns only ready descriptors. `/proc/PID/fdinfo` of the `anon_inode:[eventpoll]` descriptor lists what it watches; `socklab stat` counts it.
- **`io_uring`** (Linux 5.1+) submits operations instead of waiting for readiness.

A thread per connection still works well at small scale and is easier to reason about. Problems start at thousands of mostly idle connections, or when some limit caps the thread count:

- memory;
- `ulimit -u`;
- `kernel.threads-max`;
- Go's own default of 10,000 threads.

---

## 20.6 File Descriptor Limits

Every socket is a file descriptor, and `RLIMIT_NOFILE` caps how many a process may hold. A server started with a limit of 64 (soft and hard) was sent 100 clients:

- **The clients all connected.** The kernel completes handshakes into the accept queue without asking the application.
- `accept4()` failed with **`too many open files` (EMFILE)**; the server logged 11–14 retries.
- **42 connections sat in the accept queue** (`ss -ltn`: `LISTEN 42 4096`), and the process held exactly 64 descriptors (59 sockets).
- A new client also connected, then got **no greeting**: `i/o timeout`.

That's the support signature: **clients connect, then hang**. The server's log shows EMFILE, its listener's Recv-Q is non-zero, and `/proc/PID/limits` shows the cap. `ls /proc/PID/fd | wc -l` (or `socklab stat`) shows how close a process is to its limit; a count that grows without load is a descriptor leak.

Where the limit comes from:

- **Shells:** `ulimit -n` (often 1024 soft) and `/etc/security/limits.conf` for PAM sessions.
- **systemd services:** `LimitNOFILE=`. Since systemd 240 the default is a soft limit of 1024 and a hard limit of 524288.
- **Containers:** the runtime's default, or `--ulimit nofile=`.
- **Go programs** raise their own soft limit at start-up (Go 1.19+). Started with 64 soft / 8192 hard, the server ran with **8191**: the hard limit − 1, by design, so Go can detect outside changes (`syscall/rlimit.go`). A shell under the same limits stayed at 64. Go restores the original limit for programs it starts.
- **System-wide:** `fs.file-max` / `fs.nr_open`, which return **ENFILE** when exhausted.

---

## 20.7 Binding: SO_REUSEADDR and SO_REUSEPORT

| Situation | Result (measured) |
| --- | --- |
| Restart a server whose old instance closed connections first (its port has TIME_WAITs), **without** `SO_REUSEADDR` | `bind() failed: address already in use` (EADDRINUSE) |
| …**with** `SO_REUSEADDR` | `bind() and listen() succeeded` |
| A port with a **live listener**, with `SO_REUSEADDR` | EADDRINUSE: `SO_REUSEADDR` doesn't let two listeners share a port on Linux |
| …with `SO_REUSEADDR` and `SO_REUSEPORT`, when the first listener did not set `SO_REUSEPORT` | EADDRINUSE: every socket must set it, and run as the same user |
| Three servers with `SO_REUSEPORT` | all bind; 300 connections split 85–115 per server (four runs) |

What follows from the table:

- **`SO_REUSEADDR` on servers.** It is the standard server setting. Go, Python's `socketserver`, nginx and most frameworks set it. A Go server never sees the TIME_WAIT case, but a hand-written C or Python server without it can't restart for up to 60 s.
- **"Address already in use" on a port with a live listener is a real conflict.** `ss -ltnp 'sport = :PORT'` names the owner (`users:(("socklab",pid=…))`).
- **`SO_REUSEPORT` (Linux 3.9+):**
  - The kernel hashes each new connection to one of the listeners. The spread is statistical, not round-robin.
  - It lets multiple processes accept independently: nginx with `reuseport`, Envoy, HAProxy.
  - It enables some zero-downtime restarts.
  - Connections waiting in one listener's queue are reset if that process exits, unless `net.ipv4.tcp_migrate_req=1` (Linux 5.14+) moves them to another listener.

---

## 20.8 Backpressure

When a consumer is slower than a producer, something has to slow the producer down or queue the difference. TCP does it with the receive window (Module 11: zero window). Applications in the middle must pass that signal on.

A relay forwarded a client's 200 MB upload to a backend that never reads:

| Relay | Client's view | Relay's memory |
| --- | --- | --- |
| `io.Copy` (one fixed buffer per direction) | **blocked after 8.9–10.9 MB**: `timed out with data still unsent` | 7.3 MB, unchanged |
| Reads everything into an in-memory queue | **wrote 200,000,000 of 200,000,000 bytes**, then waited for a close that never came | **215–223 MB** |

The bounded relay stopped reading when its write blocked, the client's TCP window closed, and the client stopped. The stall reached the producer.

The unbounded relay told the client everything was delivered. It held the data in memory, would lose it on a crash, and grows until the out-of-memory killer arrives.

The same failure appears in other places:

- log shippers;
- message-queue consumers that prefetch without limits;
- WebSocket servers that queue for slow clients;
- `async` code that spawns a task per message.

Bound every queue, and decide what happens when it is full: block, drop, or reject with an error.

### UDP: the receive buffer is the only queue

UDP has no window. A consumer that took 1 ms per datagram was sent 10,000 datagrams of 200 bytes, all within a few tens of milliseconds:

| Receive buffer | Processed | Dropped (`UdpRcvbufErrors`) |
| --- | --- | --- |
| Default (`rmem_default` 212992 → `rb212992`) | **166** | **9834** |
| `-rcvbuf 4194304` (`SO_RCVBUF`) | 6553–6576 | 3424–3447 |
| `-rcvbuf 4194304 -force` (`SO_RCVBUFFORCE`) | 6553–6554 | 3446–3447 |

Lessons from the UDP runs:

- **The sender reported every datagram as sent.** UDP never tells it.
- **The receive buffer counts kernel memory, not payload.** Each 200-byte datagram used 1280 bytes of buffer: 212992 ÷ 1280 = 166, and 8 MB ÷ 1280 = 6553, exactly the processed counts.
- **`SO_RCVBUF` is silently capped at `net.core.rmem_max`, then doubled.**
  - This lab's host had `rmem_max` 4 MB, so the plain request got 8 MB.
  - On a stock Ubuntu host (`rmem_max` 212992), the same request gets about 416 KB. Raise the sysctl, or use `SO_RCVBUFFORCE` as root.
  - A bigger buffer is more queue: fewer drops, but datagrams wait longer.

Watch these counters on syslog, metrics (StatsD), DNS and video receivers:

- `nstat UdpRcvbufErrors`;
- `ss -uam` (look at the `d` drop counter in `skmem`).

---

## Hands-On Lab: Sockets Measured

### Lab Goals

- See writes vs reads on a byte stream, and Nagle vs `TCP_NODELAY` on the wire
- Exhaust ephemeral ports, and show which side holds TIME_WAIT and what `tcp_tw_reuse` really does
- Measure HTTP connection reuse and the configurations that defeat it
- Compare thread-per-connection and epoll servers; hit EMFILE; bind with and without reuse options
- Watch backpressure work (and fail) in a relay, and UDP drop silently at a full receive buffer

### Prerequisites

- The course lab VM (Ubuntu 24.04, root) and the Go toolbox (`cd labs/go && go build -o bin/ ./cmd/...`)
- `tcpdump`, `iproute2` (`ss`, `nstat`), `util-linux` (`prlimit`), `strace` and `lsof` for Part 2

### Lab Part 1: Run the Lab

```
client 198.18.200.10 ── router (pathlab: delay on demand) ── server 198.18.201.10
                                                             discard :7002 :7003 :7005, close :7004, HTTP :8080,
                                                             netpoll :7032, threads :7033, relays :7101/:7102 → noread :7020
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/sockets.json      # about 75 seconds
```

The 24 checks produce every table above. Port counts, errnos, dial counts and descriptor counts were identical in every run; latencies, segment splits and the SO_REUSEPORT spread vary run to run.

### Lab Part 2: By Hand

```bash
sudo ./bin/netlab up ../netlab/sockets.json
alias lab='sudo ./bin/netlab exec ../netlab/sockets.json'
```

1. **Watch Go connect.**
   ```bash
   lab client -- strace -f -e trace=socket,connect,getsockopt,epoll_ctl ./bin/socklab hold -to 198.18.201.10:7002 -n 1
   ```
   Look for `SOCK_NONBLOCK`, `connect() = -1 EINPROGRESS`, `epoll_ctl(… EPOLL_CTL_ADD …)` and `getsockopt(… SO_ERROR, [0] …)`: a non-blocking connect finished by the netpoller.
2. **Keep-alive in curl.**
   ```bash
   lab client -- curl -sv -o /dev/null -o /dev/null http://198.18.201.10:8080/ http://198.18.201.10:8080/obj
   ```
   The output shows `Re-using existing connection with host …` and `Connection #0 … left intact`. Unset any proxy variables first.
3. **A descriptor leak's view.** Start `socklab hold -to 198.18.201.10:7032 -n 500 -hold 30s` in the background. Then compare `socklab stat -pid $(cat /tmp/netlab-sockets-*/srv-netpoll.pid)`, `ls /proc/PID/fd | wc -l` and `lsof -p PID | grep -c TCP` on the server.
4. **Your own churn rate.** On a busy host, `ss -s` and `ss -tan state time-wait | awk '{print $5}' | sort | uniq -c | sort -rn | head` show which destinations hold the most TIME_WAITs. Divide the range by 60 s to see how close you are.

`sudo ./bin/netlab down ../netlab/sockets.json`.

### Lab Part 3: Your Services

- For a service you run: `cat /proc/PID/limits | grep files`, its descriptor count, its threads (`ls /proc/PID/task | wc -l`) and its listener's Recv-Q (`ss -ltn`).
- Find the HTTP and database pool settings in one of your applications, and compare them with the concurrency it actually runs at.
- `nstat -az | grep -E 'UdpRcvbufErrors|ListenOverflows|ListenDrops|TCPAbortOnData'` on a few hosts: any non-zero counter is a story.

---

## Real-World Troubleshooting Scenario

### Scenario: "Our Go Service Throws `cannot assign requested address` Under Load"

**The situation.**

- A Go service calls an internal pricing API over HTTP/1.1 with 64 concurrent workers per pod.
- At peak, about 1 % of calls fail with `dial tcp 10.40.2.15:8443: connect: cannot assign requested address`, and p99 latency doubles.
- The pricing API is healthy, and its own logs show nothing for the failed calls.

**Step 1 — Place the error.** EADDRNOTAVAIL comes from the client's own `connect()`: the request never left the pod. That explains the empty server logs.

**Step 2 — Count.**

```bash
ss -tan state time-wait dst 10.40.2.15 | wc -l        # 27,900
ss -tan state established dst 10.40.2.15 | wc -l      # 64
sysctl net.ipv4.ip_local_port_range                   # 32768 60999 → 28,232 ports
```

Only 64 connections are in use, yet almost the whole range is in TIME_WAIT toward one destination. This client is opening and closing connections at nearly 470 per second.

**Step 3 — Find why it closes them.** The service builds its client with `&http.Transport{}`, so `MaxIdleConnsPerHost` is 2. With 64 workers, all but 2 connections are closed after every burst. That is the lab's middle row at 64-way concurrency. A code search also finds one error path that returns without reading the response body.

**The fix.**

- Set `MaxIdleConnsPerHost` (and `MaxConnsPerHost` if the API needs protecting) to the worker count.
- Drain and close every response body on all paths.
- Reuse one `http.Client` for the life of the process.

After the change, the pod held 64 established connections and almost no TIME_WAIT. `tcp_tw_reuse=1` or a wider port range would only have postponed the failure.

**What made it fast:** knowing which side raises EADDRNOTAVAIL, counting TIME_WAIT per destination against the range, and knowing the client library's pool defaults.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| `cannot assign requested address` (EADDRNOTAVAIL) on connect | Ephemeral ports for that destination used up — usually TIME_WAIT from client-side closes | `ss -tan state time-wait dst IP` vs range | Pool/keep-alive; drain bodies; more tuples; `tcp_tw_reuse=1` as a stopgap |
| `too many open files` (EMFILE); clients connect then hang | Process descriptor limit; often a leak | `/proc/PID/limits`, `ls /proc/PID/fd \| wc -l`, listener Recv-Q | Raise `LimitNOFILE`/ulimit; fix the leak |
| `address already in use` on start | Old instance's TIME_WAIT without SO_REUSEADDR, or another live listener | `ss -tanp 'sport = :PORT'` | SO_REUSEADDR; stop the other process |
| Many connections to one backend; high client TIME_WAIT | Pool idle limit below concurrency (Go default 2/host) or no keep-alive | Dial count vs request count; TIME_WAIT per destination | `MaxIdleConnsPerHost` ≥ concurrency |
| Keep-alive configured but every request opens a connection | Response bodies not read to EOF / not closed | Server sees one request per connection; RSTs from client | Drain and close bodies |
| Messages garbled or merged under load only | Parser assumes one read = one message | Reads vs writes (socklab `reads`) | Length-prefix or delimiter framing; read loops |
| Small request-response is slow by ~RTT | Nagle + delayed ACK interplay | Capture: small segment waits for an ACK | `TCP_NODELAY` |
| Proxy memory grows; clients think uploads succeeded | No backpressure (unbounded queue) | RSS vs backend progress | Bounded buffers; copy with blocking writes |
| UDP service misses data; senders see no errors | Receive buffer overflow | `nstat UdpRcvbufErrors`, `ss -uam` drops | Faster consumer; bigger buffer (rmem_max); more sockets (SO_REUSEPORT) |
| Server slow with thousands of idle clients | Thread per connection | Thread count ≈ connection count | Event-driven server, or cap connections |

---

## Summary

- Sockets are descriptors with a small set of calls, and most application-network failures are one errno from one call. EADDRNOTAVAIL is the client's `connect()`. EMFILE stops `accept()` while clients still connect. EADDRINUSE comes from `bind()`. `close()` with unread data sends RST.
- TCP delivers bytes, not messages. 100 writes arrived as 1–19 reads, and Nagle and autocorking change the segments, so protocols must frame their own messages.
- Source ports run out per destination tuple. TIME_WAIT belongs to whoever closes first. With 32 ports, a client that closed first managed 32 connections, a server that closed first allowed 300 of 300, and `tcp_tw_reuse` buys ports per second, not unlimited ports.
- HTTP clients reuse connections only if the pool allows enough idle connections and bodies are drained. Go's default of 2 idle per host turned 1000 requests into about 340 dials. Unread bodies turned them into 1000.
- Event loops make idle connections cheap: 2000 connections took 7 threads and 27 MB with Go's epoll netpoller, against 2006 threads and 183 MB with a thread each.
- Backpressure must travel end to end. A bounded relay stopped the client at 9–11 MB, while an unbounded one accepted 200 MB into memory. UDP's only queue is the receive buffer: 166 of 10,000 datagrams survived the default, and the sender never knew.

## What's Next

Module 21 covers packet capture and analysis in depth: tcpdump filters, tshark fields and Wireshark's expert analysis. Those are the tools that turned every claim in this course into evidence.

---

## Quick Reference Card

```
MODULE 20: SOCKETS & APPLICATION-LEVEL NETWORKING — QUICK REFERENCE
═══════════════════════════════════════════════════════════════════

ERRNOS
  EADDRNOTAVAIL  connect(): no free source port for this destination (client side)
  EADDRINUSE     bind(): live listener, or TIME_WAIT without SO_REUSEADDR
  EMFILE/ENFILE  process / system descriptor limit — accept() fails, clients still connect
  ECONNREFUSED · ETIMEDOUT · EHOSTUNREACH · ENETUNREACH  (Modules 11, 18)
  ECONNRESET/EPIPE  peer reset (e.g. it closed with unread data)

PORTS & TIME_WAIT
  sysctl net.ipv4.ip_local_port_range (32768–60999 = 28,232) · ip_local_reserved_ports
  limit ≈ range ÷ 60 s new connections/s per destination when the client closes first
  ss -tan state time-wait dst IP | wc -l · ss -s
  tcp_tw_reuse: 2 (default, loopback) · 1 (outgoing, after 1 s, needs timestamps)
  fix: pooling / keep-alive; let the server close first; more tuples

HTTP CLIENTS
  Go: MaxIdleConnsPerHost default 2 → set ≥ concurrency; read body to EOF and Close(); one Client per process
  curl -v: "Re-using existing connection" / "left intact"

DESCRIPTORS
  /proc/PID/limits · ls /proc/PID/fd | wc -l · lsof -p PID · ss -ltn (Recv-Q on the listener)
  systemd LimitNOFILE= (default 1024 soft / 524288 hard) · Go raises soft to hard − 1 itself

BINDING
  SO_REUSEADDR: restart over TIME_WAIT (not two listeners) · SO_REUSEPORT: all set it, kernel hashes connections
  ss -ltnp 'sport = :PORT' → owner

I/O MODELS
  thread per connection: threads ≈ connections · epoll: few threads, /proc/PID/fdinfo (tfd lines)
  strace -f -e trace=network,epoll_ctl,epoll_pwait PROGRAM

BACKPRESSURE & UDP
  bounded buffers; blocking writes propagate to TCP windows
  nstat UdpRcvbufErrors · ss -uam (skmem d=drops) · SO_RCVBUF capped by net.core.rmem_max (×2)

COURSE TOOL
  socklab serve|writes|hold|churn|greet|bind|http|relay|udp-sink|udp-blast|stat
```

---

## References

- `man 2 socket`, `bind`, `listen`, `accept`, `connect`, `close`; `man 7 socket` (SO_REUSEADDR, SO_REUSEPORT, SO_RCVBUF, SO_RCVBUFFORCE), `man 7 tcp` (TCP_NODELAY, tcp_autocorking, tcp_tw_reuse), `man 7 ip` (ip_local_port_range, IP_BIND_ADDRESS_NO_PORT), `man 7 epoll`, `man 7 udp`
- Linux `Documentation/networking/ip-sysctl.rst`
- Go: `net/http` Transport documentation (MaxIdleConnsPerHost, response bodies); `syscall/rlimit.go` and go.dev/issue/46279 (RLIMIT_NOFILE)
- systemd.exec(5) — LimitNOFILE; systemd 240 release notes
- D. Kegel, "The C10K problem"
- RFC 9293 — TCP (TIME_WAIT); RFC 7323 — timestamps (PAWS)
