package main

import (
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// State is what kube-proxy learns from the API server: Services and their
// EndpointSlices, plus the facts about the node it runs on.
type State struct {
	ClusterCIDR string            `json:"clusterCIDR"`
	Nodes       map[string]string `json:"nodes"` // node name -> node IP
	Services    []Service         `json:"services"`
}

// Service is one Service with its endpoints.
type Service struct {
	Namespace             string     `json:"namespace"`
	Name                  string     `json:"name"`
	ClusterIP             string     `json:"clusterIP"`
	Ports                 []Port     `json:"ports"`
	ExternalTrafficPolicy string     `json:"externalTrafficPolicy,omitempty"` // Cluster (default) or Local
	Endpoints             []Endpoint `json:"endpoints"`
}

// Port is one service port.
type Port struct {
	Name       string `json:"name"`
	Port       int    `json:"port"`
	TargetPort int    `json:"targetPort,omitempty"` // defaults to Port
	Protocol   string `json:"protocol,omitempty"`   // TCP (default) or UDP
	NodePort   int    `json:"nodePort,omitempty"`
}

// Endpoint is one backend address.
type Endpoint struct {
	IP    string `json:"ip"`
	Node  string `json:"node"`
	Ready *bool  `json:"ready,omitempty"` // default true
}

// LoadState parses a state file.
func LoadState(r io.Reader) (*State, error) {
	var s State
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, err
	}
	for i := range s.Services {
		svc := &s.Services[i]
		for j := range svc.Ports {
			p := &svc.Ports[j]
			if p.Protocol == "" {
				p.Protocol = "TCP"
			}
			if p.TargetPort == 0 {
				p.TargetPort = p.Port
			}
		}
		if svc.ExternalTrafficPolicy == "" {
			svc.ExternalTrafficPolicy = "Cluster"
		}
	}
	return &s, nil
}

// chainHash is kube-proxy's chain-name hash: the first 16 characters of the
// base32 SHA-256 of the service port name and protocol (plus the endpoint
// for SEP chains).
func chainHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "")))
	return base32.StdEncoding.EncodeToString(sum[:])[:16]
}

