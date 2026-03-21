# Architectural TODOs

This file tracks major architectural issues that need to be addressed.

## Hypervisor Abstraction Layer

**Priority:** High  
**Status:** Partially implemented (Iteration 19)  
**Iteration:** 19

### Problem
The current implementation uses Firecracker as an external binary dependency, which violates the spec that requires:
- Direct KVM access on Linux via `/dev/kvm` ioctl
- Direct Hypervisor.framework access on macOS
- No external hypervisor binaries (no Firecracker, no QEMU)

### Solution
Replace the `internal/firecracker/` package with a proper hypervisor abstraction:

```
internal/hypervisor/
├── backend.go          # Common interface definition
├── backend_test.go     # Common tests
├── kvm/                # Linux KVM implementation
│   ├── kvm_linux.go
│   └── kvm_linux_test.go
└── hvf/                # macOS Hypervisor.framework implementation
    └── hvf_darwin.go
```

### Implementation Plan
1. **Iteration 19:** Create the interface and KVM backend stub using `c35s/hype` library **(COMPLETE)**
   - ✅ Hypervisor backend interface defined
   - ✅ KVM backend for Linux implemented with tests
   - ✅ Hypervisor.framework stub for macOS
   - ✅ Documentation (ARCHITECTURE_TODO.md)
   - ✅ Improvement queue entry created

2. **Iteration 20+:** Implement full KVM backend with proper device configuration
   - [ ] Integrate `c35s/hype` for actual VM operations
   - [ ] Implement kernel loading (vmlinuz)
   - [ ] Implement rootfs provisioning
   - [ ] Implement network device (virtio-net)
   - [ ] Implement block device (virtio-blk)

3. **Iteration 21+:** Implement Hypervisor.framework backend for macOS
   - [ ] Use Hypervisor.framework (or Virtualization.framework)
   - [ ] Implement VM lifecycle management
   - [ ] Implement macOS-specific networking (vmnet)

4. **Iteration 22+:** Refactor VM manager to use hypervisor backend
   - [ ] Replace FirecrackerClient interface with hypervisor.Backend
   - [ ] Update VM lifecycle methods to use new backend
   - [ ] Update tests to work with hypervisor backend

5. **Iteration 23+:** Remove Firecracker dependency
   - [ ] Delete `internal/firecracker/` directory
   - [ ] Remove external binary dependency
   - [ ] Update documentation

### Current Status
- [x] Interface definition complete
- [x] KVM backend stub implemented
- [x] Tests written (100% coverage on interface, 46.4% on KVM)
- [x] macOS placeholder implemented
- [x] Documentation complete
- [ ] Full KVM implementation pending
- [ ] Hypervisor.framework implementation pending
- [ ] VM manager refactoring pending

### Dependencies
- `github.com/c35s/hype` - KVM library with thin ioctl wrappers
  - `vmm` - VM configuration and management helpers
  - `virtio` - Virtio device implementations
  - `os/linux` - Linux boot protocol support
