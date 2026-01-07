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

// TryStartDeleting attempts to transition from Running to Deleting.
// Returns true if successful, false if not in Running state.
func (sm *StateMachine) TryStartDeleting() bool {
	return sm.state.CompareAndSwap(int32(StateRunning), int32(StateDeleting))
}

// MarkCreated transitions from Creating to Running.
// Panics if not in Creating state (programming error).
func (sm *StateMachine) MarkCreated() {
	if !sm.state.CompareAndSwap(int32(StateCreating), int32(StateRunning)) {
		panic(fmt.Sprintf("MarkCreated called in invalid state: %s", sm.State()))
	}
	log.L.Debug("state: creating -> running")
}

// MarkCreationFailed transitions from Creating back to Idle.
// Panics if not in Creating state (programming error).
func (sm *StateMachine) MarkCreationFailed() {
	if !sm.state.CompareAndSwap(int32(StateCreating), int32(StateIdle)) {
		panic(fmt.Sprintf("MarkCreationFailed called in invalid state: %s", sm.State()))
	}
	log.L.Debug("state: creating -> idle (failed)")
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

// IsShuttingDown returns true if shutdown is in progress.
func (sm *StateMachine) IsShuttingDown() bool {
	return sm.State() == StateShuttingDown
}

// CanAcceptRequests returns true if the shim can accept new RPC requests.
// Returns false during shutdown or if no container is running.
func (sm *StateMachine) CanAcceptRequests() bool {
	state := sm.State()
	return state == StateRunning || state == StateCreating
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
		return to == StateRunning || to == StateIdle
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
