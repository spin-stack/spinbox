package services

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/errdefs"
	"github.com/containerd/errdefs/pkg/errgrpc"
	"github.com/containerd/log"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/ttrpc"
	emptypb "google.golang.org/protobuf/types/known/emptypb"

	api "github.com/aledbf/qemubox/containerd/api/services/system/v1"
)

const (
	// Sysfs file values
	sysfsOnline  = "1"
	sysfsOffline = "0"

	// Retry configuration for sysfs operations
	sysfsRetryMax        = 10
	sysfsRetryBaseDelay  = 10 * time.Millisecond
	autoOnlineCheckDelay = 100 * time.Millisecond

	// File permissions
	sysfsFilePerms    = 0600
	featuresDirPerms  = 0750
	featuresFilePerms = 0600
)

type systemService struct{}

var _ api.TTRPCSystemService = &systemService{}

func init() {
	registry.Register(&plugin.Registration{
		Type:   cplugins.TTRPCPlugin,
		ID:     "system",
		InitFn: initFunc,
	})
}

func initFunc(ic *plugin.InitContext) (interface{}, error) {
	s := &systemService{}
	// Write runtime features to a file for the shim manager to read
	if err := s.writeRuntimeFeatures(); err != nil {
		// Non-fatal - log but continue
		log.G(ic.Context).WithError(err).Warn("failed to write runtime features")
	}
	return s, nil
}

func (s *systemService) RegisterTTRPC(server *ttrpc.Server) error {
	api.RegisterTTRPCSystemService(server, s)
	return nil
}

// readSysfsValue reads and trims a value from a sysfs file.
func readSysfsValue(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// writeSysfsValue writes a value to a sysfs file.
func writeSysfsValue(path, value string) error {
	return os.WriteFile(path, []byte(value), sysfsFilePerms)
}

// retrySysfsOperation retries a sysfs operation with exponential backoff.
// It calls checkFn repeatedly until it returns nil or a non-retryable error.
// The checkFn should return os.ErrNotExist if the file doesn't exist yet (will retry).
func retrySysfsOperation(ctx context.Context, name string, checkFn func() error) error {
	var lastErr error
	for retry := range sysfsRetryMax {
		if retry > 0 {
			delay := sysfsRetryBaseDelay * time.Duration(1<<uint(retry-1))
			time.Sleep(delay)
		}

		err := checkFn()
		if err == nil {
			if retry > 0 {
				log.G(ctx).WithField("retry", retry).Debugf("%s succeeded after retry", name)
			}
			return nil
		}

		if os.IsNotExist(err) {
			lastErr = err
			continue // Retry on not exist
		}

		// Non-retryable error
		return err
	}

	return fmt.Errorf("%s failed after %d retries: %w", name, sysfsRetryMax, lastErr)
}

func (s *systemService) Info(ctx context.Context, _ *emptypb.Empty) (*api.InfoResponse, error) {
	v, err := os.ReadFile("/proc/version")
	if err != nil && !os.IsNotExist(err) {
		return nil, errgrpc.ToGRPC(err)
	}
	return &api.InfoResponse{
		Version:       "dev",
		KernelVersion: string(v),
	}, nil
}

func (s *systemService) OfflineCPU(ctx context.Context, req *api.OfflineCPURequest) (*emptypb.Empty, error) {
	cpuID := req.GetCpuID()
	if cpuID == 0 {
		return nil, errgrpc.ToGRPCf(errdefs.ErrInvalidArgument,
			"cpu %d cannot be offlined (boot processor)", cpuID)
	}

	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/online", cpuID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, errgrpc.ToGRPCf(errdefs.ErrNotFound, "cpu %d not present", cpuID)
		}
		return nil, errgrpc.ToGRPC(err)
	}

	// Check if already offline
	value, err := readSysfsValue(path)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}
	if value == sysfsOffline {
		return &emptypb.Empty{}, nil
	}

	// Offline the CPU
	if err := writeSysfsValue(path, sysfsOffline); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	log.G(ctx).WithField("cpu_id", cpuID).Debug("CPU offlined successfully")
	return &emptypb.Empty{}, nil
}

func (s *systemService) OnlineCPU(ctx context.Context, req *api.OnlineCPURequest) (*emptypb.Empty, error) {
	cpuID := req.GetCpuID()
	if cpuID == 0 {
		// CPU 0 is always online (boot processor)
		return &emptypb.Empty{}, nil
	}

	path := fmt.Sprintf("/sys/devices/system/cpu/cpu%d/online", cpuID)

	// Retry with exponential backoff - kernel may need time to create sysfs files after hotplug
	err := retrySysfsOperation(ctx, fmt.Sprintf("online CPU %d", cpuID), func() error {
		// Check if file exists
		if _, err := os.Stat(path); err != nil {
			return err // Returns os.ErrNotExist if not ready yet
		}

		// Check if already online
		value, err := readSysfsValue(path)
		if err != nil {
			return err
		}
		if value == sysfsOnline {
			return nil // Already online
		}

		// Write "1" to online the CPU
		return writeSysfsValue(path, sysfsOnline)
	})

	if err != nil {
		return nil, errgrpc.ToGRPCf(errdefs.ErrNotFound, "cpu %d: %v", cpuID, err)
	}

	log.G(ctx).WithField("cpu_id", cpuID).Debug("CPU onlined successfully")
	return &emptypb.Empty{}, nil
}

