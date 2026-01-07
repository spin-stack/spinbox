// Package network provides platform-specific network setup for VMs.
// It handles network manager initialization and network configuration.
package network

import (
	"context"

	"github.com/aledbf/qemubox/containerd/internal/host/network"
	"github.com/aledbf/qemubox/containerd/internal/host/vm"
)

// Manager handles platform-specific network operations.
type Manager interface {
	// InitNetworkManager creates and initializes a NetworkManager for the platform.
	// The stateStore is used to persist network state across shim restarts.
	// If stateStore is nil, network state will not be persisted.
	InitNetworkManager(ctx context.Context, stateStore network.NetworkStateStore) (network.NetworkManager, error)

	// Setup configures networking for a VM instance.
	// Returns the network configuration and an error if setup fails.
	Setup(ctx context.Context, nm network.NetworkManager, vmi vm.Instance, containerID, netnsPath string) (*vm.NetworkConfig, error)
}

// New creates a platform-specific network manager.
// Returns the appropriate implementation for the current OS.
func New() Manager {
	return newManager()
}
