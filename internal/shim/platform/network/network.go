// Package network provides platform-specific network setup for VMs.
// It handles network manager initialization and network configuration.
package network

import (
	"context"

	"github.com/spin-stack/spinbox/internal/host/network"
	"github.com/spin-stack/spinbox/internal/host/vm"
)

// SetupResult contains the network configuration for the VM along with
// host-side metadata that can be used for container labeling.
type SetupResult struct {
	// Config contains network parameters passed to the VM kernel.
	Config *vm.NetworkConfig

	// TAPName is the host TAP device name (e.g., "spin0-abc123").
	TAPName string

	// MAC is the guest MAC address (e.g., "52:54:00:xx:xx:xx").
	MAC string
}

// Manager handles platform-specific network operations.
type Manager interface {
	// InitNetworkManager creates and initializes a NetworkManager for the platform.
	InitNetworkManager(ctx context.Context) (network.NetworkManager, error)

	// Setup configures networking for a VM instance.
	// Returns the network configuration with host-side metadata and an error if setup fails.
	Setup(ctx context.Context, nm network.NetworkManager, vmi vm.Instance, containerID, netnsPath string) (*SetupResult, error)
}

// New creates a platform-specific network manager.
// Returns the appropriate implementation for the current OS.
func New() Manager {
	return newManager()
}
