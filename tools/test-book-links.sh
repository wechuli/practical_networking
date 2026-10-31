#!/usr/bin/env bash
# Run tools/book-links.sh on a copy of tools/testdata/book-links/input and
# compare the result with tools/testdata/book-links/expected.
set -Eeuo pipefail
TOOLS=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
DATA="$TOOLS/testdata/book-links"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
cp -R "$DATA/input" "$WORK/src"
summary=$(bash "$TOOLS/book-links.sh" "$WORK/src" https://github.com/example/repo 0123abc)
[[ $summary == 'book-links: rewrote 10 links in 2 files' ]] || {
    printf 'Unexpected summary: %s\n' "$summary" >&2
    exit 1
}
# Markdown files must match the expected tree; everything else is untouched.
diff -r -x '*.md' "$DATA/input" "$WORK/src"
diff <(cd "$DATA/expected" && find . -name '*.md' | sort) <(cd "$WORK/src" && find . -name '*.md' | sort)
while IFS= read -r file; do
    diff -u "$DATA/expected/$file" "$WORK/src/$file"
done < <(cd "$DATA/expected" && find . -name '*.md' | sort)
# A second run finds nothing left to rewrite.
summary=$(bash "$TOOLS/book-links.sh" "$WORK/src" https://github.com/example/repo 0123abc)
[[ $summary == 'book-links: rewrote 0 links in 0 files' ]]
# The script refuses to edit a git work tree.
if bash "$TOOLS/book-links.sh" "$TOOLS/.." https://github.com/example/repo 0123abc 2>/dev/null; then
    printf 'book-links.sh edited a git work tree\n' >&2
    exit 1
fi
printf 'PASS: book-links rewrites file and directory links, leaves code, images and Markdown links alone.\n'
