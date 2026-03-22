# tent

A cross-platform CLI for creating, managing, and orchestrating lightweight **microVM sandboxes**. Built for AI workloads, agentic runtimes, and automated pipelines. Written in Go. Runs on **macOS and Linux**.

## Overview

`tent` creates secure, hardware-isolated microVM sandboxes from any image — Docker images, OCI images, container registry images (GCR, ECR, Docker Hub), ISOs, or raw disk images. Each sandbox gets its own kernel, filesystem, and network stack with **controlled network access** — external traffic is blocked by default, with only explicitly allowlisted endpoints reachable.

Think of it as a secure sandbox runtime for AI agents: spin up an isolated environment, give it access to the APIs it needs (OpenRouter, Anthropic, etc.), block everything else, and let the agent run safely. Orchestrate multiple sandboxes together for multi-agent systems.

`tent` drives the host hypervisor directly:
- **macOS (primary platform):** Apple's Hypervisor.framework (or Virtualization.framework) — implement and test this first
- **Linux:** KVM via `/dev/kvm` ioctl interface, or optionally Firecracker as a VMM

The agent building this project should **write as much code as possible**. Do not just wrap or port an existing tool. Use thin Go libraries for kernel-level interfaces (KVM ioctls, Hypervisor.framework cgo bindings, OCI image spec), but write everything above that: sandbox lifecycle, virtio device emulation, boot protocol, networking, egress firewall, image conversion, orchestration, and state management. The goal is a single self-contained binary.

## Commands

### Sandbox lifecycle
- `tent create <name> --from <image-ref>` — Create a sandbox from a Docker/OCI image, registry image, ISO, or raw disk image
- `tent start <name>` — Boot a stopped sandbox
- `tent stop <name>` — Gracefully shut down a running sandbox
- `tent destroy <name>` — Remove a sandbox and all its resources (rootfs, network, state)
- `tent list` — List all sandboxes with status, IP, resource usage
- `tent status <name>` — Detailed status of a specific sandbox
- `tent exec <name> <command>` — Execute a command inside a running sandbox
- `tent ssh <name>` — SSH into a running sandbox
- `tent logs <name>` — View sandbox console/boot logs

### Images
- `tent image pull <ref>` — Pull an image from a registry (Docker Hub, GCR, ECR, any OCI registry)
- `tent image list` — List locally available images

### Network policy
- `tent network list` — List network devices and sandbox connectivity
- `tent network allow <name> <endpoint>` — Allow a sandbox to reach an external endpoint
- `tent network deny <name> <endpoint>` — Revoke access to an external endpoint
- `tent network status <name>` — Show a sandbox's network policy (allowed/denied endpoints)

### Orchestration
- `tent compose up <file>` — Start a multi-sandbox environment from a YAML definition
- `tent compose down <file>` — Stop and destroy all sandboxes in a compose group
- `tent compose status <file>` — Show status of all sandboxes in a compose group

### Snapshots
- `tent snapshot create <name> <tag>` — Snapshot a sandbox's state
- `tent snapshot restore <name> <tag>` — Restore from a snapshot
- `tent snapshot list <name>` — List available snapshots

## Goals

- **macOS-first:** macOS (Apple Silicon and Intel) is the primary development and testing platform. Every feature must work on macOS first, Linux second. Never shell out to Linux-only tools without providing a macOS equivalent behind build tags.
- **Cross-platform:** First-class support for both macOS and Linux from a single codebase
- **Secure by default:** External network access blocked — only allowlisted endpoints reachable
- **Image-agnostic:** Create sandboxes from Docker images, OCI images, registry images (GCR, ECR, Docker Hub), ISOs, or raw disk images
- **Inter-sandbox networking:** Sandboxes on the same host communicate via a private bridge network
- **Orchestration:** Define and manage multi-sandbox environments via YAML compose files
- **AI-native defaults:** Default allowlist includes common AI API endpoints (OpenRouter, Anthropic, Docker Model Runner, Codex)
- Full sandbox lifecycle via CLI: create, start, stop, destroy, exec
- Drive the host hypervisor directly — Firecracker is one optional backend, not the only path
- Platform-native networking (vmnet on macOS, TAP/bridge on Linux) with egress firewall
- Configuration via YAML (vCPUs, memory, disk, network policy, kernel, mounts, env)
- Fast boot times (sub-second target)
- State tracking: persistent local state for all managed sandboxes
- Clean teardown: destroying a sandbox cleans up all resources
- Write as much code as possible — maximize original code, don't just port existing tools
- Never require root/sudo for basic sandbox operations on macOS. Use pure-Go implementations where possible (disk images, filesystem creation) instead of shelling out to system tools.

