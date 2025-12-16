//go:build linux

package network

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/aledbf/beacon/containerd/network/cni"
	boltstore "github.com/aledbf/beacon/containerd/store"
)

// newCNINetworkManager creates a NetworkManager configured for CNI mode.
func newCNINetworkManager(
	ctx context.Context,
	cancel context.CancelFunc,
	config NetworkConfig,
	networkConfigStore boltstore.Store[NetworkConfig],
) (*NetworkManager, error) {
	if networkConfigStore == nil {
		cancel()
		return nil, fmt.Errorf("NetworkConfigStore is required")
	}

	// Create CNI manager
	cniMgr, err := cni.NewCNIManager(
		config.CNIConfDir,
		config.CNIBinDir,
		config.CNINetworkName,
	)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create CNI manager: %w", err)
	}

	slog.InfoContext(ctx, "CNI mode enabled",
		"confDir", config.CNIConfDir,
		"binDir", config.CNIBinDir,
		"network", config.CNINetworkName,
	)

	nm := &NetworkManager{
		config:             config,
		networkConfigStore: networkConfigStore,
		ctx:                ctx,
		cancelFunc:         cancel,
		cniManager:         cniMgr,
		cniResults:         make(map[string]*cni.CNIResult),
	}

	return nm, nil
}

// ensureNetworkResourcesCNI allocates and configures network resources using CNI plugins.
func (nm *NetworkManager) ensureNetworkResourcesCNI(env *Environment) error {
	// Check if already configured
	nm.cniMu.RLock()
	if result, exists := nm.cniResults[env.Id]; exists {
		nm.cniMu.RUnlock()
		slog.DebugContext(nm.ctx, "CNI resources already allocated", "vmID", env.Id, "tap", result.TAPDevice)
		return nil
	}
	nm.cniMu.RUnlock()

	// Create network namespace for CNI execution
	netnsPath, err := cni.CreateNetNS(env.Id)
	if err != nil {
		return fmt.Errorf("failed to create netns for CNI: %w", err)
	}

	// Execute CNI plugin chain
	result, err := nm.cniManager.Setup(nm.ctx, env.Id, netnsPath)
	if err != nil {
		// Clean up netns on failure
		_ = cni.DeleteNetNS(env.Id)
		return fmt.Errorf("failed to setup CNI network: %w", err)
	}

	slog.InfoContext(nm.ctx, "CNI network configured",
		"vmID", env.Id,
		"tap", result.TAPDevice,
		"ip", result.IPAddress,
		"gateway", result.Gateway,
	)

	// Store CNI result for teardown
	nm.cniMu.Lock()
	nm.cniResults[env.Id] = result
	nm.cniMu.Unlock()

	// Update environment with network info
	env.NetworkInfo = &NetworkInfo{
		TapName:    result.TAPDevice,
		BridgeName: bridgeName, // CNI may use different bridge, but keep for compatibility
		IP:         result.IPAddress,
		Netmask:    "255.255.0.0", // TODO: Extract from CNI result
		Gateway:    result.Gateway,
	}

	// Clean up the temporary netns (TAP device is now in host namespace)
	if err := cni.DeleteNetNS(env.Id); err != nil {
		slog.WarnContext(nm.ctx, "Failed to delete temporary netns", "vmID", env.Id, "error", err)
	}

	return nil
}

// releaseNetworkResourcesCNI releases network resources using CNI plugins.
func (nm *NetworkManager) releaseNetworkResourcesCNI(env *Environment) error {
	// Get CNI result for this VM
	nm.cniMu.RLock()
	result, exists := nm.cniResults[env.Id]
	nm.cniMu.RUnlock()

	if !exists {
		slog.WarnContext(nm.ctx, "No CNI result found for VM", "vmID", env.Id)
		return nil
	}

	// Create temporary netns for CNI teardown
	netnsPath, err := cni.CreateNetNS(env.Id)
	if err != nil {
		slog.WarnContext(nm.ctx, "Failed to create netns for CNI teardown", "vmID", env.Id, "error", err)
		// Continue with cleanup anyway
		netnsPath = ""
	}

	// Execute CNI DEL operation
	if err := nm.cniManager.Teardown(nm.ctx, env.Id, netnsPath); err != nil {
		slog.WarnContext(nm.ctx, "Failed to teardown CNI network", "vmID", env.Id, "error", err)
		// Continue with cleanup
	}

	// Clean up netns
	if netnsPath != "" {
		if err := cni.DeleteNetNS(env.Id); err != nil {
			slog.WarnContext(nm.ctx, "Failed to delete netns", "vmID", env.Id, "error", err)
		}
	}

	// Remove from CNI results map
	nm.cniMu.Lock()
	delete(nm.cniResults, env.Id)
	nm.cniMu.Unlock()

	slog.InfoContext(nm.ctx, "CNI network released",
		"vmID", env.Id,
		"tap", result.TAPDevice,
	)

	// Clear environment network info
	env.NetworkInfo = nil

	return nil
}
