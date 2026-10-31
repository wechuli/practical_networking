// Command netlab builds network labs from declarative JSON files inside
// Linux network namespaces, runs the checks that prove what each lab teaches,
// and tears everything down again — including after Ctrl-C.
//
// Every ip and bridge command it runs is printed, so you see exactly how the
// topology is made, and saved as build.sh in the evidence directory.
//
//	sudo netlab run   ../netlab/bridge.json   # build, check, tear down
//	sudo netlab up    ../netlab/stp.json      # build and leave running
//	sudo netlab exec  ../netlab/stp.json left -- bridge link show
//	sudo netlab down  ../netlab/stp.json      # remove a lab left running
//	netlab plan       ../netlab/vlan.json     # print the commands; no root needed
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

const usage = `usage: netlab <command> [flags] LAB.json

commands:
  run    build the lab, run its checks, tear it down (-keep leaves it running)
  up     build the lab and leave it running
  check  run the checks against a lab that is already up
  down   delete the lab's namespaces
  exec   run a command in a node: netlab exec LAB.json NODE -- COMMAND...
  plan   print the commands that would build the lab (no root needed)

exit status: 0 all checks passed, 1 a check or setup failed,
             2 usage error, 3 skipped (needs root or a kernel feature)
`

func main() {
	if len(os.Args) < 3 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	sub := os.Args[1]
	fs := flag.NewFlagSet(sub, flag.ExitOnError)
	keep := fs.Bool("keep", false, "run: leave the lab up after the checks")
	quiet := fs.Bool("quiet", false, "don't print each command")
	evidence := fs.String("evidence", "", "directory for evidence files (default: a new temporary directory)")
	_ = fs.Parse(os.Args[2:])
	if fs.NArg() < 1 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	lab, err := loadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "netlab: %v\n", err)
		os.Exit(2)
	}
	runner := &Runner{Out: os.Stdout, Quiet: *quiet}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch sub {
	case "plan":
		fmt.Print(Script(Plan(lab)))
	case "down":
		fmt.Printf("deleted %d namespace(s)\n", Down(runner, lab))
	case "up":
		if err := Preflight(ctx, runner, lab); err != nil {
			exitFor(err)
		}
		dir := *evidence
		if dir == "" {
			if dir, err = os.MkdirTemp("", "netlab-"+lab.Name+"-"); err != nil {
				exitFor(err)
			}
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			exitFor(err)
		}
		// Setup commands and services can share files through this directory.
		os.Setenv("NETLAB_EVIDENCE", dir)
		if err := Up(ctx, runner, lab, dir); err != nil {
			Down(runner, lab)
			exitFor(err)
		}
		fmt.Printf("\n%s is up (service logs in %s). Try: netlab exec %s %s -- ip addr\nRemove it with: netlab down %s\n",
			lab.Name, dir, fs.Arg(0), lab.Nodes[0].Name, fs.Arg(0))
	case "run", "check":
		dir := *evidence
		if dir == "" {
			if dir, err = os.MkdirTemp("", "netlab-"+lab.Name+"-"); err != nil {
				exitFor(err)
			}
		} else if err := os.MkdirAll(dir, 0o755); err != nil {
			exitFor(err)
		}
		s := &Session{Runner: runner, Lab: lab, EvidenceDir: dir}
		fmt.Printf("netlab: %s — %s\n\n", lab.Name, lab.Description)
		var results []Result
		if sub == "run" {
			results, err = s.Run(ctx, *keep)
		} else {
			results, err = s.CheckOnly(ctx)
		}
		if err != nil {
			fmt.Printf("evidence: %s\n", dir)
			exitFor(err)
		}
		os.Exit(summarise(results, dir))
	case "exec":
		rest := fs.Args()[1:]
		if len(rest) < 2 {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		node, cmdArgs := rest[0], rest[1:]
		if cmdArgs[0] == "--" {
			cmdArgs = cmdArgs[1:]
		}
		args := append([]string{"netns", "exec", lab.Namespace(node)}, cmdArgs...)
		cmd := exec.Command("ip", args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				os.Exit(exitErr.ExitCode())
			}
			exitFor(err)
		}
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func loadFile(path string) (*Lab, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f)
}

// CheckOnly runs the checks against a lab built earlier with "up".
func (s *Session) CheckOnly(ctx context.Context) ([]Result, error) {
	os.Setenv("NETLAB_EVIDENCE", s.EvidenceDir)
	var results []Result
	for i, c := range s.Lab.Checks {
		fmt.Fprintf(s.Runner.Out, "\ncheck %d/%d: %s\n", i+1, len(s.Lab.Checks), c.Describe())
		res := s.check(ctx, c)
		verdict := "PASS"
		if !res.Pass {
			verdict = "FAIL"
		}
		fmt.Fprintf(s.Runner.Out, "  %s: %s\n", verdict, res.Detail)
		results = append(results, res)
	}
	return results, nil
}

func summarise(results []Result, dir string) int {
	passed := 0
	for _, r := range results {
		if r.Pass {
			passed++
		}
	}
	status := "PASS"
	code := 0
	if passed != len(results) {
		status, code = "FAIL", 1
	}
	fmt.Printf("\n%s: %d/%d checks passed. Evidence in %s\n", status, passed, len(results), dir)
	return code
}

func exitFor(err error) {
	if errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "netlab: interrupted; the lab was torn down")
		os.Exit(130)
	}
	if errors.Is(err, ErrSkipped) {
		fmt.Fprintf(os.Stderr, "SKIP: %s\n", strings.TrimPrefix(err.Error(), ErrSkipped.Error()+": "))
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "netlab: %v\n", err)
	os.Exit(1)
}
