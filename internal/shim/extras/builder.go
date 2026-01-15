//go:build linux

package extras

// Builder collects files from multiple sources for the extras disk.
// This allows different components to contribute files independently.
//
// Example usage:
//
//	builder := extras.NewBuilder()
//	builder.AddExecutable("/usr/local/bin/supervisor", "/usr/share/spin-stack/bin/spin-supervisor")
//	builder.Add("/etc/myapp/config.json", "/host/path/to/config.json", 0644)
//	files := builder.Files()
type Builder struct {
	files []File
}

// NewBuilder creates a new Builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Add adds a file mapping: destPath in VM <- sourcePath on host.
func (b *Builder) Add(destPath, sourcePath string, mode int64) *Builder {
	b.files = append(b.files, NewFile(destPath, sourcePath, mode))
	return b
}

// AddExecutable adds an executable file (mode 0755).
// destPath is where the file will be placed in the VM.
// sourcePath is the location of the file on the host.
func (b *Builder) AddExecutable(destPath, sourcePath string) *Builder {
	return b.Add(destPath, sourcePath, 0755)
}

// Files returns all collected files.
func (b *Builder) Files() []File {
	return b.files
}

// IsEmpty returns true if no files have been added.
func (b *Builder) IsEmpty() bool {
	return len(b.files) == 0
}
