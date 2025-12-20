//go:build linux

package task

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/api/types"
	"github.com/containerd/errdefs"
	"github.com/containerd/log"

	"github.com/aledbf/beacon/containerd/erofs"
	"github.com/aledbf/beacon/containerd/vm"
)

type diskOptions struct {
	name     string
	source   string
	readOnly bool
	vmdk     bool
}

// transformMounts does not perform any local mounts but transforms
// the mounts to be used inside the VM via virtio.
func transformMounts(ctx context.Context, vmi vm.Instance, id string, ms []*types.Mount) ([]*types.Mount, error) {
	disks := byte('a')
	var (
		addDisks []diskOptions
		am       []*types.Mount
	)

	for _, m := range ms {
		mounts, disksToAdd, err := transformMount(ctx, id, &disks, m)
		if err != nil {
			return nil, err
		}
		am = append(am, mounts...)
		addDisks = append(addDisks, disksToAdd...)
	}

	if len(addDisks) > 10 {
		return nil, fmt.Errorf("exceeded maximum virtio disk count: %d > 10: %w", len(addDisks), errdefs.ErrNotImplemented)
	}

	if err := addDisksToVM(ctx, vmi, addDisks); err != nil {
		return nil, err
	}

	return am, nil
}

func transformMount(ctx context.Context, id string, disks *byte, m *types.Mount) ([]*types.Mount, []diskOptions, error) {
	switch m.Type {
	case "erofs":
		return handleEROFS(ctx, id, disks, m)
	case "ext4":
		return handleExt4(id, disks, m)
	case "overlay", "format/overlay", "format/mkdir/overlay":
		if err := validateOverlay(ctx, m); err != nil {
			return nil, nil, err
		}
		return []*types.Mount{m}, nil, nil
	default:
		return []*types.Mount{m}, nil, nil
	}
}

func handleEROFS(ctx context.Context, id string, disks *byte, m *types.Mount) ([]*types.Mount, []diskOptions, error) {
	disk := fmt.Sprintf("disk-%d-%s", *disks, id)
	// virtiofs implementation has a limit of 36 characters for the tag
	if len(disk) > 36 {
		disk = disk[:36]
	}

	var options []string
	devices := []string{m.Source}

	// Extract device= options which specify additional EROFS layers
	for _, o := range m.Options {
		if d, found := strings.CutPrefix(o, "device="); found {
			devices = append(devices, d)
			continue
		}
		options = append(options, o)
	}

	addDisks := []diskOptions{{
		name:     disk,
		source:   m.Source,
		readOnly: true,
		vmdk:     false,
	}}

	// If multiple layers, generate VMDK descriptor to merge them
	if len(devices) > 1 {
		mergedfsPath := filepath.Dir(m.Source) + "/merged_fs.vmdk"
		if _, err := os.Stat(mergedfsPath); err != nil {
			if !os.IsNotExist(err) {
				log.G(ctx).WithError(err).Warnf("failed to stat %v", mergedfsPath)
				return nil, nil, errdefs.ErrNotImplemented
			}
			if err := erofs.DumpVMDKDescriptorToFile(mergedfsPath, 0xfffffffe, devices); err != nil {
				log.G(ctx).WithError(err).Warnf("failed to generate %v", mergedfsPath)
				return nil, nil, errdefs.ErrNotImplemented
			}
		}
		addDisks[0].source = mergedfsPath
		addDisks[0].vmdk = true
	}

	out := &types.Mount{
		Type:    "erofs",
		Source:  fmt.Sprintf("/dev/vd%c", *disks),
		Target:  m.Target,
		Options: filterOptions(options),
	}
	*disks++
	return []*types.Mount{out}, addDisks, nil
}

func handleExt4(id string, disks *byte, m *types.Mount) ([]*types.Mount, []diskOptions, error) {
	disk := fmt.Sprintf("disk-%d-%s", *disks, id)
	// virtiofs implementation has a limit of 36 characters for the tag
	if len(disk) > 36 {
		disk = disk[:36]
	}
	// Check if mount should be read-only
	readOnly := false
	for _, opt := range m.Options {
		if opt == "ro" || opt == "readonly" {
			readOnly = true
			break
		}
	}
	m.Options = filterOptions(m.Options)
	out := &types.Mount{
		Type:    "ext4",
		Source:  fmt.Sprintf("/dev/vd%c", *disks),
		Target:  m.Target,
		Options: m.Options,
	}
	*disks++

	addDisks := []diskOptions{{
		name:     disk,
		source:   m.Source,
		readOnly: readOnly,
		vmdk:     false,
	}}
	return []*types.Mount{out}, addDisks, nil
}

func validateOverlay(ctx context.Context, m *types.Mount) error {
	var (
		wdi = -1
		udi = -1
	)
	for i, opt := range m.Options {
		if strings.HasPrefix(opt, "upperdir=") {
			udi = i
		} else if strings.HasPrefix(opt, "workdir=") {
			wdi = i
		}
		// Note: virtiofs for lowerdir is not handled here.
	}
	if wdi > -1 && udi > -1 {
		//
		// If any upperdir or workdir isn't transformed, they both
		// should fall back to virtiofs passthroughfs.  But...
		//
		if !strings.Contains(m.Options[wdi], "{{") ||
			!strings.Contains(m.Options[udi], "{{") {
			// Having the upper as virtiofs may return invalid argument, avoid
			// transforming and attempt to perform the mounts on the host if
			// supported.
			return fmt.Errorf("cannot use virtiofs for upper dir in overlay: %w", errdefs.ErrNotImplemented)
		}
	} else {
		log.G(ctx).WithField("options", m.Options).Warnf("overlayfs missing workdir or upperdir")
	}
	return nil
}

func addDisksToVM(ctx context.Context, vmi vm.Instance, disks []diskOptions) error {
	for _, do := range disks {
		var opts []vm.MountOpt
		if do.readOnly {
			opts = append(opts, vm.WithReadOnly())
		}
		if do.vmdk {
			opts = append(opts, vm.WithVmdk())
		}
		if err := vmi.AddDisk(ctx, do.name, do.source, opts...); err != nil {
			return err
		}
	}
	return nil
}

func filterOptions(options []string) []string {
	var filtered []string
	for _, o := range options {
		switch o {
		case "loop":
		default:
			filtered = append(filtered, o)
		}
	}
	return filtered
}
