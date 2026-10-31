package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"path"
	"regexp"
	"strings"
	"time"
)

// Lab is a declarative lab file: the topology to build and the checks that
// prove what the lab teaches. Nodes become network namespaces named
// "nl-<lab>-<node>".
type Lab struct {
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Nodes       []Node  `json:"nodes"`
	Links       []Link  `json:"links"`
	Checks      []Check `json:"checks"`
	// Requires names programs that checks run inside shell commands (which
	// preflight cannot see), each with how to install it.
	Requires map[string]string `json:"requires,omitempty"`
	// Setup and Teardown run in the HOST's network namespace, for labs that
	// drive host software such as Docker: Setup after the nodes and services
	// exist, Teardown whenever the lab is removed (errors ignored, so each
	// command must be safe to repeat).
	Setup [][]string `json:"setup,omitempty"`
	// Preflight commands must succeed on the host, or the lab is skipped
	// (for example, "docker info": the daemon must be running).
	Preflight [][]string `json:"preflight,omitempty"`
	Teardown  [][]string `json:"teardown,omitempty"`
}

// HostNode is the node name checks use to run in the host's own network
// namespace rather than in one of the lab's.
const HostNode = "@host"

// InNode is the command that runs args in node: inside its namespace, or
// on the host as is for HostNode.
func (l *Lab) InNode(node string, args ...string) []string {
	if node == HostNode {
		return args
	}
	return append([]string{"ip", "netns", "exec", l.Namespace(node)}, args...)
}

// Node is one network namespace.
type Node struct {
	Name    string            `json:"name"`
	Sysctls map[string]string `json:"sysctls,omitempty"`
	Bridges []Bridge          `json:"bridges,omitempty"`
	VLANs   []VLANIf          `json:"vlans,omitempty"`
	Routes  []Route           `json:"routes,omitempty"`
	// Setup commands run inside the node after the topology is built,
	// for example firewall rules.
	Setup [][]string `json:"setup,omitempty"`
	// Services are long-running programs started in the node after setup;
	// they are stopped at teardown with every other process in the namespace.
	Services []Service `json:"services,omitempty"`
	// Files are configuration files that only this node sees, keyed by a path
	// under /etc (for example "/etc/resolv.conf" or "/etc/bind/named.conf").
	// They are written to /etc/netns/<namespace>/, which `ip netns exec`
	// bind-mounts over /etc for processes in that namespace — the standard
	// iproute2 mechanism — and removed at teardown. A file in a subdirectory
	// hides the host's whole directory of that name inside the node.
	Files map[string]string `json:"files,omitempty"`
}

// Service is a background program inside a node. "${BIN}" in Run expands to
// the directory holding netlab (or $NETLAB_BIN), where the course tools live.
type Service struct {
	Name string   `json:"name"`
	Run  []string `json:"run"`
	Port int      `json:"port,omitempty"`     // wait until something listens on this TCP port
	UDP  int      `json:"udp_port,omitempty"` // or until something is bound to this UDP port
}

// Bridge is a Linux bridge — a software switch — inside a node.
type Bridge struct {
	Name          string   `json:"name"`
	STP           bool     `json:"stp,omitempty"`
	VLANFiltering bool     `json:"vlan_filtering,omitempty"`
	ForwardDelay  Duration `json:"forward_delay,omitempty"` // STP listening and learning time each
	Addr          []string `json:"addr,omitempty"`
}

// VLANIf is an 802.1Q sub-interface such as eth0.10.
type VLANIf struct {
	Name   string   `json:"name"`
	Parent string   `json:"parent"`
	ID     int      `json:"id"`
	Addr   []string `json:"addr,omitempty"`
}

// Route is a static route.
type Route struct {
	Dst string `json:"dst"`
	Via string `json:"via"`
}

// Link is a veth pair: a virtual cable between two interfaces.
type Link struct {
	A Endpoint `json:"a"`
	B Endpoint `json:"b"`
}

// Endpoint is one end of a link.
type Endpoint struct {
	Node   string     `json:"node"`
	If     string     `json:"if"`
	Addr   []string   `json:"addr,omitempty"`
	MTU    int        `json:"mtu,omitempty"`
	Bridge string     `json:"bridge,omitempty"` // enslave to this bridge in the same node
	VLANs  []PortVLAN `json:"vlans,omitempty"`  // bridge port VLANs; replaces the default VLAN 1
}

