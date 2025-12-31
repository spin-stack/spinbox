//go:build linux

package qemu

import (
	"io"
	"testing"
	"time"

	"github.com/containerd/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShutdownConstants(t *testing.T) {
	// Verify shutdown timing constants are reasonable
	assert.Equal(t, 2*time.Second, shutdownQMPTimeout)
	assert.Equal(t, 500*time.Millisecond, shutdownACPIWait)
	assert.Equal(t, 1*time.Second, shutdownQuitTimeout)
	assert.Equal(t, 2*time.Second, shutdownQuitWait)
	assert.Equal(t, 2*time.Second, shutdownKillWait)

	// Total shutdown time should not exceed reasonable limit
	totalTimeout := shutdownQMPTimeout + shutdownACPIWait + shutdownQuitTimeout + shutdownQuitWait + shutdownKillWait
	assert.LessOrEqual(t, totalTimeout, 10*time.Second, "total shutdown timeout should not exceed 10 seconds")
}

func TestCloseAndLog(t *testing.T) {
	logger := log.L.WithField("test", true)

	t.Run("nil closer is no-op", func(t *testing.T) {
		// Should not panic
		closeAndLog(logger, "test", nil)
	})

	t.Run("successful close", func(t *testing.T) {
		closer := &mockCloser{}
		closeAndLog(logger, "test", closer)
		assert.True(t, closer.closed)
	})

	t.Run("close with error logs but doesn't panic", func(t *testing.T) {
		closer := &mockCloser{err: assert.AnError}
		closeAndLog(logger, "test", closer)
		assert.True(t, closer.closed)
	})
}

type mockCloser struct {
	closed bool
	err    error
}

func (m *mockCloser) Close() error {
	m.closed = true
	return m.err
}

func TestInstance_CloseClientConnections_NilFields(t *testing.T) {
	logger := log.L.WithField("test", true)

	// Test that nil fields don't cause panics
	q := &Instance{}
	// Should not panic when all connection fields are nil
	q.closeClientConnections(logger)
}

func TestInstance_CancelBackgroundMonitors(t *testing.T) {
	logger := log.L.WithField("test", true)

	t.Run("nil cancel - no panic", func(t *testing.T) {
		q := &Instance{}
		// Should not panic
		q.cancelBackgroundMonitors(logger)
	})

	t.Run("calls cancel function", func(t *testing.T) {
		cancelled := false
		q := &Instance{
			runCancel: func() { cancelled = true },
		}
		q.cancelBackgroundMonitors(logger)
		assert.True(t, cancelled)
	})
}

func TestInstance_CleanupAfterFailedKill_NilFields(t *testing.T) {
	// Test that nil qmpClient doesn't cause panics
	q := &Instance{}
	// Should not panic when qmpClient is nil
	q.cleanupAfterFailedKill()
}

func TestInstance_Shutdown_NotRunning(t *testing.T) {
	// When VM is not in running state, Shutdown should be idempotent
	q := &Instance{}
	q.setState(vmStateShutdown)

	// Verify state was set
	assert.Equal(t, vmStateShutdown, q.getState())

	// Shutdown should return nil without error (idempotent)
	// Can't fully test without starting a real VM
}

func TestInstance_StopQemuProcess_NilCmd(t *testing.T) {
	logger := log.L.WithField("test", true)
	q := &Instance{
		cmd: nil,
	}

	ctx := t.Context()
	err := q.stopQemuProcess(ctx, logger)
	require.NoError(t, err)
}

// Verify io.Closer interface satisfaction
func TestMockCloserImplementsCloser(t *testing.T) {
	var _ io.Closer = (*mockCloser)(nil)
}

// Benchmark close helper
func BenchmarkCloseAndLog(b *testing.B) {
	logger := log.L.WithField("bench", true)
	closer := &mockCloser{}

	b.ResetTimer()
	for range b.N {
		closer.closed = false
		closeAndLog(logger, "test", closer)
	}
}
