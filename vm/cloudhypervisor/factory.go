package cloudhypervisor

import (
	"context"

	"github.com/aledbf/beacon/containerd/paths"
	"github.com/aledbf/beacon/containerd/vm"
)

type factory struct{}

// init registers the Cloud Hypervisor factory
func init() {
	vm.Register(vm.VMTypeCloudHypervisor, &factory{})
}

func (f *factory) NewInstance(ctx context.Context, containerID, stateDir string, cfg *vm.VMResourceConfig) (vm.Instance, error) {
	binaryPath := paths.CloudHypervisorPath()
	return newInstance(ctx, containerID, binaryPath, stateDir, cfg)
}
