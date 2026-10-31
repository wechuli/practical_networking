# Module 15: HTTP/2, HTTP/3, gRPC & WebSockets

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 14 — Network Performance
> **Estimated time:** 4–5 hours

## Overview

Module 14 showed that interactive traffic is bound by round trips. The protocols in this module change how many round trips a request costs and what happens when one is delayed. HTTP/2 carries many requests on one TCP connection as interleaved streams. HTTP/3 moves those streams onto QUIC, over UDP, so that one lost packet stops only its own stream, and so that a connection can start with fewer round trips. gRPC is a set of conventions on top of HTTP/2. WebSockets and server-sent events keep a connection open so that the server can send at any time.

Each one also brings its own support tickets:

- HTTP/2 that silently falls back to HTTP/1.1.
- HTTP/3 that never happens because UDP 443 is blocked.
- gRPC calls that fail with UNKNOWN or UNIMPLEMENTED behind a load balancer, or that return HTTP 200 and still fail.
- WebSockets cut every 60 seconds.
- An event stream that arrives in one burst.

This module measures the behaviour behind each of these on a 50 ms path, using the course's `weblab` (Go) and `h3lab` (aioquic) tools. You will see the frames, the stream IDs, the trailers and the handshake packets directly.

## What You Will Build Toward

- Explain where HTTP/1.1 loses time (one request at a time per connection, six connections per host) and measure it against HTTP/2 and HTTP/3
- Read HTTP/2 framing: streams, SETTINGS, HEADERS, DATA, WINDOW_UPDATE and trailers, in `nghttp` output and in a capture decrypted with a key log
- Quantify HPACK, and know what replaced server push (103 Early Hints) and the old priority tree (RFC 9218)
- Explain QUIC's handshake (1 RTT, 0-RTT with resumption), its stream independence, and the caveat that QPACK's dynamic table brings back some head-of-line blocking — and measure all three
- Read a gRPC call on the wire: an HTTP/2 POST, length-prefixed protobuf, the status in trailers; explain why a failed call can be HTTP 200, how deadlines propagate, and why HTTP/1.1 hops break gRPC
- Read the WebSocket upgrade and frames, and explain what masking is for (proxy cache poisoning, not secrecy), idle timeouts and pings, and when SSE is the better tool

---

## 15.1 HTTP/1.1 — The Baseline

HTTP/1.1 (RFC 9112 for the message syntax, RFC 9110 for the semantics shared by all versions) is text: a request line, header lines, a blank line and an optional body, on a TCP connection that persists by default (keep-alive). On one connection, requests are answered **one at a time, in order**. Pipelining (sending the next request before the previous response arrives) is in the standard, but responses still come back in order, and browsers never enabled it. So a client that wants parallelism opens more connections: browsers allow **six per host name**.

The lab requests a small page and then 20 resources of 2 KB each across a 50 ms path, all at once, the way a browser does after parsing a page:

| Protocol | Connections | Total time (3 runs) |
| --- | --- | --- |
| HTTP/1.1, one connection | 1 | 1187, 1195, 1191 ms |
| HTTP/1.1, six connections (a browser) | 6 | 447, 420, 425 ms |
| HTTP/2 | 1 | 289, 287, 289 ms |
| HTTP/3 | 1 QUIC | 246, 254, 251 ms |

On one HTTP/1.1 connection, 20 requests cost 20 round trips after the handshakes. Six connections cost six TCP and TLS handshakes and still need about four rounds of requests. HTTP/2 sends all 20 requests at once on one connection. What remains is TCP slow start: 40 KB does not fit in the first congestion window (Module 14). HTTP/3 saves one handshake round trip. At 150 ms RTT (by hand, Part 2), the same page took 1223 ms over HTTP/1.1 with six connections, 833 ms over HTTP/2 and 677 ms over HTTP/3.

**Head-of-line blocking at the application layer.** On one HTTP/1.1 connection, a fast request issued behind a slow one (1 s of server time) must wait for it. Measured: the fast response arrived at **1262, 1214 and 1211 ms** over HTTP/1.1, and at **159, 165 and 158 ms** over HTTP/2 on one connection.

The workarounds of the HTTP/1.1 era are now mostly harmful under HTTP/2:

- **Domain sharding** (`cdn1.`, `cdn2.`) means more connections, more handshakes and lost coalescing.
- **Spriting and inlining** waste caching.

Bundling still helps compression, but is no longer needed for parallelism.

---

## 15.2 HTTP/2 — Streams on One TCP Connection

HTTP/2 (RFC 9113, which obsoletes RFC 7540) keeps HTTP's semantics but replaces the text format with **binary frames**. Every frame starts with a 9-byte header:

```
+-----------------------------------------------+
| Length (24 bits)                              |
+---------------+---------------+---------------+
| Type (8)      | Flags (8)     |
+-+-------------+---------------+-------------------------------+
|R| Stream Identifier (31 bits)                                 |
+=+=============================================================+
| Frame Payload (Length bytes)                                  |
+---------------------------------------------------------------+

Types:   DATA 0x0, HEADERS 0x1, PRIORITY 0x2 (deprecated), RST_STREAM 0x3, SETTINGS 0x4,
         PUSH_PROMISE 0x5, PING 0x6, GOAWAY 0x7, WINDOW_UPDATE 0x8, CONTINUATION 0x9
Flags:   END_STREAM 0x1, END_HEADERS 0x4, PADDED 0x8, PRIORITY 0x20
```

How the pieces fit together:

- A **stream** is one request and its response.
- Client-initiated streams have odd IDs (1, 3, 5, …).
- Stream 0 is the connection itself, and carries SETTINGS, PING, GOAWAY and connection-level WINDOW_UPDATE.
- Frames of different streams interleave freely on the connection.

`nghttp -nv` prints the frames of one request (from the lab):

