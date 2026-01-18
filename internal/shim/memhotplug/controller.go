// Package memhotplug provides memory hotplug control for QEMU VMs.
package memhotplug

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/log"

	"github.com/spin-stack/spinbox/internal/host/vm/qemu"
	"github.com/spin-stack/spinbox/internal/shim/hotplug"
)

const (
	// defaultMemorySlots is the default number of memory hotplug slots.
	// This should match the VMResourceConfig.MemorySlots value used when starting QEMU.
	defaultMemorySlots = 8
)

// qmpMemoryClient defines the interface for QMP memory operations.
// This interface exists to enable testing with mocks.
type qmpMemoryClient interface {
	HotplugMemory(ctx context.Context, slotID int, sizeBytes int64) error
	UnplugMemory(ctx context.Context, slotID int) error
	QueryMemorySizeSummary(ctx context.Context) (*qemu.MemorySizeSummary, error)
}

// StatsProvider returns cgroup memory usage in bytes
type StatsProvider func(ctx context.Context) (usageBytes int64, err error)

// MemoryOffliner offlines a memory block in the guest before unplug
type MemoryOffliner func(ctx context.Context, memoryID int) error

// MemoryOnliner onlines a memory block in the guest after hotplug
type MemoryOnliner func(ctx context.Context, memoryID int) error

// MemoryHotplugController defines the interface for memory hotplug management.
type MemoryHotplugController interface {
	Start(ctx context.Context)
	Stop()
}

// Config holds configuration for the memory hotplug controller
type Config struct {
	// Monitoring interval
	MonitorInterval time.Duration

	// Cooldown periods
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration

	// Thresholds (0-100 percentage of current memory)
	ScaleUpThreshold   float64 // Add memory when usage > this %
	ScaleDownThreshold float64 // Remove memory when usage < this %

	// OOM safety margin (keep this much free memory)
	OOMSafetyMarginMB int64

	// Memory increment size (128MB aligned)
	IncrementSize int64

	// Stability requirements (number of consecutive readings)
	ScaleUpStability   int
	ScaleDownStability int

	// Enable/disable features
	EnableScaleDown bool

	// MaxSlots is the number of memory hotplug slots available.
	// This must match the QEMU configuration (VMResourceConfig.MemorySlots).
	// If zero, defaults to 8.
	MaxSlots int
}

// DefaultConfig returns sensible defaults for memory hotplug
func DefaultConfig() Config {
	return Config{
		MonitorInterval:    10 * time.Second,  // Slower than CPU (memory changes less frequently)
		ScaleUpCooldown:    30 * time.Second,  // Longer cooldown (memory ops are expensive)
		ScaleDownCooldown:  60 * time.Second,  // Very conservative scale-down
		ScaleUpThreshold:   85.0,              // Add memory at 85% usage
		ScaleDownThreshold: 60.0,              // Remove memory below 60% usage
		OOMSafetyMarginMB:  128,               // Always keep 128MB free
		IncrementSize:      128 * 1024 * 1024, // 128MB (DIMM slot size)
		ScaleUpStability:   3,                 // Need 3 consecutive high readings (30s)
		ScaleDownStability: 6,                 // Need 6 consecutive low readings (60s)
		EnableScaleDown:    false,             // Disabled by default (memory unplug is risky)
		MaxSlots:           defaultMemorySlots,
	}
}

// noopMemoryController is a no-op implementation of MemoryHotplugController.
// Used when memory hotplug is not needed (maxMemory <= bootMemory).
type noopMemoryController struct{}

func (n *noopMemoryController) Start(ctx context.Context) {}
func (n *noopMemoryController) Stop()                     {}

// Controller manages dynamic memory allocation for a VM based on memory usage
type Controller struct {
	containerID   string
	qmpClient     qmpMemoryClient
	stats         StatsProvider
	offlineMemory MemoryOffliner
	onlineMemory  MemoryOnliner

	// Resource limits
	bootMemory int64 // Minimum memory (never go below this)
	maxMemory  int64 // Maximum memory (ceiling)

	// Current state (protected by mu)
	mu            sync.Mutex
	currentMemory int64        // Current online memory in bytes
	usedSlots     map[int]bool // Track which memory slots are used

	// Configuration
	config Config

	// Memory usage sampling
	lastSampleTime  time.Time
	lastMemoryUsage int64

	// Shared monitor handles lifecycle and stability tracking
	monitor *hotplug.Monitor
}

