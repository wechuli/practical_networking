package main

import (
	"os"
	"strings"
	"testing"
)

func loadSample(t *testing.T) Index {
	t.Helper()
	f, err := os.Open("testdata/rfc-index-sample.xml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	idx, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return idx
}

func TestLoadKeepsOnlyRFCEntries(t *testing.T) {
	idx := loadSample(t)
	if len(idx) != 6 {
		t.Fatalf("loaded %d entries, want 6 (the BCP entry must be skipped)", len(idx))
	}
	e := idx["RFC9293"]
	if e == nil || e.Date.Year != 2022 || e.Date.Month != "August" || e.Stream != "IETF" {
		t.Fatalf("RFC 9293 decoded as %+v", e)
	}
	if strings.Join(e.IsAlso, ",") != "STD0007" || len(e.Obsoletes) != 3 {
		t.Fatalf("RFC 9293 relations: is-also %v, obsoletes %v", e.IsAlso, e.Obsoletes)
	}
}

func TestLoadRejectsOtherXML(t *testing.T) {
	if _, err := Load(strings.NewReader("<html><body>blocked by proxy</body></html>")); err == nil {
		t.Fatal("a page that is not the index must be an error")
	}
}

func TestParseID(t *testing.T) {
	for in, want := range map[string]string{"793": "RFC0793", "rfc793": "RFC0793", "RFC 9293": "RFC9293", " RFC0768 ": "RFC0768", "10000": "RFC10000"} {
		if got, err := ParseID(in); err != nil || got != want {
			t.Errorf("ParseID(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "RFC", "abc", "0", "-5"} {
		if _, err := ParseID(bad); err == nil {
			t.Errorf("ParseID(%q) should fail", bad)
		}
	}
}

func TestShort(t *testing.T) {
	for in, want := range map[string]string{"RFC0793": "RFC 793", "STD0007": "STD 7", "RFC9293": "RFC 9293", "odd": "odd"} {
		if got := Short(in); got != want {
			t.Errorf("Short(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSuccessorsFollowsTheWholeChain(t *testing.T) {
	idx := loadSample(t)
	chains := idx.Successors("RFC1883")
	if len(chains) != 1 || strings.Join(chains[0], ">") != "RFC1883>RFC2460>RFC8200" {
		t.Fatalf("chains = %v", chains)
	}
	if got := idx.Successors("RFC8200"); len(got) != 0 {
		t.Fatalf("a current RFC has no successors, got %v", got)
	}
}

func TestSuccessorsSurvivesACycle(t *testing.T) {
	idx := Index{
		"RFC0001": {DocID: "RFC0001", ObsoletedBy: []string{"RFC0002"}},
		"RFC0002": {DocID: "RFC0002", ObsoletedBy: []string{"RFC0001"}},
	}
	_ = idx.Successors("RFC0001") // must terminate
}

func TestVerdict(t *testing.T) {
	idx := loadSample(t)
	cases := map[string]string{
		"RFC0793": "OBSOLETED — read RFC 9293 instead",
		"RFC1883": "OBSOLETED — read RFC 8200 instead",
		"RFC0768": "CURRENT (INTERNET STANDARD)",
	}
	for id, want := range cases {
		if got := idx.Verdict(idx[id]); got != want {
			t.Errorf("Verdict(%s) = %q, want %q", id, got, want)
		}
	}
	updated := &Entry{DocID: "RFC9999", CurrentStatus: "PROPOSED STANDARD", UpdatedBy: []string{"RFC9998"}}
	if got := idx.Verdict(updated); !strings.Contains(got, "1 later RFC(s) update it") {
		t.Errorf("updated RFC verdict = %q", got)
	}
	historic := &Entry{DocID: "RFC9997", CurrentStatus: "HISTORIC"}
	if got := idx.Verdict(historic); !strings.HasPrefix(got, "HISTORIC") {
		t.Errorf("historic RFC verdict = %q", got)
	}
	split := Index{
		"RFC0010": {DocID: "RFC0010", ObsoletedBy: []string{"RFC0012", "RFC0011"}},
		"RFC0011": {DocID: "RFC0011"},
		"RFC0012": {DocID: "RFC0012"},
	}
	if got := split.Verdict(split["RFC0010"]); got != "OBSOLETED — read RFC 11 and RFC 12 instead" {
		t.Errorf("split verdict = %q", got)
	}
}
