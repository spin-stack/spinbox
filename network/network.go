//go:build linux

package network

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aledbf/beacon/containerd/network/cni"
	"github.com/aledbf/beacon/containerd/network/ipallocator"
	"github.com/aledbf/beacon/containerd/store"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
)

const (
	// Network device names and addressing
	bridgeName      = "beacon0"
	bridgeCIDR      = "10.88.0.1/16"
	bridgeSubnet    = "10.88.0.0/16"
	tapDevicePrefix = "beacon-"
	maxTapNameLen   = 15 // IFNAMSIZ - 1 (Linux interface name limit)

	// Timing constants
	reconcileInterval = 1 * time.Minute

	// NFTables configuration
	tableNameFilter = "beacon_runner_filter"
	chainNameFilter = "beacon_runner_forward"
	tableNameNat    = "beacon_runner_nat"
	chainNamePost   = "beacon_runner_postrouting"

	// NFTables priorities - must be negative to run before UFW (priority 0)
	// Lower values run first in the netfilter pipeline
	filterPriorityBeforeUFW = -20 // Run before UFW's default filter priority
	forwardPriorityPreUFW   = -5  // Run before UFW in forward chain

	// Connection tracking state flags
	ctStateEstablished = 0x2 // ESTABLISHED connection state
	ctStateRelated     = 0x1 // RELATED connection state
	ctStateMask        = ctStateEstablished | ctStateRelated
)

// NetworkMode represents the network management mode.
type NetworkMode string

const (
	// NetworkModeLegacy uses the built-in NetworkManager with BoltDB IP allocation.
	NetworkModeLegacy NetworkMode = "legacy"

	// NetworkModeCNI uses CNI plugin chains for network configuration.
	NetworkModeCNI NetworkMode = "cni"
)

type NetworkConfig struct {
	// Subnet is the network subnet (legacy mode only).
	Subnet string

	// Mode specifies whether to use legacy or CNI mode.
	// Default: NetworkModeLegacy
	Mode NetworkMode

	// CNI-specific configuration (only used when Mode == NetworkModeCNI)

	// CNIConfDir is the directory containing CNI network configuration files.
	// Default: /etc/cni/net.d
	CNIConfDir string

	// CNIBinDir is the directory containing CNI plugin binaries.
	// Default: /opt/cni/bin
	CNIBinDir string

	// CNINetworkName is the name of the CNI network to use.
	// Default: beacon-net
	CNINetworkName string
}

// LoadNetworkConfig loads network configuration from environment variables.
//
// Environment variables:
//   - BEACON_CNI_MODE=1: Enable CNI mode (default: legacy mode)
//   - BEACON_CNI_CONF_DIR: CNI configuration directory (default: /etc/cni/net.d)
//   - BEACON_CNI_BIN_DIR: CNI plugin binary directory (default: /opt/cni/bin)
//   - BEACON_CNI_NETWORK: CNI network name (default: beacon-net)
//
// In legacy mode, uses the default subnet (10.88.0.0/16).
// In CNI mode, the subnet is determined by the CNI IPAM plugin.
func LoadNetworkConfig() NetworkConfig {
	cfg := NetworkConfig{
		Subnet:         bridgeSubnet, // Default subnet for legacy mode
		Mode:           NetworkModeLegacy,
		CNIConfDir:     "/etc/cni/net.d",
		CNIBinDir:      "/opt/cni/bin",
		CNINetworkName: "beacon-net",
	}

	// Check if CNI mode is enabled
	if os.Getenv("BEACON_CNI_MODE") == "1" {
		cfg.Mode = NetworkModeCNI
	}

	// Override CNI config directory if specified
	if confDir := os.Getenv("BEACON_CNI_CONF_DIR"); confDir != "" {
		cfg.CNIConfDir = confDir
	}

	// Override CNI bin directory if specified
	if binDir := os.Getenv("BEACON_CNI_BIN_DIR"); binDir != "" {
		cfg.CNIBinDir = binDir
	}

	// Override CNI network name if specified
	if networkName := os.Getenv("BEACON_CNI_NETWORK"); networkName != "" {
		cfg.CNINetworkName = networkName
	}

	return cfg
}

// NetworkInfo holds internal network configuration
type NetworkInfo struct {
	TapName    string `json:"tap_name"`
	BridgeName string `json:"bridge_name"`
	IP         net.IP `json:"ip"`
	Netmask    string `json:"netmask"`
	Gateway    net.IP `json:"gateway"`
}

// Environment represents a VM/container network environment
type Environment struct {
	// Id is the unique identifier (container ID or VM ID)
	Id string

	// NetworkInfo contains allocated network configuration
	// Set after EnsureNetworkResources() succeeds
	NetworkInfo *NetworkInfo
}

