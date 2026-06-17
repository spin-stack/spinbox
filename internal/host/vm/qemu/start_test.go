//go:build linux

package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQemuMachineType(t *testing.T) {
	tests := []struct {
		name     string
		override string // per-VM annotation value
		env      string // SPINBOX_QEMU_MACHINE
		want     string
	}{
		{name: "nothing set defaults to q35", override: "", env: "", want: "q35"},
		{name: "env pc", override: "", env: "pc", want: "pc"},
		{name: "annotation pc wins over empty env", override: "pc", env: "", want: "pc"},
		{name: "annotation wins over env", override: "q35", env: "pc", want: "q35"},
		{name: "invalid annotation falls through to env", override: "bogus", env: "pc", want: "pc"},
		{name: "invalid annotation and env default q35", override: "bogus", env: "junk", want: "q35"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("SPINBOX_QEMU_MACHINE", tt.env)
			assert.Equal(t, tt.want, qemuMachineType(tt.override))
		})
	}
}
