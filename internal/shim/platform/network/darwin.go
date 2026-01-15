//go:build darwin

package network

import (
	"context"
	"fmt"

	"github.com/spin-stack/spinbox/internal/host/network"
	"github.com/spin-stack/spinbox/internal/host/vm"
)

type darwinManager struct{}

func newManager() Manager {
	return &darwinManager{}
}

func (m *darwinManager) InitNetworkManager(ctx context.Context) (network.NetworkManager, error) {
	return nil, fmt.Errorf("network manager not supported on darwin")
}

func (m *darwinManager) Setup(_ context.Context, _ network.NetworkManager, _ vm.Instance, _, _ string) (*SetupResult, error) {
	return nil, fmt.Errorf("networking not supported on darwin")
}
