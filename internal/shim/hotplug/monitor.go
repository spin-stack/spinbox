// Package hotplug provides a shared monitoring framework for resource hotplug controllers.
// It handles the common patterns of timer-based monitoring, stability tracking,
// cooldown enforcement, and lifecycle management that are shared between CPU and memory hotplug.
package hotplug

import (
	"context"
	"sync"
	"time"

	"github.com/containerd/log"
)

// ScaleDirection indicates whether to scale up or down.
type ScaleDirection int

const (
	// ScaleNone indicates no scaling is needed.
	ScaleNone ScaleDirection = iota
	// ScaleUp indicates the resource should be increased.
	ScaleUp
	// ScaleDown indicates the resource should be decreased.
	ScaleDown
)

// MonitorConfig holds common configuration for hotplug monitoring.
type MonitorConfig struct {
	// MonitorInterval is how often to check resource usage.
	MonitorInterval time.Duration

	// ScaleUpCooldown prevents thrashing by rate-limiting scale-up operations.
	ScaleUpCooldown time.Duration

	// ScaleDownCooldown is more conservative to avoid removing resources prematurely.
	ScaleDownCooldown time.Duration

	// ScaleUpStability requires N consecutive high readings before scaling up.
	ScaleUpStability int

	// ScaleDownStability requires N consecutive low readings before scaling down.
	ScaleDownStability int

	// EnableScaleDown allows removing resources (may not be supported by all resources).
	EnableScaleDown bool
}

// ResourceScaler is implemented by resource-specific controllers (CPU, memory).
// The Monitor calls these methods during its monitoring loop.
type ResourceScaler interface {
	// Name returns the resource name for logging (e.g., "cpu", "memory").
	Name() string

	// ContainerID returns the container ID for logging.
	ContainerID() string

	// EvaluateScaling checks current resource state and returns the scaling direction.
	// This method should query current usage and determine if scaling is needed.
	// Returns ScaleNone if no action is needed.
	EvaluateScaling(ctx context.Context) (ScaleDirection, error)

	// ScaleUp increases the resource allocation.
	ScaleUp(ctx context.Context) error

	// ScaleDown decreases the resource allocation.
	ScaleDown(ctx context.Context) error
}

// Monitor manages the monitoring loop and stability tracking for a resource scaler.
// It handles the common patterns of timer-based monitoring, cooldown enforcement,
// and consecutive usage tracking.
type Monitor struct {
	scaler ResourceScaler
	config MonitorConfig

	// Stability tracking
	consecutiveHighUsage int
	consecutiveLowUsage  int

	// Cooldown tracking
	lastScaleUp   time.Time
	lastScaleDown time.Time

	// Lifecycle management
	mu        sync.Mutex
	stopCh    chan struct{}
	stoppedCh chan struct{}
	started   bool
}

// NewMonitor creates a new Monitor for the given ResourceScaler.
func NewMonitor(scaler ResourceScaler, config MonitorConfig) *Monitor {
	return &Monitor{
		scaler: scaler,
		config: config,
	}
}

// Start begins the monitoring loop in a goroutine.
// It is safe to call Start multiple times; subsequent calls are no-ops.
func (m *Monitor) Start(ctx context.Context) {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return
	}
	m.started = true
	m.stopCh = make(chan struct{})
	m.stoppedCh = make(chan struct{})
	m.mu.Unlock()

	log.G(ctx).WithFields(log.Fields{
		"resource":         m.scaler.Name(),
		"container_id":     m.scaler.ContainerID(),
		"monitor_interval": m.config.MonitorInterval,
	}).Info("hotplug: monitor started")

	go m.monitorLoop(ctx)
}

// Stop gracefully stops the monitoring loop and waits for it to finish.
// It is safe to call Stop multiple times; subsequent calls are no-ops.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.started || m.stopCh == nil {
		m.mu.Unlock()
		return
	}

	select {
	case <-m.stopCh:
		// Already stopped
		m.mu.Unlock()
		return
	default:
		close(m.stopCh)
	}
	stoppedCh := m.stoppedCh
	m.mu.Unlock()

	// Wait for monitor loop to finish
	<-stoppedCh
}

