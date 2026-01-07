package metadata

import (
	"fmt"
	"strconv"
	"strings"
)

// OCI annotation key constants for qemubox configuration.
// These annotations are set at container creation time and are immutable.
// They allow users to override default VM resource configuration.
const (
	// AnnotationPrefix is the prefix for all qemubox OCI annotations.
	// Uses reverse-DNS notation per OCI convention.
	AnnotationPrefix = "qemubox.io/"

	// === Resource Configuration Annotations ===

	// AnnotationBootCPUs overrides the default number of boot vCPUs.
	// Value: integer string (e.g., "2")
	AnnotationBootCPUs = AnnotationPrefix + "boot-cpus"

	// AnnotationMaxCPUs overrides the maximum vCPUs for hotplug.
	// Value: integer string (e.g., "8")
	AnnotationMaxCPUs = AnnotationPrefix + "max-cpus"

	// AnnotationMemorySize overrides the initial memory allocation.
	// Value: size string with optional unit (e.g., "512Mi", "1Gi", "1073741824")
	AnnotationMemorySize = AnnotationPrefix + "memory-size"

	// AnnotationMemoryHotplugSize overrides the maximum hotplug memory.
	// Value: size string with optional unit (e.g., "2Gi", "4294967296")
	AnnotationMemoryHotplugSize = AnnotationPrefix + "memory-hotplug-size"

	// AnnotationMemorySlots overrides the number of memory hotplug slots.
	// Value: integer string (e.g., "8")
	AnnotationMemorySlots = AnnotationPrefix + "memory-slots"

	// === VM Configuration Annotations ===

	// AnnotationKernelPath specifies a custom kernel path.
	// Value: absolute path to kernel image
	AnnotationKernelPath = AnnotationPrefix + "kernel-path"

	// AnnotationInitrdPath specifies a custom initrd path.
	// Value: absolute path to initrd image
	AnnotationInitrdPath = AnnotationPrefix + "initrd-path"

	// AnnotationKernelArgs specifies additional kernel command line arguments.
	// Value: space-separated kernel arguments
	AnnotationKernelArgs = AnnotationPrefix + "kernel-args"

	// === Feature Flags ===

	// AnnotationDisableCPUHotplug disables CPU hotplug for this container.
	// Value: "true" or "false"
	AnnotationDisableCPUHotplug = AnnotationPrefix + "disable-cpu-hotplug"

	// AnnotationDisableMemoryHotplug disables memory hotplug for this container.
	// Value: "true" or "false"
	AnnotationDisableMemoryHotplug = AnnotationPrefix + "disable-memory-hotplug"
)

// AnnotationConfig holds parsed qemubox configuration from OCI annotations.
// Pointer fields are nil when the annotation was not specified.
type AnnotationConfig struct {
	// Resource configuration overrides
	BootCPUs          *int
	MaxCPUs           *int
	MemorySize        *int64
	MemoryHotplugSize *int64
	MemorySlots       *int

	// VM configuration overrides
	KernelPath *string
	InitrdPath *string
	KernelArgs *string

	// Feature flags
	DisableCPUHotplug    *bool
	DisableMemoryHotplug *bool
}

// ParseAnnotations extracts qemubox configuration from OCI spec annotations.
// Returns an error if any annotation has an invalid value.
// Missing annotations are not errors - the corresponding field will be nil.
func ParseAnnotations(annotations map[string]string) (*AnnotationConfig, error) {
	cfg := &AnnotationConfig{}

	for key, value := range annotations {
		// Skip non-qemubox annotations
		if !strings.HasPrefix(key, AnnotationPrefix) {
			continue
		}

		var err error
		switch key {
		case AnnotationBootCPUs:
			cfg.BootCPUs, err = parseIntPtr(value)
		case AnnotationMaxCPUs:
			cfg.MaxCPUs, err = parseIntPtr(value)
		case AnnotationMemorySize:
			cfg.MemorySize, err = parseMemorySize(value)
		case AnnotationMemoryHotplugSize:
			cfg.MemoryHotplugSize, err = parseMemorySize(value)
		case AnnotationMemorySlots:
			cfg.MemorySlots, err = parseIntPtr(value)
		case AnnotationKernelPath:
			cfg.KernelPath = strPtr(value)
		case AnnotationInitrdPath:
			cfg.InitrdPath = strPtr(value)
		case AnnotationKernelArgs:
			cfg.KernelArgs = strPtr(value)
		case AnnotationDisableCPUHotplug:
			cfg.DisableCPUHotplug, err = parseBoolPtr(value)
		case AnnotationDisableMemoryHotplug:
			cfg.DisableMemoryHotplug, err = parseBoolPtr(value)
		}

		if err != nil {
			return nil, fmt.Errorf("invalid annotation %s=%q: %w", key, value, err)
		}
	}

	return cfg, nil
}

// HasOverrides returns true if any configuration override is set.
func (c *AnnotationConfig) HasOverrides() bool {
	if c == nil {
		return false
	}
	return c.BootCPUs != nil ||
		c.MaxCPUs != nil ||
		c.MemorySize != nil ||
		c.MemoryHotplugSize != nil ||
		c.MemorySlots != nil ||
		c.KernelPath != nil ||
		c.InitrdPath != nil ||
		c.KernelArgs != nil ||
		c.DisableCPUHotplug != nil ||
		c.DisableMemoryHotplug != nil
}

// parseIntPtr parses an integer string and returns a pointer to it.
func parseIntPtr(s string) (*int, error) {
	v, err := strconv.Atoi(s)
	if err != nil {
		return nil, err
	}
	if v < 0 {
		return nil, fmt.Errorf("value must be non-negative")
	}
	return &v, nil
}

// parseBoolPtr parses a boolean string and returns a pointer to it.
func parseBoolPtr(s string) (*bool, error) {
	v, err := strconv.ParseBool(s)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// parseMemorySize parses a memory size string with optional unit suffix.
// Supported formats: "1073741824", "512Mi", "1Gi", "2G", "1024M"
func parseMemorySize(s string) (*int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty value")
	}

	// Try parsing as plain integer first
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		if v < 0 {
			return nil, fmt.Errorf("value must be non-negative")
		}
		return &v, nil
	}

	// Parse with unit suffix
	unit := strings.ToUpper(s[len(s)-1:])
	numStr := s[:len(s)-1]

	// Handle "Mi", "Gi" suffixes (binary units)
	if len(s) >= 2 && strings.HasSuffix(strings.ToUpper(s), "I") {
		unit = strings.ToUpper(s[len(s)-2:])
		numStr = s[:len(s)-2]
	}

	multiplier, err := unitMultiplier(unit)
	if err != nil {
		return nil, err
	}

	v, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number: %s", numStr)
	}
	if v < 0 {
		return nil, fmt.Errorf("value must be non-negative")
	}

	result := v * multiplier
	return &result, nil
}

// unitMultiplier returns the byte multiplier for a memory unit suffix.
func unitMultiplier(unit string) (int64, error) {
	switch unit {
	case "K":
		return 1000, nil
	case "KI":
		return 1024, nil
	case "M":
		return 1000 * 1000, nil
	case "MI":
		return 1024 * 1024, nil
	case "G":
		return 1000 * 1000 * 1000, nil
	case "GI":
		return 1024 * 1024 * 1024, nil
	default:
		return 0, fmt.Errorf("unknown unit suffix: %s", unit)
	}
}

// strPtr returns a pointer to the string.
func strPtr(s string) *string {
	return &s
}
