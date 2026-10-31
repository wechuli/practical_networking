# Module 21: Packet Capture and Analysis

> **Audience:** Support Engineers · Developers · Cloud Engineers
> **Prerequisites:** Module 20 — Sockets and Application-Level Networking
> **Estimated time:** 5 hours

## Overview

Almost every lab in this course ended with a capture as its evidence. This module is about the instrument itself. Two things are true at once:

- **A capture is the most direct evidence available.** It shows what crossed one interface, when, and in what order, independent of what any application believes.
- **It is an observation from one point, with blind spots.** A capture can silently miss packets, see packets the wire never carried (offloads, Module 11), see addresses rewritten by NAT further along, or show encrypted bytes it cannot interpret. Each of these limits is measured here.

The module covers `tcpdump` for capturing, `tshark` for scripted analysis, and Wireshark for interactive work. Every number comes from [`labs/netlab/capture.json`](../labs/netlab/capture.json): a client, a router that can add delay and loss, and a server with DNS, HTTP and HTTPS.

## What You Will Build Toward

- Capture the right traffic at the right point, safely, and know when a capture is incomplete
- Localize where packets disappear by capturing at several points
- Use tshark display filters, fields and statistics to turn a pcap into answers
- Separate network time from server time, and measure one-way delay
- Read TCP analysis flags correctly from the sender's and the receiver's side
- Know what TLS reveals without keys, and decrypt with a key log

---

## 21.1 What a Capture Can and Cannot Tell You

A capture on a Linux host records frames as the kernel saw them at that interface (`AF_PACKET`):

- **Outgoing frames** are recorded before the NIC. With segmentation offload they can be larger than the MTU, and a recorded frame isn't proof that the wire carried it.
- **Incoming frames** are recorded after the NIC, possibly after receive coalescing (GRO).
- **Filtering:** the BPF filter runs in the kernel and keeps only matching packets.
- **Buffering:** matches go into a ring buffer, and tcpdump copies them out to print or save.

Things that make a capture lie by omission, all measured below:

- tcpdump stopped before it read its buffer;
- a buffer that overflowed;
- a snaplen that cut payloads;
- the wrong capture point;
- encryption.

When stating a conclusion, say where you captured. "The SYN left the client and never reached the server" is a claim about two capture points, not one.

---

## 21.2 tcpdump: Capturing Without Missing Things

### Basic use

```bash
tcpdump -i eth0 -nn 'tcp port 443'                       # print; -nn: no name or port lookups
tcpdump -i eth0 -nn -w /tmp/c.pcap 'host 10.0.1.5'       # save for tshark/Wireshark
tcpdump -nn -r /tmp/c.pcap 'tcp[tcpflags] & tcp-rst != 0' # read back, filtered
```

Useful options:

| Option | What it does |
| --- | --- |
| `-e` | link-layer headers |
| `-v`, `-vv` | IP header fields (TTL, ID, flags, fragment offsets) |
| `-A` / `-X` | payload as ASCII / hex+ASCII |
| `-c N` | stop after N packets |
| `-s N` | snaplen: bytes kept per packet |
| `-B KiB` | capture buffer size |
| `-Z root` | don't switch to the `tcpdump` user when writing files |
| `--immediate-mode` | deliver each packet as it arrives (see below) |

Common BPF capture filters:

| Filter | Matches |
| --- | --- |
| `host H`, `src`/`dst host H` | an address, either way or one way |
| `net 10.0.0.0/8` | a network |
| `port P`, `portrange A-B` | a port or range |
| `tcp`, `udp`, `icmp`, `arp`, `ip6` | a protocol |
| `'tcp[tcpflags] == tcp-syn'` | SYN only (not SYN-ACK) |
| `'tcp[tcpflags] & (tcp-syn\|tcp-fin\|tcp-rst) != 0'` | connection starts and ends |
| `'icmp[icmptype] == 3'` | destination unreachable |
| `'(ip[2:2] - ((ip[0]&0xf)<<2) - ((tcp[12]&0xf0)>>2)) > 0'` | TCP segments carrying data |
| `'ip[6:2] & 0x1fff != 0'` | IP fragments after the first |

