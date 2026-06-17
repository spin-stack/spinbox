//go:build linux

package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQemuMachineType(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "unset defaults to q35", env: "", want: "q35"},
		{name: "explicit q35", env: "q35", want: "q35"},
		{name: "pc for A/B", env: "pc", want: "pc"},
		{name: "unknown falls back to q35", env: "microvm", want: "q35"},
		{name: "garbage falls back to q35", env: "not-a-machine", want: "q35"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SPINBOX_QEMU_MACHINE", tt.env)
			assert.Equal(t, tt.want, qemuMachineType())
		})
	}
}
