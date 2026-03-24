package vm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Workspace represents a named shared directory that can be attached to multiple sandboxes.
type Workspace struct {
	Name        string   `json:"name"`
	HostPath    string   `json:"host_path"`
	MountPoint  string   `json:"mount_point"`
	Readonly    bool     `json:"readonly,omitempty"`
	Sandboxes   []string `json:"sandboxes,omitempty"`
	Description string   `json:"description,omitempty"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
}

// WorkspaceManager manages named workspaces that can be shared across sandboxes.
type WorkspaceManager struct {
	mu         sync.RWMutex
	baseDir    string
	workspaces map[string]*Workspace
}

// NewWorkspaceManager creates a new workspace manager.
func NewWorkspaceManager(baseDir string) (*WorkspaceManager, error) {
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		baseDir = filepath.Join(home, ".tent")
	}

	wm := &WorkspaceManager{
		baseDir:    baseDir,
		workspaces: make(map[string]*Workspace),
	}

	if err := wm.load(); err != nil {
		return nil, err
	}

	return wm, nil
}

func (wm *WorkspaceManager) stateFile() string {
	return filepath.Join(wm.baseDir, "workspaces.json")
}

func (wm *WorkspaceManager) load() error {
	data, err := os.ReadFile(wm.stateFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &wm.workspaces)
}

func (wm *WorkspaceManager) save() error {
	if err := os.MkdirAll(wm.baseDir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(wm.workspaces, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(wm.stateFile(), data, 0644)
}

// Create registers a new named workspace pointing to a host directory.
func (wm *WorkspaceManager) Create(name, hostPath, mountPoint, description string, readonly bool) (*Workspace, error) {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("workspace name is required")
	}

	if _, exists := wm.workspaces[name]; exists {
		return nil, fmt.Errorf("workspace %q already exists", name)
	}

	absPath, err := filepath.Abs(hostPath)
	if err != nil {
		return nil, fmt.Errorf("resolving host path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("host path %q does not exist: %w", absPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("host path %q is not a directory", absPath)
	}

	if mountPoint == "" {
		mountPoint = "/workspace/" + name
	}

	now := time.Now().Unix()
	ws := &Workspace{
		Name:        name,
		HostPath:    absPath,
		MountPoint:  mountPoint,
		Readonly:    readonly,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	wm.workspaces[name] = ws
	if err := wm.save(); err != nil {
		return nil, err
	}

	return ws, nil
}

// Get returns a workspace by name.
func (wm *WorkspaceManager) Get(name string) (*Workspace, error) {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	ws, exists := wm.workspaces[name]
	if !exists {
		return nil, fmt.Errorf("workspace %q not found", name)
	}
	return ws, nil
}

// List returns all registered workspaces.
func (wm *WorkspaceManager) List() []*Workspace {
	wm.mu.RLock()
	defer wm.mu.RUnlock()

	result := make([]*Workspace, 0, len(wm.workspaces))
	for _, ws := range wm.workspaces {
		result = append(result, ws)
	}
	return result
}

// Remove deletes a workspace registration. Does not delete the host directory.
func (wm *WorkspaceManager) Remove(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	ws, exists := wm.workspaces[name]
	if !exists {
		return fmt.Errorf("workspace %q not found", name)
	}

	if len(ws.Sandboxes) > 0 {
		return fmt.Errorf("workspace %q is attached to sandboxes: %v (use --force to remove anyway)", name, ws.Sandboxes)
	}

	delete(wm.workspaces, name)
	return wm.save()
}

// ForceRemove deletes a workspace registration regardless of attachments.
func (wm *WorkspaceManager) ForceRemove(name string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	if _, exists := wm.workspaces[name]; !exists {
		return fmt.Errorf("workspace %q not found", name)
	}

	delete(wm.workspaces, name)
	return wm.save()
}

// Attach records that a sandbox is using this workspace.
func (wm *WorkspaceManager) Attach(workspaceName, sandboxName string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	ws, exists := wm.workspaces[workspaceName]
	if !exists {
		return fmt.Errorf("workspace %q not found", workspaceName)
	}

	for _, s := range ws.Sandboxes {
		if s == sandboxName {
			return fmt.Errorf("workspace %q is already attached to sandbox %q", workspaceName, sandboxName)
		}
	}

	ws.Sandboxes = append(ws.Sandboxes, sandboxName)
	ws.UpdatedAt = time.Now().Unix()
	return wm.save()
}

// Detach removes a sandbox from a workspace's attachment list.
func (wm *WorkspaceManager) Detach(workspaceName, sandboxName string) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	ws, exists := wm.workspaces[workspaceName]
	if !exists {
		return fmt.Errorf("workspace %q not found", workspaceName)
	}

	found := false
	filtered := make([]string, 0, len(ws.Sandboxes))
	for _, s := range ws.Sandboxes {
		if s == sandboxName {
			found = true
		} else {
			filtered = append(filtered, s)
		}
	}

	if !found {
		return fmt.Errorf("workspace %q is not attached to sandbox %q", workspaceName, sandboxName)
	}

	ws.Sandboxes = filtered
	ws.UpdatedAt = time.Now().Unix()
	return wm.save()
}

// Update modifies workspace properties.
func (wm *WorkspaceManager) Update(name string, updateFn func(*Workspace) error) error {
	wm.mu.Lock()
	defer wm.mu.Unlock()

	ws, exists := wm.workspaces[name]
	if !exists {
		return fmt.Errorf("workspace %q not found", name)
	}

	if err := updateFn(ws); err != nil {
		return err
	}

	ws.UpdatedAt = time.Now().Unix()
	return wm.save()
}
