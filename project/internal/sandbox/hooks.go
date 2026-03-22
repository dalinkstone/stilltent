package vm

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// HookPhase represents when a lifecycle hook runs.
type HookPhase string

const (
	HookPreStart  HookPhase = "pre_start"
	HookPostStart HookPhase = "post_start"
	HookPreStop   HookPhase = "pre_stop"
	HookPostStop  HookPhase = "post_stop"
)

// HookResult records the outcome of a single hook execution.
type HookResult struct {
	Name    string
	Phase   HookPhase
	Command string
	Output  string
	Err     error
	Elapsed time.Duration
}

// defaultHookTimeout is the default timeout for a lifecycle hook (30 seconds).
const defaultHookTimeout = 30

// RunHooks executes all hooks for the given phase. It returns on the first
// hook that fails with on_failure=abort. Otherwise it continues through all
// hooks and returns accumulated results.
func (m *VMManager) RunHooks(sandboxName string, hooks *models.LifecycleHooks, phase HookPhase) ([]HookResult, error) {
	if hooks == nil {
		return nil, nil
	}

	actions := hooksForPhase(hooks, phase)
	if len(actions) == 0 {
		return nil, nil
	}

	var results []HookResult

	for i, action := range actions {
		name := action.Name
		if name == "" {
			name = fmt.Sprintf("%s-hook-%d", phase, i)
		}

		timeout := action.TimeoutSec
		if timeout <= 0 {
			timeout = defaultHookTimeout
		}

		where := action.Where
		if where == "" {
			where = "host"
		}

		onFailure := action.OnFailure
		if onFailure == "" {
			onFailure = "continue"
		}

		m.logEvent(EventHookRun, sandboxName, map[string]string{
			"hook":    name,
			"phase":   string(phase),
			"command": action.Command,
			"where":   where,
		})

		start := time.Now()
		output, err := m.executeHook(sandboxName, action.Command, where, timeout)
		elapsed := time.Since(start)

		result := HookResult{
			Name:    name,
			Phase:   phase,
			Command: action.Command,
			Output:  output,
			Err:     err,
			Elapsed: elapsed,
		}
		results = append(results, result)

		if err != nil {
			m.logEvent(EventHookError, sandboxName, map[string]string{
				"hook":    name,
				"phase":   string(phase),
				"error":   err.Error(),
				"elapsed": elapsed.String(),
			})

			if onFailure == "abort" {
				return results, fmt.Errorf("lifecycle hook %q failed (phase=%s): %w", name, phase, err)
			}
		}
	}

	return results, nil
}

// executeHook runs a single hook command either on the host or inside the guest.
func (m *VMManager) executeHook(sandboxName, command, where string, timeoutSec int) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	if where == "guest" {
		return m.executeGuestHook(ctx, sandboxName, command)
	}

	return m.executeHostHook(ctx, command)
}

// executeHostHook runs a command on the host via /bin/sh.
func (m *VMManager) executeHostHook(ctx context.Context, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("hook timed out: %s", command)
	}
	if err != nil {
		return string(out), fmt.Errorf("hook failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

// executeGuestHook runs a command inside the sandbox using the exec path.
func (m *VMManager) executeGuestHook(ctx context.Context, sandboxName, command string) (string, error) {
	// Use the manager's exec command helper to run inside the guest
	vmState, err := m.stateManager.GetVM(sandboxName)
	if err != nil {
		return "", fmt.Errorf("cannot resolve sandbox for guest hook: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return "", fmt.Errorf("sandbox %q is not running, cannot execute guest hook", sandboxName)
	}

	// Guest hooks execute via SSH to the sandbox
	if vmState.IP == "" {
		return "", fmt.Errorf("sandbox %q has no IP, cannot execute guest hook", sandboxName)
	}

	sshArgs := []string{
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
	if vmState.SSHKeyPath != "" {
		sshArgs = append(sshArgs, "-i", vmState.SSHKeyPath)
	}
	sshArgs = append(sshArgs, fmt.Sprintf("root@%s", vmState.IP), command)

	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("guest hook timed out: %s", command)
	}
	if err != nil {
		return string(out), fmt.Errorf("guest hook failed: %w: %s", err, string(out))
	}
	return string(out), nil
}

// hooksForPhase selects the hook actions for a given lifecycle phase.
func hooksForPhase(hooks *models.LifecycleHooks, phase HookPhase) []models.HookAction {
	switch phase {
	case HookPreStart:
		return hooks.PreStart
	case HookPostStart:
		return hooks.PostStart
	case HookPreStop:
		return hooks.PreStop
	case HookPostStop:
		return hooks.PostStop
	default:
		return nil
	}
}
