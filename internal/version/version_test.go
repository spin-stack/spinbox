package version

import (
	"strings"
	"testing"
)

func TestShort(t *testing.T) {
	// Default value should be "dev"
	got := Short()
	if got != "dev" {
		t.Errorf("Short() = %q, want %q", got, "dev")
	}
}

func TestInfo(t *testing.T) {
	info := Info()

	// Should contain version
	if !strings.Contains(info, Version) {
		t.Errorf("Info() = %q, should contain version %q", info, Version)
	}

	// Should contain commit
	if !strings.Contains(info, GitCommit) {
		t.Errorf("Info() = %q, should contain commit %q", info, GitCommit)
	}

	// Should contain build date
	if !strings.Contains(info, BuildDate) {
		t.Errorf("Info() = %q, should contain build date %q", info, BuildDate)
	}

	// Should contain "go" for Go version
	if !strings.Contains(info, "go") {
		t.Errorf("Info() = %q, should contain Go version", info)
	}
}
