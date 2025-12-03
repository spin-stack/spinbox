package task

import (
	"context"
	"fmt"
	"os"

	gocni "github.com/containerd/go-cni"
	"github.com/containerd/log"
)

// CNIManager manages CNI plugin lifecycle and network setup
type CNIManager struct {
	cni gocni.CNI
}

// CNIResult contains the result of CNI network setup
type CNIResult struct {
	// InterfaceName is the primary interface name created by CNI (usually "eth0")
	InterfaceName string
	// TAPName is the TAP device name created by CNI tap plugin (e.g., "tap0")
	TAPName string
	// NetworkNS is the network namespace path
	NetworkNS string
	// IP is the primary IPv4 address assigned (e.g., "10.88.0.5")
	IP string
	// Gateway is the IPv4 gateway (e.g., "10.88.0.1")
	Gateway string
	// Netmask is the network mask (e.g., "255.255.255.0")
	Netmask string
	// DNS servers
	DNS []string
	// MAC is the MAC address for the VM network interface
	MAC string
}

// NewCNIManager creates a new CNI manager
func NewCNIManager(ctx context.Context) (*CNIManager, error) {
	// Detect CNI configuration directory
	cniConfDir := os.Getenv("CNI_CONF_DIR")
	if cniConfDir == "" {
		cniConfDir = "/etc/cni/net.d"
	}

	// Detect CNI binary directory
	cniBinDir := os.Getenv("CNI_BIN_DIR")
	if cniBinDir == "" {
		cniBinDir = "/opt/cni/bin"
	}

	// Check if directories exist
	if _, err := os.Stat(cniConfDir); os.IsNotExist(err) {
		log.G(ctx).WithField("dir", cniConfDir).Warn("CNI config directory does not exist, CNI networking may not work")
	}
	if _, err := os.Stat(cniBinDir); os.IsNotExist(err) {
		log.G(ctx).WithField("dir", cniBinDir).Warn("CNI bin directory does not exist, CNI networking may not work")
	}

	// Initialize CNI library
	log.G(ctx).WithFields(log.Fields{
		"confDir": cniConfDir,
		"binDir":  cniBinDir,
	}).Debug("initializing CNI library")

	cniLib, err := gocni.New(
		gocni.WithPluginDir([]string{cniBinDir}),
		gocni.WithPluginConfDir(cniConfDir),
		gocni.WithInterfacePrefix("eth"),
		gocni.WithMinNetworkCount(1), // At least one network required
	)
	if err != nil {
		log.G(ctx).WithError(err).Error("failed to create CNI library instance")
		return nil, fmt.Errorf("failed to initialize CNI: %w", err)
	}

	log.G(ctx).Debug("CNI library instance created, loading configuration")

	// Load CNI configuration
	if err := cniLib.Load(
		gocni.WithLoNetwork,
		gocni.WithDefaultConf,
	); err != nil {
		log.G(ctx).WithError(err).WithFields(log.Fields{
			"confDir": cniConfDir,
			"binDir":  cniBinDir,
		}).Error("failed to load CNI configuration from directory")
		return nil, fmt.Errorf("failed to load CNI config: %w", err)
	}

	log.G(ctx).WithFields(log.Fields{
		"confDir": cniConfDir,
		"binDir":  cniBinDir,
	}).Info("CNI manager initialized")

	return &CNIManager{
		cni: cniLib,
	}, nil
}

