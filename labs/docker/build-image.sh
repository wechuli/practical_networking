#!/usr/bin/env bash
# Builds course/netshell, the small image the Docker lab runs, without
# pulling anything: a static busybox (sh, ip, ping, nslookup, wget, nc …)
# plus the course tools built as static Go binaries, imported as one layer.
#
#   sudo labs/docker/build-image.sh [TAG]     # default course/netshell:1
#
# Needs: docker, go, and a static busybox (Ubuntu: apt-get install busybox-static).
set -Eeuo pipefail
tag=${1:-course/netshell:1}
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
busybox=$(command -v busybox || true)
if [[ -z "$busybox" ]] || ! file -L "$busybox" | grep -q 'statically linked'; then
    echo "need a statically linked busybox (Ubuntu: apt-get install busybox-static)" >&2
    exit 1
fi
root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT
mkdir -p "$root"/{bin,etc,tmp,usr/local/bin,proc,sys,dev,root}
chmod 1777 "$root/tmp"
cp "$busybox" "$root/bin/busybox"
for applet in $("$busybox" --list); do
    [[ -e "$root/bin/$applet" ]] || ln -s busybox "$root/bin/$applet"
done
printf 'root:x:0:0:root:/root:/bin/sh\nnobody:x:65534:65534:nobody:/:/bin/false\n' > "$root/etc/passwd"
printf 'root:x:0:\nnogroup:x:65534:\n' > "$root/etc/group"
(cd "$here/../go" && CGO_ENABLED=0 go build -trimpath -o "$root/usr/local/bin/" ./cmd/weblab ./cmd/tcplab ./cmd/dnslab ./cmd/labserver)
tar -C "$root" -c . | docker import \
    -c 'ENV PATH=/usr/local/bin:/bin' -c 'CMD ["/bin/sh"]' \
    -m "course netshell: static busybox + course tools" - "$tag" >/dev/null
docker image inspect "$tag" --format "built $tag: {{.Size}} bytes, {{.Id}}"
