//go:build linux

package system

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/containerd/log"
	"golang.org/x/sys/unix"
)

// FIFREEZE/FITHAW freeze and thaw a filesystem. They are not exported by
// x/sys/unix, so define them here. Both are _IOWR('X', 119|120, int); the int
// argument is ignored by the kernel.
const (
	fifreeze = 0xC0045877
	fithaw   = 0xC0045878
)

// freezableFstypes are the writable filesystem types quiesced for a consistent
// snapshot. The container's writable layer (rwlayer) is ext4; read-only base
// layers are erofs and need no freeze.
var freezableFstypes = map[string]bool{"ext4": true}

// FreezeWritableFilesystems freezes the container's writable filesystem(s) via
// FIFREEZE and returns the mount points that were frozen. This flushes dirty
// page cache to the block device and quiesces the on-disk filesystem so the
// backing image can be read consistently. On partial failure it thaws whatever
// it already froze and returns the error.
func FreezeWritableFilesystems(ctx context.Context) ([]string, error) {
	mounts, err := writableMountPoints()
	if err != nil {
		return nil, err
	}

	var frozen []string
	for _, mp := range mounts {
		if err := ioctlNoArg(mp, fifreeze); err != nil {
			if thawErr := ThawFilesystems(ctx, frozen); thawErr != nil {
				log.G(ctx).WithError(thawErr).Warn("rollback thaw failed after freeze error")
			}
			return nil, fmt.Errorf("FIFREEZE %s: %w", mp, err)
		}
		frozen = append(frozen, mp)
		log.G(ctx).WithField("mount", mp).Debug("froze filesystem")
	}
	return frozen, nil
}

// ThawFilesystems thaws the given mount points via FITHAW. It is best-effort and
// idempotent: it attempts every path and returns the first error, if any.
func ThawFilesystems(ctx context.Context, paths []string) error {
	var firstErr error
	for _, mp := range paths {
		if err := ioctlNoArg(mp, fithaw); err != nil {
			log.G(ctx).WithError(err).WithField("mount", mp).Warn("FITHAW failed")
			if firstErr == nil {
				firstErr = fmt.Errorf("FITHAW %s: %w", mp, err)
			}
			continue
		}
		log.G(ctx).WithField("mount", mp).Debug("thawed filesystem")
	}
	return firstErr
}

// ioctlNoArg opens the mount point and issues an argument-less freeze/thaw ioctl.
func ioctlNoArg(mountPoint string, req uint) error {
	f, err := os.Open(mountPoint)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return unix.IoctlSetInt(int(f.Fd()), req, 0)
}

// writableMountPoints returns mount points whose filesystem type is freezable,
// parsed from /proc/self/mountinfo.
func writableMountPoints() ([]string, error) {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	return parseFreezableMounts(data), nil
}

// parseFreezableMounts extracts freezable mount points from mountinfo content.
func parseFreezableMounts(data []byte) []string {
	var mps []string
	for _, line := range strings.Split(string(data), "\n") {
		// mountinfo format (man 5 proc):
		//   id parent major:minor root mountpoint opts... - fstype source superopts
		// The fstype follows the " - " separator field.
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || sep+1 >= len(fields) {
			continue
		}
		if freezableFstypes[fields[sep+1]] {
			mps = append(mps, unescapeMountField(fields[4]))
		}
	}
	return mps
}

// unescapeMountField decodes the octal escapes (\040 space, \011 tab, \012
// newline, \134 backslash) that the kernel applies to mountinfo path fields.
func unescapeMountField(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if v, ok := parseOctal3(s[i+1 : i+4]); ok {
				b.WriteByte(v)
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func parseOctal3(s string) (byte, bool) {
	if len(s) != 3 {
		return 0, false
	}
	var v int
	for i := range 3 {
		if s[i] < '0' || s[i] > '7' {
			return 0, false
		}
		v = v*8 + int(s[i]-'0')
	}
	return byte(v), true
}
