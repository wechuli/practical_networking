# Copilot instructions — Practical Networking

A hands-on networking course. Markdown notes plus Linux lab tooling written in Go.

## Layout
- `modules/module-NN-*.md` — the chapters: 00–23 and 26 (capstones). 24 and 25 are planned, no files yet.
- `appendices.md`, `networking_course_outline.md`, `implementation.md` (what was changed and verified), `improvements.md` (the original review).
- `labs/` — lab guides (`README.md`, `index.md`, …), `labs/netlab/*.json` (namespace lab definitions), `labs/go/` (Go module `github.com/wechuli/practical_networking/labs/go`; one tool per `cmd/<name>`; `netlab` runs the JSON labs).
- `tools/check-course.mjs` — checks every local Markdown link and runs gofmt/vet/tests for the Go tools.

## Rules
- Do not change course content (modules, appendices, outline, lab guides, lab JSON, Go code) unless the issue explicitly asks. If a task seems to need a content change, say so in the PR instead.
- Every PR must keep `node tools/check-course.mjs` passing.
- The labs need root and network namespaces; do not run `netlab run` in CI. `go test ./...` in `labs/go` is safe (loopback only).
- Prefer the standard library; the Go module has no third-party dependencies. Keep it that way.
- Write plainly: short sentences, no marketing language.

## Commands
```bash
node tools/check-course.mjs                      # links + Go format/vet/test
cd labs/go && go build -o bin/ ./cmd/...         # build the tools
mdbook build                                     # the website (once book.toml exists)
```