// NetworkManagerInterface defines the interface for network management operations
// that can be implemented by NetworkManager or mocked for testing
type NetworkManagerInterface interface {
	// Core lifecycle methods
	Close() error

	// Network resource management
	EnsureNetworkResources(env *Environment) error
	ReleaseNetworkResources(env *Environment) error
}

type NetworkManager struct {
	config             NetworkConfig
	networkConfigStore boltstore.Store[NetworkConfig]
	ipAllocator        ipallocator.IPAllocator
	mu                 sync.RWMutex

	ctx        context.Context
	cancelFunc context.CancelFunc

	// Legacy mode fields
	netOp           NetworkOperator
	nftOp           NFTablesOperator
	iptablesChecker *IptablesChecker

	// CNI mode fields
	cniManager *cni.CNIManager

	// CNI state storage (maps VM ID to CNI result for cleanup)
	cniResults map[string]*cni.CNIResult
	cniMu      sync.RWMutex
}

func NewNetworkManager(
	config NetworkConfig,
	networkConfigStore boltstore.Store[NetworkConfig],
	ipStore boltstore.Store[ipallocator.IPAllocation],
	moduleChecker ModuleChecker,
	netOp NetworkOperator,
	nftOp NFTablesOperator,
	onPolicyChange func(status PolicyStatus),
) (NetworkManagerInterface, error) {
	ctx, cancel := context.WithCancel(context.Background())

	// Log the network mode
	slog.InfoContext(ctx, "Initializing network manager", "mode", config.Mode)

	// CNI mode initialization
	if config.Mode == NetworkModeCNI {
		return newCNINetworkManager(ctx, cancel, config, networkConfigStore)
	}

	// Legacy mode initialization
	if moduleChecker == nil {
		moduleChecker = DefaultModuleChecker
	}

	if err := checkKernelModules(moduleChecker); err != nil {
		cancel()
		return nil, fmt.Errorf("kernel module check failed: %w", err)
	}

	if networkConfigStore == nil {
		cancel()
		return nil, fmt.Errorf("NetworkConfigStore is required")
	}

	if ipStore == nil {
		cancel()
		return nil, fmt.Errorf("IPAllocationStore is required")
	}

	if netOp == nil {
		netOp = NewDefaultNetworkOperator()
	}

	// Create NFTablesOperator with context for initialization
	if nftOp == nil {
		var err error
		nftOp, err = NewDefaultNFTablesOperator(ctx)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create NFTables operator: %w", err)
		}
	}

	ipAllocator, err := ipallocator.NewBitmapIPAllocator(ipStore, ipallocator.Config{Subnet: config.Subnet})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create IP allocator: %w", err)
	}

	nm := &NetworkManager{
		config:             config,
		networkConfigStore: networkConfigStore,
		ipAllocator:        ipAllocator,
		ctx:                ctx,
		cancelFunc:         cancel,
		netOp:              netOp,
		nftOp:              nftOp,
		iptablesChecker:    NewIptablesChecker(nftOp, onPolicyChange),
	}

	// Initialize network after NFTables is ready
	if err := nm.validateSubnet(); err != nil {
		nm.Close()
		return nil, fmt.Errorf("subnet validation failed: %w", err)
	}

	if err := nm.initializeNetwork(); err != nil {
		nm.Close()
		return nil, fmt.Errorf("failed to initialize network: %w", err)
	}

	// Start the reconciler
	go nm.runReconciler()

	return nm, nil
}

func (nm *NetworkManager) Close() error {
	if nm.cancelFunc != nil {
		nm.cancelFunc()
	}

	// CNI mode cleanup
	if nm.config.Mode == NetworkModeCNI {
		// CNI resources are cleaned up per-VM via ReleaseNetworkResources
		// No global cleanup needed for CNI mode
		return nil
	}

	// Legacy mode cleanup
	// Cleanup iptables FORWARD rules
	if nm.iptablesChecker != nil {
		if err := nm.iptablesChecker.CleanupBeaconForwardRules(nm.ctx); err != nil {
			slog.WarnContext(nm.ctx, "Failed to cleanup iptables FORWARD rules", "error", err)
		}
	}

	if err := nm.cleanupNFTables(); err != nil {
		return fmt.Errorf("failed to cleanup nftables: %w", err)
	}

	if nm.nftOp != nil {
		nm.nftOp.Close()
	}
	return nil
}

