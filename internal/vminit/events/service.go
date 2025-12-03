//go:build linux

package events

import (
	"context"

	"github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/protobuf"
	"github.com/containerd/ttrpc"
	"github.com/containerd/typeurl/v2"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/aledbf/beacon/containerd/api/services/vmevents/v1"
)

type Subscriber interface {
	Subscribe(context.Context, ...string) (<-chan *events.Envelope, <-chan error)
}

type service struct {
	sub Subscriber
}

func NewService(s Subscriber) *service {
	return &service{
		sub: s,
	}
}

func (s *service) RegisterTTRPC(server *ttrpc.Server) error {
	vmevents.RegisterTTRPCEventsService(server, s)
	return nil
}

func (s *service) Stream(ctx context.Context, _ *emptypb.Empty, ss vmevents.TTRPCEvents_StreamServer) error {
	events, errs := s.sub.Subscribe(ctx)
	for {
		select {
		case event := <-events:
			if err := ss.Send(toProto(event)); err != nil {
				return err
			}
		case err := <-errs:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func toProto(env *events.Envelope) *types.Envelope {
	return &types.Envelope{
		Timestamp: protobuf.ToTimestamp(env.Timestamp),
		Namespace: env.Namespace,
		Topic:     env.Topic,
		Event:     typeurl.MarshalProto(env.Event),
	}
}
