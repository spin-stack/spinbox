// Command spinbox-commit commits a container's root filesystem into a new OCI
// image using containerd primitives directly, without nerdctl. It has two
// modes:
//
// Container mode (--container ID): commits a (running or stopped) container.
// When the container is running it performs a HOT commit - it pauses the task
// (spinbox freezes the guest filesystems and stops the vCPUs, so the on-disk
// rwlayer is consistent), diffs the frozen snapshot with the quiesced label so
// the snapshotter reads it without the exclusive image lock a paused VM still
// holds, builds the image, and resumes. A stopped container is committed cold.
// The snapshot key, parent image and snapshotter are read from the container
// record (the snapshot key is the container id for `ctr run <image> <id>`, but
// this works regardless of how it was chosen).
//
// Snapshot mode (--snapshot KEY --source-image REF): a lower-level cold path
// that builds an image directly from an already-committed snapshot, with no
// container involved (e.g. after `ctr snapshots commit`).
//
// Both modes run under a lease and produce a new config (parent diff_ids + the
// new layer) and manifest with GC labels.
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

// options holds the parsed CLI configuration for one of the two modes.
type options struct {
	address     string
	namespace   string
	imageRef    string
	container   string // mode A: derive snapshot/image/snapshotter from a container, quiesce if running
	snapshot    string // mode B: build directly from a committed snapshot key (cold)
	sourceImage string // mode B: parent image ref
	snapshotter string // mode B: snapshotter name
}

func main() {
	var opts options
	flag.StringVar(&opts.address, "address", "/var/run/spin-stack/containerd.sock", "containerd socket")
	flag.StringVar(&opts.namespace, "namespace", namespaces.Default, "containerd namespace")
	flag.StringVar(&opts.imageRef, "image", "", "new image reference to create")
	flag.StringVar(&opts.container, "container", "", "mode A: container id to commit (hot if running, derives source image and snapshotter)")
	flag.StringVar(&opts.snapshot, "snapshot", "", "mode B: committed snapshot key to build an image from (cold)")
	flag.StringVar(&opts.sourceImage, "source-image", "", "mode B: parent image ref (required with --snapshot)")
	flag.StringVar(&opts.snapshotter, "snapshotter", "spin-erofs", "mode B: snapshotter name")
	flag.Parse()

	if err := validateOptions(opts); err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, "usage:")
		fmt.Fprintln(os.Stderr, "  spinbox-commit --container ID --image REF                        # commit a (running or stopped) container")
		fmt.Fprintln(os.Stderr, "  spinbox-commit --snapshot KEY --source-image REF --image REF     # build an image from a committed snapshot")
		os.Exit(2)
	}

	if err := run(opts); err != nil {
		fmt.Fprintf(os.Stderr, "spinbox-commit failed: %v\n", err)
		os.Exit(1)
	}
}

// validateOptions enforces that exactly one mode is selected and its required
// flags are present.
func validateOptions(o options) error {
	if o.imageRef == "" {
		return fmt.Errorf("--image is required")
	}
	switch {
	case o.container != "" && o.snapshot != "":
		return fmt.Errorf("--container and --snapshot are mutually exclusive")
	case o.container == "" && o.snapshot == "":
		return fmt.Errorf("one of --container or --snapshot is required")
	case o.snapshot != "" && o.sourceImage == "":
		return fmt.Errorf("--source-image is required with --snapshot")
	}
	return nil
}

func run(o options) (retErr error) {
	client, err := containerd.New(o.address)
	if err != nil {
		return fmt.Errorf("connect containerd: %w", err)
	}
	defer client.Close()

	ctx := namespaces.WithNamespace(context.Background(), o.namespace)

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

	// Resolve the commit source (snapshot key, parent image, snapshotter) and,
	// in container mode, quiesce a running container so its frozen rwlayer can
	// be read.
	snapshotKey, sourceImage, snapshotterName := o.snapshot, o.sourceImage, o.snapshotter
	var quiesced quiesceState
	if o.container != "" {
		cont, err := client.LoadContainer(ctx, o.container)
		if err != nil {
			return fmt.Errorf("load container %s: %w", o.container, err)
		}
		info, err := cont.Info(ctx)
		if err != nil {
			return fmt.Errorf("container info: %w", err)
		}
		if info.SnapshotKey == "" {
			return fmt.Errorf("container %s has no rootfs snapshot", o.container)
		}
		if info.Image == "" {
			return fmt.Errorf("container %s has no source image recorded", o.container)
		}
		snapshotKey, sourceImage, snapshotterName = info.SnapshotKey, info.Image, info.Snapshotter

		// If the container is running, pause it so the guest freezes its
		// filesystems and the snapshot is safe to read while held; resume on exit.
		quiesced, err = quiesceContainer(ctx, cont)
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
	}

	srcImage, err := client.GetImage(ctx, sourceImage)
	if err != nil {
		return fmt.Errorf("get source image %s: %w", sourceImage, err)
	}

	ss := client.SnapshotService(snapshotterName)
	cs := client.ContentStore()

	var diffOpts []diff.Opt
	if quiesced.frozen {
		diffOpts = append(diffOpts, diff.WithLabels(map[string]string{quiescedLabel: "true"}))
	}

	layerDesc, err := rootfs.CreateDiff(ctx, snapshotKey, ss, client.DiffService(), diffOpts...)
	if err != nil {
		return fmt.Errorf("create diff from snapshot %s: %w", snapshotKey, err)
	}

	diffID, err := layerDiffID(ctx, cs, layerDesc)
	if err != nil {
		return err
	}

	manifestDesc, err := buildImage(ctx, cs, client.ImageService(), o.imageRef, srcImage.Target(), layerDesc, diffID, sourceImage)
	if err != nil {
		return err
	}

	mode := "cold"
	if quiesced.frozen {
		mode = "hot"
	}
	fmt.Printf("committed snapshot %s (%s) -> image %s\n", snapshotKey, mode, o.imageRef)
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
