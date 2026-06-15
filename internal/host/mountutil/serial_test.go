//go:build linux

package mountutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSysBlock builds a temporary /sys/block-like tree mapping device name -> serial.
func fakeSysBlock(t *testing.T, devSerials map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for dev, serial := range devSerials {
		dir := filepath.Join(root, dev)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		if serial != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "serial"), []byte(serial+"\n"), 0o600))
		}
	}
	return root
}

func TestResolveBlockSerialSource_NoScheme(t *testing.T) {
	// Ordinary sources (host fallback path) are returned unchanged, ok=false.
	resolved, ok, err := resolveBlockSerialSource(context.Background(), "/dev/sda1")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "/dev/sda1", resolved)
}

func TestResolveBlockSerialSource_Match(t *testing.T) {
	prev := sysBlockPath
	sysBlockPath = fakeSysBlock(t, map[string]string{
		"vda": "sbxblk0",
		"vdb": "sbxblk1",
		"vdc": "sbxblk2",
	})
	t.Cleanup(func() { sysBlockPath = prev })

	resolved, ok, err := resolveBlockSerialSource(context.Background(), BlockSerialScheme+"sbxblk1")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "/dev/vdb", resolved)
}

func TestResolveBlockSerialSource_NotFound(t *testing.T) {
	prev := sysBlockPath
	sysBlockPath = fakeSysBlock(t, map[string]string{"vda": "sbxblk0"})
	t.Cleanup(func() { sysBlockPath = prev })

	// Cancel the context so the bounded retry returns promptly instead of
	// waiting the full grace window.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, ok, err := resolveBlockSerialSource(ctx, BlockSerialScheme+"missing")
	assert.True(t, ok)
	require.Error(t, err)
}