func (nm *NetworkManager) validateSubnet() error {
	_, err := nm.netOp.ParseAddr(nm.config.Subnet)
	if err != nil {
		return fmt.Errorf("invalid subnet %s: %w", nm.config.Subnet, err)
	}

	links, err := nm.netOp.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %w", err)
	}

	for _, link := range links {
		name := link.Attrs().Name
		if name == bridgeName {
			continue
		}

		_, err := nm.netOp.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			slog.WarnContext(nm.ctx, "Failed to get addresses for interface", "device", name, "error", err)
			if err := nm.cleanupTapDevice(name); err != nil {
				slog.WarnContext(nm.ctx, "Failed to remove orphaned tap device", "device", name, "error", err)
			}
		}
	}

	return nil
}

// EnsureNetworkResources allocates and configures network resources for an environment.
// If network resources were previously allocated but the tap device is missing (e.g. after host reboot),
// it will release and re-allocate the resources.
//
// The function:
// 1. Checks if network resources are already allocated and the tap device exists
// 2. If tap device is missing, releases and re-allocates resources
// 3. Allocates an IP address for the environment
// 4. Creates a tap device and attaches it to the bridge
//
// The function is thread-safe and uses a mutex to prevent concurrent modifications.
// If any step fails, it will clean up any partially allocated resources.
func (nm *NetworkManager) EnsureNetworkResources(env *Environment) error {
	// Route to CNI implementation if in CNI mode
	if nm.config.Mode == NetworkModeCNI {
		return nm.ensureNetworkResourcesCNI(env)
	}

	// Legacy mode implementation below
	// Quick check with read lock
	nm.mu.RLock()
	if env.NetworkInfo != nil && env.NetworkInfo.TapName != "" {
		tapName := env.NetworkInfo.TapName
		nm.mu.RUnlock()

		// Check if tap device still exists without holding the lock
		_, err := nm.netOp.LinkByName(tapName)
		if err == nil {
			slog.DebugContext(nm.ctx, "Tap device still exists, nothing to do", "tap", tapName, "envID", env.Id)
			return nil
		}

		// Need to re-allocate - acquire write lock
		nm.mu.Lock()
		defer nm.mu.Unlock()

		// Double-check after acquiring write lock (another goroutine might have fixed it)
		if env.NetworkInfo != nil && env.NetworkInfo.TapName != "" {
			_, err := nm.netOp.LinkByName(env.NetworkInfo.TapName)
			if err == nil {
				return nil
			}
		}

		// Release and re-allocate
		slog.InfoContext(nm.ctx, "Releasing and re-allocating network resources, tap device not found (host reboot?)", "tap", tapName, "envID", env.Id, "error", err)
		if err := nm.releaseNetworkResources(env); err != nil {
			return fmt.Errorf("failed to release previous network resources: %w", err)
		}
	} else {
		nm.mu.RUnlock()
		// Acquire write lock for allocation
		nm.mu.Lock()
		defer nm.mu.Unlock()
	}

	tapName := generateTapName(env.Id)

	ip, netmask, gateway, err := nm.ipAllocator.AllocateIP(nm.ctx, env.Id)
	if err != nil {
		return fmt.Errorf("failed to allocate IP: %w", err)
	}

	success := false
	defer func() {
		if !success {
			// Use background context for cleanup to ensure it completes even if nm.ctx is cancelled
			if err := nm.ipAllocator.ReleaseIP(context.Background(), ip.String()); err != nil {
				slog.ErrorContext(nm.ctx, "Failed to release IP during cleanup",
					"ip", ip.String(), "envID", env.Id, "error", err)
			}
		}
	}()

	if err := nm.createTapDevice(tapName); err != nil {
		return fmt.Errorf("failed to create tap device: %w", err)
	}

	defer func() {
		if !success {
			if err := nm.cleanupTapDevice(tapName); err != nil {
				slog.ErrorContext(nm.ctx, "Failed to cleanup tap device during error recovery",
					"tap", tapName, "envID", env.Id, "error", err)
			}
		}
	}()

	if err := nm.attachTapToBridge(tapName); err != nil {
		return fmt.Errorf("failed to attach tap to bridge: %w", err)
	}

	if err := nm.networkConfigStore.Set(nm.ctx, env.Id, &NetworkConfig{
		Subnet: ip.String(),
	}); err != nil {
		return fmt.Errorf("failed to store network config: %w", err)
	}

	env.NetworkInfo = &NetworkInfo{
		TapName:    tapName,
		BridgeName: bridgeName,
		IP:         ip,
		Netmask:    netmask,
		Gateway:    gateway,
	}

	success = true
	return nil
}

func (nm *NetworkManager) ReleaseNetworkResources(env *Environment) error {
	// Route to CNI implementation if in CNI mode
	if nm.config.Mode == NetworkModeCNI {
		return nm.releaseNetworkResourcesCNI(env)
	}

	// Legacy mode implementation
	nm.mu.Lock()
	defer nm.mu.Unlock()

	return nm.releaseNetworkResources(env)
}

