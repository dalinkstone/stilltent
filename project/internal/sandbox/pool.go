// Package vm provides cross-platform VM management operations.
// This file implements a sandbox pool manager for pre-warming and managing
// pools of ready-to-use sandboxes. Pools eliminate boot latency for AI workloads
// by keeping a set of sandboxes pre-created and running, ready to be claimed.
package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// PoolConfig defines the configuration for a sandbox pool.
type PoolConfig struct {
	// Name is the unique identifier for this pool
	Name string `json:"name" yaml:"name"`
	// From is the image reference for pool sandboxes
	From string `json:"from" yaml:"from"`
	// MinReady is the minimum number of sandboxes kept ready (pre-warmed)
	MinReady int `json:"min_ready" yaml:"min_ready"`
	// MaxSize is the maximum total sandboxes in the pool (ready + claimed)
	MaxSize int `json:"max_size" yaml:"max_size"`
	// VCPUs for each pool sandbox
	VCPUs int `json:"vcpus" yaml:"vcpus"`
	// MemoryMB for each pool sandbox
	MemoryMB int `json:"memory_mb" yaml:"memory_mb"`
	// DiskGB for each pool sandbox
	DiskGB int `json:"disk_gb" yaml:"disk_gb"`
	// Network config applied to all pool sandboxes
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`
	// Env vars injected into all pool sandboxes
	Env map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	// TTL auto-reclaims claimed sandboxes after this duration (e.g. "1h", "30m")
	TTL string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	// CreatedAt is when the pool was created
	CreatedAt int64 `json:"created_at"`
}

// Validate checks pool configuration is valid.
func (pc *PoolConfig) Validate() error {
	if pc.Name == "" {
		return fmt.Errorf("pool name is required")
	}
	if pc.From == "" {
		return fmt.Errorf("pool image (from) is required")
	}
	if pc.MinReady < 0 {
		return fmt.Errorf("min_ready must be non-negative")
	}
	if pc.MaxSize < 1 {
		return fmt.Errorf("max_size must be at least 1")
	}
	if pc.MinReady > pc.MaxSize {
		return fmt.Errorf("min_ready (%d) cannot exceed max_size (%d)", pc.MinReady, pc.MaxSize)
	}
	if pc.VCPUs < 1 {
		pc.VCPUs = 2
	}
	if pc.MemoryMB < 1 {
		pc.MemoryMB = 1024
	}
	if pc.DiskGB < 1 {
		pc.DiskGB = 10
	}
	return nil
}

// PoolMemberState represents the state of a sandbox in a pool.
type PoolMemberState string

const (
	// PoolMemberReady means the sandbox is running and available for claiming
	PoolMemberReady PoolMemberState = "ready"
	// PoolMemberClaimed means the sandbox has been assigned to a consumer
	PoolMemberClaimed PoolMemberState = "claimed"
	// PoolMemberProvisioning means the sandbox is being created/started
	PoolMemberProvisioning PoolMemberState = "provisioning"
	// PoolMemberError means the sandbox failed to provision
	PoolMemberError PoolMemberState = "error"
)

// PoolMember tracks a single sandbox within a pool.
type PoolMember struct {
	SandboxName string          `json:"sandbox_name"`
	State       PoolMemberState `json:"state"`
	ClaimedBy   string          `json:"claimed_by,omitempty"`
	ClaimedAt   int64           `json:"claimed_at,omitempty"`
	CreatedAt   int64           `json:"created_at"`
	Error       string          `json:"error,omitempty"`
}

// PoolState is the persisted state of a pool.
type PoolState struct {
	Config  PoolConfig   `json:"config"`
	Members []PoolMember `json:"members"`
}

// PoolStatus is a summary returned by status queries.
type PoolStatus struct {
	Name         string `json:"name"`
	From         string `json:"from"`
	MinReady     int    `json:"min_ready"`
	MaxSize      int    `json:"max_size"`
	Ready        int    `json:"ready"`
	Claimed      int    `json:"claimed"`
	Provisioning int    `json:"provisioning"`
	Errored      int    `json:"errored"`
	Total        int    `json:"total"`
}

// PoolManager manages sandbox pools.
type PoolManager struct {
	baseDir string
	mu      sync.Mutex
}

// NewPoolManager creates a new pool manager.
func NewPoolManager(baseDir string) *PoolManager {
	return &PoolManager{baseDir: baseDir}
}

func (pm *PoolManager) poolDir() string {
	return filepath.Join(pm.baseDir, "pools")
}

func (pm *PoolManager) poolFile(name string) string {
	return filepath.Join(pm.poolDir(), name+".json")
}

// CreatePool creates a new sandbox pool with the given configuration.
func (pm *PoolManager) CreatePool(cfg *PoolConfig) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid pool config: %w", err)
	}

	if err := os.MkdirAll(pm.poolDir(), 0755); err != nil {
		return fmt.Errorf("failed to create pools directory: %w", err)
	}

	// Check if pool already exists
	if _, err := os.Stat(pm.poolFile(cfg.Name)); err == nil {
		return fmt.Errorf("pool %q already exists", cfg.Name)
	}

	cfg.CreatedAt = time.Now().Unix()

	state := &PoolState{
		Config:  *cfg,
		Members: []PoolMember{},
	}

	return pm.saveState(cfg.Name, state)
}

// DeletePool removes a pool and returns the names of sandboxes that should be destroyed.
func (pm *PoolManager) DeletePool(name string) ([]string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(name)
	if err != nil {
		return nil, err
	}

	var sandboxNames []string
	for _, m := range state.Members {
		sandboxNames = append(sandboxNames, m.SandboxName)
	}

	if err := os.Remove(pm.poolFile(name)); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to remove pool file: %w", err)
	}

	return sandboxNames, nil
}

// ListPools returns all pool configurations.
func (pm *PoolManager) ListPools() ([]*PoolStatus, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	dir := pm.poolDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read pools directory: %w", err)
	}

	var pools []*PoolStatus
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()[:len(e.Name())-5] // strip .json
		state, err := pm.loadState(name)
		if err != nil {
			continue
		}
		pools = append(pools, pm.buildStatus(state))
	}

	return pools, nil
}

// GetPoolStatus returns the status of a specific pool.
func (pm *PoolManager) GetPoolStatus(name string) (*PoolStatus, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(name)
	if err != nil {
		return nil, err
	}

	return pm.buildStatus(state), nil
}

// AddMember registers a sandbox as a pool member.
func (pm *PoolManager) AddMember(poolName, sandboxName string, memberState PoolMemberState) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return err
	}

	// Check max size
	if len(state.Members) >= state.Config.MaxSize {
		return fmt.Errorf("pool %q is at maximum capacity (%d)", poolName, state.Config.MaxSize)
	}

	// Check for duplicate
	for _, m := range state.Members {
		if m.SandboxName == sandboxName {
			return fmt.Errorf("sandbox %q is already in pool %q", sandboxName, poolName)
		}
	}

	state.Members = append(state.Members, PoolMember{
		SandboxName: sandboxName,
		State:       memberState,
		CreatedAt:   time.Now().Unix(),
	})

	return pm.saveState(poolName, state)
}

// SetMemberState updates the state of a pool member.
func (pm *PoolManager) SetMemberState(poolName, sandboxName string, memberState PoolMemberState) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return err
	}

	for i, m := range state.Members {
		if m.SandboxName == sandboxName {
			state.Members[i].State = memberState
			if memberState == PoolMemberError {
				state.Members[i].Error = "provisioning failed"
			}
			return pm.saveState(poolName, state)
		}
	}

	return fmt.Errorf("sandbox %q not found in pool %q", sandboxName, poolName)
}

// Claim acquires an available sandbox from the pool.
// Returns the sandbox name and its pool member info, or an error if none available.
func (pm *PoolManager) Claim(poolName, claimedBy string) (*PoolMember, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return nil, err
	}

	for i, m := range state.Members {
		if m.State == PoolMemberReady {
			state.Members[i].State = PoolMemberClaimed
			state.Members[i].ClaimedBy = claimedBy
			state.Members[i].ClaimedAt = time.Now().Unix()

			if err := pm.saveState(poolName, state); err != nil {
				return nil, err
			}
			return &state.Members[i], nil
		}
	}

	return nil, fmt.Errorf("no ready sandboxes available in pool %q", poolName)
}

// Release returns a claimed sandbox to the pool, marking it for recycling.
// The caller should stop/destroy the sandbox and provision a replacement.
func (pm *PoolManager) Release(poolName, sandboxName string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return err
	}

	for i, m := range state.Members {
		if m.SandboxName == sandboxName {
			if m.State != PoolMemberClaimed {
				return fmt.Errorf("sandbox %q is not claimed (state: %s)", sandboxName, m.State)
			}
			// Remove the member — caller should destroy and replenish
			state.Members = append(state.Members[:i], state.Members[i+1:]...)
			return pm.saveState(poolName, state)
		}
	}

	return fmt.Errorf("sandbox %q not found in pool %q", sandboxName, poolName)
}

// RemoveMember removes a sandbox from the pool regardless of state.
func (pm *PoolManager) RemoveMember(poolName, sandboxName string) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return err
	}

	for i, m := range state.Members {
		if m.SandboxName == sandboxName {
			state.Members = append(state.Members[:i], state.Members[i+1:]...)
			return pm.saveState(poolName, state)
		}
	}

	return fmt.Errorf("sandbox %q not found in pool %q", sandboxName, poolName)
}

// GetDeficit returns how many more sandboxes need to be provisioned
// to reach the min_ready target.
func (pm *PoolManager) GetDeficit(poolName string) (int, *PoolConfig, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	state, err := pm.loadState(poolName)
	if err != nil {
		return 0, nil, err
	}

	readyCount := 0
	provisioningCount := 0
	totalCount := len(state.Members)

	for _, m := range state.Members {
		switch m.State {
		case PoolMemberReady:
			readyCount++
		case PoolMemberProvisioning:
			provisioningCount++
		}
	}

	// Don't exceed max size
	needed := state.Config.MinReady - readyCount - provisioningCount
	if needed < 0 {
		needed = 0
	}
	if totalCount+needed > state.Config.MaxSize {
		needed = state.Config.MaxSize - totalCount
	}
	if needed < 0 {
		needed = 0
	}

	return needed, &state.Config, nil
}

// GenerateMemberName creates a unique sandbox name for a pool member.
func (pm *PoolManager) GenerateMemberName(poolName string, index int) string {
	return fmt.Sprintf("pool-%s-%d-%d", poolName, time.Now().Unix(), index)
}

// GenerateMemberConfig creates a VMConfig for a new pool member sandbox.
func (pm *PoolManager) GenerateMemberConfig(cfg *PoolConfig, sandboxName string) *models.VMConfig {
	vmCfg := &models.VMConfig{
		Name:     sandboxName,
		From:     cfg.From,
		VCPUs:    cfg.VCPUs,
		MemoryMB: cfg.MemoryMB,
		DiskGB:   cfg.DiskGB,
		Kernel:   "default",
		Network: models.NetworkConfig{
			Mode:   "bridge",
			Bridge: "tent0",
			Allow:  cfg.Allow,
		},
		Labels: map[string]string{
			"tent.pool":      cfg.Name,
			"tent.pool.role": "member",
		},
	}

	if len(cfg.Env) > 0 {
		vmCfg.Env = make(map[string]string)
		for k, v := range cfg.Env {
			vmCfg.Env[k] = v
		}
	}

	if cfg.TTL != "" {
		vmCfg.TTL = cfg.TTL
	}

	return vmCfg
}

// buildStatus creates a PoolStatus from a PoolState.
func (pm *PoolManager) buildStatus(state *PoolState) *PoolStatus {
	status := &PoolStatus{
		Name:     state.Config.Name,
		From:     state.Config.From,
		MinReady: state.Config.MinReady,
		MaxSize:  state.Config.MaxSize,
		Total:    len(state.Members),
	}

	for _, m := range state.Members {
		switch m.State {
		case PoolMemberReady:
			status.Ready++
		case PoolMemberClaimed:
			status.Claimed++
		case PoolMemberProvisioning:
			status.Provisioning++
		case PoolMemberError:
			status.Errored++
		}
	}

	return status
}

func (pm *PoolManager) loadState(name string) (*PoolState, error) {
	data, err := os.ReadFile(pm.poolFile(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("pool %q not found", name)
		}
		return nil, fmt.Errorf("failed to read pool state: %w", err)
	}

	var state PoolState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse pool state: %w", err)
	}

	return &state, nil
}

func (pm *PoolManager) saveState(name string, state *PoolState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal pool state: %w", err)
	}

	return os.WriteFile(pm.poolFile(name), data, 0644)
}
