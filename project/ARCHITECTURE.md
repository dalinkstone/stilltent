# tent Architecture

This document describes the architecture of the `tent` microVM sandbox runtime.

## Overview

`tent` is a cross-platform CLI tool for creating, managing, and orchestrating lightweight microVM sandboxes. It drives the host hypervisor directly:
- **macOS (primary):** Apple's Hypervisor.framework (or Virtualization.framework)
- **Linux:** KVM via `/dev/kvm` ioctl interface

The goal is a single self-contained binary that doesn't rely on external hypervisor binaries like QEMU or Firecracker.

## High-Level Architecture

```
                    ┌─────────────────────────────────┐
                    │         tent CLI                │
                    │   (cobra commands)               │
                    └──────┬──────────────┬───────────┘
                           │              │
              ┌────────────▼──────┐  ┌────▼────────────┐
              │  VM Manager       │  │  Compose Engine  │
              │  (single sandbox) │  │  (multi-sandbox) │
              └──────┬────────────┘  └────┬────────────┘
                     │                    │
        ┌────────────▼────────────────────▼─────┐
        │         hypervisor.Backend            │
        │         (platform abstraction)         │
        ├──────────┬──────────┬─────────────────┤
        │          │          │                 │
   ┌────▼────────┐ ┌───▼──────┐ ┌▼────────┐    │
   │ HVF         │ │   KVM   │ │Firecrckr│    │
   │  (macOS)    │ │ (Linux)  │ │(optional)│    │
   │  PRIMARY    │ │          │ │(Linux)  │    │
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

## Directory Structure

```
project/
├── cmd/tent/                # CLI entry point
│   ├── main.go              # Command tree initialization
│   ├── create.go            # tent create command
│   ├── start.go             # tent start command
│   ├── stop.go              # tent stop command
│   ├── destroy.go           # tent destroy command
│   ├── list.go              # tent list command
│   ├── status.go            # tent status command
│   ├── ssh.go               # tent ssh command
│   ├── logs.go              # tent logs command
│   ├── snapshot.go          # tent snapshot command
│   ├── network.go           # tent network command
│   ├── image.go             # tent image command
│   ├── options.go           # Shared flag definitions
│   ├── mocks.go             # Test mocks for CLI testing
│   ├── cli_test.go          # Unit tests for CLI commands
│   ├── cli_integration_test.go  # Integration tests
│   └── cli_e2e_test.go      # End-to-end tests
├── internal/
│   ├── hypervisor/          # Platform abstraction
│   │   ├── backend.go       # Backend interface definition
│   │   ├── backend_test.go  # Interface tests
│   │   ├── hvf/             # macOS Hypervisor.framework backend
│   │   │   └── hvf_darwin.go
│   │   ├── kvm/             # Linux KVM backend
│   │   │   ├── kvm_linux.go
│   │   │   └── kvm_linux_test.go
│   │   └── firecracker/     # Optional Firecracker backend
│   ├── vm/                  # VM lifecycle manager
│   │   ├── manager.go       # VM manager implementation
│   │   ├── platform_darwin.go  # Darwin platform hooks
│   │   └── platform_linux.go   # Linux platform hooks
│   ├── network/             # Networking + egress firewall
│   │   ├── network.go       # Network manager implementation
│   │   ├── network_test.go  # Network manager tests
│   ├── storage/             # Disk management + snapshots
│   │   ├── storage.go       # Storage manager implementation
│   │   └── storage_test.go  # Storage manager tests
│   ├── image/               # Image pipeline (OCI/Docker/ISO)
│   ├── config/              # YAML config parsing
│   │   ├── config.go        # Config parser and validation
│   │   └── config_test.go   # Config parser tests
│   ├── state/               # Local state persistence
│   │   ├── state.go         # State manager implementation
│   │   └── state_test.go    # State manager tests
│   └── compose/             # Multi-sandbox orchestration
├── pkg/
│   └── models/              # Shared types
│       ├── types.go         # Core type definitions
│       ├── types_test.go    # Type tests
│       └── errors.go        # Error types
├── testdata/                # Test fixtures
│   ├── sample-state.json    # Sample state file
│   └── sample-config.yaml   # Sample config file
├── go.mod
├── go.sum
└── ARCHITECTURE.md          # This file
```

## Core Components

### CLI (`cmd/tent/`)

The CLI is implemented using the Cobra library and follows a **thin wrapper pattern**:

1. **Parse inputs** from command line flags and arguments
2. **Validate** the configuration
3. **Create VM manager** with dependency injection (interfaces for all dependencies)
4. **Call method** on VM manager
5. **Output** results

This pattern keeps CLI code simple and testable. All business logic lives in the VM manager.

```go
func createCommand() *cobra.Command {
    return &cobra.Command{
        Use:   "create <name> --from <image>",
        Short: "Create a new sandbox",
        RunE: func(cmd *cobra.Command, args []string) error {
            // Parse inputs
            name := args[0]
            from := cmd.Flags().GetString("from")
            // ... other flags
            
            // Create manager with injected dependencies
            manager, err := vm.NewManager(baseDir, stateManager, hvBackend, networkMgr, storageMgr)
            
            // Call method
            return manager.Create(name, from, config)
        },
    }
}
```

### VM Manager (`internal/vm/`)

The VM Manager orchestrates the full sandbox lifecycle:

**Key Responsibilities:**
- Create, start, stop, destroy sandboxes
- Coordinate with underlying subsystems
- Persist state to JSON file
- Track running VM instances

**Interfaces for Dependency Injection:**
- `StateManager` - State persistence (`~/.tent/state.json`)
- `HypervisorBackend` - Platform-specific VM operations
- `NetworkManager` - Network setup and cleanup
- `StorageManager` - Rootfs creation and snapshots

**Pattern:** Constructor injects all dependencies as interfaces, enabling easy testing with mocks.

### Hypervisor Backend (`internal/hypervisor/`)

The hypervisor abstraction provides a cross-platform interface:

```go
type Backend interface {
    CreateVM(config *models.VMConfig) (VM, error)
    ListVMs() ([]VM, error)
    DestroyVM(vm VM) error
}

