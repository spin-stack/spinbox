package ipallocator

import (
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aledbf/beacon/containerd/internal/boltstore"
	"github.com/google/go-cmp/cmp"
)

func mustParseCIDR(t *testing.T, cidr string) *net.IPNet {
	t.Helper()
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("failed to parse CIDR %q: %v", cidr, err)
	}
	return ipnet
}

func TestNewIPBitmap(t *testing.T) {
	tests := []struct {
		name    string
		cidr    string
		wantErr bool
	}{
		{
			name:    "valid /30 network",
			cidr:    "192.168.0.0/30", // /30 has 4 addresses, needs at least 4 => should be OK
			wantErr: false,
		},
		{
			name:    "subnet too small /31",
			cidr:    "192.168.0.0/31", // /31 has 2 addresses, not enough for usable range
			wantErr: true,
		},
		{
			name:    "subnet too small /32",
			cidr:    "192.168.0.0/32", // single address
			wantErr: true,
		},
		{
			name:    "valid /29 network",
			cidr:    "10.0.0.0/29",
			wantErr: false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, network, err := net.ParseCIDR(tt.cidr)
			if err != nil {
				t.Fatalf("failed to parse CIDR: %v", err)
			}

			bitmap, err := newIPBitmap(network)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			} else if !tt.wantErr && err != nil {
				t.Errorf("unexpected error, got %v", err)
			}
			// If no error is expected, ensure we got a valid bitmap
			if !tt.wantErr && bitmap == nil {
				t.Errorf("expected a valid bitmap, got nil")
			}
		})
	}
}

func TestIPBitmap_MarkIPUsedAndFree(t *testing.T) {
	cidr := "192.168.0.0/29" // 8 addresses total, 6 usable if ignoring net/broadcast
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatalf("failed to parse CIDR: %v", err)
	}
	bitmap, err := newIPBitmap(network)
	if err != nil {
		t.Fatalf("failed to create IPBitmap: %v", err)
	}

	ip := net.ParseIP("192.168.0.2")
	if err := bitmap.MarkIPUsed(ip); err != nil {
		t.Fatalf("MarkIPUsed(%v) failed: %v", ip, err)
	}

	// MarkIPUsed again should succeed but not fail, ideally it sets the same bit again
	if err := bitmap.MarkIPUsed(ip); err != nil {
		t.Errorf("MarkIPUsed(%v) second time should not fail, got %v", ip, err)
	}

	// MarkIPFree
	if err := bitmap.MarkIPFree(ip); err != nil {
		t.Fatalf("MarkIPFree(%v) failed: %v", ip, err)
	}
}

func TestIPBitmap_FindFirstFreeIP(t *testing.T) {
	cidr := "192.168.1.0/29" // 8 addresses
	ipnet := mustParseCIDR(t, cidr)

	bitmap, err := newIPBitmap(ipnet)
	if err != nil {
		t.Fatalf("failed to create IPBitmap: %v", err)
	}

	// Reserve the first few usable addresses (192.168.1.1, 192.168.1.2) manually
	// net: 192.168.1.0, broadcast: 192.168.1.7
	addrsToUse := []string{"192.168.1.1", "192.168.1.2"}
	for _, addr := range addrsToUse {
		ip := net.ParseIP(addr)
		if err := bitmap.MarkIPUsed(ip); err != nil {
			t.Fatalf("failed to mark %v used: %v", ip, err)
		}
	}

	freeIP, err := bitmap.FindFirstFreeIP(ipnet)
	if err != nil {
		t.Fatalf("FindFirstFreeIP() returned error: %v", err)
	}
	// The next free IP should be 192.168.1.3 (since .0 is network, .1 and .2 are used, .7 is broadcast)
	want := net.ParseIP("192.168.1.3")
	if diff := cmp.Diff(want.String(), freeIP.String()); diff != "" {
		t.Errorf("unexpected IP (-want +got):\n%s", diff)
	}
}

