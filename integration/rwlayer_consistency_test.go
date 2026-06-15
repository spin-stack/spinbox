//go:build linux && integration

package integration

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// findRwlayerImage locates the writable ext4 backing file (rwlayer.img) among a
// snapshot's mounts. The exact shape depends on the snapshotter, so probe a few
// places; returns "" if it cannot be determined.
func findRwlayerImage(mounts []mount.Mount) string {
	for _, m := range mounts {
		if strings.Contains(m.Type, "ext4") && m.Source != "" {
			return m.Source
		}
	}
	for _, m := range mounts {
		for _, o := range m.Options {
			if strings.HasPrefix(o, "device=") && strings.Contains(o, "rwlayer") {
				return strings.TrimPrefix(o, "device=")
			}
		}
		if strings.HasSuffix(m.Source, "rwlayer.img") {
			return m.Source
		}
	}
	return ""
}

// TestContainerdPausedRwlayerIsConsistent verifies the host can read a
// consistent rwlayer.img while the container is paused. A container churns the
// writable filesystem (write + sync in a loop); Pause issues FIFREEZE before
// QMP stop, so the on-disk ext4 is quiesced and structurally consistent even
// mid-churn. e2fsck -fn on the backing file (read-only) must report clean.
func TestContainerdPausedRwlayerIsConsistent(t *testing.T) {
	e2fsck, err := exec.LookPath("e2fsck")
	if err != nil {
		t.Skip("e2fsck not available; skipping rwlayer consistency check")
	}

	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	name := "qbx-rwconsist-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	snapshotKey := name + "-snapshot"

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			// Churn the writable layer: append + sync in a tight-ish loop so the
			// ext4 journal is constantly active. Only a real freeze makes the
			// on-disk image consistent while this runs.
			oci.WithProcessArgs("/bin/sh", "-c", "i=0; while true; do i=$((i+1)); echo $i >> /churn.log; sync; sleep 0.05; done"),
		),
	)
	if err != nil {
		t.Fatalf("create container %s: %v", name, err)
	}
	defer func() {
		if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
			t.Logf("cleanup container %s: %v", name, err)
		}
	}()

	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		t.Fatalf("create task for %s: %v", name, err)
	}
	defer func() {
		if _, err := task.Delete(ctx, containerd.WithProcessKill); err != nil {
			if !strings.Contains(err.Error(), "ttrpc: closed") {
				t.Logf("cleanup task for %s: %v", name, err)
			}
		}
	}()

	exitCh, err := task.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for task %s: %v", name, err)
	}
	if err := task.Start(ctx); err != nil {
		t.Fatalf("start task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Running)

	// Let the churn run so the filesystem has in-flight journal activity.
	time.Sleep(2 * time.Second)

	// Locate the writable backing image before pausing.
	mounts, err := client.SnapshotService(cfg.Snapshotter).Mounts(ctx, snapshotKey)
	if err != nil {
		t.Fatalf("snapshot mounts for %s: %v", snapshotKey, err)
	}
	rwlayer := findRwlayerImage(mounts)
	if rwlayer == "" {
		t.Skipf("could not locate rwlayer.img in snapshot mounts: %+v", mounts)
	}

	// Pause: FIFREEZE then QMP stop. The on-disk ext4 must now be consistent.
	if err := task.Pause(ctx); err != nil {
		t.Fatalf("pause task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Paused)

	// Read-only consistency check of the frozen backing image from the host.
	// -f forces the check, -n answers no to all (read-only).
	out, fsckErr := exec.CommandContext(ctx, e2fsck, "-fn", rwlayer).CombinedOutput()
	if fsckErr != nil {
		// Resume before failing so the container is not left frozen.
		_ = task.Resume(ctx)
		t.Fatalf("e2fsck on paused rwlayer %q reported inconsistency: %v\n%s", rwlayer, fsckErr, out)
	}
	t.Logf("paused rwlayer %q is consistent:\n%s", rwlayer, out)

	// Resume the container.
	if err := task.Resume(ctx); err != nil {
		t.Fatalf("resume task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Running)

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatalf("kill task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for task %s to exit after kill", name)
	}
}
