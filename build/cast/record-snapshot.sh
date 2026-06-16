#!/usr/bin/env bash
# Record the SpinBox snapshot demo through a real tmux pane instead of expect.
# Usage: ./record-snapshot-tmux.sh [output-name]

set -euo pipefail

OUTPUT="${1:-spinbox-snapshot-demo}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CTR="ctr --address /var/run/spin-stack/containerd.sock"
IMAGE="ghcr.io/spin-stack/spinbox/sandbox:latest"
SNAPSHOT_IMAGE="docker.io/spin-stack/sandbox:with-changes"
COMMITTED_SNAPSHOT="snapshot-demo-committed"
HELPER_BIN="/tmp/spinbox-snapshot-commit-image"
SESSION="spinbox-snapshot-record-$$"
COLS=240
ROWS=40
TYPE_DELAY=0.015
SECTION_PAUSE=1.2
OUTPUT_PAUSE=1.8

cleanup_vm() {
    for name in snapshot-demo snapshot-new; do
        $CTR task kill "$name" 2>/dev/null || true
        $CTR task delete "$name" 2>/dev/null || true
        $CTR container rm "$name" 2>/dev/null || true
        $CTR snapshots --snapshotter spin-erofs rm "$name" 2>/dev/null || true
        $CTR snapshots --snapshotter spin-erofs rm "${name}-snapshot" 2>/dev/null || true
    done
    $CTR snapshots --snapshotter spin-erofs rm "$COMMITTED_SNAPSHOT" 2>/dev/null || true
    $CTR images rm "$SNAPSHOT_IMAGE" 2>/dev/null || true
}

cleanup() {
    tmux kill-session -t "$SESSION" 2>/dev/null || true
    cleanup_vm
    rm -f "$HELPER_BIN" 2>/dev/null || true
}
trap cleanup EXIT

for cmd in asciinema tmux ctr go; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: $cmd is not installed"
        exit 1
    fi
done

go build -o "$HELPER_BIN" "$SCRIPT_DIR/snapshot-commit-image.go"

pane_text() {
    tmux capture-pane -t "$SESSION:0.0" -p -S -3000 2>/dev/null || true
}

count_re() {
    local pattern="$1"
    pane_text | { grep -Eo "$pattern" || true; } | wc -l | tr -d ' '
}

wait_for_re() {
    local pattern="$1"
    local timeout_s="${2:-30}"
    local end=$((SECONDS + timeout_s))
    while (( SECONDS < end )); do
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
    sleep "$OUTPUT_PAUSE"
}

comment() {
    local before
    before=$(count_re "$2")
    type_line "# $1"
    wait_for_new_re "$2" "$before" 10
    sleep "$SECTION_PAUSE"
}


wait_task_stopped() {
    local name="$1"
    local timeout_s="${2:-60}"
    local end=$((SECONDS + timeout_s))
    while (( SECONDS < end )); do
        local status
        status=$($CTR tasks ls 2>/dev/null | awk -v name="$name" '$1 == name {print $3}')
        if [[ -z "$status" || "$status" == "STOPPED" ]]; then
            return 0
        fi
        sleep 0.2
    done
    echo "ERROR: timed out waiting for task to stop: $name" >&2
    return 1
}


task_status() {
    local name="$1"
    $CTR tasks ls 2>/dev/null | awk -v name="$name" '$1 == name {print $3}'
}

wait_for_vm_login() {
    local name="$1"
    local login_before="$2"
    local host_before="$3"
    local timeout_s="${4:-60}"
    local end=$((SECONDS + timeout_s))
    while (( SECONDS < end )); do
        local login_now host_now status
        login_now=$(count_re 'localhost login:')
        if (( login_now > login_before )); then
            return 0
        fi

        host_now=$(count_re 'HOST_READY#')
        status=$(task_status "$name")
        if (( host_now > host_before )) && [[ -z "$status" || "$status" == "STOPPED" ]]; then
            echo "ERROR: VM $name exited before login" >&2
            return 1
        fi
        sleep 0.1
    done
    echo "ERROR: timed out waiting for VM login: $name" >&2
    return 1
}

