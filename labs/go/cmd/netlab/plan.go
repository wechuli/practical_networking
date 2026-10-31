package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Plan returns the commands that build the lab, in order. It is a pure
// function of the lab file, so it can be tested without root and printed as a
// script the learner can read or rerun by hand.
func Plan(l *Lab) [][]string {
	var cmds [][]string
	add := func(args ...string) { cmds = append(cmds, args) }
	inNS := func(node string, args ...string) {
		add(append([]string{"ip", "netns", "exec", l.Namespace(node)}, args...)...)
	}
	ipn := func(node string, args ...string) {
		add(append([]string{"ip", "-n", l.Namespace(node)}, args...)...)
	}

	for _, n := range l.Nodes {
		add("ip", "netns", "add", l.Namespace(n.Name))
		ipn(n.Name, "link", "set", "lo", "up")
		keys := make([]string, 0, len(n.Sysctls))
		for k := range n.Sysctls {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			inNS(n.Name, "sysctl", "-qw", k+"="+n.Sysctls[k])
		}
		for _, b := range n.Bridges {
			args := []string{"link", "add", b.Name, "type", "bridge"}
			if b.STP {
				args = append(args, "stp_state", "1")
			}
			if b.VLANFiltering {
				args = append(args, "vlan_filtering", "1")
			}
			if d := b.ForwardDelay.Duration; d != 0 {
				// The kernel takes STP timers in centiseconds.
				args = append(args, "forward_delay", strconv.FormatInt(d.Milliseconds()/10, 10))
			}
			ipn(n.Name, args...)
			for _, a := range b.Addr {
				ipn(n.Name, "address", "add", a, "dev", b.Name)
			}
			ipn(n.Name, "link", "set", b.Name, "up")
		}
	}

	for _, k := range l.Links {
		ipn(k.A.Node, "link", "add", k.A.If, "type", "veth", "peer", "name", k.B.If, "netns", l.Namespace(k.B.Node))
		for _, e := range []Endpoint{k.A, k.B} {
			if e.MTU != 0 {
				ipn(e.Node, "link", "set", e.If, "mtu", strconv.Itoa(e.MTU))
			}
			if e.Bridge != "" {
				ipn(e.Node, "link", "set", e.If, "master", e.Bridge)
				if len(e.VLANs) > 0 {
					inNS(e.Node, "bridge", "vlan", "del", "dev", e.If, "vid", "1")
					for _, v := range e.VLANs {
						args := []string{"bridge", "vlan", "add", "dev", e.If, "vid", strconv.Itoa(v.VID)}
						if v.PVID {
							args = append(args, "pvid")
						}
						if v.Untagged {
							args = append(args, "untagged")
						}
						inNS(e.Node, args...)
					}
				}
			}
			for _, a := range e.Addr {
				ipn(e.Node, "address", "add", a, "dev", e.If)
			}
			ipn(e.Node, "link", "set", e.If, "up")
		}
	}

	for _, n := range l.Nodes {
		for _, v := range n.VLANs {
			ipn(n.Name, "link", "add", "link", v.Parent, "name", v.Name, "type", "vlan", "id", strconv.Itoa(v.ID))
			for _, a := range v.Addr {
				ipn(n.Name, "address", "add", a, "dev", v.Name)
			}
			ipn(n.Name, "link", "set", v.Name, "up")
		}
		for _, r := range n.Routes {
			ipn(n.Name, "route", "add", r.Dst, "via", r.Via)
		}
	}
	for _, n := range l.Nodes {
		for _, c := range n.Setup {
			inNS(n.Name, Expand(c)...)
		}
	}
	return cmds
}

// ServiceCommand is the command that starts a service in its node.
func (l *Lab) ServiceCommand(node string, svc Service) []string {
	return append([]string{"ip", "netns", "exec", l.Namespace(node)}, Expand(svc.Run)...)
}

// Expand replaces "${BIN}" with the course tool directory, "${EVIDENCE}"
// with the run's evidence directory ($NETLAB_EVIDENCE), where setup
// commands and services can share generated files such as keys, "${LABS}"
// with the course's labs directory, and "${PYTHON}" with the course venv's
// Python interpreter.
func Expand(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		a = strings.ReplaceAll(a, "${BIN}", BinDir())
		a = strings.ReplaceAll(a, "${LABS}", LabsDir())
		a = strings.ReplaceAll(a, "${PYTHON}", Python())
		out[i] = strings.ReplaceAll(a, "${EVIDENCE}", os.Getenv("NETLAB_EVIDENCE"))
	}
	return out
}

// LabsDir is $NETLAB_LABS, or labs relative to the tools
// (labs/go/bin).
func LabsDir() string {
	if d := os.Getenv("NETLAB_LABS"); d != "" {
		return d
	}
	return filepath.Clean(filepath.Join(BinDir(), "..", ".."))
}

// Python is $NETLAB_PYTHON, or the course venv (.venv at the repository root) when it
// exists, or python3.
func Python() string {
	if p := os.Getenv("NETLAB_PYTHON"); p != "" {
		return p
	}
	venv := filepath.Join(LabsDir(), "..", ".venv", "bin", "python")
	if _, err := os.Stat(venv); err == nil {
		return filepath.Clean(venv)
	}
	return "python3"
}

// BinDir is $NETLAB_BIN, or the directory containing the netlab executable.
func BinDir() string {
	if d := os.Getenv("NETLAB_BIN"); d != "" {
		return d
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Dir(exe)
	}
	return "."
}

// Script renders commands as a shell script for the evidence directory.
func Script(cmds [][]string) string {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n# Commands netlab ran to build this lab, in order.\nset -euo pipefail\n")
	for _, c := range cmds {
		b.WriteString(shellJoin(c) + "\n")
	}
	return b.String()
}

// FilePath is where a node's file is stored on the host: under
// /etc/netns/<namespace>/, which ip netns exec overlays on /etc.
func (l *Lab) FilePath(node, p string) string {
	return filepath.Join("/etc/netns", l.Namespace(node), strings.TrimPrefix(p, "/etc/"))
}

// fileList returns a node's file paths in a stable order.
func fileList(n Node) []string {
	ps := make([]string, 0, len(n.Files))
	for p := range n.Files {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// ScriptWithServices adds the per-node files and background services to the
// build script.
func ScriptWithServices(l *Lab) string {
	s := Script(Plan(l))
	var files strings.Builder
	for _, n := range l.Nodes {
		for _, p := range fileList(n) {
			host := l.FilePath(n.Name, p)
			files.WriteString("# " + n.Name + " sees this as " + p + "\n")
			files.WriteString("mkdir -p " + shellJoin([]string{filepath.Dir(host)}) + "\n")
			files.WriteString("cat > " + shellJoin([]string{host}) + " <<'NETLAB_EOF'\n" + strings.TrimSuffix(n.Files[p], "\n") + "\nNETLAB_EOF\n")
		}
	}
	if files.Len() > 0 {
		head := "set -euo pipefail\n"
		s = strings.Replace(s, head, head+files.String(), 1)
	}
	for _, n := range l.Nodes {
		for _, svc := range n.Services {
			s += "# service " + svc.Name + "\n" + shellJoin(l.ServiceCommand(n.Name, svc)) + " &\n"
		}
	}
	for i, c := range l.Setup {
		if i == 0 {
			s += "# host setup (the host's own namespace)\n"
		}
		s += shellJoin(Expand(c)) + "\n"
	}
	return s
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		safe := a != ""
		for _, r := range a {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=@,", r)) {
				safe = false
				break
			}
		}
		if safe {
			quoted[i] = a
		} else {
			quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(quoted, " ")
}
