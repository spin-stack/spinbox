//go:build linux

package system

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/containerd/log"
	"golang.org/x/sys/unix"
)

// kmsgInitcallRE matches an initcall_debug completion line embedded in a
// /dev/kmsg record message, e.g.:
//
//	initcall pci_subsys_init+0x0/0x40 returned 0 after 12345 usecs
var kmsgInitcallRE = regexp.MustCompile(`initcall (\S+) returned \S+ after (\d+) usecs`)

// kmsgTopN bounds how many of the slowest initcalls we print.
const kmsgTopN = 25

type kmsgInitcall struct {
	name string
	usec int
}

// DumpKernelBootProfile reads the kernel ring buffer (/dev/kmsg) and emits a
// compact KMSG_PROFILE summary of the boot's initcalls.
//
// Unlike the console-based kernel profile, this sees the *complete* set -
// including the early core/subsys initcalls (acpi_init, pci_subsys_init, ...)
// that run before virtio-console registers and that the hvc0 console therefore
// never replays. /dev/kmsg holds the full kernel log regardless of console, and
// log_buf_len=4M (set in debug-boot) keeps it from wrapping. last_ts_us is the
// timestamp of the last kernel record seen at drain time; it is only a rough
// upper bound on kernel boot - driver/handoff messages emitted after PID1
// starts push it past the real boundary. For the precise kernel-boot number use
// the VMINITD_READY pid1-entry stamp (CLOCK_BOOTTIME at init exec). The gap
// between last_ts_us and sum_us still indicates non-initcall kernel work
// (decompression, mm/SMP bring-up, ...).
//
// No-op unless boot profiling (spin.profile) is enabled.
func DumpKernelBootProfile(ctx context.Context) {
	if !profileEnabled() {
		return
	}

	calls, lastTS, err := readKmsgInitcalls()
	if err != nil {
		log.G(ctx).WithError(err).Warn("kmsg boot profile: read failed")
		return
	}
	if len(calls) == 0 {
		log.G(ctx).Warn("kmsg boot profile: no initcall_debug records (is initcall_debug set?)")
		return
	}

	sum := 0
	for _, c := range calls {
		sum += c.usec
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].usec > calls[j].usec })

	log.G(ctx).Infof("KMSG_PROFILE initcalls=%d sum_us=%d last_ts_us=%d", len(calls), sum, lastTS)
	for i, c := range calls {
		if i >= kmsgTopN {
			break
		}
		log.G(ctx).Infof("KMSG_PROFILE %8d us  %s", c.usec, c.name)
	}
}

// readKmsgInitcalls reads all currently-buffered /dev/kmsg records and returns
// the initcall timings plus the highest record timestamp seen (microseconds
// since boot).
func readKmsgInitcalls() ([]kmsgInitcall, int64, error) {
	// O_NONBLOCK so Read returns EAGAIN once we have drained the buffer instead
	// of blocking for future messages. Raw unix.Read (not os.File) avoids the Go
	// runtime poller, which would otherwise wait on EAGAIN.
	fd, err := unix.Open("/dev/kmsg", unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Close(fd) }()

	var (
		calls  []kmsgInitcall
		lastTS int64
		buf    = make([]byte, 8192) // the kernel returns one record per read
	)

	for {
		n, err := unix.Read(fd, buf)
		if err != nil {
			switch {
			case errors.Is(err, unix.EAGAIN), errors.Is(err, unix.EWOULDBLOCK):
				// Drained: caught up to the end of the buffer.
				return calls, lastTS, nil
			case errors.Is(err, unix.EINTR):
				continue
			case errors.Is(err, unix.EPIPE):
				// A record was overwritten between reads; the position has moved
				// on, so keep reading.
				continue
			default:
				return calls, lastTS, err
			}
		}
		if n == 0 {
			return calls, lastTS, nil
		}

		ts, msg, ok := parseKmsgRecord(string(buf[:n]))
		if !ok {
			continue
		}
		if ts > lastTS {
			lastTS = ts
		}
		if name, usec, ok := extractInitcall(msg); ok {
			calls = append(calls, kmsgInitcall{name: name, usec: usec})
		}
	}
}

// parseKmsgRecord splits one /dev/kmsg record into its timestamp (microseconds)
// and message text. The record format is:
//
//	<priority>,<seq>,<timestamp_us>,<flags>[,...];<message>\n[ \t<continuation>...]
//
// ok is false only when there is no ';' separating header from message.
func parseKmsgRecord(record string) (tsUS int64, msg string, ok bool) {
	semi := strings.IndexByte(record, ';')
	if semi < 0 {
		return 0, "", false
	}

	msg = record[semi+1:]
	if nl := strings.IndexByte(msg, '\n'); nl >= 0 {
		msg = msg[:nl] // drop the trailing newline and any continuation lines
	}

	fields := strings.Split(record[:semi], ",")
	if len(fields) >= 3 {
		if ts, err := strconv.ParseInt(fields[2], 10, 64); err == nil {
			tsUS = ts
		}
	}
	return tsUS, msg, true
}

// extractInitcall pulls the initcall name and duration from a record message.
func extractInitcall(msg string) (name string, usec int, ok bool) {
	m := kmsgInitcallRE.FindStringSubmatch(msg)
	if m == nil {
		return "", 0, false
	}
	usec, err := strconv.Atoi(m[2])
	if err != nil {
		return "", 0, false
	}
	return m[1], usec, true
}
