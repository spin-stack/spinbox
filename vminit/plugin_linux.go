package vminit

import (
	"github.com/containerd/containerd/v2/core/events"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	cplugins "github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"

	"github.com/aledbf/qemubox/containerd/vminit/bundle"
	"github.com/aledbf/qemubox/containerd/vminit/stream"
	"github.com/aledbf/qemubox/containerd/vminit/task"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: cplugins.TTRPCPlugin,
		ID:   "task",
		Requires: []plugin.Type{
			cplugins.EventPlugin,
			cplugins.InternalPlugin,
			StreamingPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			pp, err := ic.GetSingle(cplugins.EventPlugin)
			if err != nil {
				return nil, err
			}
			ss, err := ic.GetByID(cplugins.InternalPlugin, "shutdown")
			if err != nil {
				return nil, err
			}
			sm, err := ic.GetByID(StreamingPlugin, "vsock")
			if err != nil {
				return nil, err
			}
			return task.NewTaskService(ic.Context, bundle.RootDir, pp.(events.Publisher), ss.(shutdown.Service), sm.(stream.Manager))
		},
	})
}