func (nm *NetworkManager) releaseNetworkResources(env *Environment) error {
	var tapName string
	var ip string

	if env.NetworkInfo != nil {
		tapName = env.NetworkInfo.TapName
		if env.NetworkInfo.IP != nil {
			ip = env.NetworkInfo.IP.String()
		}
	} else {
		// Try to reconstruct from ID
		tapName = generateTapName(env.Id)
		// We can't easily know the IP without querying the store, but we can try to look it up
		// However, ipAllocator.ReleaseIP takes the IP string.
		// If we don't have the IP, we might leak it in the allocator, but we can still clean up the TAP device.

		// Try to find the IP from the network config store? No, that stores the subnet.
		// The IP allocator store has the mapping.
		// Let's try to find the IP allocated to this ID.
		// Since ipAllocator interface doesn't expose "GetIP(id)", we might need to extend it or just skip IP release if unknown.
		// But wait, ipAllocator.AllocateIP returns an error if already allocated.
		// Actually, we can just proceed with TAP cleanup which is the most visible issue.
	}

	var errs []error

	if tapName != "" {
		if err := nm.cleanupTapDevice(tapName); err != nil {
			errs = append(errs, fmt.Errorf("failed to cleanup tap device: %w", err))
		}
	}

	// If we have the IP, release it.
	// If we don't, try to release by ID using the allocator's scan capability.
	if ip != "" {
		if err := nm.ipAllocator.ReleaseIP(nm.ctx, ip); err != nil {
			errs = append(errs, fmt.Errorf("failed to release IP: %w", err))
		}
	} else {
		if err := nm.ipAllocator.ReleaseByHostID(nm.ctx, env.Id); err != nil {
			errs = append(errs, fmt.Errorf("failed to release IPs by host ID: %w", err))
		}
	}

	if err := nm.networkConfigStore.Delete(nm.ctx, env.Id); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete network config: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("failed to release network resources for %s: %w", env.Id, errors.Join(errs...))
	}

	return nil
}

func (nm *NetworkManager) createTapDevice(name string) error {
	slog.DebugContext(nm.ctx, "Creating tap device", "name", name)
	tap := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: name},
		Mode:      netlink.TUNTAP_MODE_TAP,
		Flags:     netlink.TUNTAP_DEFAULTS,
	}

	if err := nm.netOp.LinkAdd(tap); err != nil {
		return fmt.Errorf("failed to create tap device: %w", err)
	}

	if err := nm.netOp.LinkSetUp(tap); err != nil {
		nm.netOp.LinkDel(tap)
		return fmt.Errorf("failed to set tap device up: %w", err)
	}

	return nil
}

func (nm *NetworkManager) attachTapToBridge(tapName string) error {
	slog.DebugContext(nm.ctx, "Attaching tap to bridge", "tap", tapName)
	tap, err := nm.netOp.LinkByName(tapName)
	if err != nil {
		return fmt.Errorf("failed to get tap device: %w", err)
	}

	br, err := nm.netOp.LinkByName(bridgeName)
	if err != nil {
		return fmt.Errorf("failed to get bridge: %w", err)
	}

	if err := nm.netOp.LinkSetMaster(tap, br); err != nil {
		return fmt.Errorf("failed to attach tap to bridge: %w", err)
	}

	return nil
}

func (nm *NetworkManager) cleanupTapDevice(name string) error {
	slog.DebugContext(nm.ctx, "Cleaning up tap device", "name", name)
	tap, err := nm.netOp.LinkByName(name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil
		}
		return fmt.Errorf("failed to get tap device: %w", err)
	}

	if err := nm.netOp.LinkDel(tap); err != nil {
		return fmt.Errorf("failed to delete tap device: %w", err)
	}

	return nil
}

func (nm *NetworkManager) initializeNetwork() error {
	// Check if bridge exists
	br, err := nm.netOp.LinkByName(bridgeName)
	if err != nil {
		// Create new bridge if it doesn't exist
		br = &netlink.Bridge{
			LinkAttrs: netlink.LinkAttrs{
				Name: bridgeName,
				MTU:  1500,
			},
		}
		if err := nm.netOp.LinkAdd(br); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}
	}

	// Configure bridge IP
	addr, err := nm.netOp.ParseAddr(bridgeCIDR)
	if err != nil {
		return fmt.Errorf("failed to parse bridge CIDR: %w", err)
	}

	// Ensure bridge has correct IP
	addrs, err := nm.netOp.AddrList(br, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("failed to list bridge addresses: %w", err)
	}

	hasCorrectIP := false
	for _, a := range addrs {
		if a.IPNet.String() == bridgeCIDR {
			hasCorrectIP = true
			break
		}
	}

	if !hasCorrectIP {
		if err := nm.netOp.AddrAdd(br, addr); err != nil {
			return fmt.Errorf("failed to add IP to bridge: %w", err)
		}
	}

	// Ensure bridge is up
	if err := nm.netOp.LinkSetUp(br); err != nil {
		return fmt.Errorf("failed to set bridge up: %w", err)
	}

	// Setup NAT rules
	if err := nm.setupNAT(); err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}

	// Setup nftables
	if err := nm.reconcileNFTables(); err != nil {
		return fmt.Errorf("failed to setup nftables: %w", err)
	}

	return nil
}

