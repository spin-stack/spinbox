//go:build darwin

// Package network provides stub implementations for Darwin.
// Networking is only supported on Linux.
package network

import (
	"context"
	"fmt"
	"net"
)

// NetworkConfig defines network configuration (Darwin stub)
type NetworkConfig struct {
	CNIConfDir string
	CNIBinDir  string
}

// LoadNetworkConfig returns stub config.
func LoadNetworkConfig() NetworkConfig {
	return NetworkConfig{}
}

// NetworkInfo holds internal network configuration
type NetworkInfo struct {
	TapName string `json:"tap_name"`
	MAC     string `json:"mac"`
	IP      net.IP `json:"ip"`
	Netmask string `json:"netmask"`
	Gateway net.IP `json:"gateway"`
}

// Environment represents a VM/container network environment
type Environment struct {
	ID          string
	NetworkInfo *NetworkInfo
}

// NetworkManager defines the interface for network management operations
type NetworkManager interface {
	Close() error
	EnsureNetworkResources(ctx context.Context, env *Environment) error
	ReleaseNetworkResources(ctx context.Context, env *Environment) error
}

// NewNetworkManager returns an error on Darwin (not supported)
func NewNetworkManager(ctx context.Context, config NetworkConfig) (NetworkManager, error) {
	return nil, fmt.Errorf("network manager not supported on darwin")
}