```
send SETTINGS frame <length=12, flags=0x00, stream_id=0>
     [SETTINGS_MAX_CONCURRENT_STREAMS(0x03):100] [SETTINGS_INITIAL_WINDOW_SIZE(0x04):65535]
send HEADERS frame <length=45, flags=0x25, stream_id=13>      ; END_STREAM | END_HEADERS | PRIORITY
     :method: GET  :path: /obj?size=100  :scheme: https  :authority: web.test
recv SETTINGS frame <length=30, flags=0x00, stream_id=0>
     [SETTINGS_MAX_FRAME_SIZE(0x05):1048576] [SETTINGS_MAX_CONCURRENT_STREAMS(0x03):250] …
recv HEADERS frame <length=70, flags=0x04, stream_id=13>      ; :status: 200, alt-svc, content-type …
recv DATA frame <length=100, flags=0x01, stream_id=13>        ; END_STREAM
send GOAWAY frame <length=8, flags=0x00, stream_id=0>
```

The request line and the `Host` header are now **pseudo-headers**: `:method`, `:path`, `:scheme` and `:authority`. The response status is `:status`.

nghttp's request used stream 13 because nghttp2 first sent PRIORITY frames for streams 3–11, building a dependency tree. That tree is the original RFC 7540 priority scheme. RFC 9113 deprecates it, and RFC 9218 (Extensible Prioritization) replaces it with a simple `priority: u=N, i` header that works for HTTP/3 as well.

**Multiplexing, seen in a capture.** The lab downloads a page and six 20 KB resources on one connection, with the client writing its TLS secrets to a key log, and tshark decrypts the capture (Module 13). The result was one TCP connection, streams 1 3 5 7 9 11 13, and 3–4 packets per run that carry frames of several streams at once. One packet held the HEADERS of stream 11 and DATA for streams 3, 5 and 7.

**Flow control.** Each stream and the connection as a whole have a receive window (initially 65,535 bytes, changed by `SETTINGS_INITIAL_WINDOW_SIZE`). The receiver opens the window with WINDOW_UPDATE frames. This is separate from TCP's window. A slow consumer stalls its own stream without stopping the others, until the connection window also fills.

**Negotiation.**

- **Over TLS**, HTTP/2 is chosen by ALPN in the handshake. The lab's `curl -v` shows `ALPN: curl offers h2,http/1.1` and then `ALPN: server accepted h2`.
- **In cleartext ("h2c")** there is no ALPN, so a client must know in advance. `curl --http2-prior-knowledge` sends the HTTP/2 connection preface directly; gRPC clients do the same. The lab server answered `proto HTTP/1.1` to plain curl and `proto HTTP/2.0` with prior knowledge. RFC 9113 deprecates the old `Upgrade: h2c` route.
- **When HTTP/2 doesn't happen**, it is usually because ALPN was not offered or not accepted. Typical causes are an old client library, a TLS-terminating proxy configured for HTTP/1.1, or a missing `h2` in the server's ALPN list. In `curl -v`, look for "server accepted".

**HPACK** (RFC 7541) compresses headers with three parts:

- a 61-entry static table (`:method: GET` is index 2, `:status: 200` is index 8);
- a per-connection dynamic table of fields already sent;
- Huffman coding of literal values.

Measured with `h2load`, 100 responses carried 588 bytes of headers over HTTP/2 ("space savings 95.14%"). Over HTTP/1.1, the same 100 responses carried 11,100 bytes, 111 bytes each. On the request side, nghttp's second request on a connection had a HEADERS frame of 23 bytes, against 45 for the first. HPACK was designed after the CRIME attack on DEFLATE-compressed headers: it compresses whole repeated fields rather than arbitrary byte strings, and sensitive values such as `authorization` and short cookies should be sent as never-indexed literals so they never enter the table.

**Server push is gone.**

- **What it was:** PUSH_PROMISE let a server send resources before they were requested.
- **Why it failed:** a server rarely knows what the client already has cached, so pushes wasted bandwidth.
- **Removal:** Chrome disabled push by default in Chrome 106 (2022).
- **Replacement:** **103 Early Hints** (RFC 8297), an interim response carrying `Link: …; rel=preload` headers, so the browser can start fetching while the server is still preparing the page.

**TCP head-of-line blocking.** All streams share TCP's single ordered byte stream. When one TCP segment is lost, the segments after it are held by the kernel until the retransmission arrives, whichever streams they belong to. The lab drops exactly one packet: the first packet of the resources' responses (`pathlab set drop_big=4`). Every one of the ten responses then completed together at 266–276 ms, against 213–280 ms without the drop, and **none completed before the retransmission arrived**. 15.3 has the comparison with HTTP/3.

**Connection management.**

- **GOAWAY** tells the peer to finish what it has and use a new connection. Servers send it before restarts. AWS ALB sends it after 10,000 requests on one HTTP/2 connection.
- **Connection coalescing:** one HTTP/2 connection can serve several host names if the certificate covers them and they resolve to the same address.
- **`MAX_CONCURRENT_STREAMS`** (100 by nghttp2's default, 250 by Go's, 128 on AWS ALB) caps how many requests run at once on a connection.

---

## 15.3 HTTP/3 and QUIC

HTTP/3 (RFC 9114) maps HTTP onto **QUIC** (RFC 9000):

- QUIC is a transport over UDP that provides streams, reliability, flow control and congestion control (RFC 9002).
- It integrates TLS 1.3 (RFC 9001), so nearly everything after the first packets is encrypted, including most of the transport headers.
- Header compression is **QPACK** (RFC 9204), HPACK redesigned for streams that can arrive out of order.

```
  HTTP/1.1, HTTP/2           HTTP/3
  ┌──────────────┐           ┌──────────────┐
  │ HTTP         │           │ HTTP/3       │  frames, QPACK
  ├──────────────┤           ├──────────────┤
  │ TLS          │           │ QUIC         │  streams, loss recovery, congestion control,
  ├──────────────┤           │  + TLS 1.3   │  connection IDs; TLS carried in CRYPTO frames
  │ TCP (kernel) │           ├──────────────┤
  ├──────────────┤           │ UDP          │  QUIC itself runs in user space (the application or a library)
  │ IP           │           ├──────────────┤
  └──────────────┘           │ IP           │
                             └──────────────┘
```

### Handshakes, measured