func (nm *NetworkManager) setupChain(table *nftables.Table, chainName string, chainType nftables.ChainType, hook *nftables.ChainHook, priority *nftables.ChainPriority) *nftables.Chain {
	chain := &nftables.Chain{
		Name:     chainName,
		Table:    table,
		Type:     chainType,
		Hooknum:  hook,
		Priority: priority,
	}
	nm.nftOp.AddChain(chain)
	return chain
}

func (nm *NetworkManager) setupNAT() error {
	slog.InfoContext(nm.ctx, "Starting NAT setup")

	natTable, err := nm.ensureNATTable()
	if err != nil {
		return err
	}

	postChain, err := nm.ensurePostroutingChain(natTable)
	if err != nil {
		return err
	}

	if err := nm.addMasqueradeRule(natTable, postChain); err != nil {
		return err
	}

	slog.InfoContext(nm.ctx, "NAT setup completed successfully")
	return nil
}

func (nm *NetworkManager) ensureNATTable() (*nftables.Table, error) {
	slog.InfoContext(nm.ctx, "Creating NAT table")
	natTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   tableNameNat,
	}

	if err := nm.nftOp.AddTable(natTable); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to create NAT table", "error", err)
		return nil, fmt.Errorf("failed to create NAT table: %w", err)
	}

	if err := nm.nftOp.Flush(); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to flush after NAT table creation", "error", err)
		return nil, fmt.Errorf("failed to flush after NAT table creation: %w", err)
	}

	return natTable, nil
}

func (nm *NetworkManager) ensurePostroutingChain(natTable *nftables.Table) (*nftables.Chain, error) {
	slog.InfoContext(nm.ctx, "Creating NAT postrouting chain")
	postChain := nm.setupChain(natTable, chainNamePost, nftables.ChainTypeNAT,
		nftables.ChainHookPostrouting, nftables.ChainPriorityNATSource)

	if err := nm.nftOp.Flush(); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to flush after NAT chain creation", "error", err)
		return nil, fmt.Errorf("failed to flush after NAT chain creation: %w", err)
	}

	// Verify chain creation
	chains, err := nm.nftOp.GetChains(natTable)
	if err != nil {
		slog.ErrorContext(nm.ctx, "Failed to get chains", "error", err)
		return nil, fmt.Errorf("failed to verify chain creation: %w", err)
	}

	for _, chain := range chains {
		if chain.Name == chainNamePost {
			return postChain, nil
		}
	}

	return nil, fmt.Errorf("NAT chain %s was not created successfully", chainNamePost)
}

func (nm *NetworkManager) addMasqueradeRule(natTable *nftables.Table, postChain *nftables.Chain) error {
	_, network, err := net.ParseCIDR(bridgeSubnet)
	if err != nil {
		return fmt.Errorf("failed to parse bridge subnet: %w", err)
	}

	slog.InfoContext(nm.ctx, "Adding masquerade rule for subnet", "subnet", bridgeSubnet)
	nm.nftOp.AddRule(&nftables.Rule{
		Table: natTable,
		Chain: postChain,
		Exprs: []expr.Any{
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12, // Source IP offset
				Len:          4,
			},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           network.Mask,
				Xor:            []byte{0, 0, 0, 0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     network.IP.To4(),
			},
			&expr.Counter{},
			&expr.Masq{},
		},
	})

	if err := nm.nftOp.Flush(); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to flush masquerade rule", "error", err)
		return fmt.Errorf("failed to flush masquerade rule: %w", err)
	}

	return nil
}

func (nm *NetworkManager) runReconciler() {
	ticker := time.NewTicker(reconcileInterval)
	defer ticker.Stop()

	// Run an initial check
	if err := nm.reconcile(); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to reconcile network state", "error", err)
	}

	for {
		select {
		case <-nm.ctx.Done():
			return
		case <-ticker.C:
			if err := nm.reconcile(); err != nil {
				slog.ErrorContext(nm.ctx, "Failed to reconcile network state", "error", err)
			}
		}
	}
}

