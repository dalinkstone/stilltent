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

2. **Iteration 20-22:** Complete hypervisor abstraction layer **(COMPLETE)**
   - ✅ Replace Firecracker with hypervisor backend interface
   - ✅ VM Manager refactored to use hypervisor.Backend
   - ✅ Network manager integration
   - ✅ Storage manager integration
   - ✅ All tests pass (165 tests, 100% pass rate)

3. **Iteration 23:** Fix network cleanup in Destroy **(COMPLETE)**
   - ✅ Network resources cleaned up for all VM states
   - ✅ TAP devices properly removed

4. **Iteration 24:** Implement actual KVM VM operations **(COMPLETE)**
   - ✅ Integrate `c35s/hype` for actual VM creation and execution
   - ✅ Use `vmm.Config` and `vmm.Run()` for VM lifecycle
   - ✅ Linux loader integration for kernel/initrd
   - ✅ KVM backend now functional (not just stubs)
   - ✅ Tests updated to handle actual implementation

### Current Status
- [x] Interface definition complete
- [x] KVM backend with actual implementation
- [x] Tests written (100% coverage on interface, 33.3% on KVM)
- [x] macOS placeholder implemented
- [x] Documentation complete
- [x] VM Manager refactored to use hypervisor backend
- [x] Network cleanup fixed
- [ ] Full KVM feature set (kernel loading, disk devices, network devices)
- [ ] Hypervisor.framework implementation for macOS

### Dependencies
- `github.com/c35s/hype` - KVM library with thin ioctl wrappers
  - `vmm` - VM configuration and management helpers
  - `virtio` - Virtio device implementations
  - `os/linux` - Linux boot protocol support
