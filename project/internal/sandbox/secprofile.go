package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// SecurityProfile defines a named set of isolation constraints for sandboxes.
type SecurityProfile struct {
	// Name is the unique identifier for this profile.
	Name string `json:"name" yaml:"name"`
	// Description is a human-readable summary of the profile's purpose.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	// NetworkPolicy controls outbound network access.
	NetworkPolicy ProfileNetworkPolicy `json:"network_policy" yaml:"network_policy"`
	// MountPolicy controls host filesystem mount restrictions.
	MountPolicy ProfileMountPolicy `json:"mount_policy" yaml:"mount_policy"`
	// ResourceCaps sets upper bounds on resource allocation.
	ResourceCaps ProfileResourceCaps `json:"resource_caps" yaml:"resource_caps"`
	// Capabilities lists Linux capabilities allowed in the sandbox.
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// ReadonlyRootFS forces the root filesystem to be mounted read-only.
	ReadonlyRootFS bool `json:"readonly_rootfs,omitempty" yaml:"readonly_rootfs,omitempty"`
	// NoNewPrivileges prevents gaining additional privileges.
	NoNewPrivileges bool `json:"no_new_privileges,omitempty" yaml:"no_new_privileges,omitempty"`
	// Builtin indicates this is a system-provided profile that cannot be deleted.
	Builtin bool `json:"builtin,omitempty" yaml:"builtin,omitempty"`
}

// ProfileNetworkPolicy defines network isolation rules within a profile.
type ProfileNetworkPolicy struct {
	// EgressPolicy is the default outbound policy: "block" (default) or "allow".
	EgressPolicy string `json:"egress_policy" yaml:"egress_policy"`
	// AllowedEndpoints is a whitelist of allowed outbound destinations (host:port or CIDR).
	AllowedEndpoints []string `json:"allowed_endpoints,omitempty" yaml:"allowed_endpoints,omitempty"`
	// AllowDNS permits DNS resolution (port 53) even when egress is blocked.
	AllowDNS bool `json:"allow_dns,omitempty" yaml:"allow_dns,omitempty"`
	// AllowInterSandbox permits communication with other sandboxes on the same bridge.
	AllowInterSandbox bool `json:"allow_inter_sandbox,omitempty" yaml:"allow_inter_sandbox,omitempty"`
}

// ProfileMountPolicy defines mount restrictions within a profile.
type ProfileMountPolicy struct {
	// AllowHostMounts permits mounting host directories into the sandbox.
	AllowHostMounts bool `json:"allow_host_mounts" yaml:"allow_host_mounts"`
	// ForceReadonly makes all mounts read-only regardless of mount config.
	ForceReadonly bool `json:"force_readonly,omitempty" yaml:"force_readonly,omitempty"`
	// AllowedPaths restricts which host paths can be mounted (glob patterns).
	// Empty means all paths allowed (when AllowHostMounts is true).
	AllowedPaths []string `json:"allowed_paths,omitempty" yaml:"allowed_paths,omitempty"`
	// DeniedPaths blocks specific host paths from being mounted.
	DeniedPaths []string `json:"denied_paths,omitempty" yaml:"denied_paths,omitempty"`
}

// ProfileResourceCaps defines resource upper bounds within a profile.
type ProfileResourceCaps struct {
	// MaxVCPUs is the maximum vCPUs allowed (0 = no cap).
	MaxVCPUs int `json:"max_vcpus,omitempty" yaml:"max_vcpus,omitempty"`
	// MaxMemoryMB is the maximum memory in MB (0 = no cap).
	MaxMemoryMB int `json:"max_memory_mb,omitempty" yaml:"max_memory_mb,omitempty"`
	// MaxDiskGB is the maximum disk in GB (0 = no cap).
	MaxDiskGB int `json:"max_disk_gb,omitempty" yaml:"max_disk_gb,omitempty"`
	// MaxPids limits the number of processes (0 = no cap).
	MaxPids int `json:"max_pids,omitempty" yaml:"max_pids,omitempty"`
}

