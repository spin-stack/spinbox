// Package cpuhotplug provides CPU hotplug control for QEMU VMs.
package cpuhotplug

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/log"

	"github.com/spin-stack/spinbox/internal/host/vm"
	"github.com/spin-stack/spinbox/internal/shim/hotplug"
)

// StatsProvider returns cgroup CPU usage and throttling in microseconds.
type StatsProvider func(ctx context.Context) (usageUsec, throttledUsec uint64, err error)

// CPUOffliner offlines a CPU in the guest before unplug.
type CPUOffliner func(ctx context.Context, cpuID int) error

// CPUOnliner onlines a CPU in the guest after hotplug.
type CPUOnliner func(ctx context.Context, cpuID int) error

// CPUHotplugController defines the interface for CPU hotplug management.
type CPUHotplugController interface {
	Start(ctx context.Context)
	Stop()
}

// Config holds configuration for the CPU hotplug controller
type Config struct {
	// Monitoring interval
	MonitorInterval time.Duration

	// Cooldown periods
	ScaleUpCooldown   time.Duration
	ScaleDownCooldown time.Duration

	// Thresholds (0-100 percentage)
	ScaleUpThreshold     float64
	ScaleDownThreshold   float64
	ScaleUpThrottleLimit float64

	// Stability requirements (number of consecutive readings)
	ScaleUpStability   int
	ScaleDownStability int

	// Enable/disable features
	EnableScaleDown bool
}

const (
	defaultMonitorInterval    = 5 * time.Second
	defaultScaleUpCooldown    = 10 * time.Second
	defaultScaleDownCooldown  = 30 * time.Second
	defaultScaleUpThreshold   = 80.0
	defaultScaleDownThreshold = 50.0
	defaultThrottleLimit      = 5.0
	defaultScaleUpStability   = 2
	defaultScaleDownStability = 6
)

// DefaultConfig returns sensible defaults for CPU hotplug
func DefaultConfig() Config {
	return Config{
		MonitorInterval:      defaultMonitorInterval,
		ScaleUpCooldown:      defaultScaleUpCooldown,
		ScaleDownCooldown:    defaultScaleDownCooldown,
		ScaleUpThreshold:     defaultScaleUpThreshold,
		ScaleDownThreshold:   defaultScaleDownThreshold,
		ScaleUpThrottleLimit: defaultThrottleLimit,
		ScaleUpStability:     defaultScaleUpStability,
		ScaleDownStability:   defaultScaleDownStability,
		EnableScaleDown:      true,
	}
}

// noopCPUController is a no-op implementation of CPUHotplugController.
type noopCPUController struct{}

func (n *noopCPUController) Start(ctx context.Context) {}
func (n *noopCPUController) Stop()                     {}

// Controller manages dynamic vCPU allocation for a VM based on CPU usage
type Controller struct {
	containerID   string
	cpuHotplugger vm.CPUHotplugger
	stats         StatsProvider
	offlineCPU    CPUOffliner
	onlineCPU     CPUOnliner

	// Resource limits
	bootCPUs int
	maxCPUs  int

	// Current state (protected by mu)
	mu          sync.Mutex
	currentCPUs int

	// Configuration
	config Config

	// CPU usage sampling
	lastSampleTime    time.Time
	lastUsageUsec     uint64
	lastThrottledUsec uint64

	// Shared monitor handles lifecycle and stability tracking
	monitor *hotplug.Monitor
}

