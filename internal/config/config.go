// Package config provides centralized configuration management for qemubox.
// All configuration is loaded from a JSON file at /etc/qemubox/config.json
// (overridable via QEMUBOX_CONFIG environment variable).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	// DefaultConfigPath is the default location for the config file
	DefaultConfigPath = "/etc/qemubox/config.json"

	// ConfigEnvVar is the environment variable to override config file location
	ConfigEnvVar = "QEMUBOX_CONFIG"

	// Path defaults
	defaultShareDir = "/usr/share/qemubox"
	defaultStateDir = "/var/lib/qemubox"
	defaultLogDir   = "/var/log/qemubox"

	// Runtime defaults
	defaultVMM = "qemu"

	// Timeout defaults
	defaultVMStart         = "10s"
	defaultDeviceDetection = "5s"
	defaultShutdownGrace   = "2s"
	defaultEventReconnect  = "2s"
	defaultTaskClientRetry = "1s"
	defaultIOWait          = "30s"
	defaultQMPCommand      = "5s"

	// CPU hotplug defaults
	defaultCPUMonitorInterval      = "5s"
	defaultCPUScaleUpCooldown      = "10s"
	defaultCPUScaleDownCooldown    = "30s"
	defaultCPUScaleUpThreshold     = 80.0
	defaultCPUScaleDownThreshold   = 50.0
	defaultCPUScaleUpThrottleLimit = 5.0
	defaultCPUScaleUpStability     = 2
	defaultCPUScaleDownStability   = 6

	// Memory hotplug defaults
	defaultMemMonitorInterval    = "10s"
	defaultMemScaleUpCooldown    = "30s"
	defaultMemScaleDownCooldown  = "60s"
	defaultMemScaleUpThreshold   = 85.0
	defaultMemScaleDownThreshold = 60.0
	defaultMemOOMSafetyMarginMB  = 128
	defaultMemIncrementSizeMB    = 128
	defaultMemScaleUpStability   = 3
	defaultMemScaleDownStability = 6
)

// Config is the root configuration structure
type Config struct {
	Paths      PathsConfig      `json:"paths"`
	Runtime    RuntimeConfig    `json:"runtime"`
	Timeouts   TimeoutsConfig   `json:"timeouts"`
	CPUHotplug CPUHotplugConfig `json:"cpu_hotplug"`
	MemHotplug MemHotplugConfig `json:"memory_hotplug"`
}

// PathsConfig defines filesystem paths for qemubox components
type PathsConfig struct {
	ShareDir      string `json:"share_dir"`       // Binaries and config directory
	StateDir      string `json:"state_dir"`       // State files directory
	LogDir        string `json:"log_dir"`         // Logs directory
	QEMUPath      string `json:"qemu_path"`       // QEMU binary location (auto-discovered if empty)
	QEMUSharePath string `json:"qemu_share_path"` // QEMU firmware/BIOS directory (auto-discovered if empty)
}

// RuntimeConfig defines runtime behavior settings
type RuntimeConfig struct {
	VMM string `json:"vmm"` // VMM backend (currently only "qemu" supported)
}

// TimeoutsConfig defines timeout durations for various lifecycle operations.
// All values are duration strings (e.g., "5s", "2m", "500ms").
// These timeouts can be tuned based on hardware performance and workload characteristics.
//
// After validation, use the Durations() method to access parsed values.
type TimeoutsConfig struct {
	// VMStart is the timeout for VM boot (QEMU process start to vsock connection).
	// Default: 10s. Increase for slow storage or complex configurations.
	VMStart string `json:"vm_start"`

	// DeviceDetection is the timeout for guest device detection (virtio-blk).
	// Default: 5s. Increase for VMs with many disks or slow I/O.
	DeviceDetection string `json:"device_detection"`

	// ShutdownGrace is how long to wait for guest OS shutdown before SIGKILL.
	// Default: 2s. Increase for applications with slow shutdown handlers.
	ShutdownGrace string `json:"shutdown_grace"`

	// EventReconnect is how long to retry event stream reconnection.
	// Default: 2s. Increase for workloads with high VM pause frequency.
	EventReconnect string `json:"event_reconnect"`

	// TaskClientRetry is how long to retry vsock dial for task RPCs.
	// Default: 1s. Increase for environments with vsock routing instability.
	TaskClientRetry string `json:"task_client_retry"`

	// IOWait is the timeout for waiting for I/O forwarder completion on exit.
	// Default: 30s. Decrease for faster container cleanup, increase for large outputs.
	IOWait string `json:"io_wait"`

	// QMPCommand is the default timeout for QMP commands to QEMU.
	// Default: 5s. Increase for complex QMP operations or slow hosts.
	QMPCommand string `json:"qmp_command"`

	// parsed holds the parsed durations, set during validation
	parsed *TimeoutDurations
}