// Validate checks that the security profile has valid configuration.
func (p *SecurityProfile) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("profile name is required")
	}
	if p.NetworkPolicy.EgressPolicy != "" &&
		p.NetworkPolicy.EgressPolicy != "block" &&
		p.NetworkPolicy.EgressPolicy != "allow" {
		return fmt.Errorf("egress_policy must be 'block' or 'allow', got %q", p.NetworkPolicy.EgressPolicy)
	}
	if p.ResourceCaps.MaxVCPUs < 0 {
		return fmt.Errorf("max_vcpus must be non-negative")
	}
	if p.ResourceCaps.MaxMemoryMB < 0 {
		return fmt.Errorf("max_memory_mb must be non-negative")
	}
	if p.ResourceCaps.MaxDiskGB < 0 {
		return fmt.Errorf("max_disk_gb must be non-negative")
	}
	if p.ResourceCaps.MaxPids < 0 {
		return fmt.Errorf("max_pids must be non-negative")
	}
	return nil
}

// CheckVMConfig validates a VM configuration against this profile's constraints.
// Returns a list of violations, or nil if the config is compliant.
func (p *SecurityProfile) CheckVMConfig(vcpus, memoryMB, diskGB int) []string {
	var violations []string

	if p.ResourceCaps.MaxVCPUs > 0 && vcpus > p.ResourceCaps.MaxVCPUs {
		violations = append(violations, fmt.Sprintf(
			"vCPUs %d exceeds profile cap %d", vcpus, p.ResourceCaps.MaxVCPUs))
	}
	if p.ResourceCaps.MaxMemoryMB > 0 && memoryMB > p.ResourceCaps.MaxMemoryMB {
		violations = append(violations, fmt.Sprintf(
			"memory %dMB exceeds profile cap %dMB", memoryMB, p.ResourceCaps.MaxMemoryMB))
	}
	if p.ResourceCaps.MaxDiskGB > 0 && diskGB > p.ResourceCaps.MaxDiskGB {
		violations = append(violations, fmt.Sprintf(
			"disk %dGB exceeds profile cap %dGB", diskGB, p.ResourceCaps.MaxDiskGB))
	}

	return violations
}