// NewController creates a new CPU hotplug controller.
// Returns a no-op controller if hotplug is not needed (maxCPUs <= bootCPUs).
func NewController(containerID string, cpuHotplugger vm.CPUHotplugger, stats StatsProvider, offliner CPUOffliner, onliner CPUOnliner, bootCPUs, maxCPUs int, config Config) CPUHotplugController {
	if maxCPUs <= bootCPUs {
		return &noopCPUController{}
	}

	c := &Controller{
		containerID:   containerID,
		cpuHotplugger: cpuHotplugger,
		stats:         stats,
		offlineCPU:    offliner,
		onlineCPU:     onliner,
		bootCPUs:      bootCPUs,
		maxCPUs:       maxCPUs,
		currentCPUs:   bootCPUs,
		config:        config,
	}

	// Create shared monitor with CPU-specific scaler
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

// Start begins the monitoring loop
func (c *Controller) Start(ctx context.Context) {
	log.G(ctx).WithFields(log.Fields{
		"container_id":       c.containerID,
		"boot_cpus":          c.bootCPUs,
		"max_cpus":           c.maxCPUs,
		"scale_up_threshold": c.config.ScaleUpThreshold,
		"scale_up_throttle":  c.config.ScaleUpThrottleLimit,
	}).Info("cpu-hotplug: controller starting")

	c.monitor.Start(ctx)
}

// Stop gracefully stops the controller
func (c *Controller) Stop() {
	c.monitor.Stop()
}

// Name implements hotplug.ResourceScaler
func (c *Controller) Name() string {
	return "cpu"
}

// ContainerID implements hotplug.ResourceScaler
func (c *Controller) ContainerID() string {
	return c.containerID
}

// EvaluateScaling implements hotplug.ResourceScaler
func (c *Controller) EvaluateScaling(ctx context.Context) (hotplug.ScaleDirection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Query current vCPU count from QEMU
	cpus, err := c.cpuHotplugger.QueryCPUs(ctx)
	if err != nil {
		return hotplug.ScaleNone, fmt.Errorf("query vCPUs: %w", err)
	}

	actualCPUs := len(cpus)
	if actualCPUs != c.currentCPUs {
		log.G(ctx).WithFields(log.Fields{
			"container_id": c.containerID,
			"expected":     c.currentCPUs,
			"actual":       actualCPUs,
		}).Info("cpu-hotplug: CPU count mismatch, updating")
		c.currentCPUs = actualCPUs
	}

	// Sample CPU usage
	usagePct, throttledPct, ok, err := c.sampleCPU(ctx)
	if err != nil {
		log.G(ctx).WithError(err).WithField("container_id", c.containerID).
			Warn("cpu-hotplug: failed to sample CPU usage")
		return hotplug.ScaleNone, nil
	}
	if !ok {
		return hotplug.ScaleNone, nil
	}

	log.G(ctx).WithFields(log.Fields{
		"container_id":  c.containerID,
		"usage_pct":     fmt.Sprintf("%.2f", usagePct),
		"throttled_pct": fmt.Sprintf("%.2f", throttledPct),
		"current_cpus":  c.currentCPUs,
	}).Debug("cpu-hotplug: CPU usage sample")

	// Don't scale if throttled (quota-limited, not CPU-limited)
	if throttledPct >= c.config.ScaleUpThrottleLimit {
		return hotplug.ScaleNone, nil
	}

	// Check scale up
	if usagePct >= c.config.ScaleUpThreshold && c.currentCPUs < c.maxCPUs {
		return hotplug.ScaleUp, nil
	}

	// Check scale down
	if c.config.EnableScaleDown && c.currentCPUs > c.bootCPUs && c.currentCPUs > 1 {
		projectedUsage := usagePct * float64(c.currentCPUs) / float64(c.currentCPUs-1)
		if projectedUsage <= c.config.ScaleDownThreshold {
			return hotplug.ScaleDown, nil
		}
	}

	return hotplug.ScaleNone, nil
}

// ScaleUp implements hotplug.ResourceScaler
func (c *Controller) ScaleUp(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	log.G(ctx).WithFields(log.Fields{
		"container_id": c.containerID,
		"current":      c.currentCPUs,
		"target":       c.currentCPUs + 1,
	}).Info("cpu-hotplug: scaling up vCPUs")

	// Query current CPUs to find available ID
	cpus, err := c.cpuHotplugger.QueryCPUs(ctx)
	if err != nil {
		return fmt.Errorf("query CPUs for scale-up: %w", err)
	}

	existingCPUs := make(map[int]bool, len(cpus))
	for _, cpu := range cpus {
		existingCPUs[cpu.CPUIndex] = true
	}

	// Find first available CPU ID
	for nextID := range c.maxCPUs {
		if existingCPUs[nextID] {
			continue
		}

		if err := c.cpuHotplugger.HotplugCPU(ctx, nextID); err != nil {
			return fmt.Errorf("hotplug CPU %d: %w", nextID, err)
		}

		log.G(ctx).WithFields(log.Fields{
			"container_id": c.containerID,
			"cpu_id":       nextID,
		}).Info("cpu-hotplug: added vCPU")

		// Online the CPU in the guest
		if c.onlineCPU != nil {
			if err := c.onlineCPU(ctx, nextID); err != nil {
				log.G(ctx).WithError(err).WithField("cpu_id", nextID).
					Warn("cpu-hotplug: failed to online vCPU in guest")
			}
		}

		c.currentCPUs++
		return nil
	}

	return fmt.Errorf("no available CPU slot")
}

// ScaleDown implements hotplug.ResourceScaler
func (c *Controller) ScaleDown(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	log.G(ctx).WithFields(log.Fields{
		"container_id": c.containerID,
		"current":      c.currentCPUs,
		"target":       c.currentCPUs - 1,
	}).Info("cpu-hotplug: scaling down vCPUs")

	// Query current CPUs
	cpus, err := c.cpuHotplugger.QueryCPUs(ctx)
	if err != nil {
		return fmt.Errorf("query CPUs for scale-down: %w", err)
	}

	// Find highest CPU ID to remove (never remove CPU 0)
	var removeCPU int = -1
	for _, cpu := range cpus {
		if cpu.CPUIndex > 0 && cpu.CPUIndex > removeCPU {
			removeCPU = cpu.CPUIndex
		}
	}

	if removeCPU < 0 {
		return fmt.Errorf("no removable CPU found")
	}

	// Offline in guest first
	if c.offlineCPU != nil {
		if err := c.offlineCPU(ctx, removeCPU); err != nil {
			log.G(ctx).WithError(err).WithField("cpu_id", removeCPU).
				Warn("cpu-hotplug: failed to offline vCPU in guest")
		}
	}

	// Unplug CPU
	if err := c.cpuHotplugger.UnplugCPU(ctx, removeCPU); err != nil {
		log.G(ctx).WithError(err).WithField("cpu_id", removeCPU).
			Warn("cpu-hotplug: failed to remove vCPU")
		return nil // Best effort, don't fail
	}

	log.G(ctx).WithFields(log.Fields{
		"container_id": c.containerID,
		"cpu_id":       removeCPU,
	}).Info("cpu-hotplug: removed vCPU")

	c.currentCPUs--
	return nil
}

// sampleCPU samples CPU usage and returns usage and throttled percentages.
func (c *Controller) sampleCPU(ctx context.Context) (float64, float64, bool, error) {
	if c.stats == nil {
		return 0, 0, false, nil
	}

	usageUsec, throttledUsec, err := c.stats(ctx)
	if err != nil {
		return 0, 0, false, err
	}

	now := time.Now()
	if c.lastSampleTime.IsZero() {
		c.lastSampleTime = now
		c.lastUsageUsec = usageUsec
		c.lastThrottledUsec = throttledUsec
		return 0, 0, false, nil
	}

	elapsed := now.Sub(c.lastSampleTime)
	if elapsed <= 0 {
		c.lastSampleTime = now
		c.lastUsageUsec = usageUsec
		c.lastThrottledUsec = throttledUsec
		return 0, 0, false, nil
	}

	// Handle counter overflow/reset
	if usageUsec < c.lastUsageUsec || throttledUsec < c.lastThrottledUsec {
		c.lastSampleTime = now
		c.lastUsageUsec = usageUsec
		c.lastThrottledUsec = throttledUsec
		return 0, 0, false, nil
	}

	deltaUsage := usageUsec - c.lastUsageUsec
	deltaThrottled := throttledUsec - c.lastThrottledUsec

	c.lastSampleTime = now
	c.lastUsageUsec = usageUsec
	c.lastThrottledUsec = throttledUsec

	elapsedUsec := float64(elapsed.Microseconds())
	if elapsedUsec <= 0 || c.currentCPUs <= 0 {
		return 0, 0, false, nil
	}

	usagePct := (float64(deltaUsage) / (elapsedUsec * float64(c.currentCPUs))) * 100.0
	if usagePct < 0 {
		usagePct = 0
	}

	throttledPct := (float64(deltaThrottled) / elapsedUsec) * 100.0
	if throttledPct < 0 {
		throttledPct = 0
	}

	return usagePct, throttledPct, true, nil
}
