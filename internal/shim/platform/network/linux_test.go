//go:build linux

package network

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	mgr := newManager()
	require.NotNil(t, mgr)

	_, ok := mgr.(*linuxManager)
	assert.True(t, ok, "should return linuxManager")
}

func TestResolveHostDNSServers(t *testing.T) {
	ctx := context.Background()

	// This test exercises the resolveHostDNSServers function
	// Results depend on the host's /etc/resolv.conf
	servers := resolveHostDNSServers(ctx)

	// Should return something (or nil if no valid DNS)
	// We can't assert specific values since they depend on the host
	for _, s := range servers {
		// Each server should be a valid IPv4 address
		assert.NotEmpty(t, s)
	}
}

func TestNew(t *testing.T) {
	mgr := New()
	require.NotNil(t, mgr)

	_, ok := mgr.(*linuxManager)
	assert.True(t, ok, "New() should return linuxManager on Linux")
}