// SetupNetwork sets up networking for a container using CNI
// Returns CNIResult containing the interface name to use for TAP creation
func (cm *CNIManager) SetupNetwork(ctx context.Context, id, netnsPath string) (*CNIResult, error) {
	log.G(ctx).WithFields(log.Fields{
		"id":    id,
		"netns": netnsPath,
	}).Info("setting up CNI network")

	// Setup network using CNI
	log.G(ctx).Debug("calling CNI library Setup()")
	result, err := cm.cni.Setup(ctx, id, netnsPath)
	if err != nil {
		log.G(ctx).WithError(err).Error("CNI library Setup() failed")
		return nil, fmt.Errorf("CNI setup failed: %w", err)
	}

	log.G(ctx).WithField("interfaces", result.Interfaces).Debug("CNI Setup() returned result")

	// Find the primary interface (eth0) and TAP interface created by CNI
	// The tap CNI plugin creates a TAP device in the same namespace
	var primaryIface, tapIface, macAddr string
	for ifName, iface := range result.Interfaces {
		log.G(ctx).WithFields(log.Fields{
			"name":    ifName,
			"sandbox": iface.Sandbox,
		}).Debug("examining CNI interface")

		// Skip loopback
		if ifName == "lo" {
			continue
		}

		// Look for the interface that's in our network namespace
		// The Sandbox field contains the netns path for interfaces inside the netns
		if iface.Sandbox == netnsPath {
			// TAP interfaces typically start with "tap"
			if len(ifName) >= 3 && ifName[:3] == "tap" {
				tapIface = ifName
				// Get MAC address from the TAP interface
				if iface.Mac != "" {
					macAddr = iface.Mac
				}
				log.G(ctx).WithFields(log.Fields{
					"tap": tapIface,
					"mac": macAddr,
				}).Info("found TAP interface created by CNI")
			} else {
				primaryIface = ifName
				log.G(ctx).WithFields(log.Fields{
					"interface": primaryIface,
					"sandbox":   iface.Sandbox,
				}).Info("found primary interface inside network namespace")
			}
		}
	}

	// Fallback to defaults if we didn't find matches
	if primaryIface == "" {
		primaryIface = "eth0"
		log.G(ctx).WithField("interface", primaryIface).Warn("using default interface name, couldn't find interface in netns")
	}
	if tapIface == "" {
		tapIface = "tap0"
		log.G(ctx).WithField("tap", tapIface).Warn("using default TAP name, couldn't find TAP interface in CNI result")
	}

	// Extract IP configuration from CNI result using structured interfaces
	// The go-cni library already parsed and associated IPs with interfaces
	var ip, gateway, netmask string
	var dns []string

	// Get the interface configuration from the structured result
	if ifaceConfig, ok := result.Interfaces[primaryIface]; ok && len(ifaceConfig.IPConfigs) > 0 {
		// Find the first IPv4 configuration
		for _, ipConfig := range ifaceConfig.IPConfigs {
			if ipConfig.IP.To4() != nil {
				// Extract IP address
				ip = ipConfig.IP.String()

				// Extract gateway
				if ipConfig.Gateway != nil {
					gateway = ipConfig.Gateway.String()
				}

				// We need the netmask from the raw result because the structured
				// result only has IP and Gateway, not the full IPNet with mask
				rawResults := result.Raw()
				if len(rawResults) > 0 {
					raw := rawResults[0]
					// Find the matching IP in raw results to get the netmask
					for _, rawIPConfig := range raw.IPs {
						if rawIPConfig.Address.IP.Equal(ipConfig.IP) {
							mask := rawIPConfig.Address.Mask
							netmask = fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
							log.G(ctx).WithFields(log.Fields{
								"ip":      rawIPConfig.Address.IP.String(),
								"netmask": netmask,
							}).Debug("found matching IP in raw results with netmask")
							break
						}
					}

					// If we didn't find a match, use a sensible default based on the IP
					if netmask == "" {
						log.G(ctx).WithField("targetIP", ipConfig.IP.String()).Warn("no matching IP found in raw results, using default /16 netmask")
						// For CNI bridge networks, /16 is a common default (e.g., 10.88.0.0/16)
						// This matches the typical CNI bridge configuration
						netmask = "255.255.0.0"
					}
				}

				log.G(ctx).WithFields(log.Fields{
					"interface": primaryIface,
					"ip":        ip,
					"netmask":   netmask,
					"gateway":   gateway,
				}).Debug("extracted IP config from primary interface")
				break
			}
		}

		if ip == "" {
			log.G(ctx).WithField("interface", primaryIface).Warn("no IPv4 configuration found for primary interface")
		}
	} else {
		log.G(ctx).WithField("interface", primaryIface).Warn("interface not found in CNI result")
	}

	// Get DNS servers from raw results
	rawResults := result.Raw()
	if len(rawResults) > 0 {
		raw := rawResults[0]
		if !raw.DNS.IsEmpty() {
			dns = raw.DNS.Nameservers
		}
	}

	log.G(ctx).WithFields(log.Fields{
		"interface": primaryIface,
		"tap":       tapIface,
		"mac":       macAddr,
		"netns":     netnsPath,
		"ip":        ip,
		"gateway":   gateway,
		"netmask":   netmask,
		"dns":       dns,
	}).Info("CNI network setup complete")

	return &CNIResult{
		InterfaceName: primaryIface,
		TAPName:       tapIface,
		NetworkNS:     netnsPath,
		IP:            ip,
		Gateway:       gateway,
		Netmask:       netmask,
		DNS:           dns,
		MAC:           macAddr,
	}, nil
}

// TeardownNetwork tears down the CNI network for a container
func (cm *CNIManager) TeardownNetwork(ctx context.Context, id, netns string) error {
	log.G(ctx).WithFields(log.Fields{
		"id":    id,
		"netns": netns,
	}).Info("tearing down CNI network")

	if err := cm.cni.Remove(ctx, id, netns); err != nil {
		return fmt.Errorf("CNI teardown failed: %w", err)
	}

	log.G(ctx).Info("CNI network teardown complete")
	return nil
}
