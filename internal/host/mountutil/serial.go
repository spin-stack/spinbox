//go:build linux

package mountutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BlockSerialScheme is the mount Source prefix used by the spinbox shim to
// reference a virtio-blk device by its QEMU serial instead of a kernel-assigned
// /dev/vdX name. The guest resolves it to the real device by matching
// /sys/block/<dev>/serial.
//
// This makes the layer→device mapping robust against PCI enumeration reordering
// (the order in which the guest probes virtio-blk-pci devices is not guaranteed
// to match the order the host added them). Resolution is a cheap sysfs read at
// mount time, outside the kernel boot path, so it adds no boot latency.
const BlockSerialScheme = "serial:"

// blockSerialResolveTimeout bounds how long we wait for a serial-identified
// block device to appear. vminitd already waits for virtio block devices at
// boot, so the device is normally present; this is only a small grace window.
const blockSerialResolveTimeout = 5 * time.Second

// sysBlockPath is the sysfs block directory; overridable in tests.
var sysBlockPath = "/sys/block"

// resolveBlockSerialSource maps a "serial:<id>" source to its /dev/<dev> path by
// matching /sys/block/<dev>/serial. Sources without the scheme are returned
// unchanged with ok=false, so this is a no-op on the host fallback path where
// mount sources are real filesystem paths.
func resolveBlockSerialSource(ctx context.Context, source string) (resolved string, ok bool, err error) {
	serial, hasScheme := strings.CutPrefix(source, BlockSerialScheme)
	if !hasScheme {
		return source, false, nil
	}

	deadline := time.Now().Add(blockSerialResolveTimeout)
	for {
		if dev, findErr := findBlockDeviceBySerial(serial); findErr == nil {
			return dev, true, nil
		}
		if time.Now().After(deadline) {
			return "", true, fmt.Errorf("resolve block device by serial %q: not found", serial)
		}
		select {
		case <-ctx.Done():
			return "", true, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// findBlockDeviceBySerial scans /sys/block for a device whose serial attribute
// matches the requested serial and returns its /dev path.
func findBlockDeviceBySerial(serial string) (string, error) {
	entries, err := os.ReadDir(sysBlockPath)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		name := e.Name()
		data, readErr := os.ReadFile(filepath.Join(sysBlockPath, name, "serial"))
		if readErr != nil {
			continue
		}
		if strings.TrimSpace(string(data)) == serial {
			return filepath.Join("/dev", name), nil
		}
	}
	return "", fmt.Errorf("no block device with serial %q", serial)
}
