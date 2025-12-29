package events

import (
	"context"
	"testing"
	"time"

	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/errdefs"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestValidateTopic(t *testing.T) {
	tests := []struct {
		name    string
		topic   string
		wantErr bool
	}{
		{
			name:    "valid topic",
			topic:   "/containers/create",
			wantErr: false,
		},
		{
			name:    "valid topic with multiple components",
			topic:   "/containers/tasks/start",
			wantErr: false,
		},
		{
			name:    "empty topic",
			topic:   "",
			wantErr: true,
		},
		{
			name:    "topic without leading slash",
			topic:   "containers/create",
			wantErr: true,
		},
		{
			name:    "topic with only slash",
			topic:   "/",
			wantErr: true,
		},
		{
			name:    "topic with invalid character",
			topic:   "/containers/create!",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTopic(tt.topic)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestValidateEnvelope(t *testing.T) {
	validEvent, err := typeurl.MarshalAny(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("failed to marshal test event: %v", err)
	}

	tests := []struct {
		name     string
		envelope *events.Envelope
		wantErr  bool
	}{
		{
			name: "valid envelope",
			envelope: &events.Envelope{
				Namespace: "default",
				Topic:     "/containers/create",
				Timestamp: time.Now(),
				Event:     validEvent,
			},
			wantErr: false,
		},
		{
			name: "invalid namespace",
			envelope: &events.Envelope{
				Namespace: "invalid namespace!",
				Topic:     "/containers/create",
				Timestamp: time.Now(),
				Event:     validEvent,
			},
			wantErr: true,
		},
		{
			name: "invalid topic",
			envelope: &events.Envelope{
				Namespace: "default",
				Topic:     "invalid",
				Timestamp: time.Now(),
				Event:     validEvent,
			},
			wantErr: true,
		},
		{
			name: "zero timestamp",
			envelope: &events.Envelope{
				Namespace: "default",
				Topic:     "/containers/create",
				Timestamp: time.Time{},
				Event:     validEvent,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEnvelope(tt.envelope)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestNewExchange(t *testing.T) {
	ex := NewExchange()
	if ex == nil {
		t.Fatal("NewExchange() returned nil")
	}
	if ex.broadcaster == nil {
		t.Fatal("Exchange broadcaster is nil")
	}
}

func TestExchange_Publish(t *testing.T) {
	ex := NewExchange()
	ctx := context.Background()

	t.Run("valid event", func(t *testing.T) {
		err := ex.Publish(ctx, "/test/topic", &emptypb.Empty{})
		if err != nil {
			t.Fatalf("Publish() failed: %v", err)
		}
	})

	t.Run("invalid topic - empty", func(t *testing.T) {
		err := ex.Publish(ctx, "", &emptypb.Empty{})
		if err == nil {
			t.Fatal("expected error for empty topic, got nil")
		}
	})

	t.Run("invalid topic - no leading slash", func(t *testing.T) {
		err := ex.Publish(ctx, "test/topic", &emptypb.Empty{})
		if err == nil {
			t.Fatal("expected error for topic without leading slash, got nil")
		}
	})
}

func TestExchange_Forward(t *testing.T) {
	ex := NewExchange()
	ctx := context.Background()

	validEvent, err := typeurl.MarshalAny(&emptypb.Empty{})
	if err != nil {
		t.Fatalf("failed to marshal test event: %v", err)
	}

	t.Run("valid envelope", func(t *testing.T) {
		envelope := &events.Envelope{
			Namespace: "default",
			Topic:     "/test/forward",
			Timestamp: time.Now(),
			Event:     validEvent,
		}

		err := ex.Forward(ctx, envelope)
		if err != nil {
			t.Fatalf("Forward() failed: %v", err)
		}
	})

	t.Run("invalid envelope - bad namespace", func(t *testing.T) {
		envelope := &events.Envelope{
			Namespace: "invalid!namespace",
			Topic:     "/test/forward",
			Timestamp: time.Now(),
			Event:     validEvent,
		}

		err := ex.Forward(ctx, envelope)
		if err == nil {
			t.Fatal("expected error for invalid namespace, got nil")
		}
	})

	t.Run("invalid envelope - zero timestamp", func(t *testing.T) {
		envelope := &events.Envelope{
			Namespace: "default",
			Topic:     "/test/forward",
			Timestamp: time.Time{},
			Event:     validEvent,
		}

		err := ex.Forward(ctx, envelope)
		if err == nil {
			t.Fatal("expected error for zero timestamp, got nil")
		}
	})
}

func TestExchange_Subscribe(t *testing.T) {
	ex := NewExchange()

	t.Run("subscribe without filters", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		evCh, errCh := ex.Subscribe(ctx)

		// Verify channels are not nil
		if evCh == nil {
			t.Fatal("event channel is nil")
		}
		if errCh == nil {
			t.Fatal("error channel is nil")
		}

		// Publish an event
		if err := ex.Publish(ctx, "/test/subscribe", &emptypb.Empty{}); err != nil {
			t.Fatalf("Publish() failed: %v", err)
		}

		// Should receive the event
		select {
		case env := <-evCh:
			if env == nil {
				t.Fatal("received nil envelope")
			}
			if env.Topic != "/test/subscribe" {
				t.Errorf("topic = %q, want %q", env.Topic, "/test/subscribe")
			}
		case err := <-errCh:
			t.Fatalf("unexpected error: %v", err)
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for event")
		}

		// Cancel and verify error channel closes
		cancel()
		select {
		case err := <-errCh:
			// Expect context.Canceled or nil
			if err != nil && err != context.Canceled {
				t.Errorf("unexpected error on cancel: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for error channel close")
		}
	})

	t.Run("subscribe with invalid filter", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		_, errCh := ex.Subscribe(ctx, "invalid filter syntax (((")

		// Should receive error immediately
		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("expected error for invalid filter, got nil")
			}
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for filter error")
		}
	})

	t.Run("context cancelled before events", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		_, errCh := ex.Subscribe(ctx)

		// Should receive context error
		select {
		case err := <-errCh:
			// Expect context.Canceled or nil
			_ = err
		case <-time.After(time.Second):
			t.Fatal("timeout waiting for context error")
		}
	})
}

func TestAdapt(t *testing.T) {
	t.Run("non-adaptor type", func(t *testing.T) {
		result := adapt("not an adaptor")
		if result == nil {
			t.Fatal("adapt() returned nil")
		}

		// Should return empty string and false
		val, ok := result.Field([]string{"any", "field"})
		if ok {
			t.Error("expected ok=false for non-adaptor")
		}
		if val != "" {
			t.Errorf("expected empty string, got %q", val)
		}
	})
}

// Test error conditions
func TestExchange_PublishErrors(t *testing.T) {
	ex := NewExchange()
	ctx := context.Background()

	tests := []struct {
		name  string
		topic string
	}{
		{"empty topic", ""},
		{"no leading slash", "test"},
		{"only slash", "/"},
		{"invalid chars", "/test/invalid!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ex.Publish(ctx, tt.topic, &emptypb.Empty{})
			if err == nil {
				t.Errorf("expected error for topic %q, got nil", tt.topic)
			}
			// Should wrap ErrInvalidArgument
			if !errdefs.IsInvalidArgument(err) {
				t.Errorf("expected ErrInvalidArgument, got %v", err)
			}
		})
	}
}