`tcpdump -d FILTER` prints the compiled BPF program. `tcp port 8080` became 20 instructions, covering IPv4, IPv6 and fragments.

### Stopping a capture: `--immediate-mode`

On Linux, libpcap receives packets from the kernel in blocks, and by default tcpdump waits up to a second before handing a block to its reader. The lab sent three pings and stopped tcpdump straight away (four runs):

```
tcpdump (default):          0 packets captured   6 packets received by filter
tcpdump --immediate-mode:   6 packets captured   6 packets received by filter
```

The kernel matched all six packets, but the default capture **wrote none**, and `-U` (packet-buffered output) didn't help. That is how short scripted captures come back empty: the course's old Python capture fixture did exactly this in the build environment.

- Use `--immediate-mode` for short or scripted captures.
- Always read the three summary lines tcpdump prints when it stops.

### When tcpdump falls behind: kernel drops

A capture with a small buffer (`-B 64` KiB) and a reader that stalled for 3 seconds, during a 1-second iperf3 transfer:

```
slow: 364 packets captured   27561 packets received by filter   27197 packets dropped by kernel
fast: 24025 packets captured 24025 packets received by filter   0 packets dropped by kernel
```

The "slow" capture recorded 1.3 % of what passed (runs: 364–378 of about 27,600). The "fast" one used `-B 16384 -s 96 -w file`. What makes a capture slow:

- printing (`-v`) to a terminal or slow pipe;
- name resolution (no `-n`);
- slow disks;
- a full CPU.

**A capture with kernel drops cannot prove that a packet was absent.** Before arguing "the server never replied", check that the drop count was 0.

### Capture points and `-i any`

`-i any` captures every interface with a Linux "cooked" link type (`LINUX_SLL2`), which records the interface and direction instead of Ethernet headers. On the lab router one ping appears four times on its way out, because the router's path emulator adds a hop:

```
eth0  In  IP 198.18.210.10 > 198.18.211.10: ICMP echo request
pl-ab Out IP 198.18.210.10 > 198.18.211.10: ICMP echo request
pl-ab In  IP 198.18.210.10 > 198.18.211.10: ICMP echo request
eth1  Out IP 198.18.210.10 > 198.18.211.10: ICMP echo request
```

That is the forwarding path in one view. It also duplicates packets, so count carefully, and filter by interface (`tshark … -Y 'sll.ifname == "eth0"'`) when comparing.

### Snaplen and ring buffers

| Snaplen | File size (same 38-packet HTTP exchange) | What survives |
| --- | --- | --- |
| full (262144) | 23 kB | everything |
| `-s 96` | **3.6 kB** | Ethernet + IP + TCP headers + the first 30 bytes: `HTTP/1.1 200 OK` was still visible; 16 frames marked cut short (`_ws.short`); no bodies |

A small snaplen keeps TCP analysis working on long captures at a fraction of the disk, and it reduces, though does not remove, how much payload you keep. Mind the arithmetic: IPv6, VLAN tags, tunnel headers and TCP options eat into 96 bytes.

For long-running captures use a **ring buffer**. `-C 1 -W 3` kept three files of 1 MB (`ring.pcap0` to `ring.pcap2`), overwriting the oldest, during a 20 MB transfer. Disk use is bounded however long it runs. `-G SECONDS` rotates by time instead; with `-W` it stops after that many files rather than wrapping.

**Safety on production hosts:**
- Capture with a filter, a snaplen, a count or duration, and a ring buffer.
- Store the files somewhere only you can read.
- Delete them when done: captures contain data and credentials.

---

## 21.3 Localizing Loss: Capture at Several Points

The lab's router dropped TCP to port 7021. A client tried to connect, with captures running at four points:

```
SYNs seen at client:      3
SYNs seen at router-in:   3
SYNs seen at router-out:  0
SYNs seen at server:      0
```

The SYNs left the client and reached the router, and nothing left it, so the router dropped them. That holds in every run, and it's the most useful thing a capture can do in a "who is dropping it?" argument: each capture point either sees the packet or doesn't.

