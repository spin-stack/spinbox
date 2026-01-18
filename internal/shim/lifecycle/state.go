// Package lifecycle manages VM instance lifecycle.
// This file implements the shim state machine with explicit state transitions.
package lifecycle

import (
	"fmt"
	"sync/atomic"

	"github.com/containerd/log"
)

// ShimState represents the lifecycle state of the shim.
// The state machine enforces valid transitions and prevents race conditions.
type ShimState int32

const (
	// StateIdle is the initial state before any container is created.
	StateIdle ShimState = iota

	// StateCreating indicates Create() is in progress.
	// Only one Create() can run at a time.
	StateCreating

	// StateCreated indicates the container has been created but Start() has not been called.
	// This is the state between successful Create() and Start() calls.
	// Previously tracked by the separate initStarted atomic.
	StateCreated

	// StateRunning indicates a container is running in the VM.
	// This is the steady-state for a functioning container.
	StateRunning

	// StateShuttingDown indicates shutdown has been initiated.
	// No new operations are accepted; cleanup is in progress.
	StateShuttingDown

	// StateDeleting indicates Delete() is in progress.
	// The container is being removed and the VM will be shut down.
	StateDeleting
)

// String returns a human-readable name for the state.
func (s ShimState) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateCreating:
		return "creating"
	case StateCreated:
		return "created"
	case StateRunning:
		return "running"
	case StateShuttingDown:
		return "shutting_down"
	case StateDeleting:
		return "deleting"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// StateMachine manages shim state transitions with atomic operations.
// It consolidates the scattered atomics (creationInProgress, deletionInProgress,
// eventsClosed, intentionalShutdown) into a single source of truth.
type StateMachine struct {
	state atomic.Int32

	// intentionalShutdown distinguishes clean shutdown from VM crash.
	// This is kept separate because it's checked frequently in error paths.
	intentionalShutdown atomic.Bool
}

// NewStateMachine creates a new state machine in the Idle state.
func NewStateMachine() *StateMachine {
	return &StateMachine{}
}

// State returns the current state.
func (sm *StateMachine) State() ShimState {
	return ShimState(sm.state.Load())
}

// IsIntentionalShutdown returns true if shutdown was initiated intentionally.
func (sm *StateMachine) IsIntentionalShutdown() bool {
	return sm.intentionalShutdown.Load()
}

// SetIntentionalShutdown marks the shutdown as intentional.
func (sm *StateMachine) SetIntentionalShutdown(v bool) {
	sm.intentionalShutdown.Store(v)
}

// Transition attempts to transition from the expected state to the new state.
// Returns nil on success, or an error if the transition is invalid.
//
// Valid transitions:
//   - Idle -> Creating (start container creation)
//   - Creating -> Running (container created successfully)
//   - Creating -> Idle (container creation failed)
//   - Running -> Deleting (start container deletion)
//   - Running -> ShuttingDown (shutdown initiated)
//   - Deleting -> ShuttingDown (deletion triggers shutdown)
//   - Any -> ShuttingDown (forced shutdown, e.g., VM crash)
func (sm *StateMachine) Transition(from, to ShimState) error {
	if !sm.isValidTransition(from, to) {
		current := sm.State()
		return NewStateTransitionError(from.String(), to.String(), current.String())
	}

	if !sm.state.CompareAndSwap(int32(from), int32(to)) {
		current := sm.State()
		return NewStateTransitionError(from.String(), to.String(), current.String())
	}

	log.L.WithField("from", from.String()).WithField("to", to.String()).Debug("state transition")
	return nil
}

// ForceTransition transitions to a new state regardless of current state.
// Use sparingly - mainly for shutdown scenarios.
func (sm *StateMachine) ForceTransition(to ShimState) ShimState {
	old := ShimState(sm.state.Swap(int32(to)))
	log.L.WithField("from", old.String()).WithField("to", to.String()).Debug("forced state transition")
	return old
}

// TryStartCreating attempts to transition from Idle to Creating.
// Returns true if successful, false if creation is already in progress or not allowed.
func (sm *StateMachine) TryStartCreating() bool {
	return sm.state.CompareAndSwap(int32(StateIdle), int32(StateCreating))
}

