package metadata

import (
	"context"
	"net"

	"github.com/aledbf/qemubox/containerd/internal/host/vm"
)

// MetadataManager persists and recovers container/VM state via containerd labels.
// This enables state recovery across shim restarts and provides visibility
// into container state for debugging and monitoring.
//
// All methods are designed to be non-blocking and should not fail container
// operations if label persistence fails. Implementations should log warnings
// on failures rather than returning errors that would fail container lifecycle.
type MetadataManager interface {
	// SetVMState updates the VM lifecycle state label.
	SetVMState(ctx context.Context, containerID string, state VMState) error

	// SetResourceConfig persists VM resource configuration labels.
	SetResourceConfig(ctx context.Context, containerID string, cfg *vm.VMResourceConfig) error

	// SetNetworkInfo persists network configuration labels.
	SetNetworkInfo(ctx context.Context, containerID string, info *NetworkState) error

	// SetProcessInfo persists init process state labels.
	SetProcessInfo(ctx context.Context, containerID string, pid uint32, terminal bool) error

	// SetVMInfo persists VM-specific information (CID, state directory, PID).
	SetVMInfo(ctx context.Context, containerID string, info *VMInfo) error

	// SetTimestamp sets a timestamp label (created-at or started-at).
	SetTimestamp(ctx context.Context, containerID string, label string) error

	// SetError stores an error description in labels for debugging.
	SetError(ctx context.Context, containerID string, errMsg string) error

	// GetRecoverableState reads all qemubox labels and returns recoverable state.
	// Returns nil if no qemubox labels are found.
	GetRecoverableState(ctx context.Context, containerID string) (*RecoverableState, error)

	// ClearLabels removes all qemubox-prefixed labels from the container.
	// Called during container deletion to clean up metadata.
	ClearLabels(ctx context.Context, containerID string) error

	// Close releases resources (closes containerd client connection).
	Close() error
}

// NetworkState holds network configuration to persist.
type NetworkState struct {
	IP        net.IP
	Gateway   net.IP
	Netmask   string
	MAC       string
	TAPDevice string
	NetnsPath string
	DNS       []string
	Interface string
}

// VMInfo holds VM-specific information to persist.
type VMInfo struct {
	CID      uint32
	StateDir string
	PID      int
}

// RecoverableState contains all state that can be recovered from labels.
// This is used when a shim restarts and needs to reconnect to an existing VM.
type RecoverableState struct {
	// VM state
	VMState  VMState
	VMCID    uint32
	StateDir string
	VMPID    int

	// Resource configuration
	ResourceConfig *vm.VMResourceConfig

	// Network configuration
	NetworkIP        net.IP
	NetworkGateway   net.IP
	NetworkNetmask   string
	NetworkMAC       string
	NetworkTAP       string
	NetworkNetns     string
	NetworkDNS       []string
	NetworkInterface string

	// Process state
	InitPID      uint32
	InitTerminal bool

	// Timestamps
	CreatedAt string
	StartedAt string

	// Error (if any)
	Error string
}

// IsRunning returns true if the recovered state indicates the VM should be running.
func (s *RecoverableState) IsRunning() bool {
	return s != nil && s.VMState == VMStateRunning && s.VMCID > 0
}

// HasNetworkInfo returns true if network configuration was recovered.
func (s *RecoverableState) HasNetworkInfo() bool {
	return s != nil && len(s.NetworkIP) > 0
}

// noopManager is a MetadataManager that does nothing.
// Used when containerd connection is unavailable.
type noopManager struct{}

// NewNoopManager returns a MetadataManager that does nothing.
// Use this when containerd connection fails to enable graceful degradation.
func NewNoopManager() MetadataManager {
	return &noopManager{}
}

func (n *noopManager) SetVMState(context.Context, string, VMState) error {
	return nil
}

func (n *noopManager) SetResourceConfig(context.Context, string, *vm.VMResourceConfig) error {
	return nil
}

func (n *noopManager) SetNetworkInfo(context.Context, string, *NetworkState) error {
	return nil
}

func (n *noopManager) SetProcessInfo(context.Context, string, uint32, bool) error {
	return nil
}

func (n *noopManager) SetVMInfo(context.Context, string, *VMInfo) error {
	return nil
}

func (n *noopManager) SetTimestamp(context.Context, string, string) error {
	return nil
}

func (n *noopManager) SetError(context.Context, string, string) error {
	return nil
}

func (n *noopManager) GetRecoverableState(context.Context, string) (*RecoverableState, error) {
	return nil, nil
}

func (n *noopManager) ClearLabels(context.Context, string) error {
	return nil
}

func (n *noopManager) Close() error {
	return nil
}