// PortVLAN is one VLAN on a bridge port. PVID marks the VLAN that untagged
// incoming frames are assigned to; Untagged sends that VLAN without a tag.
type PortVLAN struct {
	VID      int  `json:"vid"`
	PVID     bool `json:"pvid,omitempty"`
	Untagged bool `json:"untagged,omitempty"`
}

// Check is one assertion. Type selects which fields apply.
type Check struct {
	Type string `json:"type"` // ping | stp | fdb | command | capture
	Name string `json:"name,omitempty"`

	From   string `json:"from,omitempty"`   // ping
	To     string `json:"to,omitempty"`     // ping: an IP address
	Expect string `json:"expect,omitempty"` // ping: reachable | unreachable

	Nodes      []string `json:"nodes,omitempty"`  // stp
	Bridge     string   `json:"bridge,omitempty"` // stp, fdb
	Forwarding *int     `json:"forwarding,omitempty"`
	Blocking   *int     `json:"blocking,omitempty"`

	Node   string `json:"node,omitempty"`   // fdb, command
	Port   string `json:"port,omitempty"`   // fdb: bridge port that must have learned the MAC
	Learns string `json:"learns,omitempty"` // fdb: "node:interface" whose MAC must be learned

	Run       []string `json:"run,omitempty"`  // command
	Save      string   `json:"save,omitempty"` // command, capture: evidence file name
	AllowFail bool     `json:"allow_fail,omitempty"`
	// ExpectExit, when set, is the exit status the command must return
	// (for example 7 for a refused curl, 28 for a timed-out one).
	ExpectExit *int `json:"expect_exit,omitempty"`
	// ExpectContains lists strings that must appear in the command's output,
	// or in the decoded capture.
	ExpectContains []string `json:"expect_contains,omitempty"`
	// ExpectAbsent lists strings that must not appear (for example an RST
	// in a capture of a dropped connection).
	ExpectAbsent []string `json:"expect_absent,omitempty"`

	// Decode replaces the tcpdump flags used to print a capture
	// (default -e -nn -A: link headers and payload text). Use -nn -v to see
	// IP header fields such as TTL, ID, flags, and fragment offsets.
	Decode []string `json:"decode,omitempty"`

	If     string  `json:"if,omitempty"`     // capture: interface in Node
	Filter string  `json:"filter,omitempty"` // capture: tcpdump filter
	During *Action `json:"during,omitempty"` // capture: what to run while capturing

	// Timeout: for stp and fdb, how long to wait for convergence; for a
	// command, retry it every 500 ms until its expectations hold or the
	// timeout passes (for states that take time, such as router
	// advertisements or duplicate address detection).
	Timeout Duration `json:"timeout,omitempty"`
}

// Action is a command run in a node, used by capture checks.
type Action struct {
	Node string   `json:"node"`
	Run  []string `json:"run"`
}

// Duration is a time.Duration written as "4s" or "500ms" in JSON.
type Duration struct{ time.Duration }

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"4s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