## Non-Goals

- Not a cloud deployment tool — local sandboxes only
- No GUI — CLI only
- No multi-host orchestration (single machine only)
- No Kubernetes integration
- Do not reimplement KVM ioctls or Hypervisor.framework syscalls from scratch — use existing thin Go bindings
- Do not just wrap QEMU, Firecracker, or another existing tool as a black box — build the sandbox runtime with original code, using hypervisor APIs and optionally Firecracker as one backend behind your own abstraction

## Architecture

### High-Level Design

```
                    ┌─────────────────────────────────┐
                    │         tent CLI                │
                    │   (cobra commands)               │
                    └──────┬──────────────┬───────────┘
                           │              │
              ┌────────────▼──────┐  ┌────▼────────────┐
              │  Sandbox Manager  │  │  Compose Engine  │
              │  (single sandbox) │  │  (multi-sandbox) │
              └──────┬────────────┘  └────┬────────────┘
                     │                    │
        ┌────────────▼────────────────────▼─────┐
        │         hypervisor.Backend            │
        │         (platform interface)           │
        ├──────────┬──────────┬─────────────────┤
        │          │          │                 │
   ┌────▼────────┐ ┌───▼──────┐ ┌▼────────┐    │
   │ HVF / Vz.fwk│ │   KVM   │ │Firecrckr│    │
   │  (macOS)    │ │ (Linux)  │ │(Linux)  │    │
   │  PRIMARY    │ │          │ │optional │    │
   └─────────────┘ └─────────┘ └─────────┘    │
        │                                      │
   ┌────▼──────────────────────────────────────┤
   │                                           │
   ┌▼─────────┐ ┌▼──────────┐ ┌▼─────────────┐
   │  Image   │ │ Networking │ │   Storage    │
   │ Pipeline │ │ + Egress   │ │             │
   │(OCI/ISO) │ │  Firewall  │ │             │
   └──────────┘ └───────────┘ └──────────────┘
```

### Directory Layout

```
project/
├── cmd/tent/                # CLI entry point (main.go, command files)
├── internal/
│   ├── hypervisor/           # Platform abstraction
│   │   ├── backend.go        # Backend interface definition
│   │   ├── hvf/              # macOS Hypervisor.framework backend (PRIMARY — build first)
│   │   ├── kvm/              # Linux KVM backend
│   │   └── firecracker/      # Linux Firecracker backend (optional)
│   ├── sandbox/              # Sandbox lifecycle: create, start, stop, destroy, exec
│   ├── virtio/               # Virtio device emulation (block, net, console)
│   ├── boot/                 # Linux boot protocol, kernel loading
│   ├── image/                # Image pipeline: OCI/Docker pull, ISO extract, format detection
│   ├── network/              # Platform-aware networking + egress firewall
│   │   ├── manager.go        # Network manager interface
│   │   ├── policy.go         # Egress firewall / allowlist engine
│   │   ├── tap_linux.go      # TAP/bridge on Linux
│   │   └── vmnet_darwin.go   # vmnet framework on macOS
│   ├── compose/              # Multi-sandbox orchestration engine
│   ├── storage/              # Rootfs creation, snapshots, disk management
│   ├── config/               # YAML config parsing and validation
│   └── state/                # Local state persistence (JSON)
├── pkg/
│   └── models/               # Shared types
├── testdata/                 # Test fixtures and sample configs
├── go.mod
├── go.sum
└── Makefile
```

### Core Components

