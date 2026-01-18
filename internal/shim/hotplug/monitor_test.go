package hotplug

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockScaler implements ResourceScaler for testing
type mockScaler struct {
	name        string
	containerID string

	evaluateResult ScaleDirection
	evaluateErr    error
	scaleUpErr     error
	scaleDownErr   error

	evaluateCalls atomic.Int32
	scaleUpCalls  atomic.Int32
	scaleDownCalls atomic.Int32
}

func (m *mockScaler) Name() string {
	return m.name
}

func (m *mockScaler) ContainerID() string {
	return m.containerID
}

func (m *mockScaler) EvaluateScaling(ctx context.Context) (ScaleDirection, error) {
	m.evaluateCalls.Add(1)
	return m.evaluateResult, m.evaluateErr
}

func (m *mockScaler) ScaleUp(ctx context.Context) error {
	m.scaleUpCalls.Add(1)
	return m.scaleUpErr
}

func (m *mockScaler) ScaleDown(ctx context.Context) error {
	m.scaleDownCalls.Add(1)
	return m.scaleDownErr
}

func TestScaleDirection(t *testing.T) {
	assert.Equal(t, ScaleDirection(0), ScaleNone)
	assert.Equal(t, ScaleDirection(1), ScaleUp)
	assert.Equal(t, ScaleDirection(2), ScaleDown)
}

func TestNewMonitor(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval:    5 * time.Second,
		ScaleUpCooldown:    10 * time.Second,
		ScaleDownCooldown:  30 * time.Second,
		ScaleUpStability:   2,
		ScaleDownStability: 3,
		EnableScaleDown:    true,
	}

	monitor := NewMonitor(scaler, config)

	require.NotNil(t, monitor)
	assert.Equal(t, scaler, monitor.scaler)
	assert.Equal(t, config, monitor.config)
	assert.Equal(t, 0, monitor.consecutiveHighUsage)
	assert.Equal(t, 0, monitor.consecutiveLowUsage)
}

func TestMonitor_StartStop(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval: 10 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// Start the monitor
	monitor.Start(ctx)

	// Wait for a few ticks
	time.Sleep(50 * time.Millisecond)

	// Should have called EvaluateScaling at least once
	assert.GreaterOrEqual(t, scaler.evaluateCalls.Load(), int32(1))

	// Stop the monitor
	monitor.Stop()
}

func TestMonitor_StartIdempotent(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval: 100 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// Start multiple times should be safe
	monitor.Start(ctx)
	monitor.Start(ctx) // Should be ignored
	monitor.Start(ctx) // Should be ignored

	// Stop should work
	monitor.Stop()
}

func TestMonitor_StopIdempotent(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval: 100 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	monitor.Start(ctx)

	// Stop multiple times should be safe
	monitor.Stop()
	monitor.Stop() // Should be no-op
	monitor.Stop() // Should be no-op
}

func TestMonitor_StopBeforeStart(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval: 100 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)

	// Stop before start should not panic
	monitor.Stop()
}

func TestMonitor_ContextCancellation(t *testing.T) {
	scaler := &mockScaler{name: "test", containerID: "container-1"}
	config := MonitorConfig{
		MonitorInterval: 10 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)
	ctx, cancel := context.WithCancel(context.Background())

	monitor.Start(ctx)

	// Wait a bit
	time.Sleep(30 * time.Millisecond)

	// Cancel context
	cancel()

	// Give monitor time to notice
	time.Sleep(20 * time.Millisecond)

	// Calling Stop should work (may be a no-op if already stopped)
	monitor.Stop()
}

func TestMonitor_ScaleUp_StabilityTracking(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleUp,
	}
	config := MonitorConfig{
		MonitorInterval:  10 * time.Millisecond,
		ScaleUpStability: 3, // Need 3 consecutive high readings
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// Simulate checkAndAdjust calls
	// First call: consecutiveHighUsage = 1
	err := monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, monitor.consecutiveHighUsage)
	assert.Equal(t, int32(0), scaler.scaleUpCalls.Load())

	// Second call: consecutiveHighUsage = 2
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, monitor.consecutiveHighUsage)
	assert.Equal(t, int32(0), scaler.scaleUpCalls.Load())

	// Third call: consecutiveHighUsage = 3, should trigger scale up
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleUpCalls.Load())
	assert.Equal(t, 0, monitor.consecutiveHighUsage) // Reset after scale
}

func TestMonitor_ScaleDown_StabilityTracking(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleDown,
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleDownStability: 2, // Need 2 consecutive low readings
		EnableScaleDown:    true,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// First call: consecutiveLowUsage = 1
	err := monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, monitor.consecutiveLowUsage)
	assert.Equal(t, int32(0), scaler.scaleDownCalls.Load())

	// Second call: consecutiveLowUsage = 2, should trigger scale down
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleDownCalls.Load())
	assert.Equal(t, 0, monitor.consecutiveLowUsage) // Reset after scale
}

