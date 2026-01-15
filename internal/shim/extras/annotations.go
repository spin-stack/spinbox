//go:build linux

package extras

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/opencontainers/runtime-spec/specs-go"
)

// Annotation keys for extras configuration.
const (
	// AnnotationFiles is a JSON object mapping destination paths to source configurations.
	// Format: {"<destPath>": {"source": "<sourcePath>", "mode": <mode>}, ...}
	// Example: {"/usr/local/bin/myapp": {"source": "/host/path/myapp", "mode": 493}}
	// Note: mode is decimal (493 = 0755 octal)
	AnnotationFiles = "io.spin.extras.files"
)

// FileConfig represents a single file configuration from annotations.
type FileConfig struct {
	Source string `json:"source"` // Source path on host
	Mode   int64  `json:"mode"`   // File permissions (decimal, e.g., 493 for 0755)
}

// FromAnnotations parses extra files configuration from OCI spec annotations.
// Returns nil if no extras are configured.
func FromAnnotations(spec *specs.Spec) ([]File, error) {
	if spec == nil || spec.Annotations == nil {
		return nil, nil
	}

	filesJSON, ok := spec.Annotations[AnnotationFiles]
	if !ok || filesJSON == "" {
		return nil, nil
	}

	// Parse JSON: map of destPath -> FileConfig
	var filesMap map[string]FileConfig
	if err := json.Unmarshal([]byte(filesJSON), &filesMap); err != nil {
		return nil, fmt.Errorf("parse %s annotation: %w", AnnotationFiles, err)
	}

	var files []File
	for destPath, cfg := range filesMap {
		// Validate destination path is absolute
		if !filepath.IsAbs(destPath) {
			return nil, fmt.Errorf("extras: destination path must be absolute: %s", destPath)
		}

		// Validate source path exists
		cleanSource := filepath.Clean(cfg.Source)
		if !filepath.IsAbs(cleanSource) {
			return nil, fmt.Errorf("extras: source path must be absolute: %s", cfg.Source)
		}
		if _, err := os.Stat(cleanSource); err != nil {
			return nil, fmt.Errorf("extras: source file not found %s: %w", cleanSource, err)
		}

		// Default mode to 0644 if not specified
		mode := cfg.Mode
		if mode == 0 {
			mode = 0644
		}

		files = append(files, NewFile(destPath, cleanSource, mode))
	}

	return files, nil
}
