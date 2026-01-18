package lifecycle

import (
	"errors"
	"sync"
	"testing"
)

func TestStateMachine_InitialState(t *testing.T) {
	sm := NewStateMachine()
	if sm.State() != StateIdle {
		t.Errorf("expected initial state Idle, got %s", sm.State())
	}
}

func TestStateMachine_ValidTransitions(t *testing.T) {
	tests := []struct {
		name    string
		from    ShimState
		to      ShimState
		wantErr bool
	}{
		{"idle to creating", StateIdle, StateCreating, false},
		{"creating to created", StateCreating, StateCreated, false},
		{"creating to idle (failure)", StateCreating, StateIdle, false},
		{"created to running", StateCreated, StateRunning, false},
		{"created to shutting down", StateCreated, StateShuttingDown, false},
		{"running to deleting", StateRunning, StateDeleting, false},
		{"running to shutting down", StateRunning, StateShuttingDown, false},
		{"deleting to shutting down", StateDeleting, StateShuttingDown, false},
		// Any state can transition to ShuttingDown
		{"idle to shutting down", StateIdle, StateShuttingDown, false},
		{"creating to shutting down", StateCreating, StateShuttingDown, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateMachine()
			// Set initial state
			sm.state.Store(int32(tt.from))

			err := sm.Transition(tt.from, tt.to)
			if (err != nil) != tt.wantErr {
				t.Errorf("Transition() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && sm.State() != tt.to {
				t.Errorf("expected state %s, got %s", tt.to, sm.State())
			}
		})
	}
}

func TestStateMachine_InvalidTransitions(t *testing.T) {
	tests := []struct {
		name string
		from ShimState
		to   ShimState
	}{
		{"idle to running (skip creating)", StateIdle, StateRunning},
		{"idle to deleting", StateIdle, StateDeleting},
		{"running to creating", StateRunning, StateCreating},
		{"running to idle", StateRunning, StateIdle},
		{"shutting down to anything", StateShuttingDown, StateIdle},
		{"shutting down to running", StateShuttingDown, StateRunning},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateMachine()
			sm.state.Store(int32(tt.from))

			err := sm.Transition(tt.from, tt.to)
			if err == nil {
				t.Errorf("expected error for invalid transition %s -> %s", tt.from, tt.to)
			}
			if !errors.Is(err, ErrInvalidStateTransition) {
				t.Errorf("expected ErrInvalidStateTransition, got %T", err)
			}
		})
	}
}

func TestStateMachine_ConcurrentTransitions(t *testing.T) {
	sm := NewStateMachine()

	// Try to start multiple Create() operations concurrently
	const goroutines = 100
	successes := make(chan bool, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			success := sm.TryStartCreating()
			successes <- success
		}()
	}

	wg.Wait()
	close(successes)

	// Exactly one should succeed
	successCount := 0
	for s := range successes {
		if s {
			successCount++
		}
	}

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful TryStartCreating, got %d", successCount)
	}

	if sm.State() != StateCreating {
		t.Errorf("expected state Creating, got %s", sm.State())
	}
}

func TestStateMachine_TryStartCreating(t *testing.T) {
	sm := NewStateMachine()

	// First call should succeed
	if !sm.TryStartCreating() {
		t.Error("first TryStartCreating should succeed")
	}

	// Second call should fail (already creating)
	if sm.TryStartCreating() {
		t.Error("second TryStartCreating should fail")
	}

	// After marking created, should still fail (not idle)
	if err := sm.MarkCreated(); err != nil {
		t.Errorf("MarkCreated failed: %v", err)
	}
	if sm.TryStartCreating() {
		t.Error("TryStartCreating should fail when running")
	}
}

