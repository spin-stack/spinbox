package vsock

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNewAllocator(t *testing.T) {
	tests := []struct {
		name     string
		minCID   uint32
		maxCID   uint32
		cooldown time.Duration
	}{
		{
			name:     "standard range",
			minCID:   100,
			maxCID:   200,
			cooldown: time.Second,
		},
		{
			name:     "single CID",
			minCID:   50,
			maxCID:   50,
			cooldown: 0,
		},
		{
			name:     "large range with cooldown",
			minCID:   3,
			maxCID:   1000,
			cooldown: 5 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockDir := t.TempDir()
			alloc := NewAllocator(lockDir, tt.minCID, tt.maxCID, tt.cooldown)

			if alloc.lockDir != lockDir {
				t.Errorf("lockDir = %q, want %q", alloc.lockDir, lockDir)
			}
			if alloc.minCID != tt.minCID {
				t.Errorf("minCID = %d, want %d", alloc.minCID, tt.minCID)
			}
			if alloc.maxCID != tt.maxCID {
				t.Errorf("maxCID = %d, want %d", alloc.maxCID, tt.maxCID)
			}
			if alloc.cooldown != tt.cooldown {
				t.Errorf("cooldown = %v, want %v", alloc.cooldown, tt.cooldown)
			}
		})
	}
}

func TestAllocator_Allocate_SingleCID(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 10, 20, 0)

	lease, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	defer lease.Release()

	if lease.CID < 10 || lease.CID > 20 {
		t.Errorf("CID = %d, want in range [10, 20]", lease.CID)
	}
	if lease.file == nil {
		t.Error("file is nil")
	}
	if lease.path == "" {
		t.Error("path is empty")
	}

	// Verify lock file exists
	lockPath := filepath.Join(lockDir, "10.lock")
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Errorf("lock file %q does not exist", lockPath)
	}
}

func TestAllocator_Allocate_MultipleCIDs(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 10, 15, 0)

	var leases []*Lease
	defer func() {
		for _, l := range leases {
			l.Release()
		}
	}()

	// Allocate all available CIDs
	for i := range 6 {
		lease, err := alloc.Allocate()
		if err != nil {
			t.Fatalf("Allocate() iteration %d error = %v", i, err)
		}
		leases = append(leases, lease)
	}

	// Verify all CIDs are unique
	seen := make(map[uint32]bool)
	for _, l := range leases {
		if seen[l.CID] {
			t.Errorf("duplicate CID allocated: %d", l.CID)
		}
		seen[l.CID] = true
	}

	// Verify we used all CIDs in range
	if len(seen) != 6 {
		t.Errorf("allocated %d unique CIDs, want 6", len(seen))
	}
}

func TestAllocator_Allocate_Exhaustion(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 10, 12, 0)

	var leases []*Lease
	defer func() {
		for _, l := range leases {
			l.Release()
		}
	}()

	// Allocate all CIDs
	for i := range 3 {
		lease, err := alloc.Allocate()
		if err != nil {
			t.Fatalf("Allocate() iteration %d error = %v", i, err)
		}
		leases = append(leases, lease)
	}

	// Next allocation should fail
	_, err := alloc.Allocate()
	if err == nil {
		t.Error("Allocate() should fail when CIDs exhausted")
	}
}

func TestAllocator_Allocate_ReleaseAndReuse(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 10, 10, 0) // Only one CID available

	// First allocation
	lease1, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("first Allocate() error = %v", err)
	}
	cid1 := lease1.CID

	// Release
	if err := lease1.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// Should be able to allocate again
	lease2, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("second Allocate() error = %v", err)
	}
	defer lease2.Release()

	if lease2.CID != cid1 {
		t.Errorf("reused CID = %d, want %d", lease2.CID, cid1)
	}
}

