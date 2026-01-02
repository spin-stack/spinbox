#!/usr/bin/env bash
# =============================================================================
#   VM‑optimized systemd tuning script
#
#   • Masks only the units that are *usually* super‑fluous inside a VM.
#   • Keeps everything required for proper boot (udev, journal sockets, DBus…).
#   • Switches default target to multi‑user.target.
#   • Configures journald for a tiny volatile store (RAM only).
#   • Optionally disables Docker/containerd if you never run containers.
# =============================================================================
set -euo pipefail
IFS=$'\n\t'

log()   { printf '[%s] %s\n' "$(date +%T)" "$*"; }
log_err() { printf '[%s][ERROR] %s\n' "$(date +%T)" "$*" >&2; }

die() { log_err "$*"; exit 1; }

# Units that are *normally* unnecessary inside a VM.
# Keep the list short – only those that have no observable impact on a
# typical headless, non‑hotplug VM.
MASK_UNITS=(
    # tmp.mount - Container already has /tmp, and lacks CAP_SYS_ADMIN to remount
    tmp.mount

    # udev‐related – they are only needed for hot‑plug hardware.
    systemd-udev-trigger.service
    # The daemon that actually talks to the kernel udev netlink socket.
    systemd-udevd.service
    # Control sockets used only for the "settle" wait; they disappear once all udev
    # events are processed.  In a static VM there is nothing to wait for.
    systemd-udevd-control.socket
    systemd-udevd-kernel.socket

    # Time‑sync / time‑zone helpers – usually handled by the host or a
    # user‑space NTP client.
    systemd-timesyncd.service
    systemd-timedated.service

    # Journald “maintenance” units that only run once a day on a normal host.
    systemd-journal-flush.service
    systemd-journal-catalog-update.service
    systemd-fsck-root.service          # never needed on read‑only rootfs VMs
    systemd-update-done.service         # placeholder after updates

    # Misc “support” units that only make sense on a desktop / server with
    # persistent storage, hardware crypto devices, etc.
    systemd-modules-load.service
    systemd-hibernate-clear.service
    systemd-pcrmachine.service
    systemd-pstore.service
    systemd-binfmt_misc.automount      # not installed on minimal images
    systemd-hwdb-update.service         # updates hwdb; no effect on VMs
    systemd-tpm2-setup.service          # TPM passthrough only needed on real HW
    systemd-tpm2-setup-early.service

    # Desktop‑oriented daemons that make no sense in a headless VM.
    avahi-daemon.service
    cups.service
    bluetooth.service

    # Misc daemons you probably do not need in a containerised / script‑driven VM.
    rsyslog.service
    smartd.service                     # SMART disk monitor – only for physical disks
    irqbalance.service                 # distributes IRQs on SMP hosts; not needed in VM
    tcsd.service                       # smartcard daemon – irrelevant here

    # Ext4 scrubbing & RAID‑reconstruct services (only on bare‑metal)
    e2scrub_reap.service
    multipathd.service
    multipathd.socket
    lvm2-monitor.service
    lvm2-lvmetad.service
)

# Timers that fire early in the boot sequence and waste a few milliseconds.
MASK_TIMERS=(
    motd-news.timer
    apt-daily-upgrade.timer
    apt-daily.timer
    dpkg-db-backup.timer
    e2scrub_all.timer
)

# Mount units that are only created for “special” filesystems you probably never use.
MASK_MOUNTS=(
    sys-kernel-debug.mount
    sys-kernel-tracing.mount
    proc-sys-fs-binfmt_misc.automount
)

# tmpfiles.d entries to mask by overriding with /dev/null in /etc/tmpfiles.d
MASK_TMPFILES=(
    20-systemd-shell-extra.conf
    systemd-pstore.conf
    systemd-network.conf
    openssh-client.conf
    home.conf
    provision.conf
    x11.conf
)

# Modprobe “@‑style” sockets that are auto‑generated for optional kernel modules.
MASK_MODPROBE=(
    modprobe@drm.service
    modprobe@efi_pstore.service
    # Loaded on demand; skip boot-time probing to save time.
    modprobe@fuse.service
    modprobe@configfs.service
)

