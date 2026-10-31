#!/usr/bin/env bash
# Rewrite relative Markdown links to non-Markdown files (Go commands, lab JSON,
# scripts, directories) as GitHub URLs, so they work in the mdBook site:
#   files       -> REPO_URL/blob/REF/<path>
#   directories -> REPO_URL/tree/REF/<path>
# Links to .md files, http(s):, mailto:, #anchors, images and anything inside
# fenced code blocks or inline code are left alone.
#
# Usage: bash tools/book-links.sh SRC_DIR REPO_URL REF
#
# SRC_DIR is edited in place. It must be a throwaway copy of the repository
# (the Pages workflow makes one with git ls-files), never the checkout itself.
set -Eeuo pipefail
if [[ $# -ne 3 ]]; then
    printf 'usage: %s SRC_DIR REPO_URL REF\n' "$0" >&2
    exit 2
fi
if [[ ! -d $1 ]]; then
    printf 'book-links: %s is not a directory\n' "$1" >&2
    exit 2
fi
checkout=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)
target=$(cd "$1" && pwd -P)
if [[ $target == "$checkout" || $target == "$checkout"/* || $checkout == "$target"/* || $target == / ]]; then
    printf 'book-links: %s overlaps the checkout at %s; pass a copy\n' "$1" "$checkout" >&2
    exit 2
fi
if command -v git >/dev/null && git -C "$1" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf 'book-links: %s is inside a git work tree; pass a copy\n' "$1" >&2
    exit 2
fi
exec python3 "$(dirname "${BASH_SOURCE[0]}")/book-links.py" "$@"
