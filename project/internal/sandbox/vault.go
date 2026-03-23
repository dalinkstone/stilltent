package vm

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SecretEntry represents a single encrypted secret
type SecretEntry struct {
	Name       string    `json:"name"`
	Ciphertext string    `json:"ciphertext"` // hex-encoded AES-GCM encrypted value
	Nonce      string    `json:"nonce"`       // hex-encoded nonce
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// SecretStore manages encrypted secrets for sandboxes
type SecretStore struct {
	mu       sync.Mutex
	baseDir  string
	storeDir string
}

// SecretMetadata contains non-sensitive info about a secret
type SecretMetadata struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SandboxSecretBinding maps secret names to environment variable names
type SandboxSecretBinding struct {
	Bindings map[string]string `json:"bindings"` // secret_name -> env_var_name
}

// NewSecretStore creates a new secret store
func NewSecretStore(baseDir string) (*SecretStore, error) {
	storeDir := filepath.Join(baseDir, "secrets")
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create secrets directory: %w", err)
	}
	return &SecretStore{
		baseDir:  baseDir,
		storeDir: storeDir,
	}, nil
}

// deriveKey returns the AES-256 encryption key, reading it from a key file
// or generating a new random key on first use.
func (s *SecretStore) deriveKey() []byte {
	keyPath := filepath.Join(s.storeDir, "vault.key")
	data, err := os.ReadFile(keyPath)
	if err == nil && len(data) == 32 {
		return data
	}

	// Generate a new random 32-byte key
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		// Fall back to deterministic key if random fails (should never happen)
		hostname, _ := os.Hostname()
		material := fmt.Sprintf("tent-secrets:%s:%s", s.storeDir, hostname)
		hash := sha256.Sum256([]byte(material))
		return hash[:]
	}

	// Persist the key with restrictive permissions
	_ = os.WriteFile(keyPath, key, 0600)
	return key
}

// encrypt encrypts plaintext using AES-256-GCM
func (s *SecretStore) encrypt(plaintext []byte) (ciphertext, nonce []byte, err error) {
	key := s.deriveKey()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	nonce = make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("failed to generate nonce: %w", err)
	}

	ciphertext = gcm.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

// decrypt decrypts ciphertext using AES-256-GCM
func (s *SecretStore) decrypt(ciphertext, nonce []byte) ([]byte, error) {
	key := s.deriveKey()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt: %w", err)
	}

	return plaintext, nil
}

// Set stores or updates an encrypted secret
func (s *SecretStore) Set(name string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if name == "" {
		return fmt.Errorf("secret name cannot be empty")
	}
	if strings.ContainsAny(name, "/\\. ") {
		return fmt.Errorf("secret name cannot contain /, \\, ., or spaces")
	}

	ciphertext, nonce, err := s.encrypt(value)
	if err != nil {
		return fmt.Errorf("failed to encrypt secret: %w", err)
	}

	now := time.Now().UTC()
	entry := SecretEntry{
		Name:       name,
		Ciphertext: hex.EncodeToString(ciphertext),
		Nonce:      hex.EncodeToString(nonce),
		UpdatedAt:  now,
	}

	// Preserve creation time if updating
	existing, err := s.loadEntry(name)
	if err == nil && existing != nil {
		entry.CreatedAt = existing.CreatedAt
	} else {
		entry.CreatedAt = now
	}

	return s.saveEntry(&entry)
}

// Get retrieves and decrypts a secret
func (s *SecretStore) Get(name string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, err := s.loadEntry(name)
	if err != nil {
		return nil, err
	}

	ciphertext, err := hex.DecodeString(entry.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("corrupted secret data: %w", err)
	}

	nonce, err := hex.DecodeString(entry.Nonce)
	if err != nil {
		return nil, fmt.Errorf("corrupted secret nonce: %w", err)
	}

	return s.decrypt(ciphertext, nonce)
}

// Delete removes a secret
func (s *SecretStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.entryPath(name)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("secret %q not found", name)
	}
	return os.Remove(path)
}

// List returns metadata for all secrets
func (s *SecretStore) List() ([]SecretMetadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.storeDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read secrets directory: %w", err)
	}

	var secrets []SecretMetadata
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".secret") {
			continue
		}

		name := strings.TrimSuffix(e.Name(), ".secret")
		entry, err := s.loadEntry(name)
		if err != nil {
			continue
		}

		secrets = append(secrets, SecretMetadata{
			Name:      entry.Name,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
		})
	}

	sort.Slice(secrets, func(i, j int) bool {
		return secrets[i].Name < secrets[j].Name
	})

	return secrets, nil
}

