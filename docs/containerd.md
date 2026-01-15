# Containerd Integration

This document describes how to use the spinbox runtime with containerd to run containers inside QEMU/KVM virtual machines.

## Overview

Spinbox is a containerd shim runtime (`io.containerd.spinbox.v1`) that executes containers inside lightweight QEMU/KVM virtual machines. Each container runs in its own isolated VM, providing strong security boundaries.

## Quick Start

### Using ctr CLI

```bash
# Basic container in a VM
ctr run --rm --runtime io.containerd.spinbox.v1 \
  docker.io/library/alpine:latest \
  my-container \
  /bin/sh -c "echo hello from VM"

# With custom files injected into the VM
ctr run --rm --runtime io.containerd.spinbox.v1 \
  --label 'io.spin.extras.files={"/usr/local/bin/myapp":{"source":"/host/path/myapp","mode":493}}' \
  docker.io/library/alpine:latest \
  my-container \
  /usr/local/bin/myapp
```

### Using Go Client

```go
package main

import (
    "context"

    "github.com/containerd/containerd/v2/client"
    "github.com/containerd/containerd/v2/pkg/cio"
    "github.com/containerd/containerd/v2/pkg/namespaces"
    "github.com/containerd/containerd/v2/pkg/oci"
)

func main() {
    ctx := namespaces.WithNamespace(context.Background(), "default")

    c, err := client.New("/run/containerd/containerd.sock")
    if err != nil {
        panic(err)
    }
    defer c.Close()

    image, err := c.GetImage(ctx, "docker.io/library/alpine:latest")
    if err != nil {
        panic(err)
    }

    container, err := c.NewContainer(ctx, "my-vm-container",
        client.WithImage(image),
        client.WithNewSnapshot("my-vm-snapshot", image),
        client.WithNewSpec(
            oci.WithImageConfig(image),
            oci.WithProcessArgs("/bin/sh", "-c", "echo hello"),
        ),
        // Use spinbox runtime
        client.WithRuntime("io.containerd.spinbox.v1", nil),
    )
    if err != nil {
        panic(err)
    }
    defer container.Delete(ctx, client.WithSnapshotCleanup)

    task, err := container.NewTask(ctx, cio.NewCreator(cio.WithStdio))
    if err != nil {
        panic(err)
    }
    defer task.Delete(ctx)

    if err := task.Start(ctx); err != nil {
        panic(err)
    }

    exitCh, _ := task.Wait(ctx)
    status := <-exitCh
    fmt.Printf("Exited with code: %d\n", status.ExitCode())
}
```

## Injecting Files into the VM

The `io.spin.extras.files` annotation allows injecting arbitrary files from the host into the VM. Files are specified as a JSON object mapping destination paths (in the VM) to source configurations.

### Annotation Format

```json
{
  "<destPath>": {
    "source": "<hostPath>",
    "mode": <permissions>
  }
}
```

| Field | Description |
|-------|-------------|
| `destPath` (key) | Absolute path where the file will be placed inside the VM |
| `source` | Absolute path to the source file on the host |
| `mode` | File permissions in decimal (493 = 0755, 420 = 0644) |

### Example: Single File

```bash
ctr run --rm --runtime io.containerd.spinbox.v1 \
  --label 'io.spin.extras.files={"/usr/local/bin/myapp":{"source":"/opt/bin/myapp","mode":493}}' \
  docker.io/library/alpine:latest \
  my-container \
  /usr/local/bin/myapp
```

### Example: Multiple Files

```bash
ctr run --rm --runtime io.containerd.spinbox.v1 \
  --label 'io.spin.extras.files={
    "/usr/local/bin/app":{"source":"/host/bin/app","mode":493},
    "/etc/app/config.json":{"source":"/host/configs/app.json","mode":420},
    "/usr/local/bin/helper":{"source":"/host/bin/helper","mode":493}
  }' \
  docker.io/library/alpine:latest \
  my-container \
  /usr/local/bin/app
```

### Go Client Example

```go
import "encoding/json"

// Define files to inject
extraFiles := map[string]struct {
    Source string `json:"source"`
    Mode   int64  `json:"mode"`
}{
    "/usr/local/bin/myapp":   {Source: "/host/path/to/myapp", Mode: 0755},
    "/etc/myapp/config.json": {Source: "/host/configs/app.json", Mode: 0644},
}
extraFilesJSON, _ := json.Marshal(extraFiles)

container, err := c.NewContainer(ctx, "my-container",
    client.WithImage(image),
    client.WithNewSnapshot("snapshot", image),
    client.WithNewSpec(oci.WithImageConfig(image)),
    client.WithRuntime("io.containerd.spinbox.v1", nil),
    client.WithContainerLabels(map[string]string{
        "io.spin.extras.files": string(extraFilesJSON),
    }),
)
```

### How It Works

