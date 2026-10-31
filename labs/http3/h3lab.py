"""HTTP/3 for the course's web lab, on aioquic.

Go's standard library has no QUIC, so HTTP/3 is the one part of the web lab
in Python. It mirrors weblab's /obj endpoint and its `get` output.

    h3lab.py serve --cert cert.pem --key key.pem [--host ::] [--port 443]
    h3lab.py get [--cacert ca.pem] [-n 1] [--page] [--ticket FILE] [--zero-rtt]
                 [--keylog FILE] [--qpack-table 4096] [--timeout 5] URL...
"""

import argparse
import asyncio
import os
import pickle
import sys
import time
from urllib.parse import parse_qs, urlsplit

import contextlib
import socket

from aioquic.asyncio import QuicConnectionProtocol, serve
from aioquic.h3.connection import H3_ALPN, H3Connection
from aioquic.h3.events import DataReceived, HeadersReceived
from aioquic.quic.configuration import QuicConfiguration
from aioquic.quic.connection import QuicConnection
from aioquic.quic.events import ConnectionTerminated, HandshakeCompleted, ProtocolNegotiated


def log(msg):
    print(time.strftime("%H:%M:%S.") + f"{int(time.time() * 1000) % 1000:03d} {msg}", flush=True)


def duration(s):
    s = s or "0"
    for unit, mult in (("ms", 0.001), ("s", 1.0)):
        if s.endswith(unit):
            return float(s[: -len(unit)]) * mult
    return float(s)