// NewController creates a new memory hotplug controller.
// Returns a no-op controller if hotplug is not needed (maxMemory <= bootMemory).
// In production, qmpClient should be a *qemu.QMPClient.
func NewController(
	containerID string,
	qmpClient qmpMemoryClient,
	stats StatsProvider,
	offliner MemoryOffliner,
	onliner MemoryOnliner,
	bootMemory, maxMemory int64,
	config Config,
) MemoryHotplugController {
	// Return no-op controller if hotplug is not needed
	if maxMemory <= bootMemory {
		return &noopMemoryController{}
	}

	// Apply default for MaxSlots if not set
	if config.MaxSlots < 1 {
		config.MaxSlots = defaultMemorySlots
	}

	c := &Controller{
		containerID:   containerID,
		qmpClient:     qmpClient,
		stats:         stats,
		offlineMemory: offliner,
		onlineMemory:  onliner,
		bootMemory:    bootMemory,
		maxMemory:     maxMemory,
		currentMemory: bootMemory,
		usedSlots:     make(map[int]bool),
		config:        config,
	}

	// Create shared monitor with memory-specific scaler
	c.monitor = hotplug.NewMonitor(c, hotplug.MonitorConfig{
		MonitorInterval:    config.MonitorInterval,
		ScaleUpCooldown:    config.ScaleUpCooldown,
		ScaleDownCooldown:  config.ScaleDownCooldown,
		ScaleUpStability:   config.ScaleUpStability,
		ScaleDownStability: config.ScaleDownStability,
		EnableScaleDown:    config.EnableScaleDown,
	})

	return c
}

// Start begins monitoring memory usage and managing hotplug
func (c *Controller) Start(ctx context.Context) {
	log.G(ctx).WithFields(log.Fields{
		"container_id":       c.containerID,
		"boot_memory_mb":     c.bootMemory / (1024 * 1024),
		"max_memory_mb":      c.maxMemory / (1024 * 1024),
		"scale_up_threshold": c.config.ScaleUpThreshold,
		"scale_down_enabled": c.config.EnableScaleDown,
	}).Info("memory-hotplug: controller starting")

	c.monitor.Start(ctx)
}

// Stop terminates the monitoring loop
func (c *Controller) Stop() {
	c.monitor.Stop()
}

// Name implements hotplug.ResourceScaler
func (c *Controller) Name() string {
	return "memory"
}

// ContainerID implements hotplug.ResourceScaler
func (c *Controller) ContainerID() string {
	return c.containerID
}

// EvaluateScaling implements hotplug.ResourceScaler
func (c *Controller) EvaluateScaling(ctx context.Context) (hotplug.ScaleDirection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Query current memory state from QEMU
	summary, err := c.qmpClient.QueryMemorySizeSummary(ctx)
	if err != nil {
		return hotplug.ScaleNone, fmt.Errorf("query memory summary: %w", err)
	}

	totalMemory := summary.BaseMemory + summary.PluggedMemory
	c.currentMemory = totalMemory

	// Sample memory usage
	usagePct, ok, err := c.sampleMemory(ctx)
	if err != nil {
		log.G(ctx).WithError(err).WithField("container_id", c.containerID).
			Warn("memory-hotplug: failed to sample memory usage")
		return hotplug.ScaleNone, nil
	}
	if !ok {
		return hotplug.ScaleNone, nil
	}

	// Calculate free memory
	usedMemory := int64(float64(c.currentMemory) * usagePct / 100.0)
	freeMemory := c.currentMemory - usedMemory
	safetyMargin := c.config.OOMSafetyMarginMB * 1024 * 1024

	log.G(ctx).WithFields(log.Fields{
		"container_id":      c.containerID,
		"usage_pct":         fmt.Sprintf("%.2f", usagePct),
		"current_memory_mb": c.currentMemory / (1024 * 1024),
		"free_mb":           freeMemory / (1024 * 1024),
	}).Debug("memory-hotplug: memory usage sample")

	// Check scale up: usage > threshold AND free memory < safety margin
	if usagePct >= c.config.ScaleUpThreshold && freeMemory < safetyMargin && c.currentMemory < c.maxMemory {
		return hotplug.ScaleUp, nil
	}

	// Check scale down: usage < threshold
	if c.config.EnableScaleDown && c.currentMemory > c.bootMemory {
		if usagePct < c.config.ScaleDownThreshold {
			// Verify we'd still have safety margin after removal
			newMemory := c.currentMemory - c.config.IncrementSize
			if newMemory >= c.bootMemory {
				projectedFree := newMemory - usedMemory
				if projectedFree >= safetyMargin {
					return hotplug.ScaleDown, nil
				}
			}
		}
	}

	return hotplug.ScaleNone, nil
}