func (s *systemService) OfflineMemory(ctx context.Context, req *api.OfflineMemoryRequest) (*emptypb.Empty, error) {
	memoryID := req.GetMemoryID()

	// Check if auto_online is enabled (if so, can't offline)
	if autoOnline, err := readSysfsValue("/sys/devices/system/memory/auto_online_blocks"); err == nil {
		if autoOnline == "online" {
			log.G(ctx).WithField("memory_id", memoryID).
				Debug("memory auto-online enabled, cannot offline blocks")
			return &emptypb.Empty{}, nil // Non-fatal
		}
	}

	path := fmt.Sprintf("/sys/devices/system/memory/memory%d/online", memoryID)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, errgrpc.ToGRPCf(errdefs.ErrNotFound, "memory block %d not present", memoryID)
		}
		return nil, errgrpc.ToGRPC(err)
	}

	// Check if already offline
	value, err := readSysfsValue(path)
	if err != nil {
		return nil, errgrpc.ToGRPC(err)
	}
	if value == sysfsOffline {
		return &emptypb.Empty{}, nil
	}

	// Check if block is removable
	removablePath := fmt.Sprintf("/sys/devices/system/memory/memory%d/removable", memoryID)
	if removable, err := readSysfsValue(removablePath); err == nil {
		if removable == sysfsOffline {
			log.G(ctx).WithField("memory_id", memoryID).
				Warn("memory block is not removable (kernel in use)")
			return nil, errgrpc.ToGRPCf(errdefs.ErrFailedPrecondition,
				"memory block %d is not removable (kernel in use)", memoryID)
		}
	}

	// Offline the block
	if err := writeSysfsValue(path, sysfsOffline); err != nil {
		return nil, errgrpc.ToGRPC(err)
	}

	log.G(ctx).WithField("memory_id", memoryID).Debug("memory block offlined successfully")
	return &emptypb.Empty{}, nil
}

func (s *systemService) OnlineMemory(ctx context.Context, req *api.OnlineMemoryRequest) (*emptypb.Empty, error) {
	memoryID := req.GetMemoryID()

	// Check auto_online setting
	if autoOnline, err := readSysfsValue("/sys/devices/system/memory/auto_online_blocks"); err == nil {
		if autoOnline == "online" {
			// Memory will auto-online, verify it happened using retry logic
			path := fmt.Sprintf("/sys/devices/system/memory/memory%d/online", memoryID)
			time.Sleep(autoOnlineCheckDelay)
			if value, err := readSysfsValue(path); err == nil && value == sysfsOnline {
				log.G(ctx).WithField("memory_id", memoryID).Debug("memory auto-onlined")
				return &emptypb.Empty{}, nil
			}
		}
	}

	// Explicit online with retry logic - kernel may need time to create sysfs files after hotplug
	path := fmt.Sprintf("/sys/devices/system/memory/memory%d/online", memoryID)

	err := retrySysfsOperation(ctx, fmt.Sprintf("online memory %d", memoryID), func() error {
		// Check if file exists
		if _, err := os.Stat(path); err != nil {
			return err // Returns os.ErrNotExist if not ready yet
		}

		// Check if already online
		value, err := readSysfsValue(path)
		if err != nil {
			return err
		}
		if value == sysfsOnline {
			return nil // Already online
		}

		// Write "1" to online the memory
		return writeSysfsValue(path, sysfsOnline)
	})

	if err != nil {
		return nil, errgrpc.ToGRPCf(errdefs.ErrNotFound, "memory block %d: %v", memoryID, err)
	}

	log.G(ctx).WithField("memory_id", memoryID).Debug("memory onlined successfully")
	return &emptypb.Empty{}, nil
}

// writeRuntimeFeatures writes the runtime features to a well-known location
// that can be read by the shim manager
func (s *systemService) writeRuntimeFeatures() error {
	features := map[string]string{
		"containerd.io/runtime-allow-mounts": "mkdir/*,format/*,erofs,ext4",
		"containerd.io/runtime-type":         "vm",
		"containerd.io/vm-type":              "microvm",
	}

	featuresDir := "/run/vminitd"
	if err := os.MkdirAll(featuresDir, featuresDirPerms); err != nil {
		return err
	}

	data, err := json.Marshal(features)
	if err != nil {
		return err
	}

	featuresFile := filepath.Join(featuresDir, "features.json")
	return os.WriteFile(featuresFile, data, featuresFilePerms)
}