// TimeoutDurations contains parsed timeout values ready for use.
// Obtained via TimeoutsConfig.Durations() after validation.
type TimeoutDurations struct {
	VMStart         time.Duration
	DeviceDetection time.Duration
	ShutdownGrace   time.Duration
	EventReconnect  time.Duration
	TaskClientRetry time.Duration
	IOWait          time.Duration
	QMPCommand      time.Duration
}

// parseAndCache parses all duration strings and caches the results.
// Returns an error if any duration is invalid.
func (t *TimeoutsConfig) parseAndCache() error {
	d := &TimeoutDurations{}
	var err error

	d.VMStart, err = time.ParseDuration(t.VMStart)
	if err != nil {
		return fmt.Errorf("vm_start: %w", err)
	}

	d.DeviceDetection, err = time.ParseDuration(t.DeviceDetection)
	if err != nil {
		return fmt.Errorf("device_detection: %w", err)
	}

	d.ShutdownGrace, err = time.ParseDuration(t.ShutdownGrace)
	if err != nil {
		return fmt.Errorf("shutdown_grace: %w", err)
	}

	d.EventReconnect, err = time.ParseDuration(t.EventReconnect)
	if err != nil {
		return fmt.Errorf("event_reconnect: %w", err)
	}

	d.TaskClientRetry, err = time.ParseDuration(t.TaskClientRetry)
	if err != nil {
		return fmt.Errorf("task_client_retry: %w", err)
	}

	d.IOWait, err = time.ParseDuration(t.IOWait)
	if err != nil {
		return fmt.Errorf("io_wait: %w", err)
	}

	d.QMPCommand, err = time.ParseDuration(t.QMPCommand)
	if err != nil {
		return fmt.Errorf("qmp_command: %w", err)
	}

	t.parsed = d
	return nil
}

// Durations returns the parsed timeout values.
// Panics if called before validation (parseAndCache).
func (t *TimeoutsConfig) Durations() *TimeoutDurations {
	if t.parsed == nil {
		panic("TimeoutsConfig.Durations() called before validation")
	}
	return t.parsed
}

// CPUHotplugConfig defines CPU hotplug controller settings
type CPUHotplugConfig struct {
	MonitorInterval      string  `json:"monitor_interval"`        // Monitoring interval (e.g., "5s")
	ScaleUpCooldown      string  `json:"scale_up_cooldown"`       // Cooldown after scale-up
	ScaleDownCooldown    string  `json:"scale_down_cooldown"`     // Cooldown after scale-down
	ScaleUpThreshold     float64 `json:"scale_up_threshold"`      // CPU usage % to trigger scale-up
	ScaleDownThreshold   float64 `json:"scale_down_threshold"`    // CPU usage % to trigger scale-down
	ScaleUpThrottleLimit float64 `json:"scale_up_throttle_limit"` // Don't scale up if throttling exceeds this %
	ScaleUpStability     int     `json:"scale_up_stability"`      // Consecutive high readings before scale-up
	ScaleDownStability   int     `json:"scale_down_stability"`    // Consecutive low readings before scale-down
	EnableScaleDown      bool    `json:"enable_scale_down"`       // Allow removing CPUs
}

