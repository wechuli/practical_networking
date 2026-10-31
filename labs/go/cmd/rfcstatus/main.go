// Command rfcstatus answers the question to ask before citing an RFC in a
// ticket or design: is this document still current, what replaced it, and
// which later RFCs change its behaviour?
//
// It reads the RFC Editor's official index (rfc-index.xml), from a local file
// or a URL:
//
//	curl -fsSLO https://www.rfc-editor.org/rfc-index.xml
//	rfcstatus -index rfc-index.xml 793 2460 2246 8446
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const defaultIndex = "https://www.rfc-editor.org/rfc-index.xml"

func main() {
	src := flag.String("index", defaultIndex, "path or https URL of rfc-index.xml")
	jsonOut := flag.Bool("json", false, "print the entries as JSON")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "usage: rfcstatus [-index FILE|URL] [-json] RFC...\n\nSays whether each RFC is current, what replaced it, and what updates it.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	var ids []string
	for _, a := range flag.Args() {
		id, err := ParseID(a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rfcstatus: %v\n", err)
			os.Exit(2)
		}
		ids = append(ids, id)
	}

	r, err := open(*src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rfcstatus: %v\n", err)
		os.Exit(1)
	}
	idx, err := Load(r)
	r.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rfcstatus: %v\n", err)
		os.Exit(1)
	}

	missing := 0
	var found []*Entry
	for _, id := range ids {
		e := idx[id]
		if e == nil {
			fmt.Fprintf(os.Stderr, "rfcstatus: %s is not in the index\n", Short(id))
			missing++
			continue
		}
		found = append(found, e)
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(found)
	} else {
		for _, e := range found {
			write(os.Stdout, idx, e)
		}
	}
	if missing > 0 {
		os.Exit(1)
	}
}

func open(src string) (io.ReadCloser, error) {
	if !strings.HasPrefix(src, "https://") && !strings.HasPrefix(src, "http://") {
		return os.Open(src)
	}
	client := &http.Client{Timeout: 2 * time.Minute} // honours HTTPS_PROXY
	resp, err := client.Get(src)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w (download it once and pass -index FILE)", src, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("fetching %s: %s", src, resp.Status)
	}
	return resp.Body, nil
}

func write(w io.Writer, idx Index, e *Entry) {
	fmt.Fprintf(w, "%s  %s (%s %d)\n", Short(e.DocID), e.Title, e.Date.Month, e.Date.Year)
	status := e.CurrentStatus
	if e.PublicationStatus != e.CurrentStatus {
		status += ", published as " + e.PublicationStatus
	}
	if e.Stream != "" {
		status += "; " + e.Stream + " stream"
	}
	fmt.Fprintf(w, "  status      %s\n", status)
	fmt.Fprintf(w, "  verdict     %s\n", idx.Verdict(e))
	for _, chain := range idx.Successors(e.DocID) {
		fmt.Fprintf(w, "  chain       %s\n", strings.ReplaceAll(joinShort(chain), ", ", " → "))
	}
	if len(e.Obsoletes) > 0 {
		fmt.Fprintf(w, "  obsoletes   %s\n", joinShort(e.Obsoletes))
	}
	if len(e.UpdatedBy) > 0 {
		fmt.Fprintf(w, "  updated by  %s\n", joinShort(e.UpdatedBy))
	}
	if len(e.IsAlso) > 0 {
		fmt.Fprintf(w, "  also        %s\n", joinShort(e.IsAlso))
	}
	if e.ErrataURL != "" {
		fmt.Fprintf(w, "  errata      %s\n", e.ErrataURL)
	}
	fmt.Fprintln(w)
}
