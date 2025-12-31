package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aledbf/qemubox/containerd/internal/config"
)

func TestFileExists_ResolvesSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a real file
	realFile := filepath.Join(tmpDir, "realfile")
	if err := os.WriteFile(realFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to the file
	symlinkPath := filepath.Join(tmpDir, "linkfile")
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// fileExists should return true for the symlink
	if !fileExists(symlinkPath) {
		t.Error("fileExists should return true for symlink to existing file")
	}

	// fileExists should return true for the real file
	if !fileExists(realFile) {
		t.Error("fileExists should return true for real file")
	}
}

func TestFileExists_FailsForBrokenSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a symlink to a non-existent target
	brokenLink := filepath.Join(tmpDir, "broken")
	if err := os.Symlink("/nonexistent/target", brokenLink); err != nil {
		t.Fatal(err)
	}

	// fileExists should return false for broken symlink
	if fileExists(brokenLink) {
		t.Error("fileExists should return false for broken symlink")
	}
}

func TestFileExists_FailsForDirectory(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a directory
	dirPath := filepath.Join(tmpDir, "testdir")
	if err := os.MkdirAll(dirPath, 0750); err != nil {
		t.Fatal(err)
	}

	// fileExists should return false for directory
	if fileExists(dirPath) {
		t.Error("fileExists should return false for directory")
	}
}

func TestDirExists_ResolvesSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a real directory
	realDir := filepath.Join(tmpDir, "realdir")
	if err := os.MkdirAll(realDir, 0750); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to the directory
	symlinkPath := filepath.Join(tmpDir, "linkdir")
	if err := os.Symlink(realDir, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// dirExists should return true for the symlink
	if !dirExists(symlinkPath) {
		t.Error("dirExists should return true for symlink to existing directory")
	}

	// dirExists should return true for the real directory
	if !dirExists(realDir) {
		t.Error("dirExists should return true for real directory")
	}
}

func TestDirExists_FailsForBrokenSymlink(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a symlink to a non-existent target
	brokenLink := filepath.Join(tmpDir, "broken")
	if err := os.Symlink("/nonexistent/target", brokenLink); err != nil {
		t.Fatal(err)
	}

	// dirExists should return false for broken symlink
	if dirExists(brokenLink) {
		t.Error("dirExists should return false for broken symlink")
	}
}

func TestDirExists_FailsForFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file
	filePath := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(filePath, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// dirExists should return false for file
	if dirExists(filePath) {
		t.Error("dirExists should return false for file")
	}
}

