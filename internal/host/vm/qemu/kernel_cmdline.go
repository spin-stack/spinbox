//go:build linux

package qemu

import (
	"fmt"
	"strings"

	"github.com/spin-stack/spinbox/internal/host/vm"
	"github.com/spin-stack/spinbox/internal/vsock"
)

// KernelCmdlineConfig holds the configuration for building a kernel command line.
type KernelCmdlineConfig struct {
	// Console device (e.g., "ttyS0")
	Console string

	// Vsock configuration
	VsockRPCPort    uint32
	VsockStreamPort uint32
	VsockCID        uint32

	// Network configuration (optional)
	Network *vm.NetworkConfig

	// Additional init arguments
	InitArgs []string

	// Quiet boot (reduces kernel messages)
	Quiet bool

	// Log level (0-7, lower is more verbose)
	LogLevel int

	// ExtrasDiskIndex is the 0-based index of the extras disk (nil if none).
	// The guest parses spin.extras_disk=N to locate the block device.
	ExtrasDiskIndex *int

	// Debug enables boot profiling: it turns on initcall_debug and forces
	// verbose kernel output (overriding Quiet/LogLevel) so per-initcall
	// timings land in the console log. Off by default; opt in for profiling.
	Debug bool
}

// DefaultKernelCmdlineConfig returns a default configuration.
func DefaultKernelCmdlineConfig() KernelCmdlineConfig {
	return KernelCmdlineConfig{
		Console:         "ttyS0",
		VsockRPCPort:    vsock.DefaultRPCPort,
		VsockStreamPort: vsock.DefaultStreamPort,
		Quiet:           true,
		LogLevel:        3,
	}
}

// BuildKernelCmdline constructs the kernel command line from the configuration.
func BuildKernelCmdline(cfg KernelCmdlineConfig) string {
	var parts []string

	// Console. Debug/profiling mode routes the kernel console through
	// virtio-console (hvc0) instead of the emulated 8250 UART (ttyS0): every
	// byte written to ttyS0 is a PIO VMEXIT, so the verbose initcall_debug
	// stream backpressures the guest and inflates the very per-initcall timings
	// we are measuring (the chattiest initcalls - ACPI, PCI - are penalized
	// most). virtio-console batches output over a virtqueue, so it does not skew
	// the profile. ACPI/PCI run as subsys_initcalls, before virtio-console
	// registers (a device_initcall), so their early messages buffer in the
	// printk ring and flush once hvc0 comes up - timing stays accurate.
	// Note: a hang before the device-initcall phase would leave the console log
	// empty, an acceptable trade-off for a profiling-only mode.
	console := cfg.Console
	if cfg.Debug && console != "" {
		console = "hvc0"
	}
	if console != "" {
		parts = append(parts, fmt.Sprintf("console=%s", console))
	}

	// Boot verbosity. Debug mode forces verbose output so initcall timings
	// are visible; otherwise honor the configured quiet/loglevel.
	quiet, loglevel := cfg.Quiet, cfg.LogLevel
	if cfg.Debug {
		quiet, loglevel = false, 8
	}
	if quiet {
		parts = append(parts, "quiet")
	}
	parts = append(parts, fmt.Sprintf("loglevel=%d", loglevel))

	// Systemd options
	parts = append(parts,
		"systemd.show_status=0",
		"systemd.log_level=warning",
	)

	// Panic behavior
	parts = append(parts, "panic=1")

	// Network naming
	parts = append(parts, "net.ifnames=0", "biosdevname=0")

	// Cgroup v2
	parts = append(parts,
		"systemd.unified_cgroup_hierarchy=1",
		"cgroup_no_v1=all",
	)

	// Disable tickless kernel (reduces overhead for short-lived VMs)
	parts = append(parts, "nohz=off")

	// Boot-speed tuning for a KVM guest:
	//   no_timer_check            - skip the boot-time timer IRQ delivery probe
	//   tsc=reliable              - trust the TSC and skip the clocksource watchdog
	//   rcupdate.rcu_expedited=1  - expedite RCU grace periods during boot
	//   pci=lastbus=0             - stop PCI enumeration after bus 0. On our q35
	//                               machine every virtio device sits on the root
	//                               bus (pcie.0), so scanning buses 1-255 is pure
	//                               boot-time overhead. If a device is ever placed
	//                               behind a bridge this must be revisited - the
	//                               integration tests (virtio-blk/-net) are the gate.
	parts = append(parts,
		"no_timer_check",
		"tsc=reliable",
		"rcupdate.rcu_expedited=1",
		"pci=lastbus=0",
	)

	// Boot profiling: print per-initcall timings to the console log.
	//
	// log_buf_len enlarges the printk ring buffer for the profiling boot. The
	// console is routed through virtio-console (hvc0, see above), which only
	// registers at the device_initcall phase; every message emitted before that
	// - the verbose ACPI/PCI dumps plus initcall_debug lines for every early
	// (core/subsys) initcall - accumulates in the ring and is replayed once hvc0
	// comes up. The default 256 KiB ring (CONFIG_LOG_BUF_SHIFT=18) overflows
	// under that load and silently drops the earliest entries, so the early
	// initcalls vanish from the profile. 4 MiB holds the whole pre-hvc0 burst.
	if cfg.Debug {
		parts = append(parts, "initcall_debug", "printk.time=1", "log_buf_len=4M")
		// Userspace companion to initcall_debug: vminitd emits VMINITD_PROFILE
		// lines for its boot phases when this marker is present (see
		// system.BootProfiler). Kept as a separate token so the kernel ignores
		// it and it reaches /proc/cmdline for vminitd to read.
		parts = append(parts, "spin.profile")
	}

	// Network configuration
	if netParam := buildNetworkParam(cfg.Network); netParam != "" {
		parts = append(parts, netParam)
	}

	// Extras disk index for guest to locate the extras block device
	if cfg.ExtrasDiskIndex != nil {
		parts = append(parts, fmt.Sprintf("spin.extras_disk=%d", *cfg.ExtrasDiskIndex))
	}

	// Init command with vsock args
	initArgs := buildInitArgs(cfg)
	parts = append(parts, fmt.Sprintf("init=/sbin/vminitd -- %s", formatInitArgs(initArgs)))

	return strings.Join(parts, " ")
}

// buildNetworkParam builds the ip= kernel parameter for network configuration.
func buildNetworkParam(netCfg *vm.NetworkConfig) string {
	if netCfg == nil || netCfg.IP == "" {
		return ""
	}

	// IPv4 configuration using kernel ip= parameter format:
	// ip=<client-ip>:<server-ip>:<gw-ip>:<netmask>:<hostname>:<device>:<autoconf>:<dns0-ip>:<dns1-ip>
	var b strings.Builder
	fmt.Fprintf(&b, "ip=%s::%s:%s::eth0:none",
		netCfg.IP,
		netCfg.Gateway,
		netCfg.Netmask)

	// Append DNS servers (kernel supports up to 2)
	for i, dns := range netCfg.DNS {
		if i >= 2 {
			break
		}
		b.WriteString(":")
		b.WriteString(dns)
	}

	return b.String()
}

// buildInitArgs constructs the init arguments list.
func buildInitArgs(cfg KernelCmdlineConfig) []string {
	args := []string{
		fmt.Sprintf("-vsock-rpc-port=%d", cfg.VsockRPCPort),
		fmt.Sprintf("-vsock-stream-port=%d", cfg.VsockStreamPort),
		fmt.Sprintf("-vsock-cid=%d", cfg.VsockCID),
	}
	return append(args, cfg.InitArgs...)
}
