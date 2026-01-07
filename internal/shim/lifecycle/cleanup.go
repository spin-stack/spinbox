// Package lifecycle manages VM instance lifecycle.
// This file implements the cleanup orchestrator with explicit dependency ordering.
package lifecycle

import (
	"context"
	"sync/atomic"

	"github.com/containerd/log"
)

// CleanupFunc is a function that performs a cleanup operation.
// It should be idempotent - safe to call multiple times.
type CleanupFunc func(ctx context.Context) error

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
	// Cleanup functions for each phase
	hotplugStop    CleanupFunc
	ioShutdown     CleanupFunc
	connClose      CleanupFunc
	vmShutdown     CleanupFunc
	networkCleanup CleanupFunc
	mountCleanup   CleanupFunc
	eventClose     CleanupFunc

	// Track which phases have been completed (for idempotency)
	hotplugDone atomic.Bool
	ioDone      atomic.Bool
	connDone    atomic.Bool
	vmDone      atomic.Bool
	networkDone atomic.Bool
	mountDone   atomic.Bool
	eventDone   atomic.Bool
}

// NewCleanupOrchestrator creates a new cleanup orchestrator.
func NewCleanupOrchestrator() *CleanupOrchestrator {
	return &CleanupOrchestrator{}
}

// SetHotplugStop sets the function to stop hotplug controllers.
func (c *CleanupOrchestrator) SetHotplugStop(f CleanupFunc) {
	c.hotplugStop = f
}

// SetIOShutdown sets the function to shutdown I/O forwarders.
func (c *CleanupOrchestrator) SetIOShutdown(f CleanupFunc) {
	c.ioShutdown = f
}

// SetConnectionClose sets the function to close connections.
func (c *CleanupOrchestrator) SetConnectionClose(f CleanupFunc) {
	c.connClose = f
}

// SetVMShutdown sets the function to shutdown the VM.
func (c *CleanupOrchestrator) SetVMShutdown(f CleanupFunc) {
	c.vmShutdown = f
}

// SetNetworkCleanup sets the function to cleanup network resources.
func (c *CleanupOrchestrator) SetNetworkCleanup(f CleanupFunc) {
	c.networkCleanup = f
}

// SetMountCleanup sets the function to cleanup mounts.
func (c *CleanupOrchestrator) SetMountCleanup(f CleanupFunc) {
	c.mountCleanup = f
}

// SetEventClose sets the function to close the event channel.
func (c *CleanupOrchestrator) SetEventClose(f CleanupFunc) {
	c.eventClose = f
}

// Execute runs the complete cleanup sequence in dependency order.
// Returns a ShutdownResult containing any errors that occurred.
// All phases are attempted even if earlier phases fail.
func (c *CleanupOrchestrator) Execute(ctx context.Context) *ShutdownResult {
	result := &ShutdownResult{}
	logger := log.G(ctx)

	// Phase 1: Stop hotplug controllers
	// Must happen first so they don't try to modify VM during shutdown
	if c.hotplugStop != nil && c.hotplugDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: stopping hotplug controllers")
		if err := c.hotplugStop(ctx); err != nil {
			result.Add(PhaseHotplugStop, err)
		}
	}

	// Phase 2: Shutdown I/O forwarders
	// Must happen before VM shutdown so stdin doesn't block
	if c.ioShutdown != nil && c.ioDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: shutting down I/O forwarders")
		if err := c.ioShutdown(ctx); err != nil {
			result.Add(PhaseIOShutdown, err)
		}
	}

	// Phase 3: Close connections
	// Must happen before VM shutdown to prevent hung RPCs
	if c.connClose != nil && c.connDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: closing connections")
		if err := c.connClose(ctx); err != nil {
			result.Add(PhaseConnClose, err)
		}
	}

	// Phase 4: Shutdown VM
	// Must happen before network cleanup so QEMU exits cleanly
	if c.vmShutdown != nil && c.vmDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: shutting down VM")
		if err := c.vmShutdown(ctx); err != nil {
			result.Add(PhaseVMShutdown, err)
		}
	}

	// Phase 5: Cleanup network resources
	// Must happen after VM shutdown (QEMU needs to release TAP FDs)
	if c.networkCleanup != nil && c.networkDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: releasing network resources")
		if err := c.networkCleanup(ctx); err != nil {
			result.Add(PhaseNetworkCleanup, err)
		}
	}

	// Phase 6: Cleanup mounts
	// Must happen after VM shutdown (no processes using mounts)
	if c.mountCleanup != nil && c.mountDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: cleaning up mounts")
		if err := c.mountCleanup(ctx); err != nil {
			result.Add(PhaseMountCleanup, err)
		}
	}

	// Phase 7: Close event channel
	// Must happen last to ensure all events are delivered
	if c.eventClose != nil && c.eventDone.CompareAndSwap(false, true) {
		logger.Debug("cleanup: closing event channel")
		if err := c.eventClose(ctx); err != nil {
			result.Add(PhaseEventClose, err)
		}
	}

	if result.HasErrors() {
		logger.WithField("failed_phases", result.FailedPhases()).Warn("cleanup completed with errors")
	} else {
		logger.Debug("cleanup completed successfully")
	}

	return result
}

