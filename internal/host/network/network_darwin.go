//go:build darwin

// Package network provides stub implementations for Darwin.
// Networking is only supported on Linux.
package network

import (
	"context"
	"fmt"
)

// LoadNetworkConfig returns stub config.
func LoadNetworkConfig() NetworkConfig {
	return NetworkConfig{}
}

// NewNetworkManager returns an error on Darwin (not supported)
func NewNetworkManager(ctx context.Context, config NetworkConfig) (NetworkManager, error) {
	return nil, fmt.Errorf("network manager not supported on darwin")
}