func TestMonitor_ScaleDown_Disabled(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleDown,
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleDownStability: 1,
		EnableScaleDown:    false, // Disabled
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// Should not scale down even if stability is met
	err := monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(0), scaler.scaleDownCalls.Load())
	assert.Equal(t, 0, monitor.consecutiveLowUsage) // Reset because disabled
}

func TestMonitor_ScaleUp_Cooldown(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleUp,
	}
	config := MonitorConfig{
		MonitorInterval:  10 * time.Millisecond,
		ScaleUpCooldown:  100 * time.Millisecond, // 100ms cooldown
		ScaleUpStability: 1,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// First scale up should work
	err := monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleUpCalls.Load())

	// Second scale up should be blocked by cooldown
	scaler.evaluateCalls.Store(0)
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleUpCalls.Load()) // Still 1

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Now scale up should work again
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(2), scaler.scaleUpCalls.Load())
}

func TestMonitor_ScaleDown_Cooldown(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleDown,
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleDownCooldown:  100 * time.Millisecond,
		ScaleDownStability: 1,
		EnableScaleDown:    true,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// First scale down should work
	err := monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleDownCalls.Load())

	// Second scale down should be blocked by cooldown
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(1), scaler.scaleDownCalls.Load()) // Still 1

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Now scale down should work again
	err = monitor.checkAndAdjust(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(2), scaler.scaleDownCalls.Load())
}

func TestMonitor_ScaleNone_ResetsCounters(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleUp,
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleUpStability:   3,
		ScaleDownStability: 3,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	// Build up some consecutive high usage
	_ = monitor.checkAndAdjust(ctx)
	assert.Equal(t, 1, monitor.consecutiveHighUsage)

	_ = monitor.checkAndAdjust(ctx)
	assert.Equal(t, 2, monitor.consecutiveHighUsage)

	// Now change to ScaleNone
	scaler.evaluateResult = ScaleNone
	_ = monitor.checkAndAdjust(ctx)

	// Counter should be reset
	assert.Equal(t, 0, monitor.consecutiveHighUsage)
	assert.Equal(t, 0, monitor.consecutiveLowUsage)
}

func TestMonitor_EvaluateError(t *testing.T) {
	scaler := &mockScaler{
		name:        "test",
		containerID: "container-1",
		evaluateErr: errors.New("evaluate failed"),
	}
	config := MonitorConfig{
		MonitorInterval: 10 * time.Millisecond,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	err := monitor.checkAndAdjust(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evaluate failed")
}

func TestMonitor_ScaleUpError(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleUp,
		scaleUpErr:     errors.New("scale up failed"),
	}
	config := MonitorConfig{
		MonitorInterval:  10 * time.Millisecond,
		ScaleUpStability: 1,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	err := monitor.checkAndAdjust(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scale up failed")
}

func TestMonitor_ScaleDownError(t *testing.T) {
	scaler := &mockScaler{
		name:           "test",
		containerID:    "container-1",
		evaluateResult: ScaleDown,
		scaleDownErr:   errors.New("scale down failed"),
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleDownStability: 1,
		EnableScaleDown:    true,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	err := monitor.checkAndAdjust(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scale down failed")
}

func TestMonitor_CanScaleUp(t *testing.T) {
	monitor := &Monitor{
		config: MonitorConfig{
			ScaleUpCooldown: 100 * time.Millisecond,
		},
	}

	// First check should allow (zero time)
	assert.True(t, monitor.canScaleUp())

	// Set last scale up to now
	monitor.lastScaleUp = time.Now()
	assert.False(t, monitor.canScaleUp())

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)
	assert.True(t, monitor.canScaleUp())
}

func TestMonitor_CanScaleDown(t *testing.T) {
	monitor := &Monitor{
		config: MonitorConfig{
			ScaleDownCooldown: 100 * time.Millisecond,
		},
	}

	// First check should allow (zero time)
	assert.True(t, monitor.canScaleDown())

	// Set last scale down to now
	monitor.lastScaleDown = time.Now()
	assert.False(t, monitor.canScaleDown())

	// Wait for cooldown
	time.Sleep(150 * time.Millisecond)
	assert.True(t, monitor.canScaleDown())
}

// Benchmarks

func BenchmarkMonitor_CheckAndAdjust(b *testing.B) {
	scaler := &mockScaler{
		name:           "bench",
		containerID:    "container-1",
		evaluateResult: ScaleNone,
	}
	config := MonitorConfig{
		MonitorInterval:    10 * time.Millisecond,
		ScaleUpStability:   3,
		ScaleDownStability: 3,
	}

	monitor := NewMonitor(scaler, config)
	ctx := context.Background()

	b.ResetTimer()
	for range b.N {
		_ = monitor.checkAndAdjust(ctx)
	}
}
