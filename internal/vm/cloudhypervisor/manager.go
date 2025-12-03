package cloudhypervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// NewInstance creates a new Cloud Hypervisor VM instance.
// The state parameter is the directory where VM state files will be stored.
// The resourceCfg parameter specifies CPU and memory configuration.
func NewInstance(ctx context.Context, state string, resourceCfg *VMResourceConfig) (*Instance, error) {
	// Locate cloud-hypervisor binary
	binaryPath, err := findCloudHypervisor()
	if err != nil {
		return nil, err
	}

	// Ensure state directory exists
	if err := os.MkdirAll(state, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return newInstance(ctx, binaryPath, state, resourceCfg)
}

// findCloudHypervisor locates the cloud-hypervisor binary
func findCloudHypervisor() (string, error) {
	// 1. Check environment variable
	if path := os.Getenv("CLOUD_HYPERVISOR_PATH"); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
		return "", fmt.Errorf("CLOUD_HYPERVISOR_PATH set to %q but file not found", path)
	}

	// 2. Search in PATH
	if path, err := exec.LookPath("cloud-hypervisor"); err == nil {
		return path, nil
	}

	// 3. Check common installation locations
	commonPaths := []string{
		"/usr/local/bin/cloud-hypervisor",
		"/usr/bin/cloud-hypervisor",
		"/opt/cloud-hypervisor/bin/cloud-hypervisor",
	}

	for _, path := range commonPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("cloud-hypervisor binary not found in PATH or common locations; install cloud-hypervisor or set CLOUD_HYPERVISOR_PATH")
}

// findKernel locates the kernel binary for Cloud Hypervisor
func findKernel() (string, error) {
	// Try beaconbox kernel first (same as libkrun)
	kernelName := "beaconbox-kernel-x86_64"

	// Build search paths matching libkrun's logic
	searchPaths := []string{}

	// 1. Add PATH environment variable
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		searchPaths = append(searchPaths, filepath.SplitList(pathEnv)...)
	}

	// 3. Add common beaconbox locations
	searchPaths = append(searchPaths, "/usr/share/beaconbox", "/usr/local/share/beaconbox")

	// Search through all paths
	for _, dir := range searchPaths {
		if dir == "" {
			dir = "."
		}
		path := filepath.Join(dir, kernelName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("kernel %q not found in PATH, LIBKRUN_PATH, or common locations", kernelName)
}

// findInitrd locates the initrd for Cloud Hypervisor
func findInitrd() (string, error) {
	initrdName := "beaconbox-initrd"

	// Build search paths matching libkrun's logic
	searchPaths := []string{}

	// 1. Add PATH environment variable
	if pathEnv := os.Getenv("PATH"); pathEnv != "" {
		searchPaths = append(searchPaths, filepath.SplitList(pathEnv)...)
	}

	// 3. Add common beaconbox locations
	searchPaths = append(searchPaths, "/usr/share/beaconbox", "/usr/local/share/beaconbox")

	// Search through all paths
	for _, dir := range searchPaths {
		if dir == "" {
			dir = "."
		}
		path := filepath.Join(dir, initrdName)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("initrd %q not found in PATH, LIBKRUN_PATH, or common locations", initrdName)
}
