package stdio

import (
	"context"
	"fmt"

	"github.com/containerd/containerd/v2/pkg/shutdown"
	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
	"github.com/containerd/ttrpc"
)

// StdIOPluginType is the plugin type for the stdio service.
// This must match vminit.StdIOPlugin but is defined locally to avoid import cycle.
const StdIOPluginType plugin.Type = "qemubox.stdio.v1"

func init() {
	registry.Register(&plugin.Registration{
		Type: StdIOPluginType,
		ID:   "stdio",
		Requires: []plugin.Type{
			cplugins.InternalPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			ss, err := ic.GetByID(cplugins.InternalPlugin, "shutdown")
			if err != nil {
				return nil, err
			}

			manager := NewManager()
			svc := NewService(manager)

			shutdownSvc, ok := ss.(shutdown.Service)
			if !ok {
				return nil, fmt.Errorf("unexpected shutdown service type %T", ss)
			}

			// Register shutdown callback to cleanup resources.
			shutdownSvc.RegisterCallback(func(ctx context.Context) error {
				// Manager cleanup happens when processes are unregistered.
				return nil
			})

			return &Plugin{
				manager: manager,
				service: svc,
			}, nil
		},
	})
}

// Plugin wraps the stdio manager and service.
type Plugin struct {
	manager *Manager
	service *service
}

// Manager returns the I/O manager for registering processes.
func (p *Plugin) Manager() *Manager {
	return p.manager
}

// RegisterTTRPC registers the StdIO service with a TTRPC server.
func (p *Plugin) RegisterTTRPC(server *ttrpc.Server) error {
	return p.service.RegisterTTRPC(server)
}
