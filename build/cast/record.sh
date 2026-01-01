#!/bin/bash
# Simple asciinema recorder for qemubox demo
# Usage: ./record.sh [output-name]

set -e

OUTPUT="${1:-qemubox-demo}"

# Check dependencies
for cmd in asciinema expect; do
    if ! command -v $cmd &> /dev/null; then
        echo "Error: $cmd is not installed"
        exit 1
    fi
done

# Check expect script exists
[ -f "qemubox.exp" ] || { echo "Error: qemubox.exp not found"; exit 1; }

echo "QemuBox Demo - Recording to ${OUTPUT}.cast"

# Pre-cleanup to avoid conflicts from previous runs
CTR="ctr --address /var/run/qemubox/containerd.sock"
$CTR task kill demo-vm 2>/dev/null || true
$CTR container rm demo-vm 2>/dev/null || true

echo "Starting in 3 seconds..."
sleep 3

# Terminal size
COLS=120
ROWS=40
export COLUMNS=$COLS LINES=$ROWS
stty cols $COLS rows $ROWS 2>/dev/null || true

# Record
echo "🔴 Recording..."
asciinema rec "${OUTPUT}.cast" -c "expect qemubox.exp" \
    --cols $COLS --rows $ROWS --overwrite || {
    echo "❌ Recording failed"
    exit 1
}

# Success
echo "✅ Recording saved to: ${OUTPUT}.cast"
echo ""
echo "Play:   asciinema play ${OUTPUT}.cast"
echo "Upload: asciinema upload ${OUTPUT}.cast"

# Cleanup
$CTR task kill demo-vm 2>/dev/null || true
$CTR container rm demo-vm 2>/dev/null || true
