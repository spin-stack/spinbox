package shim

import (
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/aledbf/beacon/containerd/shim/task"
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
			return task.NewTaskService(ic.Context, pp.(shim.Publisher), ss.(shutdown.Service))
		},
	})

}