func TestNewIPAllocator(t *testing.T) {
	tests := []struct {
		name    string
		subnet  string
		wantErr bool
	}{
		{
			name:    "valid /29 subnet",
			subnet:  "10.0.0.0/29",
			wantErr: false,
		},
		{
			name:    "invalid subnet format",
			subnet:  "invalid-subnet",
			wantErr: true,
		},
		{
			name:    "too small /31",
			subnet:  "192.168.0.0/31",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			store := boltstore.NewInMemoryStore[IPAllocation]()
			allocator, err := NewBitmapIPAllocator(store, Config{Subnet: tt.subnet})
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got none")
			} else if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tt.wantErr && allocator == nil {
				t.Errorf("expected valid allocator, got nil")
			}
		})
	}
}

func TestIPAllocator_AllocateIP(t *testing.T) {
	store := boltstore.NewInMemoryStore[IPAllocation]()
	allocator, err := NewBitmapIPAllocator(store, Config{Subnet: "192.168.10.0/29"})
	if err != nil {
		t.Fatalf("failed to create IPAllocator: %v", err)
	}

	tests := []struct {
		name    string
		envID   string
		wantErr bool
	}{
		{
			name:    "normal allocation #1",
			envID:   "env1",
			wantErr: false,
		},
		{
			name:    "normal allocation #2",
			envID:   "env2",
			wantErr: false,
		},
		{
			name:    "empty env ID (still can allocate)",
			envID:   "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			ip, mask, gateway, err := allocator.AllocateIP(t.Context(), tt.envID)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			} else if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if err == nil {
				// Verify we got valid return values
				if ip == nil {
					t.Error("expected IP, got nil")
				}
				if mask == "" {
					t.Error("expected mask, got nil")
				}
				if gateway == nil {
					t.Error("expected gateway, got nil")
				}

				// Verify gateway is first IP in subnet
				if !gateway.Equal(net.ParseIP("192.168.10.1")) {
					t.Errorf("expected gateway 192.168.10.0, got %v", gateway)
				}

				// Verify mask matches the /29 subnet
				expectedMaskStr := "255.255.255.248" // /29 in decimal
				if mask != expectedMaskStr {
					t.Errorf("expected mask %s, got %s", expectedMaskStr, mask)
				}
			}
		})
	}
}

