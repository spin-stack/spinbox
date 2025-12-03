package main

import (
	"context"

	"github.com/aledbf/beacon/containerd/internal/shim/manager"
	_ "github.com/aledbf/beacon/containerd/plugins/shim/task"
	"github.com/containerd/containerd/v2/pkg/shim"
)

func main() {
	shim.Run(context.Background(), manager.NewShimManager("io.containerd.beaconbox.v1"))
}
