# tent

A command-line tool for creating, managing, and destroying microVMs as lightweight, isolated development environments. Written in Go.

## Overview

`tent` provisions and manages microVMs (via Firecracker) as isolated dev environments. Each microVM gets its own kernel, filesystem, and network stack — true isolation without the overhead of traditional VMs. Think of it as "docker for VMs" but with real hardware-level separation.

## Commands

- `tent create <name> [--config <path>]` — Create a new microVM from a base image or YAML config
- `tent start <name>` — Boot a stopped microVM
- `tent stop <name>` — Gracefully shut down a running microVM
- `tent destroy <name>` — Remove a microVM and all its associated resources (rootfs, network, state)
- `tent list` — List all microVMs with status, IP, resource usage
- `tent ssh <name>` — SSH into a running microVM
- `tent status <name>` — Detailed status of a specific microVM
- `tent logs <name>` — View microVM console/boot logs
- `tent snapshot create <name> <tag>` — Snapshot a microVM's state
- `tent snapshot restore <name> <tag>` — Restore from a snapshot
- `tent snapshot list <name>` — List available snapshots
- `tent network list` — List bridges and TAP devices managed by tent
- `tent image list` — List available base rootfs images
- `tent image pull <name>` — Download a base rootfs image

## Goals

- Full microVM lifecycle via CLI: create, start, stop, destroy
- Networking: bridge networking with TAP devices, DHCP, port forwarding, inter-VM communication
- Configuration via YAML (vCPUs, memory, disk, network, kernel, mounts)
- Filesystem management: rootfs provisioning from base images, ext4 creation, host directory mounts
- Snapshot and restore (full VM state)
- Fast boot times (sub-second target with Firecracker)
- State tracking: persistent local state for all managed VMs
- Clean teardown: destroying a VM cleans up all resources (TAP devices, bridges, rootfs, state)
- Comprehensive test coverage with unit and integration tests

## Non-Goals

- Not a cloud deployment tool — local microVMs only
- No GUI — CLI only
- Not a container runtime — true VM isolation via hardware virtualization
- No multi-host orchestration
- No Kubernetes integration

## Architecture

### Directory Layout

```
project/
├── cmd/tent/           # CLI entry point (main.go)
├── internal/
│   ├── vm/             # VM lifecycle: create, start, stop, destroy, status
│   ├── network/        # TAP device setup, bridge management, DHCP, port forwarding
│   ├── storage/        # Rootfs creation (ext4), base image management, snapshots
│   ├── config/         # YAML config parsing and validation
│   ├── state/          # Local state persistence (JSON) — tracks all managed VMs
│   └── firecracker/    # Firecracker API client (Unix socket, REST)
├── pkg/
│   └── models/         # Shared types (VMConfig, VMState, NetworkConfig, etc.)
├── testdata/           # Test fixtures and sample configs
├── go.mod
├── go.sum
└── Makefile
```

### Core Components

- **CLI** (`cmd/tent/`) — cobra-based command tree, flag parsing, output formatting
- **VM Manager** (`internal/vm/`) — Orchestrates the full VM lifecycle by coordinating firecracker, network, and storage
- **Firecracker Client** (`internal/firecracker/`) — Talks to Firecracker process via Unix socket REST API (boot source, drives, network interfaces, machine config, actions)
- **Network Manager** (`internal/network/`) — Creates/destroys TAP devices, manages bridge interfaces, configures iptables for port forwarding, assigns IPs via embedded DHCP
- **Storage Manager** (`internal/storage/`) — Creates ext4 rootfs images from base images, manages overlay/snapshot layers, handles host-to-guest directory mounts (via virtio)
- **Config** (`internal/config/`) — Parses and validates YAML VM configs, provides defaults
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
  mode: bridge             # bridge | nat
  bridge: tent0            # bridge interface name
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

### Firecracker Integration

`tent` spawns and manages Firecracker processes directly:

1. Create a Unix socket path for the VM
2. Spawn `firecracker --api-sock <path>` as a child process
3. Configure the VM via the Firecracker REST API (PUT /boot-source, PUT /drives/rootfs, PUT /network-interfaces/eth0, PUT /machine-config)
4. Start the VM via PUT /actions (InstanceStart)
5. Track the PID and socket path in local state
6. Stop via PUT /actions (SendCtrlAltDel) or process signal
7. Destroy by killing the process and cleaning up socket, rootfs, TAP device

### Networking Model

Each VM gets a TAP device connected to a shared bridge (`tent0`):

```
[VM eth0] <--virtio--> [tapN] <--bridge--> [tent0] <--iptables/NAT--> [host network]
```

- `tent0` bridge is created on first `tent create` if it doesn't exist
- Each VM gets a unique TAP device (tap0, tap1, ...)
- DHCP server on the bridge assigns IPs to VMs (172.16.0.0/24 default subnet)
- Port forwarding via iptables DNAT rules
- Cleanup removes TAP devices and iptables rules; bridge removed when last VM is destroyed

## Development

### Prerequisites

- Go 1.22+
- Linux with KVM support (`/dev/kvm` must exist)
- Firecracker binary in PATH
- Root or sudo for network device setup

### Setup

```bash
cd project
go mod init github.com/dalinkstone/tent
go mod tidy
```

### Build

```bash
go build -o tent ./cmd/tent
```

### Test

```bash
go test ./... -v -count=1
```

### Lint

```bash
go vet ./...
```
