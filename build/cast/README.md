# QemuBox Demo - Asciinema Recording

Automated recording of your qemubox demonstration using asciinema and expect.

## 🚀 Quick Start

```bash
# 1. Install dependencies (Ubuntu/Debian)
sudo apt-get install asciinema expect

# 2. Make scripts executable
chmod +x *.sh *.exp

# 3. Record the demo
./record.sh

# 4. Play it back
asciinema play qemubox-demo.cast
```

## 📁 Files

- **`qemubox.exp`** - Expect script for basic demo (boot, docker)
- **`record.sh`** - Wrapper to record basic demo
- **`snapshot.exp`** - Expect script for snapshot demo (persistent state)
- **`record-snapshot.sh`** - Wrapper to record snapshot demo

## 🎬 Recording

### Basic Recording
```bash
./record.sh
```
This creates `qemubox-demo.cast`

### Snapshot Recording
```bash
./record-snapshot.sh
```
This creates `qemubox-snapshot-demo.cast`

### Custom Filename
```bash
./record.sh my-custom-name
```
This creates `my-custom-name.cast`

### What Gets Recorded

**Basic Demo (`record.sh`):**
1. Pulling the qemubox image
2. Running a container with qemubox runtime
3. Inside container:
   - systemd analysis
   - Docker operations
   - Pulling and running Alpine

**Snapshot Demo (`record-snapshot.sh`):**
1. Running a VM and making changes (files, packages)
2. Shutting down and creating a snapshot
3. Running a new VM from the snapshot
4. Verifying all changes persisted

## 📤 Sharing

### Upload to asciinema.org
```bash
asciinema upload qemubox-demo.cast
```
You'll get a shareable URL like: https://asciinema.org/a/abc123

## 🎨 Customization

### Adjust Typing Speed
Edit `qemubox.exp`:
```expect
set TYPING_DELAY 0.03    # Lower = faster, higher = slower
set CMD_DELAY 2          # Delay after commands
set LONG_DELAY 3         # Delay for long operations
```

### Modify Commands
Edit the commands in `qemubox.exp`:
```expect
show_comment "Your custom comment here"
type_cmd "your-command-here"
sleep 2
```

### Change Asciinema Settings
Record with custom settings:
```bash
# Idle time limit (skip long pauses)
asciinema rec --idle-time-limit 2 demo.cast -c "expect qemubox.exp"

# Custom title
asciinema rec --title "My QemuBox Demo" demo.cast -c "expect qemubox.exp"

# Append to existing recording
asciinema rec --append demo.cast -c "expect qemubox.exp"
```

## 🔧 Advanced Usage

### Record with Custom Terminal Size
```bash
asciinema rec --cols 120 --rows 30 demo.cast -c "expect qemubox.exp"
```

### Embed in README or Website
After uploading to asciinema.org:
```markdown
[![Demo](https://asciinema.org/a/YOUR_ID.svg)](https://asciinema.org/a/YOUR_ID)
```

## 🐛 Troubleshooting

### "expect: command not found"
```bash
sudo apt-get install expect
```

### "asciinema: command not found"
```bash
# Ubuntu/Debian
sudo apt-get install asciinema
```

### Recording is too fast/slow
Adjust delays in `qemubox.exp`:
```expect
set TYPING_DELAY 0.05    # Increase for slower typing
set CMD_DELAY 3          # Increase for more pause time
```

### Container login fails
The script expects:
- Username: `root`
- Password: `qemubox`

Modify in `qemubox.exp` if different.

## 💡 Tips

1. **Test first** - Run `expect qemubox.exp` directly to test without recording
2. **Clean terminal** - The script starts with `clear` for a clean recording
3. **Consistent timing** - Keep delays consistent for professional look
4. **Short is better** - Recordings under 5 minutes are more shareable
5. **Add context** - Use comments to explain what's happening

## 🎯 Useful Commands

```bash
# Quick record
asciinema rec demo.cast -c "expect qemubox.exp"

# Record and upload
./record.sh && asciinema upload qemubox-demo.cast

# Test without recording
expect qemubox.exp

# Record with idle time limit (skip long waits)
asciinema rec --idle-time-limit 2 demo.cast -c "expect qemubox.exp"

# Play with speed control
asciinema play -s 2 qemubox-demo.cast  # 2x speed
```

## 📚 Resources

- [Asciinema documentation](https://asciinema.org/docs)
- [Expect tutorial](https://www.tcl.tk/man/expect5.31/expect.1.html)
