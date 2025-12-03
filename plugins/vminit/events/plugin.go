//go:build linux

package events

import (
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/aledbf/beacon/containerd/internal/events"
	eventsrv "github.com/aledbf/beacon/containerd/internal/vminit/events"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.EventPlugin,
		ID:   "exchange",
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			return events.NewExchange(), nil
		},
	})
}

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.TTRPCPlugin,
		ID:   "events",
		Requires: []plugin.Type{
			plugins.EventPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			ep, err := ic.GetSingle(plugins.EventPlugin)
			if err != nil {
				return nil, err
			}
			return eventsrv.NewService(ep.(eventsrv.Subscriber)), nil
		},
	})
}
