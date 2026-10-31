package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// FlowRecord is one row of an AWS VPC flow log (version 2 default format).
type FlowRecord map[string]string

// Case6Metrics is the synthetic NAT-gateway incident summary.
type Case6Metrics struct {
	Provenance  string `json:"provenance"`
	Window      struct{ Start, End int64 }
	Attempts    int `json:"attempts"`
	Timeouts    int `json:"timeouts"`
	NATIPv4     int `json:"nat_ipv4_addresses"`
	Destination struct {
		IP       string `json:"ip"`
		Port     int    `json:"port"`
		Protocol int    `json:"protocol"`
	} `json:"destination"`
	ErrorPortAllocation      int    `json:"ErrorPortAllocation"`
	ActiveConnectionCount    int    `json:"ActiveConnectionCount"`
	ConnectionsToDestination int    `json:"connections_to_destination"`
	HealthySubnet            string `json:"healthy_comparison_subnet"`
}

// ReadFlows parses a flow-log CSV with a header row.
func ReadFlows(r io.Reader) ([]FlowRecord, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 1 {
		return nil, fmt.Errorf("empty flow log")
	}
	var out []FlowRecord
	for _, row := range rows[1:] {
		rec := FlowRecord{}
		for i, h := range rows[0] {
			if i < len(row) {
				rec[h] = row[i]
			}
		}
		out = append(out, rec)
	}
	return out, nil
}

// Case6 is the instructor's answer key for Capstone 6: it reads the flow
// records and metrics, prints what each observation supports, and returns
// an error if the dataset no longer supports the intended reading.
func Case6(flowsPath, metricsPath string, w io.Writer) error {
	f, err := os.Open(flowsPath)
	if err != nil {
		return err
	}
	defer f.Close()
	flows, err := ReadFlows(f)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(metricsPath)
	if err != nil {
		return err
	}
	var m Case6Metrics
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	dest := fmt.Sprintf("%s:%d", m.Destination.IP, m.Destination.Port)
	rejected := map[string]bool{}
	var skipped []FlowRecord
	perSrc := map[string]int{}
	for _, r := range flows {
		if r["log-status"] != "OK" {
			skipped = append(skipped, r)
			continue
		}
		if r["action"] == "REJECT" {
			rejected[r["dstaddr"]+":"+r["dstport"]] = true
		}
		if r["dstaddr"] == m.Destination.IP && r["dstport"] == strconv.Itoa(m.Destination.Port) {
			perSrc[r["interface-id"]+" "+r["srcaddr"]] += atoi(r["packets"])
		}
	}
	var rej []string
	for k := range rejected {
		rej = append(rej, k)
	}
	sort.Strings(rej)

	fmt.Fprintf(w, "dataset: %s\n", m.Provenance)
	fmt.Fprintf(w, "symptom: %d timeouts in %d attempts (%.0f%% of requests) — a request rate, not a share of flow records\n", m.Timeouts, m.Attempts, 100*float64(m.Timeouts)/float64(m.Attempts))
	fmt.Fprintf(w, "flows to the failing destination %s (packets per source in the window):\n", dest)
	var keys []string
	for k := range perSrc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "  %-28s %4d packets, ACCEPT\n", k, perSrc[k])
	}
	fmt.Fprintln(w, "  → ACCEPT means the security group and NACL let the packets through, not that anything answered; 3 packets and 180 bytes in a minute is consistent with a SYN and two retries that got no reply")
	fmt.Fprintf(w, "REJECT records: %s — a different destination; firewall denial does not explain timeouts to %s\n", strings.Join(rej, ", "), dest)
	for _, r := range skipped {
		fmt.Fprintf(w, "%s on %s from %s to %s: records were lost for that interval — absence of rows proves nothing there\n", r["log-status"], r["interface-id"], r["start"], r["end"])
	}
	fmt.Fprintf(w, "NAT gateway: ErrorPortAllocation %d in the window, %d connections to %s through %d IPv4 address(es)\n", m.ErrorPortAllocation, m.ConnectionsToDestination, dest, m.NATIPv4)
	fmt.Fprintf(w, "  → each NAT address supports about 55,000 simultaneous connections to one destination IP, port and protocol; demand on this one tuple is at that limit, and port allocation failed\n")
	fmt.Fprintf(w, "  → ActiveConnectionCount %d is across all destinations: it is not a ceiling, and on its own proves nothing\n", m.ActiveConnectionCount)
	fmt.Fprintf(w, "comparison: subnet %s reaches the same destination with complete flows in the same window — check which NAT gateway (route table) each subnet uses\n", m.HealthySubnet)
	fmt.Fprintln(w, "still missing: packet or trace evidence of the timed-out attempts at the source, and the NAT gateway metric at 1-minute resolution aligned with them")

	switch {
	case len(rej) != 1 || strings.HasPrefix(rej[0], m.Destination.IP+":"):
		return fmt.Errorf("expected one REJECTed destination, different from %s; got %v", dest, rej)
	case m.ErrorPortAllocation == 0:
		return fmt.Errorf("ErrorPortAllocation is 0: the dataset no longer shows port-allocation failure")
	case len(skipped) != 1:
		return fmt.Errorf("expected one SKIPDATA/NODATA record, got %d", len(skipped))
	}
	return nil
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }
