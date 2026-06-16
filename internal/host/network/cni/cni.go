//go:build linux

// Package cni manages CNI-based networking for VMs.
package cni

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/containerd/log"
	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"golang.org/x/sys/unix"
)

// cniLockPath is a host-wide lock file that serializes CNI plugin execution
// across all spinbox shim processes.
const cniLockPath = "/run/spinbox/cni.lock"

// withCNIHostLock runs fn while holding an exclusive, host-wide file lock.
//
// Each container runs in its own shim process, but CNI plugins (bridge,
// firewall) mutate shared host state (iptables chains, bridges). Concurrent
// ADD/DEL from different shim processes race on the kernel xtables lock and
// fail intermittently — e.g. the firewall plugin aborting an ADD with an empty
// error. Serializing the plugin chain across processes removes that race.
//
// The lock is advisory (flock) and released when fn returns. It must NOT be
// acquired recursively: a single shim goroutine that re-entered it would block
// forever waiting on its own lock, so internal cleanup paths call the
// *Locked helpers directly.
func withCNIHostLock(_ context.Context, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(cniLockPath), 0o755); err != nil {
		return fmt.Errorf("create CNI lock dir: %w", err)
	}
	f, err := os.OpenFile(cniLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open CNI lock: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("acquire CNI lock: %w", err)
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	return fn()
}

// CNIManager manages CNI plugin execution for VM networking.
type CNIManager struct {
	confDir string
	binDir  string

	// CNI library instance
	cniConfig libcni.CNI

	// Cached network configuration (protected by netConfMu)
	netConf   *libcni.NetworkConfigList
	netConfMu sync.RWMutex
}

// NewCNIManager creates a new CNI manager.
// It will auto-discover and cache CNI network configuration from confDir.
func NewCNIManager(confDir, binDir string) (*CNIManager, error) {
	if confDir == "" {
		return nil, fmt.Errorf("CNI conf directory cannot be empty")
	}
	if binDir == "" {
		return nil, fmt.Errorf("CNI bin directory cannot be empty")
	}

	m := &CNIManager{
		confDir:   confDir,
		binDir:    binDir,
		cniConfig: libcni.NewCNIConfig([]string{binDir}, nil),
	}

	// Load and cache the configuration at startup
	if err := m.loadAndCacheConfig(); err != nil {
		return nil, fmt.Errorf("failed to load initial CNI config: %w", err)
	}

	return m, nil
}

// Reload reloads the CNI network configuration from disk.
// This can be called to pick up configuration changes without restarting.
func (m *CNIManager) Reload() error {
	return m.loadAndCacheConfig()
}

// getNetworkConfig returns the cached network configuration.
// Returns an error if no configuration is cached.
func (m *CNIManager) getNetworkConfig() (*libcni.NetworkConfigList, error) {
	m.netConfMu.RLock()
	defer m.netConfMu.RUnlock()

	if m.netConf == nil {
		return nil, fmt.Errorf("no CNI configuration loaded")
	}
	return m.netConf, nil
}

// loadAndCacheConfig loads the network configuration from disk and caches it.
func (m *CNIManager) loadAndCacheConfig() error {
	netConf, err := m.loadNetworkConfigFromDisk()
	if err != nil {
		return err
	}

	m.netConfMu.Lock()
	m.netConf = netConf
	m.netConfMu.Unlock()

	return nil
}

// Setup executes the CNI plugin chain to configure networking for a VM.
// It returns a CNIResult containing the TAP device name and network configuration.
//
// Errors returned are wrapped with classification. Use errors.Is() to check:
//   - cni.ErrResourceConflict: veth/IP already exists (orphaned from previous run)
//   - cni.ErrIPAMExhausted: no IPs available in pool
//   - cni.ErrTAPNotCreated: tc-redirect-tap plugin didn't create TAP device
func (m *CNIManager) Setup(ctx context.Context, vmID string, netns string) (*CNIResult, error) {
	var result *CNIResult
	err := withCNIHostLock(ctx, func() error {
		var setupErr error
		result, setupErr = m.setupLocked(ctx, vmID, netns)
		return setupErr
	})
	return result, err
}

// setupLocked performs the CNI ADD. The caller must hold the host CNI lock.
func (m *CNIManager) setupLocked(ctx context.Context, vmID string, netns string) (*CNIResult, error) {
	// Get cached network configuration
	netConfList, err := m.getNetworkConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get CNI network config: %w", err)
	}

	// Execute CNI plugin chain
	result, err := m.execPluginChain(ctx, vmID, netns, netConfList)
	if err != nil {
		// Classify the error for callers to handle appropriately
		return nil, ClassifyError(ctx, "ADD", netConfList.Name, err)
	}
	log.G(ctx).WithFields(log.Fields{
		"net":        netConfList.Name,
		"plugins":    len(netConfList.Plugins),
		"interfaces": len(result.Interfaces),
	}).Debug("CNI plugin chain completed")

	// Parse the result
	cniResult, err := ParseCNIResultWithNetNS(result, netns)
	if err != nil {
		// Clean up on parse failure - log teardown errors but return parse error.
		// Use teardownLocked: the host CNI lock is already held by Setup.
		if teardownErr := m.teardownLocked(ctx, vmID, netns); teardownErr != nil {
			log.G(ctx).WithError(teardownErr).WithField("vmID", vmID).
				Warn("failed to teardown CNI after parse failure")
		}
		return nil, fmt.Errorf("failed to parse CNI result: %w", err)
	}

	return cniResult, nil
}