// Rules renders the nat and filter tables, as iptables-restore input, that
// kube-proxy's iptables mode programs on node for these Services. The chain
// layout, names, comments and match order follow kube-proxy (Kubernetes
// 1.28 and later); only the KUBE-* chains are written, so the rest of the
// node's tables are untouched.
func (s *State) Rules(node string) string {
	var nat, filter, natChains, filterChains []string
	natChain := func(n string) { natChains = append(natChains, ":"+n+" - [0:0]") }
	filterChain := func(n string) { filterChains = append(filterChains, ":"+n+" - [0:0]") }
	for _, c := range []string{"KUBE-SERVICES", "KUBE-NODEPORTS", "KUBE-POSTROUTING", "KUBE-MARK-MASQ"} {
		natChain(c)
	}
	for _, c := range []string{"KUBE-SERVICES", "KUBE-EXTERNAL-SERVICES", "KUBE-NODEPORTS", "KUBE-FORWARD"} {
		filterChain(c)
	}
	nat = append(nat,
		`-A KUBE-MARK-MASQ -j MARK --set-xmark 0x4000/0x4000`,
		`-A KUBE-POSTROUTING -m mark ! --mark 0x4000/0x4000 -j RETURN`,
		`-A KUBE-POSTROUTING -j MARK --set-xmark 0x4000/0x0`,
		`-A KUBE-POSTROUTING -m comment --comment "kubernetes service traffic requiring SNAT" -j MASQUERADE --random-fully`,
	)
	filter = append(filter,
		`-A KUBE-FORWARD -m conntrack --ctstate INVALID -j DROP`,
		`-A KUBE-FORWARD -m comment --comment "kubernetes forwarding rules" -m mark --mark 0x4000/0x4000 -j ACCEPT`,
		`-A KUBE-FORWARD -m comment --comment "kubernetes forwarding conntrack rule" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT`,
	)
	var nodePorts []string
	for _, svc := range s.Services {
		for _, p := range svc.Ports {
			spn := fmt.Sprintf("%s/%s:%s", svc.Namespace, svc.Name, p.Name)
			proto := strings.ToLower(p.Protocol)
			svcChain := "KUBE-SVC-" + chainHash(spn, proto)
			extChain := "KUBE-EXT-" + chainHash(spn, proto)
			svlChain := "KUBE-SVL-" + chainHash(spn, proto)
			var ready, local []Endpoint
			for _, e := range svc.Endpoints {
				if e.Ready == nil || *e.Ready {
					ready = append(ready, e)
					if e.Node == node {
						local = append(local, e)
					}
				}
			}
			match := fmt.Sprintf("-d %s/32 -p %s -m comment --comment \"%s cluster IP\" -m %s --dport %d", svc.ClusterIP, proto, spn, proto, p.Port)
			if len(ready) == 0 {
				// No endpoints: reject, so clients get "connection refused" at once.
				filter = append(filter, fmt.Sprintf("-A KUBE-SERVICES -d %s/32 -p %s -m comment --comment \"%s has no endpoints\" -m %s --dport %d -j REJECT --reject-with icmp-port-unreachable", svc.ClusterIP, proto, spn, proto, p.Port))
				if p.NodePort != 0 {
					filter = append(filter, fmt.Sprintf("-A KUBE-EXTERNAL-SERVICES -p %s -m comment --comment \"%s has no endpoints\" -m addrtype --dst-type LOCAL -m %s --dport %d -j REJECT --reject-with icmp-port-unreachable", proto, spn, proto, p.NodePort))
				}
				continue
			}
			natChain(svcChain)
			nat = append(nat, "-A KUBE-SERVICES "+match+" -j "+svcChain)
			nat = append(nat, fmt.Sprintf("-A %s ! -s %s %s -j KUBE-MARK-MASQ", svcChain, s.ClusterCIDR, match))
			sepFor := func(e Endpoint) string {
				return "KUBE-SEP-" + chainHash(spn, proto, fmt.Sprintf("%s:%d", e.IP, p.TargetPort))
			}
			writeLB := func(chain string, eps []Endpoint) {
				for i, e := range eps {
					rule := fmt.Sprintf("-A %s -m comment --comment \"%s -> %s:%d\"", chain, spn, e.IP, p.TargetPort)
					if left := len(eps) - i; left > 1 {
						rule += fmt.Sprintf(" -m statistic --mode random --probability %.11f", 1/float64(left))
					}
					nat = append(nat, rule+" -j "+sepFor(e))
				}
			}
			writeLB(svcChain, ready)
			for _, e := range ready {
				sep := sepFor(e)
				natChain(sep)
				nat = append(nat,
					fmt.Sprintf("-A %s -s %s/32 -m comment --comment \"%s\" -j KUBE-MARK-MASQ", sep, e.IP, spn),
					fmt.Sprintf("-A %s -p %s -m comment --comment \"%s\" -m %s -j DNAT --to-destination %s:%d", sep, proto, spn, proto, e.IP, p.TargetPort))
			}
			if p.NodePort == 0 {
				continue
			}
			natChain(extChain)
			nodePorts = append(nodePorts, fmt.Sprintf("-A KUBE-NODEPORTS -p %s -m comment --comment \"%s\" -m %s --dport %d -j %s", proto, spn, proto, p.NodePort, extChain))
			if svc.ExternalTrafficPolicy == "Local" {
				// Pod and host traffic still goes anywhere; external traffic only
				// to endpoints on this node, without SNAT (the client IP survives).
				nat = append(nat,
					fmt.Sprintf("-A %s -s %s -m comment --comment \"pod traffic for %s external destinations\" -j %s", extChain, s.ClusterCIDR, spn, svcChain),
					fmt.Sprintf("-A %s -m comment --comment \"masquerade LOCAL traffic for %s external destinations\" -m addrtype --src-type LOCAL -j KUBE-MARK-MASQ", extChain, spn),
					fmt.Sprintf("-A %s -m comment --comment \"route LOCAL traffic for %s external destinations\" -m addrtype --src-type LOCAL -j %s", extChain, spn, svcChain))
				if len(local) == 0 {
					filter = append(filter, fmt.Sprintf("-A KUBE-EXTERNAL-SERVICES -p %s -m comment --comment \"%s has no local endpoints\" -m addrtype --dst-type LOCAL -m %s --dport %d -j DROP", proto, spn, proto, p.NodePort))
					continue
				}
				natChain(svlChain)
				nat = append(nat, fmt.Sprintf("-A %s -j %s", extChain, svlChain))
				writeLB(svlChain, local)
			} else {
				nat = append(nat,
					fmt.Sprintf("-A %s -m comment --comment \"masquerade traffic for %s external destinations\" -j KUBE-MARK-MASQ", extChain, spn),
					fmt.Sprintf("-A %s -j %s", extChain, svcChain))
			}
		}
	}
	nat = append(nat, nodePorts...)
	nat = append(nat, `-A KUBE-SERVICES -m comment --comment "kubernetes service nodeports; NOTE: this must be the last rule in this chain" -m addrtype --dst-type LOCAL -j KUBE-NODEPORTS`)
	sort.Strings(natChains[4:])
	var b strings.Builder
	b.WriteString("*filter\n" + strings.Join(filterChains, "\n") + "\n" + strings.Join(filter, "\n") + "\nCOMMIT\n")
	b.WriteString("*nat\n" + strings.Join(natChains, "\n") + "\n" + strings.Join(nat, "\n") + "\nCOMMIT\n")
	return b.String()
}

// Hooks are the jumps from the built-in chains into kube-proxy's chains,
// as table and rule; they are inserted once.
var Hooks = [][]string{
	{"filter", "INPUT", "-m", "conntrack", "--ctstate", "NEW", "-m", "comment", "--comment", "kubernetes externally-visible service portals", "-j", "KUBE-EXTERNAL-SERVICES"},
	{"filter", "FORWARD", "-m", "conntrack", "--ctstate", "NEW", "-m", "comment", "--comment", "kubernetes service portals", "-j", "KUBE-SERVICES"},
	{"filter", "FORWARD", "-m", "conntrack", "--ctstate", "NEW", "-m", "comment", "--comment", "kubernetes externally-visible service portals", "-j", "KUBE-EXTERNAL-SERVICES"},
	{"filter", "FORWARD", "-m", "comment", "--comment", "kubernetes forwarding rules", "-j", "KUBE-FORWARD"},
	{"filter", "OUTPUT", "-m", "conntrack", "--ctstate", "NEW", "-m", "comment", "--comment", "kubernetes service portals", "-j", "KUBE-SERVICES"},
	{"nat", "PREROUTING", "-m", "comment", "--comment", "kubernetes service portals", "-j", "KUBE-SERVICES"},
	{"nat", "OUTPUT", "-m", "comment", "--comment", "kubernetes service portals", "-j", "KUBE-SERVICES"},
	{"nat", "POSTROUTING", "-m", "comment", "--comment", "kubernetes postrouting rules", "-j", "KUBE-POSTROUTING"},
}
