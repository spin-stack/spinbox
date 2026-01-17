//go:build linux

package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestParseFlags_Version(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "long form",
			args: []string{"--version"},
		},
		{
			name: "short form -v",
			args: []string{"-v"},
		},
		{
			name: "single dash version",
			args: []string{"-version"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture stdout
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			cfg, setFlags, configFile, err := ParseFlags(tt.args)

			// Restore stdout
			w.Close()
			os.Stdout = oldStdout
			var buf bytes.Buffer
			io.Copy(&buf, r)

			// Verify ErrVersionRequested is returned
			if !errors.Is(err, ErrVersionRequested) {
				t.Errorf("expected ErrVersionRequested, got %v", err)
			}

			// Config and setFlags should be nil when version is requested
			if cfg != nil {
				t.Errorf("expected nil config, got %v", cfg)
			}
			if setFlags != nil {
				t.Errorf("expected nil setFlags, got %v", setFlags)
			}
			if configFile != "" {
				t.Errorf("expected empty configFile, got %q", configFile)
			}

			// Should print version info
			output := buf.String()
			if output == "" {
				t.Error("expected version output, got empty string")
			}
			if !bytes.Contains(buf.Bytes(), []byte("vminitd")) {
				t.Errorf("expected output to contain 'vminitd', got %q", output)
			}
		})
	}
}

func TestParseFlags_Defaults(t *testing.T) {
	cfg, setFlags, configFile, err := ParseFlags([]string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if setFlags == nil {
		t.Fatal("expected non-nil setFlags")
	}
	if configFile != "" {
		t.Errorf("expected empty configFile, got %q", configFile)
	}

	// No flags should be set
	if len(setFlags) != 0 {
		t.Errorf("expected no flags to be set, got %v", setFlags)
	}
}

func TestParseFlags_CustomValues(t *testing.T) {
	args := []string{
		"-config", "/path/to/config.json",
		"-debug",
		"-vsock-rpc-port", "2000",
		"-vsock-stream-port", "2001",
		"-vsock-cid", "5",
	}

	cfg, setFlags, configFile, err := ParseFlags(args)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if configFile != "/path/to/config.json" {
		t.Errorf("configFile = %q, want %q", configFile, "/path/to/config.json")
	}
	if !cfg.Debug {
		t.Error("expected Debug to be true")
	}
	if cfg.RPCPort != 2000 {
		t.Errorf("RPCPort = %d, want %d", cfg.RPCPort, 2000)
	}
	if cfg.StreamPort != 2001 {
		t.Errorf("StreamPort = %d, want %d", cfg.StreamPort, 2001)
	}
	if cfg.VSockContextID != 5 {
		t.Errorf("VSockContextID = %d, want %d", cfg.VSockContextID, 5)
	}

	// All flags should be set
	expectedFlags := []string{"config", "debug", "vsock-rpc-port", "vsock-stream-port", "vsock-cid"}
	for _, flag := range expectedFlags {
		if !setFlags[flag] {
			t.Errorf("expected flag %q to be set", flag)
		}
	}
}

func TestParseFlags_InvalidFlag(t *testing.T) {
	args := []string{"--invalid-flag"}

	cfg, setFlags, configFile, err := ParseFlags(args)
	if err == nil {
		t.Error("expected error for invalid flag, got nil")
	}
	// Verify nil returns on error
	if cfg != nil || setFlags != nil || configFile != "" {
		t.Error("expected nil/empty returns on parse error")
	}
}

func TestLoadFromFile(t *testing.T) {
	t.Run("valid config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")

		configData := map[string]any{
			"vsock_context_id": 10,
			"rpc_port":         3000,
			"stream_port":      3001,
			"debug":            true,
			"disabled_plugins": []string{"plugin1", "plugin2"},
		}

		data, err := json.Marshal(configData)
		if err != nil {
			t.Fatalf("failed to marshal config: %v", err)
		}

		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg := &ServiceConfig{}
		setFlags := make(map[string]bool)

		if err := LoadFromFile(configPath, cfg, setFlags); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if cfg.VSockContextID != 10 {
			t.Errorf("VSockContextID = %d, want %d", cfg.VSockContextID, 10)
		}
		if cfg.RPCPort != 3000 {
			t.Errorf("RPCPort = %d, want %d", cfg.RPCPort, 3000)
		}
		if cfg.StreamPort != 3001 {
			t.Errorf("StreamPort = %d, want %d", cfg.StreamPort, 3001)
		}
		if !cfg.Debug {
			t.Error("expected Debug to be true")
		}
		if len(cfg.DisabledPlugins) != 2 {
			t.Errorf("expected 2 disabled plugins, got %d", len(cfg.DisabledPlugins))
		}
	})

	t.Run("flags override config file", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")

		configData := map[string]any{
			"rpc_port": 3000,
			"debug":    true,
		}

		data, err := json.Marshal(configData)
		if err != nil {
			t.Fatalf("failed to marshal config: %v", err)
		}

		if err := os.WriteFile(configPath, data, 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		// Simulate flag-set values
		cfg := &ServiceConfig{
			RPCPort: 4000, // Flag-set value
			Debug:   false,
		}
		setFlags := map[string]bool{
			"vsock-rpc-port": true,
		}

		if err := LoadFromFile(configPath, cfg, setFlags); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Flag-set value should be preserved
		if cfg.RPCPort != 4000 {
			t.Errorf("RPCPort = %d, want %d (flag should override)", cfg.RPCPort, 4000)
		}
		// Non-flag-set value should come from file
		if !cfg.Debug {
			t.Error("Debug should be true from config file")
		}
	})

	t.Run("file not found", func(t *testing.T) {
		cfg := &ServiceConfig{}
		setFlags := make(map[string]bool)

		err := LoadFromFile("/nonexistent/config.json", cfg, setFlags)
		if err == nil {
			t.Error("expected error for nonexistent file, got nil")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")

		if err := os.WriteFile(configPath, []byte("not json"), 0600); err != nil {
			t.Fatalf("failed to write config file: %v", err)
		}

		cfg := &ServiceConfig{}
		setFlags := make(map[string]bool)

		err := LoadFromFile(configPath, cfg, setFlags)
		if err == nil {
			t.Error("expected error for invalid JSON, got nil")
		}
	})
}

