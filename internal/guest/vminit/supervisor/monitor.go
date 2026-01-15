//go:build linux

package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/log"
)

// Restart policy constants
const (
	// maxRestarts is the maximum number of restarts allowed within restartWindow.
	maxRestarts = 10

	// restartWindow is the time window for counting restarts.
	// If the supervisor runs stably for this duration, the restart counter resets.
	restartWindow = 5 * time.Minute

	// initialBackoff is the initial delay before restarting after a crash.
	initialBackoff = 1 * time.Second

	// maxBackoff is the maximum delay between restarts.
	maxBackoff = 30 * time.Second
)

// Monitor manages the supervisor process lifecycle with automatic restart.
type Monitor struct {
	binaryPath string

	mu          sync.Mutex
	cmd         *exec.Cmd
	pid         int
	restarts    int
	windowStart time.Time
	lastStart   time.Time
	stopped     bool
}

// NewMonitor creates a new supervisor monitor.
func NewMonitor(binaryPath string) *Monitor {
	return &Monitor{
		binaryPath:  binaryPath,
		windowStart: time.Now(),
	}
}

// Run starts the supervisor and monitors it for crashes.
// It blocks until the context is cancelled or max restarts is exceeded.
func (m *Monitor) Run(ctx context.Context) error {
	logger := log.G(ctx)

	for {
		// Check if we should give up
		if m.shouldGiveUp() {
			logger.Error("supervisor exceeded max restarts, giving up")
			return fmt.Errorf("supervisor exceeded %d restarts in %v", maxRestarts, restartWindow)
		}

		// Start the supervisor
		if err := m.start(ctx); err != nil {
			logger.WithError(err).Error("failed to start supervisor")
			if !m.waitBackoff(ctx) {
				return nil // Context cancelled
			}
			continue
		}

		logger.WithField("pid", m.pid).Info("supervisor started")

		// Wait for process to exit
		exitErr := m.cmd.Wait()

		// Check if this was a requested stop
		m.mu.Lock()
		stopped := m.stopped
		m.mu.Unlock()

		if stopped {
			logger.Info("supervisor stopped by request")
			return nil
		}

		// Check if context was cancelled (graceful shutdown)
		select {
		case <-ctx.Done():
			logger.Info("supervisor monitor shutting down")
			return nil
		default:
		}

		// Process exited unexpectedly
		m.recordRestart()
		exitCode := getExitCode(exitErr)
		logger.WithFields(log.Fields{
			"exit_code": exitCode,
			"restarts":  m.restarts,
			"pid":       m.pid,
		}).Warn("supervisor exited, will restart")

		// Wait with backoff before restarting
		if !m.waitBackoff(ctx) {
			return nil // Context cancelled
		}
	}
}

// Stop signals the supervisor to stop gracefully.
func (m *Monitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.stopped = true
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Signal(syscall.SIGTERM)
	}
}

// Status returns the current supervisor status.
func (m *Monitor) Status() (pid int, restarts int, uptime time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lastStart.IsZero() {
		return m.pid, m.restarts, 0
	}
	return m.pid, m.restarts, time.Since(m.lastStart)
}

// start launches the supervisor process.
func (m *Monitor) start(ctx context.Context) error {
	// Ensure binary is executable (0755 is required for execution)
	if err := os.Chmod(m.binaryPath, 0755); err != nil { //nolint:gosec // executable needs 0755
		return fmt.Errorf("chmod supervisor binary: %w", err)
	}

	// Create log directory
	logDir := "/var/log"
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.G(ctx).WithError(err).Warn("failed to create log directory")
	}

	// Open log file
	logFile, err := os.OpenFile(LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644) //nolint:gosec
	if err != nil {
		log.G(ctx).WithError(err).Warn("failed to open supervisor log file, using /dev/null")
		logFile, _ = os.Open("/dev/null")
	}

	cmd := exec.Command(m.binaryPath) //nolint:gosec
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start supervisor: %w", err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.pid = cmd.Process.Pid
	m.lastStart = time.Now()
	m.mu.Unlock()

	// Write PID file
	if err := os.WriteFile(PidFile, fmt.Appendf(nil, "%d\n", cmd.Process.Pid), 0644); err != nil { //nolint:gosec
		log.G(ctx).WithError(err).Warn("failed to write supervisor PID file")
	}

	// Close our handle to log file (process keeps its own)
	logFile.Close()

	return nil
}

// shouldGiveUp returns true if we've exceeded max restarts in the window.
func (m *Monitor) shouldGiveUp() bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset window if enough time has passed since it started
	if time.Since(m.windowStart) > restartWindow {
		m.restarts = 0
		m.windowStart = time.Now()
	}

	return m.restarts >= maxRestarts
}

// recordRestart increments the restart counter.
func (m *Monitor) recordRestart() {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset window if stable for long enough
	if time.Since(m.windowStart) > restartWindow {
		m.restarts = 0
		m.windowStart = time.Now()
	}

	m.restarts++
}

// waitBackoff waits for the backoff duration before returning.
// Returns false if the context was cancelled.
func (m *Monitor) waitBackoff(ctx context.Context) bool {
	m.mu.Lock()
	restarts := m.restarts
	m.mu.Unlock()

	backoff := calculateBackoff(restarts)
	log.G(ctx).WithField("backoff", backoff).Debug("waiting before restart")

	select {
	case <-ctx.Done():
		return false
	case <-time.After(backoff):
		return true
	}
}

// calculateBackoff returns the backoff duration based on restart count.
func calculateBackoff(restarts int) time.Duration {
	if restarts <= 0 {
		return initialBackoff
	}

	// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s, 30s, ...
	backoff := initialBackoff
	for i := 0; i < restarts && backoff < maxBackoff; i++ {
		backoff *= 2
	}

	if backoff > maxBackoff {
		backoff = maxBackoff
	}

	return backoff
}

// getExitCode extracts the exit code from a process error.
func getExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}
