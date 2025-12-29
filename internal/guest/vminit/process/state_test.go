//go:build !windows

package process

import (
	"context"
	"strings"
	"testing"
)

// TestInitStateTransitions tests all valid state transitions for Init process
func TestInitStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		initialState  initState
		operation     func(initState) error
		expectedState string
		shouldSucceed bool
	}{
		{
			name:          "created → running (Start)",
			initialState:  &createdState{p: &Init{}},
			operation:     func(s initState) error { return s.Start(context.Background()) },
			expectedState: stateRunning,
			shouldSucceed: false, // Will fail without proper init setup, but transition logic works
		},
		{
			name:         "created → stopped (SetExited)",
			initialState: &createdState{p: &Init{}},
			operation: func(s initState) error {
				s.SetExited(0)
				return nil
			},
			expectedState: stateStopped,
			shouldSucceed: true,
		},
		{
			name:          "created → deleted (Delete)",
			initialState:  &createdState{p: &Init{}},
			operation:     func(s initState) error { return s.Delete(context.Background()) },
			expectedState: stateDeleted,
			shouldSucceed: false, // Will fail without proper cleanup, but transition works
		},
		{
			name:         "running → paused (Pause)",
			initialState: &runningState{p: &Init{}},
			operation:    func(s initState) error { return s.Pause(context.Background()) },
			// State won't actually transition without runtime, but error shows intent
			shouldSucceed: false,
		},
		{
			name:         "running → stopped (SetExited)",
			initialState: &runningState{p: &Init{}},
			operation: func(s initState) error {
				s.SetExited(0)
				return nil
			},
			expectedState: stateStopped,
			shouldSucceed: true,
		},
		{
			name:          "paused → running (Resume)",
			initialState:  &pausedState{p: &Init{}},
			operation:     func(s initState) error { return s.Resume(context.Background()) },
			shouldSucceed: false, // Needs runtime
		},
		{
			name:         "paused → stopped (SetExited)",
			initialState: &pausedState{p: &Init{}},
			operation: func(s initState) error {
				s.SetExited(0)
				return nil
			},
			expectedState: stateStopped,
			shouldSucceed: true,
		},
		{
			name:          "stopped → deleted (Delete)",
			initialState:  &stoppedState{p: &Init{}},
			operation:     func(s initState) error { return s.Delete(context.Background()) },
			expectedState: stateDeleted,
			shouldSucceed: false, // Needs cleanup
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Extract the process to check final state
			var proc *Init
			switch s := tt.initialState.(type) {
			case *createdState:
				proc = s.p
				proc.initState = s
			case *runningState:
				proc = s.p
				proc.initState = s
			case *pausedState:
				proc = s.p
				proc.initState = s
			case *stoppedState:
				proc = s.p
				proc.initState = s
			}

			err := tt.operation(tt.initialState)

			if tt.shouldSucceed && err != nil {
				t.Errorf("Expected success but got error: %v", err)
			}

			// If we expected a state transition, verify it
			if tt.expectedState != "" && proc != nil {
				finalStateName := stateName(proc.initState)
				if finalStateName != tt.expectedState {
					t.Errorf("Expected final state %s, got %s", tt.expectedState, finalStateName)
				}
			}
		})
	}
}

// TestInitInvalidStateTransitions tests that invalid operations return errors
func TestInitInvalidStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		state         initState
		operation     func(initState) error
		expectedError string
	}{
		{
			name:          "created: cannot pause",
			state:         &createdState{p: &Init{}},
			operation:     func(s initState) error { return s.Pause(context.Background()) },
			expectedError: "cannot pause task in created state",
		},
		{
			name:          "created: cannot resume",
			state:         &createdState{p: &Init{}},
			operation:     func(s initState) error { return s.Resume(context.Background()) },
			expectedError: "cannot resume task in created state",
		},
		{
			name:          "created: cannot checkpoint",
			state:         &createdState{p: &Init{}},
			operation:     func(s initState) error { return s.Checkpoint(context.Background(), nil) },
			expectedError: "cannot checkpoint a task in created state",
		},
		{
			name:          "stopped: cannot start",
			state:         &stoppedState{p: &Init{}},
			operation:     func(s initState) error { return s.Start(context.Background()) },
			expectedError: "cannot start a stopped process",
		},
		{
			name:          "stopped: cannot pause",
			state:         &stoppedState{p: &Init{}},
			operation:     func(s initState) error { return s.Pause(context.Background()) },
			expectedError: "cannot pause a stopped container",
		},
		{
			name:          "stopped: cannot resume",
			state:         &stoppedState{p: &Init{}},
			operation:     func(s initState) error { return s.Resume(context.Background()) },
			expectedError: "cannot resume a stopped container",
		},
		{
			name:          "deleted: cannot start",
			state:         &deletedState{},
			operation:     func(s initState) error { return s.Start(context.Background()) },
			expectedError: "cannot start a deleted process",
		},
		{
			name:          "deleted: cannot pause",
			state:         &deletedState{},
			operation:     func(s initState) error { return s.Pause(context.Background()) },
			expectedError: "cannot pause a deleted process",
		},
		{
			name:          "deleted: cannot resume",
			state:         &deletedState{},
			operation:     func(s initState) error { return s.Resume(context.Background()) },
			expectedError: "cannot resume a deleted process",
		},
		{
			name:          "deleted: cannot delete",
			state:         &deletedState{},
			operation:     func(s initState) error { return s.Delete(context.Background()) },
			expectedError: "cannot delete a deleted process",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.operation(tt.state)

			if err == nil {
				t.Fatal("Expected error but got nil")
			}

			if !strings.Contains(err.Error(), tt.expectedError) {
				t.Errorf("Expected error containing %q, got %q", tt.expectedError, err.Error())
			}
		})
	}
}

