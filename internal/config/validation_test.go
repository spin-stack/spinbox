//go:build linux

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalizePath(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, tmpDir string) (path string, want string)
		wantErr bool
	}{
		{
			name: "cleans dot-dot paths",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				subDir := filepath.Join(tmpDir, "subdir")
				if err := os.MkdirAll(subDir, 0750); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(subDir, "..", "subdir"), subDir
			},
		},
		{
			name: "resolves symlinks",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				realDir := filepath.Join(tmpDir, "realdir")
				if err := os.MkdirAll(realDir, 0750); err != nil {
					t.Fatal(err)
				}
				symlinkPath := filepath.Join(tmpDir, "linkdir")
				if err := os.Symlink(realDir, symlinkPath); err != nil {
					t.Fatal(err)
				}
				return symlinkPath, realDir
			},
		},
		{
			name: "handles non-existent path",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				nonExistent := filepath.Join(tmpDir, "does", "not", "exist")
				// Want prefix match, not exact
				return nonExistent, tmpDir
			},
		},
		{
			name: "reveals symlink escape attempt",
			setup: func(t *testing.T, tmpDir string) (string, string) {
				safeDir := filepath.Join(tmpDir, "safe")
				unsafeDir := filepath.Join(tmpDir, "unsafe")
				if err := os.MkdirAll(safeDir, 0750); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(unsafeDir, 0750); err != nil {
					t.Fatal(err)
				}
				escapeLink := filepath.Join(safeDir, "escape")
				if err := os.Symlink(unsafeDir, escapeLink); err != nil {
					t.Fatal(err)
				}
				return escapeLink, unsafeDir
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path, want := tt.setup(t, tmpDir)

			got, err := canonicalizePath(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("canonicalizePath() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}

			// For non-existent paths, check prefix; otherwise exact match
			if tt.name == "handles non-existent path" {
				if !strings.HasPrefix(got, want) {
					t.Errorf("expected path to start with %s, got %s", want, got)
				}
			} else if got != want {
				t.Errorf("canonicalizePath() = %s, want %s", got, want)
			}
		})
	}
}

