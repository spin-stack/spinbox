//go:build linux

package mountutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	types "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/mount"
)

func TestParseMkdirOption(t *testing.T) {
	baseDir := "/mnt/test"

	tests := []struct {
		name    string
		opt     string
		baseDir string
		want    *mkdirSpec
		wantErr string
	}{
		{
			name:    "path only",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo",
			baseDir: baseDir,
			want:    &mkdirSpec{Path: "/mnt/test/foo", Mode: 0755, UID: -1, GID: -1},
		},
		{
			name:    "path and mode",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:700",
			baseDir: baseDir,
			want:    &mkdirSpec{Path: "/mnt/test/foo", Mode: 0700, UID: -1, GID: -1},
		},
		{
			name:    "path mode uid",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:755:1000",
			baseDir: baseDir,
			want:    &mkdirSpec{Path: "/mnt/test/foo", Mode: 0755, UID: 1000, GID: -1},
		},
		{
			name:    "path mode uid gid",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:700:1000:1000",
			baseDir: baseDir,
			want:    &mkdirSpec{Path: "/mnt/test/foo", Mode: 0700, UID: 1000, GID: 1000},
		},
		{
			name:    "unknown mkdir option",
			opt:     "X-containerd.mkdir.unknown=foo",
			baseDir: baseDir,
			wantErr: "unknown mkdir mount option",
		},
		{
			name:    "path outside basedir",
			opt:     "X-containerd.mkdir.path=/etc/passwd",
			baseDir: baseDir,
			wantErr: "must be under",
		},
		{
			name:    "invalid mode",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:notamode",
			baseDir: baseDir,
			wantErr: "invalid mode",
		},
		{
			name:    "invalid uid",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:755:notauid",
			baseDir: baseDir,
			wantErr: "invalid uid",
		},
		{
			name:    "invalid gid",
			opt:     "X-containerd.mkdir.path=/mnt/test/foo:755:1000:notagid",
			baseDir: baseDir,
			wantErr: "invalid gid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMkdirOption(tt.opt, tt.baseDir)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if got.Path != tt.want.Path {
				t.Errorf("Path = %q, want %q", got.Path, tt.want.Path)
			}
			if got.Mode != tt.want.Mode {
				t.Errorf("Mode = %o, want %o", got.Mode, tt.want.Mode)
			}
			if got.UID != tt.want.UID {
				t.Errorf("UID = %d, want %d", got.UID, tt.want.UID)
			}
			if got.GID != tt.want.GID {
				t.Errorf("GID = %d, want %d", got.GID, tt.want.GID)
			}
		})
	}
}

func TestProcessMkdirOptions(t *testing.T) {
	baseDir := "/mnt/test"

	tests := []struct {
		name        string
		options     []string
		wantRemain  []string
		wantSpecLen int
		wantErr     string
	}{
		{
			name:        "no mkdir options",
			options:     []string{"ro", "bind"},
			wantRemain:  []string{"ro", "bind"},
			wantSpecLen: 0,
		},
		{
			name:        "only mkdir options",
			options:     []string{"X-containerd.mkdir.path=/mnt/test/foo"},
			wantRemain:  nil,
			wantSpecLen: 1,
		},
		{
			name:        "mixed options",
			options:     []string{"ro", "X-containerd.mkdir.path=/mnt/test/foo", "bind"},
			wantRemain:  []string{"ro", "bind"},
			wantSpecLen: 1,
		},
		{
			name:        "multiple mkdir options",
			options:     []string{"X-containerd.mkdir.path=/mnt/test/foo", "X-containerd.mkdir.path=/mnt/test/bar"},
			wantRemain:  nil,
			wantSpecLen: 2,
		},
		{
			name:    "invalid mkdir option",
			options: []string{"X-containerd.mkdir.unknown=foo"},
			wantErr: "unknown mkdir mount option",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining, specs, err := processMkdirOptions(tt.options, baseDir)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(remaining) != len(tt.wantRemain) {
				t.Errorf("remaining = %v, want %v", remaining, tt.wantRemain)
			}
			for i, r := range remaining {
				if r != tt.wantRemain[i] {
					t.Errorf("remaining[%d] = %q, want %q", i, r, tt.wantRemain[i])
				}
			}

			if len(specs) != tt.wantSpecLen {
				t.Errorf("len(specs) = %d, want %d", len(specs), tt.wantSpecLen)
			}
		})
	}
}

