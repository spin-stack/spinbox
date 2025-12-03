package ipallocator

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/aledbf/beacon/containerd/store"
)

// BitmapIPAllocator implements IPAllocator using a bitmap for efficient IP management
type BitmapIPAllocator struct {
	store   boltstore.Store[IPAllocation]
	network *net.IPNet
	bitmap  *IPBitmap
	mu      sync.RWMutex
}

// IPBitmap manages IP address allocation using a bitmap
type IPBitmap struct {
	network *net.IPNet
	bitmap  []uint64 // Each bit represents an IP address
	mu      sync.RWMutex
}

// NewBitmapIPAllocator creates a new bitmap-based IP allocator instance
func NewBitmapIPAllocator(store boltstore.Store[IPAllocation], config Config) (IPAllocator, error) {
	if config.Subnet == "" {
		return nil, fmt.Errorf("subnet cannot be empty")
	}

	_, network, err := net.ParseCIDR(config.Subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet: %w", err)
	}

	allocator := &BitmapIPAllocator{
		store:   store,
		network: network,
	}

	// Initialize bitmap
	bitmap, err := newIPBitmap(network)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize bitmap: %w", err)
	}
	allocator.bitmap = bitmap

	// Load existing allocations
	if err := allocator.loadExistingAllocationsIntoBitmap(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to load existing allocations: %w", err)
	}

	// Set total IPs metric (subtract 3 for network, gateway, and broadcast addresses)
	ones, bits := network.Mask.Size()
	totalIPs := (1 << uint(bits-ones)) - 3
	ipAllocatorMetricsProvider.SetTotalIPs(float64(totalIPs))

	slog.InfoContext(context.Background(), "Bitmap IP allocator initialized", "subnet", config.Subnet)
	return allocator, nil
}

// newIPBitmap creates a new IP bitmap for the given network
func newIPBitmap(network *net.IPNet) (*IPBitmap, error) {
	ones, bits := network.Mask.Size()
	if ones > bits-2 {
		return nil, fmt.Errorf("subnet too small, needs at least 4 addresses")
	}

	// Calculate number of uint64s needed to store all IPs
	numIPs := uint(1) << uint(bits-ones)
	numWords := (numIPs + 63) / 64 // Round up to nearest uint64

	bitmap := &IPBitmap{
		network: network,
		bitmap:  make([]uint64, numWords),
	}

	// Mark network address as used
	networkIP := network.IP.Mask(network.Mask)
	bitmap.MarkIPUsed(networkIP)

	// Mark gateway address (first usable IP) as reserved
	gatewayIP := make(net.IP, len(networkIP))
	copy(gatewayIP, networkIP)
	gatewayIP[len(gatewayIP)-1] |= 1 // Set last byte to 1
	bitmap.MarkIPUsed(gatewayIP)

	// Mark broadcast address as used
	broadcastIP := calculateBroadcastIP(networkIP, ones, bits)
	bitmap.MarkIPUsed(broadcastIP)

	return bitmap, nil
}

// findNextAvailableIP finds the next free IP using bitmap
func (a *BitmapIPAllocator) findNextAvailableIP() (net.IP, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Find first free IP in bitmap
	ip, err := a.bitmap.FindFirstFreeIP(a.network)
	if err != nil {
		return nil, err
	}

	// Mark IP as used immediately to prevent race conditions
	a.bitmap.MarkIPUsed(ip)
	return ip, nil
}

// FindFirstFreeIP finds the first free IP in the bitmap
func (b *IPBitmap) FindFirstFreeIP(network *net.IPNet) (net.IP, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	ones, bits := network.Mask.Size()
	size := uint32(1 << uint(bits-ones))

	// Start from offset 2 to skip network address AND gateway address
	for offset := uint32(2); offset < size-1; offset++ { // size-1 to skip broadcast
		wordIdx := offset / 64
		bitIdx := offset % 64

		if int(wordIdx) >= len(b.bitmap) {
			break
		}

		// Check if this bit is set
		if (b.bitmap[wordIdx] & (1 << bitIdx)) == 0 {
			// Bit is clear, this IP is free
			ip := offsetToIP(network.IP, offset)
			if ip == nil {
				continue
			}

			// Double check it's in our network
			if !network.Contains(ip) {
				continue
			}

			return ip, nil
		}
	}
	return nil, fmt.Errorf("no available IPs in network")
}

