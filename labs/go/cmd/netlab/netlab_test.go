package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const labDir = "../../../netlab" // labs/netlab

func loadLab(t *testing.T, name string) *Lab {
	t.Helper()
	lab, err := loadFile(filepath.Join(labDir, name))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return lab
}

func TestCourseLabsValidate(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(labDir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no lab files found in %s", labDir)
	}
	for _, f := range files {
		if _, err := loadFile(f); err != nil {
			t.Errorf("%s: %v", filepath.Base(f), err)
		}
	}
}

func TestValidateRejectsMistakes(t *testing.T) {
	cases := map[string]string{
		"unknown field":    `{"name":"x","nodes":[{"name":"a","colour":"red"}]}`,
		"file outside etc": `{"name":"x","nodes":[{"name":"a","files":{"/tmp/x":"y"}}]}`,
		"file dotdot":      `{"name":"x","nodes":[{"name":"a","files":{"/etc/../root/x":"y"}}]}`,
		"bad lab name":     `{"name":"Has Spaces","nodes":[{"name":"a"}]}`,
		"no nodes":         `{"name":"x","nodes":[]}`,
		"duplicate node":   `{"name":"x","nodes":[{"name":"a"},{"name":"a"}]}`,
		"unknown node":     `{"name":"x","nodes":[{"name":"a"}],"links":[{"a":{"node":"a","if":"e0"},"b":{"node":"zz","if":"e0"}}]}`,
		"long ifname":      `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"averyveryverylongname"},"b":{"node":"b","if":"e0"}}]}`,
		"duplicate if":     `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0"},"b":{"node":"b","if":"e0"}},{"a":{"node":"a","if":"e0"},"b":{"node":"b","if":"e1"}}]}`,
		"addr no prefix":   `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0","addr":["10.0.0.1"]},"b":{"node":"b","if":"e0"}}]}`,
		"unknown bridge":   `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0","bridge":"br9"},"b":{"node":"b","if":"e0"}}]}`,
		"vlan not port":    `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0","vlans":[{"vid":10}]},"b":{"node":"b","if":"e0"}}]}`,
		"vid range":        `{"name":"x","nodes":[{"name":"a","bridges":[{"name":"br0"}]},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0","bridge":"br0","vlans":[{"vid":4095}]},"b":{"node":"b","if":"e0"}}]}`,
		"forward delay":    `{"name":"x","nodes":[{"name":"a","bridges":[{"name":"br0","stp":true,"forward_delay":"1s"}]}]}`,
		"bad duration":     `{"name":"x","nodes":[{"name":"a","bridges":[{"name":"br0","forward_delay":"soon"}]}]}`,
		"ping expect":      `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"ping","from":"a","to":"10.0.0.1","expect":"maybe"}]}`,
		"ping to name":     `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"ping","from":"a","to":"example.test","expect":"reachable"}]}`,
		"unknown check":    `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"magic"}]}`,
		"stp no counts":    `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"stp","nodes":["a"],"bridge":"br0"}]}`,
		"fdb bad learns":   `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"fdb","node":"a","bridge":"br0","port":"p1","learns":"a"}]}`,
		"vlan parent":      `{"name":"x","nodes":[{"name":"a","vlans":[{"name":"e0.10","parent":"e0","id":10}]}]}`,
		"route not addr":   `{"name":"x","nodes":[{"name":"a","routes":[{"dst":"10.0.0.0/8","via":"gateway"}]}]}`,
		"command no args":  `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"command","node":"a"}]}`,
		"exit and allow":   `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"command","node":"a","run":["true"],"expect_exit":0,"allow_fail":true}]}`,
		"capture no if":    `{"name":"x","nodes":[{"name":"a"}],"checks":[{"type":"capture","node":"a","if":"eth9","save":"x.pcap","during":{"node":"a","run":["true"]}}]}`,
		"capture no run":   `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0"},"b":{"node":"b","if":"e0"}}],"checks":[{"type":"capture","node":"a","if":"e0","save":"x.pcap"}]}`,
		"capture no save":  `{"name":"x","nodes":[{"name":"a"},{"name":"b"}],"links":[{"a":{"node":"a","if":"e0"},"b":{"node":"b","if":"e0"}}],"checks":[{"type":"capture","node":"a","if":"e0","during":{"node":"a","run":["true"]}}]}`,
		"empty setup":      `{"name":"x","nodes":[{"name":"a","setup":[[]]}]}`,
		"service no run":   `{"name":"x","nodes":[{"name":"a","services":[{"name":"web"}]}]}`,
		"service port":     `{"name":"x","nodes":[{"name":"a","services":[{"name":"web","run":["x"],"port":70000}]}]}`,
	}
	for name, src := range cases {
		if _, err := Load(strings.NewReader(src)); err == nil {
			t.Errorf("%s: lab should be rejected", name)
		}
	}
}

