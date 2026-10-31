package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

func (s *Session) check(ctx context.Context, c Check) Result {
	timeout := c.Timeout.Duration
	switch c.Type {
	case "ping":
		return s.ping(ctx, c)
	case "stp":
		if timeout == 0 {
			timeout = 60 * time.Second
		}
		return s.stp(ctx, c, timeout)
	case "fdb":
		if timeout == 0 {
			timeout = 5 * time.Second
		}
		return s.fdb(ctx, c, timeout)
	case "command":
		if timeout == 0 {
			return s.command(ctx, c, true)
		}
		return s.retryCommand(ctx, c, timeout)
	case "capture":
		return s.capture(ctx, c)
	}
	return Result{Check: c, Detail: "unknown check type"}
}

var pingSummary = regexp.MustCompile(`(\d+) packets transmitted, (\d+) received`)

func (s *Session) ping(ctx context.Context, c Check) Result {
	out, err := s.Runner.Run(ctx, "ip", "netns", "exec", s.Lab.Namespace(c.From),
		"ping", "-c", "2", "-W", "1", c.To)
	m := pingSummary.FindStringSubmatch(out)
	got := "unreachable"
	detail := "no ping summary"
	if m != nil {
		detail = fmt.Sprintf("%s/%s replies", m[2], m[1])
		if m[2] != "0" {
			got = "reachable"
		}
	} else if err != nil {
		detail = strings.TrimSpace(out)
	}
	return Result{Check: c, Pass: got == c.Expect, Detail: fmt.Sprintf("%s (%s)", got, detail)}
}

// bridgePort is one element of `bridge -j link show`.
type bridgePort struct {
	IfName string `json:"ifname"`
	Master string `json:"master"`
	State  string `json:"state"`
}

// CountSTP counts port states of one bridge from `bridge -j link show` output.
func CountSTP(out []byte, bridge string) (map[string]int, []string, error) {
	var ports []bridgePort
	if err := json.Unmarshal(out, &ports); err != nil {
		return nil, nil, fmt.Errorf("parsing bridge link output: %w", err)
	}
	counts := map[string]int{}
	var desc []string
	for _, p := range ports {
		if p.Master != bridge {
			continue
		}
		counts[p.State]++
		desc = append(desc, p.IfName+"="+p.State)
	}
	return counts, desc, nil
}