func (nm *NetworkManager) reconcile() error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	if err := nm.cleanupOrphanedTaps(); err != nil {
		return fmt.Errorf("failed to cleanup orphaned taps: %w", err)
	}

	if err := nm.reconcileNFTables(); err != nil {
		return fmt.Errorf("failed to reconcile nftables: %w", err)
	}

	// Check iptables forward policy
	if nm.iptablesChecker != nil {
		if err := nm.iptablesChecker.CheckForwardPolicy(nm.ctx); err != nil {
			slog.WarnContext(nm.ctx, "Iptables forward policy check failed during reconciliation", "error", err)
			// Don't return error as this shouldn't stop other reconciliation tasks
		}
	}

	return nil
}

func (nm *NetworkManager) cleanupOrphanedTaps() error {
	links, err := nm.netOp.LinkList()
	if err != nil {
		return fmt.Errorf("failed to list network interfaces: %w", err)
	}

	activeVMs := make(map[string]bool)
	err = nm.networkConfigStore.Scan(nm.ctx, "", func(id string, networkConfig *NetworkConfig) error {
		activeVMs[generateTapName(id)] = true
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to list network configs: %w", err)
	}

	var errs []error
	for _, link := range links {
		interfaceName := link.Attrs().Name
		if !strings.HasPrefix(interfaceName, tapDevicePrefix) || strings.HasPrefix(interfaceName, bridgeName) {
			continue
		}

		if !activeVMs[interfaceName] {
			slog.InfoContext(nm.ctx, "Removing orphaned tap device", "device", interfaceName)

			if err := nm.cleanupTapDevice(interfaceName); err != nil {
				errs = append(errs, fmt.Errorf("failed to cleanup tap device %s: %w", interfaceName, err))
				continue
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered errors during tap cleanup: %w", errors.Join(errs...))
	}

	return nil
}

func (nm *NetworkManager) reconcileNFTables() error {
	slog.DebugContext(nm.ctx, "Starting NFTables reconciliation")

	//
	// 1) FILTER TABLE & FORWARD CHAIN
	//
	filterTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   tableNameFilter,
	}

	// Ensure the filter table exists
	slog.DebugContext(nm.ctx, "Ensuring filter table exists", "table", filterTable.Name)
	if err := nm.nftOp.AddTable(filterTable); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to ensure filter table", "error", err)
		return fmt.Errorf("failed to ensure filter table: %w", err)
	}

	// Get existing chains
	slog.DebugContext(nm.ctx, "Fetching filter chains", "table", filterTable.Name)
	filterChains, err := nm.nftOp.GetChains(filterTable)
	if err != nil {
		slog.ErrorContext(nm.ctx, "Failed to list chains for filter table", "error", err)
		return fmt.Errorf("failed to list chains for filter table: %w", err)
	}

	// Find or create our forward chain
	var forwardChain *nftables.Chain
	for _, ch := range filterChains {
		if ch.Name == chainNameFilter {
			forwardChain = ch
			break
		}
	}

	// Create forward chain if missing with high priority
	if forwardChain == nil {
		slog.InfoContext(nm.ctx, "Forward chain not found; creating", "chain", chainNameFilter)
		priority := nftables.ChainPriority(filterPriorityBeforeUFW)
		forwardChain = &nftables.Chain{
			Name:     chainNameFilter,
			Table:    filterTable,
			Type:     nftables.ChainTypeFilter,
			Hooknum:  nftables.ChainHookForward,
			Priority: &priority,
		}
		if err := nm.nftOp.AddChain(forwardChain); err != nil {
			slog.ErrorContext(nm.ctx, "Failed to create forward chain", "error", err)
			return fmt.Errorf("failed to create forward chain: %w", err)
		}
	}

	// Setup forwarding rules with the default interface
	if err := nm.setupForwardingRules(filterTable, forwardChain); err != nil {
		return fmt.Errorf("failed to setup forwarding rules: %w", err)
	}

	//
	// 2) NAT TABLE & POSTROUTING CHAIN
	//
	natTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   tableNameNat,
	}

	// Ensure NAT table exists
	slog.DebugContext(nm.ctx, "Ensuring NAT table exists", "table", natTable.Name)
	if err := nm.nftOp.AddTable(natTable); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to ensure NAT table", "error", err)
		return fmt.Errorf("failed to ensure NAT table: %w", err)
	}

	// Get NAT chains
	slog.DebugContext(nm.ctx, "Fetching NAT chains", "table", natTable.Name)
	natChains, err := nm.nftOp.GetChains(natTable)
	if err != nil {
		slog.ErrorContext(nm.ctx, "Failed to get NAT chains", "error", err)
		return fmt.Errorf("failed to get NAT chains: %w", err)
	}

	// Find or create postrouting chain
	var postChain *nftables.Chain
	for _, ch := range natChains {
		if ch.Name == chainNamePost {
			postChain = ch
			break
		}
	}

	if postChain == nil {
		slog.InfoContext(nm.ctx, "NAT postrouting chain not found; creating", "chain", chainNamePost)
		postChain = &nftables.Chain{
			Name:     chainNamePost,
			Table:    natTable,
			Type:     nftables.ChainTypeNAT,
			Hooknum:  nftables.ChainHookPostrouting,
			Priority: nftables.ChainPriorityNATSource,
		}
		if err := nm.nftOp.AddChain(postChain); err != nil {
			slog.ErrorContext(nm.ctx, "Failed to create NAT postrouting chain", "error", err)
			return fmt.Errorf("failed to create NAT postrouting chain: %w", err)
		}
	}

	// Check existing NAT rules
	slog.DebugContext(nm.ctx, "Checking NAT rules", "chain", chainNamePost)
	existingNATRules, err := nm.nftOp.GetRules(natTable, postChain)
	if err != nil {
		slog.ErrorContext(nm.ctx, "Failed to get NAT rules", "error", err)
		return fmt.Errorf("failed to get NAT rules: %w", err)
	}

	// Add NAT rules if none exist
	if len(existingNATRules) == 0 {
		slog.InfoContext(nm.ctx, "No existing NAT rules found; creating masquerade rule")

		_, network, err := net.ParseCIDR(bridgeSubnet)
		if err != nil {
			return fmt.Errorf("failed to parse bridge subnet: %w", err)
		}

		// Add masquerade rule for bridge network
		nm.nftOp.AddRule(&nftables.Rule{
			Table: natTable,
			Chain: postChain,
			Exprs: []expr.Any{
				// Match source IP from bridge network
				&expr.Payload{
					DestRegister: 1,
					Base:         expr.PayloadBaseNetworkHeader,
					Offset:       12,
					Len:          4,
				},
				&expr.Bitwise{
					SourceRegister: 1,
					DestRegister:   1,
					Len:            4,
					Mask:           network.Mask,
					Xor:            []byte{0, 0, 0, 0},
				},
				&expr.Cmp{
					Op:       expr.CmpOpEq,
					Register: 1,
					Data:     network.IP.To4(),
				},
				&expr.Counter{},
				&expr.Masq{},
			},
		})
	}

	//
	// 3) FINAL FLUSH
	//
	slog.DebugContext(nm.ctx, "Flushing NFTables changes")
	if err := nm.nftOp.Flush(); err != nil {
		slog.ErrorContext(nm.ctx, "Failed to flush NFTables changes", "error", err)
		return fmt.Errorf("failed to flush NFTables changes: %w", err)
	}

	slog.DebugContext(nm.ctx, "NFTables reconciliation completed successfully")
	return nil
}

