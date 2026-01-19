//go:build linux

package system

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/log"
	"golang.org/x/sys/unix"

	"github.com/spin-stack/spinbox/internal/guest/vminit/bundle"
)

// Cleanup performs pre-shutdown cleanup to remove temporary files and unmount
// filesystems. This should be called before VM poweroff to ensure snapshots
// don't contain stale container data.
//
// Cleanup operations:
// 1. Unmount all mounts under /run/bundles recursively
// 2. Remove bundle directories
// 3. Clean /tmp contents
// 4. Sync filesystems to flush pending writes
func Cleanup(ctx context.Context) {
	log.G(ctx).Info("starting pre-shutdown cleanup")

	// 1. Unmount and remove bundle directories
	cleanupBundles(ctx)

	// 2. Clean /tmp contents
	cleanupTmp(ctx)

	// 3. Sync filesystems to ensure all writes are flushed
	syncFilesystems(ctx)

	log.G(ctx).Info("pre-shutdown cleanup completed")
}

// cleanupBundles unmounts all mounts under /run/bundles and removes the directories.
func cleanupBundles(ctx context.Context) {
	bundleRoot := bundle.RootDir

	// Check if bundle root exists
	if _, err := os.Stat(bundleRoot); os.IsNotExist(err) {
		log.G(ctx).Debug("bundle root does not exist, skipping bundle cleanup")
		return
	}

	// First, get all mount points under bundle root and unmount them
	// We need to unmount in reverse order (deepest first)
	mounts, err := getMountsUnder(bundleRoot)
	if err != nil {
		log.G(ctx).WithError(err).Warn("failed to get mounts under bundle root")
	} else {
		// Sort mounts by path length descending (deepest first)
		sort.Slice(mounts, func(i, j int) bool {
			return len(mounts[i]) > len(mounts[j])
		})

		for _, mountPath := range mounts {
			log.G(ctx).WithField("path", mountPath).Debug("unmounting")
			// Use MNT_DETACH for lazy unmount - succeeds even with open file handles
			if err := mount.UnmountAll(mountPath, unix.MNT_DETACH); err != nil {
				log.G(ctx).WithError(err).WithField("path", mountPath).Warn("failed to unmount")
			}
		}
	}

	// Now remove the bundle directories
	entries, err := os.ReadDir(bundleRoot)
	if err != nil {
		log.G(ctx).WithError(err).Warn("failed to read bundle root directory")
		return
	}

	for _, entry := range entries {
		path := filepath.Join(bundleRoot, entry.Name())
		log.G(ctx).WithField("path", path).Debug("removing bundle directory")
		if err := os.RemoveAll(path); err != nil {
			log.G(ctx).WithError(err).WithField("path", path).Warn("failed to remove bundle directory")
		}
	}

	log.G(ctx).WithField("count", len(entries)).Info("cleaned up bundle directories")
}

// cleanupTmp removes all contents under /tmp.
func cleanupTmp(ctx context.Context) {
	tmpDir := "/tmp"

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.G(ctx).WithError(err).Warn("failed to read /tmp directory")
		return
	}

	removed := 0
	for _, entry := range entries {
		path := filepath.Join(tmpDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			log.G(ctx).WithError(err).WithField("path", path).Warn("failed to remove temp file")
		} else {
			removed++
		}
	}

	if removed > 0 {
		log.G(ctx).WithField("count", removed).Info("cleaned up temp files")
	}
}

// syncFilesystems syncs all filesystems to ensure pending writes are flushed.
func syncFilesystems(ctx context.Context) {
	log.G(ctx).Debug("syncing filesystems")
	unix.Sync()
}

// getMountsUnder returns all mount points that are under the given path.
func getMountsUnder(root string) ([]string, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}

	var mounts []string
	root = filepath.Clean(root)
	if !strings.HasSuffix(root, "/") {
		root += "/"
	}

	for line := range strings.SplitSeq(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		mountPoint := fields[1]
		// Check if mount point is under root (but not root itself)
		if strings.HasPrefix(mountPoint, root) {
			mounts = append(mounts, mountPoint)
		}
	}

	return mounts, nil
}
