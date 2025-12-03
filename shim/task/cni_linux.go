//go:build linux

package task

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

// createNetNS creates a new network namespace at the given path
// This is required before CNI can set up networking
func createNetNS(path string) error {
	// Lock the OS thread to ensure network namespace operations happen on the same thread
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Create a new network namespace using unshare and bind mount
	// We need to:
	// 1. Create a new netns
	// 2. Bind mount it to the persistent path
	// 3. Return to the original netns

	// Save the current network namespace
	origns, err := netns.Get()
	if err != nil {
		return fmt.Errorf("failed to get current netns: %w", err)
	}
	defer origns.Close()

	// Create a new network namespace
	newns, err := netns.New()
	if err != nil {
		return fmt.Errorf("failed to create new netns: %w", err)
	}
	defer newns.Close()

	// Return to the original namespace immediately
	// This is important so CNI doesn't run in the new netns
	if err := netns.Set(origns); err != nil {
		return fmt.Errorf("failed to return to original netns: %w", err)
	}

	// Ensure the parent directory exists
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create netns directory: %w", err)
	}

	// Create the bind mount target file
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create netns file: %w", err)
	}
	f.Close()

	// Get the path to the new netns in /proc
	newnsPath := fmt.Sprintf("/proc/self/fd/%d", newns)

	// Bind mount the new netns to the persistent path
	if err := unix.Mount(newnsPath, path, "none", unix.MS_BIND, ""); err != nil {
		os.Remove(path)
		return fmt.Errorf("failed to bind mount netns: %w", err)
	}

	return nil
}
