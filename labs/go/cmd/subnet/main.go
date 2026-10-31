// Command subnet does the subnetting arithmetic from Module 5 — network,
// broadcast, usable range, cloud provider reservations — and names the kind
// of address space a prefix is in. It checks hand calculations; it does not
// replace learning them.
//
//	subnet 10.0.48.0/20
//	subnet -cloud aws 10.0.20.0/24
//	subnet 10.0.1.50/24 10.0.1.200 10.0.2.50   # are these on-link?
//	subnet 169.254.169.254 fd42:1::99/64
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

func main() {
	provider := flag.String("cloud", "", "also apply a provider's reserved addresses: aws, azure, or gcp")
	jsonOut := flag.Bool("json", false, "print JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: subnet [-cloud aws|azure|gcp] [-json] PREFIX [ADDRESS...]\n       subnet ADDRESS...\n\n"+
			"Prints the network, broadcast, usable range, and address kind of PREFIX.\n"+
			"Extra ADDRESS arguments are checked against PREFIX: inside means on-link.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	args := flag.Args()
	// With a prefix first, the rest are membership checks; otherwise every
	// argument is analysed on its own.
	targets, checks := args, []string(nil)
	if strings.Contains(args[0], "/") {
		targets, checks = args[:1], args[1:]
	}
	var infos []*Info
	for _, t := range targets {
		info, err := Analyze(t, *provider)
		if err != nil {
			fmt.Fprintf(os.Stderr, "subnet: %v\n", err)
			os.Exit(2)
		}
		infos = append(infos, info)
	}
	type check struct {
		Address string `json:"address"`
		Inside  bool   `json:"inside"`
		Meaning string `json:"meaning"`
	}
	var results []check
	for _, c := range checks {
		in, msg, err := Contains(targets[0], c)
		if err != nil {
			fmt.Fprintf(os.Stderr, "subnet: %v\n", err)
			os.Exit(2)
		}
		results = append(results, check{c, in, msg})
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{"prefixes": infos, "checks": results})
		return
	}
	for i, info := range infos {
		if i > 0 {
			fmt.Println()
		}
		fmt.Print(Format(info))
	}
	for _, r := range results {
		fmt.Println(r.Meaning)
	}
}

// Format renders an Info as aligned text.
func Format(i *Info) string {
	var b strings.Builder
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-11s %s\n", k, v)
		}
	}
	row("prefix", i.Prefix)
	if i.Host != "" {
		row("host", i.Host+" (the input had host bits set)")
	}
	row("mask", i.Mask)
	row("kind", i.Kind)
	row("addresses", i.Addresses)
	row("network", i.Network)
	row("broadcast", i.Broadcast)
	if i.Usable == "1" {
		row("usable", i.First+" (1 address)")
	} else {
		row("usable", fmt.Sprintf("%s – %s (%s addresses)", i.First, i.Last, i.Usable))
	}
	for _, n := range i.Notes {
		row("note", n)
	}
	if c := i.Cloud; c != nil {
		if c.Error != "" {
			row(c.Key, c.Error)
		} else {
			row(c.Key, fmt.Sprintf("reserves %s: %s usable, first %s",
				strings.Join(c.Reserved, ", "), c.Usable, c.First))
		}
	}
	return b.String()
}
