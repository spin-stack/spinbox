// Command spinbox-commit commits a container's current root filesystem into a
// new image, without nerdctl. It performs a HOT commit when the container is
// running: it pauses the task (spinbox freezes the guest filesystems and stops
// the vCPUs, so the on-disk rwlayer is consistent), diffs the frozen snapshot
// with the quiesced label so the snapshotter reads it without the exclusive
// image lock a paused VM still holds, builds a new OCI image, and resumes.
//
// A stopped container is committed cold (no pause, no quiesced label - the VM
// is gone and the lock is free).
//
// The active snapshot key for a container started by `ctr run <image> <id>` is
// the container id; this tool reads it from the container record so it works
// regardless of how the snapshot key was chosen.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/diff"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/rootfs"
	"github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	digest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// quiescedLabel tells the spin-erofs snapshotter that the container's writable
// layer is quiesced (VM paused + filesystems frozen), so the diff may read the
// rwlayer without the exclusive image lock. Must match the snapshotter's
// constant.
const quiescedLabel = "containerd.io/snapshot/erofs.quiesced"

// uncompressedLabel carries the diff ID (digest of the uncompressed tar) that
// the differ records on the layer blob.
const uncompressedLabel = "containerd.io/uncompressed"

func main() {
	var (
		address   = flag.String("address", "/var/run/spin-stack/containerd.sock", "containerd socket")
		namespace = flag.String("namespace", namespaces.Default, "containerd namespace")
		container = flag.String("container", "", "container id to commit")
		imageRef  = flag.String("image", "", "new image reference to create")
	)
	flag.Parse()

	if *container == "" || *imageRef == "" {
		fmt.Fprintln(os.Stderr, "usage: spinbox-commit --container ID --image REF [--address SOCK] [--namespace NS]")
		os.Exit(2)
	}

	if err := run(*address, *namespace, *container, *imageRef); err != nil {
		fmt.Fprintf(os.Stderr, "spinbox-commit failed: %v\n", err)
		os.Exit(1)
	}
}

func run(address, namespace, containerID, imageRef string) (retErr error) {
	client, err := containerd.New(address)
	if err != nil {
		return fmt.Errorf("connect containerd: %w", err)
	}
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), namespace)

	// A lease keeps the new config/manifest blobs and the diff content alive
	// until images.Create references them; otherwise a concurrent GC sweep
	// could delete them mid-commit.
	ctx, leaseDone, err := client.WithLease(ctx)
	if err != nil {
		return fmt.Errorf("create lease: %w", err)
	}
	defer func() {
		if derr := leaseDone(context.WithoutCancel(ctx)); derr != nil {
			fmt.Fprintf(os.Stderr, "warning: release lease: %v\n", derr)
		}
	}()

	cont, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		return fmt.Errorf("load container %s: %w", containerID, err)
	}
	info, err := cont.Info(ctx)
	if err != nil {
		return fmt.Errorf("container info: %w", err)
	}
	if info.SnapshotKey == "" {
		return fmt.Errorf("container %s has no rootfs snapshot", containerID)
	}
	if info.Image == "" {
		return fmt.Errorf("container %s has no source image recorded", containerID)
	}
	snapshotterName := info.Snapshotter

	// Quiesce: if the container is running, pause it so the guest freezes its
	// filesystems and the snapshot is safe to read while held. Resume on exit.
	quiesced, err := quiesceContainer(ctx, cont)
	if err != nil {
		return err
	}
	defer func() {
		if quiesced.resume != nil {
			if rerr := quiesced.resume(); rerr != nil {
				fmt.Fprintf(os.Stderr, "warning: resume container: %v\n", rerr)
				if retErr == nil {
					retErr = fmt.Errorf("resume container after commit: %w", rerr)
				}
			}
		}
	}()

	srcImage, err := client.GetImage(ctx, info.Image)
	if err != nil {
		return fmt.Errorf("get source image %s: %w", info.Image, err)
	}

	ss := client.SnapshotService(snapshotterName)
	cs := client.ContentStore()

	var diffOpts []diff.Opt
	if quiesced.frozen {
		diffOpts = append(diffOpts, diff.WithLabels(map[string]string{quiescedLabel: "true"}))
	}

	layerDesc, err := rootfs.CreateDiff(ctx, info.SnapshotKey, ss, client.DiffService(), diffOpts...)
	if err != nil {
		return fmt.Errorf("create diff from snapshot %s: %w", info.SnapshotKey, err)
	}

	diffID, err := layerDiffID(ctx, cs, layerDesc)
	if err != nil {
		return err
	}

	manifestDesc, err := buildImage(ctx, cs, client.ImageService(), imageRef, srcImage.Target(), layerDesc, diffID, info.Image)
	if err != nil {
		return err
	}

	mode := "cold"
	if quiesced.frozen {
		mode = "hot"
	}
	fmt.Printf("committed container %s (%s) -> image %s\n", containerID, mode, imageRef)
	fmt.Printf("  manifest %s\n", manifestDesc.Digest)
	fmt.Printf("  layer    %s (diffID %s)\n", layerDesc.Digest, diffID)
	return nil
}

