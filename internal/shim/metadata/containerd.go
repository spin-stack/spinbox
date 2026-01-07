//go:build linux

package metadata

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/log"

	"github.com/aledbf/qemubox/containerd/internal/host/vm"
)

// containerdMetadataManager implements MetadataManager using containerd labels.
type containerdMetadataManager struct {
	client    *containerd.Client
	namespace string
}

// NewContainerdMetadataManager creates a MetadataManager that persists state
// via containerd container labels.
//
// The address should be the containerd socket path (e.g., "/run/containerd/containerd.sock").
// The namespace should match the container's namespace.
//
// Returns an error if the connection to containerd fails.
func NewContainerdMetadataManager(ctx context.Context, address, namespace string) (MetadataManager, error) {
	if address == "" {
		return nil, fmt.Errorf("containerd address is required")
	}
	if namespace == "" {
		namespace = "default"
	}

	client, err := containerd.New(address, containerd.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("connect to containerd at %s: %w", address, err)
	}

	// Verify connection works
	if _, err := client.Version(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("verify containerd connection: %w", err)
	}

	log.G(ctx).WithFields(log.Fields{
		"address":   address,
		"namespace": namespace,
	}).Debug("connected to containerd for metadata persistence")

	return &containerdMetadataManager{
		client:    client,
		namespace: namespace,
	}, nil
}

// SetVMState updates the VM lifecycle state label.
func (m *containerdMetadataManager) SetVMState(ctx context.Context, containerID string, state VMState) error {
	return m.updateLabels(ctx, containerID, map[string]string{
		LabelVMState: state.String(),
	})
}

// SetResourceConfig persists VM resource configuration labels.
func (m *containerdMetadataManager) SetResourceConfig(ctx context.Context, containerID string, cfg *vm.VMResourceConfig) error {
	if cfg == nil {
		return nil
	}

	labels := map[string]string{
		LabelBootCPUs:          strconv.Itoa(cfg.BootCPUs),
		LabelMaxCPUs:           strconv.Itoa(cfg.MaxCPUs),
		LabelMemorySize:        strconv.FormatInt(cfg.MemorySize, 10),
		LabelMemoryHotplugSize: strconv.FormatInt(cfg.MemoryHotplugSize, 10),
		LabelMemorySlots:       strconv.Itoa(cfg.MemorySlots),
	}

	return m.updateLabels(ctx, containerID, labels)
}

// SetNetworkInfo persists network configuration labels.
func (m *containerdMetadataManager) SetNetworkInfo(ctx context.Context, containerID string, info *NetworkState) error {
	if info == nil {
		return nil
	}

	labels := map[string]string{}

	if len(info.IP) > 0 {
		labels[LabelNetworkIP] = info.IP.String()
	}
	if len(info.Gateway) > 0 {
		labels[LabelNetworkGateway] = info.Gateway.String()
	}
	if info.Netmask != "" {
		labels[LabelNetworkNetmask] = info.Netmask
	}
	if info.MAC != "" {
		labels[LabelNetworkMAC] = info.MAC
	}
	if info.TAPDevice != "" {
		labels[LabelNetworkTAP] = info.TAPDevice
	}
	if info.NetnsPath != "" {
		labels[LabelNetworkNetns] = info.NetnsPath
	}
	if len(info.DNS) > 0 {
		labels[LabelNetworkDNS] = strings.Join(info.DNS, ",")
	}
	if info.Interface != "" {
		labels[LabelNetworkInterface] = info.Interface
	}

	if len(labels) == 0 {
		return nil
	}

	return m.updateLabels(ctx, containerID, labels)
}

// SetProcessInfo persists init process state labels.
func (m *containerdMetadataManager) SetProcessInfo(ctx context.Context, containerID string, pid uint32, terminal bool) error {
	return m.updateLabels(ctx, containerID, map[string]string{
		LabelInitPID:      strconv.FormatUint(uint64(pid), 10),
		LabelInitTerminal: strconv.FormatBool(terminal),
	})
}

// SetVMInfo persists VM-specific information.
func (m *containerdMetadataManager) SetVMInfo(ctx context.Context, containerID string, info *VMInfo) error {
	if info == nil {
		return nil
	}

	labels := map[string]string{}

	if info.CID > 0 {
		labels[LabelVMCID] = strconv.FormatUint(uint64(info.CID), 10)
	}
	if info.StateDir != "" {
		labels[LabelVMStateDir] = info.StateDir
	}
	if info.PID > 0 {
		labels[LabelVMPID] = strconv.Itoa(info.PID)
	}

	if len(labels) == 0 {
		return nil
	}

	return m.updateLabels(ctx, containerID, labels)
}

// SetTimestamp sets a timestamp label.
func (m *containerdMetadataManager) SetTimestamp(ctx context.Context, containerID string, label string) error {
	timestamp := time.Now().UTC().Format(time.RFC3339)
	return m.updateLabels(ctx, containerID, map[string]string{
		label: timestamp,
	})
}

// SetError stores an error description in labels.
func (m *containerdMetadataManager) SetError(ctx context.Context, containerID string, errMsg string) error {
	// Truncate long error messages
	const maxLen = 512
	if len(errMsg) > maxLen {
		errMsg = errMsg[:maxLen-3] + "..."
	}
	return m.updateLabels(ctx, containerID, map[string]string{
		LabelError: errMsg,
	})
}