func TestApplyMkdirSpecs(t *testing.T) {
	t.Run("creates directory with default mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "testdir")

		specs := []*mkdirSpec{{Path: path, Mode: 0755, UID: -1, GID: -1}}
		if err := applyMkdirSpecs(specs); err != nil {
			t.Fatalf("applyMkdirSpecs() error = %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if !info.IsDir() {
			t.Error("expected directory")
		}
	})

	t.Run("creates nested directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		path := filepath.Join(tmpDir, "a", "b", "c")

		specs := []*mkdirSpec{{Path: path, Mode: 0700, UID: -1, GID: -1}}
		if err := applyMkdirSpecs(specs); err != nil {
			t.Fatalf("applyMkdirSpecs() error = %v", err)
		}

		if _, err := os.Stat(path); err != nil {
			t.Fatalf("directory not created: %v", err)
		}
	})

	t.Run("multiple specs", func(t *testing.T) {
		tmpDir := t.TempDir()
		specs := []*mkdirSpec{
			{Path: filepath.Join(tmpDir, "dir1"), Mode: 0755, UID: -1, GID: -1},
			{Path: filepath.Join(tmpDir, "dir2"), Mode: 0700, UID: -1, GID: -1},
		}

		if err := applyMkdirSpecs(specs); err != nil {
			t.Fatalf("applyMkdirSpecs() error = %v", err)
		}

		for _, spec := range specs {
			if _, err := os.Stat(spec.Path); err != nil {
				t.Errorf("directory %q not created: %v", spec.Path, err)
			}
		}
	})
}