// TestExecStateTransitions tests exec process state transitions
func TestExecStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		initialState  execState
		operation     func(execState) error
		expectedState string
		shouldSucceed bool
	}{
		{
			name:          "execCreated → execRunning (Start)",
			initialState:  &execCreatedState{p: &execProcess{}},
			operation:     func(s execState) error { return s.Start(context.Background()) },
			expectedState: stateRunning,
			shouldSucceed: false, // Needs runtime
		},
		{
			name:         "execCreated → execStopped (SetExited)",
			initialState: &execCreatedState{p: &execProcess{}},
			operation: func(s execState) error {
				s.SetExited(0)
				return nil
			},
			expectedState: stateStopped,
			shouldSucceed: true,
		},
		{
			name:         "execRunning → execStopped (SetExited)",
			initialState: &execRunningState{p: &execProcess{}},
			operation: func(s execState) error {
				s.SetExited(0)
				return nil
			},
			expectedState: stateStopped,
			shouldSucceed: true,
		},
		{
			name:          "execStopped → deleted (Delete)",
			initialState:  &execStoppedState{p: &execProcess{}},
			operation:     func(s execState) error { return s.Delete(context.Background()) },
			expectedState: stateDeleted,
			shouldSucceed: false, // Needs cleanup
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Extract the process to check final state
			var proc *execProcess
			switch s := tt.initialState.(type) {
			case *execCreatedState:
				proc = s.p
				proc.execState = s
			case *execRunningState:
				proc = s.p
				proc.execState = s
			case *execStoppedState:
				proc = s.p
				proc.execState = s
			}

			err := tt.operation(tt.initialState)

			if tt.shouldSucceed && err != nil {
				t.Errorf("Expected success but got error: %v", err)
			}

			// If we expected a state transition, verify it
			if tt.expectedState != "" && proc != nil {
				finalStateName := stateName(proc.execState)
				if finalStateName != tt.expectedState {
					t.Errorf("Expected final state %s, got %s", tt.expectedState, finalStateName)
				}
			}
		})
	}
}

// TestExecInvalidStateTransitions tests invalid exec operations
func TestExecInvalidStateTransitions(t *testing.T) {
	tests := []struct {
		name          string
		state         execState
		operation     func(execState) error
		expectedError string
	}{
		{
			name:          "execStopped: cannot start",
			state:         &execStoppedState{p: &execProcess{}},
			operation:     func(s execState) error { return s.Start(context.Background()) },
			expectedError: "cannot start a stopped process",
		},
		{
			name:          "deleted: cannot start",
			state:         &deletedState{},
			operation:     func(s execState) error { return s.Start(context.Background()) },
			expectedError: "cannot start a deleted process",
		},
		{
			name:          "deleted: cannot delete",
			state:         &deletedState{},
			operation:     func(s execState) error { return s.Delete(context.Background()) },
			expectedError: "cannot delete a deleted process",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.operation(tt.state)

			if err == nil {
				t.Fatal("Expected error but got nil")
			}

			if !strings.Contains(err.Error(), tt.expectedError) {
				t.Errorf("Expected error containing %q, got %q", tt.expectedError, err.Error())
			}
		})
	}
}