class ServerProtocol(QuicConnectionProtocol):
    """Serves /obj?size=N&delay=D and / over HTTP/3."""

    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.h3 = None

    def quic_event_received(self, event):
        if isinstance(event, ProtocolNegotiated) and event.alpn_protocol in H3_ALPN:
            self.h3 = H3Connection(self._quic)
        if isinstance(event, HandshakeCompleted):
            peer = self._quic._network_paths[0].addr if self._quic._network_paths else "?"
            log(f"quic handshake with {peer[0]}:{peer[1]} alpn {event.alpn_protocol} "
                f"resumed={event.session_resumed} early_data_accepted={event.early_data_accepted}")
        if self.h3 is None:
            return
        for ev in self.h3.handle_event(event):
            if isinstance(ev, HeadersReceived):
                headers = dict(ev.headers)
                path = headers.get(b":path", b"/").decode()
                log(f"HTTP/3 stream {ev.stream_id} {headers.get(b':method', b'?').decode()} {path}")
                asyncio.ensure_future(self.respond(ev.stream_id, path))

    async def respond(self, stream_id, path):
        url = urlsplit(path)
        q = {k: v[0] for k, v in parse_qs(url.query).items()}
        if url.path == "/obj":
            size = int(q.get("size", "1024"))
            delay = duration(q.get("delay"))
            if delay:
                await asyncio.sleep(delay)
            body = (b"abcdefghijklmnopqrstuvwxyz" * (size // 26 + 1))[:size]
            ctype = b"application/octet-stream"
        else:
            body = b"proto HTTP/3 over QUIC\n"
            ctype = b"text/plain"
        self.h3.send_headers(stream_id, [(b":status", b"200"), (b"content-type", ctype),
                                         (b"content-length", str(len(body)).encode())])
        self.h3.send_data(stream_id, body, end_stream=True)
        self.transmit()


class Tickets:
    """Server-side session ticket store, so clients can resume and send 0-RTT."""

    def __init__(self):
        self.tickets = {}

    def add(self, ticket):
        self.tickets[ticket.ticket] = ticket

    def pop(self, label):
        return self.tickets.pop(label, None)


async def run_server(args):
    cfg = QuicConfiguration(is_client=False, alpn_protocols=H3_ALPN, max_datagram_frame_size=65536)
    cfg.load_cert_chain(args.cert, args.key)
    tickets = Tickets()
    await serve(args.host, args.port, configuration=cfg, create_protocol=ServerProtocol,
                session_ticket_fetcher=tickets.pop, session_ticket_handler=tickets.add)
    log(f"h3lab: HTTP/3 on udp {args.host} port {args.port}")
    await asyncio.Future()


QPACK_TABLE = 4096  # aioquic's default; --qpack-table changes it


class ClientH3(H3Connection):
    """Advertises our QPACK dynamic table size in SETTINGS. 0 forbids the
    server's encoder from using a dynamic table, so no response header block
    can wait for an encoder-stream instruction lost in another packet."""

    def _get_local_settings(self):
        settings = super()._get_local_settings()
        from aioquic.h3.connection import Setting
        settings[Setting.QPACK_MAX_TABLE_CAPACITY] = QPACK_TABLE
        return settings


class ClientProtocol(QuicConnectionProtocol):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, **kwargs)
        self.h3 = ClientH3(self._quic)
        self.pending = {}
        self.handshake = asyncio.get_event_loop().create_future()
        self.terminated = None

    def quic_event_received(self, event):
        if isinstance(event, HandshakeCompleted) and not self.handshake.done():
            self.handshake.set_result((time.monotonic(), event))
        if isinstance(event, ConnectionTerminated):
            self.terminated = event
            for fut, _ in self.pending.values():
                if not fut.done():
                    fut.set_exception(ConnectionError(f"connection closed: {event.reason_phrase}"))
        for ev in self.h3.handle_event(event):
            if ev.stream_id not in self.pending:
                continue
            fut, resp = self.pending[ev.stream_id]
            if isinstance(ev, HeadersReceived):
                resp["status"] = dict(ev.headers).get(b":status", b"?").decode()
                resp["first"] = time.monotonic()
            elif isinstance(ev, DataReceived):
                resp["bytes"] += len(ev.data)
            if getattr(ev, "stream_ended", False) and not fut.done():
                fut.set_result(time.monotonic())

    async def get(self, url):
        u = urlsplit(url)
        sid = self._quic.get_next_available_stream_id()
        fut = asyncio.get_event_loop().create_future()
        resp = {"status": "?", "bytes": 0, "stream": sid}
        self.pending[sid] = (fut, resp)
        self.h3.send_headers(sid, [(b":method", b"GET"), (b":scheme", b"https"),
                                   (b":authority", u.netloc.encode()),
                                   (b":path", (u.path + ("?" + u.query if u.query else "")).encode())],
                             end_stream=True)
        self.transmit()
        resp["done"] = await fut
        return resp


@contextlib.asynccontextmanager
async def connect(host, port, configuration, create_protocol, session_ticket_handler, wait_connected):
    """aioquic.asyncio.connect, but with a socket of the server address's own
    family (aioquic's always opens an IPv6 socket, which fails where IPv6 is
    disabled)."""
    loop = asyncio.get_running_loop()
    family, _, _, _, addr = (await loop.getaddrinfo(host, port, type=socket.SOCK_DGRAM))[0]
    conn = QuicConnection(configuration=configuration, session_ticket_handler=session_ticket_handler)
    transport, protocol = await loop.create_datagram_endpoint(
        lambda: create_protocol(conn), family=family, local_addr=("0.0.0.0", 0) if family == socket.AF_INET else ("::", 0))
    try:
        # With 0-RTT, nothing is sent until the first request is queued, so the
        # ClientHello and the request leave in the same flight.
        protocol.connect(addr, transmit=wait_connected)
        if wait_connected:
            await protocol.wait_connected()
        yield protocol
    finally:
        protocol.close()
        await protocol.wait_closed()
        transport.close()


async def run_client(args):
    global QPACK_TABLE
    QPACK_TABLE = args.qpack_table
    first = urlsplit(args.urls[0])
    host, port = first.hostname, first.port or 443
    cfg = QuicConfiguration(is_client=True, alpn_protocols=H3_ALPN, server_name=host)
    if args.cacert:
        cfg.load_verify_locations(args.cacert)
    if args.keylog:
        cfg.secrets_log_file = open(args.keylog, "a")
    if args.ticket and os.path.exists(args.ticket):
        with open(args.ticket, "rb") as f:
            cfg.session_ticket = pickle.load(f)

    got_ticket = asyncio.Event()

    def save_ticket(ticket):
        if args.ticket:
            with open(args.ticket, "wb") as f:
                pickle.dump(ticket, f)
        got_ticket.set()

    # aioquic fails its internal "connected" future when a connection is
    # abandoned; nobody awaits it, so keep asyncio from printing it.
    asyncio.get_running_loop().set_exception_handler(
        lambda loop, ctx: None if isinstance(ctx.get("exception"), ConnectionError) else loop.default_exception_handler(ctx))
    urls = args.urls * args.n
    start = time.monotonic()
    ms = lambda t: (t - start) * 1000
    try:
        async with asyncio.timeout(args.timeout):
            async with connect(host, port, configuration=cfg, create_protocol=ClientProtocol,
                               session_ticket_handler=save_ticket,
                               wait_connected=not args.zero_rtt) as client:
                if args.page:
                    # the page alone, then its resources at once, as a browser does
                    results = [await client.get(urls[0])]
                    results += await asyncio.gather(*(client.get(u) for u in urls[1:]))
                else:
                    results = await asyncio.gather(*(client.get(u) for u in urls))
                done_hs, hs = await client.handshake
                for url, r in sorted(zip(urls, results), key=lambda x: x[1]["done"]):
                    path = urlsplit(url).path + ("?" + urlsplit(url).query if urlsplit(url).query else "")
                    print(f"stream {r['stream']:<3} {path:<36} HTTP/3 {r['status']} {r['bytes']:7d} bytes  "
                          f"first byte {ms(r['first']):6.0f} ms  done {ms(r['done']):6.0f} ms")
                sent = args.zero_rtt and cfg.session_ticket is not None
                early = "not sent" if not sent else ("accepted" if hs.early_data_accepted else "rejected (sent again after the handshake)")
                print(f"handshake {ms(done_hs):.0f} ms, resumed={hs.session_resumed}, 0-RTT: {early}")
                last = max(r["done"] for r in results)
                print(f"summary: h3, {len(urls)} requests, 1 QUIC connection, "
                      f"{sum(r['bytes'] for r in results)} bytes, 0 failed, total {ms(last):.0f} ms")
                if args.ticket:
                    # The server sends a new ticket just after the handshake;
                    # wait briefly so the next run can resume.
                    try:
                        await asyncio.wait_for(got_ticket.wait(), 0.5)
                    except TimeoutError:
                        pass
                client.close()
    except TimeoutError:
        print(f"no QUIC connection after {args.timeout:g} s — UDP blocked? (a browser falls back to HTTP/2 over TCP)")
        return 1
    return 0


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("serve")
    s.add_argument("--cert", required=True)
    s.add_argument("--key", required=True)
    s.add_argument("--host", default="::")
    s.add_argument("--port", type=int, default=443)
    g = sub.add_parser("get")
    g.add_argument("urls", nargs="+")
    g.add_argument("--cacert")
    g.add_argument("-n", type=int, default=1)
    g.add_argument("--page", action="store_true", help="fetch the first URL alone, then the rest at once")
    g.add_argument("--ticket", help="file to save a session ticket to, and resume from")
    g.add_argument("--zero-rtt", action="store_true", help="send the requests as 0-RTT early data")
    g.add_argument("--keylog")
    g.add_argument("--qpack-table", type=int, default=4096,
                   help="QPACK dynamic table capacity to advertise (0: the server may not use one)")
    g.add_argument("--timeout", type=float, default=5)
    args = p.parse_args()
    if args.cmd == "serve":
        asyncio.run(run_server(args))
    else:
        sys.exit(asyncio.run(run_client(args)))


if __name__ == "__main__":
    main()
