package main

import (
	"strings"
	"testing"
)

func TestChainNamesMatchKubeProxy(t *testing.T) {
	// Names seen on every real cluster: the kubernetes API Service and kube-dns.
	for want, parts := range map[string][]string{
		"NPX46M4PTMTKRN6Y": {"default/kubernetes:https", "tcp"},
		"TCOU7JCQXEZGVUNU": {"kube-system/kube-dns:dns", "udp"},
	} {
		if got := chainHash(parts...); got != want {
			t.Errorf("%v: %s, want %s", parts, got, want)
		}
	}
}

func state(t *testing.T, etp string, eps string) *State {
	t.Helper()
	s, err := LoadState(strings.NewReader(`{"clusterCIDR":"10.244.0.0/16","nodes":{"n1":"192.168.50.11","n2":"192.168.50.12"},
	 "services":[{"namespace":"default","name":"web","clusterIP":"10.96.0.20","externalTrafficPolicy":"` + etp + `",
	 "ports":[{"name":"http","port":80,"targetPort":8080,"nodePort":30080}],"endpoints":[` + eps + `]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRules(t *testing.T) {
	two := `{"ip":"10.244.1.2","node":"n1"},{"ip":"10.244.2.2","node":"n2"},{"ip":"10.244.2.9","node":"n2","ready":false}`
	r := state(t, "Cluster", two).Rules("n1")
	for _, want := range []string{
		"--probability 0.50000000000 -j KUBE-SEP-",
		"-j DNAT --to-destination 10.244.2.2:8080",
		"masquerade traffic for default/web:http external destinations",
		"--dport 30080 -j KUBE-EXT-",
		"NOTE: this must be the last rule in this chain",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(r, "10.244.2.9") {
		t.Error("an endpoint that is not ready must not get rules")
	}
	if r := state(t, "Cluster", "").Rules("n1"); !strings.Contains(r, `"default/web:http has no endpoints" -m tcp --dport 80 -j REJECT`) {
		t.Errorf("no endpoints should REJECT:\n%s", r)
	}
	local := state(t, "Local", `{"ip":"10.244.2.2","node":"n2"}`)
	if r := local.Rules("n1"); !strings.Contains(r, "has no local endpoints") || !strings.Contains(r, "--dport 30080 -j DROP") {
		t.Errorf("Local with no local endpoint should DROP the NodePort:\n%s", r)
	}
	if r := local.Rules("n2"); !strings.Contains(r, "KUBE-SVL-") || strings.Contains(r, "has no local endpoints") {
		t.Errorf("Local with a local endpoint should use KUBE-SVL:\n%s", r)
	}
}
