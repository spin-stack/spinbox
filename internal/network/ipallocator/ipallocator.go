package ipallocator

import (
	"context"
	"net"
	"time"
)

// IPAllocator defines the interface for IP allocation services
type IPAllocator interface {
	// AllocateIP allocates a new IP address for the given host ID
	// Returns the allocated IP, subnet mask in dot-decimal notation, gateway IP, and any error
	AllocateIP(ctx context.Context, hostID string) (net.IP, string, net.IP, error)

	// ReleaseIP releases an allocated IP address
	ReleaseIP(ctx context.Context, ip string) error

	// ReleaseByHostID releases all IPs allocated to a specific host ID
	ReleaseByHostID(ctx context.Context, hostID string) error

	// BatchProcess processes batch operations on IPs
	BatchProcess(ctx context.Context, req BatchRequest) (*BatchResult, error)

	// IsExhausted returns true if all available IPs are allocated
	IsExhausted(ctx context.Context) bool

	// Close closes the allocator and releases any resources
	Close() error
}

// BatchOperation represents the type of batch operation
type BatchOperation int

const (
	BatchRelease BatchOperation = iota
)

// BatchRequest represents a batch IP operation request
type BatchRequest struct {
	Operation BatchOperation
	HostIDs   []string // Host IDs
	IPs       []net.IP // Specific IPs for release
	Timeout   time.Duration
}

// BatchResult represents the result of a batch operation
type BatchResult struct {
	Success     map[string]net.IP // Successfully processed IPs
	Failures    map[string]error  // Failed operations with errors
	SkippedIPs  []net.IP          // IPs that were already allocated/released
	Duration    time.Duration
	TotalOps    int
	FailedOps   int
	SuccessRate float64
}

// IPAllocation represents an allocated IP address
type IPAllocation struct {
	IP          string    `json:"ip"`
	HostID      string    `json:"host_id"`
	AllocatedAt time.Time `json:"allocated_at"`
}

// Config holds the configuration for IP allocator implementations
type Config struct {
	// Subnet is the CIDR notation for the IP range (e.g., "192.168.0.0/24")
	Subnet string
}