// GetRecoverableState reads all qemubox labels and returns recoverable state.
func (m *containerdMetadataManager) GetRecoverableState(ctx context.Context, containerID string) (*RecoverableState, error) {
	labels, err := m.getLabels(ctx, containerID)
	if err != nil {
		return nil, err
	}

	// Check if any qemubox labels exist
	if !hasQemuboxLabels(labels) {
		return nil, nil
	}

	state := &RecoverableState{}
	parseVMState(labels, state)
	parseResourceConfig(labels, state)
	parseNetworkInfo(labels, state)
	parseProcessInfo(labels, state)
	parseTimestamps(labels, state)

	return state, nil
}

// hasQemuboxLabels checks if the labels map contains any qemubox-prefixed labels.
func hasQemuboxLabels(labels map[string]string) bool {
	for k := range labels {
		if strings.HasPrefix(k, LabelPrefix) {
			return true
		}
	}
	return false
}

// parseVMState extracts VM state from labels into RecoverableState.
func parseVMState(labels map[string]string, state *RecoverableState) {
	if v, ok := labels[LabelVMState]; ok {
		state.VMState = VMState(v)
	}
	if v, ok := labels[LabelVMCID]; ok {
		if cid, err := strconv.ParseUint(v, 10, 32); err == nil {
			state.VMCID = uint32(cid)
		}
	}
	if v, ok := labels[LabelVMStateDir]; ok {
		state.StateDir = v
	}
	if v, ok := labels[LabelVMPID]; ok {
		if pid, err := strconv.Atoi(v); err == nil {
			state.VMPID = pid
		}
	}
	if v, ok := labels[LabelError]; ok {
		state.Error = v
	}
}

// parseResourceConfig extracts resource configuration from labels into RecoverableState.
func parseResourceConfig(labels map[string]string, state *RecoverableState) {
	state.ResourceConfig = &vm.VMResourceConfig{}
	if v, ok := labels[LabelBootCPUs]; ok {
		state.ResourceConfig.BootCPUs, _ = strconv.Atoi(v)
	}
	if v, ok := labels[LabelMaxCPUs]; ok {
		state.ResourceConfig.MaxCPUs, _ = strconv.Atoi(v)
	}
	if v, ok := labels[LabelMemorySize]; ok {
		state.ResourceConfig.MemorySize, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := labels[LabelMemoryHotplugSize]; ok {
		state.ResourceConfig.MemoryHotplugSize, _ = strconv.ParseInt(v, 10, 64)
	}
	if v, ok := labels[LabelMemorySlots]; ok {
		state.ResourceConfig.MemorySlots, _ = strconv.Atoi(v)
	}
}

// parseNetworkInfo extracts network information from labels into RecoverableState.
func parseNetworkInfo(labels map[string]string, state *RecoverableState) {
	if v, ok := labels[LabelNetworkIP]; ok {
		state.NetworkIP = net.ParseIP(v)
	}
	if v, ok := labels[LabelNetworkGateway]; ok {
		state.NetworkGateway = net.ParseIP(v)
	}
	if v, ok := labels[LabelNetworkNetmask]; ok {
		state.NetworkNetmask = v
	}
	if v, ok := labels[LabelNetworkMAC]; ok {
		state.NetworkMAC = v
	}
	if v, ok := labels[LabelNetworkTAP]; ok {
		state.NetworkTAP = v
	}
	if v, ok := labels[LabelNetworkNetns]; ok {
		state.NetworkNetns = v
	}
	if v, ok := labels[LabelNetworkDNS]; ok && v != "" {
		state.NetworkDNS = strings.Split(v, ",")
	}
	if v, ok := labels[LabelNetworkInterface]; ok {
		state.NetworkInterface = v
	}
}

// parseProcessInfo extracts process information from labels into RecoverableState.
func parseProcessInfo(labels map[string]string, state *RecoverableState) {
	if v, ok := labels[LabelInitPID]; ok {
		if pid, err := strconv.ParseUint(v, 10, 32); err == nil {
			state.InitPID = uint32(pid)
		}
	}
	if v, ok := labels[LabelInitTerminal]; ok {
		state.InitTerminal, _ = strconv.ParseBool(v)
	}
}

// parseTimestamps extracts timestamp information from labels into RecoverableState.
func parseTimestamps(labels map[string]string, state *RecoverableState) {
	if v, ok := labels[LabelCreatedAt]; ok {
		state.CreatedAt = v
	}
	if v, ok := labels[LabelStartedAt]; ok {
		state.StartedAt = v
	}
}

// ClearLabels removes all qemubox-prefixed labels from the container.
func (m *containerdMetadataManager) ClearLabels(ctx context.Context, containerID string) error {
	labels, err := m.getLabels(ctx, containerID)
	if err != nil {
		return err
	}

	// Collect qemubox labels to clear (set to empty string)
	clearLabels := make(map[string]string)
	for key := range labels {
		if strings.HasPrefix(key, LabelPrefix) {
			clearLabels[key] = ""
		}
	}

	if len(clearLabels) == 0 {
		return nil
	}

	return m.updateLabels(ctx, containerID, clearLabels)
}

// Close releases resources.
func (m *containerdMetadataManager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// updateLabels updates labels on a container using SetLabels which merges with existing.
func (m *containerdMetadataManager) updateLabels(ctx context.Context, containerID string, labels map[string]string) error {
	container, err := m.client.LoadContainer(ctx, containerID)
	if err != nil {
		return fmt.Errorf("load container %s: %w", containerID, err)
	}

	_, err = container.SetLabels(ctx, labels)
	if err != nil {
		return fmt.Errorf("set labels on container %s: %w", containerID, err)
	}

	return nil
}

// getLabels retrieves all labels from a container.
func (m *containerdMetadataManager) getLabels(ctx context.Context, containerID string) (map[string]string, error) {
	container, err := m.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("load container %s: %w", containerID, err)
	}

	return container.Labels(ctx)
}
