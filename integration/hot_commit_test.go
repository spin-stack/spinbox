//go:build linux && integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/pkg/rootfs"
	"github.com/containerd/platforms"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// quiescedLabel matches the spin-erofs snapshotter constant: set on the diff
// opts to tell it the container is paused and its filesystems frozen, so it
// may read the rwlayer without the exclusive image lock the paused VM holds.
const quiescedLabel = "containerd.io/snapshot/erofs.quiesced"

// waitForOutput polls path until it contains want, or fails after the deadline.
func waitForOutput(t *testing.T, path, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), want) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %s", want, path)
}

// TestContainerdHotCommitProducesImage validates the full hot-commit chain: a
// RUNNING container is paused (the shim freezes the guest filesystems and stops
// the vCPUs), the active snapshot is diffed with the quiesced label so the
// snapshotter reads the frozen rwlayer without the exclusive image lock, a new
// image is built from that layer, and the container resumes. This is the path
// the OFD-lock gate (TestContainerdCommitGateRunningFails) rejects WITHOUT the
// quiesce - here it must succeed because the VM is paused and frozen.
func TestContainerdHotCommitProducesImage(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	const markerPath = "/spinbox-hot-marker"
	stamp := strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	markerContent := "hot-" + stamp

	name := "qbx-hotcommit-" + stamp
	snapshotKey := name + "-snapshot"
	newImageRef := "spinbox.test/hotcommitted:" + name

	stdoutPath := filepath.Join(t.TempDir(), "stdout.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer stdoutFile.Close()

	// Write the marker into the overlay upper, flush it (so the freeze captures
	// it on-disk), signal readiness, then stay alive so the commit happens
	// while the container is running.
	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithProcessArgs("/bin/sh", "-c",
				"echo "+markerContent+" > "+markerPath+"; sync; echo READY; sleep 300"),
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

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdoutFile, nil)))
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

	// The marker is on-disk once the guest prints READY (after sync).
	waitForOutput(t, stdoutPath, "READY", 60*time.Second)

	// Pause: freeze the guest filesystems and stop the vCPUs. The VM keeps the
	// rwlayer image lock held while paused.
	if err := task.Pause(ctx); err != nil {
		t.Fatalf("pause task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Paused)

	ss := client.SnapshotService(cfg.Snapshotter)
	cs := client.ContentStore()

	// Diff the ACTIVE snapshot while paused, with the quiesced label. Without
	// it this fails with "rwlayer.img is locked by the VM"; with it the
	// snapshotter reads the frozen rwlayer read-only.
	layerDesc, err := rootfs.CreateDiff(ctx, snapshotKey, ss, client.DiffService(),
		diff.WithLabels(map[string]string{quiescedLabel: "true"}))
	if err != nil {
		t.Fatalf("hot diff of paused container must succeed with quiesced label: %v", err)
	}

	diffID, hasMarker, err := layerDiffIDAndHasFile(ctx, cs, layerDesc, markerPath, markerContent)
	if err != nil {
		t.Fatalf("inspect diff layer: %v", err)
	}
	if !hasMarker {
		t.Fatalf("hot-committed layer does not contain %s=%q", markerPath, markerContent)
	}

	// Resume: the container must keep running after the commit.
	if err := task.Resume(ctx); err != nil {
		t.Fatalf("resume task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Running)

	// Build a new image: parent config/manifest + the hot-committed layer.
	platform := platforms.Default()
	parentManifest, err := images.Manifest(ctx, cs, image.Target(), platform)
	if err != nil {
		t.Fatalf("read parent manifest: %v", err)
	}
	parentConfigBytes, err := readBlob(ctx, cs, parentManifest.Config)
	if err != nil {
		t.Fatalf("read parent config: %v", err)
	}
	var imgConfig ocispec.Image
	if err := json.Unmarshal(parentConfigBytes, &imgConfig); err != nil {
		t.Fatalf("unmarshal parent config: %v", err)
	}
	imgConfig.RootFS.DiffIDs = append(imgConfig.RootFS.DiffIDs, diffID)

	newImageRef = writeCommitImage(t, ctx, cs, client.ImageService(), newImageRef, imgConfig, parentManifest, layerDesc)
	defer func() {
		if err := client.ImageService().Delete(ctx, newImageRef); err != nil {
			t.Logf("cleanup image %s: %v", newImageRef, err)
		}
	}()

	img, err := client.ImageService().Get(ctx, newImageRef)
	if err != nil {
		t.Fatalf("get new image %s: %v", newImageRef, err)
	}
	gotManifest, err := images.Manifest(ctx, cs, img.Target, platform)
	if err != nil {
		t.Fatalf("read new manifest: %v", err)
	}
	if len(gotManifest.Layers) != len(parentManifest.Layers)+1 {
		t.Fatalf("expected %d layers, got %d", len(parentManifest.Layers)+1, len(gotManifest.Layers))
	}
	if top := gotManifest.Layers[len(gotManifest.Layers)-1]; top.Digest != layerDesc.Digest {
		t.Fatalf("top layer = %s, want hot-committed layer %s", top.Digest, layerDesc.Digest)
	}

	// Tear down the still-running container.
	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatalf("kill task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for task %s to exit after kill", name)
	}

	t.Logf("hot-committed running container into %s (layer %s contains %s)", newImageRef, layerDesc.Digest, markerPath)
}
