# Copilot instructions — Practical Networking

A hands-on networking course. Markdown notes plus Linux lab tooling written in Go.

## Layout
- `modules/module-NN-*.md` — the chapters: 00–23 and 26 (capstones). 24 and 25 are planned, no files yet.
- `appendices.md`, `networking_course_outline.md`, `implementation.md` (what was changed and verified), `improvements.md` (the original review).
- `labs/` — lab guides (`README.md`, `index.md`, …), `labs/netlab/*.json` (namespace lab definitions), `labs/go/` (Go module `github.com/wechuli/practical_networking/labs/go`; one tool per `cmd/<name>`; `netlab` runs the JSON labs).
- `tools/check-course.mjs` — checks every local Markdown link and runs gofmt/vet/tests for the Go tools.
- `.vitepress/` — the VitePress site (published to GitHub Pages by `.github/workflows/pages.yml`). The Markdown stays where it is; `config.mts` holds the sidebar and rewrites relative links to non-Markdown files (Go sources, lab JSON, scripts) to GitHub URLs. `index.md` is the site home page; `README.md` is the repository front page.

## Rules
- Do not change course content (modules, appendices, outline, lab guides, lab JSON, Go code) unless the issue explicitly asks. If a task seems to need a content change, say so in the PR instead.
- Every PR must keep `node tools/check-course.mjs` and `npm run docs:build` passing.
- Markdown outside code is compiled as a Vue template: don't write raw `{{ }}` or HTML-like `<tags>` in prose; put them in inline code or a code block.
- The labs need root and network namespaces; do not run `netlab run` in CI. `go test ./...` in `labs/go` is safe (loopback only).
- Prefer the standard library; the Go module has no third-party dependencies. Keep it that way.
- Write plainly: short sentences, no marketing language.

## Diagrams
- Diagrams are hand-written SVG files, not ASCII art in code blocks and not Mermaid. They live beside the page: `modules/img/NN/<name>.svg` for module NN, `labs/img/<name>.svg` for lab guides.
- Copy the `<style>` and `<defs>` block from `modules/img/00/course-map.svg` unchanged. Use its classes: `bg` (outer panel), `box` (neutral), `net` (hosts, routers, network devices), `warn` (failure, attack, danger), `ok` (success, the healthy end state), `l2`/`l3`/`l4`/`l7` (link, network, transport and application layers: use these consistently for headers and layers), `zone` (dashed grouping), text `t` (box title), `s` (detail), `l` (label), `m` (monospace: addresses, commands, field names), `zl` (zone or section label), lines `arrow`, `dash` (failure path) and `line` (plain connector).
- Root element: `role="img"`, `aria-labelledby="t d"`, a `<title id="t">` and a `<desc id="d">` that says what the diagram shows. `viewBox` 800 wide (up to 820). Keep every label inside its box with 10px or more padding; no text over lines or arrows.
- Keep every fact, number, address and label from the original; a diagram is a redraw, not a rewrite.
- In the Markdown, the image alt text is a full sentence that describes the diagram: `![A full sentence.](./img/NN/name.svg)`.
- Check every diagram by eye before you finish: `tools/render-svgs.sh modules/img/NN` prints PNG paths to open. Fix overlaps and clipped text.

## Commands
```bash
node tools/check-course.mjs                      # links + Go format/vet/test
cd labs/go && go build -o bin/ ./cmd/...         # build the tools
npm ci && npm run docs:dev                       # the website, locally (VitePress)
npm run docs:build                               # build the site; fails on dead links
```
