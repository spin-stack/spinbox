// Package version provides version information for spinbox binaries.
// Variables are set via ldflags at build time.
package version

import (
	"fmt"
	"runtime"
)

// These variables are set via ldflags at build time.
// Example: go build -ldflags "-X github.com/spin-stack/spinbox/internal/version.Version=v1.0.0"
var (
	// Version is the semantic version (e.g., "v1.0.0" or "dev").
	Version = "dev"

	// GitCommit is the git commit SHA.
	GitCommit = "unknown"

	// BuildDate is the build timestamp in RFC3339 format.
	BuildDate = "unknown"
)

// Info returns a formatted version string.
func Info() string {
	return fmt.Sprintf("%s (commit: %s, built: %s, go: %s)",
		Version, GitCommit, BuildDate, runtime.Version())
}

// Short returns just the version string.
func Short() string {
	return Version
}
