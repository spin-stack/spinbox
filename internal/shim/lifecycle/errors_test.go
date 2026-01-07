package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

func TestSentinelErrors(t *testing.T) {
	// Test that sentinel errors are distinct
	sentinels := []error{
		ErrVMNotRunning,
		ErrVMAlreadyExists,
		ErrVMNotCreated,
		ErrDeviceTimeout,
		ErrCIDExhausted,
		ErrNetworkSetupFailed,
		ErrMountSetupFailed,
		ErrInvalidStateTransition,
		ErrShutdownInProgress,
		ErrCreationInProgress,
		ErrDeletionInProgress,
		ErrCleanupIncomplete,
	}

	for i, err1 := range sentinels {
		for j, err2 := range sentinels {
			if i != j && errors.Is(err1, err2) {
				t.Errorf("sentinel errors should be distinct: %v == %v", err1, err2)
			}
		}
	}
}

func TestShutdownError(t *testing.T) {
	inner := errors.New("connection refused")
	err := NewShutdownError(PhaseVMShutdown, inner)

	// Test Error() output
	errStr := err.Error()
	if !strings.Contains(errStr, "vm_shutdown") {
		t.Errorf("error message should contain phase: %s", errStr)
	}
	if !strings.Contains(errStr, "connection refused") {
		t.Errorf("error message should contain inner error: %s", errStr)
	}

	// Test Unwrap()
	if !errors.Is(err, inner) {
		t.Error("should unwrap to inner error")
	}
}

func TestShutdownResult(t *testing.T) {
	t.Run("empty result", func(t *testing.T) {
		result := &ShutdownResult{}

		if result.HasErrors() {
			t.Error("empty result should not have errors")
		}
		if result.Error() != "" {
			t.Error("empty result should have empty error string")
		}
		if result.AsError() != nil {
			t.Error("empty result AsError should return nil")
		}
	})

	t.Run("with errors", func(t *testing.T) {
		result := &ShutdownResult{}
		result.Add(PhaseHotplugStop, errors.New("hotplug error"))
		result.Add(PhaseVMShutdown, nil) // nil should be ignored
		result.Add(PhaseNetworkCleanup, errors.New("network error"))

		if !result.HasErrors() {
			t.Error("result should have errors")
		}
		if len(result.Errors) != 2 {
			t.Errorf("expected 2 errors, got %d", len(result.Errors))
		}

		phases := result.FailedPhases()
		if len(phases) != 2 {
			t.Errorf("expected 2 failed phases, got %d", len(phases))
		}
		if phases[0] != PhaseHotplugStop || phases[1] != PhaseNetworkCleanup {
			t.Errorf("unexpected failed phases: %v", phases)
		}

		errStr := result.Error()
		if !strings.Contains(errStr, "hotplug_stop") {
			t.Errorf("error should mention hotplug_stop: %s", errStr)
		}
		if !strings.Contains(errStr, "network_cleanup") {
			t.Errorf("error should mention network_cleanup: %s", errStr)
		}
		if result.AsError() == nil {
			t.Error("AsError should return non-nil for result with errors")
		}
	})

	t.Run("multiple adds", func(t *testing.T) {
		result := &ShutdownResult{}
		result.Add(PhaseHotplugStop, errors.New("err1"))
		result.Add(PhaseIOShutdown, errors.New("err2"))
		result.Add(PhaseVMShutdown, errors.New("err3"))

		if len(result.Errors) != 3 {
			t.Errorf("expected 3 errors, got %d", len(result.Errors))
		}
	})
}

func TestVMStartError(t *testing.T) {
	inner := errors.New("qemu exec failed")
	err := NewVMStartError("qemu_exec", inner)

	// Test Error() output
	errStr := err.Error()
	if !strings.Contains(errStr, "qemu_exec") {
		t.Errorf("error message should contain phase: %s", errStr)
	}
	if !strings.Contains(errStr, "qemu exec failed") {
		t.Errorf("error message should contain inner error: %s", errStr)
	}

	// Test Unwrap()
	if !errors.Is(err, inner) {
		t.Error("should unwrap to inner error")
	}
}

func TestStateTransitionErrorType(t *testing.T) {
	err := NewStateTransitionError("idle", "running", "creating")

	t.Run("matches ErrInvalidStateTransition", func(t *testing.T) {
		if !errors.Is(err, ErrInvalidStateTransition) {
			t.Error("should match ErrInvalidStateTransition")
		}
	})

	t.Run("does not match other sentinels", func(t *testing.T) {
		if errors.Is(err, ErrVMNotRunning) {
			t.Error("should not match ErrVMNotRunning")
		}
	})

	t.Run("error message format", func(t *testing.T) {
		expected := "invalid state transition from idle to running (current state: creating)"
		if err.Error() != expected {
			t.Errorf("error message = %q, want %q", err.Error(), expected)
		}
	})
}

func TestShutdownPhase_Values(t *testing.T) {
	// Ensure phases have expected string values (for logging/debugging)
	phases := map[ShutdownPhase]string{
		PhaseHotplugStop:    "hotplug_stop",
		PhaseIOShutdown:     "io_shutdown",
		PhaseConnClose:      "connection_close",
		PhaseVMShutdown:     "vm_shutdown",
		PhaseNetworkCleanup: "network_cleanup",
		PhaseMountCleanup:   "mount_cleanup",
		PhaseEventClose:     "event_close",
	}

	for phase, expected := range phases {
		if string(phase) != expected {
			t.Errorf("phase %v = %q, want %q", phase, string(phase), expected)
		}
	}
}
