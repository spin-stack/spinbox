//go:build linux

package system

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseKmsgRecord(t *testing.T) {
	tests := []struct {
		name    string
		record  string
		wantTS  int64
		wantMsg string
		wantOK  bool
	}{
		{
			name:    "initcall record",
			record:  "6,123,187940,-;initcall pci_subsys_init+0x0/0x40 returned 0 after 12345 usecs",
			wantTS:  187940,
			wantMsg: "initcall pci_subsys_init+0x0/0x40 returned 0 after 12345 usecs",
			wantOK:  true,
		},
		{
			name:    "trailing newline and continuation dropped",
			record:  "5,80,5085,-;EXT4-fs (vdb): mounted\n SUBSYSTEM=block\n DEVICE=b259:0",
			wantTS:  5085,
			wantMsg: "EXT4-fs (vdb): mounted",
			wantOK:  true,
		},
		{
			name:   "no semicolon",
			record: "garbage without separator",
			wantOK: false,
		},
		{
			name:    "header too short still yields message",
			record:  "6;some message",
			wantTS:  0,
			wantMsg: "some message",
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, msg, ok := parseKmsgRecord(tt.record)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantTS, ts)
				assert.Equal(t, tt.wantMsg, msg)
			}
		})
	}
}

func TestExtractInitcall(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		name, usec, ok := extractInitcall("initcall acpi_init+0x0/0x460 returned 0 after 39000 usecs")
		assert.True(t, ok)
		assert.Equal(t, "acpi_init+0x0/0x460", name)
		assert.Equal(t, 39000, usec)
	})

	t.Run("non-initcall message", func(t *testing.T) {
		_, _, ok := extractInitcall("erofs (device vda): mounted with root inode @ nid 60")
		assert.False(t, ok)
	})

	t.Run("calling line is not a completion", func(t *testing.T) {
		_, _, ok := extractInitcall("calling  acpi_init+0x0/0x460 @ 1")
		assert.False(t, ok)
	})
}