| Connection | Round trips before the response can start | First byte, measured (3 runs) |
| --- | --- | --- |
| HTTP/2: TCP + TLS 1.3 + request | 3 | 161, 159, 161 ms (TLS done at ~108 ms) |
| HTTP/3, new connection | 2 (QUIC + TLS in one, then the request) | 121, 123, 124 ms |
| HTTP/3, resumed (session ticket) | 2 | 119, 119, 119 ms |
| HTTP/3, resumed with **0-RTT** | 1 (the request rides in the first flight) | 65, 65, 71 ms |

The HTTP/3 times include 10–20 ms of Python processing in aioquic. On the wire, a capture of the new connection showed the server's handshake reply at 53 ms and the response at 110 ms.

The 0-RTT capture shows what makes it possible:

```
     0.0 ms  198.18.110.10    1208 bytes  Initial 0-RTT        ← ClientHello and the encrypted request, one datagram
    54.0 ms  198.18.111.10    1208 bytes  Initial Handshake    ← server's handshake …
    54.0 ms  198.18.111.10    1208 bytes  1-RTT (short header) ← … and already the response
```

Several details in that capture matter:

- **Initial datagrams are padded to at least 1200 bytes** (1208 here, including the 8-byte UDP header). Before the client's address is validated, a server may send at most three times what it received. Together these stop QUIC from being used for amplification attacks, and mean a path must carry 1200-byte UDP datagrams.
- **Resumption alone saves no round trip here.** QUIC already does the transport and TLS handshake together in one round trip. Its value is skipping the certificate and enabling 0-RTT.
- **0-RTT data can be replayed** by an attacker who records it. Servers must accept only requests that are safe to repeat. RFC 8470 defines the `Early-Data: 1` header for servers behind a proxy, and the `425 Too Early` response to make a client retry after the handshake. TLS 1.3 over TCP has the same 0-RTT feature with the same caveat.

### Streams, loss, and QPACK's caveat

Each QUIC stream is delivered in order on its own, so a lost packet blocks only the streams whose data it carried. The lab repeats the single-packet drop from 15.2 over HTTP/3 (ten 3 KB resources after a page, one run shown; the others match):

| Ten resources after a page | Without the drop | One packet dropped |
| --- | --- | --- |
| HTTP/2 | 215 220 223 226 230 265 269 270 274 280 ms | **266 266 266 266 266 266 266 266 266 269 ms** — all wait |
| HTTP/3, QPACK dynamic table (default) | 178 181 183 183 231 … 236 ms | **233 233 233 233 233** 251 286 294 297 297 ms — all wait |
| HTTP/3, no QPACK dynamic table | 180 181 182 182 233 … 242 ms | **185 185 185** 238 263 263 301 309 309 309 ms — three unaffected |

Across the three runs:

- **HTTP/2:** nothing completed before the retransmission.
- **HTTP/3 with QPACK's dynamic table:** the same. A decrypted capture shows why. The lost packet carried an instruction on the server's **QPACK encoder stream** (stream 7) that adds a header field to the dynamic table, and every response's header block referred to that entry. So every stream had to wait for the encoder stream to be repaired. This is head-of-line blocking that QPACK can bring back. RFC 9204 lets the decoder bound it with `SETTINGS_QPACK_BLOCKED_STREAMS`, and an encoder can avoid it by not referencing entries the peer has not yet acknowledged.
- **HTTP/3 with the dynamic table set to 0** (`h3lab get --qpack-table 0`): the three resources whose packets arrived completed at 180–186 ms, as if nothing had been lost. Only the stream whose packet was lost waited (234–238 ms).
- **Afterwards, in every case:** the remaining resources were slower than without the drop, because congestion control is shared by the whole connection and backs off after a loss.

So HTTP/3 removes *transport* head-of-line blocking between streams. It does not remove loss or its effect on throughput, and how much it helps depends on the implementation. For bulk transfers under random loss, the Python QUIC stack in this lab was slower than Linux TCP (tested by hand; not part of the checks). Module 14's lesson about loss still applies.

### Connection IDs and migration

TCP identifies a connection by its 4-tuple. QUIC packets carry **connection IDs** instead, so a connection can survive a change of client address (Wi-Fi to cellular, or a NAT rebinding). The endpoint validates the new path before trusting it. Servers behind load balancers need connection-ID-aware routing (QUIC-LB) for migration to reach the same backend. Clients may disable migration, and servers may not support it.

### Discovery and deployment

- **Discovery.** A client learns that a server speaks HTTP/3 from an `Alt-Svc: h3=":443"; ma=3600` response header (the lab server sends one), or from a DNS **HTTPS record** (RFC 9460) before the first connection. Browsers then race or try QUIC, and fall back to TCP.
- **UDP blocked.** Many enterprise firewalls block or rate-limit UDP 443. In the lab, with UDP 443 dropped on the path, `h3lab` reported `no QUIC connection after 3 s`, while HTTP/2 over TCP to the same server still answered. This is the "works in the browser, not with my HTTP/3-only client" ticket. Browsers hide it by falling back.
- **Visibility.** QUIC encrypts almost everything, so middleboxes and `tcpdump` see little. Debugging needs endpoint tools: a TLS key log (`SSLKEYLOGFILE`, `h3lab --keylog`), which tshark can use to decrypt QUIC as it does TLS, and **qlog** traces from the QUIC library.
- **Tooling.** Ubuntu 24.04's curl 8.5 is built without HTTP/3 (`curl -V` lists no `HTTP3` feature). `--http3` needs a curl built with a QUIC library. Check `curl -V`, not the version number.
- **CPU cost.** QUIC runs in user space with per-packet encryption. Linux helps with UDP GSO/GRO, but QUIC usually costs more CPU per byte than kernel TCP with TLS offload.

---

## 15.4 gRPC

gRPC is a remote-procedure-call framework. Services and messages are defined in a `.proto` file, client stubs and server interfaces are generated from it, and the transport is **HTTP/2 with a few conventions**. The course's service ([`labs/grpc/echo.proto`](../labs/grpc/echo.proto)) has a unary `Echo`, a `Slow` call (2 s of work) and a server-streaming `Watch`. The lab's `weblab` implements the server and a client by hand on Go's HTTP/2, so you can see that there is nothing else underneath. The standard `grpcurl` talks to it unchanged.

