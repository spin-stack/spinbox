# SpinBox Demo Recording

Two ways to record:

- **`recorder/` (Go, recommended)** — headless PTY recorder. Single process, no
  on-screen rendering, and stays idle during the VM boot, so the recording does
  not steal host CPU from the guest. The boot time shown by `systemd-analyze` in
  the cast matches an interactive run (it also does a warm-up first to generate
  fsmeta/VMDK and warm caches). Emits asciicast v2 directly.
- **`record.sh` (asciinema + expect)** — the original driver. Drives the demo in
  real time and renders to the terminal, which contends with the guest during
  boot and inflates the boot time displayed.

## Go recorder (recommended)

```bash
go run ./build/cast/recorder -out spinbox-demo.cast \
  -image ghcr.io/spin-stack/spinbox/sandbox:latest

# options: -address -snapshotter -runtime -name -cols -rows
#          -warmup=false (skip warm-up)  -warmup-wait 5s
#          -boot-timeout 90s  -type-delay 30ms

asciinema play spinbox-demo.cast
```

Runs on the spinbox host (needs `ctr` in PATH). No `asciinema`/`expect` needed
to record (only to play/upload).

## asciinema + expect

```bash

```bash
# Install dependencies
sudo apt-get install asciinema expect

# Record basic demo
./record.sh demo

# Record snapshot demo
./record.sh snapshot

# Play back
asciinema play spinbox-demo.cast
```

## Usage

```bash
./record.sh [demo|snapshot] [output-name]
```

| Mode | Description | Default Output |
|------|-------------|----------------|
| `demo` | Basic demo (boot, Docker) | `spinbox-demo.cast` |
| `snapshot` | Snapshot demo (persist state) | `spinbox-snapshot-demo.cast` |

## What Gets Recorded

**Basic Demo (`demo`):**
- Pull spinbox sandbox image
- Boot VM with spinbox runtime
- Show systemd boot analysis
- Run Docker inside VM

**Snapshot Demo (`snapshot`):**
- Run VM and make changes (files, packages)
- Commit running VM to new image
- Run new VM from committed image
- Verify changes persisted

## Customization

Adjust timing in `.exp` files:
```expect
set TYPING_DELAY 0.04   # Character delay (lower = faster)
set CMD_DELAY 1         # Pause after commands
set LONG_DELAY 2        # Pause for long operations
```

## Upload

```bash
asciinema upload spinbox-demo.cast
```

## Troubleshooting

**Login credentials:** `root` / `spinbox`

**Test without recording:**
```bash
expect spinbox.exp
```

**Manual cleanup:**
```bash
./cleanup.sh demo-vm
./cleanup.sh snapshot-demo snapshot-new
```
