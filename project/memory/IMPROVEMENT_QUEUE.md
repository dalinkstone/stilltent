improvement_queue:
  - id: IQ-001
    area: cmd/tent
    type: test
    description: Add integration tests for CLI commands (test that commands actually work end-to-end)
    priority: medium
    added_iteration: 2
    rationale: CLI commands are currently only unit tested; integration tests would verify they actually work with mock VMs