### On the wire

```
> POST /course.Echo/Echo  (HTTP/2 HEADERS)
>   content-type: application/grpc
>   te: trailers
> DATA 00 00 00 00 07 0a 05 68 65 6c 6c 6f   (flag 0, length 7, then the protobuf bytes)
< HTTP/2.0 200  (HEADERS)
<   content-type: application/grpc
< DATA message "hello"
< TRAILERS grpc-status: 0
status: OK (0)
```

Reading the exchange:

- **The method is the path:** `/package.Service/Method`, always a POST.
- **Each message** is a 1-byte compressed flag, a 4-byte big-endian length, and the protobuf bytes. Here the protobuf is `0a 05 68 65 6c 6c 6f`: field 1, length-delimited, 5 bytes, "hello".
- **The result comes in the trailers**, an HTTP/2 HEADERS frame sent after the DATA with END_STREAM set. nghttp shows it as `recv HEADERS frame <length=12, flags=0x05>` carrying `grpc-status: 0`. That is why gRPC needs `te: trailers`, and why it needs HTTP/2 end to end: HTTP/1.1 trailers exist but are rarely supported by proxies.
- **An error before any message** is sent as a *trailers-only* response: HTTP 200 with `grpc-status` in the headers and no body. In the lab, an empty value gave `HTTP/2.0 200` with `grpc-status: 3` (INVALID_ARGUMENT), and an unknown method gave `HTTP/2 200` with `grpc-status: 12` (UNIMPLEMENTED) when inspected with plain curl.

**HTTP 200 does not mean the call succeeded.** Access logs and load-balancer metrics that count only HTTP status codes report failed RPCs as successes. Monitor `grpc-status`.

Protobuf's compactness, for the classic example:

```
JSON:      {"user_id": "abc123", "name": "Alice", "email": "alice@example.com", "age": 30}   79 bytes (72 minified)
protobuf:  0a 06 "abc123" 12 05 "Alice" 1a 11 "alice@example.com" 20 1e                       36 bytes
```

Field names are replaced by field numbers, and numbers are varints (30 is the single byte `1e`). The schema is required to read the bytes at all. That is the trade: compact, typed and evolvable when you keep field numbers stable, but not self-describing.

### Status codes

| Code | Name | Typical meaning for support |
| --- | --- | --- |
| 0 | OK | Success |
| 1 | CANCELLED | The client gave up (it reset the stream) |
| 2 | UNKNOWN | Includes **non-gRPC responses** with unmapped HTTP statuses (e.g. 464, 505) |
| 3 | INVALID_ARGUMENT | The request was rejected by validation |
| 4 | DEADLINE_EXCEEDED | The deadline passed (client or server side) |
| 5 / 6 / 7 | NOT_FOUND / ALREADY_EXISTS / PERMISSION_DENIED | Application results |
| 8 | RESOURCE_EXHAUSTED | Quota, rate limit, or **message larger than the receive limit** (grpc-go default 4 MiB) |
| 12 | UNIMPLEMENTED | Unknown service or method — or an HTTP 404 from a proxy |
| 13 | INTERNAL | Includes an HTTP 400 from a proxy |
| 14 | UNAVAILABLE | Connection failure, or HTTP 429/502/503/504 from a proxy — usually retryable |
| 16 | UNAUTHENTICATED | Missing or bad credentials (HTTP 401 maps here) |

When the response is not gRPC at all, the client maps the HTTP status:

| HTTP status | gRPC status |
| --- | --- |
| 400 | INTERNAL |
| 401 | UNAUTHENTICATED |
| 403 | PERMISSION_DENIED |
| 404 | UNIMPLEMENTED |
| 429, 502, 503, 504 | UNAVAILABLE |
| anything else | UNKNOWN |

So the status a user reports often describes the proxy in front of the service, not the service.

### Deadlines, cancellation, streaming

- **Deadlines.** A client deadline travels as `grpc-timeout`. `grpcurl -max-time 0.5` sent `grpc-timeout: 395090u`, which is what was *left* of 500 ms after connecting. The lab server stopped its work at 395–396 ms in every run. Sometimes that was because its own copy of the deadline expired. Other times it was because the client's RST_STREAM arrived first. Either way, the work stopped, and that is the point: a service should pass the remaining deadline on to every call it makes, and stop when cancelled. Retries need a total budget and must be limited to idempotent calls.
- **Streaming.** Unary, server streaming, client streaming and bidirectional streaming calls are all one HTTP/2 stream with several messages. `Watch` sent five messages 300 ms apart (+104 ms … +1.307 s), then the trailers. Long streams are subject to every idle timeout between the client and the server.
- **Reflection** is an optional service (`grpc.reflection.v1`). Without it, `grpcurl list` fails ("server does not support the reflection API"), but every call works with `-proto`. Health checking is another standard service (`grpc.health.v1.Health/Check`), and load balancers can be configured to call it.

### gRPC through proxies and load balancers

