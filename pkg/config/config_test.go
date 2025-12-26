package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	// Verify paths
	if cfg.Paths.ShareDir != "/usr/share/qemubox" {
		t.Errorf("expected ShareDir /usr/share/qemubox, got %s", cfg.Paths.ShareDir)
	}
	if cfg.Paths.StateDir != "/var/lib/qemubox" {
		t.Errorf("expected StateDir /var/lib/qemubox, got %s", cfg.Paths.StateDir)
	}
	if cfg.Paths.LogDir != "/var/log/qemubox" {
		t.Errorf("expected LogDir /var/log/qemubox, got %s", cfg.Paths.LogDir)
	}

	// Verify runtime
	if cfg.Runtime.VMM != "qemu" {
		t.Errorf("expected VMM qemu, got %s", cfg.Runtime.VMM)
	}
	if cfg.Runtime.ShimDebug != false {
		t.Errorf("expected ShimDebug false, got %v", cfg.Runtime.ShimDebug)
	}

	// Verify CPU hotplug
	if cfg.CPUHotplug.MonitorInterval != "5s" {
		t.Errorf("expected MonitorInterval 5s, got %s", cfg.CPUHotplug.MonitorInterval)
	}
	if cfg.CPUHotplug.ScaleUpThreshold != 80.0 {
		t.Errorf("expected ScaleUpThreshold 80.0, got %.2f", cfg.CPUHotplug.ScaleUpThreshold)
	}

	// Verify memory hotplug
	if cfg.MemHotplug.MonitorInterval != "10s" {
		t.Errorf("expected MonitorInterval 10s, got %s", cfg.MemHotplug.MonitorInterval)
	}
	if cfg.MemHotplug.OOMSafetyMarginMB != 128 {
		t.Errorf("expected OOMSafetyMarginMB 128, got %d", cfg.MemHotplug.OOMSafetyMarginMB)
	}
}

func TestLoadFrom_MissingFile(t *testing.T) {
	_, err := LoadFrom("/nonexistent/path/config.json")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}

	if !os.IsNotExist(err) {
		// Check that error message is helpful
		errMsg := err.Error()
		if errMsg == "" {
			t.Error("expected helpful error message")
		}
		t.Logf("Error message: %s", errMsg)
	}
}

func TestLoadFrom_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Write invalid JSON
	if err := os.WriteFile(configPath, []byte("{invalid json}"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFrom(configPath)
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}

	t.Logf("Error message: %s", err)
}

func TestLoadFrom_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.json")

	// Create necessary directories and files for validation
	shareDir := filepath.Join(tmpDir, "share")
	kernelDir := filepath.Join(shareDir, "kernel")
	stateDir := filepath.Join(tmpDir, "state")
	logDir := filepath.Join(tmpDir, "log")

	if err := os.MkdirAll(kernelDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create dummy kernel and initrd
	kernelPath := filepath.Join(kernelDir, "qemubox-kernel-x86_64")
	initrdPath := filepath.Join(kernelDir, "qemubox-initrd")
	if err := os.WriteFile(kernelPath, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initrdPath, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Paths: PathsConfig{
			ShareDir: shareDir,
			StateDir: stateDir,
			LogDir:   logDir,
		},
		Runtime: RuntimeConfig{
			VMM:       "qemu",
			ShimDebug: true,
		},
		CPUHotplug: CPUHotplugConfig{
			MonitorInterval:      "10s",
			ScaleUpCooldown:      "20s",
			ScaleDownCooldown:    "40s",
			ScaleUpThreshold:     85.0,
			ScaleDownThreshold:   40.0,
			ScaleUpThrottleLimit: 10.0,
			ScaleUpStability:     3,
			ScaleDownStability:   5,
			EnableScaleDown:      false,
		},
		MemHotplug: MemHotplugConfig{
			MonitorInterval:    "15s",
			ScaleUpCooldown:    "45s",
			ScaleDownCooldown:  "90s",
			ScaleUpThreshold:   90.0,
			ScaleDownThreshold: 50.0,
			OOMSafetyMarginMB:  256,
			IncrementSizeMB:    256,
			ScaleUpStability:   2,
			ScaleDownStability: 4,
			EnableScaleDown:    true,
		},
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFrom(configPath)
	if err != nil {
		t.Fatalf("failed to load valid config: %v", err)
	}

	if loaded.Runtime.ShimDebug != true {
		t.Errorf("expected ShimDebug true, got %v", loaded.Runtime.ShimDebug)
	}

	if loaded.CPUHotplug.ScaleUpThreshold != 85.0 {
		t.Errorf("expected ScaleUpThreshold 85.0, got %.2f", loaded.CPUHotplug.ScaleUpThreshold)
	}
}

func TestApplyDefaults(t *testing.T) {
	// Create config with some empty fields
	cfg := &Config{
		Paths: PathsConfig{
			ShareDir: "/custom/share",
			// StateDir and LogDir empty - should be filled with defaults
		},
		Runtime: RuntimeConfig{
			// VMM empty - should be filled with default
		},
	}

	cfg.applyDefaults()

	if cfg.Paths.ShareDir != "/custom/share" {
		t.Errorf("expected custom ShareDir to be preserved, got %s", cfg.Paths.ShareDir)
	}

	if cfg.Paths.StateDir != "/var/lib/qemubox" {
		t.Errorf("expected default StateDir, got %s", cfg.Paths.StateDir)
	}

	if cfg.Paths.LogDir != "/var/log/qemubox" {
		t.Errorf("expected default LogDir, got %s", cfg.Paths.LogDir)
	}

	if cfg.Runtime.VMM != "qemu" {
		t.Errorf("expected default VMM, got %s", cfg.Runtime.VMM)
	}

	if cfg.CPUHotplug.MonitorInterval != "5s" {
		t.Errorf("expected default CPU MonitorInterval, got %s", cfg.CPUHotplug.MonitorInterval)
	}

	if cfg.MemHotplug.IncrementSizeMB != 128 {
		t.Errorf("expected default IncrementSizeMB, got %d", cfg.MemHotplug.IncrementSizeMB)
	}
}

func TestValidate_InvalidVMM(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Runtime.VMM = "firecracker" // Not supported

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for invalid VMM")
	}

	t.Logf("Error message: %s", err)
}

