package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// VolumeState tracks a created volume's metadata on disk.
type VolumeState struct {
	Name      string            `json:"name"`
	Driver    string            `json:"driver"`
	SizeMB    int               `json:"size_mb,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Group     string            `json:"group"`
	Path      string            `json:"path"`
	CreatedAt time.Time         `json:"created_at"`
}

// VolumeManager manages named volumes for compose groups.
type VolumeManager struct {
	baseDir string
	mu      sync.Mutex
}

// NewVolumeManager creates a volume manager rooted at baseDir.
func NewVolumeManager(baseDir string) *VolumeManager {
	return &VolumeManager{baseDir: baseDir}
}

// volumesDir returns the directory where all volumes live.
func (vm *VolumeManager) volumesDir() string {
	return filepath.Join(vm.baseDir, "volumes")
}

// volumeDir returns the data directory for a specific volume.
func (vm *VolumeManager) volumeDir(group, name string) string {
	return filepath.Join(vm.volumesDir(), group, name, "data")
}

// volumeStatePath returns the state file path for a volume.
func (vm *VolumeManager) volumeStatePath(group, name string) string {
	return filepath.Join(vm.volumesDir(), group, name, "state.json")
}

// EnsureVolumes creates all volumes declared in a compose config for the
// given group name. Volumes that already exist are left untouched.
// Returns a map of volume name -> host path.
func (vm *VolumeManager) EnsureVolumes(group string, volumes map[string]*VolumeConfig) (map[string]string, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	paths := make(map[string]string, len(volumes))

	for name, cfg := range volumes {
		dataDir := vm.volumeDir(group, name)

		// Create the data directory if it doesn't exist
		if err := os.MkdirAll(dataDir, 0o755); err != nil {
			return nil, fmt.Errorf("failed to create volume directory for %q: %w", name, err)
		}

		// Write state file if it doesn't exist
		statePath := vm.volumeStatePath(group, name)
		if _, err := os.Stat(statePath); os.IsNotExist(err) {
			driver := "local"
			if cfg != nil && cfg.Driver != "" {
				driver = cfg.Driver
			}
			var sizeMB int
			var labels map[string]string
			if cfg != nil {
				sizeMB = cfg.SizeMB
				labels = cfg.Labels
			}

			state := &VolumeState{
				Name:      name,
				Driver:    driver,
				SizeMB:    sizeMB,
				Labels:    labels,
				Group:     group,
				Path:      dataDir,
				CreatedAt: time.Now(),
			}

			data, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return nil, fmt.Errorf("failed to marshal volume state for %q: %w", name, err)
			}
			if err := os.WriteFile(statePath, data, 0o644); err != nil {
				return nil, fmt.Errorf("failed to write volume state for %q: %w", name, err)
			}
		}

		paths[name] = dataDir
	}

	return paths, nil
}

// RemoveVolumes removes all volumes associated with a compose group.
func (vm *VolumeManager) RemoveVolumes(group string) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	groupDir := filepath.Join(vm.volumesDir(), group)
	if _, err := os.Stat(groupDir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(groupDir)
}

// RemoveVolume removes a single named volume from a compose group.
func (vm *VolumeManager) RemoveVolume(group, name string) error {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	volDir := filepath.Join(vm.volumesDir(), group, name)
	if _, err := os.Stat(volDir); os.IsNotExist(err) {
		return fmt.Errorf("volume %q not found in group %q", name, group)
	}
	return os.RemoveAll(volDir)
}

// GetVolume returns the state of a specific volume.
func (vm *VolumeManager) GetVolume(group, name string) (*VolumeState, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	statePath := vm.volumeStatePath(group, name)
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil, fmt.Errorf("volume %q not found in group %q: %w", name, group, err)
	}

	var state VolumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse volume state for %q: %w", name, err)
	}
	return &state, nil
}

// ListVolumes lists all volumes for a compose group.
func (vm *VolumeManager) ListVolumes(group string) ([]*VolumeState, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	groupDir := filepath.Join(vm.volumesDir(), group)
	entries, err := os.ReadDir(groupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	var volumes []*VolumeState
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		statePath := filepath.Join(groupDir, entry.Name(), "state.json")
		data, err := os.ReadFile(statePath)
		if err != nil {
			continue
		}
		var state VolumeState
		if err := json.Unmarshal(data, &state); err != nil {
			continue
		}
		volumes = append(volumes, &state)
	}

	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].Name < volumes[j].Name
	})
	return volumes, nil
}

// ListAllVolumes lists all volumes across all compose groups.
func (vm *VolumeManager) ListAllVolumes() ([]*VolumeState, error) {
	vm.mu.Lock()
	defer vm.mu.Unlock()

	volDir := vm.volumesDir()
	groups, err := os.ReadDir(volDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to list volume groups: %w", err)
	}

	var all []*VolumeState
	for _, group := range groups {
		if !group.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(volDir, group.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			statePath := filepath.Join(volDir, group.Name(), entry.Name(), "state.json")
			data, err := os.ReadFile(statePath)
			if err != nil {
				continue
			}
			var state VolumeState
			if err := json.Unmarshal(data, &state); err != nil {
				continue
			}
			all = append(all, &state)
		}
	}

	sort.Slice(all, func(i, j int) bool {
		if all[i].Group != all[j].Group {
			return all[i].Group < all[j].Group
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

// VolumePath returns the host path for a named volume in a group.
func (vm *VolumeManager) VolumePath(group, name string) string {
	return vm.volumeDir(group, name)
}