// ExecutePartial runs cleanup starting from a specific phase.
// Useful for cleanup after partial failures during creation.
func (c *CleanupOrchestrator) ExecutePartial(ctx context.Context, startPhase ShutdownPhase) *ShutdownResult {
	result := &ShutdownResult{}
	logger := log.G(ctx)

	// Determine which phases to run based on startPhase
	runHotplug := startPhase == PhaseHotplugStop
	runIO := runHotplug || startPhase == PhaseIOShutdown
	runConn := runIO || startPhase == PhaseConnClose
	runVM := runConn || startPhase == PhaseVMShutdown
	runNetwork := runVM || startPhase == PhaseNetworkCleanup
	runMount := runNetwork || startPhase == PhaseMountCleanup
	runEvent := runMount || startPhase == PhaseEventClose

	if runHotplug && c.hotplugStop != nil && c.hotplugDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: stopping hotplug controllers")
		if err := c.hotplugStop(ctx); err != nil {
			result.Add(PhaseHotplugStop, err)
		}
	}

	if runIO && c.ioShutdown != nil && c.ioDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: shutting down I/O forwarders")
		if err := c.ioShutdown(ctx); err != nil {
			result.Add(PhaseIOShutdown, err)
		}
	}

	if runConn && c.connClose != nil && c.connDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: closing connections")
		if err := c.connClose(ctx); err != nil {
			result.Add(PhaseConnClose, err)
		}
	}

	if runVM && c.vmShutdown != nil && c.vmDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: shutting down VM")
		if err := c.vmShutdown(ctx); err != nil {
			result.Add(PhaseVMShutdown, err)
		}
	}

	if runNetwork && c.networkCleanup != nil && c.networkDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: releasing network resources")
		if err := c.networkCleanup(ctx); err != nil {
			result.Add(PhaseNetworkCleanup, err)
		}
	}

	if runMount && c.mountCleanup != nil && c.mountDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: cleaning up mounts")
		if err := c.mountCleanup(ctx); err != nil {
			result.Add(PhaseMountCleanup, err)
		}
	}

	if runEvent && c.eventClose != nil && c.eventDone.CompareAndSwap(false, true) {
		logger.Debug("partial cleanup: closing event channel")
		if err := c.eventClose(ctx); err != nil {
			result.Add(PhaseEventClose, err)
		}
	}

	return result
}

// Reset clears all completion flags, allowing cleanup to run again.
// This is primarily for testing.
func (c *CleanupOrchestrator) Reset() {
	c.hotplugDone.Store(false)
	c.ioDone.Store(false)
	c.connDone.Store(false)
	c.vmDone.Store(false)
	c.networkDone.Store(false)
	c.mountDone.Store(false)
	c.eventDone.Store(false)
}

// CompletedPhases returns a list of phases that have been completed.
func (c *CleanupOrchestrator) CompletedPhases() []ShutdownPhase {
	var phases []ShutdownPhase
	if c.hotplugDone.Load() {
		phases = append(phases, PhaseHotplugStop)
	}
	if c.ioDone.Load() {
		phases = append(phases, PhaseIOShutdown)
	}
	if c.connDone.Load() {
		phases = append(phases, PhaseConnClose)
	}
	if c.vmDone.Load() {
		phases = append(phases, PhaseVMShutdown)
	}
	if c.networkDone.Load() {
		phases = append(phases, PhaseNetworkCleanup)
	}
	if c.mountDone.Load() {
		phases = append(phases, PhaseMountCleanup)
	}
	if c.eventDone.Load() {
		phases = append(phases, PhaseEventClose)
	}
	return phases
}