func TestValidateDirExists(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, tmpDir string) string
		wantErr bool
	}{
		{
			name: "accepts valid directory",
			setup: func(t *testing.T, tmpDir string) string {
				dir := filepath.Join(tmpDir, "valid")
				if err := os.MkdirAll(dir, 0750); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: false,
		},
		{
			name: "accepts symlink to valid directory",
			setup: func(t *testing.T, tmpDir string) string {
				targetDir := filepath.Join(tmpDir, "target")
				if err := os.MkdirAll(targetDir, 0750); err != nil {
					t.Fatal(err)
				}
				symlinkPath := filepath.Join(tmpDir, "link")
				if err := os.Symlink(targetDir, symlinkPath); err != nil {
					t.Fatal(err)
				}
				return symlinkPath
			},
			wantErr: false,
		},
		{
			name: "rejects non-existent directory",
			setup: func(t *testing.T, tmpDir string) string {
				return filepath.Join(tmpDir, "nonexistent")
			},
			wantErr: true,
		},
		{
			name: "rejects file instead of directory",
			setup: func(t *testing.T, tmpDir string) string {
				file := filepath.Join(tmpDir, "file")
				if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
					t.Fatal(err)
				}
				return file
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := tt.setup(t, tmpDir)

			err := validateDirExists(path, "test_field")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDirExists() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnsureDirWritable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, tmpDir string) string
		verify  func(t *testing.T, tmpDir string)
		wantErr bool
	}{
		{
			name: "accepts existing writable directory",
			setup: func(t *testing.T, tmpDir string) string {
				dir := filepath.Join(tmpDir, "writable")
				if err := os.MkdirAll(dir, 0750); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: false,
		},
		{
			name: "creates non-existent directory",
			setup: func(t *testing.T, tmpDir string) string {
				return filepath.Join(tmpDir, "newdir")
			},
			verify: func(t *testing.T, tmpDir string) {
				info, err := os.Stat(filepath.Join(tmpDir, "newdir"))
				if err != nil {
					t.Fatalf("directory not created: %v", err)
				}
				if !info.IsDir() {
					t.Error("expected directory")
				}
			},
			wantErr: false,
		},
		{
			name: "creates at canonical path through symlink",
			setup: func(t *testing.T, tmpDir string) string {
				realDir := filepath.Join(tmpDir, "realdir")
				if err := os.MkdirAll(realDir, 0750); err != nil {
					t.Fatal(err)
				}
				symlinkPath := filepath.Join(tmpDir, "linkdir")
				if err := os.Symlink(realDir, symlinkPath); err != nil {
					t.Fatal(err)
				}
				return filepath.Join(symlinkPath, "newsubdir")
			},
			verify: func(t *testing.T, tmpDir string) {
				expectedPath := filepath.Join(tmpDir, "realdir", "newsubdir")
				info, err := os.Stat(expectedPath)
				if err != nil {
					t.Fatalf("directory not created at canonical path: %v", err)
				}
				if !info.IsDir() {
					t.Error("expected directory")
				}
			},
			wantErr: false,
		},
		{
			name: "rejects file instead of directory",
			setup: func(t *testing.T, tmpDir string) string {
				file := filepath.Join(tmpDir, "file")
				if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
					t.Fatal(err)
				}
				return file
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := tt.setup(t, tmpDir)

			err := ensureDirWritable(path, "test_field")
			if (err != nil) != tt.wantErr {
				t.Errorf("ensureDirWritable() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.verify != nil && err == nil {
				tt.verify(t, tmpDir)
			}
		})
	}
}

func TestValidateExecutable(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T, tmpDir string) string
		wantErr bool
	}{
		{
			name: "accepts valid executable",
			setup: func(t *testing.T, tmpDir string) string {
				exe := filepath.Join(tmpDir, "exe")
				if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0750); err != nil {
					t.Fatal(err)
				}
				return exe
			},
			wantErr: false,
		},
		{
			name: "accepts symlink to executable",
			setup: func(t *testing.T, tmpDir string) string {
				realExe := filepath.Join(tmpDir, "realexe")
				if err := os.WriteFile(realExe, []byte("#!/bin/sh\n"), 0750); err != nil {
					t.Fatal(err)
				}
				symlinkPath := filepath.Join(tmpDir, "linkexe")
				if err := os.Symlink(realExe, symlinkPath); err != nil {
					t.Fatal(err)
				}
				return symlinkPath
			},
			wantErr: false,
		},
		{
			name: "rejects broken symlink",
			setup: func(t *testing.T, tmpDir string) string {
				brokenLink := filepath.Join(tmpDir, "broken")
				if err := os.Symlink("/nonexistent/target", brokenLink); err != nil {
					t.Fatal(err)
				}
				return brokenLink
			},
			wantErr: true,
		},
		{
			name: "rejects non-executable file",
			setup: func(t *testing.T, tmpDir string) string {
				file := filepath.Join(tmpDir, "noexec")
				if err := os.WriteFile(file, []byte("content"), 0644); err != nil {
					t.Fatal(err)
				}
				return file
			},
			wantErr: true,
		},
		{
			name: "rejects directory",
			setup: func(t *testing.T, tmpDir string) string {
				dir := filepath.Join(tmpDir, "dir")
				if err := os.MkdirAll(dir, 0750); err != nil {
					t.Fatal(err)
				}
				return dir
			},
			wantErr: true,
		},
		{
			name: "rejects non-existent file",
			setup: func(t *testing.T, tmpDir string) string {
				return filepath.Join(tmpDir, "nonexistent")
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := tt.setup(t, tmpDir)

			err := validateExecutable(path, "test_exe")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateExecutable() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
