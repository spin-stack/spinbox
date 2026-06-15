//go:build linux && integration

package integration

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/containerd/v2/pkg/rootfs"
	"github.com/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// readBlob reads a content-store blob fully into memory.
func readBlob(ctx context.Context, cs content.Provider, desc ocispec.Descriptor) ([]byte, error) {
	ra, err := cs.ReaderAt(ctx, desc)
	if err != nil {
		return nil, err
	}
	defer ra.Close()
	return io.ReadAll(io.NewSectionReader(ra, 0, ra.Size()))
}

// layerDiffIDAndHasFile gunzips a layer blob, computes its diffID (sha256 of the
// uncompressed tar) and reports whether it contains wantPath with wantContent.
func layerDiffIDAndHasFile(ctx context.Context, cs content.Provider, layer ocispec.Descriptor, wantPath, wantContent string) (digest.Digest, bool, error) {
	gzData, err := readBlob(ctx, cs, layer)
	if err != nil {
		return "", false, err
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return "", false, err
	}
	uncompressed, err := io.ReadAll(gr)
	if err != nil {
		return "", false, err
	}
	diffID := digest.FromBytes(uncompressed)

	found := false
	tr := tar.NewReader(bytes.NewReader(uncompressed))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", false, err
		}
		if strings.TrimPrefix(hdr.Name, "./") == strings.TrimPrefix(wantPath, "/") {
			body, _ := io.ReadAll(tr)
			if strings.TrimSpace(string(body)) == wantContent {
				found = true
			}
		}
	}
	return diffID, found, nil
}

// TestContainerdColdCommitProducesImage verifies the cold-commit path end to
// end: a container writes a marker and exits; once the VM is gone (lock
// released, rwlayer synced by PrepareShutdown) the snapshot Commit succeeds (the
// OFD gate no longer applies), and building an image from the diff produces a
// new image whose top layer contains the marker change.
func TestContainerdColdCommitProducesImage(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	const markerPath = "/spinbox-commit-marker"
	markerContent := "committed-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")

	name := "qbx-coldcommit-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")
	snapshotKey := name + "-snapshot"
	committedSnap := name + "-committed"
	newImageRef := "spinbox.test/committed:" + name

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(snapshotKey, image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithProcessArgs("/bin/sh", "-c", "echo "+markerContent+" > "+markerPath+"; sync"),
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

	// Run the writer to completion.
	task, err := container.NewTask(ctx, cio.NullIO)
	if err != nil {
		t.Fatalf("create task for %s: %v", name, err)
	}
	exitCh, err := task.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for task %s: %v", name, err)
	}
	if err := task.Start(ctx); err != nil {
		t.Fatalf("start task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for writer task %s", name)
	}

	// Tear down the task so QEMU exits and releases the rwlayer.img lock; the
	// shim's PrepareShutdown syncs the writable layer on poweroff.
	if _, err := task.Delete(ctx); err != nil {
		t.Fatalf("delete task %s: %v", name, err)
	}

	ss := client.SnapshotService(cfg.Snapshotter)

	// Cold commit now succeeds (the VM is gone, so the OFD gate is open).
	if err := ss.Commit(ctx, committedSnap, snapshotKey); err != nil {
		t.Fatalf("cold commit of stopped container must succeed: %v", err)
	}
	defer func() {
		if err := ss.Remove(ctx, committedSnap); err != nil {
			t.Logf("cleanup committed snapshot %s: %v", committedSnap, err)
		}
	}()

	// Create the diff layer for the committed snapshot.
	layerDesc, err := rootfs.CreateDiff(ctx, committedSnap, ss, client.DiffService())
	if err != nil {
		t.Fatalf("create diff for %s: %v", committedSnap, err)
	}

	cs := client.ContentStore()

	// The new layer must contain the marker change.
	diffID, hasMarker, err := layerDiffIDAndHasFile(ctx, cs, layerDesc, markerPath, markerContent)
	if err != nil {
		t.Fatalf("inspect diff layer: %v", err)
	}
	if !hasMarker {
		t.Fatalf("committed layer does not contain %s=%q", markerPath, markerContent)
	}

	// Build a new image: parent config/manifest + the new layer.
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

	// Verify the new image exists and its top layer is the committed change.
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
	top := gotManifest.Layers[len(gotManifest.Layers)-1]
	if top.Digest != layerDesc.Digest {
		t.Fatalf("top layer = %s, want committed layer %s", top.Digest, layerDesc.Digest)
	}
	t.Logf("committed image %s top layer %s contains %s", newImageRef, top.Digest, markerPath)
}

// writeCommitImage writes the new config + manifest blobs and registers the
// image, returning its ref.
func writeCommitImage(t *testing.T, ctx context.Context, cs content.Store, is images.Store, ref string, cfg ocispec.Image, parent ocispec.Manifest, layer ocispec.Descriptor) string {
	t.Helper()

	configBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	configDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    digest.FromBytes(configBytes),
		Size:      int64(len(configBytes)),
	}
	if err := content.WriteBlob(ctx, cs, "config-"+configDesc.Digest.String(), bytes.NewReader(configBytes), configDesc); err != nil {
		t.Fatalf("write config blob: %v", err)
	}

	manifest := ocispec.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    append(append([]ocispec.Descriptor{}, parent.Layers...), layer),
	}
	manifest.SchemaVersion = 2
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := content.WriteBlob(ctx, cs, "manifest-"+manifestDesc.Digest.String(), bytes.NewReader(manifestBytes), manifestDesc); err != nil {
		t.Fatalf("write manifest blob: %v", err)
	}

	if _, err := is.Create(ctx, images.Image{Name: ref, Target: manifestDesc}); err != nil {
		t.Fatalf("create image %s: %v", ref, err)
	}
	return ref
}
