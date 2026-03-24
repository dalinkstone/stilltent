package compose

import (
	"fmt"
	"strings"
)

// HookPhase identifies when a lifecycle hook runs.
type HookPhase string

const (
	HookPostCreate HookPhase = "post_create"
	HookPostStart  HookPhase = "post_start"
	HookPreStop    HookPhase = "pre_stop"
	HookPreDestroy HookPhase = "pre_destroy"
)

// HookResult records the outcome of running a single hook command.
type HookResult struct {
	Phase    HookPhase
	Service  string
	Command  string
	Output   string
	ExitCode int
	Err      error
}

// String returns a human-readable summary of the hook result.
func (r *HookResult) String() string {
	status := "ok"
	if r.Err != nil {
		status = fmt.Sprintf("error: %v", r.Err)
	} else if r.ExitCode != 0 {
		status = fmt.Sprintf("exit %d", r.ExitCode)
	}
	return fmt.Sprintf("[%s] %s: %q → %s", r.Phase, r.Service, r.Command, status)
}

// runHooks executes the commands for a given lifecycle phase on a sandbox.
// Commands are run sequentially inside the sandbox via the VM manager's Exec.
// If continueOnError is false, execution stops at the first failure.
// Returns all results collected so far plus any error from the first failure.
func (m *ComposeManager) runHooks(service string, hooks *LifecycleHooks, phase HookPhase, continueOnError bool) ([]HookResult, error) {
	if hooks == nil {
		return nil, nil
	}

	var commands []string
	switch phase {
	case HookPostCreate:
		commands = hooks.PostCreate
	case HookPostStart:
		commands = hooks.PostStart
	case HookPreStop:
		commands = hooks.PreStop
	case HookPreDestroy:
		commands = hooks.PreDestroy
	}

	if len(commands) == 0 {
		return nil, nil
	}

	results := make([]HookResult, 0, len(commands))
	for _, cmd := range commands {
		// Split command into shell invocation so it runs through sh -c
		shellCmd := []string{"sh", "-c", cmd}

		output, exitCode, err := m.vmManager.Exec(service, shellCmd)
		result := HookResult{
			Phase:    phase,
			Service:  service,
			Command:  cmd,
			Output:   strings.TrimSpace(output),
			ExitCode: exitCode,
			Err:      err,
		}
		results = append(results, result)

		if err != nil && !continueOnError {
			return results, fmt.Errorf("hook %s failed for %s: command %q: %w", phase, service, cmd, err)
		}
		if exitCode != 0 && !continueOnError {
			return results, fmt.Errorf("hook %s failed for %s: command %q exited with code %d", phase, service, cmd, exitCode)
		}
	}

	return results, nil
}

// runHooksForServices runs a lifecycle hook phase across multiple services.
// Services are processed in the given order. Hook failures on a service
// are collected but do not prevent hooks on subsequent services from running.
func (m *ComposeManager) runHooksForServices(services []string, configs map[string]*SandboxConfig, phase HookPhase) []HookResult {
	var allResults []HookResult
	for _, svc := range services {
		cfg, ok := configs[svc]
		if !ok || cfg.Hooks == nil {
			continue
		}
		// For pre_stop and pre_destroy, continue on error so all services get a chance to clean up
		continueOnError := (phase == HookPreStop || phase == HookPreDestroy)
		results, _ := m.runHooks(svc, cfg.Hooks, phase, continueOnError)
		allResults = append(allResults, results...)
	}
	return allResults
}