func TestAllocator_Allocate_Cooldown(t *testing.T) {
	lockDir := t.TempDir()
	cooldown := 100 * time.Millisecond

	// First allocation with NO cooldown (to create the lock file without triggering cooldown on new file modtime)
	allocNoCooldown := NewAllocator(lockDir, 10, 10, 0)
	lease1, err := allocNoCooldown.Allocate()
	if err != nil {
		t.Fatalf("first Allocate() error = %v", err)
	}
	if err := lease1.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// Now use allocator with cooldown - immediate reallocation should fail
	allocWithCooldown := NewAllocator(lockDir, 10, 10, cooldown)
	_, err = allocWithCooldown.Allocate()
	if err == nil {
		t.Error("Allocate() should fail during cooldown")
	}

	// Wait for cooldown
	time.Sleep(cooldown + 20*time.Millisecond)

	// Now allocation should succeed
	lease2, err := allocWithCooldown.Allocate()
	if err != nil {
		t.Fatalf("Allocate() after cooldown error = %v", err)
	}
	defer lease2.Release()
}

func TestAllocator_Allocate_CreatesLockDir(t *testing.T) {
	baseDir := t.TempDir()
	lockDir := filepath.Join(baseDir, "nested", "lock", "dir")
	alloc := NewAllocator(lockDir, 10, 10, 0)

	lease, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	defer lease.Release()

	// Verify directory was created
	info, err := os.Stat(lockDir)
	if err != nil {
		t.Fatalf("lock directory not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("lockDir is not a directory")
	}
}

func TestLease_Release_Nil(t *testing.T) {
	tests := []struct {
		name  string
		lease *Lease
	}{
		{
			name:  "nil lease",
			lease: nil,
		},
		{
			name:  "lease with nil file",
			lease: &Lease{},
		},
		{
			name:  "lease with empty path",
			lease: &Lease{path: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.lease.Release(); err != nil {
				t.Errorf("Release() error = %v", err)
			}
		})
	}
}

func TestLease_Release_UpdatesMetadata(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 10, 10, 0)

	lease, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}

	lockPath := lease.path
	beforeRelease := time.Now()

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}

	// Read the metadata file
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var meta cidMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if meta.ReleasedAt == nil {
		t.Error("ReleasedAt is nil after release")
	} else if meta.ReleasedAt.Before(beforeRelease) {
		t.Errorf("ReleasedAt = %v, want >= %v", *meta.ReleasedAt, beforeRelease)
	}
}

