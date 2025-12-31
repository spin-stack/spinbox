//go:build linux

package task

import (
	"sync"
	"sync/atomic"

	runcC "github.com/containerd/go-runc"

	"github.com/aledbf/qemubox/containerd/internal/guest/vminit/process"
	"github.com/aledbf/qemubox/containerd/internal/guest/vminit/runc"
)

// exitTracker manages process lifecycle coordination, specifically handling the race
// between process start and exit events.
//
// # Problem Statement
//
// Process exit events can arrive before Start() completes. We must:
//  1. Handle "early exits" - exits that occur before Start() returns
//  2. Match each exit to the correct process (PID reuse makes this tricky)
//  3. Ensure init process exits are published AFTER all exec process exits
//
// # Solution Overview
//
// The tracker uses two coordination mechanisms:
//
// 1. Subscription Pattern (for early exits):
//
//	preStart() ──► Subscribe() ──► [process starts] ──► HandleStart()
//	                   │                                     │
//	                   └─── collects exits during window ────┘
//
// 2. Exec Counter Pattern (for init exit ordering):
//
//	exec starts ──► runningExecs++ ──► ... ──► exec exits ──► runningExecs--
//	                                                                │
//	init exits ──► wait if runningExecs > 0 ◄──────────────────────┘
//
// # State Diagram
//
//	┌─────────────────────────────────────────────────────────────────┐
//	│                         exitTracker                             │
//	├─────────────────────────────────────────────────────────────────┤
//	│ subscriptions: map[subID] → collected exits (early exit window) │
//	│ running:       map[PID]   → container processes                 │
//	│ containers:    map[*Container] → containerExitState             │
//	└─────────────────────────────────────────────────────────────────┘
type exitTracker struct {
	mu sync.Mutex

	// nextSubID is a monotonic counter for subscription IDs
	nextSubID uint64

	// subscriptions tracks active subscriptions waiting for process start.
	// Each subscription collects exits that occur during the start window.
	// Key: subscription ID, Value: map of PID to exits for that PID
	subscriptions map[uint64]map[int][]runcC.Exit

	// running tracks currently running processes by PID.
	// Multiple processes may share a PID due to PID reuse.
	running map[int][]containerProcess

	// containers tracks per-container exit coordination state.
	// This consolidates exec counting and init exit stashing.
	containers map[*runc.Container]*containerExitState
}

// containerExitState holds per-container exit coordination state.
type containerExitState struct {
	// runningExecs is the number of exec processes currently running.
	// Init exit is delayed until this reaches zero.
	runningExecs int

	// execWaiter is signaled (closed) when runningExecs reaches zero.
	// Created on-demand when init exits while execs are running.
	execWaiter chan struct{}

	// initExit holds the stashed init exit event, if the init has exited.
	// Nil means init has not exited yet.
	initExit *runcC.Exit
}

func newExitTracker() *exitTracker {
	return &exitTracker{
		subscriptions: make(map[uint64]map[int][]runcC.Exit),
		running:       make(map[int][]containerProcess),
		containers:    make(map[*runc.Container]*containerExitState),
	}
}

// getOrCreateState returns the exit state for a container, creating it if needed.
// Caller must hold t.mu.
func (t *exitTracker) getOrCreateState(c *runc.Container) *containerExitState {
	state := t.containers[c]
	if state == nil {
		state = &containerExitState{}
		t.containers[c] = state
	}
	return state
}

// subscription represents an active wait for a process to start.
// It collects any exit events that occur during the start window.
type subscription struct {
	id      uint64
	tracker *exitTracker
	exits   map[int][]runcC.Exit // PID -> exits collected during start window
}

// Subscribe registers interest in process exits that occur before Start completes.
// Returns a subscription that must be completed via HandleStart or cancelled via Cancel.
//
// If restarting an existing container (c != nil), removes its init process from
// the running map so early exits during restart are properly detected.
func (t *exitTracker) Subscribe(c *runc.Container) *subscription {
	t.mu.Lock()
	defer t.mu.Unlock()

	subID := atomic.AddUint64(&t.nextSubID, 1)
	exits := make(map[int][]runcC.Exit)
	t.subscriptions[subID] = exits

	// When restarting, remove the old init process from running map
	if c != nil {
		t.removeProcessFromRunning(c.Pid(), c)
	}

	return &subscription{
		id:      subID,
		tracker: t,
		exits:   exits,
	}
}

// removeProcessFromRunning removes processes for a container from the running map.
// Caller must hold t.mu.
func (t *exitTracker) removeProcessFromRunning(pid int, c *runc.Container) {
	cps := t.running[pid]
	if len(cps) == 0 {
		return
	}

	// Filter out processes belonging to this container
	remaining := cps[:0]
	for _, cp := range cps {
		if cp.Container != c {
			remaining = append(remaining, cp)
		}
	}

	if len(remaining) > 0 {
		t.running[pid] = remaining
	} else {
		delete(t.running, pid)
	}
}

