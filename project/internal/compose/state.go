package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// FileStateManager implements StateManager using the filesystem
type FileStateManager struct {
	baseDir string
}

// NewFileStateManager creates a new file-based state manager
func NewFileStateManager(baseDir string) *FileStateManager {
	return &FileStateManager{baseDir: baseDir}
}

func (m *FileStateManager) stateDir() string {
	return filepath.Join(m.baseDir, "compose")
}

func (m *FileStateManager) statePath(name string) string {
	return filepath.Join(m.stateDir(), name+".json")
}

// SaveComposeState persists compose group state to disk
func (m *FileStateManager) SaveComposeState(name string, state *ComposeStatus) error {
	dir := m.stateDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create compose state dir: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal compose state: %w", err)
	}

	if err := os.WriteFile(m.statePath(name), data, 0o644); err != nil {
		return fmt.Errorf("failed to write compose state: %w", err)
	}

	return nil
}

// LoadComposeState reads compose group state from disk
func (m *FileStateManager) LoadComposeState(name string) (*ComposeStatus, error) {
	data, err := os.ReadFile(m.statePath(name))
	if err != nil {
		return nil, fmt.Errorf("failed to read compose state for %s: %w", name, err)
	}

	var state ComposeStatus
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal compose state: %w", err)
	}

	return &state, nil
}

// DeleteComposeState removes compose group state from disk
func (m *FileStateManager) DeleteComposeState(name string) error {
	if err := os.Remove(m.statePath(name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete compose state: %w", err)
	}
	return nil
}

// ListComposeGroups returns names of all saved compose groups
func (m *FileStateManager) ListComposeGroups() ([]string, error) {
	dir := m.stateDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read compose state dir: %w", err)
	}

	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			names = append(names, entry.Name()[:len(entry.Name())-5])
		}
	}
	return names, nil
}
