//go:build linux

package system

import (
	"context"
	"os"
	"time"

	"github.com/containerd/log"
)

// profileMarkerParam is the kernel cmdline token that enables guest userspace
// boot profiling. The host adds it in debug-boot mode (alongside
// initcall_debug); vminitd then emits VMINITD_PROFILE lines for its boot
// phases, the userspace companion to the kernel's KERNEL_PROFILE/initcall_debug
// output.
const profileMarkerParam = "spin.profile"

// BootProfiler emits grep-able VMINITD_PROFILE lines attributing guest
// userspace boot time to named phases. It is a no-op unless enabled by the
// spin.profile cmdline marker, so normal boots get no extra log noise.
//
// Each Mark prints one line:
//
//	VMINITD_PROFILE <phase> delta_us=<n> total_us=<n>
//
// where delta_us is the time since the previous Mark and total_us the time
// since the profiler's start.
type BootProfiler struct {
	start time.Time
	last  time.Time

	// enabled is resolved lazily on the first Mark, not in the constructor: the
	// profiler is created before system.Initialize mounts /proc, so reading
	// /proc/cmdline any earlier always fails and would wedge profiling off. The
	// first Mark fires right after mountFilesystems, by which point /proc exists.
	resolved bool
	enabled  bool
}

// NewBootProfiler returns a profiler whose timings are relative to start.
// Profiling is enabled only when spin.profile is present in /proc/cmdline; that
// decision is deferred to the first Mark (see the resolved field).
func NewBootProfiler(start time.Time) *BootProfiler {
	return &BootProfiler{
		start: start,
		last:  start,
	}
}

func profileEnabled() bool {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return false
	}
	return hasParam(string(cmdline), profileMarkerParam)
}

// Mark records the elapsed time under phase and resets the per-phase clock. It
// is safe to call on a nil or disabled profiler (no-op). The enabled decision
// is resolved on the first call, once /proc/cmdline is readable.
func (p *BootProfiler) Mark(ctx context.Context, phase string) {
	if p == nil {
		return
	}
	if !p.resolved {
		p.enabled = profileEnabled()
		p.resolved = true
	}
	if !p.enabled {
		return
	}
	now := time.Now()
	delta := now.Sub(p.last)
	total := now.Sub(p.start)
	p.last = now
	log.G(ctx).Infof("VMINITD_PROFILE %s delta_us=%d total_us=%d",
		phase, delta.Microseconds(), total.Microseconds())
}