func TestPlanBuildsInDependencyOrder(t *testing.T) {
	cmds := Plan(loadLab(t, "vlan.json"))
	index := func(want string) int {
		for i, c := range cmds {
			if strings.Join(c, " ") == want {
				return i
			}
		}
		t.Fatalf("plan lacks %q", want)
		return -1
	}
	nsAdd := index("ip netns add nl-vlan-switch")
	bridge := index("ip -n nl-vlan-switch link add br0 type bridge vlan_filtering 1")
	master := index("ip -n nl-vlan-switch link set access master br0")
	delVID1 := index("ip netns exec nl-vlan-switch bridge vlan del dev access vid 1")
	addVID := index("ip netns exec nl-vlan-switch bridge vlan add dev access vid 10 pvid untagged")
	sub := index("ip -n nl-vlan-tagged link add link eth0 name eth0.10 type vlan id 10")
	parent := index("ip -n nl-vlan-tagged link add eth0 type veth peer name tagged netns nl-vlan-switch")
	if !(nsAdd < bridge && bridge < master && master < delVID1 && delVID1 < addVID && parent < sub) {
		t.Fatalf("commands out of order: ns %d bridge %d master %d del %d add %d parent %d sub %d",
			nsAdd, bridge, master, delVID1, addVID, parent, sub)
	}
}

func TestPlanConvertsForwardDelayToCentiseconds(t *testing.T) {
	for _, c := range Plan(loadLab(t, "stp.json")) {
		if strings.Join(c, " ") == "ip -n nl-stp-left link add br0 type bridge stp_state 1 forward_delay 400" {
			return
		}
	}
	t.Fatal("4s forward delay should become forward_delay 400 (centiseconds)")
}

