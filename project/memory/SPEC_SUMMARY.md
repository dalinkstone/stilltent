# tent - Project Spec Summary

## What is tent?
A CLI tool for creating, managing, and destroying microVMs as isolated development environments using Firecracker.

## Core Commands
- `tent create <name> [--config <path>]` - Create microVM
- `tent start <name>` - Boot VM
- `tent stop <name>` - Shutdown VM
- `tent destroy <name>` - Remove VM
- `tent list` - List all VMs
- `tent ssh <name>` - SSH into VM
- `tent status <name>` - VM status
- `tent logs <name>` - View console logs
- `tent snapshot <create|restore|list> <name> <tag>` - Snapshots
- `tent network list` - List network resources
- `tent image <list|pull> <name>` - Base images

## Architecture
- `cmd/tent/` - CLI entry point (Cobra)
- `internal/vm/` - VM lifecycle management
- `internal/network/` - TAP/bridge/DHCP
- `internal/storage/` - Rootfs/snapshots
- `internal/config/` - YAML parsing
- `internal/state/` - JSON state persistence
- `internal/firecracker/` - Firecracker API client
- `pkg/models/` - Shared types

## Status
- scaffolding: complete
- VM lifecycle: not implemented
- tests: 11 passing
- build: clean