mask_unit() {
    local unit=$1
    if systemctl is-enabled "$unit" >/dev/null 2>&1; then
        log "Masking $unit"
        systemctl mask "$unit" || true   # ignore “already masked”
    fi
}
mask_timer() {
    local timer=$1
    if systemctl is-enabled "$timer" >/dev/null 2>&1; then
        log "Masking timer $timer"
        systemctl mask "$timer" || true
    fi
}
mask_mount() {
    local mount=$1
    if systemctl is-enabled "$mount" >/dev/null 2>&1; then
        log "Masking mount unit $mount"
        systemctl mask "$mount" || true
    fi
}
mask_modprobe() {
    local service=$1
    if systemctl is-enabled "$service" >/dev/null 2>&1; then
        log "Masking modprobe service $service"
        systemctl mask "$service" || true
    fi
}
mask_tmpfiles() {
    local conf=$1
    local target="/etc/tmpfiles.d/$conf"
    log "Masking tmpfiles rule $conf"
    mkdir -p /etc/tmpfiles.d
    ln -sf /dev/null "$target"
}

log "Masking unnecessary systemd units, timers and mount points …"
for u in "${MASK_UNITS[@]}";   do mask_unit "$u"; done
for t in "${MASK_TIMERS[@]}";  do mask_timer "$t"; done
for m in "${MASK_MOUNTS[@]}";  do mask_mount "$m"; done
for ms in "${MASK_MODPROBE[@]}"; do mask_modprobe "$ms"; done
for tf in "${MASK_TMPFILES[@]}"; do mask_tmpfiles "$tf"; done

# Docker & containerd: use socket activation for faster boot
log "Configuring Docker and containerd for socket activation…"
systemctl disable docker.service   || true
systemctl disable containerd.service || true
rm -f /etc/systemd/system/multi-user.target.wants/docker.service
rm -f /etc/systemd/system/sockets.target.wants/docker.service

# Enable socket activation (services start on first connection)
systemctl enable containerd.socket || true
systemctl enable docker.socket || true

log "Enabling required services …"

# 1️⃣ SSH – most management scripts expect a reachable sshd.
log "Enabling OpenSSH server"
systemctl enable ssh.socket || true
systemctl disable ssh.service || true

# 2️⃣ qemu‑guest‑agent – lets libvirt / OpenStack talk to the guest.
log "Enabling QEMU Guest Agent"
systemctl enable qemu-guest-agent.service || true

# 3️⃣ Set the default target to *multi‑user* (text console only).
log "Setting default boot target to multi-user.target"
systemctl set-default multi-user.target || true

log "Configuring journald to volatile storage (RAM‑only)…"
JOURNAL_CONF_DIR="/etc/systemd/journald.conf.d"

mkdir -p "$JOURNAL_CONF_DIR"

cat <<'EOF' >"$JOURNAL_CONF_DIR/volatile.conf"
[Journal]
Storage=volatile
RuntimeMaxUse=8M
SyncIntervalSec=0
RateLimitBurst=10000
RateLimitIntervalSec=30s
ForwardToConsole=no
ForwardToWall=no
EOF

# Pre-generate ld.so.cache and skip ldconfig at boot.
log "Pre-generating ld.so.cache and masking ldconfig.service"
ldconfig || true
systemctl mask ldconfig.service || true

log "Configuring systemd manager for fast boot…"
mkdir -p /etc/systemd/system.conf.d
cat <<'EOF' >/etc/systemd/system.conf.d/fast-boot.conf
[Manager]
# Reduce default timeouts significantly for VM environment
DefaultTimeoutStartSec=10s
DefaultTimeoutStopSec=5s
DefaultDeviceTimeoutSec=5s
DefaultStartLimitIntervalSec=10s
DefaultStartLimitBurst=3
# Disable watchdogs (not needed in VM)
RuntimeWatchdogSec=0
ShutdownWatchdogSec=0
# Increase file descriptor limit
DefaultLimitNOFILE=65536
# Reduce logging verbosity
LogLevel=warning
LogTarget=journal
# Limit concurrent jobs for predictable boot
DefaultTasksMax=512
EOF

