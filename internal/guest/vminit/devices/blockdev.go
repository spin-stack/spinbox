//go:build linux

// Package devices provides device detection and management for the VM guest.
package devices

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
	"unsafe"

	"github.com/containerd/log"
	"golang.org/x/sys/unix"
)

const (
	// BlockDeviceTimeout is how long to wait for virtio block devices.
	// 5 seconds is sufficient for QEMU virtio device initialization.
	BlockDeviceTimeout = 5 * time.Second
)

// findVirtioBlockDevices finds virtio block devices in /sys/block.
func findVirtioBlockDevices() ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, err
	}

	var devices []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "vd") {
			devices = append(devices, entry.Name())
		}
	}
	return devices, nil
}

// waitForDevNodesInotify waits for device nodes to appear in /dev using inotify.
// This is more efficient than polling - the kernel notifies us when devices are created.
func waitForDevNodesInotify(ctx context.Context, devices []string, timeout time.Duration) bool {
	if len(devices) == 0 {
		return true
	}

	// Build set of devices we're waiting for
	waiting := make(map[string]bool)
	for _, dev := range devices {
		devPath := "/dev/" + dev
		// Check if already exists
		if _, err := os.Stat(devPath); err == nil {
			continue
		}
		waiting[dev] = true
	}

	if len(waiting) == 0 {
		// All devices already exist
		return true
	}

	// Create inotify instance
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		log.G(ctx).WithError(err).Debug("inotify init failed, falling back to polling")
		return waitForDevNodesPoll(ctx, devices, timeout)
	}
	defer unix.Close(fd)

	// Watch /dev for CREATE events
	_, err = unix.InotifyAddWatch(fd, "/dev", unix.IN_CREATE)
	if err != nil {
		log.G(ctx).WithError(err).Debug("inotify watch failed, falling back to polling")
		return waitForDevNodesPoll(ctx, devices, timeout)
	}

	// Check again after setting up watch (race condition window)
	for dev := range waiting {
		if _, err := os.Stat("/dev/" + dev); err == nil {
			delete(waiting, dev)
		}
	}
	if len(waiting) == 0 {
		return true
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 4096)

	for time.Now().Before(deadline) && len(waiting) > 0 {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}

		// Use poll to wait for events with timeout
		pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		pollTimeout := int(remaining.Milliseconds())
		if pollTimeout > 100 {
			pollTimeout = 100 // Check context every 100ms
		}

		n, err := unix.Poll(pollFds, pollTimeout)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			log.G(ctx).WithError(err).Debug("poll failed")
			return false
		}

		if n == 0 {
			continue
		}

		// Read inotify events
		nread, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			log.G(ctx).WithError(err).Debug("read inotify failed")
			return false
		}

		// Parse events
		for offset := 0; offset < nread; {
			// #nosec G103 -- unsafe.Pointer is required to parse inotify events from the kernel
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(event.Len)
			if nameLen > 0 {
				name := string(buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+nameLen])
				name = strings.TrimRight(name, "\x00")
				if waiting[name] {
					delete(waiting, name)
					log.G(ctx).WithField("device", name).Debug("device node created")
				}
			}
			offset += unix.SizeofInotifyEvent + nameLen
		}
	}

	return len(waiting) == 0
}

// waitForDevNodesPoll is a fallback polling implementation when inotify is unavailable.
func waitForDevNodesPoll(ctx context.Context, devices []string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	backoff := 5 * time.Millisecond
	maxBackoff := 50 * time.Millisecond

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		default:
		}

		allExist := true
		for _, dev := range devices {
			if _, err := os.Stat("/dev/" + dev); err != nil {
				allExist = false
				break
			}
		}

		if allExist {
			return true
		}

		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)
	}

	return false
}

// WaitForBlockDevices waits for virtio block devices to appear in /dev.
// Uses inotify for efficient event-driven waiting instead of polling.
// The kernel needs time to probe PCI devices and create device nodes.
// This is a best-effort operation - if devices don't appear, we continue anyway.
func WaitForBlockDevices(ctx context.Context) {
	log.G(ctx).Debug("waiting for virtio block devices to appear")

	ctx, cancel := context.WithTimeout(ctx, BlockDeviceTimeout)
	defer cancel()

	// First, wait for devices to appear in /sys/block using inotify
	vdDevices := waitForSysBlockDevices(ctx)
	if len(vdDevices) == 0 {
		log.G(ctx).Warn("no virtio block devices found in /sys/block")
		return
	}

	log.G(ctx).WithField("devices", vdDevices).Info("found virtio block devices in /sys/block")

	// Now wait for /dev nodes to appear
	if waitForDevNodesInotify(ctx, vdDevices, BlockDeviceTimeout) {
		var devNodes []string
		for _, dev := range vdDevices {
			devNodes = append(devNodes, "/dev/"+dev)
		}
		log.G(ctx).WithField("dev_nodes", devNodes).Info("virtio block device nodes ready")
	} else {
		log.G(ctx).Warn("timeout waiting for some device nodes, continuing anyway")
	}
}

// waitForSysBlockDevices waits for virtio block devices to appear in /sys/block using inotify.
func waitForSysBlockDevices(ctx context.Context) []string {
	// Fast path: check if devices already exist
	devices, err := findVirtioBlockDevices()
	if err == nil && len(devices) > 0 {
		return devices
	}

	// Create inotify instance to watch /sys/block
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		// Fall back to simple polling
		return waitForSysBlockDevicesPoll(ctx)
	}
	defer unix.Close(fd)

	_, err = unix.InotifyAddWatch(fd, "/sys/block", unix.IN_CREATE)
	if err != nil {
		return waitForSysBlockDevicesPoll(ctx)
	}

	// Check again after setting up watch
	devices, err = findVirtioBlockDevices()
	if err == nil && len(devices) > 0 {
		return devices
	}

	buf := make([]byte, 4096)

	for {
		select {
		case <-ctx.Done():
			// Return whatever we have
			devices, _ = findVirtioBlockDevices()
			return devices
		default:
		}

		// Use poll with timeout
		pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pollFds, 100) // 100ms poll
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			break
		}

		if n == 0 {
			// Timeout, check if any devices appeared
			devices, err = findVirtioBlockDevices()
			if err == nil && len(devices) > 0 {
				return devices
			}
			continue
		}

		// Read events
		nread, err := unix.Read(fd, buf)
		if err != nil {
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
				continue
			}
			break
		}

		// Parse events looking for vd* devices
		for offset := 0; offset < nread; {
			// #nosec G103 -- unsafe.Pointer is required to parse inotify events from the kernel
			event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
			nameLen := int(event.Len)
			if nameLen > 0 {
				name := string(buf[offset+unix.SizeofInotifyEvent : offset+unix.SizeofInotifyEvent+nameLen])
				name = strings.TrimRight(name, "\x00")
				if strings.HasPrefix(name, "vd") {
					// Found a virtio device, now get all of them
					devices, _ = findVirtioBlockDevices()
					return devices
				}
			}
			offset += unix.SizeofInotifyEvent + nameLen
		}
	}

	devices, _ = findVirtioBlockDevices()
	return devices
}

// waitForSysBlockDevicesPoll is a fallback polling implementation.
func waitForSysBlockDevicesPoll(ctx context.Context) []string {
	backoff := 5 * time.Millisecond
	maxBackoff := 50 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			devices, _ := findVirtioBlockDevices()
			return devices
		default:
		}

		devices, err := findVirtioBlockDevices()
		if err == nil && len(devices) > 0 {
			return devices
		}

		time.Sleep(backoff)
		backoff = min(backoff*2, maxBackoff)
	}
}
