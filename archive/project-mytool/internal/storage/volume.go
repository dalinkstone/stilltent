package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Volume represents a named persistent volume that can be attached to sandboxes.
type Volume struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	SizeMB     int               `json:"size_mb"`
	MountPoint string            `json:"mount_point,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
	Sandboxes  []string          `json:"sandboxes"`
	CreatedAt  int64             `json:"created_at"`
	UsedBytes  int64             `json:"used_bytes"`
}

// VolumeStore manages named persistent volumes.
type VolumeStore struct {
	baseDir string
	volumes map[string]*Volume
	mu      sync.RWMutex
}

// NewVolumeStore creates or loads the volume store from disk.
func NewVolumeStore(baseDir string) (*VolumeStore, error) {
	vs := &VolumeStore{
		baseDir: baseDir,
		volumes: make(map[string]*Volume),
	}

	metaPath := vs.metaPath()
	data, err := os.ReadFile(metaPath)
	if err == nil {
		var vols map[string]*Volume
		if err := json.Unmarshal(data, &vols); err == nil {
			vs.volumes = vols
		}
	}

	return vs, nil
}

func (vs *VolumeStore) metaPath() string {
	return filepath.Join(vs.baseDir, "volumes", "volumes.json")
}

func (vs *VolumeStore) volumeDir(name string) string {
	return filepath.Join(vs.baseDir, "volumes", name)
}

func (vs *VolumeStore) save() error {
	dir := filepath.Join(vs.baseDir, "volumes")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(vs.volumes, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(vs.metaPath(), data, 0644)
}

// Create creates a new named volume with the given size.
func (vs *VolumeStore) Create(name, driver string, sizeMB int, labels map[string]string) (*Volume, error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	if _, exists := vs.volumes[name]; exists {
		return nil, fmt.Errorf("volume %q already exists", name)
	}

	if driver == "" {
		driver = "local"
	}
	if sizeMB <= 0 {
		sizeMB = 1024 // default 1GB
	}

	volDir := vs.volumeDir(name)
	if err := os.MkdirAll(volDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create volume directory: %w", err)
	}

	// Create the volume data file (sparse file)
	dataPath := filepath.Join(volDir, "data.img")
	f, err := os.Create(dataPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume data file: %w", err)
	}
	sizeBytes := int64(sizeMB) * 1024 * 1024
	if err := f.Truncate(sizeBytes); err != nil {
		f.Close()
		return nil, fmt.Errorf("failed to allocate volume: %w", err)
	}
	f.Close()

	vol := &Volume{
		Name:      name,
		Driver:    driver,
		SizeMB:    sizeMB,
		Labels:    labels,
		Sandboxes: []string{},
		CreatedAt: time.Now().Unix(),
	}

	vs.volumes[name] = vol
	if err := vs.save(); err != nil {
		return nil, fmt.Errorf("failed to save volume metadata: %w", err)
	}

	return vol, nil
}

// Get returns a volume by name.
func (vs *VolumeStore) Get(name string) (*Volume, error) {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	vol, ok := vs.volumes[name]
	if !ok {
		return nil, fmt.Errorf("volume %q not found", name)
	}

	// Update used bytes from disk
	dataPath := filepath.Join(vs.volumeDir(name), "data.img")
	if info, err := os.Stat(dataPath); err == nil {
		vol.UsedBytes = info.Size()
	}

	return vol, nil
}

// List returns all volumes.
func (vs *VolumeStore) List() []*Volume {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	result := make([]*Volume, 0, len(vs.volumes))
	for _, vol := range vs.volumes {
		result = append(result, vol)
	}
	return result
}

// Remove deletes a volume by name. Fails if the volume is in use unless force is true.
func (vs *VolumeStore) Remove(name string, force bool) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vol, ok := vs.volumes[name]
	if !ok {
		return fmt.Errorf("volume %q not found", name)
	}

	if len(vol.Sandboxes) > 0 && !force {
		return fmt.Errorf("volume %q is in use by sandbox(es): %v — use --force to remove anyway", name, vol.Sandboxes)
	}

	volDir := vs.volumeDir(name)
	if err := os.RemoveAll(volDir); err != nil {
		return fmt.Errorf("failed to remove volume data: %w", err)
	}

	delete(vs.volumes, name)
	return vs.save()
}

// Attach records a sandbox as using a volume.
func (vs *VolumeStore) Attach(volumeName, sandboxName string) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vol, ok := vs.volumes[volumeName]
	if !ok {
		return fmt.Errorf("volume %q not found", volumeName)
	}

	for _, s := range vol.Sandboxes {
		if s == sandboxName {
			return nil // already attached
		}
	}

	vol.Sandboxes = append(vol.Sandboxes, sandboxName)
	return vs.save()
}

// Detach removes a sandbox from a volume's user list.
func (vs *VolumeStore) Detach(volumeName, sandboxName string) error {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	vol, ok := vs.volumes[volumeName]
	if !ok {
		return fmt.Errorf("volume %q not found", volumeName)
	}

	filtered := make([]string, 0, len(vol.Sandboxes))
	for _, s := range vol.Sandboxes {
		if s != sandboxName {
			filtered = append(filtered, s)
		}
	}
	vol.Sandboxes = filtered
	return vs.save()
}

// Prune removes all volumes that are not attached to any sandbox.
func (vs *VolumeStore) Prune() ([]string, error) {
	vs.mu.Lock()
	defer vs.mu.Unlock()

	var pruned []string
	for name, vol := range vs.volumes {
		if len(vol.Sandboxes) == 0 {
			volDir := vs.volumeDir(name)
			if err := os.RemoveAll(volDir); err != nil {
				return pruned, fmt.Errorf("failed to remove volume %q: %w", name, err)
			}
			pruned = append(pruned, name)
			delete(vs.volumes, name)
		}
	}

	if len(pruned) > 0 {
		if err := vs.save(); err != nil {
			return pruned, err
		}
	}

	return pruned, nil
}

// DataPath returns the path to a volume's data file.
func (vs *VolumeStore) DataPath(name string) (string, error) {
	vs.mu.RLock()
	defer vs.mu.RUnlock()

	if _, ok := vs.volumes[name]; !ok {
		return "", fmt.Errorf("volume %q not found", name)
	}

	return filepath.Join(vs.volumeDir(name), "data.img"), nil
}