// ScaleUp implements hotplug.ResourceScaler
func (c *Controller) ScaleUp(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Find available slot
	slotID := c.findFreeSlot()
	if slotID < 0 {
		log.G(ctx).WithField("container_id", c.containerID).
			Warn("memory-hotplug: no free memory slots available")
		return fmt.Errorf("no free memory slots available")
	}

	targetMemory := c.currentMemory + c.config.IncrementSize
	if targetMemory > c.maxMemory {
		targetMemory = c.maxMemory
	}
	amountToAdd := targetMemory - c.currentMemory

	usagePct := float64(c.lastMemoryUsage) / float64(c.currentMemory) * 100.0
	freeMemory := c.currentMemory - c.lastMemoryUsage

	log.G(ctx).WithFields(log.Fields{
		"container_id":      c.containerID,
		"current_memory_mb": c.currentMemory / (1024 * 1024),
		"target_memory_mb":  targetMemory / (1024 * 1024),
		"add_mb":            amountToAdd / (1024 * 1024),
		"slot_id":           slotID,
		"usage_pct":         fmt.Sprintf("%.2f", usagePct),
		"free_mb":           freeMemory / (1024 * 1024),
	}).Info("memory-hotplug: scaling up memory")

	// Hotplug memory via QMP
	if err := c.qmpClient.HotplugMemory(ctx, slotID, amountToAdd); err != nil {
		return fmt.Errorf("failed to hotplug memory: %w", err)
	}

	// Mark slot as used
	c.usedSlots[slotID] = true

	// Online memory in guest - required for memory to be usable
	if err := c.onlineMemory(ctx, slotID); err != nil {
		log.G(ctx).WithError(err).WithField("slot_id", slotID).
			Error("memory-hotplug: failed to online memory in guest")
		// Memory was allocated via QMP but is not usable by guest
		// Try to unplug it to avoid wasting resources
		if unplugErr := c.qmpClient.UnplugMemory(ctx, slotID); unplugErr != nil {
			log.G(ctx).WithError(unplugErr).WithField("slot_id", slotID).
				Warn("memory-hotplug: failed to unplug unusable memory")
		}
		delete(c.usedSlots, slotID)
		return fmt.Errorf("memory allocated but failed to online in guest: %w", err)
	}

	c.currentMemory = targetMemory
	return nil
}

// ScaleDown implements hotplug.ResourceScaler
func (c *Controller) ScaleDown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Find used slot to remove (last added, LIFO)
	slotID := c.findUsedSlot()
	if slotID < 0 {
		return fmt.Errorf("no used memory slots to remove")
	}

	targetMemory := c.currentMemory - c.config.IncrementSize
	if targetMemory < c.bootMemory {
		targetMemory = c.bootMemory
	}
	amountToRemove := c.currentMemory - targetMemory

	usagePct := float64(c.lastMemoryUsage) / float64(c.currentMemory) * 100.0
	projectedFree := targetMemory - c.lastMemoryUsage

	log.G(ctx).WithFields(log.Fields{
		"container_id":      c.containerID,
		"current_memory_mb": c.currentMemory / (1024 * 1024),
		"target_memory_mb":  targetMemory / (1024 * 1024),
		"remove_mb":         amountToRemove / (1024 * 1024),
		"slot_id":           slotID,
		"usage_pct":         fmt.Sprintf("%.2f", usagePct),
		"projected_free_mb": projectedFree / (1024 * 1024),
	}).Info("memory-hotplug: scaling down memory")

	// Offline memory in guest first
	if err := c.offlineMemory(ctx, slotID); err != nil {
		log.G(ctx).WithError(err).WithField("slot_id", slotID).
			Warn("memory-hotplug: failed to offline memory in guest")
		return fmt.Errorf("failed to offline memory: %w", err)
	}

	// Unplug memory via QMP
	if err := c.qmpClient.UnplugMemory(ctx, slotID); err != nil {
		// Try to bring memory back online if unplug failed
		if onlineErr := c.onlineMemory(ctx, slotID); onlineErr != nil {
			log.G(ctx).WithError(onlineErr).WithField("slot_id", slotID).
				Error("memory-hotplug: CRITICAL - failed to re-online memory after unplug failure, guest may have offline memory")
		}
		return fmt.Errorf("failed to unplug memory: %w", err)
	}

	// Mark slot as free
	delete(c.usedSlots, slotID)
	c.currentMemory = targetMemory

	return nil
}

// sampleMemory samples current memory usage and returns percentage
// Returns (usagePercentage, dataValid, error)
func (c *Controller) sampleMemory(ctx context.Context) (float64, bool, error) {
	if c.stats == nil {
		return 0, false, nil
	}

	usageBytes, err := c.stats(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("failed to get memory stats: %w", err)
	}

	now := time.Now()
	if c.lastSampleTime.IsZero() {
		// First sample
		c.lastSampleTime = now
		c.lastMemoryUsage = usageBytes
		return 0, false, nil
	}

	// Calculate usage percentage
	usagePct := float64(usageBytes) / float64(c.currentMemory) * 100.0

	c.lastSampleTime = now
	c.lastMemoryUsage = usageBytes

	return usagePct, true, nil
}

// findFreeSlot finds the first available memory slot
func (c *Controller) findFreeSlot() int {
	for i := range c.config.MaxSlots {
		if !c.usedSlots[i] {
			return i
		}
	}
	return -1
}

// findUsedSlot finds a used memory slot (LIFO - last added first)
func (c *Controller) findUsedSlot() int {
	for i := c.config.MaxSlots - 1; i >= 0; i-- {
		if c.usedSlots[i] {
			return i
		}
	}
	return -1
}