func (s *Session) stp(ctx context.Context, c Check, timeout time.Duration) Result {
	deadline := time.Now().Add(timeout)
	var last []string
	for {
		total := map[string]int{}
		last = last[:0]
		ok := true
		for _, n := range c.Nodes {
			out, err := s.Runner.Silent(ctx, "ip", "netns", "exec", s.Lab.Namespace(n), "bridge", "-j", "link", "show")
			if err != nil {
				return Result{Check: c, Detail: err.Error()}
			}
			counts, desc, err := CountSTP([]byte(out), c.Bridge)
			if err != nil {
				return Result{Check: c, Detail: err.Error()}
			}
			for k, v := range counts {
				total[k] += v
			}
			last = append(last, n+": "+strings.Join(desc, " "))
		}
		if c.Forwarding != nil && total["forwarding"] != *c.Forwarding {
			ok = false
		}
		if c.Blocking != nil && total["blocking"] != *c.Blocking {
			ok = false
		}
		if ok {
			return Result{Check: c, Pass: true, Detail: strings.Join(last, "; ")}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return Result{Check: c, Detail: "not converged; last seen " + strings.Join(last, "; ")}
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// fdbEntry is one element of `bridge -j fdb show`.
type fdbEntry struct {
	MAC    string `json:"mac"`
	IfName string `json:"ifname"`
	Master string `json:"master"`
	State  string `json:"state"`
}

// Learned reports whether a bridge learned mac on port (a dynamic entry, not
// the port's own permanent address).
func Learned(out []byte, bridge, port, mac string) (bool, error) {
	var entries []fdbEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return false, fmt.Errorf("parsing fdb output: %w", err)
	}
	for _, e := range entries {
		if e.Master == bridge && e.IfName == port && strings.EqualFold(e.MAC, mac) && e.State != "permanent" {
			return true, nil
		}
	}
	return false, nil
}

func (s *Session) fdb(ctx context.Context, c Check, timeout time.Duration) Result {
	node, ifName, _ := strings.Cut(c.Learns, ":")
	out, err := s.Runner.Silent(ctx, "ip", "-n", s.Lab.Namespace(node), "-j", "link", "show", ifName)
	if err != nil {
		return Result{Check: c, Detail: err.Error()}
	}
	var links []struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal([]byte(out), &links); err != nil || len(links) != 1 {
		return Result{Check: c, Detail: "could not read the MAC of " + c.Learns}
	}
	mac := links[0].Address
	deadline := time.Now().Add(timeout)
	for {
		out, err := s.Runner.Silent(ctx, "ip", "netns", "exec", s.Lab.Namespace(c.Node), "bridge", "-j", "fdb", "show", "br", c.Bridge)
		if err != nil {
			return Result{Check: c, Detail: err.Error()}
		}
		ok, err := Learned([]byte(out), c.Bridge, c.Port, mac)
		if err != nil {
			return Result{Check: c, Detail: err.Error()}
		}
		if ok {
			return Result{Check: c, Pass: true, Detail: fmt.Sprintf("%s learned on %s", mac, c.Port)}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return Result{Check: c, Detail: fmt.Sprintf("%s not in the forwarding database on %s", mac, c.Port)}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// retryCommand reruns a command until it passes or the timeout expires,
// echoing it once and showing only the final attempt's output.
func (s *Session) retryCommand(ctx context.Context, c Check, timeout time.Duration) Result {
	deadline := time.Now().Add(timeout)
	attempts := 0
	for {
		attempts++
		res := s.command(ctx, c, attempts == 1 && !s.Runner.Quiet)
		if res.Pass || time.Now().After(deadline) || ctx.Err() != nil {
			printIndented(s, res.output, 0) // the attempt that decided the check
			if attempts > 1 {
				res.Detail += fmt.Sprintf(" (attempt %d)", attempts)
			}
			return res
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (s *Session) command(ctx context.Context, c Check, echo bool) Result {
	args := s.Lab.InNode(c.Node, Expand(c.Run)...)
	var out string
	var err error
	if echo {
		out, err = s.Runner.Run(ctx, args...)
	} else {
		out, err = s.Runner.Silent(ctx, args...)
	}
	if c.Save != "" {
		if werr := os.WriteFile(filepath.Join(s.EvidenceDir, filepath.Base(c.Save)), []byte(out), 0o644); werr != nil {
			return Result{Check: c, Detail: werr.Error()}
		}
	}
	if c.Timeout.Duration == 0 {
		printIndented(s, out, 0)
	}
	res := s.judge(c, out, err)
	res.output = out
	return res
}

// judge compares a command's output and exit status with the check.
func (s *Session) judge(c Check, out string, err error) Result {
	code := exitCode(err)
	switch {
	case c.ExpectExit != nil && code != *c.ExpectExit:
		return Result{Check: c, Detail: fmt.Sprintf("exit status %d, want %d", code, *c.ExpectExit)}
	case c.ExpectExit == nil && err != nil && !c.AllowFail:
		return Result{Check: c, Detail: "command failed: " + err.Error()}
	}
	if missing := missingStrings(out, c.ExpectContains); len(missing) > 0 {
		return Result{Check: c, Detail: "output lacks " + strings.Join(quoteAll(missing), ", ")}
	}
	if found := presentStrings(out, c.ExpectAbsent); len(found) > 0 {
		return Result{Check: c, Detail: "output unexpectedly contains " + strings.Join(quoteAll(found), ", ")}
	}
	detail := fmt.Sprintf("exit status %d", code)
	if c.Save != "" {
		detail += ", saved to " + filepath.Base(c.Save)
	}
	return Result{Check: c, Pass: true, Detail: detail}
}

// capture runs tcpdump in one node while a command runs (often in another),
// saves the pcap, decodes it with link-layer headers and payload text, and
// checks the decode for expected strings.
func (s *Session) capture(ctx context.Context, c Check) Result {
	pcap := filepath.Join(s.EvidenceDir, filepath.Base(c.Save))
	args := s.Lab.InNode(c.Node, "tcpdump", "--immediate-mode", "-Z", "root", "-U", "-n", "-i", Expand([]string{c.If})[0], "-w", pcap)
	if c.Filter != "" {
		args = append(args, c.Filter)
	}
	if !s.Runner.Quiet {
		fmt.Fprintf(s.Runner.Out, "+ %s &\n", shellJoin(args))
	}
	stderr := &lockedBuffer{}
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return Result{Check: c, Detail: "starting tcpdump: " + err.Error()}
	}
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	ready := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "listening on") {
		select {
		case err := <-waitErr:
			return Result{Check: c, Detail: fmt.Sprintf("tcpdump exited: %v: %s", err, strings.TrimSpace(stderr.String()))}
		case <-time.After(50 * time.Millisecond):
		}
		if time.Now().After(ready) {
			_ = cmd.Process.Kill()
			return Result{Check: c, Detail: "tcpdump did not start listening within 5s"}
		}
	}

	during := s.Lab.InNode(c.During.Node, Expand(c.During.Run)...)
	out, runErr := s.Runner.Run(ctx, during...)
	printIndented(s, out, 0)
	time.Sleep(300 * time.Millisecond) // let the last packets arrive
	_ = cmd.Process.Signal(os.Interrupt)
	select {
	case <-waitErr:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-waitErr
	}

	flags := c.Decode
	if len(flags) == 0 {
		flags = []string{"-e", "-nn", "-A"}
	}
	decoded, err := s.Runner.Run(ctx, append(append([]string{"tcpdump"}, flags...), "-r", pcap)...)
	if err != nil {
		return Result{Check: c, Detail: "reading the capture: " + err.Error()}
	}
	_ = os.WriteFile(pcap+".txt", []byte(decoded), 0o644)
	printIndented(s, decoded, 40)
	// Expectations match the decoded packets and the command's own output,
	// so one check can assert what was on the wire and what the user saw.
	evidence := decoded + "\n" + out
	if missing := missingStrings(evidence, c.ExpectContains); len(missing) > 0 {
		return Result{Check: c, Detail: "capture and command output lack " + strings.Join(quoteAll(missing), ", ")}
	}
	if found := presentStrings(evidence, c.ExpectAbsent); len(found) > 0 {
		return Result{Check: c, Detail: "capture or command output unexpectedly contains " + strings.Join(quoteAll(found), ", ")}
	}
	packets := 0
	for _, line := range strings.Split(decoded, "\n") {
		if len(line) > 0 && line[0] >= '0' && line[0] <= '9' {
			packets++
		}
	}
	detail := fmt.Sprintf("%d packets in %s (decoded: %s.txt)", packets, filepath.Base(pcap), filepath.Base(pcap))
	if runErr != nil {
		detail += fmt.Sprintf("; the command exited with status %d", exitCode(runErr))
	}
	return Result{Check: c, Pass: true, Detail: detail}
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func missingStrings(out string, want []string) []string {
	var missing []string
	for _, w := range want {
		if !strings.Contains(out, w) {
			missing = append(missing, w)
		}
	}
	return missing
}

func presentStrings(out string, unwanted []string) []string {
	var found []string
	for _, u := range unwanted {
		if strings.Contains(out, u) {
			found = append(found, u)
		}
	}
	return found
}

func quoteAll(ss []string) []string {
	q := make([]string, len(ss))
	for i, s := range ss {
		q[i] = fmt.Sprintf("%q", s)
	}
	return q
}

// printIndented echoes command output under the check; max limits the lines
// shown (0 = all), and the full text is always in the evidence directory.
func printIndented(s *Session, out string, max int) {
	out = strings.TrimSpace(out)
	if out == "" {
		return
	}
	lines := strings.Split(out, "\n")
	shown := lines
	if max > 0 && len(lines) > max {
		shown = lines[:max]
	}
	for _, line := range shown {
		fmt.Fprintf(s.Runner.Out, "  | %s\n", line)
	}
	if len(shown) < len(lines) {
		fmt.Fprintf(s.Runner.Out, "  | … %d more lines in the evidence directory\n", len(lines)-len(shown))
	}
}

// lockedBuffer is a bytes.Buffer safe to read while a process writes to it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