func (nm *NetworkManager) cleanupNFTables() error {
	// Delete filter table and chain
	filterTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   tableNameFilter,
	}

	filterChain := &nftables.Chain{
		Name:  chainNameFilter,
		Table: filterTable,
	}

	// Delete NAT table and chain
	natTable := &nftables.Table{
		Family: nftables.TableFamilyIPv4,
		Name:   tableNameNat,
	}

	postChain := &nftables.Chain{
		Name:  chainNamePost,
		Table: natTable,
	}

	nm.nftOp.DelChain(filterChain)
	nm.nftOp.DelTable(filterTable)
	nm.nftOp.DelChain(postChain)
	nm.nftOp.DelTable(natTable)

	return nm.nftOp.Flush()
}

func generateTapName(envId string) string {
	hash := sha256.Sum256([]byte(envId))
	name := fmt.Sprintf("%s%x", tapDevicePrefix, hash[:4])
	if len(name) > maxTapNameLen {
		panic(fmt.Sprintf("tap name %q exceeds interface name limit of %d characters", name, maxTapNameLen))
	}
	return name
}

func checkNFTablesReadiness(ctx context.Context) error {
	maxAttempts := 30
	backoff := 100 * time.Millisecond

	for attempt := range maxAttempts {
		// Try to create a temporary connection to verify subsystem readiness
		conn, err := nftables.New()
		if err == nil {
			// Test if we can actually communicate
			if _, err := conn.ListTables(); err == nil {
				conn.CloseLasting()
				return nil
			}
			conn.CloseLasting()
		}

		slog.InfoContext(ctx, "Waiting for NFTables subsystem to be ready", "attempt", attempt, "maxAttempts", maxAttempts, "error", err)

		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for NFTables: %w", ctx.Err())
		case <-time.After(backoff):
			// Exponential backoff with max of 2 seconds
			backoff = time.Duration(math.Min(float64(backoff)*1.5, float64(2*time.Second)))
		}
	}

	return fmt.Errorf("NFTables subsystem failed to become ready after %d attempts", maxAttempts)
}