func TestHostChecksAndSetup(t *testing.T) {
	lab, err := Load(strings.NewReader(`{"name":"hostonly","setup":[["docker","network","create","x"]],
		"teardown":[["docker","network","rm","x"]],
		"checks":[{"type":"command","node":"@host","run":["ip","link"]},
		          {"type":"capture","node":"@host","if":"docker0","save":"a.pcap","during":{"node":"@host","run":["true"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := lab.InNode(HostNode, "ip", "link"); strings.Join(got, " ") != "ip link" {
		t.Fatalf("host commands must not enter a namespace: %v", got)
	}
	if got := lab.InNode("c1", "ip", "link"); got[0] != "ip" || got[3] != "nl-hostonly-c1" {
		t.Fatalf("%v", got)
	}
	if !strings.Contains(ScriptWithServices(lab), "docker network create x") {
		t.Fatal("build script should list host setup")
	}
	if _, err := Load(strings.NewReader(`{"name":"empty","checks":[]}`)); err == nil {
		t.Fatal("a lab with no nodes and no host setup should be rejected")
	}
}

func TestExpandLabsAndPython(t *testing.T) {
	t.Setenv("NETLAB_BIN", "/opt/course/labs/go/bin")
	t.Setenv("NETLAB_PYTHON", "")
	got := Expand([]string{"${PYTHON}", "${LABS}/http3/h3lab.py"})
	if got[1] != "/opt/course/labs/http3/h3lab.py" {
		t.Fatalf("${LABS} expanded to %q", got[1])
	}
	if got[0] != "python3" {
		t.Fatalf("${PYTHON} without a venv should be python3, got %q", got[0])
	}
	t.Setenv("NETLAB_PYTHON", "/venv/bin/python")
	if got := Expand([]string{"${PYTHON}"}); got[0] != "/venv/bin/python" {
		t.Fatalf("$NETLAB_PYTHON ignored: %q", got[0])
	}
}

func TestSetupRunsAfterTopologyAndExpandsBin(t *testing.T) {
	t.Setenv("NETLAB_BIN", "/opt/course/bin")
	lab := loadLab(t, "osi.json")
	cmds := Plan(lab)
	last := strings.Join(cmds[len(cmds)-1], " ")
	if !strings.HasPrefix(last, "ip netns exec nl-osi-server iptables -A INPUT") {
		t.Fatalf("setup commands should come last, got %q", last)
	}
	svc := lab.ServiceCommand("server", lab.Nodes[1].Services[0])
	if svc[4] != "/opt/course/bin/labserver" {
		t.Fatalf("${BIN} not expanded: %v", svc)
	}
	if !strings.Contains(ScriptWithServices(lab), "/opt/course/bin/labserver -addr 198.18.4.2:8443") {
		t.Fatal("build script should include the service")
	}
}

func TestExpectations(t *testing.T) {
	out := "Flags [S]\nFlags [R.]\n"
	if m := missingStrings(out, []string{"Flags [S]", "Flags [S.]"}); len(m) != 1 || m[0] != "Flags [S.]" {
		t.Fatalf("missing = %v", m)
	}
	if p := presentStrings(out, []string{"Flags [R", "is-at"}); len(p) != 1 || p[0] != "Flags [R" {
		t.Fatalf("present = %v", p)
	}
	if exitCode(nil) != 0 {
		t.Fatal("nil error is exit 0")
	}
	if err := exec.Command("sh", "-c", "exit 7").Run(); exitCode(err) != 7 {
		t.Fatalf("exitCode = %d, want 7", exitCode(err))
	}
}

func TestExpandEvidence(t *testing.T) {
	t.Setenv("NETLAB_EVIDENCE", "/tmp/ev")
	got := Expand([]string{"${EVIDENCE}/pki", "x"})
	if got[0] != "/tmp/ev/pki" || got[1] != "x" {
		t.Fatal(got)
	}
}

func TestFilesGoUnderEtcNetns(t *testing.T) {
	l, err := Load(strings.NewReader(`{"name":"dns","nodes":[{"name":"c","files":{"/etc/resolv.conf":"nameserver 192.0.2.53\n"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := l.FilePath("c", "/etc/resolv.conf"); got != "/etc/netns/nl-dns-c/resolv.conf" {
		t.Fatal(got)
	}
	s := ScriptWithServices(l)
	if !strings.Contains(s, "cat > /etc/netns/nl-dns-c/resolv.conf <<'NETLAB_EOF'\nnameserver 192.0.2.53\nNETLAB_EOF") {
		t.Fatal(s)
	}
}

func TestNeedsVLAN(t *testing.T) {
	if !loadLab(t, "vlan.json").NeedsVLAN() || loadLab(t, "bridge.json").NeedsVLAN() || loadLab(t, "stp.json").NeedsVLAN() {
		t.Fatal("only the VLAN lab needs 802.1Q support")
	}
}

func TestNeedsIPv6(t *testing.T) {
	if !loadLab(t, "ipv6.json").NeedsIPv6() {
		t.Error("the IPv6 lab uses IPv6 addresses and sysctls")
	}
	for _, name := range []string{"bridge.json", "stp.json", "osi.json", "ip.json"} {
		if loadLab(t, name).NeedsIPv6() {
			t.Errorf("%s is IPv4-only", name)
		}
	}
}

// Output captured from `bridge -j link show` in a two-switch STP lab.
const stpSample = `[{"ifindex":3,"ifname":"path1","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"mtu":1500,"master":"br0","state":"forwarding","priority":32,"cost":2},
{"ifindex":4,"ifname":"path2","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"mtu":1500,"master":"br0","state":"blocking","priority":32,"cost":2},
{"ifindex":5,"link":"br0","ifname":"host","flags":["BROADCAST","MULTICAST","UP","LOWER_UP"],"mtu":1500,"master":"br1","state":"forwarding","priority":32,"cost":2}]`

func TestCountSTP(t *testing.T) {
	counts, desc, err := CountSTP([]byte(stpSample), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if counts["forwarding"] != 1 || counts["blocking"] != 1 || len(desc) != 2 {
		t.Fatalf("counts %v desc %v (ports of other bridges must be ignored)", counts, desc)
	}
	if _, _, err := CountSTP([]byte("not json"), "br0"); err == nil {
		t.Fatal("bad output must be an error")
	}
}

// Output captured from `bridge -j fdb show br br0` after one ping.
const fdbSample = `[{"mac":"01:00:5e:00:00:01","ifname":"br0","flags":["self"],"state":"permanent"},
{"mac":"ea:51:f9:a7:2f:8d","ifname":"p1","flags":[],"master":"br0","state":""},
{"mac":"92:96:1e:f1:5b:53","ifname":"p1","flags":[],"master":"br0","state":"permanent"}]`

func TestLearned(t *testing.T) {
	for _, tc := range []struct {
		port, mac string
		want      bool
	}{
		{"p1", "EA:51:F9:A7:2F:8D", true},  // dynamic entry, case-insensitive
		{"p1", "92:96:1e:f1:5b:53", false}, // the port's own permanent address is not learning
		{"p2", "ea:51:f9:a7:2f:8d", false}, // wrong port
	} {
		got, err := Learned([]byte(fdbSample), "br0", tc.port, tc.mac)
		if err != nil || got != tc.want {
			t.Errorf("Learned(%s, %s) = %v, %v; want %v", tc.port, tc.mac, got, err, tc.want)
		}
	}
}

func TestShellJoinQuotes(t *testing.T) {
	got := shellJoin([]string{"ip", "netns", "exec", "nl-x-a", "sh", "-c", "echo it's here"})
	if got != `ip netns exec nl-x-a sh -c 'echo it'\''s here'` {
		t.Fatalf("got %s", got)
	}
}

// Integration tests build real namespaces, so they are opt-in: run them as
// root on Linux with NETLAB_INTEGRATION=1 (see labs/go/README.md).
func requireNetns(t *testing.T) {
	t.Helper()
	if os.Getenv("NETLAB_INTEGRATION") != "1" {
		t.Skip("set NETLAB_INTEGRATION=1 (as root) to build real namespaces")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root for network namespaces")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("needs iproute2")
	}
}

func runLab(t *testing.T, name string) []Result {
	t.Helper()
	requireNetns(t)
	lab := loadLab(t, name)
	s := &Session{Runner: &Runner{Out: io.Discard, Quiet: true}, Lab: lab, EvidenceDir: t.TempDir()}
	results, err := s.Run(context.Background(), false)
	if errors.Is(err, ErrSkipped) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if !r.Pass {
			t.Errorf("%s: %s", r.Check.Describe(), r.Detail)
		}
	}
	out, _ := exec.Command("ip", "netns", "list").Output()
	if strings.Contains(string(out), "nl-"+lab.Name+"-") {
		t.Errorf("namespaces left behind:\n%s", out)
	}
	return results
}

func TestBridgeLab(t *testing.T) { runLab(t, "bridge.json") }
func TestVLANLab(t *testing.T)   { runLab(t, "vlan.json") }
func TestSTPLab(t *testing.T) {
	if testing.Short() {
		t.Skip("STP convergence takes about 20 seconds")
	}
	runLab(t, "stp.json")
}

// buildTools builds the course tools that lab files call as ${BIN}/...
func buildTools(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if out, err := exec.Command("go", "build", "-o", bin, "../labserver", "../phases", "../gotrace", "../udplab", "../tcplab", "../dnslab", "../tlslab", "../pathlab", "../weblab", "../kproxy", "../socklab", "../obslab", "../edgelab", "../caselab").CombinedOutput(); err != nil {
		t.Skipf("cannot build the course tools: %v\n%s", err, out)
	}
	t.Setenv("NETLAB_BIN", bin)
}

func TestOSILab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	runLab(t, "osi.json")
}

func TestRoutingLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	runLab(t, "routing.json")
}

func TestOSPFLab(t *testing.T) {
	if testing.Short() {
		t.Skip("OSPF convergence takes about 30 seconds")
	}
	runLab(t, "ospf.json")
}

func TestBGPLab(t *testing.T) {
	if testing.Short() {
		t.Skip("BGP convergence takes about a minute")
	}
	requireNetns(t)
	buildTools(t)
	runLab(t, "bgp.json")
}

func TestUDPLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the loss trials take about five minutes")
	}
	requireNetns(t)
	buildTools(t)
	runLab(t, "udp.json")
}

func TestTCPLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the SYN timeouts and transfers take about 40 seconds")
	}
	requireNetns(t)
	buildTools(t)
	runLab(t, "tcp.json")
}

func TestDNSLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the TTL and timeout measurements take about 90 seconds")
	}
	requireNetns(t)
	buildTools(t)
	runLab(t, "dns.json")
}

func TestTLSLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	runLab(t, "tls.json")
}

func TestPerfLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the throughput measurements take about two minutes")
	}
	requireNetns(t)
	buildTools(t)
	runLab(t, "perf.json")
}

func TestWebLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the web lab takes about a minute")
	}
	requireNetns(t)
	buildTools(t)
	labs, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETLAB_LABS", labs) // h3lab.py and echo.proto; ${PYTHON} is labs/../.venv
	runLab(t, "web.json")
}

func TestContainersLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	runLab(t, "containers.json")
}

func TestDockerLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the Docker lab builds an image and takes about a minute")
	}
	requireNetns(t)
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("the Docker daemon is not running")
	}
	buildTools(t)
	labs, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETLAB_LABS", labs) // docker/build-image.sh
	runLab(t, "docker.json")
}

func TestK8sLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the Kubernetes lab includes a 40-second DNS timeout")
	}
	requireNetns(t)
	buildTools(t)
	labs, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETLAB_LABS", labs) // kubernetes/services.json
	runLab(t, "k8s.json")
}

func TestFirewallLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"hping3", "nc", "nft", "conntrack", "nstat", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "firewall.json")
}

