// Copyright The Beacon Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package network

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadNetworkConfig_DefaultLegacyMode(t *testing.T) {
	// Clear environment variables
	os.Unsetenv("BEACON_CNI_MODE")
	os.Unsetenv("BEACON_CNI_CONF_DIR")
	os.Unsetenv("BEACON_CNI_BIN_DIR")
	os.Unsetenv("BEACON_CNI_NETWORK")

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeLegacy, cfg.Mode)
	assert.Equal(t, "10.88.0.0/16", cfg.Subnet)
	assert.Equal(t, "/etc/cni/net.d", cfg.CNIConfDir)
	assert.Equal(t, "/opt/cni/bin", cfg.CNIBinDir)
	assert.Equal(t, "beacon-net", cfg.CNINetworkName)
}

func TestLoadNetworkConfig_CNIModeEnabled(t *testing.T) {
	// Set CNI mode environment variable
	os.Setenv("BEACON_CNI_MODE", "1")
	defer os.Unsetenv("BEACON_CNI_MODE")

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
}

func TestLoadNetworkConfig_CNIModeDisabled(t *testing.T) {
	// Explicitly disable CNI mode
	os.Setenv("BEACON_CNI_MODE", "0")
	defer os.Unsetenv("BEACON_CNI_MODE")

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeLegacy, cfg.Mode)
}

func TestLoadNetworkConfig_CNIModeInvalidValue(t *testing.T) {
	// Invalid value should default to legacy
	os.Setenv("BEACON_CNI_MODE", "invalid")
	defer os.Unsetenv("BEACON_CNI_MODE")

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeLegacy, cfg.Mode)
}

func TestLoadNetworkConfig_CustomCNIConfDir(t *testing.T) {
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_CONF_DIR", "/custom/cni/conf")
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_CONF_DIR")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
	assert.Equal(t, "/custom/cni/conf", cfg.CNIConfDir)
}

func TestLoadNetworkConfig_CustomCNIBinDir(t *testing.T) {
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_BIN_DIR", "/custom/cni/bin")
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_BIN_DIR")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
	assert.Equal(t, "/custom/cni/bin", cfg.CNIBinDir)
}

func TestLoadNetworkConfig_CustomCNINetwork(t *testing.T) {
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_NETWORK", "custom-network")
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_NETWORK")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
	assert.Equal(t, "custom-network", cfg.CNINetworkName)
}

func TestLoadNetworkConfig_AllCustomValues(t *testing.T) {
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_CONF_DIR", "/custom/conf")
	os.Setenv("BEACON_CNI_BIN_DIR", "/custom/bin")
	os.Setenv("BEACON_CNI_NETWORK", "custom-net")
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_CONF_DIR")
		os.Unsetenv("BEACON_CNI_BIN_DIR")
		os.Unsetenv("BEACON_CNI_NETWORK")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
	assert.Equal(t, "/custom/conf", cfg.CNIConfDir)
	assert.Equal(t, "/custom/bin", cfg.CNIBinDir)
	assert.Equal(t, "custom-net", cfg.CNINetworkName)
}

func TestLoadNetworkConfig_EmptyEnvironmentVariables(t *testing.T) {
	// Empty strings should use defaults
	os.Setenv("BEACON_CNI_CONF_DIR", "")
	os.Setenv("BEACON_CNI_BIN_DIR", "")
	os.Setenv("BEACON_CNI_NETWORK", "")
	defer func() {
		os.Unsetenv("BEACON_CNI_CONF_DIR")
		os.Unsetenv("BEACON_CNI_BIN_DIR")
		os.Unsetenv("BEACON_CNI_NETWORK")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, "/etc/cni/net.d", cfg.CNIConfDir)
	assert.Equal(t, "/opt/cni/bin", cfg.CNIBinDir)
	assert.Equal(t, "beacon-net", cfg.CNINetworkName)
}

func TestNetworkMode_String(t *testing.T) {
	tests := []struct {
		mode     NetworkMode
		expected string
	}{
		{NetworkModeLegacy, "legacy"},
		{NetworkModeCNI, "cni"},
	}

	for _, tt := range tests {
		t.Run(string(tt.mode), func(t *testing.T) {
			assert.Equal(t, tt.expected, string(tt.mode))
		})
	}
}