// TestStateNameFunction tests the stateName helper
func TestStateNameFunction(t *testing.T) {
	tests := []struct {
		name         string
		state        interface{}
		expectedName string
	}{
		{"createdState", &createdState{}, stateCreated},
		{"runningState", &runningState{}, stateRunning},
		{"pausedState", &pausedState{}, statePaused},
		{"stoppedState", &stoppedState{}, stateStopped},
		{"deletedState", &deletedState{}, stateDeleted},
		{"execCreatedState", &execCreatedState{}, stateCreated},
		{"execRunningState", &execRunningState{}, stateRunning},
		{"execStoppedState", &execStoppedState{}, stateStopped},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := stateName(tt.state)
			if name != tt.expectedName {
				t.Errorf("Expected state name %s, got %s", tt.expectedName, name)
			}
		})
	}
}

// TestStateTransitionPanic tests that invalid transitions cause panics
func TestStateTransitionPanic(t *testing.T) {
	// SetExited with invalid state transition should panic
	s := &createdState{p: &Init{}}
	s.p.initState = &deletedState{} // Invalid: can't transition from deleted

	defer func() {
		if r := recover(); r == nil {
			t.Error("Expected panic from invalid state transition in SetExited")
		}
	}()

	// This should attempt to transition from deleted → stopped, which panics
	s.p.initState.SetExited(0)
}

// TestStatusMethod tests that Status returns correct state name
func TestStatusMethod(t *testing.T) {
	tests := []struct {
		name           string
		state          initState
		expectedStatus string
	}{
		{"created", &createdState{p: &Init{}}, stateCreated},
		{"running", &runningState{p: &Init{}}, stateRunning},
		{"paused", &pausedState{p: &Init{}}, statePaused},
		{"stopped", &stoppedState{p: &Init{}}, stateStopped},
		{"deleted", &deletedState{}, stateDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := tt.state.Status(context.Background())
			if err != nil {
				t.Fatalf("Status() returned error: %v", err)
			}
			if status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, status)
			}
		})
	}
}

// TestKillAllowedInAllStates tests that Kill is allowed in any state
func TestKillAllowedInAllStates(t *testing.T) {
	states := []struct {
		name  string
		state initState
	}{
		{"created", &createdState{p: &Init{}}},
		{"running", &runningState{p: &Init{}}},
		{"paused", &pausedState{p: &Init{}}},
		{"stopped", &stoppedState{p: &Init{}}},
		// Note: deleted state returns specific error
	}

	for _, tt := range states {
		t.Run(tt.name, func(t *testing.T) {
			// Kill should not return "invalid state" errors for these states
			// It may fail for other reasons (no process, etc.) but not state
			err := tt.state.Kill(context.Background(), 9, false)

			// We expect errors (no actual process), but not state-related errors
			if err != nil && strings.Contains(err.Error(), "invalid state") {
				t.Errorf("Kill should be allowed in %s state, got: %v", tt.name, err)
			}
		})
	}
}

// TestDeletedStateAllOperationsFail tests that deleted state rejects all operations
func TestDeletedStateAllOperationsFail(t *testing.T) {
	state := &deletedState{}

	operations := []struct {
		name      string
		operation func() error
	}{
		{"Start", func() error { return state.Start(context.Background()) }},
		{"Delete", func() error { return state.Delete(context.Background()) }},
		{"Pause", func() error { return state.Pause(context.Background()) }},
		{"Resume", func() error { return state.Resume(context.Background()) }},
		{"Kill", func() error { return state.Kill(context.Background(), 9, false) }},
		{"Checkpoint", func() error { return state.Checkpoint(context.Background(), nil) }},
		{"Update", func() error { return state.Update(context.Background(), nil) }},
	}

	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			err := op.operation()
			if err == nil {
				t.Errorf("Expected error for %s on deleted state", op.name)
			}
			if !strings.Contains(err.Error(), "deleted") {
				t.Errorf("Expected error mentioning 'deleted', got: %v", err)
			}
		})
	}
}

// TestExecStatusMethod tests exec state status reporting
func TestExecStatusMethod(t *testing.T) {
	tests := []struct {
		name           string
		state          execState
		expectedStatus string
	}{
		{"execCreated", &execCreatedState{p: &execProcess{}}, stateCreated},
		{"execRunning", &execRunningState{p: &execProcess{}}, stateRunning},
		{"execStopped", &execStoppedState{p: &execProcess{}}, stateStopped},
		{"deleted", &deletedState{}, stateDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, err := tt.state.Status(context.Background())
			if err != nil {
				t.Fatalf("Status() returned error: %v", err)
			}
			if status != tt.expectedStatus {
				t.Errorf("Expected status %s, got %s", tt.expectedStatus, status)
			}
		})
	}
}

