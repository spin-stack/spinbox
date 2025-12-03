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
	path, err := findCloudHypervisor()
	if err != nil {
		t.Skipf("cloud-hypervisor not found (expected if not installed): %v", err)
		return
	}

	if path == "" {
		t.Fatal("findCloudHypervisor returned empty path without error")
	}

	t.Logf("Found cloud-hypervisor at: %s", path)
}

func TestFindKernel(t *testing.T) {
	path, err := findKernel()
	if err != nil {
		t.Skipf("No kernel found (expected if not installed): %v", err)
		return
	}

	if path == "" {
		t.Fatal("findKernel returned empty path without error")
	}

	t.Logf("Found kernel at: %s", path)
}

func TestFindInitrd(t *testing.T) {
	path, err := findInitrd()
	if err != nil {
		t.Skipf("No initrd found (expected if not installed): %v", err)
		return
	}

	if path == "" {
		t.Fatal("findInitrd returned empty path without error")
	}

	t.Logf("Found initrd at: %s", path)
}

func TestNewInstance(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	instance, err := NewInstance(ctx, tmpDir, nil)
	if err != nil {
		t.Skipf("NewInstance failed (expected without cloud-hypervisor or kernel/initrd): %v", err)
		return
	}

	if instance == nil {
		t.Fatal("instance should not be nil when err is nil")
	}

	// Verify instance was configured correctly
	if instance.stateDir != tmpDir {
		t.Errorf("stateDir = %q, want %q", instance.stateDir, tmpDir)
	}

	// Verify resource defaults were applied
	if instance.resourceCfg == nil {
		t.Fatal("resourceCfg should not be nil")
	}
	if instance.resourceCfg.BootCPUs != defaultBootCPUs {
		t.Errorf("BootCPUs = %d, want %d", instance.resourceCfg.BootCPUs, defaultBootCPUs)
	}
}