// Teardown executes the CNI plugin chain to clean up networking for a VM.
// Errors are classified - use errors.Is() to check error categories.
func (m *CNIManager) Teardown(ctx context.Context, vmID string, netns string) error {
	return withCNIHostLock(ctx, func() error {
		return m.teardownLocked(ctx, vmID, netns)
	})
}

// teardownLocked performs the CNI DEL. The caller must hold the host CNI lock.
func (m *CNIManager) teardownLocked(ctx context.Context, vmID string, netns string) error {
	// Get cached network configuration
	netConfList, err := m.getNetworkConfig()
	if err != nil {
		return fmt.Errorf("failed to get CNI network config: %w", err)
	}

	// Create runtime configuration
	rt := &libcni.RuntimeConf{
		ContainerID: vmID,
		NetNS:       netns,
		IfName:      "eth0",
	}

	// Execute DEL operation
	if err := m.cniConfig.DelNetworkList(ctx, netConfList, rt); err != nil {
		return ClassifyError(ctx, "DEL", netConfList.Name, err)
	}

	return nil
}

// loadNetworkConfigFromDisk loads the CNI network configuration from the conf directory.
// It auto-discovers the first available .conflist file (sorted lexicographically).
// This is called internally by loadAndCacheConfig; callers should use getNetworkConfig.
func (m *CNIManager) loadNetworkConfigFromDisk() (*libcni.NetworkConfigList, error) {
	// Get all CNI config files from the directory
	files, err := libcni.ConfFiles(m.confDir, []string{".conflist", ".conf"})
	if err != nil {
		return nil, fmt.Errorf("failed to read CNI config files from %s: %w", m.confDir, err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no CNI configuration files found in %s", m.confDir)
	}

	// Files are returned sorted lexicographically, use the first one
	// This follows standard CNI practice where files are named like:
	// 10-mynet.conflist, 20-othernet.conflist, etc.
	confFile := files[0]

	// Load the network configuration
	netConfList, err := libcni.ConfListFromFile(confFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load CNI config from %s: %w", confFile, err)
	}
	// Note: No context available here - this is called from both Setup and Teardown
	// Could pass context through if needed, but for now use package logger
	log.L.WithFields(log.Fields{
		"config": confFile,
		"name":   netConfList.Name,
	}).Info("CNI configuration loaded")

	return netConfList, nil
}

// cniAddRetries bounds retries of the CNI ADD operation. Bridge/veth/IPAM
// setup can fail transiently under rapid container churn (a previous
// container's async teardown racing the next ADD); a short retry resolves it.
const (
	cniAddRetries = 3
	cniAddBackoff = 250 * time.Millisecond
)

// execPluginChain executes the CNI plugin chain and returns the result.
func (m *CNIManager) execPluginChain(ctx context.Context, vmID string, netns string, netConfList *libcni.NetworkConfigList) (*current.Result, error) {
	// Create runtime configuration
	rt := &libcni.RuntimeConf{
		ContainerID: vmID,
		NetNS:       netns,
		IfName:      "eth0",
	}

	// Execute ADD operation, retrying transient setup failures.
	var result types.Result
	var err error
	for attempt := 1; attempt <= cniAddRetries; attempt++ {
		if attempt > 1 {
			// A failed ADD may leave partial bridge/veth/IPAM state; DEL
			// before retrying so the next ADD starts clean (CNI ADD/DEL is
			// idempotent).
			if delErr := m.cniConfig.DelNetworkList(ctx, netConfList, rt); delErr != nil {
				log.G(ctx).WithError(delErr).WithField("vmID", vmID).Debug("CNI DEL before ADD retry failed")
			}
			select {
			case <-time.After(cniAddBackoff):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		result, err = m.cniConfig.AddNetworkList(ctx, netConfList, rt)
		if err == nil {
			break
		}
		log.G(ctx).WithError(err).WithFields(log.Fields{
			"vmID":    vmID,
			"attempt": attempt,
		}).Warn("CNI ADD failed; retrying")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to add CNI network after %d attempts: %w", cniAddRetries, err)
	}

	// Convert to current version
	currentResult, err := current.NewResultFromResult(result)
	if err != nil {
		// Clean up on conversion failure - log errors but return conversion error
		if delErr := m.cniConfig.DelNetworkList(ctx, netConfList, rt); delErr != nil {
			log.G(ctx).WithError(delErr).WithField("vmID", vmID).
				Warn("failed to cleanup CNI after result conversion failure")
		}
		return nil, fmt.Errorf("failed to convert CNI result: %w", err)
	}

	return currentResult, nil
}

// GetCNIVersion returns the CNI spec version supported by this implementation.
func (m *CNIManager) GetCNIVersion() string {
	return current.ImplementedSpecVersion
}