// AllocateIP allocates a new IP address and returns network information
func (a *BitmapIPAllocator) AllocateIP(ctx context.Context, hostID string) (net.IP, string, net.IP, error) {
	start := time.Now()
	defer func() {
		ipAllocatorMetricsProvider.ObserveIPAllocationDuration(time.Since(start))
	}()

	ipAllocatorMetricsProvider.IncrementIPAllocations()

	ip, err := a.findNextAvailableIP()
	if err != nil {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, "", nil, fmt.Errorf("failed to find available IP: %w", err)
	}

	allocation := &IPAllocation{
		IP:          ip.String(),
		HostID:      hostID,
		AllocatedAt: time.Now(),
	}

	if err := a.store.Set(ctx, ip.String(), allocation); err != nil {
		// Mark IP as free again in bitmap
		if a.bitmap != nil {
			a.bitmap.MarkIPFree(ip)
		}
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, "", nil, fmt.Errorf("failed to store allocation: %w", err)
	}

	// Successful allocation
	ipAllocatorMetricsProvider.IncrementAllocatedIPs()

	// Calculate gateway (first usable IP in subnet)
	gateway := make(net.IP, len(a.network.IP))
	copy(gateway, a.network.IP)
	gateway[len(gateway)-1] |= 1 // Set last byte to 1

	// Convert mask to dot-decimal notation
	maskBytes := net.IP(a.network.Mask).To4()

	return ip, fmt.Sprintf("%d.%d.%d.%d", maskBytes[0], maskBytes[1], maskBytes[2], maskBytes[3]), gateway, nil
}

