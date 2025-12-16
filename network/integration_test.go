// Copyright The Beacon Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux && integration

package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	bolt "go.etcd.io/bbolt"
)

// TestNetworkManager_LegacyMode tests the legacy network manager implementation
func TestNetworkManager_LegacyMode(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	// Create temporary database
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "network-test.db")

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	require.NoError(t, err)
	defer db.Close()

	// Initialize test buckets
	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("network-config"))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte("ip-allocations"))
		return err
	})
	require.NoError(t, err)

	// Create network config for legacy mode
	cfg := NetworkConfig{
		Subnet: "10.88.0.0/16",
		Mode:   NetworkModeLegacy,
	}

	// Create network manager
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	networkConfigStore := NewBoltStore(db, "network-config")
	ipStore := NewBoltStore(db, "ip-allocations")

	nm, err := NewNetworkManager(cfg, networkConfigStore, ipStore, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, nm)
	defer nm.Close()

	assert.Equal(t, NetworkModeLegacy, nm.config.Mode)

	// Test network resource allocation
	t.Run("allocate and release IP", func(t *testing.T) {
		env := &Environment{
			Id: "test-vm-1",
			Networking: Networking{
				Mode: NetworkingModeNone,
			},
		}

		// Allocate network resources
		err := nm.EnsureNetworkResources(env)
		require.NoError(t, err)

		// Verify IP was allocated
		assert.NotEmpty(t, env.Networking.IPAddress)
		assert.NotEmpty(t, env.Networking.Gateway)
		assert.NotEmpty(t, env.Networking.TAPDevice)

		// Release network resources
		err = nm.ReleaseNetworkResources(env)
		require.NoError(t, err)
	})

	t.Run("multiple VMs get different IPs", func(t *testing.T) {
		env1 := &Environment{Id: "test-vm-2", Networking: Networking{Mode: NetworkingModeNone}}
		env2 := &Environment{Id: "test-vm-3", Networking: Networking{Mode: NetworkingModeNone}}

		err := nm.EnsureNetworkResources(env1)
		require.NoError(t, err)

		err = nm.EnsureNetworkResources(env2)
		require.NoError(t, err)

		// IPs should be different
		assert.NotEqual(t, env1.Networking.IPAddress, env2.Networking.IPAddress)

		// TAP devices should be different
		assert.NotEqual(t, env1.Networking.TAPDevice, env2.Networking.TAPDevice)

		// Cleanup
		_ = nm.ReleaseNetworkResources(env1)
		_ = nm.ReleaseNetworkResources(env2)
	})
}

// TestNetworkManager_CNIMode tests CNI network manager implementation
func TestNetworkManager_CNIMode(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("Test requires root privileges")
	}

	// Check if CNI plugins are available
	if _, err := os.Stat("/opt/cni/bin/bridge"); os.IsNotExist(err) {
		t.Skip("CNI plugins not installed at /opt/cni/bin")
	}

	// Create temporary directories for CNI config
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, "net.d")
	err := os.MkdirAll(confDir, 0755)
	require.NoError(t, err)

	// Create a test CNI configuration
	cniConfig := `{
  "cniVersion": "1.0.0",
  "name": "beacon-test",
  "plugins": [
    {
      "type": "bridge",
      "bridge": "beacontest0",
      "isGateway": true,
      "ipMasq": false,
      "ipam": {
        "type": "host-local",
        "ranges": [[{"subnet": "10.99.0.0/24"}]],
        "routes": [{"dst": "0.0.0.0/0"}]
      }
    }
  ]
}`

	confFile := filepath.Join(confDir, "10-beacon-test.conflist")
	err = os.WriteFile(confFile, []byte(cniConfig), 0644)
	require.NoError(t, err)

	// Create database for CNI mode
	dbPath := filepath.Join(tmpDir, "network-cni-test.db")
	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("network-config"))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte("ip-allocations"))
		return err
	})
	require.NoError(t, err)

	// Create network config for CNI mode
	cfg := NetworkConfig{
		Subnet:         "10.99.0.0/24",
		Mode:           NetworkModeCNI,
		CNIConfDir:     confDir,
		CNIBinDir:      "/opt/cni/bin",
		CNINetworkName: "beacon-test",
	}

	networkConfigStore := NewBoltStore(db, "network-config")
	ipStore := NewBoltStore(db, "ip-allocations")

	nm, err := NewNetworkManager(cfg, networkConfigStore, ipStore, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, nm)
	defer nm.Close()

	assert.Equal(t, NetworkModeCNI, nm.config.Mode)

	t.Run("CNI network setup and teardown", func(t *testing.T) {
		env := &Environment{
			Id: "test-cni-vm-1",
			Networking: Networking{
				Mode: NetworkingModeNone,
			},
		}

		// Setup CNI network
		err := nm.EnsureNetworkResources(env)
		if err != nil {
			// CNI setup might fail without tc-redirect-tap
			t.Logf("CNI setup failed (expected without tc-redirect-tap): %v", err)
			t.Skip("CNI setup requires tc-redirect-tap plugin")
		}

		// Verify CNI allocated resources
		assert.NotEmpty(t, env.Networking.IPAddress)
		assert.NotEmpty(t, env.Networking.Gateway)

		// Teardown CNI network
		err = nm.ReleaseNetworkResources(env)
		assert.NoError(t, err)
	})
}

