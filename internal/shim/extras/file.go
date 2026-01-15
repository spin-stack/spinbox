//go:build linux

// Package extras provides a generic mechanism for injecting files into VMs.
// It creates cached tar archives that are attached as virtio-blk devices,
// allowing the guest to extract files without TTRPC message size limits.
package extras

import "path/filepath"

// File represents a file to include in the extras disk.
// This is a generic structure usable by any component.
type File struct {
	Name string // Filename in archive (flat namespace, e.g., "spin-supervisor")
	Path string // Source path on host filesystem
	Mode int64  // File permissions (e.g., 0755 for executables)
}

// NewFile creates a File with name derived from path basename.
func NewFile(path string, mode int64) File {
	return File{
		Name: filepath.Base(path),
		Path: path,
		Mode: mode,
	}
}

// NewFileWithName creates a File with explicit name.
func NewFileWithName(name, path string, mode int64) File {
	return File{Name: name, Path: path, Mode: mode}
}