func TestParseIndex(t *testing.T) {
	tests := []struct {
		name     string
		indexStr string
		maxLen   int
		want     int
		wantErr  string
	}{
		{
			name:     "valid index 0",
			indexStr: "0",
			maxLen:   5,
			want:     0,
		},
		{
			name:     "valid index 4",
			indexStr: "4",
			maxLen:   5,
			want:     4,
		},
		{
			name:     "index out of bounds (too high)",
			indexStr: "5",
			maxLen:   5,
			wantErr:  "index out of bounds",
		},
		{
			name:     "negative index",
			indexStr: "-1",
			maxLen:   5,
			wantErr:  "index out of bounds",
		},
		{
			name:     "invalid index (not a number)",
			indexStr: "abc",
			maxLen:   5,
			wantErr:  "invalid index",
		},
		{
			name:     "empty maxLen",
			indexStr: "0",
			maxLen:   0,
			wantErr:  "index out of bounds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseIndex(tt.indexStr, tt.maxLen)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("parseIndex() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBuildOverlayDirs(t *testing.T) {
	mounts := []mount.ActiveMount{
		{MountPoint: "/mnt/0"},
		{MountPoint: "/mnt/1"},
		{MountPoint: "/mnt/2"},
		{MountPoint: "/mnt/3"},
	}

	tests := []struct {
		name    string
		start   int
		end     int
		mounts  []mount.ActiveMount
		want    string
		wantErr string
	}{
		{
			name:   "ascending order",
			start:  0,
			end:    2,
			mounts: mounts,
			want:   "/mnt/0:/mnt/1:/mnt/2",
		},
		{
			name:   "descending order",
			start:  2,
			end:    0,
			mounts: mounts,
			want:   "/mnt/2:/mnt/1:/mnt/0",
		},
		{
			name:   "single element",
			start:  1,
			end:    1,
			mounts: mounts,
			want:   "/mnt/1",
		},
		{
			name:   "full range",
			start:  0,
			end:    3,
			mounts: mounts,
			want:   "/mnt/0:/mnt/1:/mnt/2:/mnt/3",
		},
		{
			name:    "start out of bounds (ascending)",
			start:   -1,
			end:     2,
			mounts:  mounts,
			wantErr: "invalid range",
		},
		{
			name:    "end out of bounds (ascending)",
			start:   0,
			end:     5,
			mounts:  mounts,
			wantErr: "invalid range",
		},
		{
			name:    "start out of bounds (descending)",
			start:   5,
			end:     0,
			mounts:  mounts,
			wantErr: "invalid range",
		},
		{
			name:    "end out of bounds (descending)",
			start:   2,
			end:     -1,
			mounts:  mounts,
			wantErr: "invalid range",
		},
		{
			name:    "empty mounts",
			start:   0,
			end:     0,
			mounts:  nil,
			wantErr: "invalid range",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildOverlayDirs(tt.start, tt.end, tt.mounts)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("buildOverlayDirs() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatString(t *testing.T) {
	mounts := []mount.ActiveMount{
		{Mount: mount.Mount{Source: "/src/0", Target: "/tgt/0"}, MountPoint: "/mnt/0"},
		{Mount: mount.Mount{Source: "/src/1", Target: "/tgt/1"}, MountPoint: "/mnt/1"},
		{Mount: mount.Mount{Source: "/src/2", Target: "/tgt/2"}, MountPoint: "/mnt/2"},
	}

	tests := []struct {
		name    string
		input   string
		mounts  []mount.ActiveMount
		want    string
		wantNil bool
		wantErr string
	}{
		{
			name:    "no pattern returns nil function",
			input:   "plain string",
			wantNil: true,
		},
		{
			name:   "source pattern",
			input:  "prefix-{{ source 0 }}-suffix",
			mounts: mounts,
			want:   "prefix-/src/0-suffix",
		},
		{
			name:   "target pattern",
			input:  "prefix-{{ target 1 }}-suffix",
			mounts: mounts,
			want:   "prefix-/tgt/1-suffix",
		},
		{
			name:   "mount pattern",
			input:  "prefix-{{ mount 2 }}-suffix",
			mounts: mounts,
			want:   "prefix-/mnt/2-suffix",
		},
		{
			name:   "overlay pattern ascending",
			input:  "lowerdir={{ overlay 0 2 }}",
			mounts: mounts,
			want:   "lowerdir=/mnt/0:/mnt/1:/mnt/2",
		},
		{
			name:   "overlay pattern descending",
			input:  "lowerdir={{ overlay 2 0 }}",
			mounts: mounts,
			want:   "lowerdir=/mnt/2:/mnt/1:/mnt/0",
		},
		{
			name:   "multiple patterns",
			input:  "{{ source 0 }}-{{ mount 1 }}",
			mounts: mounts,
			want:   "/src/0-/mnt/1",
		},
		{
			name:   "flexible whitespace",
			input:  "{{source 0}}-{{  mount  1  }}",
			mounts: mounts,
			want:   "/src/0-/mnt/1",
		},
		{
			name:    "source out of bounds",
			input:   "{{ source 5 }}",
			mounts:  mounts,
			wantErr: "index out of bounds",
		},
		{
			name:    "overlay out of bounds",
			input:   "{{ overlay 0 10 }}",
			mounts:  mounts,
			wantErr: "invalid range",
		},
		{
			name:    "unsupported pattern",
			input:   "{{ unknown 0 }}",
			mounts:  mounts,
			wantErr: "unsupported format pattern",
		},
		{
			name:    "empty mounts with pattern",
			input:   "{{ source 0 }}",
			mounts:  nil,
			wantErr: "index out of bounds",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := formatString(tt.input)

			if tt.wantNil {
				if fn != nil {
					t.Fatal("expected nil function")
				}
				return
			}

			if fn == nil {
				t.Fatal("expected non-nil function")
			}

			got, err := fn(tt.mounts)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("formatString()() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyFormatSubstitution(t *testing.T) {
	mounts := []mount.ActiveMount{
		{Mount: mount.Mount{Source: "/src/0", Target: "/tgt/0"}, MountPoint: "/mnt/0"},
		{Mount: mount.Mount{Source: "/src/1", Target: "/tgt/1"}, MountPoint: "/mnt/1"},
	}

	tests := []struct {
		name       string
		mount      *types.Mount
		wantSource string
		wantTarget string
		wantOpts   []string
		wantErr    string
	}{
		{
			name: "no substitution needed",
			mount: &types.Mount{
				Source:  "/plain/source",
				Target:  "/plain/target",
				Options: []string{"ro", "bind"},
			},
			wantSource: "/plain/source",
			wantTarget: "/plain/target",
			wantOpts:   []string{"ro", "bind"},
		},
		{
			name: "substitute source",
			mount: &types.Mount{
				Source:  "{{ source 0 }}",
				Target:  "/target",
				Options: []string{"ro"},
			},
			wantSource: "/src/0",
			wantTarget: "/target",
			wantOpts:   []string{"ro"},
		},
		{
			name: "substitute target",
			mount: &types.Mount{
				Source:  "/source",
				Target:  "{{ target 1 }}",
				Options: []string{"rw"},
			},
			wantSource: "/source",
			wantTarget: "/tgt/1",
			wantOpts:   []string{"rw"},
		},
		{
			name: "substitute option",
			mount: &types.Mount{
				Source:  "/source",
				Target:  "/target",
				Options: []string{"lowerdir={{ overlay 0 1 }}", "upperdir=/upper"},
			},
			wantSource: "/source",
			wantTarget: "/target",
			wantOpts:   []string{"lowerdir=/mnt/0:/mnt/1", "upperdir=/upper"},
		},
		{
			name: "invalid source pattern",
			mount: &types.Mount{
				Source: "{{ source 99 }}",
			},
			wantErr: "source",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := applyFormatSubstitution(tt.mount, mounts)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.mount.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", tt.mount.Source, tt.wantSource)
			}
			if tt.mount.Target != tt.wantTarget {
				t.Errorf("Target = %q, want %q", tt.mount.Target, tt.wantTarget)
			}
			if len(tt.mount.Options) != len(tt.wantOpts) {
				t.Errorf("Options = %v, want %v", tt.mount.Options, tt.wantOpts)
			}
			for i, opt := range tt.mount.Options {
				if opt != tt.wantOpts[i] {
					t.Errorf("Options[%d] = %q, want %q", i, opt, tt.wantOpts[i])
				}
			}
		})
	}
}

func TestAll_EmptyMounts(t *testing.T) {
	cleanup, err := All(context.Background(), "/rootfs", "/mdir", nil)
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if cleanup != nil {
		t.Error("expected nil cleanup for empty mounts")
	}
}

func TestAll_BindMount(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to perform bind mounts")
	}

	ctx := context.Background()
	rootfs := t.TempDir()
	source := t.TempDir()
	mountDir := t.TempDir()

	// Create a test file in source
	testFile := filepath.Join(source, "testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	cleanup, err := All(ctx, rootfs, mountDir, []*types.Mount{{
		Type:    "bind",
		Source:  source,
		Options: []string{"rbind", "rw"},
	}})
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if cleanup == nil {
		t.Fatal("expected cleanup function")
	}

	// Verify mount worked - test file should be visible
	mountedFile := filepath.Join(rootfs, "testfile")
	if _, err := os.Stat(mountedFile); err != nil {
		t.Errorf("test file not visible after mount: %v", err)
	}

	// Cleanup
	if err := cleanup(ctx); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}

	// Verify unmounted
	if isMountPoint(rootfs) {
		t.Error("rootfs still mounted after cleanup")
	}
}

func isMountPoint(path string) bool {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		if fields[4] == path {
			return true
		}
	}
	return false
}
