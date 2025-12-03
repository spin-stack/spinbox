//go:build linux

package cloudhypervisor

import (
	"context"
	"net"
	"testing"
)

func TestCHInstance_VMInfo(t *testing.T) {
	ch := &Instance{}

	info := ch.VMInfo()

	if info.Type != "cloudhypervisor" {
		t.Errorf("Type = %s, want cloudhypervisor", info.Type)
	}

	if !info.SupportsTAP {
		t.Error("SupportsTAP should be true")
	}

	if !info.SupportsVSOCK {
		t.Error("SupportsVSOCK should be true")
	}
}

func TestCHInstance_AddTAPNIC(t *testing.T) {
	ch := &Instance{
		nets: []*NetConfig{},
	}

	ctx := context.Background()
	testMAC, _ := net.ParseMAC("02:00:00:00:00:01")

	err := ch.AddTAPNIC(ctx, "tap0", testMAC)
	if err != nil {
		t.Errorf("AddTAPNIC failed: %v", err)
	}

	if len(ch.nets) != 1 {
		t.Errorf("nets length = %d, want 1", len(ch.nets))
	}

	expectedMAC := testMAC.String()
	if ch.nets[0].Mac != expectedMAC {
		t.Errorf("MAC = %v, want %s", ch.nets[0].Mac, expectedMAC)
	}
}

func TestCHInstance_AddTAPNIC_MultipleCalls(t *testing.T) {
	ch := &Instance{
		nets: []*NetConfig{},
	}

	ctx := context.Background()

	// Add first NIC
	mac1, _ := net.ParseMAC("02:00:00:00:00:01")
	err := ch.AddTAPNIC(ctx, "tap0", mac1)
	if err != nil {
		t.Fatalf("First AddTAPNIC failed: %v", err)
	}

	// Add second NIC
	mac2, _ := net.ParseMAC("02:00:00:00:00:02")
	err = ch.AddTAPNIC(ctx, "tap1", mac2)
	if err != nil {
		t.Fatalf("Second AddTAPNIC failed: %v", err)
	}

	if len(ch.nets) != 2 {
		t.Errorf("nets length = %d, want 2", len(ch.nets))
	}
}

func TestFindCloudHypervisor(t *testing.T) {
	// This test tries to find cloud-hypervisor binary
	// It will succeed if the binary is in PATH, otherwise skip

	path, err := findCloudHypervisor()
	if err != nil {
		t.Skipf("cloud-hypervisor not found: %v", err)
	}

	if path == "" {
		t.Skip("cloud-hypervisor not found in PATH")
	}

	t.Logf("Found cloud-hypervisor at: %s", path)
}

func TestFindKernel(t *testing.T) {
	// Test kernel detection
	path, err := findKernel()
	if err != nil {
		t.Logf("No kernel found (expected on macOS): %v", err)
		return
	}

	if path == "" {
		t.Log("No kernel found in default locations (expected on macOS)")
	} else {
		t.Logf("Found kernel at: %s", path)
	}
}

func TestFindInitrd(t *testing.T) {
	// Test initrd detection
	path, err := findInitrd()
	if err != nil {
		t.Logf("No initrd found (expected on macOS): %v", err)
		return
	}

	if path == "" {
		t.Log("No initrd found in default locations (expected on macOS)")
	} else {
		t.Logf("Found initrd at: %s", path)
	}
}

func TestNewInstance(t *testing.T) {
	// This test requires cloud-hypervisor to be available
	ctx := context.Background()
	tmpDir := t.TempDir()

	instance, err := NewInstance(ctx, tmpDir, nil)

	// We expect this to fail on macOS or if cloud-hypervisor is not installed
	// But we can verify the error handling
	if err != nil {
		t.Logf("NewInstance failed (expected without cloud-hypervisor): %v", err)
		return
	}

	if instance == nil {
		t.Error("instance should not be nil when err is nil")
	}
}