// MemHotplugConfig defines memory hotplug controller settings
type MemHotplugConfig struct {
	MonitorInterval    string  `json:"monitor_interval"`     // Monitoring interval (e.g., "10s")
	ScaleUpCooldown    string  `json:"scale_up_cooldown"`    // Cooldown after scale-up
	ScaleDownCooldown  string  `json:"scale_down_cooldown"`  // Cooldown after scale-down
	ScaleUpThreshold   float64 `json:"scale_up_threshold"`   // Memory usage % to trigger scale-up
	ScaleDownThreshold float64 `json:"scale_down_threshold"` // Memory usage % to trigger scale-down
	OOMSafetyMarginMB  int64   `json:"oom_safety_margin_mb"` // Keep this much memory free (OOM protection)
	IncrementSizeMB    int64   `json:"increment_size_mb"`    // Memory increment size (must be 128MB-aligned)
	ScaleUpStability   int     `json:"scale_up_stability"`   // Consecutive high readings before scale-up
	ScaleDownStability int     `json:"scale_down_stability"` // Consecutive low readings before scale-down
	EnableScaleDown    bool    `json:"enable_scale_down"`    // Allow removing memory
}

var (
	globalConfig *Config
	configOnce   sync.Once
	configMu     sync.Mutex
	errConfig    error
)

// Reset clears the cached global config, forcing the next Get() call to reload.
// This is intended for testing only. Callers must ensure no concurrent Get() calls
// are in progress when calling Reset().
func Reset() {
	configMu.Lock()
	defer configMu.Unlock()
	globalConfig = nil
	errConfig = nil
	configOnce = sync.Once{}
}

// Get returns the global config, loading it on first call.
// This is the primary way to access configuration throughout the codebase.
func Get() (*Config, error) {
	configOnce.Do(func() {
		globalConfig, errConfig = Load()
	})
	return globalConfig, errConfig
}

// Load loads configuration from QEMUBOX_CONFIG env var or /etc/qemubox/config.json.
// Missing files return an error for the caller to handle.
func Load() (*Config, error) {
	configPath := os.Getenv(ConfigEnvVar)
	if configPath == "" {
		configPath = DefaultConfigPath
	}

	return LoadFrom(configPath)
}

// LoadFrom loads configuration from a specific path.
// Returns error if file doesn't exist or is invalid.
func LoadFrom(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s. Please create a config file (see examples/config.json) or set %s environment variable", path, ConfigEnvVar)
		}
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w (ensure it's valid JSON)", path, err)
	}

	// Apply defaults for empty fields
	cfg.applyDefaults()

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration in %s: %w", path, err)
	}

	return &cfg, nil
}

// DefaultConfig returns the default configuration.
// This is primarily for reference and documentation - production code should use Get().
func DefaultConfig() *Config {
	return &Config{
		Paths: PathsConfig{
			ShareDir:      defaultShareDir,
			StateDir:      defaultStateDir,
			LogDir:        defaultLogDir,
			QEMUPath:      "", // Auto-discovered
			QEMUSharePath: "", // Auto-discovered
		},
		Runtime: RuntimeConfig{
			VMM: defaultVMM,
		},
		Timeouts: TimeoutsConfig{
			VMStart:         defaultVMStart,
			DeviceDetection: defaultDeviceDetection,
			ShutdownGrace:   defaultShutdownGrace,
			EventReconnect:  defaultEventReconnect,
			TaskClientRetry: defaultTaskClientRetry,
			IOWait:          defaultIOWait,
			QMPCommand:      defaultQMPCommand,
		},
		CPUHotplug: CPUHotplugConfig{
			MonitorInterval:      defaultCPUMonitorInterval,
			ScaleUpCooldown:      defaultCPUScaleUpCooldown,
			ScaleDownCooldown:    defaultCPUScaleDownCooldown,
			ScaleUpThreshold:     defaultCPUScaleUpThreshold,
			ScaleDownThreshold:   defaultCPUScaleDownThreshold,
			ScaleUpThrottleLimit: defaultCPUScaleUpThrottleLimit,
			ScaleUpStability:     defaultCPUScaleUpStability,
			ScaleDownStability:   defaultCPUScaleDownStability,
			EnableScaleDown:      true,
		},
		MemHotplug: MemHotplugConfig{
			MonitorInterval:    defaultMemMonitorInterval,
			ScaleUpCooldown:    defaultMemScaleUpCooldown,
			ScaleDownCooldown:  defaultMemScaleDownCooldown,
			ScaleUpThreshold:   defaultMemScaleUpThreshold,
			ScaleDownThreshold: defaultMemScaleDownThreshold,
			OOMSafetyMarginMB:  defaultMemOOMSafetyMarginMB,
			IncrementSizeMB:    defaultMemIncrementSizeMB,
			ScaleUpStability:   defaultMemScaleUpStability,
			ScaleDownStability: defaultMemScaleDownStability,
			EnableScaleDown:    false,
		},
	}
}

