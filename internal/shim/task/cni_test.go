//go:build linux

package task

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNewCNIManager(t *testing.T) {
	tests := []struct {
		name        string
		setupEnv    func() func()
		expectError bool
	}{
		{
			name: "default paths",
			setupEnv: func() func() {
				// Save original env
				origConfDir := os.Getenv("CNI_CONF_DIR")
				origBinDir := os.Getenv("CNI_BIN_DIR")

				// Clear env for test
				os.Unsetenv("CNI_CONF_DIR")
				os.Unsetenv("CNI_BIN_DIR")

				return func() {
					if origConfDir != "" {
						os.Setenv("CNI_CONF_DIR", origConfDir)
					}
					if origBinDir != "" {
						os.Setenv("CNI_BIN_DIR", origBinDir)
					}
				}
			},
			expectError: false, // CNI init succeeds even if dirs don't exist (logs warning)
		},
		{
			name: "custom paths with temp directories",
			setupEnv: func() func() {
				tmpDir := t.TempDir()
				confDir := filepath.Join(tmpDir, "conf")
				binDir := filepath.Join(tmpDir, "bin")

				// Create directories
				os.MkdirAll(confDir, 0755)
				os.MkdirAll(binDir, 0755)

				// Create a minimal CNI config
				confFile := filepath.Join(confDir, "10-test.conf")
				configContent := `{
  "cniVersion": "0.4.0",
  "name": "test-network",
  "type": "bridge",
  "bridge": "test-br0",
  "isGateway": true,
  "ipMasq": true,
  "ipam": {
    "type": "host-local",
    "subnet": "10.88.0.0/16"
  }
}`
				os.WriteFile(confFile, []byte(configContent), 0644)

				// Save original env
				origConfDir := os.Getenv("CNI_CONF_DIR")
				origBinDir := os.Getenv("CNI_BIN_DIR")

				os.Setenv("CNI_CONF_DIR", confDir)
				os.Setenv("CNI_BIN_DIR", binDir)

				return func() {
					if origConfDir != "" {
						os.Setenv("CNI_CONF_DIR", origConfDir)
					} else {
						os.Unsetenv("CNI_CONF_DIR")
					}
					if origBinDir != "" {
						os.Setenv("CNI_BIN_DIR", origBinDir)
					} else {
						os.Unsetenv("CNI_BIN_DIR")
					}
				}
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setupEnv()
			defer cleanup()

			ctx := context.Background()
			mgr, err := NewCNIManager(ctx)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				if mgr == nil {
					t.Error("expected CNIManager but got nil")
				}
			}
		})
	}
}

func TestCNIManager_SetupNetwork_WithoutCNI(t *testing.T) {
	// This test verifies behavior when CNI is not available
	// We expect setup to fail gracefully

	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, "conf")
	binDir := filepath.Join(tmpDir, "bin")

	os.MkdirAll(confDir, 0755)
	os.MkdirAll(binDir, 0755)

	// Create minimal config
	confFile := filepath.Join(confDir, "10-test.conf")
	configContent := `{
  "cniVersion": "0.4.0",
  "name": "test-network",
  "type": "bridge",
  "bridge": "test-br0"
}`
	os.WriteFile(confFile, []byte(configContent), 0644)

	origConfDir := os.Getenv("CNI_CONF_DIR")
	origBinDir := os.Getenv("CNI_BIN_DIR")
	defer func() {
		if origConfDir != "" {
			os.Setenv("CNI_CONF_DIR", origConfDir)
		} else {
			os.Unsetenv("CNI_CONF_DIR")
		}
		if origBinDir != "" {
			os.Setenv("CNI_BIN_DIR", origBinDir)
		} else {
			os.Unsetenv("CNI_BIN_DIR")
		}
	}()

	os.Setenv("CNI_CONF_DIR", confDir)
	os.Setenv("CNI_BIN_DIR", binDir)

	ctx := context.Background()
	mgr, err := NewCNIManager(ctx)
	if err != nil {
		t.Skipf("CNI manager creation failed (expected in test env): %v", err)
	}

	// Try to setup network - should fail because binaries don't exist
	_, err = mgr.SetupNetwork(ctx, "test-container", "/var/run/netns/test")
	if err == nil {
		t.Error("expected setup to fail without CNI binaries")
	}
}

func TestCNIManager_TeardownNetwork(t *testing.T) {
	tmpDir := t.TempDir()
	confDir := filepath.Join(tmpDir, "conf")
	binDir := filepath.Join(tmpDir, "bin")

	os.MkdirAll(confDir, 0755)
	os.MkdirAll(binDir, 0755)

	confFile := filepath.Join(confDir, "10-test.conf")
	configContent := `{
  "cniVersion": "0.4.0",
  "name": "test-network",
  "type": "bridge"
}`
	os.WriteFile(confFile, []byte(configContent), 0644)

	origConfDir := os.Getenv("CNI_CONF_DIR")
	origBinDir := os.Getenv("CNI_BIN_DIR")
	defer func() {
		if origConfDir != "" {
			os.Setenv("CNI_CONF_DIR", origConfDir)
		} else {
			os.Unsetenv("CNI_CONF_DIR")
		}
		if origBinDir != "" {
			os.Setenv("CNI_BIN_DIR", origBinDir)
		} else {
			os.Unsetenv("CNI_BIN_DIR")
		}
	}()

	os.Setenv("CNI_CONF_DIR", confDir)
	os.Setenv("CNI_BIN_DIR", binDir)

	ctx := context.Background()
	mgr, err := NewCNIManager(ctx)
	if err != nil {
		t.Skipf("CNI manager creation failed: %v", err)
	}

	// Teardown of non-existent network should not panic
	err = mgr.TeardownNetwork(ctx, "test-container", "/var/run/netns/test")
	// Error is expected since network doesn't exist
	if err == nil {
		t.Log("teardown succeeded (network may not have existed)")
	}
}
