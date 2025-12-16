// Copyright The Beacon Authors.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package cni

import (
	"net"
	"testing"

	"github.com/containernetworking/cni/pkg/types"
	current "github.com/containernetworking/cni/pkg/types/100"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseCNIResult_Success(t *testing.T) {
	tests := []struct {
		name           string
		result         *current.Result
		expectedTAP    string
		expectedIP     string
		expectedGW     string
		expectedRoutes int
	}{
		{
			name: "valid result with TAP device",
			result: &current.Result{
				CNIVersion: "1.0.0",
				Interfaces: []*current.Interface{
					{Name: "beacon0", Mac: "aa:bb:cc:dd:ee:ff"},
					{Name: "tap123", Mac: "11:22:33:44:55:66", Sandbox: ""},
				},
				IPs: []*current.IPConfig{
					{
						Address: net.IPNet{
							IP:   net.ParseIP("10.88.0.5"),
							Mask: net.CIDRMask(16, 32),
						},
						Gateway:   net.ParseIP("10.88.0.1"),
						Interface: intPtr(1),
					},
				},
				Routes: []*types.Route{
					{Dst: net.IPNet{IP: net.ParseIP("0.0.0.0"), Mask: net.CIDRMask(0, 32)}},
				},
			},
			expectedTAP:    "tap123",
			expectedIP:     "10.88.0.5",
			expectedGW:     "10.88.0.1",
			expectedRoutes: 1,
		},
		{
			name: "result with tapXYZ device",
			result: &current.Result{
				CNIVersion: "1.0.0",
				Interfaces: []*current.Interface{
					{Name: "beacon0", Mac: "aa:bb:cc:dd:ee:ff"},
					{Name: "tapABC123", Mac: "11:22:33:44:55:66", Sandbox: ""},
				},
				IPs: []*current.IPConfig{
					{
						Address: net.IPNet{
							IP:   net.ParseIP("10.88.0.10"),
							Mask: net.CIDRMask(16, 32),
						},
						Gateway:   net.ParseIP("10.88.0.1"),
						Interface: intPtr(1),
					},
				},
			},
			expectedTAP:    "tapABC123",
			expectedIP:     "10.88.0.10",
			expectedGW:     "10.88.0.1",
			expectedRoutes: 0,
		},
		{
			name: "result with multiple IPs (uses first)",
			result: &current.Result{
				CNIVersion: "1.0.0",
				Interfaces: []*current.Interface{
					{Name: "tap456", Sandbox: ""},
				},
				IPs: []*current.IPConfig{
					{
						Address: net.IPNet{
							IP:   net.ParseIP("10.88.0.20"),
							Mask: net.CIDRMask(16, 32),
						},
						Gateway: net.ParseIP("10.88.0.1"),
					},
					{
						Address: net.IPNet{
							IP:   net.ParseIP("fd00::1"),
							Mask: net.CIDRMask(64, 128),
						},
					},
				},
			},
			expectedTAP:    "tap456",
			expectedIP:     "10.88.0.20",
			expectedGW:     "10.88.0.1",
			expectedRoutes: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseCNIResult(tt.result)
			require.NoError(t, err)
			assert.NotNil(t, result)

			assert.Equal(t, tt.expectedTAP, result.TAPDevice)
			assert.Equal(t, tt.expectedIP, result.IPAddress.String())

			if tt.expectedGW != "" {
				assert.Equal(t, tt.expectedGW, result.Gateway.String())
			}

			assert.Len(t, result.Routes, tt.expectedRoutes)
		})
	}
}

func TestParseCNIResult_NoIPs(t *testing.T) {
	result := &current.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*current.Interface{
			{Name: "tap123", Sandbox: ""},
		},
		IPs: []*current.IPConfig{},
	}

	_, err := ParseCNIResult(result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no IP addresses")
}

func TestParseCNIResult_NoTAPDevice(t *testing.T) {
	result := &current.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*current.Interface{
			{Name: "beacon0", Mac: "aa:bb:cc:dd:ee:ff"},
			{Name: "veth123", Mac: "11:22:33:44:55:66", Sandbox: "/var/run/netns/test"},
		},
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   net.ParseIP("10.88.0.5"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
	}

	_, err := ParseCNIResult(result)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no TAP device found")
}

func TestParseCNIResult_NilResult(t *testing.T) {
	_, err := ParseCNIResult(nil)
	assert.Error(t, err)
}

func TestParseCNIResult_GatewayOptional(t *testing.T) {
	result := &current.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*current.Interface{
			{Name: "tap789", Sandbox: ""},
		},
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   net.ParseIP("10.88.0.30"),
					Mask: net.CIDRMask(16, 32),
				},
				Gateway: nil, // No gateway
			},
		},
	}

	cniResult, err := ParseCNIResult(result)
	require.NoError(t, err)
	assert.Nil(t, cniResult.Gateway)
}

func TestParseCNIResult_Routes(t *testing.T) {
	result := &current.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*current.Interface{
			{Name: "tap999", Sandbox: ""},
		},
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   net.ParseIP("10.88.0.40"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
		Routes: []*types.Route{
			{
				Dst: net.IPNet{IP: net.ParseIP("0.0.0.0"), Mask: net.CIDRMask(0, 32)},
			},
			{
				Dst: net.IPNet{IP: net.ParseIP("10.88.0.0"), Mask: net.CIDRMask(16, 32)},
				GW:  net.ParseIP("10.88.0.1"),
			},
		},
	}

	cniResult, err := ParseCNIResult(result)
	require.NoError(t, err)
	assert.Len(t, cniResult.Routes, 2)

	// Verify first route (default)
	assert.Equal(t, "0.0.0.0/0", cniResult.Routes[0].Dst.String())

	// Verify second route with gateway
	assert.Equal(t, "10.88.0.0/16", cniResult.Routes[1].Dst.String())
	assert.Equal(t, "10.88.0.1", cniResult.Routes[1].GW.String())
}

func TestParseCNIResult_DNS(t *testing.T) {
	result := &current.Result{
		CNIVersion: "1.0.0",
		Interfaces: []*current.Interface{
			{Name: "tap111", Sandbox: ""},
		},
		IPs: []*current.IPConfig{
			{
				Address: net.IPNet{
					IP:   net.ParseIP("10.88.0.50"),
					Mask: net.CIDRMask(16, 32),
				},
			},
		},
		DNS: types.DNS{
			Nameservers: []string{"8.8.8.8", "8.8.4.4"},
			Domain:      "example.com",
			Search:      []string{"example.com", "local"},
		},
	}

	cniResult, err := ParseCNIResult(result)
	require.NoError(t, err)
	assert.Equal(t, 2, len(cniResult.DNS.Nameservers))
	assert.Equal(t, "8.8.8.8", cniResult.DNS.Nameservers[0])
	assert.Equal(t, "example.com", cniResult.DNS.Domain)
}

// intPtr returns a pointer to an int
func intPtr(i int) *int {
	return &i
}
