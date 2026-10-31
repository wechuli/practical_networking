# Go Lab Toolbox

All new labs in this course are Go programs in this one Go module. Each tool lives
under `cmd/<tool>`; the diagnostic tools grow into the capstone `netdoctor` CLI.

| Tool | Module | What it does |
| --- | --- | --- |
| [`phases`](cmd/phases) | 0, 2 | Times DNS, connect, proxy tunnel, TLS, request, server wait, and transfer for each HTTP(S) request, names the step that failed, and shows connection reuse with `-count` |
| [`labserver`](cmd/labserver) | 2+ | Local HTTPS/HTTP server with a throwaway CA, chosen body size, response delay, and an `X-Served-By` identity (`-id`), so labs run offline |
| [`netlab`](cmd/netlab) | 3+ | Builds labs from JSON files in [`../netlab`](../netlab) inside network namespaces (nodes, bridges, VLANs, routes, setup commands, background services, per-node files such as `/etc/resolv.conf` overlaid through `/etc/netns`), prints every command, runs checks (ping, STP, FDB, commands with expected exit codes and output, packet captures) in a node or in the host itself (`@host`, with lab-level `setup`, `teardown` and `preflight` commands for labs that drive Docker), and always tears down |
| [`subnet`](cmd/subnet) | 5 | Network, broadcast, usable range, cloud provider reservations (`-cloud aws|azure|gcp`), on-link checks, and the kind of address space for IPv4 and IPv6 prefixes |
| [`gotrace`](cmd/gotrace) | 7 | A traceroute in standard-library Go: UDP or TCP SYN probes with rising TTL, ICMP read on a raw socket, each reply explained (`!H`, `!N`, `!X`, `!F-mtu`); `-fixed` keeps one flow for ECMP. Needs root |
| [`udplab`](cmd/udplab) | 10 | UDP echo server and numbered-datagram sender: answered %, retries, duplicates, reordering, truncation (MSG_TRUNC), RTT percentiles, and the socket errors connected vs unconnected sockets see |
| [`tcplab`](cmd/tcplab) | 11 | TCP server that misbehaves on purpose (never close, never read, never accept, reset, close without answering; chosen backlog and SO_RCVBUF) and a client that classifies connect failures by errno and timing, and reports each transfer's `tcp_info`: RTT, RTO, MSS, cwnd, ssthresh, retransmissions, probes, options, congestion control |
| [`dnslab`](cmd/dnslab) | 12 | Standard-library DNS toolkit with a readable message encoder/decoder: `trace` resolves iteratively from root hints explaining referrals, glue, CNAME restarts, truncation and lame servers; `watch` shows when each server's answer changes; `query` prints one exchange with its negative-caching TTL; `lookup` uses Go's own resolver |
| [`tlslab`](cmd/tlslab) | 13 | TLS toolkit on `crypto/tls` and `crypto/x509`: `pki` (a lab root, issuing CA, good and broken leaves, client, rogue and inspection CAs), `serve` (SNI-selected certificates, version/group limits, mTLS, handshake logging), `inspect` (version, cipher, ALPN, timing, resumption, the chain as sent, verification with Go's exact error), `relay` (added path delay), `intercept` (a transparent TLS-inspecting proxy), `get`; `serve -clientca … -authz IDENTITY=/prefix` authorizes requests by client-certificate identity (Module 18) |
| [`pathlab`](cmd/pathlab) | 14 | A userspace path emulator (a readable stand-in for `tc netem`) over two TUN devices: one-way delay, jitter, random loss, a rate-limited bottleneck with a bounded queue, runtime changes (`pathlab set`), a one-shot targeted drop (`drop_big=N`) and per-direction counters (`pathlab stats`) |
| [`weblab`](cmd/weblab) | 15 | Web protocols by hand on the standard library: a server for HTTP/1.1, HTTP/2 (TLS and h2c), gRPC built on HTTP/2 for `labs/grpc/echo.proto`, WebSockets (RFC 6455) and server-sent events; clients that time a page load per protocol, print a gRPC call frame by frame, and show WebSocket frames; proxies that forward over HTTP/1.1 or buffer, and an idle-timeout relay. HTTP/3 is [`labs/http3/h3lab.py`](../http3/h3lab.py) (aioquic), since Go's standard library has no QUIC |
| [`kproxy`](cmd/kproxy) | 17 | Programs a node's iptables the way kube-proxy's iptables mode does (KUBE-SERVICES/SVC/SEP/NODEPORTS/EXT/SVL chains with kube-proxy's hashed names, comments and order; random per-connection choice; MARK-MASQ/POSTROUTING; REJECT for no endpoints; DROP for `Local` without local endpoints) from a JSON file of Services and endpoints (`show`, `sync`, `flush`) |
| [`socklab`](cmd/socklab) | 20 | Sockets as applications use them: `serve` (netpoll or thread-per-connection models; echo, discard, greet, reads, close modes; SO_REUSEPORT), `writes` (many small writes, Nagle on/off), `hold`/`churn` (ephemeral ports and TIME_WAIT, with errno explanations), `greet`, `bind` (raw bind with chosen SO_REUSEADDR/SO_REUSEPORT), `http` (a Go HTTP client reporting dials, reuse and latency), `relay` (with and without backpressure), `udp-sink`/`udp-blast` (receive-buffer drops), `stat` (threads, RSS, descriptors, epoll registrations) |
| [`obslab`](cmd/obslab) | 22 | A frontend → backend application instrumented by hand: W3C `traceparent` propagation and JSON spans, Prometheus-format counters and histograms, JSON logs with trace IDs, injectable faults (`/admin/faults`); `load` (head-sampled traffic with client spans), `spans` (percentiles per service, orphans, per-trace breakdown of service vs network time, tail-sampling what-if), `metrics` (histogram_quantile, increase over a window with `-save`/`-since`), `logs`, `flows` (conntrack as flow records) |
| [`edgelab`](cmd/edgelab) | 23 | Load-balancer and edge pieces: `backend` (reports peer, X-Forwarded-For and PROXY-protocol client; `/health` and `/admin/health?code=N`; optional PROXY v1 listener), `hits` (counts which backend answered, failures and slow requests; cookies, forged XFF, `-env-proxy`), `raw` (send exact bytes, print the reply line), `fproxy` (forward proxy: CONNECT and absolute-URI, logged), `ntp serve` (an SNTP server with a clock offset and one-way delays in and out) and `ntp query` (offset and delay from the four timestamps), `tlsclock` (verify a certificate with a shifted clock) |
| [`caselab`](cmd/caselab) | 26 | Capstone support: `probe` (the capstone application — resolve through the stub resolver and its search list, connect, TLS against its configured CA bundle, one HTTP GET — logged stage by stage with classified DNS, connect and TLS errors but no diagnosis), `case6` (the instructor's reading of the Capstone 6 flow-log and NAT dataset, failing if the dataset stops supporting it) |
| [`rfcstatus`](cmd/rfcstatus) | 1 | Says whether an RFC is current, follows obsoleted-by chains to the current document, and lists updates, from the RFC Editor's index |

Python labs elsewhere under `labs/` stay until their Go replacements pass.

## Requirements

- Go 1.24 or newer (`go version`). Install from <https://go.dev/dl/>; Ubuntu 24.04's
  `golang` package is older than this module needs.
- Linux or macOS. The tools use Unix socket error numbers.

## Build, Test, Run

Run from this directory (`labs/go`):

```bash
go vet ./...
go test -race ./...                 # loopback-only tests; no root, no internet
go build -o bin/ ./cmd/...          # binaries land in bin/ (git-ignored)
./bin/phases https://example.com
```

`go run ./cmd/phases <url>` works too, without a separate build step.

The tests start local servers on 127.0.0.1 and never touch the internet, so they
pass on an offline machine and inside CI. `netlab`'s integration tests build real
network namespaces, so they are opt-in and need root:

```bash
go test -c -o /tmp/netlab.test ./cmd/netlab
cd cmd/netlab      # the tests find the lab files relative to this directory
sudo NETLAB_INTEGRATION=1 /tmp/netlab.test -test.v    # about 2 minutes (STP converges twice; the ARP lab waits on real timers)
```

Lab exercises that need the internet, root, namespaces, or a cloud account say so
in their module.

## Conventions

- Standard library first. Third-party modules are added only when the standard
  library cannot do the job (packet decoding, DNS wire format, netlink), and each
  one is named in the module that introduces it.
- Every tool exits 0 on success, 1 when the network operation failed (the report
  says why), and 2 for invalid usage.
- Every tool has a `-json` flag so its output can be scripted or attached to a
  ticket.
- Failure explanations say what happened on the wire and the first command to
  run, not only the error text.