ensure_image_exists() {
    local image="$1"
    if ! $CTR images ls 2>/dev/null | awk -v image="$image" '$1 == image {found = 1} END {exit found ? 0 : 1}'; then
        echo "ERROR: expected image was not created: $image" >&2
        return 1
    fi
}

ensure_snapshot_exists() {
    local snapshot="$1"
    if ! $CTR snapshots --snapshotter spin-erofs ls 2>/dev/null | awk -v snapshot="$snapshot" '$1 == snapshot {found = 1} END {exit found ? 0 : 1}'; then
        echo "ERROR: expected snapshot was not created: $snapshot" >&2
        return 1
    fi
}

login_vm() {
    tmux send-keys -t "$SESSION:0.0" C-u

    local password_before
    password_before=$(count_re 'Password:')
    type_line 'root'
    wait_for_new_re 'Password:' "$password_before" 20

    local root_prompt_before
    root_prompt_before=$(count_re 'root@localhost:[^#]*#')
    type_line 'spinbox'
    wait_for_new_re 'root@localhost:[^#]*#' "$root_prompt_before" 30

    local vm_prompt_before
    vm_prompt_before=$(count_re 'VM_READY#')
    send_line "unset PROMPT_COMMAND; bind 'set enable-bracketed-paste off' 2>/dev/null || true; PS1='VM_READY# '"
    sleep 0.8
    wait_for_new_re 'VM_READY#' "$vm_prompt_before" 10
}

