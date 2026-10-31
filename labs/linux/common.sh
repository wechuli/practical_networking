#!/usr/bin/env bash
set -Eeuo pipefail

LAB_NAMESPACES=()
LAB_PIDS=()
LAB_SECRET_FILES=()
LAB_PREFIX="netcourse-$$-${RANDOM}"
LAB_DIR=""

require_commands() {
    local executable
    for executable in "$@"; do
        command -v "$executable" >/dev/null || {
            printf 'Missing prerequisite: %s\n' "$executable" >&2
            return 1
        }
    done
}

cleanup() {
    local process namespace secret_file
    trap - EXIT
    set +e
    for process in "${LAB_PIDS[@]}"; do
        kill "$process" 2>/dev/null
    done
    for namespace in "${LAB_NAMESPACES[@]}"; do
        while read -r process; do
            [[ -z "$process" ]] || kill "$process" 2>/dev/null
        done < <(ip netns pids "$namespace" 2>/dev/null)
        ip netns delete "$namespace"
    done
    for process in "${LAB_PIDS[@]}"; do
        kill -KILL "$process" 2>/dev/null
        wait "$process" 2>/dev/null
    done
    for secret_file in "${LAB_SECRET_FILES[@]}"; do
        rm -f "$secret_file"
    done
    if [[ -n "$LAB_DIR" ]]; then
        printf 'Evidence retained in %s\n' "$LAB_DIR"
    fi
}

lab_start() {
    [[ "$(uname -s)" == Linux ]] || {
        printf 'Run this lab inside the dedicated Ubuntu VM, not macOS.\n' >&2
        return 1
    }
    [[ "$EUID" -eq 0 ]] || {
        printf 'Network namespace creation requires root in the lab VM.\n' >&2
        return 1
    }
    require_commands ip timeout "$@"
    umask 077
    LAB_DIR=$(mktemp -d /tmp/netcourse.XXXXXXXX)
    trap cleanup EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM
    uname -a > "$LAB_DIR/environment.txt"
    ip -Version >> "$LAB_DIR/environment.txt"
    mkdir "$LAB_DIR/www"
    printf 'network-course-ok\n' > "$LAB_DIR/www/index.html"
}

create_namespace() {
    ip netns add "$1"
    LAB_NAMESPACES+=("$1")
    ip -n "$1" link set lo up
}

ns() {
    local namespace=$1
    shift
    ip netns exec "$namespace" "$@"
}

connect_namespaces() {
    local left=$1 right=$2 left_device=$3 right_device=$4 left_address=$5 right_address=$6
    ip -n "$left" link add "$left_device" type veth peer name "$right_device" netns "$right"
    ip -n "$left" address add "$left_address" dev "$left_device"
    ip -n "$right" address add "$right_address" dev "$right_device"
    ip -n "$left" link set "$left_device" up
    ip -n "$right" link set "$right_device" up
}

start_http() {
    local namespace=$1 address=$2 port=$3
    ns "$namespace" python3 -m http.server "$port" --bind "$address" --directory "$LAB_DIR/www" \
        > "$LAB_DIR/http-${namespace}-${port}.log" 2>&1 &
    LAB_PIDS+=("$!")
    ns "$namespace" timeout 12 curl --noproxy '*' --fail --silent --show-error \
        --max-time 1 --retry 10 --retry-connrefused --retry-delay 1 --retry-max-time 10 \
        "http://${address}:${port}/" >/dev/null
}

expect_http() {
    local namespace=$1 address=$2 port=$3
    ns "$namespace" curl --noproxy '*' --fail --silent --show-error --max-time 3 \
        "http://${address}:${port}/" | grep -qx network-course-ok
}

expect_blocked() {
    local namespace=$1 address=$2 port=$3 expected=${4:-28} status=0
    ns "$namespace" curl --noproxy '*' --fail --silent --show-error --max-time 2 \
        "http://${address}:${port}/" > "$LAB_DIR/denied.out" 2> "$LAB_DIR/denied.err" || status=$?
    if [[ "$status" -ne "$expected" ]]; then
        printf 'FAIL: expected curl status %s, got %s for %s:%s\n' "$expected" "$status" "$address" "$port" >&2
        return 1
    fi
    cat "$LAB_DIR/denied.err"
}

routed_topology() {
    client="${LAB_PREFIX}-client"
    router="${LAB_PREFIX}-router"
    server="${LAB_PREFIX}-server"
    create_namespace "$client"
    create_namespace "$router"
    create_namespace "$server"
    connect_namespaces "$client" "$router" eth0 eth0 198.18.0.1/24 198.18.0.254/24
    connect_namespaces "$router" "$server" eth1 eth0 198.19.0.254/24 198.19.0.2/24
    ns "$router" sysctl -qw net.ipv4.ip_forward=1
    ip -n "$client" route add 198.19.0.0/24 via 198.18.0.254
    ip -n "$server" route add 198.18.0.0/24 via 198.19.0.254
}

start_capture() {
    local namespace=$1 interface=$2 name=$3 filter=$4
    CAPTURE_FILE="$LAB_DIR/$name.pcap"
    ip netns exec "$namespace" tcpdump -Z root -U -n -i "$interface" \
        -w "$CAPTURE_FILE" "$filter" > "$LAB_DIR/$name.capture.log" 2>&1 &
    CAPTURE_PID=$!
    LAB_PIDS+=("$CAPTURE_PID")
    local deadline=$((SECONDS + 5))
    until grep -q 'listening on' "$LAB_DIR/$name.capture.log"; do
        kill -0 "$CAPTURE_PID"
        if (( SECONDS >= deadline )); then
            printf 'Capture did not become ready\n' >&2
            return 1
        fi
        sleep 0.05
    done
}

forget_pid() {
    local index
    for index in "${!LAB_PIDS[@]}"; do
        if [[ "${LAB_PIDS[$index]}" == "$1" ]]; then
            unset 'LAB_PIDS[index]'
        fi
    done
}

stop_capture() {
    kill -INT "$CAPTURE_PID"
    wait "$CAPTURE_PID"
    forget_pid "$CAPTURE_PID"
    [[ -s "$CAPTURE_FILE" ]]
}
