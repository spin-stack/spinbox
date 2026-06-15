//go:build linux && integration

package integration

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
)

// waitForTaskStatus polls the task status until it reaches want or the timeout
// elapses, failing the test on timeout.
func waitForTaskStatus(t *testing.T, ctx context.Context, task containerd.Task, want containerd.ProcessStatus) {
	t.Helper()

	deadline := time.Now().Add(15 * time.Second)
	var last containerd.ProcessStatus
	for time.Now().Before(deadline) {
		st, err := task.Status(ctx)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		last = st.Status
		if last == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for status %q, last status was %q", want, last)
}

// TestContainerdPauseResume verifies the full pause/resume path end to end:
// containerd → shim → QMP stop/cont on the live VM. Pause suspends the VM's
// vCPUs and the shim reports PAUSED from cached state (the frozen guest cannot
// answer), then Resume restarts the vCPUs and the guest becomes responsive again.
func TestContainerdPauseResume(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	name := "qbx-pause-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(name+"-snapshot", image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithProcessArgs("/bin/sleep", "300"),
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

	exitCh, err := task.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for task %s: %v", name, err)
	}

	if err := task.Start(ctx); err != nil {
		t.Fatalf("start task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Running)

	// Pause: suspend the VM's vCPUs (QMP stop) through the full stack.
	if err := task.Pause(ctx); err != nil {
		t.Fatalf("pause task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Paused)

	// Resume: restart the vCPUs (QMP cont); the guest answers State again.
	if err := task.Resume(ctx); err != nil {
		t.Fatalf("resume task %s: %v", name, err)
	}
	waitForTaskStatus(t, ctx, task, containerd.Running)

	// Stop the container.
	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatalf("kill task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for task %s to exit after kill", name)
	}
}

// lastTick returns the highest N from "TICK N" lines in s, or -1 if none.
func lastTick(s string) int {
	last := -1
	for _, line := range strings.Split(s, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "TICK" {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				last = n
			}
		}
	}
	return last
}

func readLastTick(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stdout %s: %v", path, err)
	}
	return lastTick(string(data))
}

// TestContainerdPauseFreezesExecution proves that Pause actually halts the
// container (FIFREEZE + QMP stop) and Resume restarts it, observed end to end
// via the container's stdout on the host. A ticker prints "TICK n" every 250ms;
// while paused the whole VM is frozen, so the host-side stdout stops advancing,
// and after Resume it advances again.
func TestContainerdPauseFreezesExecution(t *testing.T) {
	cfg := loadTestConfig()

	client := setupContainerdClient(t, cfg)
	defer client.Close()

	ensureImagePulled(t, client, cfg)

	ctx := namespaces.WithNamespace(t.Context(), cfg.Namespace)

	image, err := client.GetImage(ctx, cfg.Image)
	if err != nil {
		t.Fatalf("get image %s: %v", cfg.Image, err)
	}

	stdoutPath := filepath.Join(t.TempDir(), "stdout.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatalf("create stdout file: %v", err)
	}
	defer stdoutFile.Close()

	name := "qbx-freeze-" + strings.ReplaceAll(time.Now().Format("150405.000"), ".", "")

	container, err := client.NewContainer(ctx, name,
		containerd.WithSnapshotter(cfg.Snapshotter),
		containerd.WithImage(image),
		containerd.WithNewSnapshot(name+"-snapshot", image),
		containerd.WithRuntime(cfg.Runtime, nil),
		containerd.WithNewSpec(
			oci.WithImageConfig(image),
			oci.WithProcessArgs("/bin/sh", "-c", "i=0; while true; do i=$((i+1)); echo TICK $i; sleep 0.25; done"),
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

	exitCh, err := task.Wait(ctx)
	if err != nil {
		t.Fatalf("wait for task %s: %v", name, err)
	}
	if err := task.Start(ctx); err != nil {
		t.Fatalf("start task %s: %v", name, err)
	}

	// Let the ticker run so stdout is flowing.
	time.Sleep(1500 * time.Millisecond)
	if got := readLastTick(t, stdoutPath); got < 1 {
		t.Fatalf("expected ticks before pause, got last=%d", got)
	}

	// Pause: freeze the fs and stop the vCPUs.
	if err := task.Pause(ctx); err != nil {
		t.Fatalf("pause task %s: %v", name, err)
	}

	// Let any in-flight stdout drain, then sample the tick at pause start.
	time.Sleep(300 * time.Millisecond)
	pauseStart := readLastTick(t, stdoutPath)

	// While paused the VM is frozen: stdout must not advance.
	time.Sleep(2500 * time.Millisecond)
	pauseEnd := readLastTick(t, stdoutPath)
	if pauseEnd-pauseStart > 1 {
		t.Fatalf("expected execution frozen while paused, but ticks advanced %d -> %d", pauseStart, pauseEnd)
	}

	// Resume: execution (and stdout) must advance again.
	if err := task.Resume(ctx); err != nil {
		t.Fatalf("resume task %s: %v", name, err)
	}
	time.Sleep(1500 * time.Millisecond)
	if afterResume := readLastTick(t, stdoutPath); afterResume <= pauseEnd {
		t.Fatalf("expected execution to resume, ticks stuck at %d (was %d before resume)", afterResume, pauseEnd)
	}

	if err := task.Kill(ctx, syscall.SIGKILL); err != nil {
		t.Fatalf("kill task %s: %v", name, err)
	}
	select {
	case <-exitCh:
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for task %s to exit after kill", name)
	}
}
