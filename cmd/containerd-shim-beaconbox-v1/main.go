package main

import (
	"context"

	_ "github.com/aledbf/beacon/containerd/shim"
	"github.com/aledbf/beacon/containerd/shim/manager"
	"github.com/containerd/containerd/v2/pkg/shim"
)

func main() {
	ctx := context.Background()
	shim.Run(ctx, manager.NewShimManager("io.containerd.beaconbox.v1"))
}
