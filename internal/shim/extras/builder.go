//go:build linux

package extras

// Builder collects files from multiple sources for the extras disk.
// This allows different components to contribute files independently.
//
// Example usage:
//
//	builder := extras.NewBuilder()
//	builder.AddExecutable("spin-supervisor", "/usr/share/spin-stack/bin/spin-supervisor")
//	builder.AddExecutable("metrics-agent", "/path/to/metrics-agent")
//	files := builder.Files()
type Builder struct {
	files []File
}

// NewBuilder creates a new Builder.
func NewBuilder() *Builder {
	return &Builder{}
}

// Add adds a file to the extras disk.
func (b *Builder) Add(f File) *Builder {
	b.files = append(b.files, f)
	return b
}

// AddExecutable adds an executable file (mode 0755).
func (b *Builder) AddExecutable(name, path string) *Builder {
	return b.Add(NewFileWithName(name, path, 0755))
}

// Files returns all collected files.
func (b *Builder) Files() []File {
	return b.files
}

// IsEmpty returns true if no files have been added.
func (b *Builder) IsEmpty() bool {
	return len(b.files) == 0
}
