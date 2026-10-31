package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Runner executes commands, echoing each one so the learner sees exactly
// which ip/bridge commands build the lab.
type Runner struct {
	Out   io.Writer // command echo and progress; io.Discard to silence
	Quiet bool      // don't echo commands
}

// Run executes one command and returns its combined output.
func (r *Runner) Run(ctx context.Context, args ...string) (string, error) {
	if !r.Quiet {
		fmt.Fprintf(r.Out, "+ %s\n", shellJoin(args))
	}
	return r.Silent(ctx, args...)
}

// Silent executes a command without echoing it (used for polling).
func (r *Runner) Silent(ctx context.Context, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("%s: %w: %s", shellJoin(args), err, strings.TrimSpace(out))
	}
	return out, nil
}

// Up builds the lab and starts its services, writing service output to
// logDir. On error, the caller should still call Down.
func Up(ctx context.Context, r *Runner, l *Lab, logDir string) error {
	if err := writeFiles(r, l); err != nil {
		return err
	}
	for _, c := range Plan(l) {
		if _, err := r.Run(ctx, c...); err != nil {
			return err
		}
	}
	for _, n := range l.Nodes {
		for _, svc := range n.Services {
			if err := startService(ctx, r, l, n.Name, svc, logDir); err != nil {
				return err
			}
		}
	}
	for _, c := range l.Setup {
		if _, err := r.Run(ctx, Expand(c)...); err != nil {
			return err
		}
	}
	return nil
}

