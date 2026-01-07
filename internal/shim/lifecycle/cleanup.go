// Package lifecycle manages VM instance lifecycle.
// This file implements the cleanup orchestrator with explicit dependency ordering.
package lifecycle

import (
	"context"
	"slices"
	"sync"

	"github.com/containerd/log"
)

// CleanupFunc is a function that performs a cleanup operation.
// It should be idempotent - safe to call multiple times.
type CleanupFunc func(ctx context.Context) error

// cleanupPhase represents a single cleanup phase with its function and state.
type cleanupPhase struct {
	name ShutdownPhase
	fn   CleanupFunc
	done bool
}

// CleanupOrchestrator manages the shutdown sequence with explicit ordering.
// It ensures that cleanup operations happen in the correct dependency order
// and collects all errors for reporting.
//
// Cleanup order (dependencies flow left to right):
//
//	hotplug -> io -> connection -> vm -> network -> mounts -> events
//
// Each step depends on the previous steps completing first.
type CleanupOrchestrator struct {
	mu     sync.Mutex
	phases []cleanupPhase
}

// phaseOrder defines the canonical ordering of cleanup phases.
var phaseOrder = []ShutdownPhase{
	PhaseHotplugStop,
	PhaseIOShutdown,
	PhaseConnClose,
	PhaseVMShutdown,
	PhaseNetworkCleanup,
	PhaseMountCleanup,
	PhaseEventClose,
}

// CleanupPhases configures all cleanup functions at once.
// Keys are phase names, values are the cleanup functions.
// Phases not included will be skipped during cleanup.
type CleanupPhases struct {
	HotplugStop    CleanupFunc
	IOShutdown     CleanupFunc
	ConnClose      CleanupFunc
	VMShutdown     CleanupFunc
	NetworkCleanup CleanupFunc
	MountCleanup   CleanupFunc
	EventClose     CleanupFunc
}

// NewCleanupOrchestrator creates a new cleanup orchestrator with the given phases.
func NewCleanupOrchestrator(phases CleanupPhases) *CleanupOrchestrator {
	return &CleanupOrchestrator{
		phases: []cleanupPhase{
			{name: PhaseHotplugStop, fn: phases.HotplugStop},
			{name: PhaseIOShutdown, fn: phases.IOShutdown},
			{name: PhaseConnClose, fn: phases.ConnClose},
			{name: PhaseVMShutdown, fn: phases.VMShutdown},
			{name: PhaseNetworkCleanup, fn: phases.NetworkCleanup},
			{name: PhaseMountCleanup, fn: phases.MountCleanup},
			{name: PhaseEventClose, fn: phases.EventClose},
		},
	}
}

// Execute runs the complete cleanup sequence in dependency order.
// Returns a ShutdownResult containing any errors that occurred.
// All phases are attempted even if earlier phases fail.
func (c *CleanupOrchestrator) Execute(ctx context.Context) *ShutdownResult {
	return c.executeFrom(ctx, 0)
}

// ExecutePartial runs cleanup starting from a specific phase.
// Useful for cleanup after partial failures during creation.
func (c *CleanupOrchestrator) ExecutePartial(ctx context.Context, startPhase ShutdownPhase) *ShutdownResult {
	startIdx := slices.Index(phaseOrder, startPhase)
	if startIdx < 0 {
		// Unknown phase, run everything
		startIdx = 0
	}
	return c.executeFrom(ctx, startIdx)
}

// executeFrom runs cleanup starting from the given index.
func (c *CleanupOrchestrator) executeFrom(ctx context.Context, startIdx int) *ShutdownResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := &ShutdownResult{}
	logger := log.G(ctx)

	for i := startIdx; i < len(c.phases); i++ {
		phase := &c.phases[i]

		// Skip if no function registered or already done
		if phase.fn == nil || phase.done {
			continue
		}

		logger.WithField("phase", string(phase.name)).Debug("cleanup: executing phase")
		phase.done = true

		if err := phase.fn(ctx); err != nil {
			result.Add(phase.name, err)
		}
	}

	if result.HasErrors() {
		logger.WithField("failed_phases", result.FailedPhases()).Warn("cleanup completed with errors")
	} else {
		logger.Debug("cleanup completed successfully")
	}

	return result
}

// Reset clears all completion flags, allowing cleanup to run again.
// This is primarily for testing.
func (c *CleanupOrchestrator) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range c.phases {
		c.phases[i].done = false
	}
}

// CompletedPhases returns a list of phases that have been completed.
func (c *CleanupOrchestrator) CompletedPhases() []ShutdownPhase {
	c.mu.Lock()
	defer c.mu.Unlock()

	var phases []ShutdownPhase
	for _, phase := range c.phases {
		if phase.done {
			phases = append(phases, phase.name)
		}
	}
	return phases
}
