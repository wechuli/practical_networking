// Command kproxy programs a node's iptables the way kube-proxy's iptables
// mode does, from a JSON file of Services and endpoints instead of the
// Kubernetes API. The course uses it to build Services on namespace "nodes"
// where a real cluster is not available; the rules it writes have
// kube-proxy's chain structure (KUBE-SERVICES → KUBE-SVC-… → KUBE-SEP-…,
// KUBE-NODEPORTS → KUBE-EXT-…, KUBE-MARK-MASQ and KUBE-POSTROUTING, REJECT
// for Services without endpoints), so reading them is reading kube-proxy.
//
//	kproxy show -f services.json -node node1     # print the iptables-restore input
//	kproxy sync -f services.json -node node1     # install it (only KUBE-* chains change)
//	kproxy flush                                  # remove every KUBE-* chain and hook
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	file := fs.String("f", "", "state file: Services, endpoints, nodes, cluster CIDR")
	node := fs.String("node", "", "the node these rules are for (decides local endpoints)")
	_ = fs.Parse(os.Args[2:])
	switch os.Args[1] {
	case "show", "sync":
		if *file == "" || *node == "" {
			usage()
		}
		f, err := os.Open(*file)
		if err != nil {
			fail(err)
		}
		st, err := LoadState(f)
		f.Close()
		if err != nil {
			fail(fmt.Errorf("%s: %w", *file, err))
		}
		if _, ok := st.Nodes[*node]; !ok {
			fail(fmt.Errorf("node %q is not in %s", *node, *file))
		}
		rules := st.Rules(*node)
		if os.Args[1] == "show" {
			fmt.Print(rules)
			return
		}
		rules = withStaleChainsRemoved(rules)
		if err := run(rules, "iptables-restore", "--noflush"); err != nil {
			fail(err)
		}
		for _, h := range Hooks {
			check := append([]string{"-t", h[0], "-C", h[1]}, h[2:]...)
			if exec.Command("iptables", check...).Run() != nil {
				insert := append([]string{"-t", h[0], "-I", h[1]}, h[2:]...)
				if err := run("", "iptables", insert...); err != nil {
					fail(err)
				}
			}
		}
		n := 0
		for _, svc := range st.Services {
			n += len(svc.Ports)
		}
		fmt.Printf("kproxy: %s: %d service ports synced\n", *node, n)
	case "flush":
		for _, h := range Hooks {
			del := append([]string{"-t", h[0], "-D", h[1]}, h[2:]...)
			_ = exec.Command("iptables", del...).Run()
		}
		for _, table := range []string{"nat", "filter"} {
			out, _ := exec.Command("iptables-save", "-t", table).Output()
			var chains []string
			for _, l := range strings.Split(string(out), "\n") {
				if strings.HasPrefix(l, ":KUBE-") {
					chains = append(chains, strings.Fields(l[1:])[0])
				}
			}
			for _, c := range chains {
				_ = exec.Command("iptables", "-t", table, "-F", c).Run()
			}
			for _, c := range chains {
				_ = exec.Command("iptables", "-t", table, "-X", c).Run()
			}
		}
		fmt.Println("kproxy: KUBE-* chains removed")
	default:
		usage()
	}
}

func run(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(out.String()))
	}
	return nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "kproxy: %v\n", err)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, "usage: kproxy show|sync -f STATE.json -node NODE\n       kproxy flush\n")
	os.Exit(2)
}

// withStaleChainsRemoved adds, to the nat table's transaction, the deletion
// of per-service and per-endpoint chains left from an earlier sync (an
// endpoint that went away), as kube-proxy does.
func withStaleChainsRemoved(rules string) string {
	out, _ := exec.Command("iptables-save", "-t", "nat").Output()
	declared := map[string]bool{}
	for _, l := range strings.Split(rules, "\n") {
		if strings.HasPrefix(l, ":") {
			declared[strings.Fields(l[1:])[0]] = true
		}
	}
	var decl, del []string
	for _, l := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(l, ":KUBE-") {
			continue
		}
		c := strings.Fields(l[1:])[0]
		for _, p := range []string{"KUBE-SVC-", "KUBE-SEP-", "KUBE-EXT-", "KUBE-SVL-"} {
			if strings.HasPrefix(c, p) && !declared[c] {
				decl = append(decl, ":"+c+" - [0:0]")
				del = append(del, "-X "+c)
			}
		}
	}
	if len(del) == 0 {
		return rules
	}
	i := strings.Index(rules, "*nat\n") + len("*nat\n")
	rules = rules[:i] + strings.Join(decl, "\n") + "\n" + rules[i:]
	j := strings.LastIndex(rules, "COMMIT")
	return rules[:j] + strings.Join(del, "\n") + "\n" + rules[j:]
}
