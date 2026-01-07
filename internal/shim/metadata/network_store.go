//go:build linux

package metadata

import (
	"context"

	"github.com/aledbf/qemubox/containerd/internal/host/network"
)

// networkStateStoreAdapter adapts MetadataManager to implement network.NetworkStateStore.
// This bridges the metadata persistence layer with the network manager.
type networkStateStoreAdapter struct {
	manager MetadataManager
}

// NewNetworkStateStore creates a NetworkStateStore that persists network state
// via the given MetadataManager.
func NewNetworkStateStore(manager MetadataManager) network.NetworkStateStore {
	if manager == nil {
		return &noopNetworkStateStore{}
	}
	return &networkStateStoreAdapter{manager: manager}
}

// GetNetworkInfo retrieves persisted network state for a container.
func (a *networkStateStoreAdapter) GetNetworkInfo(ctx context.Context, containerID string) (*network.NetworkInfo, error) {
	state, err := a.manager.GetRecoverableState(ctx, containerID)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, nil
	}

	// Check if we have network state
	if state.NetworkTAP == "" && len(state.NetworkIP) == 0 {
		return nil, nil
	}

	return &network.NetworkInfo{
		TapName: state.NetworkTAP,
		MAC:     state.NetworkMAC,
		IP:      state.NetworkIP,
		Netmask: state.NetworkNetmask,
		Gateway: state.NetworkGateway,
	}, nil
}

// SetNetworkInfo persists network state for a container.
func (a *networkStateStoreAdapter) SetNetworkInfo(ctx context.Context, containerID string, info *network.NetworkInfo) error {
	if info == nil {
		return nil
	}

	state := &NetworkState{
		IP:        info.IP,
		Gateway:   info.Gateway,
		Netmask:   info.Netmask,
		MAC:       info.MAC,
		TAPDevice: info.TapName,
	}
	return a.manager.SetNetworkInfo(ctx, containerID, state)
}

// ClearNetworkInfo removes persisted network state for a container.
func (a *networkStateStoreAdapter) ClearNetworkInfo(ctx context.Context, containerID string) error {
	// Clear network-related labels by setting empty NetworkState
	// The metadata manager will handle this appropriately
	return a.manager.SetNetworkInfo(ctx, containerID, &NetworkState{})
}

// noopNetworkStateStore is a NetworkStateStore that does nothing.
// Used when no MetadataManager is available.
type noopNetworkStateStore struct{}

func (n *noopNetworkStateStore) GetNetworkInfo(context.Context, string) (*network.NetworkInfo, error) {
	return nil, nil
}

func (n *noopNetworkStateStore) SetNetworkInfo(context.Context, string, *network.NetworkInfo) error {
	return nil
}

func (n *noopNetworkStateStore) ClearNetworkInfo(context.Context, string) error {
	return nil
}

// Ensure interfaces are satisfied at compile time.
var (
	_ network.NetworkStateStore = (*networkStateStoreAdapter)(nil)
	_ network.NetworkStateStore = (*noopNetworkStateStore)(nil)
)
