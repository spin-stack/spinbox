//go:build linux

// Package system provides system initialization for the VM guest environment.
package system

import (
	"context"
	"fmt"
	"os"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/log"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/spinbox/internal/guest/vminit/devices"
	"github.com/spin-stack/spinbox/internal/guest/vminit/extras"
)

// Filesystem types and mount options used across guest mount setup.
const (
	fsTypeTmpfs    = "tmpfs"
	mountOptNosuid = "nosuid"
	mountOptNoexec = "noexec"
	mountOptNodev  = "nodev"
)

// Initialize performs all system initialization tasks for the VM guest.
// This includes mounting filesystems, configuring cgroups, and setting up DNS.
//
// prof attributes each phase's wall-clock time to a VMINITD_PROFILE line; it is
// a no-op unless boot profiling is enabled, and may be nil.
func Initialize(ctx context.Context, prof *BootProfiler) error {
	if err := mountFilesystems(); err != nil {
		return err
	}
	prof.Mark(ctx, "mount-filesystems")

	if err := setupDevNodes(ctx); err != nil {
		return err
	}
	prof.Mark(ctx, "dev-nodes")

	// Configure CTRL+ALT+DELETE to send SIGINT to init instead of immediately rebooting
	// This allows vminitd to catch the signal and perform a clean shutdown
	// Default behavior (1) causes immediate kernel reboot without notifying init
	if err := os.WriteFile("/proc/sys/kernel/ctrl-alt-del", []byte("0"), 0644); err != nil {
		// In production, unexpected reboots could be a security concern
		// Log at error level but continue - the setting may not be available in all kernels
		log.G(ctx).WithError(err).Error("failed to configure ctrl-alt-del behavior - VM may reboot unexpectedly on CTRL+ALT+DEL")
	}

	// Wait for virtio block devices to appear
	// This is necessary because the kernel may not have probed all virtio devices yet
	// Not fatal if devices don't appear - they might appear later or not be needed
	devices.WaitForBlockDevices(ctx)
	prof.Mark(ctx, "wait-block-devices")

	// Extract files from extras disk if configured (spin.extras_disk kernel parameter)
	// Not fatal if extraction fails - extras may not be configured for this container
	if err := extras.Extract(ctx); err != nil {
		log.G(ctx).WithError(err).Warn("failed to extract extras disk, continuing anyway")
	}
	prof.Mark(ctx, "extras-extract")

	if err := setupCgroupControl(); err != nil {
		return err
	}
	prof.Mark(ctx, "cgroup-control")

	// #nosec G301 -- /etc must be world-readable inside the VM.
	if err := os.Mkdir("/etc", 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create /etc: %w", err)
	}

	// Configure networking from the kernel ip= parameter. We dropped
	// CONFIG_IP_PNP, so the interface is brought up here in userspace (via
	// netlink) instead of by the kernel's ip_auto_config; ip= is now purely a
	// config channel that we parse from /proc/cmdline. Each step is best-effort:
	// a workload without networking should still boot.
	if err := configureNetworking(ctx); err != nil {
		log.G(ctx).WithError(err).Warn("failed to read /proc/cmdline, skipping network configuration")
	}
	prof.Mark(ctx, "network")

	return nil
}

// configureNetworking reads the kernel ip= parameter and applies it: brings up
// the interface, assigns the address and default route, writes /etc/resolv.conf,
// and adds the metadata-service route. Individual steps log and continue on
// error so a workload that does not need networking still boots.
func configureNetworking(ctx context.Context) error {
	cmdlineBytes, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return fmt.Errorf("read /proc/cmdline: %w", err)
	}
	cmdline := string(cmdlineBytes)

	cfg, ok := parseIPConfig(cmdline)
	if !ok {
		log.G(ctx).Debug("no ip= parameter in kernel cmdline, skipping network configuration")
		return nil
	}

	if err := configureNetwork(ctx, cfg); err != nil {
		log.G(ctx).WithError(err).Warn("failed to configure network interface, continuing anyway")
	}
	if err := configureDNS(ctx, cfg); err != nil {
		log.G(ctx).WithError(err).Warn("failed to configure DNS, continuing anyway")
	}
	if err := configureMetadataRoute(ctx, cmdline, cfg); err != nil {
		log.G(ctx).WithError(err).Warn("failed to configure metadata route, continuing anyway")
	}
	return nil
}

