package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// Template represents a reusable sandbox configuration template.
type Template struct {
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	Config      models.VMConfig  `json:"config"`
}

// TemplateManager manages saved sandbox configuration templates.
type TemplateManager struct {
	dir string
}

// NewTemplateManager creates a new template manager using the given data directory.
// Templates are stored as JSON files under <dataDir>/templates/.
func NewTemplateManager(dataDir string) (*TemplateManager, error) {
	if dataDir == "" {
		dataDir = "~/.tent"
	}

	// Expand ~
	if len(dataDir) > 0 && dataDir[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("template: cannot determine home directory: %w", err)
		}
		dataDir = filepath.Join(home, dataDir[1:])
	}

	dir := filepath.Join(dataDir, "templates")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("template: cannot create templates directory: %w", err)
	}

	return &TemplateManager{dir: dir}, nil
}

// Save saves a VMConfig as a named template. If a template with the same name
// already exists, it is overwritten and the UpdatedAt timestamp is refreshed.
func (tm *TemplateManager) Save(name, description string, cfg models.VMConfig) error {
	if err := validateTemplateName(name); err != nil {
		return err
	}

	path := tm.templatePath(name)

	now := time.Now().UTC()
	tmpl := Template{
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
		Config:      cfg,
	}

	// Preserve original creation time if updating
	existing, err := tm.Get(name)
	if err == nil && existing != nil {
		tmpl.CreatedAt = existing.CreatedAt
	}

	// Clear the name from the embedded config to avoid redundancy
	tmpl.Config.Name = ""

	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return fmt.Errorf("template: failed to marshal template: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("template: failed to write template file: %w", err)
	}

	return nil
}

// Get retrieves a template by name.
func (tm *TemplateManager) Get(name string) (*Template, error) {
	path := tm.templatePath(name)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("template '%s' not found", name)
		}
		return nil, fmt.Errorf("template: failed to read template: %w", err)
	}

	var tmpl Template
	if err := json.Unmarshal(data, &tmpl); err != nil {
		return nil, fmt.Errorf("template: failed to parse template: %w", err)
	}

	return &tmpl, nil
}

// List returns all saved templates, sorted by name.
func (tm *TemplateManager) List() ([]*Template, error) {
	entries, err := os.ReadDir(tm.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("template: failed to list templates: %w", err)
	}

	var templates []*Template
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		name := strings.TrimSuffix(entry.Name(), ".json")
		tmpl, err := tm.Get(name)
		if err != nil {
			continue // Skip malformed templates
		}
		templates = append(templates, tmpl)
	}

	sort.Slice(templates, func(i, j int) bool {
		return templates[i].Name < templates[j].Name
	})

	return templates, nil
}

// Delete removes a template by name.
func (tm *TemplateManager) Delete(name string) error {
	path := tm.templatePath(name)

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("template '%s' not found", name)
	}

	if err := os.Remove(path); err != nil {
		return fmt.Errorf("template: failed to delete template: %w", err)
	}

	return nil
}

// Apply creates a VMConfig from a template, overriding the sandbox name.
func (tm *TemplateManager) Apply(templateName, sandboxName string) (*models.VMConfig, error) {
	tmpl, err := tm.Get(templateName)
	if err != nil {
		return nil, err
	}

	cfg := tmpl.Config
	cfg.Name = sandboxName

	return &cfg, nil
}

// templatePath returns the file path for a template.
func (tm *TemplateManager) templatePath(name string) string {
	return filepath.Join(tm.dir, name+".json")
}

// validateTemplateName checks that a template name is valid.
func validateTemplateName(name string) error {
	if name == "" {
		return fmt.Errorf("template name cannot be empty")
	}
	if len(name) > 128 {
		return fmt.Errorf("template name too long (max 128 characters)")
	}
	for _, c := range name {
		if !isValidNameChar(c) {
			return fmt.Errorf("template name contains invalid character '%c' (allowed: a-z, 0-9, dash, underscore)", c)
		}
	}
	if name[0] == '-' || name[0] == '_' {
		return fmt.Errorf("template name must start with a letter or digit")
	}
	return nil
}

// isValidNameChar returns true if the rune is allowed in a template name.
func isValidNameChar(c rune) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
}
