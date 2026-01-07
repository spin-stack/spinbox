// Package lifecycle manages VM instance lifecycle.
// This file defines sentinel errors and structured error types for lifecycle operations.
package lifecycle

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors for common lifecycle failures.
// Use errors.Is() to check for these error types.
var (
	// ErrVMNotRunning indicates an operation was attempted on a VM that is not running.
	ErrVMNotRunning = errors.New("VM not running")

	// ErrVMAlreadyExists indicates a VM creation was attempted when one already exists.
	ErrVMAlreadyExists = errors.New("VM already exists")

	// ErrVMNotCreated indicates an operation requires a VM but none has been created.
	ErrVMNotCreated = errors.New("VM not created")

	// ErrDeviceTimeout indicates device detection timed out during VM boot.
	ErrDeviceTimeout = errors.New("device detection timeout")

	// ErrCIDExhausted indicates the vsock CID pool is exhausted.
	ErrCIDExhausted = errors.New("vsock CID pool exhausted")

	// ErrNetworkSetupFailed indicates network setup (CNI) failed.
	ErrNetworkSetupFailed = errors.New("network setup failed")

	// ErrMountSetupFailed indicates mount transformation/setup failed.
	ErrMountSetupFailed = errors.New("mount setup failed")

	// ErrInvalidStateTransition indicates an invalid state machine transition was attempted.
	ErrInvalidStateTransition = errors.New("invalid state transition")

	// ErrShutdownInProgress indicates shutdown is already in progress.
	ErrShutdownInProgress = errors.New("shutdown in progress")

	// ErrCreationInProgress indicates container creation is already in progress.
	ErrCreationInProgress = errors.New("creation in progress")

	// ErrDeletionInProgress indicates container deletion is already in progress.
	ErrDeletionInProgress = errors.New("deletion in progress")

	// ErrCleanupIncomplete indicates cleanup did not fully complete.
	ErrCleanupIncomplete = errors.New("cleanup incomplete")
)

// ShutdownPhase identifies the phase of shutdown where an error occurred.
type ShutdownPhase string

const (
	PhaseHotplugStop    ShutdownPhase = "hotplug_stop"
	PhaseIOShutdown     ShutdownPhase = "io_shutdown"
	PhaseConnClose      ShutdownPhase = "connection_close"
	PhaseVMShutdown     ShutdownPhase = "vm_shutdown"
	PhaseNetworkCleanup ShutdownPhase = "network_cleanup"
	PhaseMountCleanup   ShutdownPhase = "mount_cleanup"
	PhaseEventClose     ShutdownPhase = "event_close"
)

// ShutdownError represents an error during the shutdown sequence.
// It captures which phase failed and the underlying error.
type ShutdownError struct {
	Phase ShutdownPhase
	Err   error
}

func (e *ShutdownError) Error() string {
	return fmt.Sprintf("shutdown failed at %s: %v", e.Phase, e.Err)
}

func (e *ShutdownError) Unwrap() error {
	return e.Err
}

// NewShutdownError creates a new shutdown error for the given phase.
func NewShutdownError(phase ShutdownPhase, err error) *ShutdownError {
	return &ShutdownError{Phase: phase, Err: err}
}

// ShutdownResult collects errors from all shutdown phases.
// Use this to report multiple failures during cleanup.
// Note: This type implements error but is primarily a result container, not an error type.
//
//nolint:errname // ShutdownResult is a result container that can be used as an error
type ShutdownResult struct {
	Errors []*ShutdownError
}

// Add records an error for a shutdown phase.
// Nil errors are ignored.
func (r *ShutdownResult) Add(phase ShutdownPhase, err error) {
	if err != nil {
		r.Errors = append(r.Errors, NewShutdownError(phase, err))
	}
}

// HasErrors returns true if any shutdown phase failed.
func (r *ShutdownResult) HasErrors() bool {
	return len(r.Errors) > 0
}

// Error implements the error interface.
// Returns a combined error message listing all failed phases.
func (r *ShutdownResult) Error() string {
	if !r.HasErrors() {
		return ""
	}
	var msgs []string
	for _, e := range r.Errors {
		msgs = append(msgs, e.Error())
	}
	return fmt.Sprintf("shutdown completed with errors: %s", strings.Join(msgs, "; "))
}

// AsError returns the ShutdownResult as an error, or nil if no errors occurred.
func (r *ShutdownResult) AsError() error {
	if !r.HasErrors() {
		return nil
	}
	return r
}

// FailedPhases returns the list of phases that failed.
func (r *ShutdownResult) FailedPhases() []ShutdownPhase {
	phases := make([]ShutdownPhase, 0, len(r.Errors))
	for _, e := range r.Errors {
		phases = append(phases, e.Phase)
	}
	return phases
}

// VMStartError represents an error during VM startup.
// It captures the phase of startup where the error occurred.
type VMStartError struct {
	Phase string // e.g., "config_validation", "qemu_exec", "qmp_connect", "vsock_connect"
	Err   error
}

func (e *VMStartError) Error() string {
	return fmt.Sprintf("VM start failed at %s: %v", e.Phase, e.Err)
}

func (e *VMStartError) Unwrap() error {
	return e.Err
}

// NewVMStartError creates a new VM start error for the given phase.
func NewVMStartError(phase string, err error) *VMStartError {
	return &VMStartError{Phase: phase, Err: err}
}

// StateTransitionError represents an invalid state transition attempt.
type StateTransitionError struct {
	From    string
	To      string
	Current string
}

func (e *StateTransitionError) Error() string {
	return fmt.Sprintf("invalid state transition from %s to %s (current state: %s)", e.From, e.To, e.Current)
}

func (e *StateTransitionError) Is(target error) bool {
	return target == ErrInvalidStateTransition
}

// NewStateTransitionError creates a new state transition error.
func NewStateTransitionError(from, to, current string) *StateTransitionError {
	return &StateTransitionError{From: from, To: to, Current: current}
}