- **CLI** (`cmd/tent/`) — cobra-based command tree, flag parsing, output formatting
- **Sandbox Manager** (`internal/sandbox/`) — Orchestrates the full sandbox lifecycle by coordinating hypervisor backend, image pipeline, network, and storage. Platform-agnostic — talks only to interfaces.
- **Hypervisor Backend** (`internal/hypervisor/`) — Platform interface + implementations. **macOS (primary):** Hypervisor.framework or Virtualization.framework via cgo — implement this first. **Linux:** KVM via ioctl (thin Go bindings) or Firecracker as an optional VMM. Each backend handles: vCPU creation, memory mapping, device attachment, VM start/stop.
- **Image Pipeline** (`internal/image/`) — Converts any image source into a bootable rootfs. Pulls Docker/OCI images from registries (Docker Hub, GCR, ECR), extracts layers, handles ISOs, passes through raw disk images. Auto-detects format.
- **Virtio Devices** (`internal/virtio/`) — Virtio device emulation in pure Go: virtio-blk (block devices), virtio-net (networking), virtio-console (serial console).
- **Boot** (`internal/boot/`) — Linux boot protocol: loads vmlinuz, sets up boot parameters, initial ramdisk.
- **Network Manager** (`internal/network/`) — Platform-specific networking behind a common interface + **egress firewall**. Default policy: block all outbound traffic. Per-sandbox allowlists. Inter-sandbox communication via private bridge subnet. Embedded DHCP.
- **Compose Engine** (`internal/compose/`) — Multi-sandbox orchestration. Parses YAML compose files, starts/stops sandbox groups, manages shared networks, coordinates lifecycle.
- **Storage Manager** (`internal/storage/`) — Disk image management, snapshots, overlays. Cross-platform.
- **Config** (`internal/config/`) — Parses and validates YAML configs, provides platform-aware defaults
- **State** (`internal/state/`) — Persists sandbox state to `~/.tent/state.json`

### Sandbox Configuration Format

```yaml
name: my-agent
from: ubuntu:22.04           # Docker image, registry ref, ISO path, or raw disk image
vcpus: 2
memory_mb: 1024
disk_gb: 10
network:
  allow:                      # external endpoints this sandbox can reach (default: none)
    - api.anthropic.com
    - openrouter.ai
    - api.openai.com
  deny: []                    # explicit denials (overrides allow)
  ports:
    - host: 8080
      guest: 80
    - host: 2222
      guest: 22
mounts:
  - host: ./workspace
    guest: /workspace
    readonly: false
env:
  ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
  OPENROUTER_API_KEY: ${OPENROUTER_API_KEY}
```

### Compose Format (Multi-Sandbox)

```yaml
# tent-compose.yaml
sandboxes:
  agent:
    from: ubuntu:22.04
    vcpus: 2
    memory_mb: 2048
    network:
      allow: [api.anthropic.com, openrouter.ai]
    env:
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
  tool-runner:
    from: python:3.12-slim
    vcpus: 1
    memory_mb: 512
    network:
      allow: []               # no external access — can only talk to other sandboxes
  shared-db:
    from: postgres:16
    vcpus: 1
    memory_mb: 1024
    network:
      allow: []
```

All sandboxes in a compose group share a private network and can reach each other by name. External access is controlled per-sandbox.

### How a Sandbox Boots

1. Parse config, resolve `--from` image reference (Docker image, registry ref, ISO, raw disk)
2. Pull/convert image to rootfs via image pipeline (if not already cached)
3. Set up network device (TAP+bridge on Linux, vmnet on macOS)
4. Apply egress firewall rules (block all, then allow listed endpoints)
5. Initialize hypervisor backend: allocate VM, map guest memory, create vCPUs
6. Attach virtio devices: virtio-blk (rootfs), virtio-net (network), virtio-console (serial)
7. Load kernel + initrd into guest memory using Linux boot protocol
8. Start vCPU threads — guest begins executing
9. Track state: PID, socket paths, network info, image source
10. Stop via guest shutdown signal or host-side vCPU halt
11. Destroy cleans up: stop sandbox, remove network devices + firewall rules, optionally remove rootfs, remove state

### Networking Model

**Security model:**
```
[sandbox] --> [egress firewall] --> BLOCKED (default)
                                --> ALLOWED (if endpoint is in allowlist)
```
- Default allowlist for AI workloads: `api.anthropic.com`, `openrouter.ai`, `api.openai.com`, Docker Model Runner (local), Codex endpoints
- Per-sandbox overrides via `tent network allow/deny` or config YAML
- Inter-sandbox traffic on the same host is always allowed via the private bridge

**macOS (primary):**
```
[sandbox eth0] <--virtio-net--> [vmnet interface] <--PF/egress--> [host network]
```
- vmnet.framework provides NAT, DHCP, and inter-VM networking out of the box
- Egress firewall via PF rules or userspace proxy
- No root required for basic sandbox networking (vmnet shared mode)