# =============================================================================
# ADDITIONAL UNITS TO MASK - User sessions, first-boot, random seed
# =============================================================================
log "Masking additional unnecessary units for VM boot…"
ADDITIONAL_MASK_UNITS=(
    # User session management (not needed for container workloads)
    systemd-user-sessions.service
    systemd-logind.service
    user@.service
    user-runtime-dir@.service

    # First-boot & machine-id setup (done at build time)
    systemd-firstboot.service
    systemd-machine-id-commit.service

    # Random seed (VM gets entropy from host via virtio-rng)
    systemd-random-seed.service

    # Sysctl/sysusers (do it at build time instead)
    systemd-sysctl.service
    systemd-sysusers.service

    # Boot complete notification (not needed)
    systemd-boot-system-token.service

    # Additional tmpfiles processing
    systemd-tmpfiles-setup-dev.service

    # Hostname setup (kernel param or static)
    systemd-hostnamed.service
)

for u in "${ADDITIONAL_MASK_UNITS[@]}"; do
    log "Masking additional unit: $u"
    systemctl mask "$u" 2>/dev/null || true
done

log "Disabling unnecessary systemd generators…"
mkdir -p /etc/systemd/system-generators
DISABLE_GENERATORS=(
    systemd-fstab-generator
    systemd-getty-generator
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

log "Creating kernel module blacklist for VM…"
cat <<'EOF' >/etc/modprobe.d/blacklist-boot.conf
# Hardware not present in VM
blacklist floppy
blacklist pcspkr
blacklist snd_pcsp
blacklist cdrom
blacklist sr_mod
blacklist i2c_piix4
blacklist parport
blacklist parport_pc
blacklist lp
blacklist ppdev

# Bluetooth (not needed in VM)
blacklist bluetooth
blacklist btusb
blacklist btrtl
blacklist btbcm
blacklist btintel

# Wireless (not needed in VM)
blacklist iwlwifi
blacklist iwlmvm
blacklist cfg80211

# Firewire (legacy, not in VM)
blacklist firewire_ohci
blacklist firewire_core
EOF

log "Disabling unnecessary getty instances…"
# Keep only tty1, mask the rest
for tty in tty2 tty3 tty4 tty5 tty6; do
    systemctl mask "getty@${tty}.service" 2>/dev/null || true
done
rm -f /etc/systemd/system/getty.target.wants/getty@tty[2-6].service 2>/dev/null || true
log "Pre-warming caches at build time…"

# Pre-generate locale archive
locale-gen en_US.UTF-8 2>/dev/null || true
update-locale LANG=en_US.UTF-8 LC_ALL=en_US.UTF-8 2>/dev/null || true

# Pre-generate gconv modules cache
iconvconfig 2>/dev/null || true

# Update font cache
fc-cache -f 2>/dev/null || true

# Pre-compile Python bytecode if Python is installed
if command -v python3 &>/dev/null; then
    log "Pre-compiling Python bytecode…"
    python3 -m compileall -q /usr/lib/python3* 2>/dev/null || true
fi

log "Optimizing nsswitch.conf for fast lookups…"
cat <<'EOF' >/etc/nsswitch.conf
# Optimized for VM - minimal lookups
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

log "Optimizing PAM configuration…"
# Disable pam_systemd for faster session setup (not needed in container workloads)
if [ -f /etc/pam.d/common-session ]; then
    sed -i '/pam_systemd.so/s/^/# /' /etc/pam.d/common-session || true
fi

# Disable pam_motd in login (already disabled in sshd)
if [ -f /etc/pam.d/login ]; then
    sed -i '/pam_motd.so/s/^/# /' /etc/pam.d/login || true
fi

log "Running final cleanup…"

# Remove unnecessary documentation
rm -rf /usr/share/doc/* 2>/dev/null || true
rm -rf /usr/share/man/* 2>/dev/null || true
rm -rf /usr/share/info/* 2>/dev/null || true

# Remove apt cache
rm -rf /var/lib/apt/lists/* 2>/dev/null || true

# Clear systemd journal
rm -rf /var/log/journal/* 2>/dev/null || true

log "Boot optimization complete!"