// Helper functions for applying defaults
func setDefault(s *string, dflt string) {
	if *s == "" {
		*s = dflt
	}
}

func setDefaultFloat(f *float64, dflt float64) {
	if *f == 0 {
		*f = dflt
	}
}

func setDefaultInt(i *int, dflt int) {
	if *i == 0 {
		*i = dflt
	}
}

func setDefaultInt64(i *int64, dflt int64) {
	if *i == 0 {
		*i = dflt
	}
}

// applyDefaults fills in default values for any empty fields
func (c *Config) applyDefaults() {
	// Paths (QEMUPath and QEMUSharePath are intentionally left empty for auto-discovery)
	setDefault(&c.Paths.ShareDir, defaultShareDir)
	setDefault(&c.Paths.StateDir, defaultStateDir)
	setDefault(&c.Paths.LogDir, defaultLogDir)

	// Runtime
	setDefault(&c.Runtime.VMM, defaultVMM)

	// Timeouts
	setDefault(&c.Timeouts.VMStart, defaultVMStart)
	setDefault(&c.Timeouts.DeviceDetection, defaultDeviceDetection)
	setDefault(&c.Timeouts.ShutdownGrace, defaultShutdownGrace)
	setDefault(&c.Timeouts.EventReconnect, defaultEventReconnect)
	setDefault(&c.Timeouts.TaskClientRetry, defaultTaskClientRetry)
	setDefault(&c.Timeouts.IOWait, defaultIOWait)
	setDefault(&c.Timeouts.QMPCommand, defaultQMPCommand)

	// CPU Hotplug
	setDefault(&c.CPUHotplug.MonitorInterval, defaultCPUMonitorInterval)
	setDefault(&c.CPUHotplug.ScaleUpCooldown, defaultCPUScaleUpCooldown)
	setDefault(&c.CPUHotplug.ScaleDownCooldown, defaultCPUScaleDownCooldown)
	setDefaultFloat(&c.CPUHotplug.ScaleUpThreshold, defaultCPUScaleUpThreshold)
	setDefaultFloat(&c.CPUHotplug.ScaleDownThreshold, defaultCPUScaleDownThreshold)
	setDefaultFloat(&c.CPUHotplug.ScaleUpThrottleLimit, defaultCPUScaleUpThrottleLimit)
	setDefaultInt(&c.CPUHotplug.ScaleUpStability, defaultCPUScaleUpStability)
	setDefaultInt(&c.CPUHotplug.ScaleDownStability, defaultCPUScaleDownStability)

	// Memory Hotplug
	setDefault(&c.MemHotplug.MonitorInterval, defaultMemMonitorInterval)
	setDefault(&c.MemHotplug.ScaleUpCooldown, defaultMemScaleUpCooldown)
	setDefault(&c.MemHotplug.ScaleDownCooldown, defaultMemScaleDownCooldown)
	setDefaultFloat(&c.MemHotplug.ScaleUpThreshold, defaultMemScaleUpThreshold)
	setDefaultFloat(&c.MemHotplug.ScaleDownThreshold, defaultMemScaleDownThreshold)
	setDefaultInt64(&c.MemHotplug.OOMSafetyMarginMB, defaultMemOOMSafetyMarginMB)
	setDefaultInt64(&c.MemHotplug.IncrementSizeMB, defaultMemIncrementSizeMB)
	setDefaultInt(&c.MemHotplug.ScaleUpStability, defaultMemScaleUpStability)
	setDefaultInt(&c.MemHotplug.ScaleDownStability, defaultMemScaleDownStability)
}
