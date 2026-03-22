package compose

import (
	"fmt"
	"strings"
	"sync"
	"time"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

// HealthStatus represents the health state of a service
type HealthStatus struct {
	Service       string    `json:"service"`
	Status        string    `json:"status"` // "healthy", "unhealthy", "starting", "none"
	FailCount     int       `json:"fail_count"`
	LastCheck     time.Time `json:"last_check,omitempty"`
	LastOutput    string    `json:"last_output,omitempty"`
	Restarts      int       `json:"restarts"`
	CheckInterval int       `json:"check_interval_sec"`
}

// HealthMonitor watches compose services and runs periodic health checks.
// It auto-restarts unhealthy services based on their restart policy.
type HealthMonitor struct {
	mu            sync.Mutex
	vmManager     *vm.VMManager
	stateManager  StateManager
	groupName     string
	config        *ComposeConfig
	statuses      map[string]*HealthStatus
	stopCh        chan struct{}
	stopped       bool
}

// NewHealthMonitor creates a health monitor for a compose group
func NewHealthMonitor(groupName string, config *ComposeConfig, vmManager *vm.VMManager, stateManager StateManager) *HealthMonitor {
	statuses := make(map[string]*HealthStatus)
	for name, svc := range config.Sandboxes {
		hs := &HealthStatus{
			Service: name,
			Status:  "none",
		}
		if svc.HealthCheck != nil {
			hs.Status = "starting"
			hs.CheckInterval = svc.HealthCheck.IntervalSec
			if hs.CheckInterval <= 0 {
				hs.CheckInterval = 30
			}
		}
		statuses[name] = hs
	}

	return &HealthMonitor{
		vmManager:    vmManager,
		stateManager: stateManager,
		groupName:    groupName,
		config:       config,
		statuses:     statuses,
		stopCh:       make(chan struct{}),
	}
}

// Start begins monitoring health checks in background goroutines
func (hm *HealthMonitor) Start() {
	for name, svc := range hm.config.Sandboxes {
		if svc.HealthCheck == nil {
			continue
		}
		go hm.monitorService(name, svc)
	}
}

// Stop terminates all health monitoring goroutines
func (hm *HealthMonitor) Stop() {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if !hm.stopped {
		hm.stopped = true
		close(hm.stopCh)
	}
}

// Statuses returns a snapshot of all service health statuses
func (hm *HealthMonitor) Statuses() []HealthStatus {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	result := make([]HealthStatus, 0, len(hm.statuses))
	for _, hs := range hm.statuses {
		result = append(result, *hs)
	}
	return result
}

// ServiceHealth returns the health status for a specific service
func (hm *HealthMonitor) ServiceHealth(service string) (*HealthStatus, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hs, ok := hm.statuses[service]
	if !ok {
		return nil, fmt.Errorf("service %q not found", service)
	}
	cp := *hs
	return &cp, nil
}

func (hm *HealthMonitor) monitorService(name string, svc *SandboxConfig) {
	hc := svc.HealthCheck
	interval := time.Duration(hc.IntervalSec) * time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	timeout := hc.TimeoutSec
	if timeout <= 0 {
		timeout = 10
	}
	retries := hc.Retries
	if retries <= 0 {
		retries = 3
	}
	startPeriod := time.Duration(hc.StartPeriodSec) * time.Second

	// Wait for start period before beginning checks
	if startPeriod > 0 {
		select {
		case <-time.After(startPeriod):
		case <-hm.stopCh:
			return
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failCount := 0

	for {
		select {
		case <-hm.stopCh:
			return
		case <-ticker.C:
			output, healthy := hm.runCheck(name, hc.Command, timeout)

			hm.mu.Lock()
			hs := hm.statuses[name]
			hs.LastCheck = time.Now()
			hs.LastOutput = output

			if healthy {
				failCount = 0
				hs.FailCount = 0
				hs.Status = "healthy"
			} else {
				failCount++
				hs.FailCount = failCount
				if failCount >= retries {
					hs.Status = "unhealthy"
				}
			}

			// Auto-restart if unhealthy and restart policy allows it
			needsRestart := hs.Status == "unhealthy" && shouldRestart(svc.RestartPolicy)
			hm.mu.Unlock()

			if needsRestart {
				hm.restartService(name)
				// Reset fail count after restart attempt
				hm.mu.Lock()
				hs.Restarts++
				hs.Status = "starting"
				failCount = 0
				hs.FailCount = 0
				hm.mu.Unlock()

				// Wait for start period again after restart
				if startPeriod > 0 {
					select {
					case <-time.After(startPeriod):
					case <-hm.stopCh:
						return
					}
				}
			}
		}
	}
}

// runCheck executes the health check command inside the sandbox
func (hm *HealthMonitor) runCheck(service string, command []string, timeoutSec int) (string, bool) {
	if len(command) == 0 {
		return "", true
	}

	// Join command for exec
	cmdStr := strings.Join(command, " ")
	output, err := hm.vmManager.ExecInVM(service, cmdStr, timeoutSec)
	if err != nil {
		return fmt.Sprintf("check failed: %v", err), false
	}
	return output, true
}

// restartService stops and starts a service
func (hm *HealthMonitor) restartService(name string) {
	_ = hm.vmManager.Stop(name)
	_ = hm.vmManager.Start(name)

	// Update compose state with restart count
	hm.mu.Lock()
	restarts := hm.statuses[name].Restarts
	hm.mu.Unlock()

	state, err := hm.stateManager.LoadComposeState(hm.groupName)
	if err == nil {
		if ss, ok := state.Sandboxes[name]; ok {
			ss.Restarts = restarts
			ss.Health = "starting"
			_ = hm.stateManager.SaveComposeState(hm.groupName, state)
		}
	}
}

// shouldRestart returns true if the restart policy allows auto-restart
func shouldRestart(policy string) bool {
	switch policy {
	case "always", "on-failure":
		return true
	default:
		return false
	}
}

// ValidateHealthCheck validates a health check configuration
func ValidateHealthCheck(hc *HealthCheckConf) error {
	if hc == nil {
		return nil
	}
	if len(hc.Command) == 0 {
		return fmt.Errorf("health_check.command is required")
	}
	if hc.IntervalSec < 0 {
		return fmt.Errorf("health_check.interval_sec must be non-negative")
	}
	if hc.TimeoutSec < 0 {
		return fmt.Errorf("health_check.timeout_sec must be non-negative")
	}
	if hc.Retries < 0 {
		return fmt.Errorf("health_check.retries must be non-negative")
	}
	if hc.StartPeriodSec < 0 {
		return fmt.Errorf("health_check.start_period_sec must be non-negative")
	}
	return nil
}

// ValidateRestartPolicy validates a restart policy string
func ValidateRestartPolicy(policy string) error {
	switch policy {
	case "", "no", "on-failure", "always":
		return nil
	default:
		return fmt.Errorf("invalid restart policy %q: must be one of: no, on-failure, always", policy)
	}
}