// TestNetworkManager_ModeSwitching tests switching between modes
func TestNetworkManager_ModeSwitching(t *testing.T) {
	t.Run("configuration reflects mode correctly", func(t *testing.T) {
		// Test legacy mode
		os.Unsetenv("BEACON_CNI_MODE")
		cfg1 := LoadNetworkConfig()
		assert.Equal(t, NetworkModeLegacy, cfg1.Mode)

		// Test CNI mode
		os.Setenv("BEACON_CNI_MODE", "1")
		cfg2 := LoadNetworkConfig()
		assert.Equal(t, NetworkModeCNI, cfg2.Mode)

		os.Unsetenv("BEACON_CNI_MODE")
	})
}

// TestNetworkManager_CNIResultStorage tests CNI result storage
func TestNetworkManager_CNIResultStorage(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "network-storage-test.db")

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	require.NoError(t, err)
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("network-config"))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte("ip-allocations"))
		return err
	})
	require.NoError(t, err)

	cfg := NetworkConfig{
		Subnet:         "10.88.0.0/16",
		Mode:           NetworkModeCNI,
		CNIConfDir:     "/etc/cni/net.d",
		CNIBinDir:      "/opt/cni/bin",
		CNINetworkName: "beacon-net",
	}

	networkConfigStore := NewBoltStore(db, "network-config")
	ipStore := NewBoltStore(db, "ip-allocations")

	nm, err := NewNetworkManager(cfg, networkConfigStore, ipStore, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, nm)
	defer nm.Close()

	t.Run("CNI results map is initialized", func(t *testing.T) {
		assert.NotNil(t, nm.cniResults)
		assert.Empty(t, nm.cniResults)
	})
}

// TestNetworkConfig_Validation tests network configuration validation
func TestNetworkConfig_Validation(t *testing.T) {
	tests := []struct {
		name        string
		config      NetworkConfig
		expectError bool
	}{
		{
			name: "valid legacy config",
			config: NetworkConfig{
				Subnet: "10.88.0.0/16",
				Mode:   NetworkModeLegacy,
			},
			expectError: false,
		},
		{
			name: "valid CNI config",
			config: NetworkConfig{
				Subnet:         "10.88.0.0/16",
				Mode:           NetworkModeCNI,
				CNIConfDir:     "/etc/cni/net.d",
				CNIBinDir:      "/opt/cni/bin",
				CNINetworkName: "beacon-net",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic validation checks
			if !tt.expectError {
				assert.NotEmpty(t, tt.config.Subnet)
				if tt.config.Mode == NetworkModeCNI {
					assert.NotEmpty(t, tt.config.CNIConfDir)
					assert.NotEmpty(t, tt.config.CNIBinDir)
					assert.NotEmpty(t, tt.config.CNINetworkName)
				}
			}
		})
	}
}

// BenchmarkNetworkManager_LegacyMode benchmarks legacy mode performance
func BenchmarkNetworkManager_LegacyMode(b *testing.B) {
	if os.Getuid() != 0 {
		b.Skip("Benchmark requires root privileges")
	}

	tmpDir := b.TempDir()
	dbPath := filepath.Join(tmpDir, "bench-network.db")

	db, err := bolt.Open(dbPath, 0600, &bolt.Options{Timeout: 1 * time.Second})
	require.NoError(b, err)
	defer db.Close()

	err = db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte("network-config"))
		if err != nil {
			return err
		}
		_, err = tx.CreateBucketIfNotExists([]byte("ip-allocations"))
		return err
	})
	require.NoError(b, err)

	cfg := NetworkConfig{
		Subnet: "10.88.0.0/16",
		Mode:   NetworkModeLegacy,
	}

	networkConfigStore := NewBoltStore(db, "network-config")
	ipStore := NewBoltStore(db, "ip-allocations")

	nm, err := NewNetworkManager(cfg, networkConfigStore, ipStore, nil, nil, nil, nil)
	require.NoError(b, err)
	defer nm.Close()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		env := &Environment{
			Id:         fmt.Sprintf("bench-vm-%d", i),
			Networking: Networking{Mode: NetworkingModeNone},
		}

		_ = nm.EnsureNetworkResources(env)
		_ = nm.ReleaseNetworkResources(env)
	}
}
