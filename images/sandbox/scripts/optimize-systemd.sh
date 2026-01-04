#!/usr/bin/env bash
# =============================================================================
#   VM-optimized systemd tuning script
#
#   Works during Docker build (no running systemd required).
#   Uses direct symlinks instead of systemctl commands.
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

log()   { printf '[%s] %s\n' "$(date +%T)" "$*"; }

# Directory for unit masks
MASK_DIR="/etc/systemd/system"
mkdir -p "$MASK_DIR"

# Function to mask a unit by creating symlink to /dev/null
mask_unit() {
    local unit=$1
    log "Masking $unit"
    ln -sf /dev/null "$MASK_DIR/$unit"
}

# Units to mask - safe for VM environment
MASK_UNITS=(
    # udev - not needed for static VM hardware
    systemd-udev-trigger.service
    systemd-udevd.service
    systemd-udevd-control.socket
    systemd-udevd-kernel.socket

    # Time sync - handled by host
    systemd-timesyncd.service
    systemd-timedated.service

    # Journal maintenance
    systemd-journal-flush.service
    systemd-journal-catalog-update.service

    # Filesystem checks - not needed
    systemd-fsck-root.service
    systemd-update-done.service

    # Hardware-specific services
    systemd-modules-load.service
    systemd-hibernate-clear.service
    systemd-pcrmachine.service
    systemd-pstore.service
    systemd-hwdb-update.service
    systemd-tpm2-setup.service
    systemd-tpm2-setup-early.service

    # First-boot setup (done at build time)
    systemd-firstboot.service
    systemd-machine-id-commit.service

    # Random seed (VM gets entropy from virtio-rng)
    systemd-random-seed.service

    # Boot token
    systemd-boot-system-token.service

    # Desktop services
    avahi-daemon.service
    cups.service
    bluetooth.service

    # Storage services
    smartd.service
    e2scrub_reap.service
    multipathd.service
    multipathd.socket
    lvm2-monitor.service

    # Timers
    motd-news.timer
    apt-daily-upgrade.timer
    apt-daily.timer
    dpkg-db-backup.timer
    e2scrub_all.timer

    # Extra getty instances (keep tty1 only)
    getty@tty2.service
    getty@tty3.service
    getty@tty4.service
    getty@tty5.service
    getty@tty6.service

    # === Services from systemd-analyze blame ===
    # ldconfig - already pre-warmed at build time (99ms)
    ldconfig.service

    # sysctl/sysusers - done at build time (25ms + 20ms)
    systemd-sysctl.service
    systemd-sysusers.service

    # Modprobe services - not needed in VM (35ms + 31ms + 24ms)
    modprobe@configfs.service
    modprobe@drm.service
    modprobe@fuse.service
    modprobe@efi_pstore.service

    # Debug/tracing mounts - not needed (32ms + 26ms)
    sys-kernel-debug.mount
    sys-kernel-tracing.mount

    # tmp.mount - already have /tmp (24ms)
    tmp.mount

    # tmpfiles-setup - vminitd already creates /tmp, /run, /dev nodes (19ms + 13ms + 11ms)
    systemd-tmpfiles-setup.service
    systemd-tmpfiles-setup-dev.service
    systemd-tmpfiles-setup-dev-early.service

    # Mounts already handled by vminitd or not needed (15ms + 15ms + 12ms)
    dev-hugepages.mount
    dev-mqueue.mount
    run-lock.mount
)

log "Masking unnecessary systemd units..."
for unit in "${MASK_UNITS[@]}"; do
    mask_unit "$unit"
done

# =============================================================================
# DISABLE GENERATORS - Create symlinks to /dev/null
# =============================================================================
log "Disabling unnecessary systemd generators..."
mkdir -p /etc/systemd/system-generators

DISABLE_GENERATORS=(
    systemd-fstab-generator
    systemd-debug-generator
    systemd-gpt-auto-generator
    systemd-sysv-generator
    systemd-rc-local-generator
    systemd-hibernate-resume-generator
    systemd-system-update-generator
)

for gen in "${DISABLE_GENERATORS[@]}"; do
    ln -sf /dev/null "/etc/systemd/system-generators/$gen"
    log "Disabled generator: $gen"
done

log "Masking unnecessary tmpfiles rules..."
mkdir -p /etc/tmpfiles.d

