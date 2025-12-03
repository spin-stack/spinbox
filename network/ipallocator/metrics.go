package ipallocator

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// IPAllocatorMetricsProvider defines the interface for providing metrics for IP allocation operations.
type IPAllocatorMetricsProvider interface {
	IncrementAllocatedIPs()
	DecrementAllocatedIPs()
	SetAllocatedIPs(count float64)
	IncrementIPAllocations()
	IncrementIPReleases()
	IncrementIPAllocationErrors()
	IncrementIPReleaseErrors()
	ObserveIPAllocationDuration(duration time.Duration)
	ObserveIPReleaseDuration(duration time.Duration)
	SetTotalIPs(count float64)
}

// NoopIPAllocatorMetricsProvider implements PAllocatorMetricsProvider with no-op operations.
type NoopIPAllocatorMetricsProvider struct{}

func (n *NoopIPAllocatorMetricsProvider) IncrementAllocatedIPs()                             {}
func (n *NoopIPAllocatorMetricsProvider) DecrementAllocatedIPs()                             {}
func (n *NoopIPAllocatorMetricsProvider) SetAllocatedIPs(count float64)                      {}
func (n *NoopIPAllocatorMetricsProvider) IncrementIPAllocations()                            {}
func (n *NoopIPAllocatorMetricsProvider) IncrementIPReleases()                               {}
func (n *NoopIPAllocatorMetricsProvider) IncrementIPAllocationErrors()                       {}
func (n *NoopIPAllocatorMetricsProvider) IncrementIPReleaseErrors()                          {}
func (n *NoopIPAllocatorMetricsProvider) ObserveIPAllocationDuration(duration time.Duration) {}
func (n *NoopIPAllocatorMetricsProvider) ObserveIPReleaseDuration(duration time.Duration)    {}
func (n *NoopIPAllocatorMetricsProvider) SetTotalIPs(count float64)                          {}

// NewNoopIPAllocatorMetricsProvider creates a new NoopIPAllocatorMetricsProvider.
func NewNoopIPAllocatorMetricsProvider() *NoopIPAllocatorMetricsProvider {
	return &NoopIPAllocatorMetricsProvider{}
}

// PrometheusIPAllocatorMetricsProvider implements IPAllocatorMetricsProvider using Prometheus.
type PrometheusIPAllocatorMetricsProvider struct {
	allocatedIPs         prometheus.Gauge
	totalIPs             prometheus.Gauge
	ipAllocations        prometheus.Counter
	ipReleases           prometheus.Counter
	ipAllocationErrors   prometheus.Counter
	ipReleaseErrors      prometheus.Counter
	ipAllocationDuration prometheus.Histogram
	ipReleaseDuration    prometheus.Histogram
}

// NewPrometheusIPAllocatorMetricsProvider creates a new PrometheusIPAllocatorMetricsProvider.
func NewPrometheusIPAllocatorMetricsProvider(registry prometheus.Registerer) *PrometheusIPAllocatorMetricsProvider {
	p := &PrometheusIPAllocatorMetricsProvider{
		allocatedIPs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gitpod_ip_allocator_allocated_ips",
			Help: "Number of currently allocated IP addresses",
		}),
		totalIPs: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "gitpod_ip_allocator_total_ips",
			Help: "Total number of allocatable IP addresses in the subnet",
		}),
		ipAllocations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gitpod_ip_allocator_allocations_total",
			Help: "Total number of IP allocation attempts",
		}),
		ipReleases: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gitpod_ip_allocator_releases_total",
			Help: "Total number of IP release attempts",
		}),
		ipAllocationErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gitpod_ip_allocator_allocation_errors_total",
			Help: "Total number of IP allocation errors",
		}),
		ipReleaseErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "gitpod_ip_allocator_release_errors_total",
			Help: "Total number of IP release errors",
		}),
		ipAllocationDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gitpod_ip_allocator_allocation_duration_seconds",
			Help:    "Duration of IP allocation operations",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		}),
		ipReleaseDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "gitpod_ip_allocator_release_duration_seconds",
			Help:    "Duration of IP release operations",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 10),
		}),
	}

	registry.MustRegister(
		p.allocatedIPs,
		p.totalIPs,
		p.ipAllocations,
		p.ipReleases,
		p.ipAllocationErrors,
		p.ipReleaseErrors,
		p.ipAllocationDuration,
		p.ipReleaseDuration,
	)

	return p
}

func (p *PrometheusIPAllocatorMetricsProvider) IncrementAllocatedIPs() {
	p.allocatedIPs.Inc()
}

func (p *PrometheusIPAllocatorMetricsProvider) DecrementAllocatedIPs() {
	p.allocatedIPs.Dec()
}

func (p *PrometheusIPAllocatorMetricsProvider) SetAllocatedIPs(count float64) {
	p.allocatedIPs.Set(count)
}

func (p *PrometheusIPAllocatorMetricsProvider) IncrementIPAllocations() {
	p.ipAllocations.Inc()
}

func (p *PrometheusIPAllocatorMetricsProvider) IncrementIPReleases() {
	p.ipReleases.Inc()
}

func (p *PrometheusIPAllocatorMetricsProvider) IncrementIPAllocationErrors() {
	p.ipAllocationErrors.Inc()
}

func (p *PrometheusIPAllocatorMetricsProvider) IncrementIPReleaseErrors() {
	p.ipReleaseErrors.Inc()
}

func (p *PrometheusIPAllocatorMetricsProvider) ObserveIPAllocationDuration(duration time.Duration) {
	p.ipAllocationDuration.Observe(duration.Seconds())
}

func (p *PrometheusIPAllocatorMetricsProvider) ObserveIPReleaseDuration(duration time.Duration) {
	p.ipReleaseDuration.Observe(duration.Seconds())
}

func (p *PrometheusIPAllocatorMetricsProvider) SetTotalIPs(count float64) {
	p.totalIPs.Set(count)
}

var ipAllocatorMetricsProvider IPAllocatorMetricsProvider = NewNoopIPAllocatorMetricsProvider()

// SetIPAllocatorMetricsProvider sets the global IP allocator metrics provider.
func SetIPAllocatorMetricsProvider(provider IPAllocatorMetricsProvider) {
	ipAllocatorMetricsProvider = provider
}
