//go:build linux && integration

package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// bootTimelineRE matches the shim's BOOT_TIMELINE line, e.g.:
//
//	BOOT_TIMELINE qemu_launch_us=8123 guest_boot_us=146210 total_us=154333
var bootTimelineRE = regexp.MustCompile(`BOOT_TIMELINE qemu_launch_us=(\d+) guest_boot_us=(\d+) total_us=(\d+)`)

// annotationQemuMachine selects the QEMU machine type per container (mirrors
// the shim constant). Empty uses the backend default (q35).
const annotationQemuMachine = "io.spin.qemu.machine"

type bootTimeline struct {
	qemuLaunchUS int
	guestBootUS  int
	totalUS      int
}

// TestBootTimeline boots one VM on the normal (non-debug) path and reports the
// shim's BOOT_TIMELINE breakdown from the journal: how cold-start splits between
// QEMU launch (process + firmware/machine init until QMP) and guest boot (kernel
// + vminitd until its vsock RPC accepts). This is the wall-clock companion to the
// kernel/userspace profiles, which only cover slices of the guest side.
func TestBootTimeline(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	tl := bootAndMeasureTimeline(t, ctx, client, cfg, image, "")
	t.Logf("BOOT_TIMELINE qemu_launch_us=%d guest_boot_us=%d total_us=%d (%.1f ms total: %.1f ms qemu + %.1f ms guest)",
		tl.qemuLaunchUS, tl.guestBootUS, tl.totalUS,
		float64(tl.totalUS)/1000, float64(tl.qemuLaunchUS)/1000, float64(tl.guestBootUS)/1000)
}

// TestBootTimelineMachines boots the same workload on q35 and on pc (i440fx) and
// reports both BOOT_TIMELINE breakdowns plus the delta - the q35-vs-pc A/B. pc
// is the lighter chipset (still ACPI+PCI+virtio); a clear qemu_launch win argues
// for pursuing it (and the QEMU build strip), otherwise q35 stays.
func TestBootTimelineMachines(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	results := make(map[string]bootTimeline)
	for _, machine := range []string{"q35", "pc"} {
		tl := bootAndMeasureTimeline(t, ctx, client, cfg, image, machine)
		results[machine] = tl
		t.Logf("BOOT_TIMELINE[%s] qemu_launch_us=%d guest_boot_us=%d total_us=%d (%.1f ms = %.1f qemu + %.1f guest)",
			machine, tl.qemuLaunchUS, tl.guestBootUS, tl.totalUS,
			float64(tl.totalUS)/1000, float64(tl.qemuLaunchUS)/1000, float64(tl.guestBootUS)/1000)
	}

	q, p := results["q35"], results["pc"]
	t.Logf("BOOT_TIMELINE A/B (pc - q35): qemu_launch %+d us, guest_boot %+d us, total %+d us",
		p.qemuLaunchUS-q.qemuLaunchUS, p.guestBootUS-q.guestBootUS, p.totalUS-q.totalUS)
}

// bootAndMeasureTimeline boots one container (optionally pinned to a QEMU
// machine type via annotation) whose entrypoint prints BOOTED, then returns the
// BOOT_TIMELINE the shim emitted for that boot. machine "" uses the default.
func bootAndMeasureTimeline(t *testing.T, ctx context.Context, client *containerd.Client, cfg testConfig, image containerd.Image, machine string) bootTimeline {
	t.Helper()

	label := machine
	if label == "" {
		label = "default"
	}
	name := fmt.Sprintf("qbx-timeline-%s-%s", label, strings.ReplaceAll(time.Now().Format("150405.000"), ".", ""))

	stdoutPath := filepath.Join(t.TempDir(), "stdout.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer stdoutFile.Close()

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs("/bin/echo", "BOOTED"),
	}
	if machine != "" {
		specOpts = append(specOpts, oci.WithAnnotations(map[string]string{annotationQemuMachine: machine}))
	}

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(name+"-snapshot", image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(specOpts...),
	)
	if err != nil {
		t.Fatalf("create container %s: %v", name, err)
	}
	defer func() {
		if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
			t.Logf("cleanup container %s: %v", name, err)
		}
	}()

	// The VM boots (and emits BOOT_TIMELINE) during NewTask, so capture the
	// journal window from just before it.
	since := time.Now().Add(-1 * time.Second)

	task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStreams(nil, stdoutFile, nil)))
	if err != nil {
		t.Fatalf("create task for %s: %v", name, err)
	}
	defer func() {
		if _, err := task.Delete(ctx, containerd.WithProcessKill); err != nil {
			if !strings.Contains(err.Error(), "ttrpc: closed") {
				t.Logf("cleanup task for %s: %v", name, err)
			}
		}
	}()

	if _, err := task.Wait(ctx); err != nil {
		t.Fatalf("wait for task %s: %v", name, err)
	}
	if err := task.Start(ctx); err != nil {
		t.Fatalf("start task %s: %v", name, err)
	}
	waitForOutput(t, stdoutPath, "BOOTED", 60*time.Second)

	return readBootTimeline(t, since)
}

// readBootTimeline reads the shim journal since the given time and returns the
// most recent BOOT_TIMELINE entry (the boot this test just triggered).
func readBootTimeline(t *testing.T, since time.Time) bootTimeline {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if tl, ok := parseLastBootTimeline(journalSince(t, since)); ok {
			return tl
		}
		if time.Now().After(deadline) {
			t.Fatalf("no BOOT_TIMELINE line in spinbox journal since %s", since.Format(time.RFC3339))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// journalSince returns the spinbox unit's journal output since the given time.
func journalSince(t *testing.T, since time.Time) string {
	t.Helper()
	cmd := exec.Command("journalctl",
		"-u", "spinbox",
		"--since", since.Format("2006-01-02 15:04:05"),
		"--no-pager",
		"-o", "cat",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Logf("journalctl failed: %v (stderr: %s)", err, stderr.String())
		return ""
	}
	return stdout.String()
}

// parseLastBootTimeline returns the last BOOT_TIMELINE entry in the text.
func parseLastBootTimeline(text string) (bootTimeline, bool) {
	matches := bootTimelineRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return bootTimeline{}, false
	}
	m := matches[len(matches)-1]
	return bootTimeline{
		qemuLaunchUS: mustAtoi(m[1]),
		guestBootUS:  mustAtoi(m[2]),
		totalUS:      mustAtoi(m[3]),
	}, true
}

func mustAtoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(fmt.Sprintf("parse int %q: %v", s, err))
	}
	return n
}
