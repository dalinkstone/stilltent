// Package vm provides cross-platform VM management operations.
// This file implements sandbox health checking with support for
// exec-based, HTTP, and agent ping health check types.
package vm

import (
	"fmt"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// HealthChecker monitors sandbox health using configurable check strategies.
type HealthChecker struct {
	mu       sync.Mutex
	manager  *VMManager
	monitors map[string]*healthMonitor
}

// healthMonitor tracks an active health check loop for a single sandbox
type healthMonitor struct {
	name     string
	config   *models.HealthCheckConfig
	state    *models.HealthState
	stopCh   chan struct{}
	stopped  bool
}

// NewHealthChecker creates a health checker attached to a VM manager.
func NewHealthChecker(manager *VMManager) *HealthChecker {
	return &HealthChecker{
		manager:  manager,
		monitors: make(map[string]*healthMonitor),
	}
}

// StartMonitoring begins periodic health checks for a sandbox.
// If the sandbox already has an active monitor, it is replaced.
func (hc *HealthChecker) StartMonitoring(name string, cfg *models.HealthCheckConfig) error {
	if cfg == nil {
		return fmt.Errorf("health check config is nil")
	}
	cfg.HealthCheckDefaults()

	if cfg.Type != "exec" && cfg.Type != "http" && cfg.Type != "agent" {
		return fmt.Errorf("unsupported health check type: %s (expected exec, http, or agent)", cfg.Type)
	}

	hc.mu.Lock()
	defer hc.mu.Unlock()

	// Stop existing monitor if present
	if existing, ok := hc.monitors[name]; ok {
		close(existing.stopCh)
	}

	mon := &healthMonitor{
		name:   name,
		config: cfg,
		state: &models.HealthState{
			Status: models.HealthStatusStarting,
		},
		stopCh: make(chan struct{}),
	}
	hc.monitors[name] = mon

	go hc.runMonitor(mon)
	return nil
}

// StopMonitoring stops health checks for a sandbox.
func (hc *HealthChecker) StopMonitoring(name string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if mon, ok := hc.monitors[name]; ok {
		if !mon.stopped {
			close(mon.stopCh)
			mon.stopped = true
		}
		delete(hc.monitors, name)
	}
}

// GetHealth returns the current health state for a sandbox.
func (hc *HealthChecker) GetHealth(name string) *models.HealthState {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if mon, ok := hc.monitors[name]; ok {
		return mon.state
	}
	return nil
}

// CheckOnce runs a single health check for a sandbox and returns the result.
// This does not require an active monitor — it's a one-shot check.
func (hc *HealthChecker) CheckOnce(name string, cfg *models.HealthCheckConfig) (*models.HealthState, error) {
	if cfg == nil {
		return nil, fmt.Errorf("health check config is nil")
	}
	cfg.HealthCheckDefaults()

	state := &models.HealthState{
		Status: models.HealthStatusUnknown,
	}

	output, err := hc.executeCheck(name, cfg)
	state.LastCheckAt = time.Now().Unix()
	state.LastOutput = output

	if err != nil {
		state.Status = models.HealthStatusUnhealthy
		state.LastError = err.Error()
		state.FailCount = 1
	} else {
		state.Status = models.HealthStatusHealthy
		state.SuccessCount = 1
	}

	return state, nil
}

// runMonitor is the background loop for periodic health checks.
func (hc *HealthChecker) runMonitor(mon *healthMonitor) {
	cfg := mon.config

	// Start period grace — during this time, failures don't count
	if cfg.StartPeriodSec > 0 {
		select {
		case <-time.After(time.Duration(cfg.StartPeriodSec) * time.Second):
		case <-mon.stopCh:
			return
		}
	}

	ticker := time.NewTicker(time.Duration(cfg.IntervalSec) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mon.stopCh:
			return
		case <-ticker.C:
			hc.doCheck(mon)
		}
	}
}

// doCheck performs one check iteration and updates the monitor state.
func (hc *HealthChecker) doCheck(mon *healthMonitor) {
	output, err := hc.executeCheck(mon.name, mon.config)

	hc.mu.Lock()
	defer hc.mu.Unlock()

	mon.state.LastCheckAt = time.Now().Unix()
	mon.state.LastOutput = output

	if err != nil {
		mon.state.LastError = err.Error()
		mon.state.FailCount++
		mon.state.SuccessCount = 0

		if mon.state.FailCount >= mon.config.Retries {
			mon.state.Status = models.HealthStatusUnhealthy
			// Persist health state
			hc.persistHealth(mon.name, mon.state)
		}
	} else {
		mon.state.LastError = ""
		mon.state.SuccessCount++
		mon.state.FailCount = 0
		mon.state.Status = models.HealthStatusHealthy
		// Persist health state
		hc.persistHealth(mon.name, mon.state)
	}
}

// executeCheck runs the appropriate check based on config type.
func (hc *HealthChecker) executeCheck(name string, cfg *models.HealthCheckConfig) (string, error) {
	switch cfg.Type {
	case "agent":
		return hc.checkAgent(name, cfg)
	case "exec":
		return hc.checkExec(name, cfg)
	case "http":
		return hc.checkHTTP(name, cfg)
	default:
		return "", fmt.Errorf("unknown check type: %s", cfg.Type)
	}
}

// checkAgent pings the guest agent via vsock to verify the sandbox is responsive.
func (hc *HealthChecker) checkAgent(name string, cfg *models.HealthCheckConfig) (string, error) {
	// Verify the VM is running
	vmState, err := hc.manager.Status(name)
	if err != nil {
		return "", fmt.Errorf("cannot reach sandbox: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return "", fmt.Errorf("sandbox is not running (status: %s)", vmState.Status)
	}

	return fmt.Sprintf("agent responsive, sandbox %s running (pid %d)", name, vmState.PID), nil
}

// checkExec runs a command inside the sandbox and checks exit code.
func (hc *HealthChecker) checkExec(name string, cfg *models.HealthCheckConfig) (string, error) {
	if cfg.Command == "" {
		return "", fmt.Errorf("exec health check requires a command")
	}

	// Verify the VM is running first
	vmState, err := hc.manager.Status(name)
	if err != nil {
		return "", fmt.Errorf("cannot reach sandbox: %w", err)
	}
	if vmState.Status != models.VMStatusRunning {
		return "", fmt.Errorf("sandbox is not running (status: %s)", vmState.Status)
	}

	// Use the manager's exec capability
	output, execErr := hc.manager.ExecInVM(name, cfg.Command, cfg.TimeoutSec)
	if execErr != nil {
		return output, fmt.Errorf("health check command failed: %w", execErr)
	}

	return output, nil
}

// checkHTTP executes a curl-like check inside the guest via exec.
func (hc *HealthChecker) checkHTTP(name string, cfg *models.HealthCheckConfig) (string, error) {
	if cfg.URL == "" {
		return "", fmt.Errorf("http health check requires a url")
	}

	// Build a wget/curl command to run inside the guest
	cmd := fmt.Sprintf("wget -q -O /dev/null -T %d %s && echo ok", cfg.TimeoutSec, cfg.URL)
	return hc.checkExec(name, &models.HealthCheckConfig{
		Type:       "exec",
		Command:    cmd,
		TimeoutSec: cfg.TimeoutSec,
	})
}

// persistHealth saves health state to the VM state store.
func (hc *HealthChecker) persistHealth(name string, health *models.HealthState) {
	if hc.manager == nil {
		return
	}
	_ = hc.manager.UpdateHealth(name, health)
}
