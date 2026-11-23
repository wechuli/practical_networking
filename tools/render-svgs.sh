#!/usr/bin/env bash
# render-svgs.sh DIR [OUT]: renders every SVG in DIR to PNG with headless Chrome,
# so diagrams can be checked by eye before they're committed. OUT defaults to
# /tmp/pn-svg-render. PNGs are for review only; never commit them.
set -euo pipefail
dir=${1:?usage: render-svgs.sh DIR [OUT]}
out=${2:-/tmp/pn-svg-render}
mkdir -p "$out"
for c in "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" google-chrome chromium chromium-browser; do
  if command -v "$c" >/dev/null 2>&1 || [[ -x "$c" ]]; then chrome=$c; break; fi
done
[[ -n "${chrome:-}" ]] || { echo "no Chrome or Chromium found" >&2; exit 1; }
for f in "$dir"/*.svg; do
  read -r w h < <(grep -o 'viewBox="0 0 [0-9]* [0-9]*"' "$f" | head -1 | tr -dc '0-9 ' | awk '{print $3, $4}')
  # A timestamp in the name avoids stale cached previews.
  png="$out/$(basename "${f%.svg}")-$(date +%s).png"
  "$chrome" --headless=new --user-data-dir="$out/chrome-profile" --no-first-run --disable-gpu --hide-scrollbars --force-device-scale-factor=1.5 \
    --window-size="$w,$h" --screenshot="$png" "file://$(cd "$(dirname "$f")" && pwd)/$(basename "$f")" >/dev/null 2>&1
  echo "$png"
done
