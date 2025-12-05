//go:build darwin

package task

import (
	"context"
	"fmt"

	"github.com/aledbf/beacon/containerd/vm"
	"github.com/containerd/containerd/api/types"
)

// setupMounts is a stub for Darwin
func setupMounts(ctx context.Context, vmi vm.Instance, id string, rootfs []*types.Mount, bundleRootfs string, mountDir string) ([]*types.Mount, error) {
	return nil, fmt.Errorf("not supported on darwin")
}
