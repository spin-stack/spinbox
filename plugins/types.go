// Package plugins package stores all the plugin types used internally.
package plugins

import "github.com/containerd/plugin"

const (
	// TTRPCPlugin implements a ttrpc service
	TTRPCPlugin plugin.Type = "io.containerd.ttrpc.v1"

	// StreamingPlugin implements a stream manager
	StreamingPlugin plugin.Type = "beaconbox.streaming.v1"
)