// AllocateSpecificIP allocates a specific IP address if available
func (a *BitmapIPAllocator) AllocateSpecificIP(ctx context.Context, ipStr, hostID string) (net.IP, error) {
	start := time.Now()
	defer func() {
		ipAllocatorMetricsProvider.ObserveIPAllocationDuration(time.Since(start))
	}()

	ipAllocatorMetricsProvider.IncrementIPAllocations()

	ip := net.ParseIP(ipStr)
	if ip == nil {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("invalid IP address: %s", ipStr)
	}

	ip4 := ip.To4()
	if ip4 == nil {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("not an IPv4 address: %s", ipStr)
	}

	// Check if IP is within our network
	if !a.network.Contains(ip4) {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("IP %s is not within network %s", ipStr, a.network.String())
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	// Check if IP is already allocated in the store
	allocation, err := a.store.Get(ctx, ipStr)
	if err == nil && allocation != nil {
		// IP is already allocated, check if it's for the same host
		if allocation.HostID == hostID {
			slog.DebugContext(ctx, "IP already allocated to same host", "ip", ipStr, "hostID", hostID)
			return ip4, nil // Already allocated to this host, that's fine
		}
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("IP %s is already allocated to host %s", ipStr, allocation.HostID)
	}
	// If err != nil, the IP is not allocated yet, which is what we want for new allocations

	// Check if this is a reserved IP (network, gateway, broadcast)
	if isReservedIP(ip4, a.network) {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("IP %s is reserved (network, gateway, or broadcast)", ipStr)
	}

	// Mark IP as used in bitmap
	if err := a.bitmap.MarkIPUsed(ip4); err != nil {
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("failed to mark IP as used in bitmap: %w", err)
	}

	// Store allocation
	newAllocation := &IPAllocation{
		IP:          ipStr,
		HostID:      hostID,
		AllocatedAt: time.Now(),
	}

	if err := a.store.Set(ctx, ipStr, newAllocation); err != nil {
		// Rollback bitmap change
		a.bitmap.MarkIPFree(ip4)
		ipAllocatorMetricsProvider.IncrementIPAllocationErrors()
		return nil, fmt.Errorf("failed to store allocation: %w", err)
	}

	// Successful allocation
	ipAllocatorMetricsProvider.IncrementAllocatedIPs()

	slog.DebugContext(ctx, "Allocated specific IP", "ip", ipStr, "hostID", hostID)
	return ip4, nil
}

// ReleaseIP releases an allocated IP address
func (a *BitmapIPAllocator) ReleaseIP(ctx context.Context, ip string) error {
	start := time.Now()
	defer func() {
		ipAllocatorMetricsProvider.ObserveIPReleaseDuration(time.Since(start))
	}()

	ipAllocatorMetricsProvider.IncrementIPReleases()

	a.mu.Lock()
	defer a.mu.Unlock()

	parsedIP := net.ParseIP(ip)
	if parsedIP == nil {
		ipAllocatorMetricsProvider.IncrementIPReleaseErrors()
		return fmt.Errorf("invalid IP address")
	}

	// Verify IP is actually allocated
	_, err := a.store.Get(ctx, ip)
	if err != nil {
		if isNotFoundError(err) {
			return nil // IP wasn't allocated, nothing to do
		}
		ipAllocatorMetricsProvider.IncrementIPReleaseErrors()
		return fmt.Errorf("failed to check IP allocation: %w", err)
	}

	// Remove allocation from store
	if err := a.store.Delete(ctx, ip); err != nil {
		ipAllocatorMetricsProvider.IncrementIPReleaseErrors()
		return fmt.Errorf("failed to delete IP allocation: %w", err)
	}

	// Successful release
	ipAllocatorMetricsProvider.DecrementAllocatedIPs()

	// Mark as free in bitmap
	if a.bitmap != nil {
		if err := a.bitmap.MarkIPFree(parsedIP); err != nil {
			slog.WarnContext(ctx, "Failed to mark IP as free in bitmap",
				"ip", ip,
				"error", err)
		}
	}

	return nil
}

// ReleaseByHostID releases all IPs allocated to a specific host ID
func (a *BitmapIPAllocator) ReleaseByHostID(ctx context.Context, hostID string) error {
	start := time.Now()
	defer func() {
		ipAllocatorMetricsProvider.ObserveIPReleaseDuration(time.Since(start))
	}()

	ipAllocatorMetricsProvider.IncrementIPReleases()
	var ipsToRelease []string

	// Find all IPs allocated to this host ID
	err := a.store.Scan(ctx, "", func(_ string, alloc *IPAllocation) error {
		if alloc.HostID == hostID {
			ipsToRelease = append(ipsToRelease, alloc.IP)
		}
		return nil
	})

	if err != nil {
		ipAllocatorMetricsProvider.IncrementIPReleaseErrors()
		return fmt.Errorf("failed to scan allocations: %w", err)
	}

	// Release each IP
	var lastErr error
	for _, ip := range ipsToRelease {
		if err := a.ReleaseIP(ctx, ip); err != nil {
			lastErr = err
			slog.ErrorContext(ctx, "Failed to release IP for host",
				"host_id", hostID,
				"ip", ip,
				"error", err)
		}
	}

	if lastErr != nil {
		ipAllocatorMetricsProvider.IncrementIPReleaseErrors()
		return fmt.Errorf("failed to release some IPs: %w", lastErr)
	}

	return nil
}

// BatchProcess handles batch IP operations
func (a *BitmapIPAllocator) BatchProcess(ctx context.Context, req BatchRequest) (*BatchResult, error) {
	if len(req.HostIDs) == 0 && len(req.IPs) == 0 {
		return nil, fmt.Errorf("batch request must include HostIDs or IPs")
	}

	// Create context with timeout if specified
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	result := &BatchResult{
		Success:  make(map[string]net.IP),
		Failures: make(map[string]error),
	}

	startTime := time.Now()

	// Process based on operation type
	switch req.Operation {
	case BatchRelease:
		a.processBatchRelease(ctx, req, result)
	default:
		return nil, fmt.Errorf("unknown batch operation type")
	}

	// Calculate statistics
	result.Duration = time.Since(startTime)
	result.TotalOps = len(req.HostIDs)
	result.FailedOps = len(result.Failures)
	if result.TotalOps > 0 {
		result.SuccessRate = float64(result.TotalOps-result.FailedOps) / float64(result.TotalOps) * 100
	}

	return result, nil
}

// MarkIPUsed marks an IP as used in the bitmap
func (b *IPBitmap) MarkIPUsed(ip net.IP) error {
	if ip == nil {
		return fmt.Errorf("invalid IP: nil")
	}

	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("invalid IPv4 address: %v", ip)
	}

	if !b.network.Contains(ip4) {
		return fmt.Errorf("IP %v not in network %v", ip4, b.network)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	ipNum := ipToNum(ip4, b.network.IP)
	wordIdx := ipNum / 64
	bitIdx := ipNum % 64

	if int(wordIdx) >= len(b.bitmap) {
		return fmt.Errorf("IP %v results in index out of range: %d", ip4, wordIdx)
	}

	b.bitmap[wordIdx] |= 1 << bitIdx
	return nil
}

// MarkIPFree marks an IP as free in the bitmap
func (b *IPBitmap) MarkIPFree(ip net.IP) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	ipNum := ipToNum(ip, b.network.IP)
	wordIdx := ipNum / 64
	bitIdx := ipNum % 64

	if int(wordIdx) >= len(b.bitmap) {
		return fmt.Errorf("IP out of range")
	}

	b.bitmap[wordIdx] &^= 1 << bitIdx
	return nil
}

