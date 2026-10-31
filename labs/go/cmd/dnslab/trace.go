package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"time"
)

// Tracer resolves a name iteratively — the way a recursive resolver does —
// starting from root hints and printing every step: which server it asked,
// what came back, and why it asked the next one.
type Tracer struct {
	Roots   []netip.Addr
	Port    string // server port; "" means 53
	Timeout time.Duration
	Out     io.Writer
	step    int
	depth   int
}

// TraceResult is where a trace ended.
type TraceResult struct {
	Rcode   int
	Answer  []RR
	Steps   int
	Elapsed time.Duration
}

const maxSteps = 40

// Resolve follows referrals and CNAMEs from the root to the answer.
func (t *Tracer) Resolve(ctx context.Context, name string, qtype uint16) (*TraceResult, error) {
	start := time.Now()
	res := &TraceResult{}
	name = Canonical(name)
	for cnames := 0; cnames < 8; cnames++ {
		rcode, answer, err := t.iterate(ctx, name, qtype)
		if err != nil {
			return nil, err
		}
		res.Rcode = rcode
		res.Answer = append(res.Answer, answer...)
		// If the answer is only a CNAME, start again from the root for its target.
		target := ""
		for _, r := range answer {
			if r.Type == qtype || qtype == TypeCNAME {
				target = ""
				break
			}
			if r.Type == TypeCNAME && Canonical(r.Name) == name {
				target = r.Target
			}
		}
		if target == "" || rcode != RcodeNoError {
			break
		}
		t.say("          %s is an alias (CNAME) for %s: start again from the root\n", name, target)
		name = target
	}
	res.Steps, res.Elapsed = t.step, time.Since(start)
	return res, nil
}

type nsServer struct {
	name string
	addr netip.Addr
}

func (t *Tracer) say(format string, a ...any) {
	fmt.Fprintf(t.Out, strings.Repeat("  ", t.depth)+format, a...)
}

// iterate asks servers from the root down for name, following referrals.
func (t *Tracer) iterate(ctx context.Context, name string, qtype uint16) (int, []RR, error) {
	var servers []nsServer
	for _, a := range t.Roots {
		servers = append(servers, nsServer{"root hint", a})
	}
	zone := "."
	for {
		var m *Message
		var used nsServer
		for _, s := range servers {
			if t.step >= maxSteps {
				return 0, nil, fmt.Errorf("gave up after %d steps", maxSteps)
			}
			if !s.addr.IsValid() {
				addr, err := t.addressOf(ctx, s.name)
				if err != nil || !addr.IsValid() {
					t.say("          cannot find an address for %s: %v\n", s.name, err)
					continue
				}
				s.addr = addr
			}
			t.step++
			port := t.Port
			if port == "" {
				port = "53"
			}
			ex, err := Ask(ctx, net.JoinHostPort(s.addr.String(), port), name, qtype, QueryOptions{UDPSize: 1232}, false, t.Timeout)
			if err != nil {
				t.say("step %2d  %-15s %-18s %s %s → %s; try the next server\n", t.step, s.addr, "("+s.name+")", name, TypeName(qtype), Describe(err))
				continue
			}
			via := ""
			if ex.Retried {
				via = ", truncated over UDP so repeated over TCP"
			}
			t.say("step %2d  %-15s %-18s %s %s  %s%s\n", t.step, s.addr, "("+s.name+")", name, TypeName(qtype), fmtRTT(ex.RTT), via)
			t.say("          ← %s [%s]\n", Summary(ex.Msg), ex.Msg.Header.Flags())
			if ex.Msg.Header.Rcode == RcodeRefused || ex.Msg.Header.Rcode == RcodeServFail {
				t.say("          %s does not serve %s (a lame delegation?); try the next server\n", s.addr, zone)
				continue
			}
			m, used = ex.Msg, s
			break
		}
		if m == nil {
			t.say("          every server for %s failed\n", zone)
			return RcodeServFail, nil, nil
		}
		_ = used
		switch {
		case len(m.Answer) > 0:
			for _, r := range m.Answer {
				if r.Type != TypeRRSIG {
					t.say("          %s\n", r)
				}
			}
			return m.Header.Rcode, m.Answer, nil
		case isReferral(m):
			next, cut := referral(m)
			if !inZone(cut, zone) || cut == zone {
				t.say("          the referral to %s does not lead below %s; stop\n", cut, zone)
				return RcodeServFail, nil, nil
			}
			var names []string
			for _, s := range next {
				g := "no glue"
				if s.addr.IsValid() {
					g = "glue " + s.addr.String()
				}
				names = append(names, s.name+" ("+g+")")
			}
			t.say("          %s is delegated to %s\n", cut, strings.Join(names, ", "))
			zone, servers = cut, next
		default:
			ttl, soa := NegativeTTL(m)
			if soa != nil {
				t.say("          SOA %s: negative answer cacheable for %d s = min(SOA TTL %d, MINIMUM %d)\n",
					soa.Name, ttl, soa.TTL, soa.SOA.MinTTL)
			}
			return m.Header.Rcode, nil, nil
		}
	}
}

// referral extracts the delegated zone and its servers, with glue addresses
// from the additional section where present.
func referral(m *Message) ([]nsServer, string) {
	glue := map[string]netip.Addr{}
	for _, r := range m.Additional {
		if r.Type == TypeA {
			if _, ok := glue[r.Name]; !ok {
				glue[r.Name] = r.Addr
			}
		}
	}
	var out []nsServer
	cut := ""
	for _, r := range m.Authority {
		if r.Type == TypeNS {
			cut = r.Name
			out = append(out, nsServer{r.Target, glue[r.Target]})
		}
	}
	return out, cut
}

// inZone reports whether name is at or below zone.
func inZone(name, zone string) bool {
	return zone == "." || name == zone || strings.HasSuffix(name, "."+zone)
}

// addressOf resolves a name server's own address (no glue was given) with a
// nested trace.
func (t *Tracer) addressOf(ctx context.Context, name string) (netip.Addr, error) {
	t.say("          no glue for %s: resolve it first\n", name)
	t.depth++
	defer func() { t.depth-- }()
	rcode, answer, err := t.iterate(ctx, Canonical(name), TypeA)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, r := range answer {
		if r.Type == TypeA {
			return r.Addr, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("%s", RcodeName(rcode))
}

func fmtRTT(d time.Duration) string {
	return fmt.Sprintf("%.1f ms", float64(d)/1e6)
}
