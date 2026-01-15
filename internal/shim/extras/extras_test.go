//go:build linux

package extras

import (
	"archive/tar"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewFile(t *testing.T) {
	f := NewFile("/path/to/binary", 0755)
	assert.Equal(t, "binary", f.Name)
	assert.Equal(t, "/path/to/binary", f.Path)
	assert.Equal(t, int64(0755), f.Mode)
}

func TestNewFileWithName(t *testing.T) {
	f := NewFileWithName("custom-name", "/path/to/binary", 0644)
	assert.Equal(t, "custom-name", f.Name)
	assert.Equal(t, "/path/to/binary", f.Path)
	assert.Equal(t, int64(0644), f.Mode)
}

func TestBuilder(t *testing.T) {
	b := NewBuilder()
	assert.True(t, b.IsEmpty())

	b.AddExecutable("bin1", "/path/to/bin1")
	assert.False(t, b.IsEmpty())
	assert.Len(t, b.Files(), 1)

	b.Add(NewFileWithName("config", "/path/to/config", 0644))
	assert.Len(t, b.Files(), 2)

	files := b.Files()
	assert.Equal(t, "bin1", files[0].Name)
	assert.Equal(t, int64(0755), files[0].Mode)
	assert.Equal(t, "config", files[1].Name)
	assert.Equal(t, int64(0644), files[1].Mode)
}

func TestBuilderChaining(t *testing.T) {
	b := NewBuilder().
		AddExecutable("bin1", "/path/to/bin1").
		AddExecutable("bin2", "/path/to/bin2").
		Add(NewFile("/path/to/file", 0600))

	assert.Len(t, b.Files(), 3)
}

func TestManager_GetOrCreateDisk(t *testing.T) {
	// Create temp directories
	cacheDir := t.TempDir()
	stateDir := t.TempDir()

	// Create test file
	testFile := filepath.Join(t.TempDir(), "testbin")
	testContent := []byte("test binary content")
	require.NoError(t, os.WriteFile(testFile, testContent, 0644))

	mgr := NewManager(cacheDir)

	// Test with files
	files := []File{
		NewFileWithName("testbin", testFile, 0755),
	}

	diskPath, err := mgr.GetOrCreateDisk(stateDir, files)
	require.NoError(t, err)
	assert.NotEmpty(t, diskPath)
	assert.FileExists(t, diskPath)

	// Verify tar content
	verifyTarContent(t, diskPath, map[string][]byte{
		"testbin": testContent,
	})
}

func TestManager_GetOrCreateDisk_EmptyFiles(t *testing.T) {
	cacheDir := t.TempDir()
	stateDir := t.TempDir()

	mgr := NewManager(cacheDir)

	diskPath, err := mgr.GetOrCreateDisk(stateDir, nil)
	require.NoError(t, err)
	assert.Empty(t, diskPath)

	diskPath, err = mgr.GetOrCreateDisk(stateDir, []File{})
	require.NoError(t, err)
	assert.Empty(t, diskPath)
}

func TestManager_CacheHit(t *testing.T) {
	cacheDir := t.TempDir()
	stateDir1 := t.TempDir()
	stateDir2 := t.TempDir()

	// Create test file
	testFile := filepath.Join(t.TempDir(), "testbin")
	require.NoError(t, os.WriteFile(testFile, []byte("content"), 0644))

	mgr := NewManager(cacheDir)
	files := []File{NewFileWithName("testbin", testFile, 0755)}

	// First call - cache miss
	disk1, err := mgr.GetOrCreateDisk(stateDir1, files)
	require.NoError(t, err)

	// Check cache was created
	cacheFiles, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	assert.Len(t, cacheFiles, 1)

	// Second call - cache hit
	disk2, err := mgr.GetOrCreateDisk(stateDir2, files)
	require.NoError(t, err)

	// Both disks should exist
	assert.FileExists(t, disk1)
	assert.FileExists(t, disk2)

	// Cache should still have only one file
	cacheFiles, err = os.ReadDir(cacheDir)
	require.NoError(t, err)
	assert.Len(t, cacheFiles, 1)

	// Verify they have the same content
	content1, err := os.ReadFile(disk1)
	require.NoError(t, err)
	content2, err := os.ReadFile(disk2)
	require.NoError(t, err)
	assert.Equal(t, content1, content2)
}

func TestManager_DifferentContentDifferentCache(t *testing.T) {
	cacheDir := t.TempDir()
	stateDir1 := t.TempDir()
	stateDir2 := t.TempDir()

	// Create two different test files
	testFile1 := filepath.Join(t.TempDir(), "testbin1")
	require.NoError(t, os.WriteFile(testFile1, []byte("content1"), 0644))

	testFile2 := filepath.Join(t.TempDir(), "testbin2")
	require.NoError(t, os.WriteFile(testFile2, []byte("content2"), 0644))

	mgr := NewManager(cacheDir)

	// First disk with file1
	files1 := []File{NewFileWithName("testbin", testFile1, 0755)}
	_, err := mgr.GetOrCreateDisk(stateDir1, files1)
	require.NoError(t, err)

	// Second disk with file2 (different content)
	files2 := []File{NewFileWithName("testbin", testFile2, 0755)}
	_, err = mgr.GetOrCreateDisk(stateDir2, files2)
	require.NoError(t, err)

	// Cache should have two files (different hashes)
	cacheFiles, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	assert.Len(t, cacheFiles, 2)
}

func TestManager_MultipleFiles(t *testing.T) {
	cacheDir := t.TempDir()
	stateDir := t.TempDir()
	srcDir := t.TempDir()

	// Create multiple test files
	files := []File{
		createTestFile(t, srcDir, "binary1", []byte("binary1 content"), 0755),
		createTestFile(t, srcDir, "binary2", []byte("binary2 content"), 0755),
		createTestFile(t, srcDir, "config.txt", []byte("config content"), 0644),
	}

	mgr := NewManager(cacheDir)
	diskPath, err := mgr.GetOrCreateDisk(stateDir, files)
	require.NoError(t, err)

	// Verify tar contains all files
	verifyTarContent(t, diskPath, map[string][]byte{
		"binary1":    []byte("binary1 content"),
		"binary2":    []byte("binary2 content"),
		"config.txt": []byte("config content"),
	})
}

func TestManager_HashDeterminism(t *testing.T) {
	cacheDir := t.TempDir()
	srcDir := t.TempDir()

	// Create test files
	file1 := createTestFile(t, srcDir, "aaa", []byte("content aaa"), 0755)
	file2 := createTestFile(t, srcDir, "bbb", []byte("content bbb"), 0755)

	mgr := NewManager(cacheDir)

	// Order 1: aaa, bbb
	hash1, err := mgr.computeHash([]File{file1, file2})
	require.NoError(t, err)

	// Order 2: bbb, aaa (should produce same hash due to sorting)
	hash2, err := mgr.computeHash([]File{file2, file1})
	require.NoError(t, err)

	assert.Equal(t, hash1, hash2, "hash should be deterministic regardless of input order")
}

// Helper functions

func createTestFile(t *testing.T, dir, name string, content []byte, mode int64) File {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, content, os.FileMode(mode)))
	return NewFileWithName(name, path, mode)
}

func verifyTarContent(t *testing.T, tarPath string, expected map[string][]byte) {
	t.Helper()

	f, err := os.Open(tarPath)
	require.NoError(t, err)
	defer f.Close()

	tr := tar.NewReader(f)
	found := make(map[string]bool)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)

		expectedContent, ok := expected[hdr.Name]
		require.True(t, ok, "unexpected file in tar: %s", hdr.Name)

		content, err := io.ReadAll(tr)
		require.NoError(t, err)
		assert.Equal(t, expectedContent, content, "content mismatch for %s", hdr.Name)

		found[hdr.Name] = true
	}

	for name := range expected {
		assert.True(t, found[name], "expected file not found in tar: %s", name)
	}
}
