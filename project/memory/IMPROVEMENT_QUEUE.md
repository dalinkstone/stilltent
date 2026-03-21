improvement_queue:
  - id: IQ-002
    area: architecture
    type: refactor
    description: Replace Firecracker external binary dependency with hypervisor abstraction layer (KVM on Linux, Hypervisor.framework on macOS)
    priority: high
    added_iteration: 19
    rationale: The spec explicitly requires direct hypervisor access without external binaries. Current implementation violates this by using Firecracker as an external process.
    status: partially_complete
    completed_in_pr: 25
