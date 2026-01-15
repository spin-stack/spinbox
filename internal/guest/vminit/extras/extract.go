//go:build linux

// Package extras provides file extraction from extras disk inside the VM guest.
// It parses the spin.extras_disk kernel parameter, reads the tar archive from
// the corresponding block device, and extracts files to a well-known location.
package extras

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/containerd/log"
)

// TargetDir is where extras are extracted inside the VM.
const TargetDir = "/run/spin-stack/extras"

// GetDeviceFromCmdline parses spin.extras_disk=N from kernel cmdline.
// Returns device path (e.g., "/dev/vdb") or empty string if not configured.
func GetDeviceFromCmdline(ctx context.Context) (string, error) {
	cmdline, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return "", fmt.Errorf("read cmdline: %w", err)
	}

	for param := range strings.FieldsSeq(string(cmdline)) {
		if indexStr, ok := strings.CutPrefix(param, "spin.extras_disk="); ok {
			index, err := strconv.Atoi(indexStr)
			if err != nil {
				return "", fmt.Errorf("parse extras_disk index: %w", err)
			}
			// Convert index to device: 0→vda, 1→vdb, etc.
			device := fmt.Sprintf("/dev/vd%c", 'a'+index)
			log.G(ctx).WithFields(log.Fields{
				"index":  index,
				"device": device,
			}).Debug("found extras disk in kernel cmdline")
			return device, nil
		}
	}

	return "", nil
}

// Extract is the main entry point called during system init.
// It checks for extras disk and extracts files if present.
func Extract(ctx context.Context) error {
	device, err := GetDeviceFromCmdline(ctx)
	if err != nil {
		return err
	}
	if device == "" {
		log.G(ctx).Debug("no extras disk configured")
		return nil
	}

	return ExtractFromDevice(ctx, device, TargetDir)
}

// ExtractFromDevice reads tar from block device and extracts to targetDir.
func ExtractFromDevice(ctx context.Context, devicePath, targetDir string) error {
	f, err := os.Open(devicePath)
	if err != nil {
		return fmt.Errorf("open device %s: %w", devicePath, err)
	}
	defer f.Close()

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	tr := tar.NewReader(f)
	var extracted int
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar entry: %w", err)
		}

		// Security: use only base name, prevent path traversal
		targetPath := filepath.Join(targetDir, filepath.Base(hdr.Name))

		outFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
		if err != nil {
			return fmt.Errorf("create file %s: %w", hdr.Name, err)
		}

		// Use CopyN with header size to prevent decompression bombs (gosec G110)
		// The tar header contains the expected file size, so we limit to that.
		n, err := io.CopyN(outFile, tr, hdr.Size)
		outFile.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("extract file %s: %w", hdr.Name, err)
		}

		log.G(ctx).WithFields(log.Fields{
			"file": hdr.Name,
			"size": n,
			"mode": fmt.Sprintf("%o", hdr.Mode),
		}).Debug("extracted file from extras disk")
		extracted++
	}

	log.G(ctx).WithField("count", extracted).Info("extracted files from extras disk")
	return nil
}

// GetFile returns the full path to an extracted file.
// Returns error if file doesn't exist.
func GetFile(name string) (string, error) {
	path := filepath.Join(TargetDir, name)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("extras file %s not found: %w", name, err)
	}
	return path, nil
}

// FileExists checks if a file exists in the extras directory.
func FileExists(name string) bool {
	path := filepath.Join(TargetDir, name)
	_, err := os.Stat(path)
	return err == nil
}