MASK_TMPFILES=(
    systemd-pstore.conf
    systemd-network.conf
    x11.conf
)

for conf in "${MASK_TMPFILES[@]}"; do
    ln -sf /dev/null "/etc/tmpfiles.d/$conf"
done

log "Configuring systemd manager for quiet, fast boot..."
mkdir -p /etc/systemd/system.conf.d

cat <<'EOF' >/etc/systemd/system.conf.d/fast-boot.conf
[Manager]
# Reduce default timeouts
DefaultTimeoutStartSec=15s
DefaultTimeoutStopSec=10s
DefaultDeviceTimeoutSec=10s

# Disable watchdogs
RuntimeWatchdogSec=0
ShutdownWatchdogSec=0

# Reduce logging - only errors
LogLevel=err
LogTarget=journal

# Show less during boot
ShowStatus=no
StatusUnitFormat=name
EOF

log "Configuring journald for minimal overhead..."
mkdir -p /etc/systemd/journald.conf.d

cat <<'EOF' >/etc/systemd/journald.conf.d/volatile.conf
[Journal]
# Use memory-only storage - no disk I/O
Storage=volatile
RuntimeMaxUse=4M
RuntimeMaxFileSize=1M

# Disable compression for speed
Compress=no

# Disable sealing (cryptographic protection)
Seal=no

# Minimal sync - we don't care about journal persistence
SyncIntervalSec=0

# High rate limits to avoid blocking
RateLimitBurst=10000
RateLimitIntervalSec=30s

# Disable forwarding
ForwardToConsole=no
ForwardToWall=no
ForwardToSyslog=no
ForwardToKMsg=no

# Don't split by user
SplitMode=none

# Minimal audit
Audit=no

# Line max to avoid large allocations
LineMax=48K
EOF

log "Configuring quiet console..."

# Configure kernel console to be quiet
mkdir -p /etc/sysctl.d
cat <<'EOF' >/etc/sysctl.d/99-quiet-boot.conf
# Reduce kernel console verbosity
kernel.printk = 3 3 3 3
EOF

log "Configuring Docker and containerd for socket activation..."

# Remove auto-start symlinks
rm -f /etc/systemd/system/multi-user.target.wants/docker.service 2>/dev/null || true
rm -f /etc/systemd/system/multi-user.target.wants/containerd.service 2>/dev/null || true

# Enable sockets (create wants symlinks)
mkdir -p /etc/systemd/system/sockets.target.wants
ln -sf /etc/systemd/system/containerd.socket /etc/systemd/system/sockets.target.wants/containerd.socket
ln -sf /etc/systemd/system/docker.socket /etc/systemd/system/sockets.target.wants/docker.socket

log "Configuring SSH for socket activation..."
rm -f /etc/systemd/system/multi-user.target.wants/ssh.service 2>/dev/null || true
mkdir -p /etc/systemd/system/sockets.target.wants
ln -sf /lib/systemd/system/ssh.socket /etc/systemd/system/sockets.target.wants/ssh.socket

log "Setting default target to multi-user..."
ln -sf /lib/systemd/system/multi-user.target /etc/systemd/system/default.target

log "Creating kernel module blacklist..."
cat <<'EOF' >/etc/modprobe.d/blacklist-vm.conf
# Hardware not present in VM
blacklist floppy
blacklist pcspkr
blacklist cdrom
blacklist sr_mod
blacklist parport
blacklist parport_pc
blacklist bluetooth
blacklist btusb
blacklist iwlwifi
blacklist cfg80211
EOF

log "Optimizing nsswitch.conf..."
cat <<'EOF' >/etc/nsswitch.conf
passwd:         files
group:          files
shadow:         files
gshadow:        files
hosts:          files dns
networks:       files
protocols:      files
services:       files
ethers:         files
rpc:            files
EOF

log "Pre-warming caches..."
ldconfig || true
locale-gen en_US.UTF-8 2>/dev/null || true
update-locale LANG=en_US.UTF-8 2>/dev/null || true
iconvconfig 2>/dev/null || true
fc-cache -f 2>/dev/null || true

log "Running cleanup..."
rm -rf /usr/share/doc/* 2>/dev/null || true
rm -rf /usr/share/man/* 2>/dev/null || true
rm -rf /var/lib/apt/lists/* 2>/dev/null || true
rm -rf /var/log/journal/* 2>/dev/null || true

log "Boot optimization complete!"
