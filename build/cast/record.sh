#!/bin/bash

# Simple asciinema recorder for qemubox demo
# Usage: ./record.sh [output-name]

set -e

OUTPUT="${1:-qemubox-demo}"

# Check if asciinema is installed
if ! command -v asciinema &> /dev/null; then
    echo "Error: asciinema is not installed"
    echo ""
    echo "Install it with:"
    echo "  Ubuntu/Debian: sudo apt-get install asciinema"
    echo "  macOS: brew install asciinema"
    echo "  pip: pip install asciinema"
    exit 1
fi

# Check if expect is installed
if ! command -v expect &> /dev/null; then
    echo "Error: expect is not installed"
    echo ""
    echo "Install it with:"
    echo "  Ubuntu/Debian: sudo apt-get install expect"
    echo "  macOS: brew install expect"
    exit 1
fi

# Check if the expect script exists
if [ ! -f "qemubox.exp" ]; then
    echo "Error: qemubox.exp not found"
    echo "Make sure you're in the correct directory"
    exit 1
fi

echo "╔════════════════════════════════════════════════════════════╗"
echo "║                                                            ║"
echo "║         QemuBox Demo - Asciinema Recording                ║"
echo "║                                                            ║"
echo "╚════════════════════════════════════════════════════════════╝"
echo ""
echo "Output file: ${OUTPUT}.cast"
echo ""
echo "The demo will run automatically with realistic typing."
echo "Recording will start in 3 seconds..."
echo ""

sleep 1
echo "3..."
sleep 1
echo "2..."
sleep 1
echo "1..."
sleep 1

# Terminal size for recording (120 cols is more compatible than default)
COLS=120
ROWS=40

# Record with asciinema
echo ""
echo "🔴 Recording started (${COLS}x${ROWS} terminal)..."
echo ""

# Set terminal size for the recording
export COLUMNS=$COLS
export LINES=$ROWS

if ! stty cols $COLS rows $ROWS 2>/dev/null; then
    echo "Note: Could not set terminal size, using current size"
fi

if ! asciinema rec "${OUTPUT}.cast" -c "expect qemubox.exp" --cols $COLS --rows $ROWS --overwrite; then
    echo ""
    echo "❌ Recording failed!"
    echo ""
    echo "The demo encountered an error. Check the output above."
    echo "The partial recording may still be saved to: ${OUTPUT}.cast"
    exit 1
fi

echo ""
echo "✅ Recording complete!"
echo ""
echo "📁 Saved to: ${OUTPUT}.cast"
echo ""
echo "Next steps:"
echo "─────────────────────────────────────────────────────────────"
echo ""
echo "  ▶️  Play the recording:"
echo "      asciinema play ${OUTPUT}.cast"
echo ""
echo "  📤 Upload to asciinema.org:"
echo "      asciinema upload ${OUTPUT}.cast"
echo ""
echo ""
echo "  📊 Get file info:"
echo "      file ${OUTPUT}.cast"
echo "      ls -lh ${OUTPUT}.cast"
echo ""

# Cleanup
echo "🧹 Cleaning up..."
CTR="ctr --address /var/run/qemubox/containerd.sock"
$CTR container rm demo-vm 2>/dev/null || true
echo "✅ Cleanup complete"
echo ""
