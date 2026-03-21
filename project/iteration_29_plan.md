# Iteration 29 Plan

## Context
- Iteration: 29
- Current state: All spec features implemented, all tests pass (41.6% coverage)
- Improvement queue: IQ-003 (docs), IQ-004 (integration tests), IQ-006 (kernel extraction), IQ-007 (macOS hvf)

## Action Type
**feature** - Implement kernel/initrd extraction from rootfs images

## Summary
Implement kernel extraction from rootfs images to enable actual VM booting. Currently, the KVM backend uses placeholder paths for kernel/initrd, preventing actual VM creation. This feature requires parsing rootfs image formats (qcow2, raw) and extracting the kernel and initrd.

## Files
- `internal/hypervisor/kvm/kvm_linux.go` - Add kernel extraction function
- `internal/storage/storage.go` - Add rootfs image parsing
- `internal/hypervisor/kvm/kvm_linux_test.go` - Add tests for kernel extraction

## Tests
```bash
go test ./internal/hypervisor/kvm/... -v
go test ./internal/storage/... -v
go test ./... -v -count=1
go vet ./...
```

## Confidence
0.7 - High confidence in the approach, but depends on external library availability for image parsing

## Risk
- If external libraries don't support the required image formats, may need to implement parsing manually
- Kernel extraction may require additional dependencies (e.g., qemu-img for format conversion)

## Hypothesis
Adding kernel extraction will enable actual VM booting with rootfs images, bringing tent closer to the "minimal working VM" goal. The KVM backend already has the VM execution infrastructure; this completes the kernel loading path.

## Prediction
- 2-3 new test functions
- 50-100 lines of new code for kernel extraction
- VM creation with rootfs images should succeed (though VM may not fully boot without additional setup)

## Source
improvement_queue IQ-006
