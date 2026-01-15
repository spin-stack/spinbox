//go:build linux

// Package extras provides a generic mechanism for injecting files into VMs.
// It creates cached tar archives that are attached as virtio-blk devices,
// allowing the guest to extract files without TTRPC message size limits.
//
// Files are specified as key-value pairs where:
//   - Key (DestPath): The destination path in the VM where the file will be extracted
//   - Value (SourcePath): The source path on the host filesystem
package extras

// File represents a file to include in the extras disk.
// DestPath specifies where the file will be extracted in the VM.
// SourcePath is the location of the file on the host.
type File struct {
	DestPath   string // Destination path in VM (e.g., "/usr/local/bin/supervisor")
	SourcePath string // Source path on host filesystem
	Mode       int64  // File permissions (e.g., 0755)
}

// NewFile creates a File mapping a host path to a VM destination path.
func NewFile(destPath, sourcePath string, mode int64) File {
	return File{
		DestPath:   destPath,
		SourcePath: sourcePath,
		Mode:       mode,
	}
}
