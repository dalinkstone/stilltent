improvement_queue:
  - id: IQ-002
    area: architecture
    type: refactor
    description: Replace Firecracker external binary dependency with hypervisor abstraction layer (KVM on Linux, Hypervisor.framework on macOS)
    priority: high
    added_iteration: 19
    rationale: The spec explicitly requires direct hypervisor access without external binaries. Current implementation violates this by using Firecracker as an external process.

  - id: IQ-001
    area: cmd/tent
    type: test
    description: Add integration tests for CLI commands (test that commands actually work end-to-end)
    priority: medium
    added_iteration: 2
    rationale: CLI commands are currently only unit tested; integration tests would verify they actually work with mock VMs
