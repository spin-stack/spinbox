//go:build linux && integration

package integration

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// annotationDebugBoot enables per-VM kernel boot profiling (initcall_debug).
// Must match the shim constant.
const annotationDebugBoot = "io.spin.debug.boot"

// initcallRE matches a kernel initcall_debug completion line, e.g.:
//
//	initcall pci_subsys_init+0x0/0x40 returned 0 after 12345 usecs
var initcallRE = regexp.MustCompile(`initcall (\S+) returned \S+ after (\d+) usecs`)

type initcall struct {
	name string
	usec int
}

// logDirBase returns the host directory that holds per-container console logs.
func logDirBase() string {
	if v := os.Getenv("SPINBOX_LOG_DIR"); v != "" {
		return v
	}
	return "/var/log/spin-stack"
}

// TestKernelBootProfile boots a VM with the debug-boot annotation, which makes
// the kernel emit per-initcall timings (initcall_debug) to the console log,
// then parses that log and reports the slowest initcalls. This turns kernel
// boot detail into a CI artifact: run it to see where boot time goes and to
// spot a new slow initcall after a kernel/config change.
func TestKernelBootProfile(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	name := "qbx-kprofile-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(name+"-snapshot", image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithAnnotations(map[string]string{annotationDebugBoot: "true"}),
			oci.WithProcessArgs("/bin/echo", "BOOTED"),
		),
	)
	if err != nil {
		t.Fatalf("create container %s: %v", name, err)
	}
	defer func() {
		if err := container.Delete(ctx, containerd.WithSnapshotCleanup); err != nil {
			t.Logf("cleanup container %s: %v", name, err)
		}
	}()

	task, err := container.NewTask(ctx, cio.NullIO)
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
	waitForTaskStatus(t, ctx, task, containerd.Running)

	// Initcalls complete during kernel init, before the container starts, so
	// by the time the task is Running they are in the console log.
	consolePath := filepath.Join(logDirBase(), name, "console.log")
	calls := waitForInitcalls(t, consolePath, 30*time.Second)

	total := 0
	for _, c := range calls {
		total += c.usec
	}
	sort.Slice(calls, func(a, b int) bool { return calls[a].usec > calls[b].usec })

	t.Logf("KERNEL_PROFILE initcalls=%d total_usec=%d (%.1f ms)", len(calls), total, float64(total)/1000)
	const topN = 15
	for i, c := range calls {
		if i >= topN {
			break
		}
		t.Logf("KERNEL_PROFILE   %5d us  %s", c.usec, c.name)
	}
}

// waitForInitcalls polls the console log until it contains initcall_debug
// lines (the kernel may still be flushing) and returns the parsed entries.
func waitForInitcalls(t *testing.T, consolePath string, timeout time.Duration) []initcall {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		calls := parseInitcalls(consolePath)
		if len(calls) > 0 {
			return calls
		}
		if time.Now().After(deadline) {
			data, _ := os.ReadFile(consolePath) //nolint:errcheck // best-effort diagnostics
			t.Fatalf("no initcall_debug output in %s after %v (is the debug-boot annotation honored?); console tail:\n%s",
				consolePath, timeout, lastLines(string(data), 20))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// parseInitcalls extracts initcall timings from the console log.
func parseInitcalls(consolePath string) []initcall {
	data, err := os.ReadFile(consolePath)
	if err != nil {
		return nil
	}
	var calls []initcall
	for _, m := range initcallRE.FindAllStringSubmatch(string(data), -1) {
		usec, err := strconv.Atoi(m[2])
		if err != nil {
			continue
		}
		calls = append(calls, initcall{name: m[1], usec: usec})
	}
	return calls
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