// loadExistingAllocationsIntoBitmap loads existing IP allocations into the bitmap
func (a *BitmapIPAllocator) loadExistingAllocationsIntoBitmap(ctx context.Context) error {
	var allocatedCount int
	err := a.store.Scan(ctx, "", func(_ string, alloc *IPAllocation) error {
		ip := net.ParseIP(alloc.IP)
		if ip == nil {
			return fmt.Errorf("invalid IP in allocation: %s", alloc.IP)
		}
		allocatedCount++
		return a.bitmap.MarkIPUsed(ip)
	})
	if err != nil {
		return err
	}

	// Set the current allocated IPs count
	ipAllocatorMetricsProvider.SetAllocatedIPs(float64(allocatedCount))

	return nil
}

// IsExhausted checks if all IPs are allocated
func (a *BitmapIPAllocator) IsExhausted(ctx context.Context) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	ones, bits := a.network.Mask.Size()
	size := uint32(1 << uint(bits-ones))

	// Start from offset 1 to skip network address
	for offset := uint32(1); offset < size-1; offset++ { // size-1 to skip broadcast
		wordIdx := offset / 64
		bitIdx := offset % 64

		if int(wordIdx) >= len(a.bitmap.bitmap) {
			break
		}

		// If we find any unset bit in the usable range, we're not exhausted
		if (a.bitmap.bitmap[wordIdx] & (1 << bitIdx)) == 0 {
			return false
		}
	}

	return true
}

// processBatchRelease handles batch IP release operations
func (a *BitmapIPAllocator) processBatchRelease(ctx context.Context, req BatchRequest, result *BatchResult) {
	// Process specific IPs if provided
	if len(req.IPs) > 0 {
		for _, ip := range req.IPs {
			select {
			case <-ctx.Done():
				result.Failures[ip.String()] = fmt.Errorf("operation cancelled")
				continue
			default:
				if err := a.ReleaseIP(ctx, ip.String()); err != nil {
					result.Failures[ip.String()] = err
					continue
				}
				result.Success[ip.String()] = ip
			}
		}
		return
	}

	// Process by host IDs
	for _, id := range req.HostIDs {
		var ipToRelease string
		err := a.store.Scan(ctx, "", func(_ string, alloc *IPAllocation) error {
			if alloc.HostID == id {
				ipToRelease = alloc.IP
				return fmt.Errorf("stop scanning")
			}
			return nil
		})
		if err != nil && err.Error() != "stop scanning" {
			// Only treat real errors as failures, not our scan termination signal
			result.Failures[id] = err
			continue
		}

		if ipToRelease == "" {
			// No IP found for this ID - not an error case
			continue
		}

		if err := a.ReleaseIP(ctx, ipToRelease); err != nil {
			result.Failures[id] = err
			continue
		}

		result.Success[id] = net.ParseIP(ipToRelease)
	}
}

// Close closes the allocator and releases any resources
func (a *BitmapIPAllocator) Close() error {
	return a.store.Close()
}

// Helper functions

// Helper functions for IP conversion
func ipToNum(ip, base net.IP) uint32 {
	ip4 := ip.To4()
	base4 := base.To4()
	if ip4 == nil || base4 == nil {
		// IPv6 or invalid
		return 0
	}
	return binary.BigEndian.Uint32(ip4) - binary.BigEndian.Uint32(base4)
}

// Converts a 0-based offset back to an IP within that subnet's base.
func offsetToIP(base net.IP, offset uint32) net.IP {
	base4 := base.To4()
	if base4 == nil {
		return nil
	}
	newVal := binary.BigEndian.Uint32(base4) + offset
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, newVal)
	return ip
}

func calculateBroadcastIP(networkIP net.IP, ones, bits int) net.IP {
	broadcast := make(net.IP, len(networkIP))
	copy(broadcast, networkIP)
	for i := ones; i < bits; i++ {
		byteIndex := i / 8
		bitOffset := uint(7 - (i % 8))
		broadcast[byteIndex] |= 1 << bitOffset
	}
	return broadcast
}

// Helper function to check for "not found" errors
func isNotFoundError(err error) bool {
	// Check common "not found" error patterns
	if err == nil {
		return false
	}

	errStr := err.Error()
	return errStr == "not found" ||
		errStr == "key not found" ||
		errStr == "record not found"
}

// isReservedIP checks if an IP is reserved (network, gateway, or broadcast)
func isReservedIP(ip net.IP, network *net.IPNet) bool {
	// Network address
	networkIP := network.IP.Mask(network.Mask)
	if ip.Equal(networkIP) {
		return true
	}

	// Gateway address (first usable IP)
	gatewayIP := make(net.IP, len(networkIP))
	copy(gatewayIP, networkIP)
	gatewayIP[len(gatewayIP)-1] |= 1
	if ip.Equal(gatewayIP) {
		return true
	}

	// Broadcast address
	ones, bits := network.Mask.Size()
	broadcastIP := calculateBroadcastIP(networkIP, ones, bits)
	return ip.Equal(broadcastIP)
}