// writeFiles puts each node's files where ip netns exec will overlay them.
func writeFiles(r *Runner, l *Lab) error {
	for _, n := range l.Nodes {
		for _, p := range fileList(n) {
			host := l.FilePath(n.Name, p)
			if !r.Quiet {
				fmt.Fprintf(r.Out, "+ write %s (%s sees %s)\n", host, n.Name, p)
			}
			if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(host, []byte(n.Files[p]), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// startService launches a service in the background. It is stopped by Down,
// which kills every process in the lab's namespaces.
func startService(ctx context.Context, r *Runner, l *Lab, node string, svc Service, logDir string) error {
	args := l.ServiceCommand(node, svc)
	if !r.Quiet {
		fmt.Fprintf(r.Out, "+ %s &\n", shellJoin(args))
	}
	logFile, err := os.Create(filepath.Join(logDir, "service-"+node+"-"+svc.Name+".log"))
	if err != nil {
		return err
	}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("starting service %s on %s: %w", svc.Name, node, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait(); logFile.Close() }()
	if svc.Port == 0 && svc.UDP == 0 {
		return nil
	}
	ssArgs, port := []string{"-Hltn"}, svc.Port
	if svc.Port == 0 {
		ssArgs, port = []string{"-Hlun"}, svc.UDP
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, _ := r.Silent(ctx, append(append([]string{"ip", "netns", "exec", l.Namespace(node), "ss"}, ssArgs...), fmt.Sprintf("sport = :%d", port))...)
		if strings.TrimSpace(out) != "" {
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("service %s on %s exited before listening on port %d (%v); see %s",
				svc.Name, node, port, err, logFile.Name())
		default:
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return fmt.Errorf("service %s on %s is not listening on port %d after 10s", svc.Name, node, port)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Down deletes the lab's namespaces, stopping any process still inside them.
// It is safe to call when some or all namespaces don't exist.
func Down(r *Runner, l *Lab) (deleted int) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, c := range l.Teardown {
		_, _ = r.Silent(ctx, Expand(c)...)
	}
	existing, _ := r.Silent(ctx, "ip", "netns", "list")
	for _, n := range l.Nodes {
		ns := l.Namespace(n.Name)
		if !hasNamespace(existing, ns) {
			continue
		}
		if pids, err := r.Silent(ctx, "ip", "netns", "pids", ns); err == nil {
			var killed []int
			for _, f := range strings.Fields(pids) {
				if pid, err := strconv.Atoi(f); err == nil {
					if p, err := os.FindProcess(pid); err == nil && p.Kill() == nil {
						killed = append(killed, pid)
					}
				}
			}
			waitGone(killed, 3*time.Second)
		}
		if _, err := r.Run(ctx, "ip", "netns", "delete", ns); err == nil {
			deleted++
		}
	}
	for _, n := range l.Nodes {
		if len(n.Files) > 0 {
			_ = os.RemoveAll(filepath.Join("/etc/netns", l.Namespace(n.Name)))
		}
	}
	return deleted
}

func hasNamespace(list, name string) bool {
	for _, line := range strings.Split(list, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return true
		}
	}
	return false
}

// ErrSkipped means the lab cannot run on this machine; the reason says why.
var ErrSkipped = errors.New("skipped")

// Preflight confirms root, the required tools, and kernel features before
// anything is created, so a missing feature is reported as such rather than
// as a confusing failure halfway through setup.
func Preflight(ctx context.Context, r *Runner, l *Lab) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("%w: network namespaces need root; run inside the lab VM with sudo", ErrSkipped)
	}
	need := []string{"ip"}
	for _, c := range l.Checks {
		switch c.Type {
		case "ping":
			need = append(need, "ping")
		case "stp", "fdb":
			need = append(need, "bridge")
		case "command":
			need = append(need, Expand(c.Run)[0])
		case "capture":
			need = append(need, "tcpdump", Expand(c.During.Run)[0])
		}
	}
	for _, c := range l.Setup {
		need = append(need, Expand(c)[0])
	}
	for _, n := range l.Nodes {
		for _, c := range n.Setup {
			need = append(need, Expand(c)[0])
		}
		for _, svc := range n.Services {
			need = append(need, Expand(svc.Run)[0])
			if svc.Port != 0 {
				need = append(need, "ss")
			}
		}
	}
	if l.NeedsVLAN() {
		need = append(need, "bridge")
	}
	for tool, hint := range l.Requires {
		path := Expand([]string{tool})[0]
		if strings.Contains(path, "/") {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("%w: %s not found; %s", ErrSkipped, path, hint)
			}
		} else if _, err := exec.LookPath(path); err != nil {
			return fmt.Errorf("%w: %s is not installed; %s", ErrSkipped, path, hint)
		}
	}
	for _, tool := range need {
		if strings.Contains(tool, "/") {
			if info, err := os.Stat(tool); err != nil || info.IsDir() {
				hint := "build the course tools first (cd labs/go && go build -o bin/ ./cmd/...)"
				if strings.HasPrefix(tool, "/usr/lib/frr/") {
					hint = "install FRR (Ubuntu: apt-get install frr)"
				}
				return fmt.Errorf("%w: %s not found; %s", ErrSkipped, tool, hint)
			}
			continue
		}
		if _, err := exec.LookPath(tool); err != nil {
			pkg := "iproute2 iputils-ping iputils-arping tcpdump iptables curl dnsmasq-base ethtool"
			switch tool {
			case "named":
				pkg = "bind9 bind9-utils"
			case "unbound":
				pkg = "unbound"
			case "dig":
				pkg = "bind9-dnsutils"
			}
			return fmt.Errorf("%w: %s is not installed (Ubuntu: apt-get install %s)", ErrSkipped, tool, pkg)
		}
	}
	for _, c := range l.Preflight {
		if out, err := r.Silent(ctx, Expand(c)...); err != nil {
			first := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
			return fmt.Errorf("%w: %s failed: %s", ErrSkipped, shellJoin(c), first)
		}
	}
	// ip netns exec can only bind-mount over /etc entries that exist on the
	// host, so a node file whose top-level entry is missing would silently not
	// appear inside the node.
	for _, n := range l.Nodes {
		for p := range n.Files {
			top := "/etc/" + strings.SplitN(strings.TrimPrefix(p, "/etc/"), "/", 2)[0]
			if _, err := os.Stat(top); err != nil {
				return fmt.Errorf("%w: node %s needs %s, but %s does not exist on this host (ip netns exec only overlays existing /etc entries; is the package installed?)", ErrSkipped, n.Name, p, top)
			}
		}
	}
	if l.NeedsIPv6() {
		if _, err := os.Stat("/proc/sys/net/ipv6"); err != nil {
			reason := "this kernel has IPv6 disabled or not built in"
			if cmdline, _ := os.ReadFile("/proc/cmdline"); strings.Contains(string(cmdline), "ipv6.disable=1") {
				reason = "IPv6 is disabled on the kernel command line (ipv6.disable=1)"
			}
			return fmt.Errorf("%w: %s; stock Ubuntu kernels have IPv6 enabled", ErrSkipped, reason)
		}
	}
	if !l.NeedsVLAN() {
		return nil
	}
	probe := fmt.Sprintf("nl-probe-%d", os.Getpid())
	if _, err := r.Silent(ctx, "ip", "netns", "add", probe); err != nil {
		return err
	}
	defer r.Silent(context.Background(), "ip", "netns", "delete", probe) //nolint:errcheck
	if _, err := r.Silent(ctx, "ip", "-n", probe, "link", "add", "br0", "type", "bridge", "vlan_filtering", "1"); err != nil {
		return fmt.Errorf("%w: this kernel cannot create a VLAN-filtering bridge (CONFIG_BRIDGE_VLAN_FILTERING); stock Ubuntu kernels can", ErrSkipped)
	}
	if _, err := r.Silent(ctx, "ip", "-n", probe, "link", "add", "p0", "type", "veth", "peer", "name", "p1"); err != nil {
		return err
	}
	if _, err := r.Silent(ctx, "ip", "-n", probe, "link", "add", "link", "p0", "name", "p0.10", "type", "vlan", "id", "10"); err != nil {
		return fmt.Errorf("%w: this kernel has no 802.1Q VLAN support (CONFIG_VLAN_8021Q); stock Ubuntu kernels have it", ErrSkipped)
	}
	return nil
}

// Result is the outcome of one check.
type Result struct {
	Check  Check
	Pass   bool
	Detail string
	Took   time.Duration
	output string // for retried commands: the final attempt's output
}

// Session runs a lab end to end and records evidence.
type Session struct {
	Runner      *Runner
	Lab         *Lab
	EvidenceDir string
}

// Run builds the lab, runs every check, and always tears the lab down unless
// keep is set. It returns the check results; err is for setup failures.
func (s *Session) Run(ctx context.Context, keep bool) ([]Result, error) {
	if err := Preflight(ctx, s.Runner, s.Lab); err != nil {
		return nil, err
	}
	// Commands in checks can read earlier evidence (pcaps) from here.
	os.Setenv("NETLAB_EVIDENCE", s.EvidenceDir)
	if !keep {
		defer func() {
			n := Down(s.Runner, s.Lab)
			fmt.Fprintf(s.Runner.Out, "teardown: deleted %d namespace(s)\n", n)
		}()
	}
	Down(&Runner{Out: io.Discard, Quiet: true}, s.Lab) // leftovers from an interrupted run
	if err := os.WriteFile(filepath.Join(s.EvidenceDir, "build.sh"), []byte(ScriptWithServices(s.Lab)), 0o644); err != nil {
		return nil, err
	}
	if err := Up(ctx, s.Runner, s.Lab, s.EvidenceDir); err != nil {
		return nil, fmt.Errorf("building the lab: %w", err)
	}
	var results []Result
	for i, c := range s.Lab.Checks {
		if ctx.Err() != nil {
			return results, ctx.Err()
		}
		fmt.Fprintf(s.Runner.Out, "\ncheck %d/%d: %s\n", i+1, len(s.Lab.Checks), c.Describe())
		start := time.Now()
		res := s.check(ctx, c)
		res.Took = time.Since(start)
		verdict := "PASS"
		if !res.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(s.Runner.Out, "  %s after %s: %s\n", verdict, res.Took.Round(10*time.Millisecond), res.Detail)
		results = append(results, res)
	}
	return results, nil
}

// Describe summarises a check in one line.
func (c Check) Describe() string {
	if c.Name != "" {
		return c.Name
	}
	switch c.Type {
	case "ping":
		return fmt.Sprintf("ping from %s to %s is %s", c.From, c.To, c.Expect)
	case "stp":
		return fmt.Sprintf("STP on %s in %s converges", c.Bridge, strings.Join(c.Nodes, ", "))
	case "fdb":
		return fmt.Sprintf("%s on %s learns %s's MAC on port %s", c.Bridge, c.Node, c.Learns, c.Port)
	case "command":
		return fmt.Sprintf("on %s: %s", c.Node, shellJoin(c.Run))
	case "capture":
		return fmt.Sprintf("capture on %s:%s while %s runs %s", c.Node, c.If, c.During.Node, shellJoin(c.During.Run))
	}
	return c.Type
}

// waitGone waits until the processes have exited (or are zombies awaiting
// their parent), so a lab is really gone when Down returns.
func waitGone(pids []int, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for _, pid := range pids {
		for time.Now().Before(deadline) {
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil {
				break // exited and reaped
			}
			// The state field follows the command name in parentheses.
			if i := strings.LastIndexByte(string(stat), ')'); i > 0 && i+2 < len(stat) && stat[i+2] == 'Z' {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}