func TestTunnelsLab(t *testing.T) {
	if testing.Short() {
		t.Skip("the tunnels lab takes about two and a half minutes (NAT expiry, loss measurements)")
	}
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"wg", "openvpn", "swanctl", "ssh", "iperf3", "conntrack", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "tunnels.json")
}

func TestSocketsLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"prlimit", "nstat", "tcpdump"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "sockets.json")
}

func TestCaptureLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"tshark", "mergecap", "capinfos", "dnsmasq", "dig", "iperf3", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "capture.json")
}

func TestObserveLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"conntrack", "nstat", "curl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "observe.json")
}

func TestDHCPLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"dnsmasq", "busybox", "tcpdump", "tshark", "nft", "ethtool"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "dhcp.json")
}

func TestEdgeLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"haproxy", "nginx", "curl", "iptables", "nc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "edge.json")
}

func TestTimeLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"chronyd", "chronyc"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "time.json")
}

func TestCaseLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	for _, tool := range []string{"named", "rndc", "unbound", "unbound-control", "nginx", "dig", "openssl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	runLab(t, "case.json")
}

func TestICMPLab(t *testing.T) {
	requireNetns(t)
	buildTools(t)
	runLab(t, "icmp.json")
}

func TestIPLab(t *testing.T)   { runLab(t, "ip.json") }
func TestARPLab(t *testing.T)  { runLab(t, "arp.json") }
func TestIPv6Lab(t *testing.T) { runLab(t, "ipv6.json") }