func TestDirExists_SymlinkToFile(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a file
	realFile := filepath.Join(tmpDir, "realfile")
	if err := os.WriteFile(realFile, []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a symlink to the file (named like a directory)
	symlinkPath := filepath.Join(tmpDir, "fakedir")
	if err := os.Symlink(realFile, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// dirExists should return false because target is a file, not a directory
	if dirExists(symlinkPath) {
		t.Error("dirExists should return false for symlink pointing to a file")
	}
}

// Tests for path functions

func TestKernelPath(t *testing.T) {
	cfg := config.PathsConfig{
		ShareDir: "/usr/share/qemubox",
	}

	got := KernelPath(cfg)
	want := "/usr/share/qemubox/kernel/qemubox-kernel-x86_64"

	if got != want {
		t.Errorf("KernelPath() = %q, want %q", got, want)
	}
}

func TestInitrdPath(t *testing.T) {
	cfg := config.PathsConfig{
		ShareDir: "/usr/share/qemubox",
	}

	got := InitrdPath(cfg)
	want := "/usr/share/qemubox/kernel/qemubox-initrd"

	if got != want {
		t.Errorf("InitrdPath() = %q, want %q", got, want)
	}
}

func TestQemuPath_ExplicitConfig(t *testing.T) {
	cfg := config.PathsConfig{
		ShareDir: "/usr/share/qemubox",
		QEMUPath: "/custom/path/qemu-system-x86_64",
	}

	got := QemuPath(cfg)
	want := "/custom/path/qemu-system-x86_64"

	if got != want {
		t.Errorf("QemuPath() with explicit config = %q, want %q", got, want)
	}
}

func TestQemuSharePath_ExplicitConfig(t *testing.T) {
	cfg := config.PathsConfig{
		ShareDir:      "/usr/share/qemubox",
		QEMUSharePath: "/custom/share/qemu",
	}

	got := QemuSharePath(cfg)
	want := "/custom/share/qemu"

	if got != want {
		t.Errorf("QemuSharePath() with explicit config = %q, want %q", got, want)
	}
}

func TestDiscoverQemuPath_FindsInShareDir(t *testing.T) {
	shareDir := t.TempDir()

	// Create qemu binary in share dir
	binDir := filepath.Join(shareDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	qemuPath := filepath.Join(binDir, "qemu-system-x86_64")
	if err := os.WriteFile(qemuPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	got := discoverQemuPath(shareDir)
	if got != qemuPath {
		t.Errorf("discoverQemuPath() = %q, want %q", got, qemuPath)
	}
}

func TestDiscoverQemuPath_FallsBackToDefault(t *testing.T) {
	// Use a temp dir with no qemu binary
	shareDir := t.TempDir()

	got := discoverQemuPath(shareDir)
	want := "/usr/bin/qemu-system-x86_64"

	if got != want {
		t.Errorf("discoverQemuPath() fallback = %q, want %q", got, want)
	}
}

func TestDiscoverQemuSharePath_FindsInShareDir(t *testing.T) {
	shareDir := t.TempDir()

	// Create qemu share directory
	qemuShareDir := filepath.Join(shareDir, "qemu")
	if err := os.MkdirAll(qemuShareDir, 0755); err != nil {
		t.Fatal(err)
	}

	got := discoverQemuSharePath(shareDir)
	if got != qemuShareDir {
		t.Errorf("discoverQemuSharePath() = %q, want %q", got, qemuShareDir)
	}
}

func TestDiscoverQemuSharePath_FallsBackToDefault(t *testing.T) {
	// Use a temp dir with no qemu share directory
	shareDir := t.TempDir()

	got := discoverQemuSharePath(shareDir)
	want := "/usr/share/qemu"

	if got != want {
		t.Errorf("discoverQemuSharePath() fallback = %q, want %q", got, want)
	}
}

func TestQemuPath_Discovery(t *testing.T) {
	shareDir := t.TempDir()

	// Create qemu binary in share dir
	binDir := filepath.Join(shareDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	qemuPath := filepath.Join(binDir, "qemu-system-x86_64")
	if err := os.WriteFile(qemuPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	cfg := config.PathsConfig{
		ShareDir: shareDir,
		// QEMUPath not set - should trigger discovery
	}

	got := QemuPath(cfg)
	if got != qemuPath {
		t.Errorf("QemuPath() with discovery = %q, want %q", got, qemuPath)
	}
}

func TestQemuSharePath_Discovery(t *testing.T) {
	shareDir := t.TempDir()

	// Create qemu share directory
	qemuShareDir := filepath.Join(shareDir, "qemu")
	if err := os.MkdirAll(qemuShareDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := config.PathsConfig{
		ShareDir: shareDir,
		// QEMUSharePath not set - should trigger discovery
	}

	got := QemuSharePath(cfg)
	if got != qemuShareDir {
		t.Errorf("QemuSharePath() with discovery = %q, want %q", got, qemuShareDir)
	}
}

func TestDiscoverQemuPath_SymlinkToQemu(t *testing.T) {
	shareDir := t.TempDir()

	// Create a real qemu binary somewhere else
	realBinDir := filepath.Join(shareDir, "real-bin")
	if err := os.MkdirAll(realBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	realQemuPath := filepath.Join(realBinDir, "qemu-system-x86_64")
	if err := os.WriteFile(realQemuPath, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create symlink in expected location
	binDir := filepath.Join(shareDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	symlinkPath := filepath.Join(binDir, "qemu-system-x86_64")
	if err := os.Symlink(realQemuPath, symlinkPath); err != nil {
		t.Fatal(err)
	}

	got := discoverQemuPath(shareDir)
	// Should find the symlink location (which resolves to the real binary)
	if got != symlinkPath {
		t.Errorf("discoverQemuPath() with symlink = %q, want %q", got, symlinkPath)
	}
}
