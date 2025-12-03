package task

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/errdefs"

	"github.com/aledbf/beacon/containerd/mountutil"
	"github.com/aledbf/beacon/containerd/vm/cloudhypervisor"
)

func setupMounts(ctx context.Context, vmi *cloudhypervisor.Instance, id string, m []*types.Mount, rootfs, lmounts string) ([]*types.Mount, error) {
	// Handle mounts

	// Cloud Hypervisor doesn't support AddFS (virtiofs requires shared memory + virtiofsd)
	// Skip filesystem mounts for Cloud Hypervisor - rely on EROFS snapshotter instead
	vmInfo := vmi.VMInfo()
	if vmInfo.Type == "cloudhypervisor" {
		// For Cloud Hypervisor, only process mounts through transformMounts
		// which handles EROFS and other disk-based mounts
		if len(m) == 0 {
			// No mounts specified, return empty - EROFS snapshotter provides rootfs
			return nil, nil
		}
		// Let transformMounts handle all mounts for Cloud Hypervisor
		return transformMounts(ctx, vmi, id, m)
	}

	if len(m) == 1 && (m[0].Type == "overlay" || m[0].Type == "bind") {
		tag := fmt.Sprintf("rootfs-%s", id)
		// virtiofs implementation has a limit of 36 characters for the tag
		if len(tag) > 36 {
			tag = tag[:36]
		}
		mnt := mount.Mount{
			Type:    m[0].Type,
			Source:  m[0].Source,
			Options: m[0].Options,
		}
		if err := mnt.Mount(rootfs); err != nil {
			return nil, err
		}
		if err := vmi.AddFS(ctx, tag, rootfs); err != nil {
			return nil, err
		}
		return []*types.Mount{{
			Type:   "virtiofs",
			Source: tag,
			// TODO: Translate the options
			//Options: m[0].Options,
		}}, nil
	} else if len(m) == 0 {
		tag := fmt.Sprintf("rootfs-%s", id)
		// virtiofs implementation has a limit of 36 characters for the tag
		if len(tag) > 36 {
			tag = tag[:36]
		}
		if err := vmi.AddFS(ctx, tag, rootfs); err != nil {
			return nil, err
		}
		return []*types.Mount{{
			Type:   "virtiofs",
			Source: tag,
		}}, nil
	}
	mounts, err := transformMounts(ctx, vmi, id, m)
	if err != nil && errdefs.IsNotImplemented(err) {
		if err := mountutil.All(ctx, rootfs, lmounts, m); err != nil {
			return nil, err
		}

		// Fallback to original rootfs mount
		tag := fmt.Sprintf("rootfs-%s", id)
		// virtiofs implementation has a limit of 36 characters for the tag
		if len(tag) > 36 {
			tag = tag[:36]
		}
		if err := vmi.AddFS(ctx, tag, rootfs); err != nil {
			return nil, err
		}
		return []*types.Mount{{
			Type:   "virtiofs",
			Source: tag,
		}}, nil
	}
	return mounts, err
}