func TestApplyPluginConfig(t *testing.T) {
	type testPluginConfig struct {
		Name    string `json:"name"`
		Port    int    `json:"port"`
		Enabled bool   `json:"enabled"`
	}

	t.Run("valid config", func(t *testing.T) {
		pluginCfg := &testPluginConfig{}
		configMap := map[string]any{
			"name":    "test-plugin",
			"port":    8080,
			"enabled": true,
		}

		err := ApplyPluginConfig(pluginCfg, configMap)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if pluginCfg.Name != "test-plugin" {
			t.Errorf("Name = %q, want %q", pluginCfg.Name, "test-plugin")
		}
		if pluginCfg.Port != 8080 {
			t.Errorf("Port = %d, want %d", pluginCfg.Port, 8080)
		}
		if !pluginCfg.Enabled {
			t.Error("expected Enabled to be true")
		}
	})

	t.Run("empty config map", func(t *testing.T) {
		pluginCfg := &testPluginConfig{Name: "original"}
		configMap := map[string]any{}

		err := ApplyPluginConfig(pluginCfg, configMap)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Original values should be preserved
		if pluginCfg.Name != "original" {
			t.Errorf("Name = %q, want %q", pluginCfg.Name, "original")
		}
	})

	t.Run("nil plugin config", func(t *testing.T) {
		configMap := map[string]any{"name": "test"}

		err := ApplyPluginConfig(nil, configMap)
		if err == nil {
			t.Error("expected error for nil plugin config, got nil")
		}
	})

	t.Run("type mismatch", func(t *testing.T) {
		pluginCfg := &testPluginConfig{}
		configMap := map[string]any{
			"port": "not-a-number", // Should be int
		}

		err := ApplyPluginConfig(pluginCfg, configMap)
		if err == nil {
			t.Error("expected error for type mismatch, got nil")
		}
	})
}
