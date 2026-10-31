#!/usr/bin/env bash
set -Eeuo pipefail
[[ "$(uname -s)" == Linux && "$EUID" -eq 0 ]] || {
    printf 'Use root inside the disposable Ubuntu VM.\n' >&2
    exit 1
}
cd "$(dirname "${BASH_SOURCE[0]}")/.."
command -v go >/dev/null || PATH="$PATH:/usr/local/go/bin"   # sudo resets PATH
(cd labs/go && go build -o bin/ ./cmd/...)
for lab in bridge vlan stp osi ip ipv6 arp icmp routing ospf bgp udp tcp dns tls perf web containers docker k8s firewall tunnels sockets capture observe dhcp edge time case; do
    printf '\nRUN: netlab %s\n' "$lab"
    status=0
    timeout --signal=INT --kill-after=10 600 labs/go/bin/netlab run -quiet "labs/netlab/$lab.json" || status=$?
    if (( status == 3 )); then
        printf 'SKIPPED: %s (see the reason above)\n' "$lab"
    elif (( status != 0 )); then
        exit "$status"
    fi
done
printf '\nPASS: Linux fixtures completed. Run the interrupted-cleanup gate separately.\n'