// mountFilesystems mounts all required filesystems for the VM guest.
func mountFilesystems() error {
	// Create /lib if it doesn't exist (needed for modules)
	// #nosec G301 -- /lib must be world-readable inside the VM.
	if err := os.MkdirAll("/lib", 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create /lib: %w", err)
	}

	// Mount base filesystems first
	if err := mount.All([]mount.Mount{
		{
			Type:    "proc",
			Source:  "proc",
			Target:  "/proc",
			Options: []string{mountOptNosuid, mountOptNoexec, mountOptNodev},
		},
		{
			Type:    "sysfs",
			Source:  "sysfs",
			Target:  "/sys",
			Options: []string{mountOptNosuid, mountOptNoexec, mountOptNodev},
		},
		{
			Type:   "cgroup2",
			Source: "none",
			Target: "/sys/fs/cgroup",
		},
		{
			Type:    fsTypeTmpfs,
			Source:  fsTypeTmpfs,
			Target:  "/run",
			Options: []string{mountOptNosuid, mountOptNoexec, mountOptNodev},
		},
		{
			Type:    fsTypeTmpfs,
			Source:  fsTypeTmpfs,
			Target:  "/tmp",
			Options: []string{mountOptNosuid, mountOptNoexec, mountOptNodev},
		},
		{
			Type:    "devtmpfs",
			Source:  "devtmpfs",
			Target:  "/dev",
			Options: []string{mountOptNosuid, mountOptNoexec},
		},
	}, "/"); err != nil {
		return err
	}

	// Create /run/lock with sticky bit (replaces run-lock.mount)
	// #nosec G301 -- /run/lock needs sticky bit like /tmp for lock files.
	if err := os.MkdirAll("/run/lock", 0o1777); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create /run/lock: %w", err)
	}

	// Create /var/lib/spin-stack for supervisor binary (must be executable)
	// This is a separate tmpfs without noexec since /run has noexec
	// #nosec G301 -- /var/lib/spin-stack needs to be readable for supervisor execution.
	if err := os.MkdirAll("/var/lib/spin-stack", 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create /var/lib/spin-stack: %w", err)
	}
	if err := mount.All([]mount.Mount{
		{
			Type:    fsTypeTmpfs,
			Source:  fsTypeTmpfs,
			Target:  "/var/lib/spin-stack",
			Options: []string{mountOptNosuid, mountOptNodev, "size=32m"},
		},
	}, "/"); err != nil {
		return fmt.Errorf("failed to mount /var/lib/spin-stack: %w", err)
	}

	// Create /dev subdirectories after devtmpfs is mounted
	// #nosec G301 -- /dev/pts and /dev/shm must be accessible inside the VM.
	for _, dir := range []string{"/dev/pts", "/dev/shm"} {
		if err := os.MkdirAll(dir, 0755); err != nil && !os.IsExist(err) {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}

	// Mount /dev subdirectories
	return mount.All([]mount.Mount{
		{
			Type:    "devpts",
			Source:  "devpts",
			Target:  "/dev/pts",
			Options: []string{mountOptNosuid, mountOptNoexec, "gid=5", "mode=0620", "ptmxmode=0666"},
		},
		{
			Type:    fsTypeTmpfs,
			Source:  "shm",
			Target:  "/dev/shm",
			Options: []string{mountOptNosuid, mountOptNoexec, mountOptNodev, "mode=1777", "size=64m"},
		},
	}, "/")
}

// setupDevNodes creates device nodes and symlinks that may not be created by devtmpfs.
// This includes /dev/fuse for FUSE filesystems and standard symlinks like /dev/fd.
func setupDevNodes(ctx context.Context) error {
	// Create /dev/fuse if it doesn't exist (major 10, minor 229)
	// FUSE is built into the kernel but devtmpfs may not create the device node
	// until something tries to use it. Docker's fuse-overlayfs needs this.
	fusePath := "/dev/fuse"
	if _, err := os.Stat(fusePath); os.IsNotExist(err) {
		// #nosec G302 -- /dev/fuse must be world-readable for FUSE operations.
		if err := unix.Mknod(fusePath, unix.S_IFCHR|0666, int(unix.Mkdev(10, 229))); err != nil {
			log.G(ctx).WithError(err).Warn("failed to create /dev/fuse, FUSE filesystems may not work")
		} else {
			log.G(ctx).Info("created /dev/fuse device node")
		}
	}

	// Create standard /dev symlinks if they don't exist
	// These are typically created by udev but we don't run udev in the VM
	symlinks := map[string]string{
		"/dev/fd":     "/proc/self/fd",
		"/dev/stdin":  "/proc/self/fd/0",
		"/dev/stdout": "/proc/self/fd/1",
		"/dev/stderr": "/proc/self/fd/2",
	}

	for link, target := range symlinks {
		if _, err := os.Lstat(link); os.IsNotExist(err) {
			if err := os.Symlink(target, link); err != nil {
				log.G(ctx).WithError(err).WithField("link", link).Warn("failed to create symlink")
			}
		}
	}

	// Create /dev/ptmx symlink to /dev/pts/ptmx if it doesn't exist
	// This is needed for pseudo-terminal allocation with devpts
	ptmxPath := "/dev/ptmx"
	if _, err := os.Lstat(ptmxPath); os.IsNotExist(err) {
		if err := os.Symlink("/dev/pts/ptmx", ptmxPath); err != nil {
			log.G(ctx).WithError(err).Warn("failed to create /dev/ptmx symlink")
		}
	}

	return nil
}

// setupCgroupControl enables cgroup controllers for container resource management.
func setupCgroupControl() error {
	// #nosec G306 -- kernel-managed cgroup control file expects 0644.
	return os.WriteFile("/sys/fs/cgroup/cgroup.subtree_control", []byte("+cpu +cpuset +io +memory +pids"), 0644)
}