// quiesceState records whether the commit is reading a frozen snapshot and how
// to resume the container afterwards.
type quiesceState struct {
	frozen bool
	resume func() error
}

// quiesceContainer pauses a running container so the guest freezes its
// filesystems (spinbox does FIFREEZE before the QMP stop), making the rwlayer
// safe to read while the VM still holds it. A stopped container needs no
// quiesce. A container with no task is treated as stopped.
func quiesceContainer(ctx context.Context, cont containerd.Container) (quiesceState, error) {
	task, err := cont.Task(ctx, nil)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return quiesceState{}, nil // no running task: cold commit
		}
		return quiesceState{}, fmt.Errorf("load task: %w", err)
	}

	status, err := task.Status(ctx)
	if err != nil {
		return quiesceState{}, fmt.Errorf("task status: %w", err)
	}

	switch status.Status {
	case containerd.Running:
		if err := task.Pause(ctx); err != nil {
			return quiesceState{}, fmt.Errorf("pause container (freeze guest): %w", err)
		}
		return quiesceState{
			frozen: true,
			resume: func() error { return task.Resume(context.WithoutCancel(ctx)) },
		}, nil
	case containerd.Paused:
		// Already paused by spinbox means already frozen; read it quiesced but
		// leave it paused (we did not pause it, so we must not resume it).
		return quiesceState{frozen: true}, nil
	default:
		// created/stopped/unknown: the VM is not actively writing.
		return quiesceState{}, nil
	}
}

// layerDiffID returns the layer's diff ID. The differ records it on the blob
// as the containerd.io/uncompressed label (for any media type), so read it
// from there rather than re-decompressing.
func layerDiffID(ctx context.Context, cs content.Store, layer ocispec.Descriptor) (digest.Digest, error) {
	info, err := cs.Info(ctx, layer.Digest)
	if err != nil {
		return "", fmt.Errorf("layer blob info: %w", err)
	}
	d, err := digest.Parse(info.Labels[uncompressedLabel])
	if err != nil {
		return "", fmt.Errorf("layer %s missing %s label: %w", layer.Digest, uncompressedLabel, err)
	}
	return d, nil
}

// buildImage writes a new config (parent diff_ids + the new layer) and manifest
// referencing the parent layers plus the new one, with GC labels so the
// content store keeps them alive, then registers the image.
func buildImage(ctx context.Context, cs content.Store, is images.Store, ref string, srcTarget, layerDesc ocispec.Descriptor, diffID digest.Digest, sourceImage string) (ocispec.Descriptor, error) {
	platform := platforms.Default()
	parentManifest, err := images.Manifest(ctx, cs, srcTarget, platform)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("read parent manifest: %w", err)
	}

	configBytes, err := content.ReadBlob(ctx, cs, parentManifest.Config)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("read parent config: %w", err)
	}
	var cfg ocispec.Image
	if err := json.Unmarshal(configBytes, &cfg); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("unmarshal parent config: %w", err)
	}
	now := time.Now().UTC()
	cfg.Created = &now
	cfg.RootFS.DiffIDs = append(cfg.RootFS.DiffIDs, diffID)
	cfg.History = append(cfg.History, ocispec.History{
		Created:   &now,
		CreatedBy: "spinbox-commit",
		Comment:   "committed from " + sourceImage,
	})

	newConfigBytes, err := json.Marshal(cfg)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("marshal config: %w", err)
	}
	configDesc := ocispec.Descriptor{
		MediaType: parentManifest.Config.MediaType,
		Digest:    digest.FromBytes(newConfigBytes),
		Size:      int64(len(newConfigBytes)),
	}
	if err := content.WriteBlob(ctx, cs, "spinbox-commit-config-"+configDesc.Digest.Encoded(),
		bytes.NewReader(newConfigBytes), configDesc); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("write config: %w", err)
	}

	manifest := ocispec.Manifest{
		Versioned: parentManifest.Versioned,
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    append(append([]ocispec.Descriptor{}, parentManifest.Layers...), layerDesc),
	}
	if manifest.SchemaVersion == 0 {
		manifest.SchemaVersion = 2
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("marshal manifest: %w", err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    digest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}

	// GC labels: the manifest keeps its config and every layer reachable.
	labels := map[string]string{
		"containerd.io/gc.ref.content.config": configDesc.Digest.String(),
	}
	for i, l := range manifest.Layers {
		labels[fmt.Sprintf("containerd.io/gc.ref.content.l.%d", i)] = l.Digest.String()
	}
	if err := content.WriteBlob(ctx, cs, "spinbox-commit-manifest-"+manifestDesc.Digest.Encoded(),
		bytes.NewReader(manifestBytes), manifestDesc, content.WithLabels(labels)); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("write manifest: %w", err)
	}

	img := images.Image{Name: ref, Target: manifestDesc}
	if _, err := is.Create(ctx, img); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return ocispec.Descriptor{}, fmt.Errorf("create image %s: %w", ref, err)
		}
		if _, err := is.Update(ctx, img); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("update image %s: %w", ref, err)
		}
	}
	return manifestDesc, nil
}
