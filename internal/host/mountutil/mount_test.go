//go:build linux

package mountutil

import (
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
)

func TestFormatString_MountPattern(t *testing.T) {
	active := []mount.ActiveMount{
		{MountPoint: "/mnt/layer0"},
		{MountPoint: "/mnt/layer1"},
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{
			name:  "compact format",
			input: "{{mount 0}}/upper",
			want:  "/mnt/layer0/upper",
		},
		{
			name:  "spaced format",
			input: "{{ mount 0 }}/upper",
			want:  "/mnt/layer0/upper",
		},
		{
			name:  "extra spaces",
			input: "{{  mount  1  }}/work",
			want:  "/mnt/layer1/work",
		},
		{
			name:  "mkdir option style",
			input: "X-containerd.mkdir.path={{ mount 0 }}/upper:0755",
			want:  "X-containerd.mkdir.path=/mnt/layer0/upper:0755",
		},
		{
			name:    "invalid pattern",
			input:   "{{ invalid 0 }}",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := formatString(tt.input)
			if fn == nil {
				t.Fatal("formatString returned nil for input with {{}}")
			}

			got, err := fn(active)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatString_SourceTargetPatterns(t *testing.T) {
	active := []mount.ActiveMount{
		{Mount: mount.Mount{Source: "/dev/vda", Target: "/mnt/root"}, MountPoint: "/mnt/0"},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"source compact", "{{source 0}}", "/dev/vda"},
		{"source spaced", "{{ source 0 }}", "/dev/vda"},
		{"target compact", "{{target 0}}", "/mnt/root"},
		{"target spaced", "{{ target 0 }}", "/mnt/root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := formatString(tt.input)
			if fn == nil {
				t.Fatal("formatString returned nil")
			}

			got, err := fn(active)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatString_OverlayPattern(t *testing.T) {
	active := []mount.ActiveMount{
		{MountPoint: "/mnt/0"},
		{MountPoint: "/mnt/1"},
		{MountPoint: "/mnt/2"},
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"compact", "{{overlay 0 2}}", "/mnt/0:/mnt/1:/mnt/2"},
		{"spaced", "{{ overlay 0 2 }}", "/mnt/0:/mnt/1:/mnt/2"},
		{"reverse", "{{overlay 2 0}}", "/mnt/2:/mnt/1:/mnt/0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fn := formatString(tt.input)
			if fn == nil {
				t.Fatal("formatString returned nil")
			}

			got, err := fn(active)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatString_NoPattern(t *testing.T) {
	// Strings without {{ should return nil
	fn := formatString("plain string")
	if fn != nil {
		t.Error("expected nil for string without format markers")
	}
}