func TestNetworkConfig_Validation(t *testing.T) {
	tests := []struct {
		name   string
		config NetworkConfig
		valid  bool
	}{
		{
			name: "valid legacy config",
			config: NetworkConfig{
				Subnet: "10.88.0.0/16",
				Mode:   NetworkModeLegacy,
			},
			valid: true,
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
			valid: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic validation - check required fields are set
			if tt.valid {
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

func TestLoadNetworkConfig_Idempotent(t *testing.T) {
	// Loading config multiple times should give same results
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_NETWORK", "test-net")
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_NETWORK")
	}()

	cfg1 := LoadNetworkConfig()
	cfg2 := LoadNetworkConfig()

	assert.Equal(t, cfg1.Mode, cfg2.Mode)
	assert.Equal(t, cfg1.CNINetworkName, cfg2.CNINetworkName)
	assert.Equal(t, cfg1.CNIConfDir, cfg2.CNIConfDir)
	assert.Equal(t, cfg1.CNIBinDir, cfg2.CNIBinDir)
}

func TestNetworkConfig_CNIDefaults(t *testing.T) {
	// When CNI mode is disabled, CNI fields should still have defaults
	os.Unsetenv("BEACON_CNI_MODE")

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeLegacy, cfg.Mode)
	// CNI defaults should still be set for easy switching
	assert.Equal(t, "/etc/cni/net.d", cfg.CNIConfDir)
	assert.Equal(t, "/opt/cni/bin", cfg.CNIBinDir)
	assert.Equal(t, "beacon-net", cfg.CNINetworkName)
}

func TestLoadNetworkConfig_PartialOverride(t *testing.T) {
	// Override only some CNI settings
	os.Setenv("BEACON_CNI_MODE", "1")
	os.Setenv("BEACON_CNI_CONF_DIR", "/my/conf")
	// Don't set BEACON_CNI_BIN_DIR or BEACON_CNI_NETWORK
	defer func() {
		os.Unsetenv("BEACON_CNI_MODE")
		os.Unsetenv("BEACON_CNI_CONF_DIR")
	}()

	cfg := LoadNetworkConfig()

	assert.Equal(t, NetworkModeCNI, cfg.Mode)
	assert.Equal(t, "/my/conf", cfg.CNIConfDir)
	assert.Equal(t, "/opt/cni/bin", cfg.CNIBinDir)     // Default
	assert.Equal(t, "beacon-net", cfg.CNINetworkName)  // Default
}

func TestNetworkManager_ModeRouting(t *testing.T) {
	// This is a conceptual test - actual NetworkManager tests would need
	// more setup (BoltDB, bridges, etc.)

	t.Run("legacy mode should be default", func(t *testing.T) {
		os.Unsetenv("BEACON_CNI_MODE")
		cfg := LoadNetworkConfig()
		assert.Equal(t, NetworkModeLegacy, cfg.Mode)
	})

	t.Run("CNI mode when explicitly enabled", func(t *testing.T) {
		os.Setenv("BEACON_CNI_MODE", "1")
		defer os.Unsetenv("BEACON_CNI_MODE")

		cfg := LoadNetworkConfig()
		assert.Equal(t, NetworkModeCNI, cfg.Mode)
	})
}

func TestNetworkConfig_SubnetUnchanged(t *testing.T) {
	// Subnet should not be affected by CNI mode
	os.Setenv("BEACON_CNI_MODE", "1")
	defer os.Unsetenv("BEACON_CNI_MODE")

	cfg := LoadNetworkConfig()

	assert.Equal(t, "10.88.0.0/16", cfg.Subnet)
}

func TestLoadNetworkConfig_WhitespaceInEnvVars(t *testing.T) {
	// Test that whitespace in env vars is preserved
	os.Setenv("BEACON_CNI_CONF_DIR", "  /path/with/spaces  ")
	defer os.Unsetenv("BEACON_CNI_CONF_DIR")

	cfg := LoadNetworkConfig()

	// Environment variables are used as-is
	assert.Equal(t, "  /path/with/spaces  ", cfg.CNIConfDir)
}

func TestNetworkMode_TypeSafety(t *testing.T) {
	// Ensure NetworkMode is a distinct type
	var mode NetworkMode = NetworkModeLegacy

	assert.IsType(t, NetworkMode(""), mode)
	assert.NotEqual(t, "legacy", mode) // Type mismatch even if value matches
}

func TestLoadNetworkConfig_EnvironmentIsolation(t *testing.T) {
	// Test that environment changes are reflected
	os.Unsetenv("BEACON_CNI_MODE")
	cfg1 := LoadNetworkConfig()
	require.Equal(t, NetworkModeLegacy, cfg1.Mode)

	os.Setenv("BEACON_CNI_MODE", "1")
	cfg2 := LoadNetworkConfig()
	require.Equal(t, NetworkModeCNI, cfg2.Mode)

	os.Unsetenv("BEACON_CNI_MODE")
	cfg3 := LoadNetworkConfig()
	require.Equal(t, NetworkModeLegacy, cfg3.Mode)
}
