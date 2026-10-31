package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Entry is one <rfc-entry> from the RFC Editor's rfc-index.xml
// (schema: https://www.rfc-editor.org/rfc-index.xsd). Tags carry no
// namespace, so they match the index whatever its root namespace is.
type Entry struct {
	DocID string `xml:"doc-id" json:"doc_id"`
	Title string `xml:"title" json:"title"`
	Date  struct {
		Month string `xml:"month" json:"month"`
		Year  int    `xml:"year" json:"year"`
	} `xml:"date" json:"date"`
	Obsoletes         []string `xml:"obsoletes>doc-id" json:"obsoletes,omitempty"`
	ObsoletedBy       []string `xml:"obsoleted-by>doc-id" json:"obsoleted_by,omitempty"`
	Updates           []string `xml:"updates>doc-id" json:"updates,omitempty"`
	UpdatedBy         []string `xml:"updated-by>doc-id" json:"updated_by,omitempty"`
	IsAlso            []string `xml:"is-also>doc-id" json:"is_also,omitempty"`
	CurrentStatus     string   `xml:"current-status" json:"current_status"`
	PublicationStatus string   `xml:"publication-status" json:"publication_status"`
	Stream            string   `xml:"stream" json:"stream,omitempty"`
	ErrataURL         string   `xml:"errata-url" json:"errata_url,omitempty"`
}

// Index maps "RFC0793"-style IDs to entries.
type Index map[string]*Entry

// Load stream-decodes rfc-index.xml, keeping only <rfc-entry> elements. The
// file also lists BCP, STD, and FYI entries, which this tool reports through
// each RFC's is-also field instead.
func Load(r io.Reader) (Index, error) {
	idx := Index{}
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading RFC index: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "rfc-entry" {
			continue
		}
		e := new(Entry)
		if err := dec.DecodeElement(e, &start); err != nil {
			return nil, fmt.Errorf("decoding rfc-entry: %w", err)
		}
		idx[e.DocID] = e
	}
	if len(idx) == 0 {
		return nil, fmt.Errorf("no <rfc-entry> elements found; is this rfc-index.xml?")
	}
	return idx, nil
}

// ParseID accepts 793, rfc793, RFC0793, or "RFC 793" and returns "RFC0793".
func ParseID(s string) (string, error) {
	digits := strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(s)), "RFC"))
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return "", fmt.Errorf("%q is not an RFC number", s)
	}
	return fmt.Sprintf("RFC%04d", n), nil
}

// Short turns "RFC0793" into "RFC 793" and "STD0007" into "STD 7".
func Short(id string) string {
	i := strings.IndexFunc(id, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return id
	}
	n, err := strconv.Atoi(id[i:])
	if err != nil {
		return id
	}
	return fmt.Sprintf("%s %d", id[:i], n)
}

// Successors follows obsoleted-by links to the documents that are not
// themselves obsoleted, returning one chain per current successor. A document
// can be split across several replacements, so there may be more than one.
func (idx Index) Successors(id string) [][]string {
	var chains [][]string
	var walk func(cur string, path []string)
	walk = func(cur string, path []string) {
		path = append(path, cur)
		e := idx[cur]
		if e == nil || len(e.ObsoletedBy) == 0 {
			if len(path) > 1 {
				chains = append(chains, append([]string(nil), path...))
			}
			return
		}
		for _, next := range e.ObsoletedBy {
			seen := false
			for _, p := range path {
				seen = seen || p == next
			}
			if !seen { // the index should be acyclic; don't trust it to be
				walk(next, path)
			}
		}
	}
	walk(id, nil)
	sort.Slice(chains, func(i, j int) bool { return chains[i][len(chains[i])-1] < chains[j][len(chains[j])-1] })
	return chains
}

// Verdict says whether to rely on a document, in one line.
func (idx Index) Verdict(e *Entry) string {
	if chains := idx.Successors(e.DocID); len(chains) > 0 {
		var current []string
		for _, c := range chains {
			current = append(current, Short(c[len(c)-1]))
		}
		return "OBSOLETED — read " + strings.Join(current, " and ") + " instead"
	}
	if len(e.ObsoletedBy) > 0 { // replacement missing from the index
		return "OBSOLETED by " + joinShort(e.ObsoletedBy)
	}
	if e.CurrentStatus == "HISTORIC" {
		return "HISTORIC — kept for the record, no longer recommended"
	}
	v := "CURRENT (" + e.CurrentStatus + ")"
	if n := len(e.UpdatedBy); n > 0 {
		v += fmt.Sprintf(" — but %d later RFC(s) update it; read those too", n)
	}
	return v
}

func joinShort(ids []string) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = Short(id)
	}
	return strings.Join(s, ", ")
}
