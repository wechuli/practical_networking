"""Rewrite relative links to non-Markdown files as GitHub URLs.

Called by tools/book-links.sh; see that file for usage. Every Markdown file
under SRC_DIR is edited in place, so SRC_DIR must be a throwaway copy.
"""

import os
import posixpath
import re
import sys
from urllib.parse import quote, unquote

FENCE = re.compile(r"^\s*(`{3,}|~{3,})(.*)$")
# [text](target) with at most one level of brackets inside the text, like
# [`a[0]`](target). The leading group catches images, which are left alone.
LINK = re.compile(r"(!?)\[(?:[^\[\]]|\[[^\[\]]*\])*\]\(([^)\s]+)\)")
NOT_RELATIVE = re.compile(r"^([a-zA-Z][a-zA-Z0-9+.-]*:|[#/<])")


def code_spans(line):
    """Return (start, end) offsets of inline code spans in one line."""
    runs = [(m.start(), m.end()) for m in re.finditer(r"`+", line)]
    spans = []
    k = 0
    while k < len(runs):
        start, end = runs[k]
        for j in range(k + 1, len(runs)):
            if runs[j][1] - runs[j][0] == end - start:
                spans.append((start, runs[j][1]))
                k = j
                break
        k += 1
    return spans


def rewrite_target(target, page, root, base, ref):
    """Return the GitHub URL for target, or None to leave it alone."""
    if NOT_RELATIVE.match(target):
        return None
    path, hash_, fragment = target.partition("#")
    if not path or path.lower().endswith(".md"):
        return None
    resolved = posixpath.normpath(
        posixpath.join(posixpath.dirname(page), unquote(path))
    )
    if resolved == ".." or resolved.startswith("../"):
        print(f"book-links: {page}: {target} is outside the tree; left alone", file=sys.stderr)
        return None
    full = os.path.join(root, *resolved.split("/"))
    if not os.path.exists(full):
        print(f"book-links: {page}: {target} does not exist", file=sys.stderr)
    kind = "tree" if os.path.isdir(full) else "blob"
    url = f"{base}/{kind}/{ref}"
    if resolved != ".":
        url += "/" + quote(resolved, safe="/")
    return url + hash_ + fragment


def rewrite_page(text, page, root, base, ref):
    """Return (new text, number of links rewritten) for one Markdown page."""
    out = []
    count = 0
    fence = None
    for line in text.splitlines(keepends=True):
        match = FENCE.match(line.rstrip("\r\n"))
        if match:
            marker, rest = match.groups()
            if fence is None:
                fence = marker
            elif marker[0] == fence[0] and len(marker) >= len(fence) and not rest.strip():
                fence = None
            out.append(line)
            continue
        if fence is not None:
            out.append(line)
            continue
        spans = code_spans(line)
        pieces = []
        last = 0
        for link in LINK.finditer(line):
            if link.group(1) or any(s < link.start() < e for s, e in spans):
                continue
            url = rewrite_target(link.group(2), page, root, base, ref)
            if url is None:
                continue
            pieces += [line[last : link.start(2)], url]
            last = link.end(2)
            count += 1
        out.append("".join(pieces) + line[last:])
    return "".join(out), count


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: book-links.py SRC_DIR REPO_URL REF")
    root = os.path.realpath(sys.argv[1])
    base = sys.argv[2].rstrip("/")
    ref = sys.argv[3]
    links = files = 0
    for directory, subdirs, names in os.walk(root):
        subdirs.sort()
        for name in sorted(names):
            full = os.path.join(directory, name)
            if not name.lower().endswith(".md") or os.path.islink(full):
                continue
            page = os.path.relpath(full, root).replace(os.sep, "/")
            with open(full, encoding="utf-8", newline="") as handle:
                text = handle.read()
            new, count = rewrite_page(text, page, root, base, ref)
            if count:
                with open(full, "w", encoding="utf-8", newline="") as handle:
                    handle.write(new)
                links += count
                files += 1
    print(f"book-links: rewrote {links} links in {files} files")


if __name__ == "__main__":
    main()