func TestStateMachine_TryStartDeleting(t *testing.T) {
	t.Run("from idle", func(t *testing.T) {
		sm := NewStateMachine()
		// Should fail when idle
		if sm.TryStartDeleting() {
			t.Error("TryStartDeleting should fail when idle")
		}
	})

	t.Run("from created", func(t *testing.T) {
		sm := NewStateMachine()
		sm.TryStartCreating()
		if err := sm.MarkCreated(); err != nil {
			t.Errorf("MarkCreated failed: %v", err)
		}

		// Should succeed from Created state (before Start was called)
		if !sm.TryStartDeleting() {
			t.Error("TryStartDeleting should succeed when created")
		}

		// Second call should fail
		if sm.TryStartDeleting() {
			t.Error("second TryStartDeleting should fail")
		}
	})

	t.Run("from running", func(t *testing.T) {
		sm := NewStateMachine()
		sm.TryStartCreating()
		if err := sm.MarkCreated(); err != nil {
			t.Errorf("MarkCreated failed: %v", err)
		}
		if err := sm.MarkStarted(); err != nil {
			t.Errorf("MarkStarted failed: %v", err)
		}

		// Should succeed from Running state
		if !sm.TryStartDeleting() {
			t.Error("TryStartDeleting should succeed when running")
		}

		// Second call should fail
		if sm.TryStartDeleting() {
			t.Error("second TryStartDeleting should fail")
		}
	})
}

func TestStateMachine_MarkCreated(t *testing.T) {
	sm := NewStateMachine()
	sm.TryStartCreating()

	if err := sm.MarkCreated(); err != nil {
		t.Errorf("MarkCreated failed: %v", err)
	}

	if sm.State() != StateCreated {
		t.Errorf("expected Created after MarkCreated, got %s", sm.State())
	}
}

func TestStateMachine_MarkStarted(t *testing.T) {
	sm := NewStateMachine()
	sm.TryStartCreating()
	if err := sm.MarkCreated(); err != nil {
		t.Errorf("MarkCreated failed: %v", err)
	}

	if err := sm.MarkStarted(); err != nil {
		t.Errorf("MarkStarted failed: %v", err)
	}

	if sm.State() != StateRunning {
		t.Errorf("expected Running after MarkStarted, got %s", sm.State())
	}
}

func TestStateMachine_MarkStarted_InvalidState(t *testing.T) {
	sm := NewStateMachine()
	// Don't transition to Created first

	err := sm.MarkStarted()
	if err == nil {
		t.Error("MarkStarted should fail when not in Created state")
	}
	if !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("expected ErrInvalidStateTransition, got %T", err)
	}
}

func TestStateMachine_MarkCreated_InvalidState(t *testing.T) {
	sm := NewStateMachine()
	// Don't transition to Creating first

	err := sm.MarkCreated()
	if err == nil {
		t.Error("MarkCreated should fail when not in Creating state")
	}
	if !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("expected ErrInvalidStateTransition, got %T", err)
	}
}

func TestStateMachine_MarkCreationFailed(t *testing.T) {
	sm := NewStateMachine()
	sm.TryStartCreating()

	if err := sm.MarkCreationFailed(); err != nil {
		t.Errorf("MarkCreationFailed failed: %v", err)
	}

	if sm.State() != StateIdle {
		t.Errorf("expected Idle after MarkCreationFailed, got %s", sm.State())
	}
}

func TestStateMachine_MarkCreationFailed_InvalidState(t *testing.T) {
	sm := NewStateMachine()
	// Don't transition to Creating first

	err := sm.MarkCreationFailed()
	if err == nil {
		t.Error("MarkCreationFailed should fail when not in Creating state")
	}
	if !errors.Is(err, ErrInvalidStateTransition) {
		t.Errorf("expected ErrInvalidStateTransition, got %T", err)
	}
}

func TestStateMachine_ForceTransition(t *testing.T) {
	sm := NewStateMachine()
	sm.TryStartCreating()
	if err := sm.MarkCreated(); err != nil {
		t.Errorf("MarkCreated failed: %v", err)
	}
	if err := sm.MarkStarted(); err != nil {
		t.Errorf("MarkStarted failed: %v", err)
	}

	old := sm.ForceTransition(StateShuttingDown)

	if old != StateRunning {
		t.Errorf("ForceTransition should return old state Running, got %s", old)
	}
	if sm.State() != StateShuttingDown {
		t.Errorf("expected ShuttingDown after ForceTransition, got %s", sm.State())
	}
}