- **Every hop must speak HTTP/2 and pass trailers.** In the lab, a proxy that forwards over HTTP/1.1 turned every call into `Code: Unknown`, "unexpected HTTP status code received from server: 505 (HTTP Version Not Supported)". 505 is what grpc-go's HTTP handler answers to HTTP/1.x, and the same proxy forwarding over h2c worked. On AWS ALB, a gRPC request to a target group whose protocol version is HTTP1 is answered by the load balancer itself with **HTTP 464** ("incoming request protocol … incompatible with the version config of the target group protocol"). A gRPC client reports that as UNKNOWN too.
- **Connection-level load balancing does not balance gRPC.** A gRPC client keeps one long-lived HTTP/2 connection and multiplexes every call on it, so an L4 load balancer (or a Kubernetes ClusterIP Service) picks a backend once, and all calls go there. Balance per request with an L7 proxy (Envoy, a mesh, ALB with the gRPC protocol version) or client-side load balancing (a headless Service plus the client's `round_robin` policy). Use `MAX_CONNECTION_AGE` on servers to rebalance.
- **Browsers cannot make native gRPC calls**: they don't expose HTTP/2 framing or trailers to JavaScript. gRPC-Web, and protocols like Connect, translate.

---

## 15.5 WebSockets

A WebSocket (RFC 6455) starts as an HTTP/1.1 request that asks to switch protocols, then becomes a full-duplex stream of frames in both directions. From the lab:

```
> GET /ws HTTP/1.1
> Upgrade: websocket
> Connection: Upgrade
> Sec-WebSocket-Key: URU/T948tvlpnTV8DJwp5g==
> Sec-WebSocket-Version: 13
< 101 Switching Protocols
< Sec-Websocket-Accept: 5JNVnmb8Dhw/56cicPgD7iaXY28=
  accept checks out: base64(sha1(key + 258EAFA5-E914-47DA-95CA-C5AB0DC85B11))
> text "hello": 11 bytes on the wire (2 header + 4 mask + 5 payload): 81 85 df 48 68 5a b7 2d 04 36 b0
< text "hello": 7 bytes on the wire, masked=false
< close 1000 (normal closure)
```

Reading the exchange:

- **The handshake.** `Sec-WebSocket-Accept` is the SHA-1 of the client's key plus a fixed GUID. `weblab accept dGhlIHNhbXBsZSBub25jZQ==` reproduces RFC 6455's own example, `s3pPLMBiTxaQ9kYGzzhZRbK+xOo=`. It proves that the server understood the handshake. It is not authentication. Authenticate with a cookie, a token or the `Origin` check, as with any HTTP request.
- **The frame header.** A frame header is 2 bytes for payloads up to 125 bytes, 4 bytes up to 65,535, and 10 beyond, plus 4 bytes of masking key on frames from the client. `81 85` decodes as FIN set, opcode 1 (text), MASK bit set, length 5. The opcodes are 0x1 text, 0x2 binary, 0x8 close, 0x9 ping, 0xA pong.
- **Masking** (client to server only) is a random XOR key per frame. Its purpose is to stop a malicious page from crafting bytes that look like HTTP to a transparent proxy in the path, and so poisoning its cache. It is not encryption. Use `wss://` (TLS) for confidentiality.
- **Only HTTP/1.1 upgrades.** The lab's server answered a WebSocket request over HTTP/2 with `426` and `Sec-WebSocket-Version: 13`. WebSockets over HTTP/2 and HTTP/3 use extended CONNECT (RFC 8441, RFC 9220), which clients and servers must both support. Most deployments still upgrade over HTTP/1.1, so a TLS terminator must offer `http/1.1` in ALPN to WebSocket clients.
- **Idle timeouts close quiet connections.** Load balancers and proxies close connections that carry no data for a while: AWS ALB after 60 s by default (and TCP keepalives do not count), nginx `proxy_read_timeout` 60 s. In the lab, a relay with a 5 s idle timeout cut a quiet WebSocket **after 5 s**. With a ping every 2 s, the same connection was **still open after 8 s**. The fix is an application-level ping or heartbeat shorter than the shortest idle timeout on the path, or a longer timeout where you control it. Clients must reconnect anyway, because networks change.

---

## 15.6 Server-Sent Events and Polling

**Server-sent events** (the HTML standard's `EventSource`) are an ordinary HTTP response with `Content-Type: text/event-stream`, kept open and written to in pieces:

```
id: 1
event: tick
data: {"n":1}

```

The browser reconnects automatically after a drop, sending `Last-Event-ID` with the last `id` it saw, so the server can resume. Measured in the lab:

- **The stream itself:** events arrived 500 ms apart (+157, +659, +1160 ms).
- **Resuming:** reconnecting with `Last-Event-ID: 3` resumed at ids 4 and 5.
- **Through a proxy that buffers responses:** all three events arrived together at +1106 ms, when the stream ended. A buffering proxy (nginx's default `proxy_buffering on`, some CDNs, compression middleware) holds the response until it has enough, so real-time updates arrive in bursts or at the end. Disable buffering for the stream: `X-Accel-Buffering: no` for nginx, `proxy_buffering off`, or the CDN's streaming setting.

The other options and their limits:

- **SSE is one direction only** (server to client) and text only. Over HTTP/1.1, each open stream holds one of the browser's six connections per host. Over HTTP/2, streams share one connection.
- **Long polling** (the server holds a request until there is news) works through anything. It costs a request per event and needs care with timeouts.
- **Plain polling** trades latency against wasted requests.
- **WebTransport** (over HTTP/3) is the emerging option for bidirectional streams and datagrams in browsers.

| Need | Use |
| --- | --- |
| Server pushes updates (notifications, dashboards, progress) | SSE — plain HTTP, auto-reconnect, resumable |
| Both sides send at any time (chat, collaboration, games) | WebSocket |
| Service-to-service calls with schemas, streaming, deadlines | gRPC |
| Public API that browsers and every tool can call | HTTP + JSON (REST), over HTTP/2 or HTTP/3 |
| Infrequent updates, or very restrictive networks | Polling or long polling |

---

## Hands-On Lab: HTTP/2, HTTP/3, gRPC and WebSockets

### Lab Goals

- Compare a page load over HTTP/1.1 (one and six connections), HTTP/2 and HTTP/3 on a 50 ms path
- See HTTP/2 frames, multiplexed streams in a decrypted capture, and HPACK's savings
- Measure QUIC's handshake, resumption and 0-RTT, and capture the 0-RTT flight
- Drop exactly one packet and compare HTTP/2, HTTP/3 and HTTP/3 without QPACK's dynamic table
- Read gRPC on the wire: trailers, status codes in HTTP 200, deadlines, streaming, and an HTTP/1.1 proxy breaking it
- See the WebSocket handshake and frames, a load-balancer idle timeout, and SSE through a buffering proxy

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), with the Go toolbox built (`cd labs/go && go build -o bin/ ./cmd/...`)
- The course Python environment with aioquic: `python3 -m venv .venv && .venv/bin/pip install -r labs/requirements.txt` (from `networking/`)
- `curl`, `tcpdump`, `tshark`, `iptables`, `nghttp2-client` (for `nghttp` and `h2load`), and `grpcurl` 1.9+ from its [GitHub releases](https://github.com/fullstorydev/grpcurl/releases)

The tools:

- [`weblab`](../labs/go/cmd/weblab) serves HTTPS (HTTP/1.1 and HTTP/2 by ALPN) on port 443 and cleartext HTTP/1.1 plus h2c on port 80, with `/obj?size=&delay=`, `/events`, `/ws` and the gRPC service. Its clients are `get`, `grpc`, `ws` and `sse`. `proxy` and `relay` play the load balancers.
- [`h3lab.py`](../labs/http3/h3lab.py) is the HTTP/3 server and client (aioquic; Go's standard library has no QUIC). It listens on UDP 443 with the same certificate.
- The path is Module 14's [`pathlab`](../labs/go/cmd/pathlab), with 25 ms each way. `pathlab set drop_big=N` drops exactly the N-th packet of 1000 bytes or more travelling towards the client.

---

### Lab Part 1: Run the Web Lab

[`web.json`](../labs/netlab/web.json):

```
  client 198.18.110.10 ── router (pathlab: 25 ms each way) ── server 198.18.111.10 (web.test)
                                                               weblab :443 (h2, http/1.1) :80 (http/1.1, h2c)
                                                               h3lab udp/443 (HTTP/3)
                                                               proxy-h1 :8080 · proxy-h2c :8083 · proxy-buffer :8082
                                                               relay :8081 (5 s idle timeout)
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/web.json        # about a minute
```

The tables and outputs in 15.1–15.6 come from three runs. The evidence directory has each check's output:

- `page-load.txt`, `hol-h1.txt`;
- `h2-mux.pcap` with `client-keys.log`, and `h2-mux.txt`;
- `hpack.txt`, `nghttp.txt`;
- `h3-handshakes.txt`, `h3-0rtt.pcap`, `h3-0rtt.txt`;
- `hol-loss.txt`, `udp-blocked.txt`;
- `grpc-*.txt`, `ws*.txt`, `sse*.txt`;
- the service logs, in which the server logs every request, gRPC call and WebSocket frame.

---

### Lab Part 2: Experiment By Hand

```bash
sudo ./bin/netlab up ../netlab/web.json
alias lab='sudo ./bin/netlab exec ../netlab/web.json'
EV=$(ls -dt /tmp/netlab-web-* | head -1)        # evidence directory: ca.pem, keys, logs
```

Unset `HTTPS_PROXY`/`HTTP_PROXY` in client commands if your shell sets them. grpcurl honours them too.

1. **HPACK on the request side.** `lab client -- nghttp -nv https://web.test/obj?size=100 https://web.test/obj?size=200 | grep 'send HEADERS'`. Two requests on one connection gave HEADERS frames of 45 and 23 bytes: the second request reuses the dynamic table.
2. **Make the path longer.** `lab router -- ./bin/pathlab set delay=75ms` (150 ms RTT), then repeat the page load: `lab client -- ./bin/weblab get -page -cacert $EV/ca.pem -proto h1 -conns 6 -n 21 'https://web.test/obj?size=2000'`, then `-proto h2`, and `../../.venv/bin/python ../http3/h3lab.py get --page --cacert $EV/ca.pem -n 21 'https://web.test/obj?size=2000'`. Measured: 1223, 833 and 677 ms. Every saved round trip is now worth 150 ms. Set `delay=25ms` again afterwards.
3. **A gRPC call without a gRPC client.** Build the request bytes yourself and read the response frames:
   ```bash
   lab client -- sh -c "printf '\000\000\000\000\007\012\005hello' > /tmp/req.bin; nghttp -v -d /tmp/req.bin \
     -H 'content-type: application/grpc' -H 'te: trailers' http://web.test/course.Echo/Echo" | grep -a -E 'recv|grpc'
   ```
   Measured: `recv HEADERS` (`:status: 200`), `recv DATA frame <length=12>` (the 5-byte prefix plus 7 bytes of protobuf), then `grpc-status: 0` in `recv HEADERS frame <length=12, flags=0x05>`: the trailers, with END_STREAM. Break the framing and read what comes back: a prefix claiming 9 bytes (`\011`) gave `grpc-status: 13` with `grpc-message: reading request: unexpected EOF`; a protobuf length of 7 for a 5-byte string (`\007hello`) gave `13` with `decoding request: bad length`. Both still arrived as HTTP 200.
4. **WebSocket by curl.** `lab client -- curl -si --http1.1 -m 2 -H 'Connection: Upgrade' -H 'Upgrade: websocket' -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' http://web.test/ws` returns `101 Switching Protocols` with RFC 6455's example accept value. Then `curl -si --http2 --cacert $EV/ca.pem https://web.test/ws` returns `HTTP/2 426` with `sec-websocket-version: 13`: no upgrade over HTTP/2.
5. **Find the QPACK encoder stream.** Drop the first resource packet again and decrypt the QUIC capture:
   ```bash
   lab router -- ./bin/pathlab set drop_big=4
   lab client -- timeout 4 tcpdump -i eth0 -nn -U udp port 443 -w /tmp/q.pcap &
   lab client -- ../../.venv/bin/python ../http3/h3lab.py get --keylog /tmp/qkeys.log --page --cacert $EV/ca.pem -n 11 'https://web.test/obj?size=3000'
   tshark -r /tmp/q.pcap -o tls.keylog_file:/tmp/qkeys.log -Y 'ip.src==198.18.111.10 && quic.stream.stream_id' -T fields -e frame.time_relative -e quic.stream.stream_id
   ```
   The first server packets carry streams 3, 7 and 11: the server's control stream, QPACK encoder stream and QPACK decoder stream (all server-initiated unidirectional streams). Streams 0, 4, 8 … are the requests. Find stream 7 again later in the list: that is the retransmitted encoder instruction that every response was waiting for.

`sudo ./bin/netlab down ../netlab/web.json` removes the lab.

---

### Lab Part 3: Your Own Machine

```bash
curl -sv -o /dev/null https://www.cloudflare.com 2>&1 | grep -iE 'ALPN|^< HTTP|alt-svc'   # h2, and an Alt-Svc for h3
curl -V | grep -o HTTP3 || echo "this curl has no HTTP/3"                                 # then: curl --http3-only -v …
dig +short HTTPS cloudflare.com                                                            # HTTP/3 advertised in DNS (RFC 9460)
nghttp -nv https://www.cloudflare.com | grep -E 'SETTINGS_|recv HEADERS|recv DATA' | head
```

In a browser's developer tools (Network tab, Protocol column) you can see h2 and h3 per request. Chrome's `chrome://net-export` records a session for detailed analysis.

---

## Real-World Troubleshooting Scenario

### Scenario: "gRPC Calls Fail Through the Load Balancer"

**The situation.** A team moved service-to-service calls from REST to gRPC.

- Direct calls inside the cluster work.
- Calls through the new AWS Application Load Balancer fail with `Code: Unknown`, and the ALB's metrics show 4XX errors.
- The REST endpoints on the same ALB work, and the targets are healthy.

**Step 1 — Is the client reaching a gRPC server at all?** Check what grpcurl actually got back:

```bash
grpcurl -proto user.proto -d '{"id":"test"}' api.example.com:443 user.UserService/GetUser
# ERROR: Code: Unknown  Message: unexpected HTTP status code received from server: 464 …
```

The real answer is in the message. The status that came back was an HTTP status, so a gRPC server never answered: the load balancer did. HTTP 464 is the ALB's own response to a request whose protocol does not match the target group's protocol version, and gRPC maps unknown HTTP statuses to UNKNOWN.

**Step 2 — Check the hop.**

```
Target group:   protocol HTTP (or HTTPS), protocol version HTTP1     ← gRPC needs "gRPC" here
Listener:       HTTPS (required for gRPC and HTTP/2 to targets)
Health check:   HTTP GET /healthz                                     ← must become a gRPC health method
```

**The fix.**

1. Create a target group with protocol version **gRPC**. The protocol version is fixed per target group, so this is a new group.
2. Set its health check to a gRPC method, such as `/grpc.health.v1.Health/Check`, with gRPC success codes (`0`).
3. Point the listener rule for the gRPC paths at the new group.

Afterwards, remember that the ALB counts **HTTP** status codes: a gRPC call that fails with `grpc-status: 14` is still an HTTP 200 to the ALB. Alert on gRPC status from the services or the mesh.

**The lab version.** `weblab proxy -backend h1` is the same mistake in miniature. grpcurl reported `Code: Unknown … 505 (HTTP Version Not Supported)`, and `-backend h2c` fixed it. The clue in both is the HTTP status inside a gRPC error message.

**Related symptom.** After the fix, one backend pod gets most of the traffic. gRPC's long-lived connection was balanced once, at connect time. The fix is per-request balancing (the ALB with protocol version gRPC already does this), or `MAX_CONNECTION_AGE` on the servers.

---

## Common Failure Patterns

| Symptom | Cause | How to confirm | Fix |
| --- | --- | --- | --- |
| HTTP/2 never used | ALPN not offered or not accepted (old client, TLS terminator configured for HTTP/1.1) | `curl -v`: "ALPN: server accepted http/1.1" | Enable h2 at the TLS terminator; check client library |
| Cleartext HTTP/2 client fails against a server | Server does not support h2c (prior knowledge) | `curl --http2-prior-knowledge http://…` | Enable h2c, or use TLS |
| One slow response delays others | HTTP/1.1 head-of-line blocking | Timing per request on one connection | HTTP/2; more connections as a stopgap |
| Everything stalls together on a lossy path over HTTP/2 | TCP head-of-line blocking | Capture: retransmission, then all streams' data at once | Fix loss; HTTP/3 where possible |
| HTTP/3 never used, or HTTP/3-only clients time out | UDP 443 blocked or rate-limited | No UDP replies in a capture; `Alt-Svc` present | Allow UDP 443, or accept TCP fallback |
| gRPC `Unknown`/`Internal`/`Unimplemented` with an HTTP status in the message | A proxy or load balancer answered, not the service (HTTP/1.1 hop, wrong route) | The HTTP status in the error text; LB logs (ALB 464) | HTTP/2 or gRPC protocol version end to end; correct routing |
| gRPC errors invisible in dashboards | Status is in trailers; HTTP status is 200 | `grpc-status` in logs or traces | Monitor gRPC status codes |
| `DEADLINE_EXCEEDED` deep in a call chain | Deadline not propagated, or retries exceed the budget | `grpc-timeout` on each hop | Propagate remaining deadline; bounded retries |
| `RESOURCE_EXHAUSTED` on large responses | Message exceeds receive limit (4 MiB default in grpc-go) | Error text names the size | Raise the limit deliberately, or stream/paginate |
| One gRPC backend overloaded | Connection-level balancing of long-lived HTTP/2 | Per-pod request counts | L7 or client-side load balancing; `MAX_CONNECTION_AGE` |
| WebSockets drop after exactly N seconds idle | Proxy or LB idle timeout (ALB 60 s default) | Close at a fixed interval after the last message | Application ping/heartbeat shorter than the timeout; raise timeout |
| WebSocket upgrade fails behind a proxy | Proxy strips `Upgrade`/`Connection`, or speaks HTTP/2 only to the client | 400/426 instead of 101 | Configure upgrade forwarding; offer `http/1.1` in ALPN |
| SSE events arrive in bursts or only at the end | Buffering proxy, CDN or compression | Event timestamps vs arrival times | Disable buffering for the stream (`X-Accel-Buffering: no`) |
| HTTP/2 push "not working" | Browsers removed push (Chrome 106) | No PUSH_PROMISE in captures | 103 Early Hints, preload |

---

## Summary

- HTTP/1.1 answers one request at a time per connection. Browsers open six connections per host. A page of 20 resources took ~1.19 s on one connection, ~0.43 s on six, ~0.29 s over HTTP/2 and ~0.25 s over HTTP/3 at 50 ms RTT.
- HTTP/2 multiplexes streams as binary frames on one TCP connection, negotiated by ALPN (h2c needs prior knowledge). HPACK cut response headers by 95%. Push is gone (103 Early Hints), and RFC 9218 replaces the old priority tree. One lost TCP segment held all ten streams until its retransmission.
- HTTP/3 runs over QUIC. First byte came after 3 RTTs for HTTP/2, 2 for a new QUIC connection and 1 with 0-RTT, where the request rides in the first datagram (replayable, so idempotent requests only). Streams are independent: without QPACK's dynamic table, three responses arrived unaffected by a loss that blocked every HTTP/2 stream. With it, a lost encoder-stream instruction blocked them all. UDP blocking makes clients fall back to TCP.
- gRPC is HTTP/2: a POST to `/package.Service/Method`, 5-byte-prefixed protobuf messages, and the status in trailers. An HTTP 200 can be a failed call. Deadlines travel as `grpc-timeout` and must be propagated. An HTTP/1.1 hop breaks gRPC, and the HTTP status in the error message (464, 505) names the hop.
- WebSockets upgrade an HTTP/1.1 request (101). Client frames are masked, which guards proxies, not secrets. Idle timeouts cut quiet connections, and pings keep them open.
- SSE is a long HTTP response with ids and automatic resumption. It is simpler than WebSockets for server-to-client updates, and buffering proxies are its usual failure.

## What's Next

Module 16 covers networking in Docker and containers: namespaces, veth pairs, bridges, NAT and container DNS. These are the plumbing under every service in this module once it runs in a container.

---

## Quick Reference Card

```
MODULE 15: HTTP/2, HTTP/3, gRPC & WEBSOCKETS — QUICK REFERENCE
═══════════════════════════════════════════════════════════════

HTTP/1.1     one request at a time per connection; browsers: 6 connections per host
HTTP/2       RFC 9113 · binary frames, 9-byte header · streams (client odd IDs) · ALPN "h2"
             h2c = cleartext with prior knowledge · HPACK (RFC 7541) · flow control per stream + connection
             frames: DATA HEADERS RST_STREAM SETTINGS PING GOAWAY WINDOW_UPDATE CONTINUATION
             push: removed (Chrome 106) → 103 Early Hints · priorities: RFC 9218 (priority: u=N, i)
HTTP/3       RFC 9114 over QUIC (RFC 9000/9001/9002) on UDP 443 · QPACK (RFC 9204)
             handshake 1 RTT (+1 for the request) · 0-RTT on resumption (replayable: RFC 8470, 425 Too Early)
             Initial datagrams ≥ 1200 bytes · 3× anti-amplification · connection IDs, migration
             discovery: Alt-Svc: h3=":443" · DNS HTTPS record (RFC 9460) · fallback to TCP if UDP blocked
gRPC         POST /pkg.Service/Method · content-type application/grpc · te: trailers · grpc-timeout
             message = 1 byte flag + 4 byte length + protobuf · status in TRAILERS (grpc-status, grpc-message)
             HTTP→gRPC: 400 INTERNAL · 401 UNAUTHENTICATED · 403 PERMISSION_DENIED · 404 UNIMPLEMENTED
                        429/502/503/504 UNAVAILABLE · other UNKNOWN
WEBSOCKET    RFC 6455 · GET + Upgrade: websocket → 101 · Accept = base64(SHA1(key + 258EAFA5-…-C5AB0DC85B11))
             header 2–14 bytes; client frames masked (+4) · opcodes 1 text 2 binary 8 close 9 ping A pong
             over HTTP/2 / HTTP/3: extended CONNECT (RFC 8441 / 9220) · beat idle timeouts with pings
SSE          text/event-stream · id/event/data/retry · Last-Event-ID resume · disable proxy buffering

COMMANDS     curl -v (ALPN) · curl --http1.1 / --http2 / --http2-prior-knowledge / --http3-only (if built in)
             nghttp -nv URL (frames) · h2load -n 100 -c 1 -m 10 URL (HPACK savings, throughput)
             grpcurl -plaintext|-cacert F -proto X.proto -d '{…}' HOST:PORT pkg.Service/Method · -max-time
             tshark -o tls.keylog_file:KEYS -Y http2 | quic | websocket
COURSE TOOLS weblab serve|get|grpc|ws|sse|proxy|relay|accept · h3lab.py serve|get · pathlab set drop_big=N
```

---

## References

- RFC 9110 — HTTP Semantics; RFC 9111 — HTTP Caching; RFC 9112 — HTTP/1.1
- RFC 9113 — HTTP/2; RFC 7541 — HPACK; RFC 9218 — Extensible Prioritization Scheme for HTTP; RFC 8297 — 103 Early Hints
- RFC 9000 — QUIC; RFC 9001 — Using TLS to Secure QUIC; RFC 9002 — QUIC Loss Detection and Congestion Control
- RFC 9114 — HTTP/3; RFC 9204 — QPACK; RFC 8470 — Using Early Data in HTTP; RFC 9460 — SVCB and HTTPS DNS records; RFC 7838 — HTTP Alternative Services
- RFC 6455 — The WebSocket Protocol; RFC 8441 — Bootstrapping WebSockets with HTTP/2; RFC 9220 — Bootstrapping WebSockets with HTTP/3
- WHATWG HTML Standard — Server-sent events
- gRPC over HTTP/2 (`doc/PROTOCOL-HTTP2.md`), status codes and HTTP-to-gRPC status mapping — https://grpc.io/docs/ and https://github.com/grpc/grpc/tree/master/doc
- Protocol Buffers encoding — https://protobuf.dev/programming-guides/encoding/
- Chrome Developers — "Remove HTTP/2 Server Push from Chrome" (2022)
- AWS — Application Load Balancer target groups (protocol version) and troubleshooting (HTTP 464, idle timeout)
- `man nghttp`, `man h2load`, `man curl`; grpcurl — https://github.com/fullstorydev/grpcurl; aioquic — https://github.com/aiortc/aioquic
