package vm

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

// SSHKeyPair holds paths to a generated SSH key pair for a sandbox.
type SSHKeyPair struct {
	PrivateKeyPath string
	PublicKeyPath  string
}

// GenerateSSHKeys creates an ed25519 keypair for a sandbox and writes
// the private key (PEM) and public key (authorized_keys format) to disk
// under <baseDir>/keys/<name>/.
func GenerateSSHKeys(baseDir, name string) (*SSHKeyPair, error) {
	keyDir := filepath.Join(baseDir, "keys", name)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return nil, fmt.Errorf("create key directory: %w", err)
	}

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ed25519 key: %w", err)
	}

	// Marshal private key to OpenSSH PEM format
	privPEM, err := ssh.MarshalPrivateKey(privKey, "")
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}

	privPath := filepath.Join(keyDir, "id_ed25519")
	if err := os.WriteFile(privPath, pem.EncodeToMemory(privPEM), 0600); err != nil {
		return nil, fmt.Errorf("write private key: %w", err)
	}

	// Marshal public key to authorized_keys format
	sshPub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		return nil, fmt.Errorf("convert to ssh public key: %w", err)
	}

	pubBytes := ssh.MarshalAuthorizedKey(sshPub)
	pubPath := filepath.Join(keyDir, "id_ed25519.pub")
	if err := os.WriteFile(pubPath, pubBytes, 0644); err != nil {
		return nil, fmt.Errorf("write public key: %w", err)
	}

	return &SSHKeyPair{
		PrivateKeyPath: privPath,
		PublicKeyPath:  pubPath,
	}, nil
}

// SSHKeyDir returns the key directory for a sandbox.
func SSHKeyDir(baseDir, name string) string {
	return filepath.Join(baseDir, "keys", name)
}

// SSHPrivateKeyPath returns the private key path for a sandbox.
func SSHPrivateKeyPath(baseDir, name string) string {
	return filepath.Join(baseDir, "keys", name, "id_ed25519")
}

// RemoveSSHKeys deletes the keypair for a sandbox.
func RemoveSSHKeys(baseDir, name string) error {
	keyDir := SSHKeyDir(baseDir, name)
	if err := os.RemoveAll(keyDir); err != nil {
		return fmt.Errorf("remove SSH keys: %w", err)
	}
	return nil
}
