# tent

A cross-platform command-line tool for creating, managing, and destroying lightweight VMs as isolated development environments. Written in Go. Runs on **macOS and Linux**.

## Overview

`tent` provisions and manages lightweight VMs as isolated dev environments. Each VM gets its own kernel, filesystem, and network stack — true isolation without the overhead of traditional VMs. Think of it as "docker for VMs" but with real hardware-level separation.

Unlike tools that wrap an external hypervisor binary, `tent` drives the host hypervisor directly:
- **Linux:** KVM via `/dev/kvm` ioctl interface
- **macOS:** Apple's Hypervisor.framework (or Virtualization.framework)

The agent building this project should **write as much code as possible**. Do not shell out to external binaries for core VM functionality. Use thin Go libraries for the KVM/Hypervisor.framework syscall layer (the same way you use `cobra` for the CLI), but write everything above that: the VM lifecycle orchestration, virtio device emulation, boot protocol, networking, storage, and all glue code. The goal is a single self-contained binary with no runtime dependencies beyond the OS kernel.

## Commands

- `tent create <name> [--config <path>]` — Create a new VM from a base image or YAML config
- `tent start <name>` — Boot a stopped VM
- `tent stop <name>` — Gracefully shut down a running VM
- `tent destroy <name>` — Remove a VM and all its associated resources (rootfs, network, state)
- `tent list` — List all VMs with status, IP, resource usage
- `tent ssh <name>` — SSH into a running VM
- `tent status <name>` — Detailed status of a specific VM
- `tent logs <name>` — View VM console/boot logs
- `tent snapshot create <name> <tag>` — Snapshot a VM's state
- `tent snapshot restore <name> <tag>` — Restore from a snapshot
- `tent snapshot list <name>` — List available snapshots
- `tent network list` — List network devices managed by tent
- `tent image list` — List available base rootfs images
- `tent image pull <name>` — Download a base rootfs image

## Goals

- **Cross-platform:** First-class support for both macOS and Linux from a single codebase
- Full VM lifecycle via CLI: create, start, stop, destroy
- Drive the host hypervisor directly — no external hypervisor binaries (no Firecracker, no QEMU)
- Networking: platform-native networking (TAP/bridge on Linux, vmnet on macOS), DHCP, port forwarding
- Configuration via YAML (vCPUs, memory, disk, network, kernel, mounts)
- Filesystem management: rootfs provisioning from base images, disk image creation, host directory mounts
- Snapshot and restore (full VM state)
- Fast boot times (sub-second target)
- State tracking: persistent local state for all managed VMs
- Clean teardown: destroying a VM cleans up all resources (network devices, rootfs, state)
- Write as much code as possible — maximize lines of original code, minimize shelling out
- Comprehensive test coverage with unit and integration tests

## Non-Goals

- Not a cloud deployment tool — local VMs only
- No GUI — CLI only
- Not a container runtime — true VM isolation via hardware virtualization
- No multi-host orchestration
- No Kubernetes integration
- Do not reimplement KVM ioctls or Hypervisor.framework syscalls from scratch — use existing thin Go bindings for those

## Architecture

### Cross-Platform Design

`tent` uses a **platform abstraction layer**. All VM operations go through a `hypervisor.Backend` interface. Each platform provides its own implementation:

```
                    ┌─────────────────────────┐
                    │      tent CLI           │
                    │   (cobra commands)       │
                    └────────┬────────────────┘
                             │
                    ┌────────▼────────────────┐
                    │     VM Manager           │
                    │  (lifecycle orchestration)│
                    └────────┬────────────────┘
                             │
                ┌────────────▼────────────────┐
                │   hypervisor.Backend        │
                │   (platform interface)       │
                ├─────────────┬───────────────┤
                │             │               │
         ┌──────▼──────┐ ┌───▼────────────┐  │
         │ kvm.Backend │ │ hvf.Backend    │  │
         │  (Linux)    │ │ (macOS)        │  │
         │             │ │                │  │
         │ /dev/kvm    │ │ Hypervisor.fwk │  │
         │ ioctl calls │ │ or Vz.fwk     │  │
         └─────────────┘ └────────────────┘  │
                                              │
                ┌─────────────────────────────┤
                │                             │
         ┌──────▼──────┐  ┌──────▼──────────┐
         │  Networking  │  │    Storage      │
         │  (per-plat)  │  │  (cross-plat)   │
         └─────────────┘  └─────────────────┘
```

### Directory Layout

```
project/
├── cmd/tent/              # CLI entry point (main.go, command files)
├── internal/
│   ├── hypervisor/         # Platform abstraction
│   │   ├── backend.go      # Backend interface definition
│   │   ├── kvm/            # Linux KVM backend
│   │   └── hvf/            # macOS Hypervisor.framework backend
│   ├── vm/                 # VM lifecycle: create, start, stop, destroy, status
│   ├── virtio/             # Virtio device emulation (block, net, console)
│   ├── boot/               # Linux boot protocol, kernel loading
│   ├── network/            # Platform-aware networking
│   │   ├── network.go      # Network manager interface
│   │   ├── tap_linux.go    # TAP/bridge on Linux
│   │   └── vmnet_darwin.go # vmnet framework on macOS
│   ├── storage/            # Rootfs creation, base image management, snapshots
│   ├── config/             # YAML config parsing and validation
│   └── state/              # Local state persistence (JSON)
├── pkg/
│   └── models/             # Shared types (VMConfig, VMState, NetworkConfig, etc.)
├── testdata/               # Test fixtures and sample configs
├── go.mod
├── go.sum
└── Makefile
```

