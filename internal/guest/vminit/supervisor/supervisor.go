//go:build linux

// Package supervisor provides supervisor agent lifecycle management in the VM guest.
// The supervisor binary fetches its own configuration from the runner's metadata
// service via vsock - vminitd only handles starting and monitoring the binary.
package supervisor

import (
	"context"
	"os"
	"path/filepath"

	"github.com/containerd/log"
)

// Default values
const (
	// BundleBasePath is where bundle files are placed by the bundle service.
	BundleBasePath = "/run/spin-stack"

	// BinaryName is the supervisor binary filename in the bundle.
	BinaryName = "spin-supervisor"

	// BinaryPath is the expected path for supervisor binary from extras disk.
	// Uses /var/lib instead of /run because /run is mounted noexec.
	BinaryPath = "/var/lib/spin-stack/bin/spin-supervisor"

	// PidFile is where the supervisor PID is written.
	PidFile = "/run/spin-supervisor.pid"

	// LogFile is where supervisor logs are written.
	LogFile = "/var/log/spin-supervisor.log"

	// OverlayRootEnvVar is the environment variable name used to pass
	// the overlay filesystem root path to the supervisor.
	OverlayRootEnvVar = "SPINBOX_OVERLAY_ROOT"
)

// findBinary searches for the supervisor binary.
// Returns the path if found, empty string otherwise.
func findBinary(ctx context.Context) string {
	// First, check the expected path from extras disk
	if _, err := os.Stat(BinaryPath); err == nil {
		log.G(ctx).WithField("path", BinaryPath).Debug("found supervisor binary at expected path")
		return BinaryPath
	}

	// Fall back to bundle directories (legacy/backward compatibility)
	entries, err := os.ReadDir(BundleBasePath)
	if err != nil {
		return ""
	}

	for _, nsEntry := range entries {
		if !nsEntry.IsDir() {
			continue
		}

		nsPath := filepath.Join(BundleBasePath, nsEntry.Name())
		idEntries, err := os.ReadDir(nsPath)
		if err != nil {
			continue
		}

		for _, idEntry := range idEntries {
			if !idEntry.IsDir() {
				continue
			}

			binaryPath := filepath.Join(nsPath, idEntry.Name(), BinaryName)
			if _, err := os.Stat(binaryPath); err == nil {
				log.G(ctx).WithField("path", binaryPath).Debug("found supervisor binary in bundle directory")
				return binaryPath
			}
		}
	}

	return ""
}

// RunWithMonitoring starts the supervisor agent with automatic restart on crash.
// It blocks until the context is cancelled or max restarts is exceeded.
//
// The supervisor handles its own configuration by connecting to the runner's
// metadata service via vsock (CID 2, port 1027).
//
// Returns nil immediately if supervisor binary is not found.
func RunWithMonitoring(ctx context.Context) error {
	return runWithEnv(ctx, nil)
}

// StartWithOverlayRoot starts the supervisor agent with the overlay root path
// passed via environment variable. This should be called after the container's
// rootfs is mounted so the supervisor can access files in the overlay.
//
// The overlayRoot parameter is the path to the container's mounted rootfs,
// which will be passed to the supervisor via SPINBOX_OVERLAY_ROOT env var.
//
// This function runs in a goroutine and does not block.
func StartWithOverlayRoot(ctx context.Context, overlayRoot string) {
	env := []string{OverlayRootEnvVar + "=" + overlayRoot}

	log.G(ctx).WithField("overlay_root", overlayRoot).Info("starting supervisor with overlay root")

	if err := runWithEnv(ctx, env); err != nil {
		log.G(ctx).WithError(err).Error("supervisor monitor exited with error")
	}
}

// runWithEnv starts the supervisor with optional additional environment variables.
func runWithEnv(ctx context.Context, extraEnv []string) error {
	binaryPath := findBinary(ctx)
	if binaryPath == "" {
		log.G(ctx).Info("supervisor binary not found, skipping")
		return nil
	}

	log.G(ctx).WithField("path", binaryPath).Info("starting supervisor with monitoring")
	monitor := NewMonitor(binaryPath)
	monitor.SetExtraEnv(extraEnv)
	return monitor.Run(ctx)
}
