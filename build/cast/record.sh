#!/usr/bin/env bash
# Record the SpinBox demo through a real tmux pane instead of expect.
# Usage: ./record-tmux.sh [output-name]

set -euo pipefail

OUTPUT="${1:-spinbox-demo}"
CTR="ctr --address /var/run/spin-stack/containerd.sock"
IMAGE="ghcr.io/spin-stack/spinbox/sandbox:latest"
SESSION="spinbox-demo-record-$$"
COLS=240
ROWS=40
TYPE_DELAY=0.015
SECTION_PAUSE=1.2
OUTPUT_PAUSE=1.8

cleanup_vm() {
    $CTR task kill demo-vm 2>/dev/null || true
    $CTR task delete demo-vm 2>/dev/null || true
    $CTR container rm demo-vm 2>/dev/null || true
    $CTR snapshots --snapshotter spin-erofs rm demo-vm 2>/dev/null || true
}

cleanup() {
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    cleanup_vm
}
trap cleanup EXIT

for cmd in asciinema tmux ctr; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: $cmd is not installed"
        exit 1
    fi
done

pane_text() {
    tmux capture-pane -t "$SESSION:0.0" -p -S -2000 2>/dev/null || true
                                                                                                                                                }

                                                                                                                                                                                                                      count_re() {
    local pattern="$1"
    pane_text | grep -Eo "$pattern" | wc -l | tr -d ' '
}

wait_for_re() {
    local pattern="$1"
    local timeout_s="${2:-30}"
    local end=$((SECONDS + timeout_s))    while (( SECONDS < end )); do
        if pane_text | grep -Eq "$pattern"; then
            return 0
        fi
        sleep 0.1
    done
    echo "ERROR: timed out waiting for pattern: $pattern" >&2
    return 1
}

wait_for_new_re() {
    local pattern="$1"
    local previous="$2"
    local timeout_s="${3:-30}"
    local end=$((SECONDS + timeout_s))
    while (( SECONDS < end )); do
        local current
        current=$(count_re "$pattern")
        if (( current > previous )); then
            return 0
        fi
        sleep 0.1
    done
    echo "ERROR: timed out waiting for new pattern: $pattern" >&2
    return 1
}

send_line() {
    local line="$1"
    tmux send-keys -t "$SESSION:0.0" -l "$line"
    tmux send-keys -t "$SESSION:0.0" C-m
}

type_line() {
    local line="$1"
    local i char
    for ((i = 0; i < ${#line}; i++)); do
        char="${line:i:1}"
        tmux send-keys -t "$SESSION:0.0" -l "$char"
        sleep "$TYPE_DELAY"
    done
    tmux send-keys -t "$SESSION:0.0" C-m
}

run_wait_prompt() {
    local line="$1"
    local prompt_pattern="$2"
    local timeout_s="${3:-60}"
    local before
    before=$(count_re "$prompt_pattern")
    type_line "$line"
    wait_for_new_re "$prompt_pattern" "$before" "$timeout_s"
    sleep "$OUTPUT_PAUSE"                                                                                                                                                                                              }
                                                                                                                                                                                                                      comment() {                                                                                                                                                                                                                local before                                                                                                                                                                                                           before=$(count_re "$2")                                                                                                                                                                                                type_line "# $1"    wait_for_new_re "$2" "$before" 10
    sleep "$SECTION_PAUSE"
}

cleanup_vm
rm -f "${OUTPUT}.cast"

# Make a real terminal endpoint for ctr. The controller sends keys from outside;
# it does not sit between ctr and the terminal like expect/pexpect does.
tmux new-session -d -s "$SESSION" -x "$COLS" -y "$ROWS" \
    "bash --norc --noprofile -i"
tmux set-option -t "$SESSION" status off >/dev/null
sleep 0.5
send_line "PS1='HOST_READY# '"
wait_for_re 'HOST_READY#' 10
send_line "bind 'set enable-bracketed-paste off' 2>/dev/null || true"
wait_for_re 'HOST_READY#' 10
send_line "clear"
sleep 0.2
wait_for_re 'HOST_READY#' 10

controller() {
    wait_for_re 'HOST_READY#' 10

    comment '=== HOST: Starting SpinBox VM ===' 'HOST_READY#'
    comment 'Pull spinbox sandbox (Ubuntu 26.04 + Docker)' 'HOST_READY#'
    run_wait_prompt "$CTR image pull --snapshotter spin-erofs $IMAGE" 'HOST_READY#' 120

    comment 'Launch VM (watch the boot speed!)' 'HOST_READY#'
    type_line "$CTR run -t --snapshotter spin-erofs --runtime io.containerd.spinbox.v1 $IMAGE demo-vm"

    wait_for_re 'localhost login:' 60
    tmux send-keys -t "$SESSION:0.0" C-u
    type_line 'root'
    wait_for_re 'Password:' 20
    type_line 'spinbox'
    wait_for_re 'root@localhost:[^#]*#' 30

    send_line "unset PROMPT_COMMAND; bind 'set enable-bracketed-paste off' 2>/dev/null || true; PS1='VM_READY# '"
    sleep 0.8
    wait_for_re 'VM_READY#' 10

    comment '=== INSIDE VM ===' 'VM_READY#'
    comment 'VM kernel (isolated from host)' 'VM_READY#'
    run_wait_prompt 'uname -r' 'VM_READY#' 20

    comment 'Boot time' 'VM_READY#'
    run_wait_prompt 'systemd-analyze time' 'VM_READY#' 20

    comment 'Service startup costs' 'VM_READY#'
    run_wait_prompt 'systemd-analyze blame | head -10' 'VM_READY#' 20

    comment '=== DOCKER IN VM ===' 'VM_READY#'
    comment 'Docker version' 'VM_READY#'
    run_wait_prompt "docker version --format 'Docker {{.Server.Version}}'" 'VM_READY#' 30

    comment 'Pull Alpine' 'VM_READY#'
    run_wait_prompt 'docker pull alpine:latest' 'VM_READY#' 120

    comment 'Run container (same kernel as VM)' 'VM_READY#'
    run_wait_prompt 'docker run --rm alpine:latest uname -a' 'VM_READY#' 60

    comment 'Shutting down VM' 'VM_READY#'
    local host_prompts_before
    host_prompts_before=$(count_re 'HOST_READY#')
    type_line 'halt'
    wait_for_new_re 'HOST_READY#' "$host_prompts_before" 60 || true

    type_line 'exit'
}

controller &
controller_pid=$!

echo "SpinBox tmux Demo - Recording to ${OUTPUT}.cast"
echo "Recording..."
asciinema rec "${OUTPUT}.cast" \
    -c "tmux attach-session -t '$SESSION'" \
    --return --window-size "${COLS}x${ROWS}" --overwrite

wait "$controller_pid" 2>/dev/null || true

echo ""
echo "Recording saved to: ${OUTPUT}.cast"
echo "Play:   asciinema play ${OUTPUT}.cast"
echo "Upload: asciinema upload ${OUTPUT}.cast"
