# Iteration 31 Plan

## Context
- **Iteration:** 31
- **Milestone type:** Standard iteration (not 5th, 10th, 25th, or 50th)
- **Current state:** All 202 tests passing, 38 Go files, code health 5/5
- **Improvement queue status:** IQ-008 (CLI dependency injection) pending since iteration 30
- **最近自省:** Iteration 30 identified CLI commands lack dependency injection, limiting test coverage

## Action Type
**refactor** - Add dependency injection pattern to CLI commands for better testability

## Summary
Refactor CLI commands to accept interfaces for VMManager, HypervisorBackend, NetworkManager, and StorageManager dependencies. This enables comprehensive end-to-end testing with mocks while maintaining the current functionality.

## Files
- `cmd/tent/main.go` - Add dependency injection setup
- `cmd/tent/create.go` - Accept interfaces via functional options or constructor
- `cmd/tent/start.go` - Accept interfaces via functional options or constructor  
- `cmd/tent/stop.go` - Accept interfaces via functional options or constructor
- `cmd/tent/destroy.go` - Accept interfaces via functional options or constructor
- `cmd/tent/list.go` - Accept interfaces via functional options or constructor
- `cmd/tent/ssh.go` - Accept interfaces via functional options or constructor
- `cmd/tent/status.go` - Accept interfaces via functional options or constructor
- `cmd/tent/logs.go` - Accept interfaces via functional options or constructor
- `cmd/tent/mocks.go` - Add/update mocks for new interfaces

## Tests
```bash
cd /workspace/repo/project
go test ./cmd/tent/... -v -count=1
go test ./internal/vm/... -v -count=1
go test ./... -coverprofile=coverage.out
go vet ./...
```

## Confidence
0.7 - High confidence in the approach, moderate risk from refactoring CLI structure

## Risk
- CLI commands may need to be split into smaller functions for better testability
- Breaking changes to public API if not careful about backward compatibility
- Increased code complexity from additional dependency injection patterns

## Hypothesis
**Hypothesis:** Adding dependency injection to CLI commands will enable comprehensive end-to-end testing with mocks, improving test coverage from current ~11% (cmd/tent) to >50%.

**Basis:** VM Manager already uses interface-based design for testability (shown in VM tests). Applying the same pattern to CLI layer is natural extension of existing architecture.

**Prediction:** 
- CLI integration tests will be able to mock all dependencies
- Test coverage for cmd/tent package will increase from ~11% to >50%
- Code remains fully functional with same external behavior
- No breaking changes to public API

**Risk:** Some CLI commands may be difficult to refactor without major restructuring; may need to add helper functions for testability.

## Source
**IQ-008** - CLI dependency injection (proposed in iteration 30 self-reflection)

## Goals
1. CLI commands accept dependencies as interfaces rather than creating them internally
2. Existing tests continue to pass
3. New tests can mock all dependencies for comprehensive coverage
4. No breaking changes to public API (binary still works the same)
