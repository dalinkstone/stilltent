// Package image provides the OCI registry client and credential management.
package image

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Credential represents stored authentication credentials for a registry.
type Credential struct {
	Registry  string    `json:"registry"`
	Username  string    `json:"username"`
	Password  string    `json:"password"` // base64-encoded
	CreatedAt time.Time `json:"created_at"`
}

// CredentialStore manages persistent registry credentials.
// Credentials are stored in a JSON file at ~/.tent/credentials.json.
type CredentialStore struct {
	mu       sync.RWMutex
	filePath string
	creds    map[string]*Credential // registry -> credential
}

// NewCredentialStore creates a credential store backed by the given base directory.
func NewCredentialStore(baseDir string) (*CredentialStore, error) {
	fp := filepath.Join(baseDir, "credentials.json")
	cs := &CredentialStore{
		filePath: fp,
		creds:    make(map[string]*Credential),
	}
	if err := cs.load(); err != nil {
		return nil, err
	}
	return cs, nil
}

// Store saves credentials for a registry, overwriting any existing entry.
func (cs *CredentialStore) Store(registry, username, password string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.creds[registry] = &Credential{
		Registry:  registry,
		Username:  username,
		Password:  base64.StdEncoding.EncodeToString([]byte(password)),
		CreatedAt: time.Now().UTC(),
	}
	return cs.save()
}

// Get returns credentials for a registry, or nil if not found.
func (cs *CredentialStore) Get(registry string) (username, password string, ok bool) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	cred, exists := cs.creds[registry]
	if !exists {
		return "", "", false
	}

	decoded, err := base64.StdEncoding.DecodeString(cred.Password)
	if err != nil {
		return cred.Username, "", true
	}
	return cred.Username, string(decoded), true
}

// Remove deletes credentials for a registry.
func (cs *CredentialStore) Remove(registry string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if _, exists := cs.creds[registry]; !exists {
		return fmt.Errorf("no credentials stored for %s", registry)
	}
	delete(cs.creds, registry)
	return cs.save()
}

// List returns all stored credentials (passwords are masked).
func (cs *CredentialStore) List() []Credential {
	cs.mu.RLock()
	defer cs.mu.RUnlock()

	result := make([]Credential, 0, len(cs.creds))
	for _, cred := range cs.creds {
		// Return a copy with masked password
		result = append(result, Credential{
			Registry:  cred.Registry,
			Username:  cred.Username,
			Password:  "********",
			CreatedAt: cred.CreatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Registry < result[j].Registry
	})
	return result
}

// load reads credentials from disk.
func (cs *CredentialStore) load() error {
	data, err := os.ReadFile(cs.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no credentials yet
		}
		return fmt.Errorf("failed to read credentials: %w", err)
	}

	var creds []*Credential
	if err := json.Unmarshal(data, &creds); err != nil {
		return fmt.Errorf("failed to parse credentials: %w", err)
	}

	for _, c := range creds {
		cs.creds[c.Registry] = c
	}
	return nil
}

// save writes credentials to disk with restricted permissions.
func (cs *CredentialStore) save() error {
	creds := make([]*Credential, 0, len(cs.creds))
	for _, c := range cs.creds {
		creds = append(creds, c)
	}
	sort.Slice(creds, func(i, j int) bool {
		return creds[i].Registry < creds[j].Registry
	})

	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal credentials: %w", err)
	}

	dir := filepath.Dir(cs.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create credentials directory: %w", err)
	}

	return os.WriteFile(cs.filePath, data, 0600)
}

// NormalizeRegistry normalizes a registry hostname.
// "docker.io" and "index.docker.io" both map to "index.docker.io".
func NormalizeRegistry(registry string) string {
	switch registry {
	case "docker.io", "":
		return "index.docker.io"
	default:
		return registry
	}
}
