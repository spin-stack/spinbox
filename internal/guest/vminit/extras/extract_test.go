//go:build linux

package extras

import (
	"archive/tar"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractFromDevice(t *testing.T) {
	ctx := context.Background()

	// Create a temp directory to use as extraction target
	targetDir := t.TempDir()

	// Create a test tar file with absolute paths
	tarPath := createTestTar(t, map[string]tarFile{
		filepath.Join(targetDir, "binary1"): {content: []byte("binary1 content"), mode: 0755},
		filepath.Join(targetDir, "config"):  {content: []byte("config content"), mode: 0644},
	})

	err := ExtractFromDevice(ctx, tarPath)
	require.NoError(t, err)

	// Verify extracted files
	content1, err := os.ReadFile(filepath.Join(targetDir, "binary1"))
	require.NoError(t, err)
	assert.Equal(t, []byte("binary1 content"), content1)

	content2, err := os.ReadFile(filepath.Join(targetDir, "config"))
	require.NoError(t, err)
	assert.Equal(t, []byte("config content"), content2)

	// Verify permissions
	fi1, err := os.Stat(filepath.Join(targetDir, "binary1"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0755), fi1.Mode().Perm())

	fi2, err := os.Stat(filepath.Join(targetDir, "config"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0644), fi2.Mode().Perm())
}

func TestExtractFromDevice_SkipsRelativePaths(t *testing.T) {
	ctx := context.Background()

	targetDir := t.TempDir()

	// Create tar with both absolute and relative paths
	// Relative paths should be skipped
	tarPath := createTestTar(t, map[string]tarFile{
		"relative/path":                      {content: []byte("should be skipped"), mode: 0644},
		"../evil":                            {content: []byte("should be skipped"), mode: 0644},
		filepath.Join(targetDir, "absolute"): {content: []byte("should be extracted"), mode: 0644},
	})

	err := ExtractFromDevice(ctx, tarPath)
	require.NoError(t, err)

	// Only absolute path should be extracted
	assert.FileExists(t, filepath.Join(targetDir, "absolute"))

	// Relative paths should NOT be extracted anywhere
	assert.NoFileExists(t, filepath.Join(targetDir, "relative", "path"))
	assert.NoFileExists(t, filepath.Join(targetDir, "evil"))
}

func TestExtractFromDevice_CreatesParentDirs(t *testing.T) {
	ctx := context.Background()

	targetDir := t.TempDir()

	// Create tar with nested path
	nestedPath := filepath.Join(targetDir, "deep", "nested", "path", "file.txt")
	tarPath := createTestTar(t, map[string]tarFile{
		nestedPath: {content: []byte("nested content"), mode: 0644},
	})

	err := ExtractFromDevice(ctx, tarPath)
	require.NoError(t, err)

	// File should exist with parent directories created
	assert.FileExists(t, nestedPath)
	content, err := os.ReadFile(nestedPath)
	require.NoError(t, err)
	assert.Equal(t, []byte("nested content"), content)
}

func TestExtractFromDevice_CleansPath(t *testing.T) {
	ctx := context.Background()

	targetDir := t.TempDir()

	// Create tar with path that needs cleaning (but is still absolute)
	dirtyPath := targetDir + "/foo/../bar/./file.txt"
	cleanPath := filepath.Join(targetDir, "bar", "file.txt")

	tarPath := createTestTar(t, map[string]tarFile{
		dirtyPath: {content: []byte("content"), mode: 0644},
	})

	err := ExtractFromDevice(ctx, tarPath)
	require.NoError(t, err)

	// File should be at the cleaned path
	assert.FileExists(t, cleanPath)
}

// Helper types and functions

type tarFile struct {
	content []byte
	mode    int64
}

func createTestTar(t *testing.T, files map[string]tarFile) string {
	t.Helper()

	tarPath := filepath.Join(t.TempDir(), "test.tar")
	f, err := os.Create(tarPath)
	require.NoError(t, err)
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	for name, file := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: file.mode,
			Size: int64(len(file.content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write(file.content)
		require.NoError(t, err)
	}

	return tarPath
}