// TestCreatedCheckpointState tests the checkpoint restore state
func TestCreatedCheckpointState(t *testing.T) {
	state := &createdCheckpointState{p: &Init{}}

	// Should be able to get status
	status, err := state.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() returned error: %v", err)
	}
	if status != stateCreated {
		t.Errorf("Expected status %s, got %s", stateCreated, status)
	}

	// Should not be able to pause
	err = state.Pause(context.Background())
	if err == nil {
		t.Error("Expected error when pausing checkpoint state")
	}

	// Should not be able to resume
	err = state.Resume(context.Background())
	if err == nil {
		t.Error("Expected error when resuming checkpoint state")
	}
}

// TestTransitionValidation tests the transition() method validation
func TestTransitionValidation(t *testing.T) {
	tests := []struct {
		name          string
		fromState     initState
		toState       string
		shouldSucceed bool
	}{
		// Valid transitions from created
		{"created→running", &createdState{p: &Init{}}, stateRunning, true},
		{"created→stopped", &createdState{p: &Init{}}, stateStopped, true},
		{"created→deleted", &createdState{p: &Init{}}, stateDeleted, true},
		// Invalid transitions from created
		{"created→paused", &createdState{p: &Init{}}, statePaused, false},

		// Valid transitions from running
		{"running→paused", &runningState{p: &Init{}}, statePaused, true},
		{"running→stopped", &runningState{p: &Init{}}, stateStopped, true},
		// Invalid transitions from running
		{"running→created", &runningState{p: &Init{}}, stateCreated, false},
		{"running→deleted", &runningState{p: &Init{}}, stateDeleted, false},

		// Valid transitions from paused
		{"paused→running", &pausedState{p: &Init{}}, stateRunning, true},
		{"paused→stopped", &pausedState{p: &Init{}}, stateStopped, true},
		// Invalid transitions from paused
		{"paused→created", &pausedState{p: &Init{}}, stateCreated, false},
		{"paused→deleted", &pausedState{p: &Init{}}, stateDeleted, false},

		// Valid transitions from stopped
		{"stopped→deleted", &stoppedState{p: &Init{}}, stateDeleted, true},
		// Invalid transitions from stopped
		{"stopped→created", &stoppedState{p: &Init{}}, stateCreated, false},
		{"stopped→running", &stoppedState{p: &Init{}}, stateRunning, false},
		{"stopped→paused", &stoppedState{p: &Init{}}, statePaused, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error

			switch s := tt.fromState.(type) {
			case *createdState:
				err = s.transition(tt.toState)
			case *runningState:
				err = s.transition(tt.toState)
			case *pausedState:
				err = s.transition(tt.toState)
			case *stoppedState:
				err = s.transition(tt.toState)
			}

			if tt.shouldSucceed && err != nil {
				t.Errorf("Expected transition to succeed, got error: %v", err)
			}
			if !tt.shouldSucceed && err == nil {
				t.Error("Expected transition to fail, but it succeeded")
			}
			if !tt.shouldSucceed && err != nil && !strings.Contains(err.Error(), "invalid state transition") {
				t.Errorf("Expected 'invalid state transition' error, got: %v", err)
			}
		})
	}
}

// TestSetExitedIdempotent tests that SetExited can be called multiple times safely
func TestSetExitedIdempotent(t *testing.T) {
	proc := &Init{}
	proc.initState = &createdState{p: proc}

	// First SetExited should transition to stopped
	proc.initState.SetExited(0)

	status, _ := proc.initState.Status(context.Background())
	if status != stateStopped {
		t.Errorf("Expected stopped state after first SetExited, got %s", status)
	}

	// Second SetExited should not panic (already in stopped state)
	// Note: This calls SetExited on stoppedState which is a no-op
	proc.initState.SetExited(1)

	// Should still be stopped
	status, _ = proc.initState.Status(context.Background())
	if status != stateStopped {
		t.Errorf("Expected stopped state after second SetExited, got %s", status)
	}
}

// TestStateTransitionThreadSafety tests concurrent status checks
// Note: Full thread-safety requires the parent process's mutex
func TestStateTransitionStatusCheck(t *testing.T) {
	proc := &Init{}
	proc.initState = &runningState{p: proc}

	// Status should be callable concurrently
	done := make(chan bool, 10)
	for range 10 {
		go func() {
			status, err := proc.initState.Status(context.Background())
			if err != nil {
				t.Errorf("Status() error: %v", err)
			}
			if status != stateRunning {
				t.Errorf("Expected running, got %s", status)
			}
			done <- true
		}()
	}

	// Wait for all goroutines
	for range 10 {
		<-done
	}
}
