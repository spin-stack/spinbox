//go:build linux

package extras

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// DefaultCacheDir is the default location for cached extras disks.
const DefaultCacheDir = "/var/cache/spin-stack/extras"

// Manager handles extras disk creation with content-based caching.
// It creates tar archives and caches them by content hash.
type Manager struct {
	cacheDir string
}

// NewManager creates a new Manager.
// If cacheDir is empty, DefaultCacheDir is used.
func NewManager(cacheDir string) *Manager {
	if cacheDir == "" {
		cacheDir = DefaultCacheDir
	}
	return &Manager{cacheDir: cacheDir}
}

// GetOrCreateDisk creates (or retrieves from cache) an extras disk.
// Returns empty string if no files provided.
// The returned path is in stateDir and will be cleaned up with the container.
func (m *Manager) GetOrCreateDisk(stateDir string, files []File) (string, error) {
	if len(files) == 0 {
		return "", nil
	}

	hash, err := m.computeHash(files)
	if err != nil {
		return "", fmt.Errorf("compute hash: %w", err)
	}

	if err := os.MkdirAll(m.cacheDir, 0755); err != nil {
		return "", fmt.Errorf("create cache dir: %w", err)
	}

	cachePath := filepath.Join(m.cacheDir, hash+".tar")
	stateDiskPath := filepath.Join(stateDir, "extras.tar")

	// Cache hit - hard link to state dir
	if _, err := os.Stat(cachePath); err == nil {
		if err := os.Link(cachePath, stateDiskPath); err != nil {
			// Fallback to copy if hard link fails (cross-device)
			if err := copyFile(cachePath, stateDiskPath); err != nil {
				return "", fmt.Errorf("copy cached disk: %w", err)
			}
		}
		return stateDiskPath, nil
	}

	// Cache miss - create and cache
	if err := m.createTar(cachePath, files); err != nil {
		return "", fmt.Errorf("create tar: %w", err)
	}

	if err := os.Link(cachePath, stateDiskPath); err != nil {
		// Fallback to copy if hard link fails
		if err := copyFile(cachePath, stateDiskPath); err != nil {
			return "", fmt.Errorf("link cached disk: %w", err)
		}
	}

	return stateDiskPath, nil
}

// computeHash computes a deterministic SHA256 hash of all files.
// Files are sorted by name to ensure consistent ordering.
func (m *Manager) computeHash(files []File) (string, error) {
	// Sort files by name for deterministic hashing
	sorted := make([]File, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	h := sha256.New()
	for _, f := range sorted {
		// Hash: name + mode + file content
		h.Write([]byte(f.Name))
		_, _ = fmt.Fprintf(h, "%d", f.Mode) // hash.Write never returns an error

		file, err := os.Open(f.Path)
		if err != nil {
			return "", fmt.Errorf("open %s: %w", f.Path, err)
		}
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", fmt.Errorf("hash %s: %w", f.Path, err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// createTar creates a tar archive at path containing all files.
func (m *Manager) createTar(path string, files []File) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	tw := tar.NewWriter(f)
	defer tw.Close()

	for _, file := range files {
		fi, err := os.Stat(file.Path)
		if err != nil {
			return fmt.Errorf("stat %s: %w", file.Path, err)
		}

		hdr := &tar.Header{
			Name: file.Name,
			Mode: file.Mode,
			Size: fi.Size(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write header for %s: %w", file.Name, err)
		}

		src, err := os.Open(file.Path)
		if err != nil {
			return fmt.Errorf("open %s: %w", file.Path, err)
		}
		_, err = io.Copy(tw, src)
		src.Close()
		if err != nil {
			return fmt.Errorf("copy %s: %w", file.Name, err)
		}
	}

	return nil
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
