improvement_queue:
  - id: IQ-002
    area: architecture
    type: refactor
    description: Replace Firecracker external binary dependency with hypervisor abstraction layer (KVM on Linux, Hypervisor.framework on macOS)
    priority: high
    added_iteration: 19
    rationale: The spec explicitly requires direct hypervisor access without external binaries. Current implementation violates this by using Firecracker as an external process.
    status: complete
    completed_in_pr: 22
    notes: Completed in iteration 22 - hypervisor.Backend interface implemented with KVM backend using c35s/hype library, macOS placeholder stub exists.
  - id: IQ-003
    area: documentation
    type: docs
    description: Update ARCHITECTURE.md to reflect hypervisor abstraction implementation
    priority: medium
    added_iteration: 22
    rationale: Current architecture docs reference Firecracker but implementation now uses hypervisor abstraction. Documentation needs update to match code.
    status: pending
    notes: ARCHITECTURE.md still needs update to reflect current state (hypervisor abstraction complete, Firecracker removed). Priority: medium.
  - id: IQ-004
    area: tests
    type: test
    description: Add integration tests for actual hypervisor backend functionality (requires KVM access)
    priority: low
    added_iteration: 22
    rationale: Current tests use mocks. End-to-end tests with real hypervisor would provide additional confidence.
    status: pending
    notes: Integration tests require actual /dev/kvm access which isn't available in CI environment. Low priority - unit tests cover code paths.
  - id: IQ-006
    area: internal/hypervisor/kvm
    type: feature-gap
    description: Implement kernel/initrd extraction from rootfs images for actual VM booting
    priority: high
    added_iteration: 24
    rationale: Without kernel loading from rootfs images, VMs cannot boot. This is the final blocker before tent can create functional VMs. KVM backend uses placeholder paths.
    status: pending
    notes: Requires rootfs image format parsing (qcow2, raw, etc.) and kernel extraction. High priority - blocks actual VM creation.
  - id: IQ-007
    area: internal/hypervisor/hvf
    type: feature-gap
    description: Implement macOS Hypervisor.framework backend for cross-platform support
    priority: high
    added_iteration: 25
    rationale: Current implementation only works on Linux (KVM backend). macOS requires Hypervisor.framework or Virtualization.framework. No cross-platform support without this.
    status: pending
    notes: Requires macOS dev environment and cgo bindings to Hypervisor.framework. High priority - blocks macOS support per spec.
