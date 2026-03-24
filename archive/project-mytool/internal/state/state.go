package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// StateManager manages persistent VM state
type StateManager struct {
	mu     sync.RWMutex
	path   string
	state  map[string]*models.VMState
	images map[string]*models.ImageInfo
}

// NewStateManager creates a new state manager
func NewStateManager(dataDir string) (*StateManager, error) {
	if dataDir == "" {
		dataDir = "~/.tent"
	}
	
	// Expand ~
	if dataDir[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		dataDir = filepath.Join(home, dataDir[1:])
	}
	
	// Create data directory
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	
	sm := &StateManager{
		path:   filepath.Join(dataDir, "state.json"),
		state:  make(map[string]*models.VMState),
		images: make(map[string]*models.ImageInfo),
	}
	
	if err := sm.load(); err != nil {
		return nil, err
	}
	
	return sm, nil
}

func (sm *StateManager) load() error {
	data, err := os.ReadFile(sm.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	
	var stored struct {
		VMs      map[string]*models.VMState `json:"vms"`
		Images   map[string]*models.ImageInfo `json:"images"`
	}
	
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	
	if stored.VMs != nil {
		sm.state = stored.VMs
	}
	if stored.Images != nil {
		sm.images = stored.Images
	}
	
	return nil
}

func (sm *StateManager) save() error {
	// NOTE: This function does NOT acquire the lock. The caller must hold the lock.
	data, err := json.MarshalIndent(struct {
		VMs      map[string]*models.VMState `json:"vms"`
		Images   map[string]*models.ImageInfo `json:"images"`
	}{
		VMs:      sm.state,
		Images:   sm.images,
	}, "", "  ")
	if err != nil {
		return err
	}
	
	return os.WriteFile(sm.path, data, 0600)
}

// StoreVM stores a VM state
func (sm *StateManager) StoreVM(vm *models.VMState) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	vm.UpdatedAt = sm.now()
	sm.state[vm.Name] = vm
	
	return sm.save()
}

// GetVM retrieves a VM state by name
func (sm *StateManager) GetVM(name string) (*models.VMState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	vm, ok := sm.state[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	
	return vm, nil
}

// ListVMs returns all VM states
func (sm *StateManager) ListVMs() ([]*models.VMState, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	vms := make([]*models.VMState, 0, len(sm.state))
	for _, vm := range sm.state {
		vms = append(vms, vm)
	}
	
	return vms, nil
}

// DeleteVM removes a VM state
func (sm *StateManager) DeleteVM(name string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	if _, ok := sm.state[name]; !ok {
		return os.ErrNotExist
	}
	
	delete(sm.state, name)
	return sm.save()
}

// RenameVM renames a VM in the state store
func (sm *StateManager) RenameVM(oldName, newName string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	vm, ok := sm.state[oldName]
	if !ok {
		return os.ErrNotExist
	}

	if _, exists := sm.state[newName]; exists {
		return fmt.Errorf("sandbox %q already exists", newName)
	}

	delete(sm.state, oldName)
	vm.Name = newName
	vm.UpdatedAt = sm.now()
	sm.state[newName] = vm

	return sm.save()
}

// UpdateVM updates a VM state
func (sm *StateManager) UpdateVM(name string, updateFn func(*models.VMState) error) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	vm, ok := sm.state[name]
	if !ok {
		return os.ErrNotExist
	}
	
	if err := updateFn(vm); err != nil {
		return err
	}
	
	vm.UpdatedAt = sm.now()
	return sm.save()
}

// StoreImage stores an image info
func (sm *StateManager) StoreImage(img *models.ImageInfo) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	
	sm.images[img.Name] = img
	
	return sm.save()
}

// GetImage retrieves an image info by name
func (sm *StateManager) GetImage(name string) (*models.ImageInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	img, ok := sm.images[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	
	return img, nil
}

// ListImages returns all image infos
func (sm *StateManager) ListImages() ([]*models.ImageInfo, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	
	images := make([]*models.ImageInfo, 0, len(sm.images))
	for _, img := range sm.images {
		images = append(images, img)
	}
	
	return images, nil
}

func (sm *StateManager) now() int64 {
	return time.Now().Unix()
}
