package vsock

import "testing"

func TestConstants(t *testing.T) {
	// GuestCID must be >= 3 (0=hypervisor, 1=reserved, 2=host)
	if GuestCID < 3 {
		t.Errorf("GuestCID = %d, want >= 3", GuestCID)
	}

	// Ports must be > 1024 (privileged ports)
	if DefaultRPCPort <= 1024 {
		t.Errorf("DefaultRPCPort = %d, want > 1024", DefaultRPCPort)
	}

	if DefaultStreamPort <= 1024 {
		t.Errorf("DefaultStreamPort = %d, want > 1024", DefaultStreamPort)
	}

	// Ports must be different
	if DefaultRPCPort == DefaultStreamPort {
		t.Errorf("DefaultRPCPort and DefaultStreamPort are the same: %d", DefaultRPCPort)
	}
}

func TestConstants_Values(t *testing.T) {
	// Document expected values
	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"GuestCID", GuestCID, 3},
		{"DefaultRPCPort", DefaultRPCPort, 1025},
		{"DefaultStreamPort", DefaultStreamPort, 1026},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}