func TestValidate_InvalidThresholds(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(*Config)
	}{
		{
			name: "CPU scale_up_threshold too high",
			setupFunc: func(c *Config) {
				c.CPUHotplug.ScaleUpThreshold = 150.0
			},
		},
		{
			name: "CPU scale_up_threshold too low",
			setupFunc: func(c *Config) {
				c.CPUHotplug.ScaleUpThreshold = 0
			},
		},
		{
			name: "Memory scale_down_threshold too high",
			setupFunc: func(c *Config) {
				c.MemHotplug.ScaleDownThreshold = 101.0
			},
		},
		{
			name: "Invalid monitor interval",
			setupFunc: func(c *Config) {
				c.CPUHotplug.MonitorInterval = "invalid"
			},
		},
		{
			name: "Non-aligned increment size",
			setupFunc: func(c *Config) {
				c.MemHotplug.IncrementSizeMB = 100 // Not 128-aligned
			},
		},
		{
			name: "Zero stability counter",
			setupFunc: func(c *Config) {
				c.CPUHotplug.ScaleUpStability = 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.setupFunc(cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s", tt.name)
			}

			t.Logf("Error message: %s", err)
		})
	}
}

func TestToCPUHotplugConfig(t *testing.T) {
	cfg := DefaultConfig()

	hotplugCfg, err := cfg.ToCPUHotplugConfig()
	if err != nil {
		t.Fatalf("failed to convert to CPU hotplug config: %v", err)
	}

	if hotplugCfg.MonitorInterval.String() != "5s" {
		t.Errorf("expected MonitorInterval 5s, got %s", hotplugCfg.MonitorInterval)
	}

	if hotplugCfg.ScaleUpThreshold != 80.0 {
		t.Errorf("expected ScaleUpThreshold 80.0, got %.2f", hotplugCfg.ScaleUpThreshold)
	}

	if hotplugCfg.EnableScaleDown != true {
		t.Errorf("expected EnableScaleDown true, got %v", hotplugCfg.EnableScaleDown)
	}
}

func TestToMemHotplugConfig(t *testing.T) {
	cfg := DefaultConfig()

	hotplugCfg, err := cfg.ToMemHotplugConfig()
	if err != nil {
		t.Fatalf("failed to convert to memory hotplug config: %v", err)
	}

	if hotplugCfg.MonitorInterval.String() != "10s" {
		t.Errorf("expected MonitorInterval 10s, got %s", hotplugCfg.MonitorInterval)
	}

	if hotplugCfg.ScaleUpThreshold != 85.0 {
		t.Errorf("expected ScaleUpThreshold 85.0, got %.2f", hotplugCfg.ScaleUpThreshold)
	}

	// Check MB to bytes conversion
	expectedBytes := int64(128 * 1024 * 1024)
	if hotplugCfg.IncrementSize != expectedBytes {
		t.Errorf("expected IncrementSize %d bytes, got %d", expectedBytes, hotplugCfg.IncrementSize)
	}

	if hotplugCfg.EnableScaleDown != false {
		t.Errorf("expected EnableScaleDown false, got %v", hotplugCfg.EnableScaleDown)
	}
}

func TestToCPUHotplugConfig_InvalidDuration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.CPUHotplug.MonitorInterval = "invalid-duration"

	_, err := cfg.ToCPUHotplugConfig()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}

	t.Logf("Error message: %s", err)
}

func TestToMemHotplugConfig_InvalidDuration(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MemHotplug.ScaleUpCooldown = "not-a-duration"

	_, err := cfg.ToMemHotplugConfig()
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}

	t.Logf("Error message: %s", err)
}
