package shim

import (
	"fmt"

	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/spin-stack/spinbox/internal/shim/task"
)

const (
	// PropertyGRPCAddress is the property key for the containerd gRPC address.
	// This is set by containerd's shim runtime during plugin initialization.
	propertyGRPCAddress = "io.containerd.plugin.grpc.address"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: TTRPCPlugin,
		ID:   "task",
		Requires: []plugin.Type{
			cplugins.EventPlugin,
			cplugins.InternalPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			pp, err := ic.GetByID(cplugins.EventPlugin, "publisher")
			if err != nil {
				return nil, err
			}
			ss, err := ic.GetByID(cplugins.InternalPlugin, "shutdown")
			if err != nil {
				return nil, err
			}

			// VM instances are created directly by the task service when needed
			publisher, ok := pp.(shim.Publisher)
			if !ok {
				return nil, fmt.Errorf("unexpected publisher type %T", pp)
			}
			shutdownSvc, ok := ss.(shutdown.Service)
			if !ok {
				return nil, fmt.Errorf("unexpected shutdown service type %T", ss)
			}

			// Extract containerd gRPC address from plugin properties.
			// This address is passed by containerd's shim runtime and is needed
			// for updating container labels after VM creation.
			containerdAddress := ic.Properties[propertyGRPCAddress]

			return task.NewTaskService(ic.Context, publisher, shutdownSvc, containerdAddress)
		},
	})
}
