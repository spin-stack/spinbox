package main

import (
	"context"
	"os"
	"strconv"

	_ "github.com/aledbf/qemubox/containerd/shim"
	"github.com/aledbf/qemubox/containerd/shim/manager"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/log"
)

func main() {
	setShimLogLevel()
	//nolint:staticcheck // shim.Run ignores the context on this build.
	ctx := context.Background()
	shim.Run(ctx, manager.NewShimManager("io.containerd.qemubox.v1"))
}

func setShimLogLevel() {
	debug, _ := strconv.ParseBool(os.Getenv("QEMUBOX_SHIM_DEBUG"))
	if debug {
		_ = log.SetLevel("debug")
		return
	}
	_ = log.SetLevel("info")
}