func TestStateMachine_IntentionalShutdown(t *testing.T) {
	sm := NewStateMachine()

	if sm.IsIntentionalShutdown() {
		t.Error("intentional shutdown should be false initially")
	}

	sm.SetIntentionalShutdown(true)

	if !sm.IsIntentionalShutdown() {
		t.Error("intentional shutdown should be true after setting")
	}
}

func TestStateMachine_StatePredicates(t *testing.T) {
	tests := []struct {
		name                string
		state               ShimState
		isCreating          bool
		isCreatedNotStarted bool
		isRunning           bool
		isDeleting          bool
		isShuttingDown      bool
		canAcceptRequests   bool
	}{
		{"idle", StateIdle, false, false, false, false, false, false},
		{"creating", StateCreating, true, false, false, false, false, true},
		{"created", StateCreated, false, true, false, false, false, true},
		{"running", StateRunning, false, false, true, false, false, true},
		{"deleting", StateDeleting, false, false, false, true, false, false},
		{"shutting_down", StateShuttingDown, false, false, false, false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := NewStateMachine()
			sm.state.Store(int32(tt.state))

			if got := sm.IsCreating(); got != tt.isCreating {
				t.Errorf("IsCreating() = %v, want %v", got, tt.isCreating)
			}
			if got := sm.IsCreatedNotStarted(); got != tt.isCreatedNotStarted {
				t.Errorf("IsCreatedNotStarted() = %v, want %v", got, tt.isCreatedNotStarted)
			}
			if got := sm.IsRunning(); got != tt.isRunning {
				t.Errorf("IsRunning() = %v, want %v", got, tt.isRunning)
			}
			if got := sm.IsDeleting(); got != tt.isDeleting {
				t.Errorf("IsDeleting() = %v, want %v", got, tt.isDeleting)
			}
			if got := sm.IsShuttingDown(); got != tt.isShuttingDown {
				t.Errorf("IsShuttingDown() = %v, want %v", got, tt.isShuttingDown)
			}
			if got := sm.CanAcceptRequests(); got != tt.canAcceptRequests {
				t.Errorf("CanAcceptRequests() = %v, want %v", got, tt.canAcceptRequests)
			}
		})
	}
}

func TestStateMachine_Snapshot(t *testing.T) {
	sm := NewStateMachine()
	sm.TryStartCreating()
	if err := sm.MarkCreated(); err != nil {
		t.Errorf("MarkCreated failed: %v", err)
	}
	if err := sm.MarkStarted(); err != nil {
		t.Errorf("MarkStarted failed: %v", err)
	}
	sm.SetIntentionalShutdown(true)

	snap := sm.Snapshot()

	if snap.State != StateRunning {
		t.Errorf("snapshot state should be Running, got %s", snap.State)
	}
	if !snap.IntentionalShutdown {
		t.Error("snapshot intentional shutdown should be true")
	}
}

func TestShimState_String(t *testing.T) {
	tests := []struct {
		state    ShimState
		expected string
	}{
		{StateIdle, "idle"},
		{StateCreating, "creating"},
		{StateCreated, "created"},
		{StateRunning, "running"},
		{StateShuttingDown, "shutting_down"},
		{StateDeleting, "deleting"},
		{ShimState(99), "unknown(99)"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("%v.String() = %q, want %q", tt.state, got, tt.expected)
		}
	}
}

func TestStateTransitionError(t *testing.T) {
	err := NewStateTransitionError("creating", "deleting", "running")

	if !errors.Is(err, ErrInvalidStateTransition) {
		t.Error("StateTransitionError should match ErrInvalidStateTransition")
	}

	expected := "invalid state transition from creating to deleting (current state: running)"
	if err.Error() != expected {
		t.Errorf("error message = %q, want %q", err.Error(), expected)
	}
}