### Core Components

- **CLI** (`cmd/tent/`) — cobra-based command tree, flag parsing, output formatting
- **VM Manager** (`internal/vm/`) — Orchestrates the full VM lifecycle by coordinating hypervisor backend, network, and storage. Platform-agnostic — talks only to interfaces.
- **Hypervisor Backend** (`internal/hypervisor/`) — Platform interface + implementations. On Linux, drives KVM via ioctl (using a thin Go KVM library). On macOS, drives Hypervisor.framework or Virtualization.framework via cgo. Each backend handles: vCPU creation, memory mapping, device attachment, VM start/stop.
- **Virtio Devices** (`internal/virtio/`) — Implements virtio device emulation in pure Go: virtio-blk (block devices), virtio-net (networking), virtio-console (serial console). This is code tent writes itself — not an external library.
- **Boot** (`internal/boot/`) — Linux boot protocol implementation: loads vmlinuz, sets up boot parameters, initial ramdisk. Written in Go.
- **Network Manager** (`internal/network/`) — Platform-specific networking behind a common interface. Linux: creates/destroys TAP devices, manages bridge interfaces, configures iptables for port forwarding, assigns IPs via embedded DHCP. macOS: uses vmnet.framework for VM networking, configures NAT via PF.
- **Storage Manager** (`internal/storage/`) — Creates disk images from base images, manages overlay/snapshot layers, handles host-to-guest directory mounts. Cross-platform.
- **Config** (`internal/config/`) — Parses and validates YAML VM configs, provides platform-aware defaults
- **State** (`internal/state/`) — Persists VM state to `~/.tent/state.json`, tracks running/stopped/created status, PIDs, IPs, paths

### VM Configuration Format

```yaml
name: my-dev-env
vcpus: 2
memory_mb: 1024
kernel: default            # uses built-in vmlinux or path to custom kernel
rootfs: ubuntu-22.04       # base image name or path to custom rootfs
disk_gb: 10                # rootfs size
network:
  mode: nat                # nat (default, works everywhere) | bridge (Linux only)
  ports:
    - host: 8080
      guest: 80
    - host: 2222
      guest: 22
mounts:
  - host: ./src
    guest: /workspace
    readonly: false
env:
  EDITOR: vim
  LANG: en_US.UTF-8
```

### How a VM Boots

1. Parse config, select hypervisor backend for current OS
2. Create disk image from base rootfs (or use existing)
3. Set up network device (TAP+bridge on Linux, vmnet on macOS)
4. Initialize hypervisor backend: allocate VM, map guest memory, create vCPUs
5. Attach virtio devices: virtio-blk (rootfs), virtio-net (network), virtio-console (serial)
6. Load kernel + initrd into guest memory using Linux boot protocol
7. Start vCPU threads — guest begins executing
8. Track PID, socket paths, network info in local state
9. Stop via guest shutdown signal or host-side vCPU halt
10. Destroy cleans up: stop VM, remove network devices, optionally remove disk image, remove state

### Networking Model

**Linux:**
```
[VM eth0] <--virtio-net--> [tapN] <--bridge--> [tent0] <--iptables/NAT--> [host network]
```
- `tent0` bridge created on first `tent create` if it doesn't exist
- Each VM gets a unique TAP device
- DHCP server on the bridge assigns IPs to VMs (172.16.0.0/24 default subnet)
- Port forwarding via iptables DNAT rules

**macOS:**
```
[VM eth0] <--virtio-net--> [vmnet interface] <--NAT--> [host network]
```
- Uses vmnet.framework shared mode (NAT, automatic IP assignment)
- Port forwarding via PF rules or userspace proxy
- No bridge setup required — vmnet handles it

### Allowed External Dependencies

The principle is: **write as much as possible, but don't reimplement kernel interfaces.**

| Dependency | Why It's OK |
|---|---|
| `cobra` | CLI framework — same category as stdlib |
| `yaml.v3` | YAML parsing — commodity |
| A thin Go KVM library | Wraps `/dev/kvm` ioctls — this is kernel ABI, not application logic |
| cgo for Hypervisor.framework / vmnet | Required to call macOS frameworks — no pure-Go alternative |

Everything else — VM orchestration, virtio device emulation, boot protocol, networking setup, DHCP, storage management, state tracking — is code that tent writes itself.

## Development

### Prerequisites

**Both platforms:**
- Go 1.22+

**Linux:**
- KVM support (`/dev/kvm` must exist)
- Root or sudo for TAP/bridge network device setup

**macOS:**
- macOS 11+ (Big Sur or later) for Hypervisor.framework
- Entitlement for hypervisor access (automatic in dev builds)
- Admin privileges for vmnet network setup

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

Tests should be cross-platform wherever possible. Platform-specific tests use build tags. Tests that require KVM or Hypervisor.framework access should be gated behind an integration test flag.

### Lint

```bash
go vet ./...
```