var (
	labNameRE  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,19}$`)
	nodeNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,15}$`)
	ifNameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`) // Linux limit: 15 characters
)

// Load reads and validates a lab file.
func Load(r io.Reader) (*Lab, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields() // a typo in a lab file should fail, not be ignored
	var lab Lab
	if err := dec.Decode(&lab); err != nil {
		return nil, fmt.Errorf("parsing lab file: %w", err)
	}
	if err := lab.Validate(); err != nil {
		return nil, err
	}
	return &lab, nil
}

// Namespace returns the namespace name for a node.
func (l *Lab) Namespace(node string) string { return "nl-" + l.Name + "-" + node }

// NeedsIPv6 reports whether the lab uses IPv6 addresses or settings.
func (l *Lab) NeedsIPv6() bool {
	v6 := func(addrs []string) bool {
		for _, a := range addrs {
			if p, err := netip.ParsePrefix(a); err == nil && p.Addr().Is6() {
				return true
			}
		}
		return false
	}
	for _, n := range l.Nodes {
		for k := range n.Sysctls {
			if strings.HasPrefix(k, "net.ipv6.") {
				return true
			}
		}
		for _, b := range n.Bridges {
			if v6(b.Addr) {
				return true
			}
		}
		for _, v := range n.VLANs {
			if v6(v.Addr) {
				return true
			}
		}
		for _, r := range n.Routes {
			if a, err := netip.ParseAddr(r.Via); err == nil && a.Is6() {
				return true
			}
		}
	}
	for _, k := range l.Links {
		if v6(k.A.Addr) || v6(k.B.Addr) {
			return true
		}
	}
	return false
}

// NeedsVLAN reports whether the lab uses 802.1Q features.
func (l *Lab) NeedsVLAN() bool {
	for _, n := range l.Nodes {
		if len(n.VLANs) > 0 {
			return true
		}
		for _, b := range n.Bridges {
			if b.VLANFiltering {
				return true
			}
		}
	}
	for _, k := range l.Links {
		if len(k.A.VLANs) > 0 || len(k.B.VLANs) > 0 {
			return true
		}
	}
	return false
}

// Validate checks names, references, and addresses before anything is created.
func (l *Lab) Validate() error {
	for _, n := range l.Nodes {
		for p := range n.Files {
			if !strings.HasPrefix(p, "/etc/") || path.Clean(p) != p || strings.Contains(p, "..") || p == "/etc/" {
				return fmt.Errorf("node %s: file %q must be a clean path under /etc/", n.Name, p)
			}
		}
	}
	if !labNameRE.MatchString(l.Name) {
		return fmt.Errorf("lab name %q: use 1-20 lower-case letters, digits, and dashes", l.Name)
	}
	if len(l.Nodes) == 0 && len(l.Setup) == 0 {
		return fmt.Errorf("lab %q has no nodes and no host setup", l.Name)
	}
	nodes := map[string]*Node{}
	ifaces := map[string]bool{} // "node/if"
	bridges := map[string]bool{}
	addIf := func(node, name string) error {
		if !ifNameRE.MatchString(name) {
			return fmt.Errorf("interface name %q on %s: 1-15 characters, letters, digits, _ . -", name, node)
		}
		key := node + "/" + name
		if ifaces[key] {
			return fmt.Errorf("interface %s defined twice on %s", name, node)
		}
		ifaces[key] = true
		return nil
	}
	for i := range l.Nodes {
		n := &l.Nodes[i]
		if !nodeNameRE.MatchString(n.Name) {
			return fmt.Errorf("node name %q: use 1-16 lower-case letters, digits, and dashes", n.Name)
		}
		if nodes[n.Name] != nil {
			return fmt.Errorf("node %q defined twice", n.Name)
		}
		nodes[n.Name] = n
		for _, b := range n.Bridges {
			if err := addIf(n.Name, b.Name); err != nil {
				return err
			}
			if err := checkPrefixes(n.Name, b.Name, b.Addr); err != nil {
				return err
			}
			if b.ForwardDelay.Duration != 0 && (b.ForwardDelay.Duration < 2*time.Second || b.ForwardDelay.Duration > 30*time.Second) {
				return fmt.Errorf("bridge %s on %s: forward_delay must be between 2s and 30s (the kernel's STP limits)", b.Name, n.Name)
			}
			bridges[n.Name+"/"+b.Name] = true
		}
	}
	checkEnd := func(e Endpoint) error {
		if nodes[e.Node] == nil {
			return fmt.Errorf("link end %s:%s refers to an unknown node", e.Node, e.If)
		}
		if err := addIf(e.Node, e.If); err != nil {
			return err
		}
		if e.Bridge != "" && !bridges[e.Node+"/"+e.Bridge] {
			return fmt.Errorf("link end %s:%s refers to unknown bridge %q", e.Node, e.If, e.Bridge)
		}
		if len(e.VLANs) > 0 && e.Bridge == "" {
			return fmt.Errorf("link end %s:%s has vlans but is not a bridge port", e.Node, e.If)
		}
		for _, v := range e.VLANs {
			if v.VID < 1 || v.VID > 4094 {
				return fmt.Errorf("link end %s:%s: VLAN %d outside 1-4094", e.Node, e.If, v.VID)
			}
		}
		if e.MTU != 0 && (e.MTU < 68 || e.MTU > 65535) {
			return fmt.Errorf("link end %s:%s: MTU %d outside 68-65535", e.Node, e.If, e.MTU)
		}
		return checkPrefixes(e.Node, e.If, e.Addr)
	}
	for _, k := range l.Links {
		if err := checkEnd(k.A); err != nil {
			return err
		}
		if err := checkEnd(k.B); err != nil {
			return err
		}
	}
	for _, n := range l.Nodes {
		for _, v := range n.VLANs {
			if !ifaces[n.Name+"/"+v.Parent] {
				return fmt.Errorf("vlan %s on %s: parent %q is not an interface of that node", v.Name, n.Name, v.Parent)
			}
			if v.ID < 1 || v.ID > 4094 {
				return fmt.Errorf("vlan %s on %s: id %d outside 1-4094", v.Name, n.Name, v.ID)
			}
			if err := addIf(n.Name, v.Name); err != nil {
				return err
			}
			if err := checkPrefixes(n.Name, v.Name, v.Addr); err != nil {
				return err
			}
		}
		for i, c := range n.Setup {
			if len(c) == 0 {
				return fmt.Errorf("setup command %d on %s is empty", i+1, n.Name)
			}
		}
		for _, svc := range n.Services {
			if svc.Name == "" || len(svc.Run) == 0 {
				return fmt.Errorf("service on %s needs a name and a run command", n.Name)
			}
			if svc.Port < 0 || svc.Port > 65535 || svc.UDP < 0 || svc.UDP > 65535 {
				return fmt.Errorf("service %s on %s: port out of range", svc.Name, n.Name)
			}
		}
		for _, r := range n.Routes {
			if _, err := netip.ParsePrefix(r.Dst); err != nil && r.Dst != "default" {
				return fmt.Errorf("route on %s: destination %q is not a prefix", n.Name, r.Dst)
			}
			if _, err := netip.ParseAddr(r.Via); err != nil {
				return fmt.Errorf("route on %s: next hop %q is not an address", n.Name, r.Via)
			}
		}
	}
	for i, c := range l.Checks {
		if err := c.validate(nodes, ifaces); err != nil {
			return fmt.Errorf("check %d (%s): %w", i+1, c.Type, err)
		}
	}
	return nil
}

func checkPrefixes(node, ifName string, addrs []string) error {
	for _, a := range addrs {
		if _, err := netip.ParsePrefix(a); err != nil {
			return fmt.Errorf("%s on %s: address %q needs a prefix length, e.g. 10.0.0.1/24", ifName, node, a)
		}
	}
	return nil
}

func (c Check) validate(nodes map[string]*Node, ifaces map[string]bool) error {
	known := func(n string) error {
		if nodes[n] == nil && n != HostNode {
			return fmt.Errorf("unknown node %q", n)
		}
		return nil
	}
	switch c.Type {
	case "ping":
		if err := known(c.From); err != nil {
			return err
		}
		if _, err := netip.ParseAddr(c.To); err != nil {
			return fmt.Errorf("to %q is not an IP address", c.To)
		}
		if c.Expect != "reachable" && c.Expect != "unreachable" {
			return fmt.Errorf("expect must be reachable or unreachable, got %q", c.Expect)
		}
	case "stp":
		if len(c.Nodes) == 0 || c.Bridge == "" || (c.Forwarding == nil && c.Blocking == nil) {
			return fmt.Errorf("needs nodes, bridge, and forwarding and/or blocking counts")
		}
		for _, n := range c.Nodes {
			if err := known(n); err != nil {
				return err
			}
		}
	case "fdb":
		if err := known(c.Node); err != nil {
			return err
		}
		node, ifName, ok := strings.Cut(c.Learns, ":")
		if !ok || !ifaces[node+"/"+ifName] {
			return fmt.Errorf("learns %q must name an existing node:interface", c.Learns)
		}
		if c.Bridge == "" || !ifaces[c.Node+"/"+c.Port] {
			return fmt.Errorf("needs bridge and an existing port on %s", c.Node)
		}
	case "command":
		if err := known(c.Node); err != nil {
			return err
		}
		if len(c.Run) == 0 {
			return fmt.Errorf("run is empty")
		}
		if c.ExpectExit != nil && c.AllowFail {
			return fmt.Errorf("use either expect_exit or allow_fail, not both")
		}
	case "capture":
		if err := known(c.Node); err != nil {
			return err
		}
		if c.Node != HostNode && !ifaces[c.Node+"/"+c.If] {
			return fmt.Errorf("if %q is not an interface of %s", c.If, c.Node)
		}
		if c.During == nil || len(c.During.Run) == 0 {
			return fmt.Errorf("needs during: {node, run}")
		}
		if err := known(c.During.Node); err != nil {
			return err
		}
		if c.Save == "" {
			return fmt.Errorf("needs save: a pcap file name")
		}
	default:
		return fmt.Errorf("unknown check type %q", c.Type)
	}
	return nil
}