func TestIPAllocator_ReleaseIP(t *testing.T) {
	store := boltstore.NewInMemoryStore[IPAllocation]()
	allocator, err := NewBitmapIPAllocator(store, Config{Subnet: "10.10.0.0/29"})
	if err != nil {
		t.Fatalf("failed to create IPAllocator: %v", err)
	}

	ip, _, _, err := allocator.AllocateIP(t.Context(), "env1")
	if err != nil {
		t.Fatalf("failed to allocate IP: %v", err)
	}

	tests := []struct {
		name    string
		ip      string
		wantErr bool
	}{
		{
			name:    "release valid IP",
			ip:      ip.String(),
			wantErr: false,
		},
		{
			name:    "release unallocated IP (should not fail hard)",
			ip:      "10.10.0.200",
			wantErr: false,
		},
		{
			name:    "release invalid IP",
			ip:      "invalid-ip",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := allocator.ReleaseIP(t.Context(), tt.ip)
			if tt.wantErr && err == nil {
				t.Errorf("expected error, got none")
			} else if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestIPAllocator_BatchProcess_Release(t *testing.T) {
	store := boltstore.NewInMemoryStore[IPAllocation]()
	allocator, err := NewBitmapIPAllocator(store, Config{Subnet: "192.168.60.0/29"})
	if err != nil {
		t.Fatalf("failed to create IPAllocator: %v", err)
	}

	// Allocate a couple IPs first
	ip1, _, _, err := allocator.AllocateIP(t.Context(), "env1")
	if err != nil {
		t.Fatalf("failed to allocate IP for env1: %v", err)
	}
	ip2, _, _, err := allocator.AllocateIP(t.Context(), "env2")
	if err != nil {
		t.Fatalf("failed to allocate IP for env2: %v", err)
	}

	t.Run("release by IP list", func(t *testing.T) {
		req := BatchRequest{
			Operation: BatchRelease,
			IPs:       []net.IP{ip1, ip2},
			Timeout:   2 * time.Second,
		}
		res, err := allocator.BatchProcess(t.Context(), req)
		if err != nil {
			t.Fatalf("BatchProcess release failed: %v", err)
		}

		if len(res.Success) != 2 {
			t.Errorf("expected 2 successful releases, got %d", len(res.Success))
		}
		if len(res.Failures) != 0 {
			t.Errorf("expected 0 failures, got %d", len(res.Failures))
		}
	})

	t.Run("release by env ID when none left", func(t *testing.T) {
		req := BatchRequest{
			Operation: BatchRelease,
			HostIDs:   []string{"env1", "env2"},
			Timeout:   2 * time.Second,
		}
		res, err := allocator.BatchProcess(t.Context(), req)
		if err != nil {
			t.Fatalf("BatchProcess release by envID failed: %v", err)
		}

		// They should have no allocated IPs at this point, so no actual releases
		if len(res.Success) != 0 {
			t.Errorf("expected 0 successes, got %d", len(res.Success))
		}
		if len(res.Failures) != 0 {
			t.Errorf("expected 0 failures, got %d", len(res.Failures))
		}
	})
}

func TestIPAllocator_IsExhausted(t *testing.T) {
	store := boltstore.NewInMemoryStore[IPAllocation]()
	// Using a /29 network which gives us 8 addresses:
	// x.x.x.0 (network)
	// x.x.x.1 (gateway - reserved)
	// x.x.x.2 through x.x.x.6 (usable)
	// x.x.x.7 (broadcast)
	allocator, err := NewBitmapIPAllocator(store, Config{Subnet: "10.0.0.0/29"})
	if err != nil {
		t.Fatalf("failed to create IPAllocator: %v", err)
	}

	if allocator.IsExhausted(t.Context()) {
		t.Errorf("should not be exhausted at the start")
	}

	// We should be able to allocate 5 IPs (from .2 to .6)
	for i := 1; i <= 5; i++ {
		_, _, _, err = allocator.AllocateIP(t.Context(), fmt.Sprintf("env%d", i))
		if err != nil {
			t.Fatalf("failed to allocate IP #%d: %v", i, err)
		}
	}

	// At this point we should be exhausted
	if !allocator.IsExhausted(t.Context()) {
		t.Errorf("expected the allocator to be exhausted after 5 allocations")
	}

	// One more allocation should fail
	//nolint:dogsled
	_, _, _, err = allocator.AllocateIP(t.Context(), "env6")
	if err == nil {
		t.Error("expected allocation to fail when exhausted")
	}
}

func TestIPAllocator_BatchProcess_ErrorsAndTimeout(t *testing.T) {
	store := boltstore.NewInMemoryStore[IPAllocation]()
	allocator, err := NewBitmapIPAllocator(store, Config{Subnet: "192.168.70.0/30"})
	if err != nil {
		t.Fatalf("failed to create IPAllocator: %v", err)
	}

	t.Run("empty request", func(t *testing.T) {
		req := BatchRequest{}
		_, err := allocator.BatchProcess(t.Context(), req)
		if err == nil {
			t.Errorf("expected error for empty IDs and IPs, got none")
		}
	})

	t.Run("unknown operation", func(t *testing.T) {
		req := BatchRequest{
			Operation: 999, // invalid
			HostIDs:   []string{"env1"},
		}
		_, err := allocator.BatchProcess(t.Context(), req)
		if err == nil {
			t.Errorf("expected error for unknown batch operation, got none")
		}
	})
}
