package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestCleanupOrchestrator_ExecuteOrder(t *testing.T) {
	// Track the order of cleanup calls
	var mu sync.Mutex
	var order []string

	record := func(name string) CleanupFunc {
		return func(ctx context.Context) error {
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return nil
		}
	}

	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop:    record("hotplug"),
		IOShutdown:     record("io"),
		ConnClose:      record("conn"),
		VMShutdown:     record("vm"),
		NetworkCleanup: record("network"),
		MountCleanup:   record("mount"),
		EventClose:     record("event"),
	})

	result := c.Execute(context.Background())

	if result.HasErrors() {
		t.Errorf("unexpected errors: %v", result.Error())
	}

	// Verify order
	expectedOrder := []string{"hotplug", "io", "conn", "vm", "network", "mount", "event"}
	if len(order) != len(expectedOrder) {
		t.Fatalf("expected %d cleanup calls, got %d", len(expectedOrder), len(order))
	}

	for i, expected := range expectedOrder {
		if order[i] != expected {
			t.Errorf("cleanup order[%d] = %q, want %q", i, order[i], expected)
		}
	}
}

func TestCleanupOrchestrator_CollectsAllErrors(t *testing.T) {
	errHotplug := errors.New("hotplug error")
	errVM := errors.New("vm error")
	errNetwork := errors.New("network error")

	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop:    func(ctx context.Context) error { return errHotplug },
		IOShutdown:     func(ctx context.Context) error { return nil },
		ConnClose:      func(ctx context.Context) error { return nil },
		VMShutdown:     func(ctx context.Context) error { return errVM },
		NetworkCleanup: func(ctx context.Context) error { return errNetwork },
		MountCleanup:   func(ctx context.Context) error { return nil },
		EventClose:     func(ctx context.Context) error { return nil },
	})

	result := c.Execute(context.Background())

	if !result.HasErrors() {
		t.Error("expected errors")
	}

	// Should have 3 errors
	if len(result.Errors) != 3 {
		t.Errorf("expected 3 errors, got %d", len(result.Errors))
	}

	// Check failed phases
	failedPhases := result.FailedPhases()
	expected := []ShutdownPhase{PhaseHotplugStop, PhaseVMShutdown, PhaseNetworkCleanup}
	if len(failedPhases) != len(expected) {
		t.Fatalf("expected %d failed phases, got %d", len(expected), len(failedPhases))
	}
	for i, phase := range expected {
		if failedPhases[i] != phase {
			t.Errorf("failed phase[%d] = %v, want %v", i, failedPhases[i], phase)
		}
	}
}

func TestCleanupOrchestrator_Idempotent(t *testing.T) {
	callCount := 0
	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop: func(ctx context.Context) error {
			callCount++
			return nil
		},
	})

	// Execute twice
	c.Execute(context.Background())
	c.Execute(context.Background())

	// Should only be called once
	if callCount != 1 {
		t.Errorf("expected cleanup to be called once, got %d", callCount)
	}
}

func TestCleanupOrchestrator_NilFunctionsSkipped(t *testing.T) {
	c := NewCleanupOrchestrator(CleanupPhases{})

	result := c.Execute(context.Background())

	if result.HasErrors() {
		t.Errorf("unexpected errors with nil functions: %v", result.Error())
	}
}

func TestCleanupOrchestrator_PartialCleanup(t *testing.T) {
	var mu sync.Mutex
	var called []string

	record := func(name string) CleanupFunc {
		return func(ctx context.Context) error {
			mu.Lock()
			called = append(called, name)
			mu.Unlock()
			return nil
		}
	}

	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop:    record("hotplug"),
		IOShutdown:     record("io"),
		ConnClose:      record("conn"),
		VMShutdown:     record("vm"),
		NetworkCleanup: record("network"),
		MountCleanup:   record("mount"),
		EventClose:     record("event"),
	})

	// Start from VM shutdown phase
	result := c.ExecutePartial(context.Background(), PhaseVMShutdown)

	if result.HasErrors() {
		t.Errorf("unexpected errors: %v", result.Error())
	}

	// Should only call vm, network, mount, event
	expectedCalled := []string{"vm", "network", "mount", "event"}
	if len(called) != len(expectedCalled) {
		t.Fatalf("expected %d cleanup calls, got %d: %v", len(expectedCalled), len(called), called)
	}

	for i, expected := range expectedCalled {
		if called[i] != expected {
			t.Errorf("called[%d] = %q, want %q", i, called[i], expected)
		}
	}
}

func TestCleanupOrchestrator_Reset(t *testing.T) {
	callCount := 0
	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop: func(ctx context.Context) error {
			callCount++
			return nil
		},
	})

	c.Execute(context.Background())
	c.Reset()
	c.Execute(context.Background())

	if callCount != 2 {
		t.Errorf("expected cleanup to be called twice after reset, got %d", callCount)
	}
}

func TestCleanupOrchestrator_CompletedPhases(t *testing.T) {
	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop: func(ctx context.Context) error { return nil },
		VMShutdown:  func(ctx context.Context) error { return nil },
	})

	c.Execute(context.Background())

	completed := c.CompletedPhases()

	// Should have hotplug and vm (the only ones set)
	if len(completed) != 2 {
		t.Errorf("expected 2 completed phases, got %d", len(completed))
	}

	hasHotplug := false
	hasVM := false
	for _, p := range completed {
		if p == PhaseHotplugStop {
			hasHotplug = true
		}
		if p == PhaseVMShutdown {
			hasVM = true
		}
	}

	if !hasHotplug || !hasVM {
		t.Error("expected hotplug and vm phases to be completed")
	}
}

func TestCleanupOrchestrator_ContinuesAfterError(t *testing.T) {
	var called []string

	c := NewCleanupOrchestrator(CleanupPhases{
		HotplugStop: func(ctx context.Context) error {
			called = append(called, "hotplug")
			return errors.New("hotplug failed")
		},
		IOShutdown: func(ctx context.Context) error {
			called = append(called, "io")
			return nil
		},
		VMShutdown: func(ctx context.Context) error {
			called = append(called, "vm")
			return errors.New("vm failed")
		},
		NetworkCleanup: func(ctx context.Context) error {
			called = append(called, "network")
			return nil
		},
	})

	result := c.Execute(context.Background())

	// All should be called despite errors
	if len(called) != 4 {
		t.Errorf("expected 4 cleanup calls, got %d: %v", len(called), called)
	}

	// Should have 2 errors
	if len(result.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d", len(result.Errors))
	}
}

func TestShutdownResult_AsError(t *testing.T) {
	result := &ShutdownResult{}

	if result.AsError() != nil {
		t.Error("empty result should return nil error")
	}

	result.Add(PhaseVMShutdown, errors.New("vm error"))

	if result.AsError() == nil {
		t.Error("result with errors should return non-nil error")
	}
}

func TestShutdownError_Unwrap(t *testing.T) {
	inner := errors.New("inner error")
	err := NewShutdownError(PhaseVMShutdown, inner)

	if !errors.Is(err, inner) {
		t.Error("should be able to unwrap to inner error")
	}
}
