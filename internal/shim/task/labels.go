//go:build linux

package task

import (
	"context"
	"strconv"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/log"

	platformNetwork "github.com/spin-stack/spinbox/internal/shim/platform/network"
)

const (
	// Label prefixes for VM runtime metadata
	labelPrefix = "io.spin.vm."

	// Individual label keys
	labelCID     = labelPrefix + "cid"
	labelIP      = labelPrefix + "ip"
	labelGateway = labelPrefix + "gateway"
	labelNetmask = labelPrefix + "netmask"
	labelMAC     = labelPrefix + "mac"
	labelTAP     = labelPrefix + "tap"
)

// updateContainerLabels updates the container's labels in containerd with VM runtime metadata.
// This is a best-effort operation - failures are logged but do not cause container creation to fail.
// containerdAddress is the gRPC address for connecting back to containerd.
func updateContainerLabels(ctx context.Context, containerID string, cid uint32, netResult *platformNetwork.SetupResult, containerdAddress string) {
	if containerdAddress == "" {
		log.G(ctx).Debug("skipping label update: containerd address not available")
		return
	}

	if netResult == nil || netResult.Config == nil {
		log.G(ctx).Debug("skipping label update: no network result")
		return
	}

	// Get namespace from context
	ns, ok := namespaces.Namespace(ctx)
	if !ok {
		log.G(ctx).Warn("skipping label update: namespace not found in context")
		return
	}

	// Build labels map with VM runtime metadata
	labels := map[string]string{
		labelCID:     strconv.FormatUint(uint64(cid), 10),
		labelIP:      netResult.Config.IP,
		labelGateway: netResult.Config.Gateway,
		labelNetmask: netResult.Config.Netmask,
	}

	// Add optional fields if present
	if netResult.MAC != "" {
		labels[labelMAC] = netResult.MAC
	}
	if netResult.TAPName != "" {
		labels[labelTAP] = netResult.TAPName
	}

	// Create containerd client
	client, err := containerd.New(containerdAddress, containerd.WithDefaultNamespace(ns))
	if err != nil {
		log.G(ctx).WithError(err).Warn("skipping label update: failed to create containerd client")
		return
	}
	defer client.Close()

	// Load container and update labels
	container, err := client.LoadContainer(ctx, containerID)
	if err != nil {
		log.G(ctx).WithError(err).WithField("container", containerID).Warn("skipping label update: failed to load container")
		return
	}

	if _, err := container.SetLabels(ctx, labels); err != nil {
		log.G(ctx).WithError(err).WithField("container", containerID).Warn("failed to update container labels")
		return
	}

	log.G(ctx).WithFields(log.Fields{
		"container": containerID,
		"cid":       cid,
		"ip":        netResult.Config.IP,
		"gateway":   netResult.Config.Gateway,
	}).Info("updated container labels with VM runtime metadata")
}
