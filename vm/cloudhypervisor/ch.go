package cloudhypervisor

import (
	"net"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/aledbf/beacon/containerd/vm"
	"github.com/containerd/ttrpc"
)

// vmState represents the lifecycle state of a VM instance.
type vmState uint32

const (
	vmStateNew      vmState = iota // Instance created, not started
	vmStateStarting                // Start() in progress
	vmStateRunning                 // VM is running
	vmStateShutdown                // Shutdown() called
)

// Instance represents a Cloud Hypervisor VM instance.
type Instance struct {
	// mu protects fields that are read/written concurrently during configuration
	// and startup: disks, filesystems, nets, cmd, api, client, vsockConn.
	// State transitions are managed via vmState atomic.
	mu            sync.Mutex
	vmState       atomic.Uint32 // Current VM state (vmState)
	binaryPath    string
	stateDir      string // State directory path for sockets and other runtime files
	logDir        string // Log directory path for persistent logs
	kernelPath    string
	initrdPath    string
	apiSocketPath string
	vsockRPCPath  string
	streamPath    string
	consolePath   string

	cmd       *exec.Cmd
	api       *CloudHypervisorClient
	client    *ttrpc.Client
	vsockConn net.Conn // Connection to vsock RPC server
	streamC   uint32

	// Configuration collected before Start()
	disks       []*DiskConfig
	filesystems []*FsConfig
	nets        []*NetConfig
	networkCfg  *vm.NetworkConfig       // CNI network configuration (IP, gateway, netmask)
	resourceCfg *vm.VMResourceConfig    // CPU and memory resource configuration
}

// VMResourceConfig is an alias for the shared vm.VMResourceConfig type.
// Deprecated: Use vm.VMResourceConfig directly.
type VMResourceConfig = vm.VMResourceConfig