// monitorLoop runs the periodic resource check.
func (m *Monitor) monitorLoop(ctx context.Context) {
	defer close(m.stoppedCh)

	ticker := time.NewTicker(m.config.MonitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopCh:
			log.G(ctx).WithFields(log.Fields{
				"resource":     m.scaler.Name(),
				"container_id": m.scaler.ContainerID(),
			}).Info("hotplug: monitor stopped")
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.checkAndAdjust(ctx); err != nil {
				log.G(ctx).WithError(err).WithFields(log.Fields{
					"resource":     m.scaler.Name(),
					"container_id": m.scaler.ContainerID(),
				}).Warn("hotplug: failed to check/adjust resource")
			}
		}
	}
}

// checkAndAdjust evaluates the current resource state and adjusts if needed.
func (m *Monitor) checkAndAdjust(ctx context.Context) error {
	direction, err := m.scaler.EvaluateScaling(ctx)
	if err != nil {
		return err
	}

	switch direction {
	case ScaleUp:
		return m.handleScaleUp(ctx)
	case ScaleDown:
		if m.config.EnableScaleDown {
			return m.handleScaleDown(ctx)
		}
		// Scale down disabled, reset counter
		m.consecutiveLowUsage = 0
	default:
		// No scaling needed, reset both counters
		m.consecutiveHighUsage = 0
		m.consecutiveLowUsage = 0
	}

	return nil
}

// handleScaleUp handles the scale-up decision with cooldown and stability checks.
func (m *Monitor) handleScaleUp(ctx context.Context) error {
	// Check cooldown
	if !m.canScaleUp() {
		log.G(ctx).WithFields(log.Fields{
			"resource":     m.scaler.Name(),
			"container_id": m.scaler.ContainerID(),
			"cooldown":     time.Since(m.lastScaleUp),
			"required":     m.config.ScaleUpCooldown,
		}).Debug("hotplug: scale-up cooldown active")
		return nil
	}

	m.consecutiveHighUsage++
	m.consecutiveLowUsage = 0

	if m.consecutiveHighUsage < m.config.ScaleUpStability {
		log.G(ctx).WithFields(log.Fields{
			"resource":     m.scaler.Name(),
			"container_id": m.scaler.ContainerID(),
			"consecutive":  m.consecutiveHighUsage,
			"required":     m.config.ScaleUpStability,
		}).Debug("hotplug: waiting for stable high usage")
		return nil
	}

	// Stability threshold met, perform scale-up
	if err := m.scaler.ScaleUp(ctx); err != nil {
		return err
	}

	m.lastScaleUp = time.Now()
	m.consecutiveHighUsage = 0
	return nil
}

// handleScaleDown handles the scale-down decision with cooldown and stability checks.
func (m *Monitor) handleScaleDown(ctx context.Context) error {
	// Check cooldown
	if !m.canScaleDown() {
		log.G(ctx).WithFields(log.Fields{
			"resource":     m.scaler.Name(),
			"container_id": m.scaler.ContainerID(),
			"cooldown":     time.Since(m.lastScaleDown),
			"required":     m.config.ScaleDownCooldown,
		}).Debug("hotplug: scale-down cooldown active")
		return nil
	}

	m.consecutiveLowUsage++
	m.consecutiveHighUsage = 0

	if m.consecutiveLowUsage < m.config.ScaleDownStability {
		log.G(ctx).WithFields(log.Fields{
			"resource":     m.scaler.Name(),
			"container_id": m.scaler.ContainerID(),
			"consecutive":  m.consecutiveLowUsage,
			"required":     m.config.ScaleDownStability,
		}).Debug("hotplug: waiting for stable low usage")
		return nil
	}

	// Stability threshold met, perform scale-down
	if err := m.scaler.ScaleDown(ctx); err != nil {
		return err
	}

	m.lastScaleDown = time.Now()
	m.consecutiveLowUsage = 0
	return nil
}

// canScaleUp checks if scale-up cooldown has elapsed.
func (m *Monitor) canScaleUp() bool {
	if m.lastScaleUp.IsZero() {
		return true
	}
	return time.Since(m.lastScaleUp) >= m.config.ScaleUpCooldown
}

// canScaleDown checks if scale-down cooldown has elapsed.
func (m *Monitor) canScaleDown() bool {
	if m.lastScaleDown.IsZero() {
		return true
	}
	return time.Since(m.lastScaleDown) >= m.config.ScaleDownCooldown
}