type VM interface {
    Start() error
    Stop() error
    Kill() error
    Status() (models.VMStatus, error)
    GetConfig() *models.VMConfig
    GetIP() string
    GetPID() int
    Cleanup() error
}
```

**Implementations:**
- **KVM (`internal/hypervisor/kvm/`)** - Linux with `/dev/kvm` via `c35s/hype` library
- **HVF (`internal/hypervisor/hvf/`)** - macOS with Hypervisor.framework (stub)

### Network Manager (`internal/network/`)

Networking setup:

- **Linux:** Creates TAP devices and bridge interface (`tent0`)
- **macOS:** Uses vmnet.framework for NAT and inter-VM networking

**Egress Firewall:**
- Default: Block all outbound traffic
- Allowlists per-sandbox via configuration
- Inter-sandbox communication enabled by default

### Storage Manager (`internal/storage/`)

- Rootfs creation from container images, ISOs, or raw disk
- Snapshot creation and restoration
- Cross-platform disk image handling (no shell commands to external tools)

### Image Pipeline (`internal/image/`)

Converts any image source into a bootable rootfs:
- Pulls Docker/OCI images from registries
- Extracts layers
- Handles ISOs
- Passes through raw disk images
- Auto-detects format

## Platform Support

### macOS (Primary)

- **Hypervisor:** Hypervisor.framework (or Virtualization.framework)
- **Networking:** vmnet.framework
- **Build tags:** `//go:build darwin`
- **Status:** HVF backend stub implemented; full implementation pending

### Linux (Secondary)

- **Hypervisor:** KVM via `/dev/kvm` ioctl
- **Networking:** TAP + bridge (`tent0`)
- **Build tags:** `//go:build linux`
- **Status:** KVM backend fully implemented

## Configuration

Sandbox configuration is YAML-based:

```yaml
name: my-agent
from: ubuntu:22.04           # Docker image, registry ref, ISO path, or raw disk
vcpus: 2
memory_mb: 1024
disk_gb: 10
network:
  allow:                      # External endpoints (default: none)
    - api.anthropic.com
    - openrouter.ai
  deny: []                    # Explicit denials
  ports:
    - host: 8080
      guest: 80
mounts:
  - host: ./workspace
    guest: /workspace
    readonly: false
env:
  ANTHROPIC_API_KEY: ${ANTHROPIC_API_KEY}
```

## State Management

All sandbox state is persisted to `~/.tent/state.json`:

```json
{
  "name": "my-agent",
  "status": "running",
  "pid": 12345,
  "ip": "172.16.0.2",
  "socket_path": "/tmp/tent-my-agent.sock",
  "rootfs_path": "/home/user/.tent/vms/my-agent/rootfs.img",
  "tap_device": "tap-my-agent",
  "created_at": 1711000000,
  "updated_at": 1711000100
}
```

## Build System

```bash
# Build for current platform
go build -o tent ./cmd/tent

# Build for macOS (from any platform)
GOOS=darwin go build -o tent-darwin ./cmd/tent

# Build for Linux (from any platform)
GOOS=linux go build -o tent-linux ./cmd/tent

# Run tests
go test ./... -v -count=1

# Lint
go vet ./...
```

## Development Patterns

### Thin CLI Wrapper Pattern

CLI commands are thin wrappers that:
1. Parse and validate inputs
2. Create manager with injected dependencies
3. Call manager methods
4. Format and output results

### Dependency Injection Pattern

All major interfaces are injected via constructor:

```go
func NewManager(baseDir string, stateManager StateManager, hv HypervisorBackend, networkMgr NetworkManager, storageMgr StorageManager)
```

This enables:
- Easy testing with mocks
- Platform-specific implementations
- Swappable components

### Platform Abstraction Pattern

Platform-specific code is isolated behind interfaces:
- `hypervisor.Backend` - VM operations
- `network.Manager` - Network operations
- Platform detection via build tags

### Testable-by-Construction Pattern

All major components are testable:
- Unit tests: `*_test.go` files in each package
- Integration tests: `*_integration_test.go` with `//go:build integration` tag
- E2E tests: `*_e2e_test.go` for full workflow validation

## Current Status

- **Hypervisor abstraction:** Complete (KVM backend functional, HVF stub)
- **VM manager:** Complete with dependency injection
- **Networking:** Linux TAP/bridge implemented, macOS vmnet pending
- **Storage:** Cross-platform rootfs creation
- **Tests:** 202 tests, 100% pass rate
- **Coverage:** ~42% overall

## Future Work

1. **Hypervisor.framework implementation** - Complete macOS support
2. **Kernel/initrd extraction** - Full boot support from container images
3. **Compose engine** - Multi-sandbox orchestration
4. **Egress firewall** - Per-sandbox network policies
5. **Integration tests** - Real hypervisor backend validation

## References

- Project spec: `README.md`
- Improvement queue: `memory/IMPROVEMENT_QUEUE.md`
- Architecture TODOs: `ARCHITECTURE_TODO.md`