// CheckMount validates whether a host mount path is allowed under this profile.
func (p *SecurityProfile) CheckMount(hostPath string, readonly bool) error {
	if !p.MountPolicy.AllowHostMounts {
		return fmt.Errorf("host mounts are not allowed under profile %q", p.Name)
	}

	// Check denied paths
	for _, denied := range p.MountPolicy.DeniedPaths {
		matched, _ := filepath.Match(denied, hostPath)
		if matched {
			return fmt.Errorf("mount path %q is denied by profile %q", hostPath, p.Name)
		}
	}

	// Check allowed paths if specified
	if len(p.MountPolicy.AllowedPaths) > 0 {
		allowed := false
		for _, pattern := range p.MountPolicy.AllowedPaths {
			matched, _ := filepath.Match(pattern, hostPath)
			if matched {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("mount path %q is not in allowed paths for profile %q", hostPath, p.Name)
		}
	}

	// Check readonly enforcement
	if p.MountPolicy.ForceReadonly && !readonly {
		return fmt.Errorf("profile %q requires all mounts to be read-only", p.Name)
	}

	return nil
}

// SecurityProfileManager manages security profile storage and lookup.
type SecurityProfileManager struct {
	mu       sync.RWMutex
	baseDir  string
	profiles map[string]*SecurityProfile
}

// NewSecurityProfileManager creates a new profile manager.
func NewSecurityProfileManager(baseDir string) *SecurityProfileManager {
	m := &SecurityProfileManager{
		baseDir:  baseDir,
		profiles: make(map[string]*SecurityProfile),
	}
	m.registerBuiltins()
	return m
}

// registerBuiltins adds the built-in security profiles.
func (m *SecurityProfileManager) registerBuiltins() {
	m.profiles["default"] = &SecurityProfile{
		Name:        "default",
		Description: "Standard isolation: blocked egress, host mounts allowed, no resource caps",
		Builtin:     true,
		NetworkPolicy: ProfileNetworkPolicy{
			EgressPolicy:      "block",
			AllowDNS:          true,
			AllowInterSandbox: true,
		},
		MountPolicy: ProfileMountPolicy{
			AllowHostMounts: true,
			DeniedPaths:     []string{"/etc/shadow", "/etc/passwd"},
		},
		ResourceCaps:    ProfileResourceCaps{},
		NoNewPrivileges: true,
	}

	m.profiles["strict"] = &SecurityProfile{
		Name:        "strict",
		Description: "Maximum isolation: no egress, no host mounts, tight resource caps, read-only rootfs",
		Builtin:     true,
		NetworkPolicy: ProfileNetworkPolicy{
			EgressPolicy:      "block",
			AllowDNS:          false,
			AllowInterSandbox: false,
		},
		MountPolicy: ProfileMountPolicy{
			AllowHostMounts: false,
			ForceReadonly:   true,
		},
		ResourceCaps: ProfileResourceCaps{
			MaxVCPUs:    4,
			MaxMemoryMB: 4096,
			MaxDiskGB:   20,
			MaxPids:     1024,
		},
		ReadonlyRootFS:  true,
		NoNewPrivileges: true,
	}

	m.profiles["privileged"] = &SecurityProfile{
		Name:        "privileged",
		Description: "Minimal isolation: open egress, host mounts allowed, no resource caps",
		Builtin:     true,
		NetworkPolicy: ProfileNetworkPolicy{
			EgressPolicy:      "allow",
			AllowDNS:          true,
			AllowInterSandbox: true,
		},
		MountPolicy: ProfileMountPolicy{
			AllowHostMounts: true,
		},
		ResourceCaps:    ProfileResourceCaps{},
		NoNewPrivileges: false,
	}
}

// Get returns a security profile by name, checking custom profiles first,
// then built-ins. If the profile is not in memory, it attempts to load it
// from disk.
func (m *SecurityProfileManager) Get(name string) (*SecurityProfile, error) {
	m.mu.RLock()
	if p, ok := m.profiles[name]; ok {
		m.mu.RUnlock()
		return p, nil
	}
	m.mu.RUnlock()

	// Not in memory — try loading from disk under a write lock
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if p, ok := m.profiles[name]; ok {
		return p, nil
	}

	p, err := m.loadFromDisk(name)
	if err != nil {
		return nil, fmt.Errorf("security profile %q not found", name)
	}
	m.profiles[name] = p
	return p, nil
}

// List returns all available security profiles sorted by name.
func (m *SecurityProfileManager) List() []*SecurityProfile {
	m.mu.Lock()
	// Load any custom profiles from disk (writes to m.profiles)
	m.loadCustomFromDisk()

	profiles := make([]*SecurityProfile, 0, len(m.profiles))
	for _, p := range m.profiles {
		profiles = append(profiles, p)
	}
	m.mu.Unlock()

	sort.Slice(profiles, func(i, j int) bool {
		return profiles[i].Name < profiles[j].Name
	})
	return profiles
}

// Save stores a custom security profile to disk.
func (m *SecurityProfileManager) Save(p *SecurityProfile) error {
	if err := p.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Don't allow overwriting builtins
	if existing, ok := m.profiles[p.Name]; ok && existing.Builtin {
		return fmt.Errorf("cannot modify built-in profile %q", p.Name)
	}

	profileDir := filepath.Join(m.baseDir, "security-profiles")
	if err := os.MkdirAll(profileDir, 0755); err != nil {
		return fmt.Errorf("failed to create profile directory: %w", err)
	}

	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	path := filepath.Join(profileDir, p.Name+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	m.profiles[p.Name] = p
	return nil
}

// Delete removes a custom security profile.
func (m *SecurityProfileManager) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if p, ok := m.profiles[name]; ok && p.Builtin {
		return fmt.Errorf("cannot delete built-in profile %q", name)
	}

	path := filepath.Join(m.baseDir, "security-profiles", name+".json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove profile file: %w", err)
	}

	delete(m.profiles, name)
	return nil
}

// loadFromDisk loads a single profile from disk.
func (m *SecurityProfileManager) loadFromDisk(name string) (*SecurityProfile, error) {
	path := filepath.Join(m.baseDir, "security-profiles", name+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var p SecurityProfile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// loadCustomFromDisk scans the profile directory and loads any not yet in memory.
func (m *SecurityProfileManager) loadCustomFromDisk() {
	profileDir := filepath.Join(m.baseDir, "security-profiles")
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) < 6 || name[len(name)-5:] != ".json" {
			continue
		}
		profileName := name[:len(name)-5]
		if _, exists := m.profiles[profileName]; exists {
			continue
		}
		if p, err := m.loadFromDisk(profileName); err == nil {
			m.profiles[profileName] = p
		}
	}
}
