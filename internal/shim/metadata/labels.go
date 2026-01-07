// Package metadata provides persistent metadata storage for qemubox containers
// using containerd's label system.
//
// This package defines two types of metadata:
//   - Labels (qemubox/*): Runtime state that can be updated during container lifecycle
//   - Annotations (qemubox.io/*): Configuration set at creation time (immutable)
//
// Labels are stored in containerd's BoltDB and persist across shim restarts,
// enabling state recovery and debugging.
package metadata

// Label key prefixes and constants for qemubox container metadata.
// These labels are stored in containerd's metadata database and can be
// updated at runtime via the containerd client API.
const (
	// LabelPrefix is the prefix for all qemubox-managed labels.
	// Labels use forward slash as separator per containerd convention.
	LabelPrefix = "qemubox/"

	// === VM State Labels ===

	// LabelVMState tracks the VM lifecycle state.
	// Values: "creating", "running", "shutting_down"
	LabelVMState = LabelPrefix + "vm.state"

	// LabelVMCID stores the vsock guest context ID (CID).
	// This is required for host-to-guest communication.
	LabelVMCID = LabelPrefix + "vm.cid"

	// LabelVMStateDir stores the VM state directory path.
	// Contains QMP socket, logs, and other VM runtime files.
	LabelVMStateDir = LabelPrefix + "vm.state-dir"

	// LabelVMPID stores the QEMU process PID.
	LabelVMPID = LabelPrefix + "vm.pid"

	// === Resource Configuration Labels ===

	// LabelBootCPUs is the initial number of vCPUs allocated to the VM.
	LabelBootCPUs = LabelPrefix + "resources.boot-cpus"

	// LabelMaxCPUs is the maximum vCPUs available for hotplug.
	LabelMaxCPUs = LabelPrefix + "resources.max-cpus"

	// LabelMemorySize is the initial memory in bytes.
	LabelMemorySize = LabelPrefix + "resources.memory-size"

	// LabelMemoryHotplugSize is the maximum memory for hotplug in bytes.
	LabelMemoryHotplugSize = LabelPrefix + "resources.memory-hotplug-size"

	// LabelMemorySlots is the number of memory hotplug slots.
	LabelMemorySlots = LabelPrefix + "resources.memory-slots"

	// === Network State Labels ===

	// LabelNetworkIP is the IPv4 address allocated to the VM.
	LabelNetworkIP = LabelPrefix + "network.ip"

	// LabelNetworkGateway is the gateway IP address.
	LabelNetworkGateway = LabelPrefix + "network.gateway"

	// LabelNetworkNetmask is the network mask.
	LabelNetworkNetmask = LabelPrefix + "network.netmask"

	// LabelNetworkMAC is the MAC address of the VM's network interface.
	LabelNetworkMAC = LabelPrefix + "network.mac"

	// LabelNetworkTAP is the TAP device name on the host.
	LabelNetworkTAP = LabelPrefix + "network.tap"

	// LabelNetworkNetns is the path to the network namespace.
	LabelNetworkNetns = LabelPrefix + "network.netns"

	// LabelNetworkDNS is a comma-separated list of DNS servers.
	LabelNetworkDNS = LabelPrefix + "network.dns"

	// LabelNetworkInterface is the interface name inside the VM.
	LabelNetworkInterface = LabelPrefix + "network.interface"

	// === Process State Labels ===

	// LabelInitPID is the init process PID inside the VM.
	LabelInitPID = LabelPrefix + "process.init-pid"

	// LabelInitTerminal indicates whether the init process uses a terminal.
	LabelInitTerminal = LabelPrefix + "process.init-terminal"

	// === Timestamp Labels ===

	// LabelCreatedAt records when the container was created (RFC3339).
	LabelCreatedAt = LabelPrefix + "created-at"

	// LabelStartedAt records when the VM was started (RFC3339).
	LabelStartedAt = LabelPrefix + "started-at"

	// === Error Labels ===

	// LabelError stores a human-readable error description if the container
	// failed during creation or runtime.
	LabelError = LabelPrefix + "error"
)

// VMState represents the VM lifecycle state stored in labels.
type VMState string

const (
	// VMStateCreating indicates container/VM creation is in progress.
	VMStateCreating VMState = "creating"

	// VMStateRunning indicates the VM is running and container is active.
	VMStateRunning VMState = "running"

	// VMStateShuttingDown indicates shutdown has been initiated.
	VMStateShuttingDown VMState = "shutting_down"
)

// String returns the string representation of the VM state.
func (s VMState) String() string {
	return string(s)
}

// IsValid returns true if the state is a recognized value.
func (s VMState) IsValid() bool {
	switch s {
	case VMStateCreating, VMStateRunning, VMStateShuttingDown:
		return true
	default:
		return false
	}
}
