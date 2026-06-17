//go:build linux

// Package boottime stamps guest boot milestones against CLOCK_BOOTTIME so the
// guest-boot half of cold-start can be decomposed into kernel vs vminitd vs
// host-readiness.
package boottime

import (
	"context"
	"time"

	"github.com/containerd/log"
	"golang.org/x/sys/unix"
)

// Uptime returns time since the kernel started (CLOCK_BOOTTIME). Read at PID1
// entry it is the kernel boot duration (kernel start -> init exec); read later
// it also includes vminitd's own startup.
func Uptime() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return 0
	}
	return time.Duration(ts.Sec)*time.Second + time.Duration(ts.Nsec)*time.Nanosecond
}

// LogReady emits a grep-able VMINITD_READY line stamping a boot phase against
// CLOCK_BOOTTIME (microseconds since kernel start). Always on - it is a handful
// of lines per boot and is the only clean view of where guest-boot time goes:
//
//	pid1-entry   - kernel boot: kernel start -> /sbin/vminitd exec
//	system-init  - + vminitd system.Initialize (mounts, devices, network)
//	vsock-listen - + listener bound, before plugin loading
//	serve        - ttrpc serving (plugin loading done)
//	first-accept - host connected (gap from serve = host-readiness lag)
//
// Deltas between phases attribute the cost; the absolute pid1-entry value is the
// kernel number that last_ts_us in the kmsg profile only approximated.
func LogReady(ctx context.Context, phase string) {
	log.G(ctx).Infof("VMINITD_READY uptime_us=%d phase=%s", Uptime().Microseconds(), phase)
}
