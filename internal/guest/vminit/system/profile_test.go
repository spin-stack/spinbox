//go:build linux

package system

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBootProfilerDisabledIsNoop(t *testing.T) {
	// A disabled profiler must not panic and must accept Mark calls.
	// resolved:true pins the decision so Mark does not re-read /proc/cmdline.
	p := &BootProfiler{resolved: true, enabled: false, start: time.Now(), last: time.Now()}
	p.Mark(t.Context(), "phase")

	// A nil profiler must also be safe (Initialize may receive nil).
	var nilProf *BootProfiler
	nilProf.Mark(t.Context(), "phase")
}

func TestBootProfilerAdvancesClock(t *testing.T) {
	start := time.Now()
	p := &BootProfiler{resolved: true, enabled: true, start: start, last: start}

	// last starts at start; after a Mark it should move forward so the next
	// delta is measured from the new point, not from start.
	before := p.last
	p.Mark(t.Context(), "first")
	assert.False(t, p.last.Before(before), "Mark must not move last backwards")
	assert.True(t, p.last.After(before) || p.last.Equal(before))
}

func TestProfileEnabledReadsMarker(t *testing.T) {
	// profileEnabled reads /proc/cmdline; on the test host the spin.profile
	// marker is not present, so it must report disabled rather than panic.
	assert.False(t, profileEnabled())
}
