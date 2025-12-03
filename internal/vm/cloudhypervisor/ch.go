package cloudhypervisor

import (
	"context"
	"os/exec"
	"sync"

	"github.com/aledbf/beacon/containerd/internal/vm"
	"github.com/containerd/ttrpc"
)

// Instance represents a Cloud Hypervisor VM instance.
type Instance struct {
	mu            sync.Mutex
	binaryPath    string
	state         string
	kernelPath    string
	initrdPath    string
	apiSocketPath string
	vsockRPCPath  string
	streamPath    string
	consolePath   string

	cmd     *exec.Cmd
	api     *CloudHypervisorClient
	client  *ttrpc.Client
	streamC uint32

	// Configuration collected before Start()
	disks       []*DiskConfig
	filesystems []*FsConfig
	nets        []*NetConfig
	networkCfg  *vm.NetworkConfig // CNI network configuration (IP, gateway, netmask)
	resourceCfg *VMResourceConfig // CPU and memory resource configuration

	shutdownCallbacks []func(context.Context) error
}

// VMResourceConfig holds the CPU and memory configuration for a VM instance
type VMResourceConfig struct {
	// BootCPUs is the number of vCPUs to boot with (minimum)
	BootCPUs int
	// MaxCPUs is the maximum number of vCPUs that can be hotplugged
	MaxCPUs int
	// MemorySize is the initial memory size in bytes
	MemorySize int64
	// MemoryHotplugSize is the maximum memory size in bytes (for hotplug)
	MemoryHotplugSize int64
}