**Linux:**
```
[sandbox eth0] <--virtio-net--> [tapN] <--bridge--> [tent0] <--iptables/egress--> [host network]
```

### Allowed External Dependencies

The principle is: **write as much as possible, but don't reimplement kernel interfaces or image specs.**

| Dependency | Why It's OK |
|---|---|
| `cobra` | CLI framework — same category as stdlib |
| `yaml.v3` | YAML parsing — commodity |
| A thin Go KVM library | Wraps `/dev/kvm` ioctls — kernel ABI |
| cgo for Hypervisor.framework / vmnet | Required to call macOS frameworks |
| `go-containerregistry` or similar | OCI image spec parsing + registry auth — this is a wire protocol, not application logic |
| Firecracker binary (optional, Linux) | One backend option — sits behind `hypervisor.Backend` interface |

Everything else — sandbox orchestration, virtio device emulation, boot protocol, networking, egress firewall, image conversion, compose engine, state tracking — is code that tent writes itself.

## Quickstart

### Build

```bash
cd project
go build -o tent ./cmd/tent
```

### Pull an image and create a sandbox

```bash
# Pull an image from Docker Hub (or any OCI registry)
tent image pull ubuntu:22.04

# Create a sandbox from the image
tent create my-sandbox --from ubuntu:22.04

# Start it
tent start my-sandbox
```

### Run commands and interact

```bash
# Execute a command inside the running sandbox
tent exec my-sandbox -- ls /
tent exec my-sandbox -- apt-get update

# SSH into the sandbox for interactive use
tent ssh my-sandbox

# View console/boot logs
tent logs my-sandbox
```

### Control network access

By default, all outbound traffic is blocked. Allowlist only what you need:

```bash
# Allow the sandbox to reach specific APIs
tent network allow my-sandbox api.anthropic.com
tent network allow my-sandbox openrouter.ai

# Check what's allowed
tent network status my-sandbox

# Revoke access
tent network deny my-sandbox openrouter.ai
```

### Orchestrate a multi-sandbox app

Create a `tent-compose.yaml`:

```yaml
sandboxes:
  agent:
    from: ubuntu:22.04
    vcpus: 2
    memory_mb: 2048
    network:
      allow: [api.anthropic.com, openrouter.ai]
    env:
      ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
    hooks:
      post_start:
        - /app/warmup.sh

  tool-runner:
    from: python:3.12-slim
    vcpus: 1
    memory_mb: 512
    depends_on: [agent]
    network:
      allow: []  # no external access — talks only to other sandboxes
    health_check:
      command: [curl, -f, http://localhost:8000/health]
      interval_sec: 30
    restart: on-failure

  shared-db:
    from: postgres:16
    vcpus: 1
    memory_mb: 1024
    network:
      allow: []

volumes:
  shared-data:
    size_mb: 1024
```

All sandboxes in a compose group share a private network and can reach each other by name (e.g., `tool-runner` can connect to `shared-db:5432`).

```bash
# Start everything (respects depends_on order)
tent compose up tent-compose.yaml

# Check status
tent compose status tent-compose.yaml

# View logs across all sandboxes
tent compose logs tent-compose.yaml --follow

# Run a command in a specific service
tent compose exec tent-compose.yaml agent -- curl http://tool-runner:8000/status

# Tear it all down
tent compose down tent-compose.yaml
```

### Clean up

```bash
tent stop my-sandbox
tent destroy my-sandbox
```

## Development

### Prerequisites

**Both platforms:**
- Go 1.22+

**macOS (primary development platform):**
- macOS 11+ (Big Sur or later) for Hypervisor.framework
- Entitlement for hypervisor access (automatic in dev builds)
- Admin privileges for vmnet network setup (first time only)
- No other system dependencies — everything else is pure Go or cgo to macOS frameworks

**Linux:**
- KVM support (`/dev/kvm` must exist)
- Root or sudo for TAP/bridge network device setup
- Optional: Firecracker binary for the Firecracker backend

### Build

```bash
cd project
go build -o tent ./cmd/tent
```

The binary auto-detects the platform at runtime. Build tags (`_linux.go`, `_darwin.go`) ensure only the relevant backend compiles on each OS.

### Test

```bash
go test ./... -v -count=1
```

### Lint

```bash
go vet ./...
```
