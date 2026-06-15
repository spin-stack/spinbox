//go:build linux && integration

package integration

import (
	"strings"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// TestContainerdCommitGateRunningFails verifies the OFD-lock commit gate:
// committing a running container's active snapshot must fail, because QEMU holds
// a file.locking F_WRLCK on rwlayer.img and the snapshotter refuses to commit a
// layer that is still owned by a live VM. This closes the loop with the
// file.locking=on pin on the writable drive.
func TestContainerdCommitGateRunningFails(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	name := "qbx-commit-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	snapshotKey := name + "-snapshot"
	committed := name + "-committed"

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithProcessArgs("/bin/sleep", "300"),
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

	// The VM is running, so QEMU holds the rwlayer.img lock: committing the
	// active snapshot must fail.
	ss := client.SnapshotService(cfg.Snapshotter)
	commitErr := ss.Commit(ctx, committed, snapshotKey)
	if commitErr == nil {
		// If it unexpectedly succeeded, clean up the committed snapshot.
		if rmErr := ss.Remove(ctx, committed); rmErr != nil {
			t.Logf("cleanup committed snapshot %s: %v", committed, rmErr)
		}
		t.Fatalf("commit of a running container's snapshot must fail (OFD lock gate), but it succeeded")
	}
	t.Logf("commit correctly rejected while running: %v", commitErr)

	// The committed snapshot must not have been created.
	if _, statErr := ss.Stat(ctx, committed); statErr == nil {
		_ = ss.Remove(ctx, committed)
		t.Fatalf("committed snapshot %s must not exist after a gated commit", committed)
	}

	// Stop the container.
	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatalf("kill task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for task %s to exit after kill", name)
	}
}