func TestIsCoolingDown(t *testing.T) {
	now := time.Now()
	cooldown := time.Second

	tests := []struct {
		name     string
		meta     cidMetadata
		cooldown time.Duration
		want     bool
	}{
		{
			name:     "zero cooldown",
			meta:     cidMetadata{},
			cooldown: 0,
			want:     false,
		},
		{
			name:     "no release time, no alloc time - immediately available",
			meta:     cidMetadata{},
			cooldown: cooldown,
			want:     false,
		},
		{
			name: "released recently",
			meta: cidMetadata{
				ReleasedAt: func() *time.Time { t := now.Add(-500 * time.Millisecond); return &t }(),
			},
			cooldown: cooldown,
			want:     true,
		},
		{
			name: "released long ago",
			meta: cidMetadata{
				ReleasedAt: func() *time.Time { t := now.Add(-2 * time.Second); return &t }(),
			},
			cooldown: cooldown,
			want:     false,
		},
		{
			name: "allocated recently, no release (crash recovery)",
			meta: cidMetadata{
				AllocatedAt: now.Add(-500 * time.Millisecond),
			},
			cooldown: cooldown,
			want:     true,
		},
		{
			name: "allocated long ago, no release (crash recovery)",
			meta: cidMetadata{
				AllocatedAt: now.Add(-2 * time.Second),
			},
			cooldown: cooldown,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCoolingDown(now, tt.meta, tt.cooldown)
			if got != tt.want {
				t.Errorf("isCoolingDown() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadWriteMetadata(t *testing.T) {
	tmpFile, err := os.CreateTemp(t.TempDir(), "meta-*.json")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	defer tmpFile.Close()

	now := time.Now().Truncate(time.Second) // Truncate for JSON round-trip
	releasedAt := now.Add(-time.Minute)

	original := cidMetadata{
		PID:         12345,
		AllocatedAt: now,
		ReleasedAt:  &releasedAt,
	}

	if err := writeMetadata(tmpFile, original); err != nil {
		t.Fatalf("writeMetadata() error = %v", err)
	}

	got := readMetadata(tmpFile)

	if got.PID != original.PID {
		t.Errorf("PID = %d, want %d", got.PID, original.PID)
	}
	if !got.AllocatedAt.Equal(original.AllocatedAt) {
		t.Errorf("AllocatedAt = %v, want %v", got.AllocatedAt, original.AllocatedAt)
	}
	if got.ReleasedAt == nil {
		t.Error("ReleasedAt is nil")
	} else if !got.ReleasedAt.Equal(*original.ReleasedAt) {
		t.Errorf("ReleasedAt = %v, want %v", *got.ReleasedAt, *original.ReleasedAt)
	}
}

func TestReadMetadata_EdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantPID int
	}{
		{
			name:    "empty file",
			content: "",
			wantPID: 0,
		},
		{
			name:    "invalid JSON",
			content: "not valid json",
			wantPID: 0,
		},
		{
			name:    "partial JSON",
			content: `{"pid": 123`,
			wantPID: 0,
		},
		{
			name:    "empty JSON object",
			content: `{}`,
			wantPID: 0,
		},
		{
			name:    "null values",
			content: `{"pid": null}`,
			wantPID: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpFile, err := os.CreateTemp(t.TempDir(), "meta-*.json")
			if err != nil {
				t.Fatalf("CreateTemp() error = %v", err)
			}
			defer tmpFile.Close()

			if tt.content != "" {
				if _, err := tmpFile.WriteString(tt.content); err != nil {
					t.Fatalf("WriteString() error = %v", err)
				}
			}

			got := readMetadata(tmpFile)

			if got.PID != tt.wantPID {
				t.Errorf("PID = %d, want %d", got.PID, tt.wantPID)
			}
			if !got.AllocatedAt.IsZero() {
				t.Errorf("AllocatedAt = %v, want zero", got.AllocatedAt)
			}
			if got.ReleasedAt != nil {
				t.Errorf("ReleasedAt = %v, want nil", got.ReleasedAt)
			}
		})
	}
}

func TestAllocator_Concurrent(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 100, 110, 0)

	const goroutines = 5
	var wg sync.WaitGroup
	results := make(chan *Lease, goroutines)
	errors := make(chan error, goroutines)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, err := alloc.Allocate()
			if err != nil {
				errors <- err
				return
			}
			results <- lease
		}()
	}

	wg.Wait()
	close(results)
	close(errors)

	// Check for errors
	for err := range errors {
		t.Errorf("concurrent Allocate() error = %v", err)
	}

	// Collect and verify leases
	var leases []*Lease
	seen := make(map[uint32]bool)
	for lease := range results {
		if seen[lease.CID] {
			t.Errorf("duplicate CID allocated concurrently: %d", lease.CID)
		}
		seen[lease.CID] = true
		leases = append(leases, lease)
	}

	// Release all leases
	for _, l := range leases {
		l.Release()
	}

	if len(leases) != goroutines {
		t.Errorf("got %d leases, want %d", len(leases), goroutines)
	}
}

func TestAllocator_LockExclusion(t *testing.T) {
	lockDir := t.TempDir()
	alloc := NewAllocator(lockDir, 50, 50, 0) // Single CID

	// First allocation
	lease1, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("first Allocate() error = %v", err)
	}

	// Second allocation should fail (CID is locked)
	_, err = alloc.Allocate()
	if err == nil {
		t.Error("second Allocate() should fail while CID is locked")
	}

	// After release, should work
	lease1.Release()

	lease2, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("Allocate() after release error = %v", err)
	}
	defer lease2.Release()
}

func TestAllocator_SkipsLockedCIDs(t *testing.T) {
	lockDir := t.TempDir()

	// Pre-lock CID 10 using raw file lock
	lockPath := filepath.Join(lockDir, "10.lock")
	if err := os.MkdirAll(lockDir, 0750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	preLockedFile, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if err := syscall.Flock(int(preLockedFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("Flock() error = %v", err)
	}
	defer func() {
		syscall.Flock(int(preLockedFile.Fd()), syscall.LOCK_UN)
		preLockedFile.Close()
	}()

	// Allocator should skip CID 10 and allocate CID 11
	alloc := NewAllocator(lockDir, 10, 15, 0)
	lease, err := alloc.Allocate()
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	defer lease.Release()

	if lease.CID == 10 {
		t.Errorf("CID = 10, should have skipped locked CID")
	}
	if lease.CID != 11 {
		t.Errorf("CID = %d, want 11 (first available after locked 10)", lease.CID)
	}
}