// TryStartDeleting attempts to transition to Deleting from either Created or Running state.
// Returns true if successful, false if not in a deletable state.
// A container can be deleted before Start() is called (Created state) or after (Running state).
func (sm *StateMachine) TryStartDeleting() bool {
	// Try from Running first (most common case)
	if sm.state.CompareAndSwap(int32(StateRunning), int32(StateDeleting)) {
		return true
	}
	// Try from Created (delete before Start was called)
	return sm.state.CompareAndSwap(int32(StateCreated), int32(StateDeleting))
}

// MarkCreated transitions from Creating to Created.
// Returns an error if not in Creating state.
// This indicates the task was successfully created but Start() has not been called yet.
func (sm *StateMachine) MarkCreated() error {
	if !sm.state.CompareAndSwap(int32(StateCreating), int32(StateCreated)) {
		return NewStateTransitionError(StateCreating.String(), StateCreated.String(), sm.State().String())
	}
	log.L.Debug("state: creating -> created")
	return nil
}

// MarkStarted transitions from Created to Running.
// Returns an error if not in Created state.
// This indicates the init process has been started via Start() RPC.
func (sm *StateMachine) MarkStarted() error {
	if !sm.state.CompareAndSwap(int32(StateCreated), int32(StateRunning)) {
		return NewStateTransitionError(StateCreated.String(), StateRunning.String(), sm.State().String())
	}
	log.L.Debug("state: created -> running")
	return nil
}

// MarkCreationFailed transitions from Creating back to Idle.
// Returns an error if not in Creating state.
func (sm *StateMachine) MarkCreationFailed() error {
	if !sm.state.CompareAndSwap(int32(StateCreating), int32(StateIdle)) {
		return NewStateTransitionError(StateCreating.String(), StateIdle.String(), sm.State().String())
	}
	log.L.Debug("state: creating -> idle (failed)")
	return nil
}

// IsCreating returns true if container creation is in progress.
func (sm *StateMachine) IsCreating() bool {
	return sm.State() == StateCreating
}

// IsDeleting returns true if container deletion is in progress.
func (sm *StateMachine) IsDeleting() bool {
	return sm.State() == StateDeleting
}

// IsRunning returns true if a container is running.
func (sm *StateMachine) IsRunning() bool {
	return sm.State() == StateRunning
}

// IsCreatedNotStarted returns true if the task has been created but Start() has not been called.
// This replaces the initStarted atomic that was previously used to track this state.
func (sm *StateMachine) IsCreatedNotStarted() bool {
	return sm.State() == StateCreated
}

// IsShuttingDown returns true if shutdown is in progress.
func (sm *StateMachine) IsShuttingDown() bool {
	return sm.State() == StateShuttingDown
}

// CanAcceptRequests returns true if the shim can accept new RPC requests.
// Returns false during shutdown or if no container is running.
func (sm *StateMachine) CanAcceptRequests() bool {
	state := sm.State()
	return state == StateRunning || state == StateCreating || state == StateCreated
}

// isValidTransition checks if a state transition is valid.
func (sm *StateMachine) isValidTransition(from, to ShimState) bool {
	// ShuttingDown can be reached from any state (forced shutdown)
	if to == StateShuttingDown {
		return true
	}

	switch from {
	case StateIdle:
		return to == StateCreating
	case StateCreating:
		return to == StateCreated || to == StateIdle
	case StateCreated:
		return to == StateRunning || to == StateShuttingDown
	case StateRunning:
		return to == StateDeleting || to == StateShuttingDown
	case StateDeleting:
		return to == StateShuttingDown
	case StateShuttingDown:
		return false // Terminal state
	default:
		return false
	}
}

// StateSnapshot captures the current state for diagnostic purposes.
type StateSnapshot struct {
	State               ShimState
	IntentionalShutdown bool
}

// Snapshot returns a snapshot of the current state.
func (sm *StateMachine) Snapshot() StateSnapshot {
	return StateSnapshot{
		State:               sm.State(),
		IntentionalShutdown: sm.intentionalShutdown.Load(),
	}
}
