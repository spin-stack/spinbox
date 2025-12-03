package main

import (
	"context"

	"github.com/aledbf/beacon/containerd/shim/manager"
	_ "github.com/aledbf/beacon/containerd/shim"
	"github.com/containerd/containerd/v2/pkg/shim"
)

func main() {
	shim.Run(context.Background(), manager.NewShimManager("io.containerd.beaconbox.v1"))
}