// HandleStart completes the subscription, checking for early exits.
//
// Returns exits that occurred before Start completed. If non-empty, the process
// exited before we could register it as running.
//
// If pid == 0, the process failed to start - returns any collected exits.
// Otherwise, registers the process as running and tracks exec count.
func (s *subscription) HandleStart(c *runc.Container, p process.Process, pid int) []runcC.Exit {
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()

	// Complete subscription
	delete(t.subscriptions, s.id)

	// Check for early exits
	earlyExits := s.exits[pid]
	if pid == 0 || len(earlyExits) > 0 {
		return earlyExits
	}

	// Register process as running
	t.running[pid] = append(t.running[pid], containerProcess{
		Container: c,
		Process:   p,
	})

	// Track exec processes for init exit ordering
	if _, isInit := p.(*process.Init); !isInit {
		state := t.getOrCreateState(c)
		state.runningExecs++
	}

	return nil
}

// Cancel cancels the subscription without completing a start.
// Must be called if HandleStart is not called, to prevent memory leaks.
func (s *subscription) Cancel() {
	t := s.tracker
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.subscriptions, s.id)
}

// NotifyExit handles a process exit event.
// Returns the container processes that exited (may be >1 due to PID reuse).
func (t *exitTracker) NotifyExit(e runcC.Exit) []containerProcess {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Notify all active subscriptions (for early exit detection)
	for _, exits := range t.subscriptions {
		exits[e.Pid] = append(exits[e.Pid], e)
	}

	// Find and remove running processes with this PID
	cps := t.running[e.Pid]
	delete(t.running, e.Pid)

	// Stash init exits for later (need to wait for execs to complete)
	for _, cp := range cps {
		if _, isInit := cp.Process.(*process.Init); isInit {
			state := t.getOrCreateState(cp.Container)
			state.initExit = &e
		}
	}

	return cps
}

// ShouldDelayInitExit checks if an init process exit should be delayed
// until all exec processes exit.
//
// Returns:
//   - (false, nil): No delay needed, safe to publish init exit immediately
//   - (true, chan): Delay needed, wait on channel for signal when execs complete
func (t *exitTracker) ShouldDelayInitExit(c *runc.Container) (bool, <-chan struct{}) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.containers[c]
	if state == nil || state.runningExecs == 0 {
		// No execs running, safe to publish immediately
		return false, nil
	}

	// Execs still running - create waiter channel
	waitChan := make(chan struct{})
	state.execWaiter = waitChan

	return true, waitChan
}

// NotifyExecExit decrements the exec counter for a container.
// If the counter reaches 0 and an init exit is waiting, signals the waiter.
func (t *exitTracker) NotifyExecExit(c *runc.Container) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.containers[c]
	if state == nil {
		return
	}

	state.runningExecs--
	if state.runningExecs > 0 {
		return
	}

	// All execs done - signal waiter if init is waiting
	if state.execWaiter != nil {
		close(state.execWaiter)
		state.execWaiter = nil
	}
}

// GetInitExit returns and clears the stashed init exit for a container.
// Returns (exit, true) if init has exited, (zero, false) otherwise.
func (t *exitTracker) GetInitExit(c *runc.Container) (runcC.Exit, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.containers[c]
	if state == nil || state.initExit == nil {
		return runcC.Exit{}, false
	}

	exit := *state.initExit
	state.initExit = nil
	return exit, true
}

// InitHasExited checks if the container's init process has exited.
func (t *exitTracker) InitHasExited(c *runc.Container) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	state := t.containers[c]
	return state != nil && state.initExit != nil
}

// DecrementExecCount manually decrements the exec counter.
// Used when process start fails and HandleStart wasn't called.
func (t *exitTracker) DecrementExecCount(c *runc.Container) {
	t.NotifyExecExit(c)
}

// Cleanup removes all tracking state for a container.
// Should be called when a container is deleted.
func (t *exitTracker) Cleanup(c *runc.Container) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Remove container state
	delete(t.containers, c)

	// Remove any running processes for this container
	for pid := range t.running {
		t.removeProcessFromRunningLocked(pid, c)
	}
}

// removeProcessFromRunningLocked is like removeProcessFromRunning but assumes lock is held.
// Caller must hold t.mu.
func (t *exitTracker) removeProcessFromRunningLocked(pid int, c *runc.Container) {
	cps := t.running[pid]
	if len(cps) == 0 {
		return
	}

	remaining := cps[:0]
	for _, cp := range cps {
		if cp.Container != c {
			remaining = append(remaining, cp)
		}
	}

	if len(remaining) > 0 {
		t.running[pid] = remaining
	} else {
		delete(t.running, pid)
	}
}
