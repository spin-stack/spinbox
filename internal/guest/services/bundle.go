// Package services implements containerd TTRPC services for the qemubox VM runtime.
//
// These services run inside the guest VM (vminitd) and provide:
//   - Bundle management: creating OCI bundle directories
//   - System management: CPU/memory hotplug operations
//
// The services communicate with the host shim via TTRPC over vsock.
package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/ttrpc"

	api "github.com/aledbf/qemubox/containerd/api/services/bundle/v1"
	"github.com/aledbf/qemubox/containerd/internal/guest/vminit/bundle"
)

const (
	rootfsDir       = "rootfs"
	bundleDirPerms  = 0750 // rwxr-x---: owner + group readable
	bundleFilePerms = 0600 // rw-------: owner only
)

func init() {
	registry.Register(&plugin.Registration{
		Type: cplugins.TTRPCPlugin,
		ID:   "bundle",
		Requires: []plugin.Type{
			cplugins.EventPlugin,
			cplugins.InternalPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			if err := os.MkdirAll(bundle.RootDir, bundleDirPerms); err != nil {
				return nil, err
			}
			return &service{
				bundleRoot: bundle.RootDir,
			}, nil
		},
	})
}

// service implements the bundle creation API for vminitd.
// It manages OCI bundle directories on the guest VM filesystem.
type service struct {
	bundleRoot string // root directory for all bundles
}

func (s *service) RegisterTTRPC(server *ttrpc.Server) error {
	api.RegisterTTRPCBundleService(server, s)
	return nil
}

func (s *service) Create(ctx context.Context, r *api.CreateRequest) (_ *api.CreateResponse, retErr error) {
	// Validate bundle ID
	if r.ID == "" {
		return nil, errgrpc.ToGRPCf(errdefs.ErrInvalidArgument, "bundle ID cannot be empty")
	}
	if strings.Contains(r.ID, "..") || strings.Contains(r.ID, "/") {
		return nil, errgrpc.ToGRPCf(errdefs.ErrInvalidArgument, "invalid bundle ID: %q", r.ID)
	}

	// Validate file paths to prevent directory traversal
	for filename := range r.Files {
		if filepath.IsAbs(filename) || strings.Contains(filename, "..") {
			return nil, errgrpc.ToGRPCf(errdefs.ErrInvalidArgument,
				"invalid file path in bundle: %q", filename)
		}
	}

	d := filepath.Join(s.bundleRoot, r.ID)
	if err := os.Mkdir(d, bundleDirPerms); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	// Cleanup bundle directory on any error
	defer func() {
		if retErr != nil {
			_ = os.RemoveAll(d)
		}
	}()

	log.G(ctx).Infof("Creating bundle at %s", d)

	if err := os.Mkdir(filepath.Join(d, rootfsDir), bundleDirPerms); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	for f, b := range r.Files {
		if err := os.WriteFile(filepath.Join(d, f), b, bundleFilePerms); err != nil {
			return nil, errgrpc.ToGRPC(err)
		}
	}

	return &api.CreateResponse{
		Bundle: d,
	}, nil
}