**Where to capture in real systems:**
- the client host;
- each side of a firewall or load balancer (if you can't capture there, use its logs and counters);
- the server host;
- in Kubernetes, the pod's interface and the node's interface;
- cloud mirroring: AWS VPC Traffic Mirroring, Google Cloud Packet Mirroring.

**NAT changes what each point sees.** After a NAT, the server sees the translating device's address and often a different port. Match packets across points by fields NAT leaves alone: the IP ID (usually), TCP sequence numbers (unless an intermediary rewrites them) and timing. Module 16's containers lab shows the same flow before and after SNAT.

---

## 21.4 tshark: From Packets to Answers

tshark applies Wireshark's dissectors from the command line. Two filter languages are involved, and they are not the same:

- **Capture filters** (`-f`, or tcpdump's filter) are BPF: fast and limited, applied while capturing.
- **Display filters** (`-Y`) use Wireshark's field names (`tcp.analysis.retransmission`, `http.response.code == 500`, `dns.flags.rcode == 3`). They run on dissected packets, and some need **two passes (`-2`)** because they refer to later packets.

```bash
tshark -n -r c.pcap -Y 'tcp.flags.syn==1 && tcp.flags.ack==0' -T fields -e frame.time_relative -e ip.src -e tcp.srcport
tshark -n -r c.pcap -q -z conv,tcp              # conversations: frames and bytes per direction, duration
tshark -n -r c.pcap -q -z expert                # Expert Info, summarized
tshark -n -r c.pcap -q -z io,stat,0.1,"COUNT(tcp.analysis.retransmission)tcp.analysis.retransmission"
tshark -n -r c.pcap -q -z follow,tcp,ascii,0    # the bytes of stream 0
editcap -r big.pcap first100.pcap 1-100         # cut a capture; mergecap joins, capinfos summarizes
```

`-z io,stat` turned the lossy transfer below into retransmissions per 100 ms (0, 8, 13, 15, 15, 10). That shows when the trouble happened, not just how much.

### Network time vs server time

One HTTP request with 200 ms of server work, over a 20 ms round trip, captured at both ends (three runs):

| Measured | Client capture | Server capture |
| --- | --- | --- |
| Initial RTT (`tcp.analysis.initial_rtt`, from the handshake) | 20.9–21.5 ms | 20.9–21.7 ms |
| Request → first response packet (`http.time`) | 222.2–222.6 ms | **200.9–201.0 ms** |

How to read the table:

- **The server's capture isolates the server.** 201 ms between request and response means the server spent the time, not the network.
- **The client's 222 ms is 201 ms + one round trip.** curl agreed: first byte at 0.244 s, which also includes the 21 ms TCP handshake.
- **The server's handshake gives the client's RTT.** A server-side capture alone can tell how far away a slow client is.
- **Desegmentation changes what `http.time` measures.** By default Wireshark reassembles the response and timestamps its *last* segment. Turn reassembly off (`-o tcp.desegment_tcp_streams:FALSE`) to time the first byte.

**One-way delay.** `mergecap` joined the two captures. The same SYN (matched by IP ID) left the client and reached the server **10.5–10.9 ms** later: the router's 10 ms each way. Both captures used one clock here; across hosts, one-way numbers are only as good as their clock synchronization (NTP typically to within a millisecond or a few, PTP to microseconds). Round-trip numbers need no synchronization.

---

## 21.5 TCP Problems in tshark

### The same loss, seen from both ends

A 5 MB transfer through 2 % random loss each way, captured at the sender and the receiver (three runs):

| Display filter | Sender's capture | Receiver's capture |
| --- | --- | --- |
| `tcp.analysis.retransmission` | 58–62 | 5–46 |
| `tcp.analysis.fast_retransmission` | 0–2 | 5–45 |
| `tcp.analysis.lost_segment` ("previous segment not captured") | 0 | **58–61** |
| `tcp.analysis.out_of_order` | 0 | 16–59 |
| `tcp.analysis.duplicate_ack` | 90–290 | 91–298 |

The sender saw its own retransmissions, about one per lost segment (the sender's TCP reported 59–74 retransmitted).

The receiver never saw the lost segments. It saw **gaps** ("previous segment not captured") followed by the retransmissions arriving late, which Wireshark labels out-of-order, fast retransmission or retransmission depending on timing.

The same event gets different names depending on where you captured, and the labels are heuristics, not facts:

- **"Previous segment not captured" at a receiver** means loss upstream of the capture point, *or* that the capture itself dropped it. Check the kernel-drop count.
- **The same message at the sender** usually means the capture started mid-stream or the capture dropped packets.
- **Many duplicate ACKs with SACK blocks** mean the receiver is reporting holes. That is normal loss recovery (Module 11), not a problem in itself.
- **"Spurious retransmission"** means the data had already arrived: the timer fired too early, or ACKs were lost.

### A receiver that stops reading

A transfer to a server that never reads its socket:

```
TCP window specified by the receiver is now completely full   ×2   (Warning)
TCP Zero Window segment                                        ×4–5 (Warning)
TCP keep-alive segment                                         ×4   (Note)
```

Wireshark labels Linux's zero-window probes as "keep-alive" segments: the probe is a zero-length segment one byte behind, which matches Wireshark's keep-alive heuristic. Its `tcp.analysis.zero_window_probe` filter counted 0. So a zero-window episode looks like **Zero Window from the receiver, then "keep-alives" from the sender at growing intervals**, not "zero window probes". The server-side fix is never in the network (Module 11).

### Other signatures

| What you see | Filter | Usually means |
| --- | --- | --- |
| SYNs repeated at 1 s, 2 s, 3 s… (Linux 6.5+ timing, Module 11) with no SYN-ACK | `tcp.flags.syn==1 && tcp.analysis.retransmission` | dropped on the way out or the way back (capture both ends) |
| RST right after SYN | `tcp.flags.reset==1` | nothing listening, or a REJECT (TTL tells which: Module 11) |
| Full-size segments retransmitted, small ones fine | `tcp.analysis.retransmission && tcp.len > 1000` | PMTU black hole (Modules 14, 19) |
| Data then silence then RST from a third party | IP TTL and ID of the RST | middlebox idle timeout |
| `tcp.analysis.ack_rtt` rising during a transfer | IO graph of the field | queueing (bufferbloat, Module 14) |

---

## 21.6 DNS in a Capture

One capture with three lookups: an answer, NXDOMAIN, and a query whose response the router dropped:

```
answered:     api.capture.test    rcode 0   dns.time 0.022   198.18.211.10
              nope.capture.test   rcode 3   dns.time 0.021
unanswered:   lost.capture.test
```

Useful fields and filters:

| Field or filter | What it gives |
| --- | --- |
| `dns.time` | the response time, from the query |
| `dns.flags.rcode` | 0 NOERROR, 2 SERVFAIL, 3 NXDOMAIN, 5 REFUSED |
| `tshark -2 … -Y 'dns.flags.response == 0 && !dns.response_in'` | queries that were never answered (`-2` because the response comes later) |
| `dns.retransmission` | retries of the same query |

Without `-2` the filter matched all three queries.

---

## 21.7 TLS: What You See Without and With Keys

Three HTTPS requests with curl (OpenSSL 3.0): one against an untrusted CA, one with TLS 1.3, one with TLS 1.2.

**Without keys:**

- **The ClientHello** shows the SNI (`api.capture.test`) and the ALPN offer (`http/1.1`) in clear text. That is what firewalls, proxies and SNI-based routing use.
- **TLS 1.3** encrypts everything after the ServerHello, so the certificate is invisible.
- **TLS 1.2** sent `Server Hello, Certificate, …` in clear, so its subject and SANs can be read straight from the capture (`x509ce.dNSName`).
- **The failed request** showed `Alert (Level: Fatal, Description: Unknown CA)` in clear. OpenSSL sent it before switching to encrypted handshake records. Other libraries may send it encrypted (an "Encrypted Alert").
- **No HTTP message** was visible.

**With keys.** `SSLKEYLOGFILE` made curl write the session secrets: `CLIENT_RANDOM` for TLS 1.2, and the `*_HANDSHAKE_TRAFFIC_SECRET`, `*_TRAFFIC_SECRET_0` and `EXPORTER_SECRET` labels for TLS 1.3. With `-o tls.keylog_file:keys.log`, tshark showed `GET /obj?size=10`, `HTTP/1.1 200 OK` and the `Close Notify` alerts.

What supports key logging:

| Client | Key logging |
| --- | --- |
| curl, Firefox, Chrome | honour `SSLKEYLOGFILE` |
| Python | `ssl.SSLContext.keylog_filename` |
| Go | `tls.Config.KeyLogWriter` (the course's `tlslab -keylog`, `weblab -keylog`) |
| Java | needs an agent |

A key log is as sensitive as the traffic it decrypts. Keep it next to the capture with the same protections, and delete both.

---

## 21.8 Capturing in Containers, Kubernetes and the Cloud

- **A container's namespace:**
  - `nsenter -t PID -n tcpdump -i eth0 …` runs the host's tcpdump inside the container's network namespace, with no tools needed in the image.
  - Or capture the host side of its veth (Module 16).
- **Kubernetes:**
  - `kubectl debug -it POD --image=<debug image> --target=CONTAINER -- tcpdump -i eth0 -w - > pod.pcap` uses an ephemeral container in the pod's namespaces.
  - Alternatively, run `crictl inspect` on the node to get the PID, then `nsenter` (Module 17).
  - Capture at the node's interface too, to see the encapsulated (VXLAN) or SNATed view.
- **Streaming a remote capture:**
  ```bash
  ssh host 'sudo tcpdump -U --immediate-mode -i eth0 -w - "port 443"' | wireshark -k -i -
  ```
  Exclude your own SSH session from the filter.
- **Cloud:**
  - Managed load balancers and NAT gateways can't be captured.
  - Mirror traffic (VPC Traffic Mirroring, Packet Mirroring) to a capture instance, or use flow logs as the per-flow summary.

---

## Hands-On Lab: Captures Measured

### Lab Goals

- Prove that captures can be incomplete, and how to avoid it
- Localize a drop with four capture points
- Separate network and server time; measure one-way delay
- Read loss, zero window and DNS failures with tshark
- Compare TLS with and without a key log

### Prerequisites

- The course lab VM (Ubuntu 24.04, root), the Go toolbox (`cd labs/go && go build -o bin/ ./cmd/...`)
- `tcpdump`, `tshark` and `wireshark-common` (mergecap, editcap, capinfos), `dnsmasq-base`, `dnsutils`, `iperf3`, `curl`. Wireshark itself on your workstation for the GUI exercises.

### Lab Part 1: Run the Lab

```
client 198.18.210.10 ── router (pathlab: delay/loss on demand) ── server 198.18.211.10
 resolver → server                                                 dnsmasq :53 · HTTP :8080 · HTTPS :8443
                                                                   iperf3 :5201 · noread :7020 · discard :7021
```

```bash
cd labs/go
sudo ./bin/netlab run ../netlab/capture.json     # about 40 seconds
```

All 16 checks passed in three runs; every pcap is kept in the evidence directory for Wireshark.

### Lab Part 2: By Hand

On the lab's pcaps (`E=/tmp/netlab-capture-…` from the run):

1. **See a filter compiled.** Run `tcpdump -d 'tcp port 8080'` and `tcpdump -d 'tcp[tcpflags] == tcp-syn'`, and compare how many instructions each needs.
2. **When did the loss happen?** Run `tshark -n -r $E/loss-client.pcap -q -z io,stat,0.1,"COUNT(tcp.analysis.retransmission)tcp.analysis.retransmission"`. Then open the same file in Wireshark: Statistics → TCP Stream Graphs → Stevens. The retransmissions show as dots below the line.
3. **Follow a stream.** In `time-client.pcap`, use `tshark -Y 'tcp.stream eq 0'` with `-T fields -e frame.number -e tcp.time_relative -e _ws.col.Info`, then `-q -z follow,tcp,ascii,0`. In Wireshark: right-click → Follow → TCP Stream.
4. **Decrypt in the GUI.** Open `tls.pcap` and set Edit → Preferences → Protocols → TLS → (Pre)-Master-Secret log filename to `tls-keys.log`. Then filter `http`.
5. **Cut and merge.** Run `editcap -r $E/loss-client.pcap /tmp/first100.pcap 1-100` (100 packets) and `mergecap -w /tmp/both.pcap $E/time-client.pcap $E/time-server.pcap`. Find the same SYN twice.

### Lab Part 3: Your Systems

- Capture one request to a service you support at the client and the server at the same time, and split its latency into network and server time as in 21.4.
- Check how your organization captures in Kubernetes and the cloud (debug images, mirroring, who may hold key logs), and write the runbook down.

---

## Real-World Troubleshooting Scenario

### Scenario: "Some API Calls Take Exactly 1 Second Longer"

**The situation.**

- A service calls an internal API.
- Most calls take 15 ms; about 2 % take about 1015 ms.
- The API's own latency metrics show nothing above 20 ms.

**Step 1 — Capture at both ends, correctly.**

- Run `tcpdump -i eth0 --immediate-mode -s 128 -C 100 -W 5 -w api.pcap 'host API and port 8443'` on a client pod and on one API instance.
- Confirm "0 packets dropped by kernel" on both before trusting them.

**Step 2 — Find the slow ones.**

```bash
tshark -n -r client.pcap -Y 'tcp.flags.syn==1 && tcp.flags.ack==0 && tcp.analysis.retransmission' \
  -T fields -e frame.time -e tcp.srcport
```

Every slow call starts with a **retransmitted SYN** one second after the first. The client's first SYN got no SYN-ACK.

**Step 3 — Which direction failed?** In the server's capture, those connections show **one** SYN (the retransmission) and its SYN-ACK. The first SYN never arrived, so the drop is between the hosts, not a server-side accept-queue drop (which would show the SYN arriving unanswered: Module 11).

**Step 4 — Narrow it down.** The path crosses a NAT gateway. Its connection-tracking counters show insertion failures in the same minutes. A capture on the node interface shows the dropped SYNs reused source ports from connections the gateway still tracked. That is SNAT port exhaustion and reuse, as in Module 16.

**The fix** belongs to the NAT layer:
- more SNAT addresses and ports;
- fully randomized source ports;
- connection reuse in the client, so fewer new connections are made.

The 1 s is the SYN retransmission timer, not anyone's processing time.

**What made it fast:** captures at both ends with a verified drop count of zero, a display filter for retransmitted SYNs, and reading "first SYN absent at the server" as "lost in between".

---

## Common Failure Patterns

| Symptom in or of the capture | What it means | How to confirm | Next step |
| --- | --- | --- | --- |
| Capture file empty or short though traffic flowed | tcpdump stopped before reading its buffer | "received by filter" > "captured" | `--immediate-mode`; stop later |
| "N packets dropped by kernel" | Capture couldn't keep up — the capture is incomplete | tcpdump's exit summary | Write to file, `-s`, `-B`, tighter filter |
| Segments larger than the MTU on the sender | TSO/GSO before the NIC (Module 11) | `ethtool -k`; capture on the far side | Read them as offload, not jumbo frames |
| "Previous segment not captured" at a receiver | Loss upstream — or capture drops | Kernel drop count; capture upstream | Localize with more capture points |
| Retransmitted SYNs at 1 s steps, no SYN-ACK | SYN or SYN-ACK lost | Capture at the server too | Firewall, NAT, conntrack, accept queue |
| Zero Window + "keep-alive" segments | Receiver not reading | Server's Recv-Q | Application side |
| `http.time` larger at the client than the server | Network time = the difference | Both captures | RTT, retransmissions, proxies |
| Only "Application Data" in TLS | Encrypted, as designed | SNI/ALPN in ClientHello | Key log, or server-side logs |
| Same packet appears several times with `-i any` | Seen on each interface it crossed | `sll.ifname` | Filter by interface |
| Addresses differ between capture points | NAT or a proxy in between | IP ID, sequence numbers, timing | Map translations at the device |

---

## Summary

- A capture is an observation at one point. It can miss packets (stopped too early: 0 of 6 written without `--immediate-mode`; overloaded: 1.3 % kept with a stalled reader), cut payloads (snaplen) and record frames the wire never carried (offloads). Read tcpdump's summary lines before trusting what's absent.
- Capturing at several points localizes loss: the SYNs reached the router and never left it. NAT and `-i any` change what each point shows.
- tshark turns captures into answers: display filters, `-T fields`, `-z conv/expert/io,stat/follow`, `-2` for filters that look ahead, and `mergecap`/`editcap`/`capinfos` around them.
- The handshake gives RTT at either end, and the server's capture separates server time from network time (201 ms vs 222 ms). Merged captures give one-way delay when clocks agree.
- The same loss looks different at the sender (retransmissions) and the receiver (previous segment not captured, out-of-order). Linux's zero-window probes appear as "keep-alive" segments. Analysis labels are heuristics.
- Without keys, TLS shows SNI, ALPN and (in TLS 1.2) the certificate. A key log decrypts the rest and must be guarded like the data.

## What's Next

Module 22 covers observability and troubleshooting methodology: a systematic way to go from symptom to cause using metrics, logs, flow data, traces and captures, and choosing the cheapest evidence that answers the question.

---

## Quick Reference Card

```
MODULE 21: PACKET CAPTURE AND ANALYSIS — QUICK REFERENCE
══════════════════════════════════════════════════════════

TCPDUMP
  tcpdump -i IF -nn [--immediate-mode] [-s 128] [-B 16384] -w f.pcap 'FILTER'
  -C MB -W N (ring) · -G SEC (time rotation) · -c N · -Z root · -i any (LINUX_SLL2: interface + In/Out)
  ALWAYS read: "captured / received by filter / dropped by kernel"
  tcpdump -d 'FILTER' → compiled BPF

BPF
  host · net · port · portrange · src/dst · tcp/udp/icmp/arp/ip6
  'tcp[tcpflags] == tcp-syn' · 'tcp[tcpflags] & (tcp-syn|tcp-fin|tcp-rst) != 0' · 'icmp[icmptype]==3'
  data only: '(ip[2:2] - ((ip[0]&0xf)<<2) - ((tcp[12]&0xf0)>>2)) > 0' · fragments: 'ip[6:2] & 0x1fff != 0'

TSHARK
  tshark -n -r f.pcap -Y 'DISPLAY' -T fields -e FIELD … · -2 (two-pass) · -o PREF:VALUE
  -q -z conv,tcp | expert | io,stat,0.1[,COUNT(f)f] | follow,tcp,ascii,N
  mergecap · editcap -r f out 1-100 · capinfos

DISPLAY FILTERS
  tcp.analysis.retransmission | fast_retransmission | lost_segment | out_of_order | duplicate_ack
  tcp.analysis.zero_window | window_full · (Linux persist probes show as keep-alive)
  tcp.analysis.initial_rtt · tcp.analysis.ack_rtt · tcp.stream eq N · tcp.len > 0
  http.time (-o tcp.desegment_tcp_streams:FALSE for first byte) · http.response.code >= 500
  dns.flags.rcode · dns.time · dns.flags.response==0 && !dns.response_in (-2)
  tls.handshake.extensions_server_name · tls.alert_message.desc · x509ce.dNSName

TLS
  SSLKEYLOGFILE=keys.log (curl, browsers) · Go: tls.Config.KeyLogWriter · Python: keylog_filename
  tshark -o tls.keylog_file:keys.log · Wireshark: Preferences → Protocols → TLS

WHERE
  nsenter -t PID -n tcpdump … · kubectl debug --target · host veth / node interface
  ssh host 'tcpdump -U --immediate-mode -w - …' | wireshark -k -i -
  cloud: traffic mirroring; flow logs as summaries
```

---

## References

- `man tcpdump`, `man pcap-filter`, `man pcap` (immediate mode, buffer size, timeouts); `man tshark`, `man editcap`, `man mergecap`, `man capinfos`
- Wireshark User's Guide — TCP analysis, Expert Information; Display Filter Reference — https://www.wireshark.org/docs/dfref/
- NSS key log format; TLS 1.3 key schedule (RFC 8446 §7)
- Kubernetes — Debugging with ephemeral containers (`kubectl debug`)
- AWS VPC Traffic Mirroring; Google Cloud Packet Mirroring
