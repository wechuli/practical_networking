#!/usr/bin/env bash
set -Eeuo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/../labs/linux/common.sh"
LAB_DIR=$(mktemp -d)
trap 'rm -rf "$LAB_DIR"' EXIT
ns() { return "$SIMULATED_CURL_STATUS"; }
SIMULATED_CURL_STATUS=28
expect_blocked test 198.18.0.1 8080
SIMULATED_CURL_STATUS=7
expect_blocked test 198.18.0.1 80 7
for SIMULATED_CURL_STATUS in 0 6 22 127; do
    if expect_blocked test 198.18.0.1 8080 2>/dev/null; then
        printf 'Unexpected status was accepted as policy denial\n' >&2
        exit 1
    fi
done
LAB_PIDS=(100 200 300)
forget_pid 200
[[ "${LAB_PIDS[*]}" == '100 300' ]]
forget_pid 100
forget_pid 300
[[ "${#LAB_PIDS[@]}" -eq 0 ]]
printf 'PASS: denial assertions reject success, DNS, HTTP and missing-command errors.\n'