cleanup_vm
rm -f "${OUTPUT}.cast"

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

    comment '=== SPINBOX SNAPSHOT DEMO ===' 'HOST_READY#'
    comment 'Demonstrating persistent disk state between VM runs' 'HOST_READY#'
    comment 'Pull the sandbox image' 'HOST_READY#'
    run_wait_prompt "$CTR image pull --snapshotter spin-erofs $IMAGE" 'HOST_READY#' 120

    comment 'Run original VM and make changes' 'HOST_READY#'
    local login_before host_before
    login_before=$(count_re 'localhost login:')
    host_before=$(count_re 'HOST_READY#')
    type_line "$CTR run -t --snapshotter spin-erofs --runtime io.containerd.spinbox.v1 $IMAGE snapshot-demo"
    wait_for_vm_login snapshot-demo "$login_before" "$host_before" 60
    login_vm

    comment '=== INSIDE VM: Making persistent changes ===' 'VM_READY#'
    comment 'Create a file that will persist' 'VM_READY#'
    run_wait_prompt "echo 'Hello from the original VM!' > /root/persistent-data.txt" 'VM_READY#' 20

    comment 'Create a directory with content' 'VM_READY#'
    run_wait_prompt "mkdir -p /root/my-project && echo 'version: 1.0' > /root/my-project/config.yaml" 'VM_READY#' 20

    comment 'Add timestamp and extra content' 'VM_READY#'
    run_wait_prompt "date > /root/timestamp.txt && echo 'snapshot test' >> /root/persistent-data.txt" 'VM_READY#' 20

    comment 'Show what we created' 'VM_READY#'
    run_wait_prompt 'cat /root/persistent-data.txt && cat /root/my-project/config.yaml && cat /root/timestamp.txt' 'VM_READY#' 20

    comment 'Sync filesystem and stop VM' 'VM_READY#'
    run_wait_prompt 'sync' 'VM_READY#' 20
    type_line 'halt'
    wait_task_stopped snapshot-demo 60 || true
    wait_for_re 'HOST_READY#' 20 || true
    sleep "$OUTPUT_PAUSE"

    comment '=== HOST: Create image from VM ===' 'HOST_READY#'
    comment 'Commit stopped VM snapshot' 'HOST_READY#'
    run_wait_prompt "$CTR snapshots --snapshotter spin-erofs commit $COMMITTED_SNAPSHOT snapshot-demo" 'HOST_READY#' 60
    ensure_snapshot_exists "$COMMITTED_SNAPSHOT"

    comment 'Create image from committed snapshot' 'HOST_READY#'
    run_wait_prompt "$HELPER_BIN --address /var/run/spin-stack/containerd.sock --snapshotter spin-erofs --source-image $IMAGE --snapshot $COMMITTED_SNAPSHOT --image $SNAPSHOT_IMAGE" 'HOST_READY#' 120
    ensure_image_exists "$SNAPSHOT_IMAGE"

    comment 'Verify new image was created' 'HOST_READY#'
    run_wait_prompt "$CTR images ls | grep with-changes" 'HOST_READY#' 30

    comment 'Compare original image metadata' 'HOST_READY#'
    run_wait_prompt "$CTR images inspect $IMAGE | head -12" 'HOST_READY#' 30

    comment 'New image includes our committed changes' 'HOST_READY#'
    run_wait_prompt "$CTR images inspect $SNAPSHOT_IMAGE | head -13" 'HOST_READY#' 30

    comment 'Clean up original VM container' 'HOST_READY#'
    run_wait_prompt "$CTR task delete snapshot-demo 2>/dev/null || true" 'HOST_READY#' 20
    run_wait_prompt "$CTR container rm snapshot-demo 2>/dev/null || true" 'HOST_READY#' 20
    run_wait_prompt "$CTR snapshots --snapshotter spin-erofs rm snapshot-demo 2>/dev/null || true" 'HOST_READY#' 20

    ensure_image_exists "$SNAPSHOT_IMAGE"
    comment '=== Run VM from our custom image ===' 'HOST_READY#'
    local login_before_new host_before_new
    login_before_new=$(count_re 'localhost login:')
    host_before_new=$(count_re 'HOST_READY#')
    type_line "$CTR run -t --snapshotter spin-erofs --runtime io.containerd.spinbox.v1 $SNAPSHOT_IMAGE snapshot-new"
    wait_for_vm_login snapshot-new "$login_before_new" "$host_before_new" 60
    login_vm

    comment '=== VERIFY: Changes from first VM are here! ===' 'VM_READY#'
    comment 'Check our persistent file' 'VM_READY#'
    run_wait_prompt 'cat /root/persistent-data.txt' 'VM_READY#' 20

    comment 'Check our project directory' 'VM_READY#'
    run_wait_prompt 'cat /root/my-project/config.yaml' 'VM_READY#' 20

    comment 'Check timestamp file' 'VM_READY#'
    run_wait_prompt 'cat /root/timestamp.txt' 'VM_READY#' 20

    comment 'Done. Shutting down custom VM' 'VM_READY#'
    type_line 'halt'
    wait_task_stopped snapshot-new 60 || true
    wait_for_re 'HOST_READY#' 20 || true
    sleep "$OUTPUT_PAUSE"

    comment '=== Cleaning up ===' 'HOST_READY#'
    run_wait_prompt "$CTR task delete snapshot-new 2>/dev/null || true" 'HOST_READY#' 20
    run_wait_prompt "$CTR container rm snapshot-new 2>/dev/null || true" 'HOST_READY#' 20
    run_wait_prompt "$CTR images rm $SNAPSHOT_IMAGE 2>/dev/null || true" 'HOST_READY#' 30
    run_wait_prompt "$CTR snapshots --snapshotter spin-erofs rm $COMMITTED_SNAPSHOT 2>/dev/null || true" 'HOST_READY#' 20

    comment 'Cleanup complete' 'HOST_READY#'
    type_line 'exit'
}

controller &
controller_pid=$!

echo "SpinBox snapshot tmux Demo - Recording to ${OUTPUT}.cast"
echo "Recording..."
asciinema rec "${OUTPUT}.cast" \
    -c "tmux attach-session -t '$SESSION'" \
    --return --window-size "${COLS}x${ROWS}" --overwrite

wait "$controller_pid" 2>/dev/null || true

echo ""
echo "Recording saved to: ${OUTPUT}.cast"
echo "Play:   asciinema play ${OUTPUT}.cast"
echo "Upload: asciinema upload ${OUTPUT}.cast"
