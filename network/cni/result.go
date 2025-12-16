//go:build linux

package cni

import (
	"fmt"
	"net"

	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
)

// CNIResult contains the parsed result from CNI plugin execution.
type CNIResult struct {
	// TAPDevice is the name of the TAP device created by CNI plugins.
	TAPDevice string

	// IPAddress is the IP address allocated to the VM.
	IPAddress net.IP

	// Gateway is the gateway IP address for the network.
	Gateway net.IP

	// Routes contains the routing table entries.
	Routes []*types.Route

	// DNS contains DNS configuration.
	DNS types.DNS

	// Interfaces contains all network interfaces created.
	Interfaces []*current.Interface
}

// ParseCNIResult parses a CNI result and extracts networking information.
//
// This function:
// 1. Extracts the TAP device from the CNI result interfaces
// 2. Parses IP address and gateway information
// 3. Collects routes and DNS configuration
func ParseCNIResult(result *current.Result) (*CNIResult, error) {
	if result == nil {
		return nil, fmt.Errorf("CNI result is nil")
	}

	// Extract TAP device
	tapDevice, err := ExtractTAPDevice(result)
	if err != nil {
		return nil, fmt.Errorf("failed to extract TAP device: %w", err)
	}

	// Parse IP address and gateway
	var ipAddress net.IP
	var gateway net.IP

	if len(result.IPs) > 0 {
		// Use the first IP configuration
		ipConfig := result.IPs[0]
		ipAddress = ipConfig.Address.IP
		gateway = ipConfig.Gateway
	}

	// Collect routes
	var routes []*types.Route
	for _, r := range result.Routes {
		routes = append(routes, &types.Route{
			Dst: r.Dst,
			GW:  r.GW,
		})
	}

	return &CNIResult{
		TAPDevice:  tapDevice,
		IPAddress:  ipAddress,
		Gateway:    gateway,
		Routes:     routes,
		DNS:        result.DNS,
		Interfaces: result.Interfaces,
	}, nil
}

// GetInterface returns the interface with the given name from the CNI result.
func (r *CNIResult) GetInterface(name string) *current.Interface {
	for _, iface := range r.Interfaces {
		if iface.Name == name {
			return iface
		}
	}
	return nil
}

// GetIPConfig returns the IP configuration for the specified interface.
func (r *CNIResult) GetIPConfig(result *current.Result, ifaceName string) *current.IPConfig {
	// Find interface index
	var ifaceIdx int
	found := false
	for i, iface := range result.Interfaces {
		if iface.Name == ifaceName {
			ifaceIdx = i
			found = true
			break
		}
	}

	if !found {
		return nil
	}

	// Find IP config for this interface
	for _, ipConfig := range result.IPs {
		if ipConfig.Interface != nil && *ipConfig.Interface == ifaceIdx {
			return ipConfig
		}
	}

	return nil
}