// ifname returns a null-padded 16-byte interface name for nftables matching
func ifname(name string) []byte {
	b := make([]byte, 16)
	copy(b, []byte(name))
	return b
}

func (nm *NetworkManager) setupForwardingRules(filterTable *nftables.Table, forwardChain *nftables.Chain) error {
	// Clear existing rules in our chain
	if err := nm.nftOp.FlushChain(forwardChain); err != nil {
		return fmt.Errorf("failed to flush forward chain: %w", err)
	}

	// Recreate the forwarding chain with correct priority
	priority := nftables.ChainPriority(forwardPriorityPreUFW)
	forwardChain = &nftables.Chain{
		Name:     chainNameFilter,
		Table:    filterTable,
		Type:     nftables.ChainTypeFilter,
		Hooknum:  nftables.ChainHookForward,
		Priority: &priority,
	}

	// 1. Allow established/related connections
	nm.nftOp.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: forwardChain,
		Exprs: []expr.Any{
			&expr.Ct{
				Key:      expr.CtKeySTATE,
				Register: 1,
			},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           []byte{0x0, 0x0, 0x0, ctStateMask},
				Xor:            []byte{0x0, 0x0, 0x0, 0x0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpNeq,
				Register: 1,
				Data:     []byte{0x0, 0x0, 0x0, 0x0},
			},
			&expr.Counter{},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	// 2. Allow all traffic from bridge
	nm.nftOp.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: forwardChain,
		Exprs: []expr.Any{
			// Match traffic from our bridge interface
			&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     ifname(bridgeName),
			},
			&expr.Counter{},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	// 3. Allow all traffic to bridge
	nm.nftOp.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: forwardChain,
		Exprs: []expr.Any{
			// Match traffic to our bridge interface
			&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     ifname(bridgeName),
			},
			&expr.Counter{},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	// 4. Allow all traffic from our subnet as fallback
	nm.nftOp.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: forwardChain,
		Exprs: []expr.Any{
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       12, // Source IP
				Len:          4,
			},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           []byte{255, 255, 0, 0}, // 10.88.0.0/16
				Xor:            []byte{0, 0, 0, 0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     []byte{10, 88, 0, 0},
			},
			&expr.Counter{},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	// 5. Allow all return traffic to our subnet
	nm.nftOp.AddRule(&nftables.Rule{
		Table: filterTable,
		Chain: forwardChain,
		Exprs: []expr.Any{
			&expr.Payload{
				DestRegister: 1,
				Base:         expr.PayloadBaseNetworkHeader,
				Offset:       16, // Destination IP
				Len:          4,
			},
			&expr.Bitwise{
				SourceRegister: 1,
				DestRegister:   1,
				Len:            4,
				Mask:           []byte{255, 255, 0, 0}, // 10.88.0.0/16
				Xor:            []byte{0, 0, 0, 0},
			},
			&expr.Cmp{
				Op:       expr.CmpOpEq,
				Register: 1,
				Data:     []byte{10, 88, 0, 0},
			},
			&expr.Counter{},
			&expr.Verdict{Kind: expr.VerdictAccept},
		},
	})

	return nil
}

// ModuleChecker is a function type that checks for loaded kernel modules
type ModuleChecker func() ([]string, error)

// DefaultModuleChecker reads from /proc/modules
func DefaultModuleChecker() ([]string, error) {
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return nil, fmt.Errorf("failed to read kernel modules: %w", err)
	}
	return strings.Split(string(data), "\n"), nil
}

func checkKernelModules(moduleChecker ModuleChecker) error {
	modules, err := moduleChecker()
	if err != nil {
		return fmt.Errorf("failed to check kernel modules: %w", err)
	}

	requiredModules := map[string]bool{
		"bridge":       false,
		"br_netfilter": false,
		"nf_tables":    false,
	}

	for _, module := range modules {
		for requiredModule := range requiredModules {
			if strings.Contains(module, requiredModule) {
				requiredModules[requiredModule] = true
			}
		}
	}

	var missingModules []string
	for module, found := range requiredModules {
		if !found {
			missingModules = append(missingModules, module)
		}
	}

	if len(missingModules) > 0 {
		return fmt.Errorf("missing required kernel modules: %s. Please run: sudo modprobe %s",
			strings.Join(missingModules, ", "),
			strings.Join(missingModules, " "))
	}

	return nil
}