// BindToSandbox binds a secret to a sandbox as an environment variable
func (s *SecretStore) BindToSandbox(sandboxName, secretName, envVar string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Verify secret exists
	if _, err := s.loadEntry(secretName); err != nil {
		return err
	}

	binding, err := s.loadBinding(sandboxName)
	if err != nil {
		binding = &SandboxSecretBinding{
			Bindings: make(map[string]string),
		}
	}

	binding.Bindings[secretName] = envVar
	return s.saveBinding(sandboxName, binding)
}

// UnbindFromSandbox removes a secret binding from a sandbox
func (s *SecretStore) UnbindFromSandbox(sandboxName, secretName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, err := s.loadBinding(sandboxName)
	if err != nil {
		return fmt.Errorf("no secret bindings for sandbox %q", sandboxName)
	}

	if _, ok := binding.Bindings[secretName]; !ok {
		return fmt.Errorf("secret %q not bound to sandbox %q", secretName, sandboxName)
	}

	delete(binding.Bindings, secretName)
	return s.saveBinding(sandboxName, binding)
}

// GetSandboxSecrets resolves all secret bindings for a sandbox into env vars
func (s *SecretStore) GetSandboxSecrets(sandboxName string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, err := s.loadBinding(sandboxName)
	if err != nil {
		return nil, nil // No bindings is not an error
	}

	result := make(map[string]string)
	for secretName, envVar := range binding.Bindings {
		entry, err := s.loadEntry(secretName)
		if err != nil {
			return nil, fmt.Errorf("bound secret %q not found: %w", secretName, err)
		}

		ciphertext, err := hex.DecodeString(entry.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("corrupted secret %q: %w", secretName, err)
		}

		nonce, err := hex.DecodeString(entry.Nonce)
		if err != nil {
			return nil, fmt.Errorf("corrupted secret %q nonce: %w", secretName, err)
		}

		plaintext, err := s.decrypt(ciphertext, nonce)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt secret %q: %w", secretName, err)
		}

		result[envVar] = string(plaintext)
	}

	return result, nil
}

// ListBindings returns secret bindings for a sandbox
func (s *SecretStore) ListBindings(sandboxName string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, err := s.loadBinding(sandboxName)
	if err != nil {
		return nil, nil
	}
	return binding.Bindings, nil
}

// entryPath returns the file path for a secret entry
func (s *SecretStore) entryPath(name string) string {
	return filepath.Join(s.storeDir, name+".secret")
}

// bindingPath returns the file path for a sandbox's secret bindings
func (s *SecretStore) bindingPath(sandboxName string) string {
	return filepath.Join(s.storeDir, "bindings", sandboxName+".json")
}

// loadEntry loads a secret entry from disk
func (s *SecretStore) loadEntry(name string) (*SecretEntry, error) {
	path := s.entryPath(name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("secret %q not found", name)
		}
		return nil, fmt.Errorf("failed to read secret: %w", err)
	}

	var entry SecretEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("corrupted secret file: %w", err)
	}
	return &entry, nil
}

// saveEntry writes a secret entry to disk
func (s *SecretStore) saveEntry(entry *SecretEntry) error {
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal secret: %w", err)
	}
	path := s.entryPath(entry.Name)
	return os.WriteFile(path, data, 0600)
}

// loadBinding loads sandbox secret bindings
func (s *SecretStore) loadBinding(sandboxName string) (*SandboxSecretBinding, error) {
	path := s.bindingPath(sandboxName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no bindings found")
		}
		return nil, fmt.Errorf("failed to read bindings: %w", err)
	}

	var binding SandboxSecretBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return nil, fmt.Errorf("corrupted bindings file: %w", err)
	}
	return &binding, nil
}

// saveBinding writes sandbox secret bindings to disk
func (s *SecretStore) saveBinding(sandboxName string, binding *SandboxSecretBinding) error {
	dir := filepath.Dir(s.bindingPath(sandboxName))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create bindings directory: %w", err)
	}

	data, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal bindings: %w", err)
	}
	return os.WriteFile(s.bindingPath(sandboxName), data, 0600)
}
