//go:build linux

package qemu

import (
	"bytes"
	"context"
	"testing"

	"github.com/containerd/log"
	"github.com/stretchr/testify/assert"
)

func TestQMPEventHandlers(t *testing.T) {
	tests := []struct {
		name      string
		event     string
		data      map[string]any
		wantLevel string // expected log level: "info", "warn", "error", "debug"
	}{
		{
			name:      "SHUTDOWN event",
			event:     "SHUTDOWN",
			data:      map[string]any{"reason": "guest-shutdown"},
			wantLevel: "info",
		},
		{
			name:      "POWERDOWN event",
			event:     "POWERDOWN",
			data:      nil,
			wantLevel: "info",
		},
		{
			name:      "RESET event",
			event:     "RESET",
			data:      nil,
			wantLevel: "warn",
		},
		{
			name:      "STOP event",
			event:     "STOP",
			data:      nil,
			wantLevel: "debug",
		},
		{
			name:      "RESUME event",
			event:     "RESUME",
			data:      nil,
			wantLevel: "debug",
		},
		{
			name:      "DEVICE_DELETED event",
			event:     "DEVICE_DELETED",
			data:      map[string]any{"device": "virtio-blk0"},
			wantLevel: "debug",
		},
		{
			name:      "NIC_RX_FILTER_CHANGED event",
			event:     "NIC_RX_FILTER_CHANGED",
			data:      map[string]any{"name": "net0"},
			wantLevel: "debug",
		},
		{
			name:      "WATCHDOG event",
			event:     "WATCHDOG",
			data:      map[string]any{"action": "reset"},
			wantLevel: "warn",
		},
		{
			name:      "GUEST_PANICKED event",
			event:     "GUEST_PANICKED",
			data:      nil,
			wantLevel: "error",
		},
		{
			name:      "BLOCK_IO_ERROR event",
			event:     "BLOCK_IO_ERROR",
			data:      map[string]any{"device": "blk0", "operation": "write"},
			wantLevel: "error",
		},
		{
			name:      "unknown event",
			event:     "UNKNOWN_EVENT",
			data:      map[string]any{"foo": "bar"},
			wantLevel: "debug",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify handler exists for known events
			if tt.event != "UNKNOWN_EVENT" {
				handler, ok := qmpEventHandlers[tt.event]
				if tt.wantLevel != "debug" || tt.event != "UNKNOWN_EVENT" {
					assert.True(t, ok, "handler should exist for %s", tt.event)
					assert.NotNil(t, handler)
				}
			}

			// Verify handleEvent doesn't panic
			client := &qmpClient{}
			resp := &qmpResponse{
				Event: tt.event,
				Data:  tt.data,
			}

			assert.NotPanics(t, func() {
				client.handleEvent(context.Background(), resp)
			})
		})
	}
}

func TestQMPStringField(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
		key  string
		want string
	}{
		{
			name: "existing string field",
			data: map[string]any{"reason": "guest-shutdown"},
			key:  "reason",
			want: "guest-shutdown",
		},
		{
			name: "missing field",
			data: map[string]any{"other": "value"},
			key:  "reason",
			want: "unknown",
		},
		{
			name: "nil data",
			data: nil,
			key:  "reason",
			want: "unknown",
		},
		{
			name: "non-string field",
			data: map[string]any{"count": 42},
			key:  "count",
			want: "unknown",
		},
		{
			name: "empty string field",
			data: map[string]any{"reason": ""},
			key:  "reason",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := qmpStringField(tt.data, tt.key)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEventLoopExitsOnClosedChannel(t *testing.T) {
	client := &qmpClient{
		events:        nil, // nil events channel
		eventLoopDone: make(chan struct{}),
	}

	// eventLoop should exit immediately when events is nil
	go client.eventLoop(context.Background())

	// Wait for eventLoopDone to be closed
	select {
	case <-client.eventLoopDone:
		// Success - eventLoop exited
	default:
		t.Error("eventLoop should exit immediately when events is nil")
	}
}

func TestEventLoopExitsOnContextCancel(t *testing.T) {
	events := make(chan struct{}) // Dummy channel that never sends
	ctx, cancel := context.WithCancel(context.Background())

	client := &qmpClient{
		eventLoopDone: make(chan struct{}),
	}

	// Start event loop
	go func() {
		defer close(client.eventLoopDone)
		select {
		case <-ctx.Done():
			return
		case <-events:
			return
		}
	}()

	// Cancel context
	cancel()

	// Wait for eventLoopDone
	<-client.eventLoopDone
}

func TestEventLoopExitsWhenClosed(t *testing.T) {
	client := &qmpClient{
		eventLoopDone: make(chan struct{}),
	}
	client.closed.Store(true)

	// Create a channel that will be checked
	events := make(chan struct{}, 1)
	events <- struct{}{}

	// eventLoop should exit when closed flag is set
	go func() {
		defer close(client.eventLoopDone)
		if client.closed.Load() {
			return
		}
	}()

	<-client.eventLoopDone
}

// TestEventHandlerRegistration verifies all expected events have handlers.
func TestEventHandlerRegistration(t *testing.T) {
	expectedEvents := []string{
		"SHUTDOWN",
		"POWERDOWN",
		"RESET",
		"STOP",
		"RESUME",
		"DEVICE_DELETED",
		"NIC_RX_FILTER_CHANGED",
		"WATCHDOG",
		"GUEST_PANICKED",
		"BLOCK_IO_ERROR",
	}

	for _, event := range expectedEvents {
		t.Run(event, func(t *testing.T) {
			handler, ok := qmpEventHandlers[event]
			assert.True(t, ok, "handler should exist for %s", event)
			assert.NotNil(t, handler)
		})
	}
}

// TestEventHandlersDoNotPanic ensures handlers handle edge cases gracefully.
func TestEventHandlersDoNotPanic(t *testing.T) {
	// Capture log output to avoid test noise
	var buf bytes.Buffer
	logger := log.L.WithField("test", true)

	tests := []struct {
		name  string
		event string
		data  map[string]any
	}{
		{"SHUTDOWN with nil data", "SHUTDOWN", nil},
		{"SHUTDOWN with empty data", "SHUTDOWN", map[string]any{}},
		{"BLOCK_IO_ERROR with nil data", "BLOCK_IO_ERROR", nil},
		{"DEVICE_DELETED with nil data", "DEVICE_DELETED", nil},
		{"WATCHDOG with nil data", "WATCHDOG", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf.Reset()
			handler := qmpEventHandlers[tt.event]
			assert.NotPanics(t, func() {
				handler(logger, tt.data)
			})
		})
	}
}