1. The shim parses the `io.spin.extras.files` annotation
2. Creates a tar archive containing all specified files
3. Caches the tar by content hash (for efficiency)
4. Attaches the tar as a read-only virtio-blk device to the VM
5. Guest extracts files to their destination paths on boot
6. Extraction is idempotent (runs once per boot)

## Supervisor Agent

The supervisor agent provides additional capabilities inside the VM for integration with external control planes. To use the supervisor:

1. Inject the supervisor binary via `io.spin.extras.files`
2. Enable supervisor and provide runtime configuration via annotations

### Required Setup

The supervisor binary must be placed at `/run/spin-stack/spin-supervisor` inside the VM:

```json
{
  "/run/spin-stack/spin-supervisor": {
    "source": "/usr/share/spin-stack/bin/spin-supervisor",
    "mode": 493
  }
}
```

### Supervisor Annotations

| Annotation | Required | Description |
|------------|----------|-------------|
| `io.spin.supervisor.enabled` | Yes | Set to `"true"` to enable supervisor |
| `io.spin.supervisor.workspace_id` | Yes | Workspace UUID |
| `io.spin.supervisor.secret` | Yes | 64-character hex-encoded HMAC secret |
| `io.spin.supervisor.control_plane` | Yes | Control plane URL |
| `io.spin.supervisor.metadata_addr` | No | Metadata service address (default: `169.254.169.254:80`) |

### Complete Supervisor Example

```bash
ctr run --rm --runtime io.containerd.spinbox.v1 \
  --label 'io.spin.extras.files={"/run/spin-stack/spin-supervisor":{"source":"/usr/share/spin-stack/bin/spin-supervisor","mode":493}}' \
  --label io.spin.supervisor.enabled=true \
  --label io.spin.supervisor.workspace_id=550e8400-e29b-41d4-a716-446655440000 \
  --label io.spin.supervisor.secret=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef \
  --label io.spin.supervisor.control_plane=https://control.example.com:443 \
  docker.io/library/alpine:latest \
  my-container \
  /bin/sh
```

### Go Client with Supervisor

```go
import "encoding/json"

// Files including supervisor binary
extraFiles := map[string]struct {
    Source string `json:"source"`
    Mode   int64  `json:"mode"`
}{
    // Supervisor binary - required path
    "/run/spin-stack/spin-supervisor": {
        Source: "/usr/share/spin-stack/bin/spin-supervisor",
        Mode:   0755,
    },
    // Additional application files
    "/usr/local/bin/myapp": {
        Source: "/host/path/to/myapp",
        Mode:   0755,
    },
}
extraFilesJSON, _ := json.Marshal(extraFiles)

container, err := c.NewContainer(ctx, "my-container",
    client.WithImage(image),
    client.WithNewSnapshot("snapshot", image),
    client.WithNewSpec(
        oci.WithImageConfig(image),
        oci.WithProcessArgs("/usr/local/bin/myapp"),
    ),
    client.WithRuntime("io.containerd.spinbox.v1", nil),
    client.WithContainerLabels(map[string]string{
        // Inject files
        "io.spin.extras.files": string(extraFilesJSON),
        // Supervisor configuration
        "io.spin.supervisor.enabled":       "true",
        "io.spin.supervisor.workspace_id":  "550e8400-e29b-41d4-a716-446655440000",
        "io.spin.supervisor.secret":        "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        "io.spin.supervisor.control_plane": "https://control.example.com:443",
    }),
)
```

## Annotations Reference

### File Injection

| Annotation | Format | Description |
|------------|--------|-------------|
| `io.spin.extras.files` | JSON object | Files to inject into VM (see format above) |

### Supervisor Configuration

| Annotation | Format | Description |
|------------|--------|-------------|
| `io.spin.supervisor.enabled` | `"true"` | Enable supervisor agent |
| `io.spin.supervisor.workspace_id` | UUID string | Workspace identifier |
| `io.spin.supervisor.secret` | 64-char hex | HMAC secret for authentication |
| `io.spin.supervisor.control_plane` | URL | Control plane endpoint |
| `io.spin.supervisor.metadata_addr` | `host:port` | Metadata service (default: `169.254.169.254:80`) |

## File Permission Reference

Common permission values (decimal):

| Octal | Decimal | Description |
|-------|---------|-------------|
| 0755 | 493 | Executable (rwxr-xr-x) |
| 0644 | 420 | Regular file (rw-r--r--) |
| 0600 | 384 | Private file (rw-------) |
| 0700 | 448 | Private executable (rwx------) |

## Troubleshooting

### Files Not Appearing in VM

1. Verify source paths exist on the host
2. Check paths are absolute (both source and destination)
3. Review shim logs: `journalctl -u containerd`

### Supervisor Not Starting

1. Ensure binary is at exact path: `/run/spin-stack/spin-supervisor`
2. Verify all required annotations are set
3. Check supervisor logs inside VM: `/var/log/spin-supervisor.log`

### Force Re-extraction

Add `spin.extras_force=1` to kernel cmdline to force file re-extraction on boot (useful for debugging).
