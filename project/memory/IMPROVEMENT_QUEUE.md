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
  - id: IQ-003
    area: documentation
    type: docs
    description: Update ARCHITECTURE.md to reflect hypervisor abstraction implementation
    priority: medium
    added_iteration: 22
    rationale: Current architecture docs reference Firecracker but implementation now uses hypervisor abstraction. Documentation needs update to match code.
    status: pending
  - id: IQ-004
    area: tests
    type: test
    description: Add integration tests for actual hypervisor backend functionality (requires KVM access)
    priority: low
    added_iteration: 22
    rationale: Current tests use mocks. End-to-end tests with real hypervisor would provide additional confidence.
    status: pending
