package compose

import (
	"os"
	"path/filepath"
	"testing"
)

// TestParseConfigFile tests the ParseConfigFile function
func TestParseConfigFile(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
    vcpus: 2
    memory_mb: 1024
    network:
      allow:
        - api.anthropic.com
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if config == nil {
		t.Fatal("expected config, got nil")
	}

	if len(config.Sandboxes) != 1 {
		t.Errorf("expected 1 sandbox, got %d", len(config.Sandboxes))
	}

	if _, exists := config.Sandboxes["test-vm"]; !exists {
		t.Errorf("expected test-vm in sandboxes")
	}

	if config.Sandboxes["test-vm"].From != "ubuntu:22.04" {
		t.Errorf("expected from ubuntu:22.04, got %s", config.Sandboxes["test-vm"].From)
	}

	if config.Sandboxes["test-vm"].VCPUs != 2 {
		t.Errorf("expected vcpus 2, got %d", config.Sandboxes["test-vm"].VCPUs)
	}

	if config.Sandboxes["test-vm"].MemoryMB != 1024 {
		t.Errorf("expected memory_mb 1024, got %d", config.Sandboxes["test-vm"].MemoryMB)
	}
}

func TestParseConfigFileWithMultipleSandboxes(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  agent:
    from: ubuntu:22.04
    vcpus: 2
    memory_mb: 2048
  tool-runner:
    from: python:3.12-slim
    vcpus: 1
    memory_mb: 512
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if len(config.Sandboxes) != 2 {
		t.Errorf("expected 2 sandboxes, got %d", len(config.Sandboxes))
	}

	if _, exists := config.Sandboxes["agent"]; !exists {
		t.Errorf("expected agent in sandboxes")
	}

	if _, exists := config.Sandboxes["tool-runner"]; !exists {
		t.Errorf("expected tool-runner in sandboxes")
	}
}

func TestParseConfigFileWithEnv(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
    env:
      API_KEY: secret123
      DEBUG: "true"
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if len(config.Sandboxes["test-vm"].Env) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(config.Sandboxes["test-vm"].Env))
	}

	if config.Sandboxes["test-vm"].Env["API_KEY"] != "secret123" {
		t.Errorf("expected API_KEY secret123, got %s", config.Sandboxes["test-vm"].Env["API_KEY"])
	}

	if config.Sandboxes["test-vm"].Env["DEBUG"] != "true" {
		t.Errorf("expected DEBUG true, got %s", config.Sandboxes["test-vm"].Env["DEBUG"])
	}
}

func TestParseConfigFileNotFound(t *testing.T) {
	_, err := ParseConfigFile("/nonexistent/compose.yaml")
	if err == nil {
		t.Error("expected error for nonexistent file")
	}
}

func TestParseConfigFileInvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `invalid yaml: [`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	_, err = ParseConfigFile(composeFile)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestComposeStatus(t *testing.T) {
	status := &ComposeStatus{
		Name: "test-group",
		Sandboxes: map[string]*SandboxStatus{
			"vm1": {
				Name:   "vm1",
				Status: "running",
				IP:     "10.0.0.1",
				PID:    1234,
			},
			"vm2": {
				Name:   "vm2",
				Status: "stopped",
			},
		},
	}

	if status.Name != "test-group" {
		t.Errorf("expected name test-group, got %s", status.Name)
	}

	if len(status.Sandboxes) != 2 {
		t.Errorf("expected 2 sandboxes, got %d", len(status.Sandboxes))
	}

	vm1Status, exists := status.Sandboxes["vm1"]
	if !exists {
		t.Fatal("expected vm1 in sandboxes")
	}

	if vm1Status.Status != "running" {
		t.Errorf("expected status running, got %s", vm1Status.Status)
	}

	if vm1Status.IP != "10.0.0.1" {
		t.Errorf("expected IP 10.0.0.1, got %s", vm1Status.IP)
	}

	if vm1Status.PID != 1234 {
		t.Errorf("expected PID 1234, got %d", vm1Status.PID)
	}
}

func TestSandboxStatus_String(t *testing.T) {
	status := &SandboxStatus{
		Name:   "test-vm",
		Status: "running",
		IP:     "10.0.0.1",
		PID:    1234,
	}

	// Just verify the struct can be created and fields accessed
	if status.Name != "test-vm" {
		t.Errorf("expected name test-vm, got %s", status.Name)
	}
}

func TestSandboxConfig_Validation(t *testing.T) {
	config := &SandboxConfig{
		Name:     "test-vm",
		From:     "ubuntu:22.04",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   10,
		Env: map[string]string{
			"API_KEY": "test-key",
		},
	}

	if config.Name != "test-vm" {
		t.Errorf("expected name test-vm, got %s", config.Name)
	}

	if config.From != "ubuntu:22.04" {
		t.Errorf("expected from ubuntu:22.04, got %s", config.From)
	}
}

func TestNetworkConf(t *testing.T) {
	conf := &NetworkConf{
		Allow: []string{"api.anthropic.com", "openrouter.ai"},
		Deny:  []string{"blocked.example.com"},
	}

	if len(conf.Allow) != 2 {
		t.Errorf("expected 2 allowed endpoints, got %d", len(conf.Allow))
	}

	if len(conf.Deny) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(conf.Deny))
	}
}

func TestMount(t *testing.T) {
	mount := &Mount{
		Host:     "/host/path",
		Guest:    "/guest/path",
		Readonly: false,
	}

	if mount.Host != "/host/path" {
		t.Errorf("expected host /host/path, got %s", mount.Host)
	}

	if mount.Guest != "/guest/path" {
		t.Errorf("expected guest /guest/path, got %s", mount.Guest)
	}

	if mount.Readonly {
		t.Error("expected readonly false, got true")
	}
}

func TestComposeConfig_Validation(t *testing.T) {
	config := &ComposeConfig{
		Sandboxes: map[string]*SandboxConfig{
			"vm1": {
				From:     "ubuntu:22.04",
				VCPUs:    2,
				MemoryMB: 1024,
			},
		},
	}

	if len(config.Sandboxes) != 1 {
		t.Errorf("expected 1 sandbox, got %d", len(config.Sandboxes))
	}

	if _, exists := config.Sandboxes["vm1"]; !exists {
		t.Error("expected vm1 in sandboxes")
	}
}

// TestParseConfigFileEmpty tests parsing an empty compose file
func TestParseConfigFileEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := ``
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	_, err = ParseConfigFile(composeFile)
	if err == nil {
		t.Error("expected error for empty compose file (must have at least one sandbox)")
	}
}

// TestParseConfigFileWithNetwork tests parsing compose file with network config
func TestParseConfigFileWithNetwork(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
    network:
      allow:
        - api.anthropic.com
        - openrouter.ai
      deny:
        - blocked.example.com
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if config.Sandboxes["test-vm"].Network == nil {
		t.Fatal("expected network config")
	}

	if len(config.Sandboxes["test-vm"].Network.Allow) != 2 {
		t.Errorf("expected 2 allowed endpoints, got %d", len(config.Sandboxes["test-vm"].Network.Allow))
	}

	if len(config.Sandboxes["test-vm"].Network.Deny) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(config.Sandboxes["test-vm"].Network.Deny))
	}
}

// TestParseConfigFileWithMounts tests parsing compose file with mounts
func TestParseConfigFileWithMounts(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
    mounts:
      - host: /workspace
        guest: /workspace
        readonly: false
      - host: /data
        guest: /data
        readonly: true
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if len(config.Sandboxes["test-vm"].Mounts) != 2 {
		t.Errorf("expected 2 mounts, got %d", len(config.Sandboxes["test-vm"].Mounts))
	}

	if config.Sandboxes["test-vm"].Mounts[0].Host != "/workspace" {
		t.Errorf("expected host /workspace, got %s", config.Sandboxes["test-vm"].Mounts[0].Host)
	}

	if !config.Sandboxes["test-vm"].Mounts[1].Readonly {
		t.Error("expected readonly true for /data mount")
	}
}

// TestParseConfigFileMinimal tests parsing a minimal compose file
func TestParseConfigFileMinimal(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	// The parser sets defaults: vcpus=2, memory_mb=1024, disk_gb=10 when <= 0
	if config.Sandboxes["test-vm"].VCPUs != 2 {
		t.Errorf("expected default vcpus 2, got %d", config.Sandboxes["test-vm"].VCPUs)
	}

	if config.Sandboxes["test-vm"].MemoryMB != 1024 {
		t.Errorf("expected default memory_mb 1024, got %d", config.Sandboxes["test-vm"].MemoryMB)
	}

	if config.Sandboxes["test-vm"].DiskGB != 10 {
		t.Errorf("expected default disk_gb 10, got %d", config.Sandboxes["test-vm"].DiskGB)
	}
}

// TestSandboxStatusWithEmptyValues tests SandboxStatus with minimal fields
func TestSandboxStatusWithEmptyValues(t *testing.T) {
	status := &SandboxStatus{
		Name:   "test-vm",
		Status: "stopped",
		IP:     "",
		PID:    0,
	}

	if status.Name != "test-vm" {
		t.Errorf("expected name test-vm, got %s", status.Name)
	}

	if status.Status != "stopped" {
		t.Errorf("expected status stopped, got %s", status.Status)
	}
}

// TestComposeConfigWithNilSandboxes tests ComposeConfig with nil sandboxes
func TestComposeConfigWithNilSandboxes(t *testing.T) {
	config := &ComposeConfig{
		Sandboxes: nil,
	}

	if config.Sandboxes != nil {
		t.Error("expected nil sandboxes")
	}
}

// TestSandboxConfigWithEmptyFields tests SandboxConfig with empty optional fields
func TestSandboxConfigWithEmptyFields(t *testing.T) {
	config := &SandboxConfig{
		Name:     "",
		From:     "ubuntu:22.04",
		VCPUs:    0,
		MemoryMB: 0,
		DiskGB:   0,
		Network:  nil,
		Mounts:   nil,
		Env:      nil,
	}

	if config.From != "ubuntu:22.04" {
		t.Errorf("expected from ubuntu:22.04, got %s", config.From)
	}
}

// TestParseConfigFileMultipleNetworks tests parsing compose file with multiple sandboxes having different network configs
func TestParseConfigFileMultipleNetworks(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  agent:
    from: ubuntu:22.04
    network:
      allow:
        - api.anthropic.com
  tool-runner:
    from: python:3.12-slim
    network:
      allow: []
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	if config.Sandboxes["agent"].Network == nil {
		t.Fatal("expected network config for agent")
	}

	if len(config.Sandboxes["agent"].Network.Allow) != 1 {
		t.Errorf("expected 1 allowed endpoint for agent, got %d", len(config.Sandboxes["agent"].Network.Allow))
	}

	if config.Sandboxes["tool-runner"].Network == nil {
		t.Fatal("expected network config for tool-runner")
	}

	if len(config.Sandboxes["tool-runner"].Network.Allow) != 0 {
		t.Errorf("expected 0 allowed endpoints for tool-runner, got %d", len(config.Sandboxes["tool-runner"].Network.Allow))
	}
}

// TestParseConfigFileWithAllFields tests parsing compose file with all optional fields
func TestParseConfigFileWithAllFields(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    name: test-vm
    from: ubuntu:22.04
    vcpus: 4
    memory_mb: 2048
    disk_gb: 20
    network:
      allow:
        - api.anthropic.com
        - openrouter.ai
      deny:
        - blocked.example.com
    mounts:
      - host: /workspace
        guest: /workspace
        readonly: false
      - host: /data
        guest: /data
        readonly: true
    env:
      API_KEY: secret123
      DEBUG: "true"
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	vmConfig := config.Sandboxes["test-vm"]

	if vmConfig.Name != "test-vm" {
		t.Errorf("expected name test-vm, got %s", vmConfig.Name)
	}

	if vmConfig.VCPUs != 4 {
		t.Errorf("expected vcpus 4, got %d", vmConfig.VCPUs)
	}

	if vmConfig.MemoryMB != 2048 {
		t.Errorf("expected memory_mb 2048, got %d", vmConfig.MemoryMB)
	}

	if vmConfig.DiskGB != 20 {
		t.Errorf("expected disk_gb 20, got %d", vmConfig.DiskGB)
	}

	if len(vmConfig.Network.Allow) != 2 {
		t.Errorf("expected 2 allowed endpoints, got %d", len(vmConfig.Network.Allow))
	}

	if len(vmConfig.Network.Deny) != 1 {
		t.Errorf("expected 1 denied endpoint, got %d", len(vmConfig.Network.Deny))
	}

	if len(vmConfig.Mounts) != 2 {
		t.Errorf("expected 2 mounts, got %d", len(vmConfig.Mounts))
	}

	if len(vmConfig.Env) != 2 {
		t.Errorf("expected 2 env vars, got %d", len(vmConfig.Env))
	}
}

// TestParseConfigFileWithZeroValues tests parsing compose file with explicit zero values
func TestParseConfigFileWithZeroValues(t *testing.T) {
	tmpDir := t.TempDir()
	composeFile := filepath.Join(tmpDir, "compose.yaml")

	content := `
sandboxes:
  test-vm:
    from: ubuntu:22.04
    vcpus: 0
    memory_mb: 0
    disk_gb: 0
`
	err := os.WriteFile(composeFile, []byte(content), 0644)
	if err != nil {
		t.Fatalf("failed to write compose file: %v", err)
	}

	config, err := ParseConfigFile(composeFile)
	if err != nil {
		t.Fatalf("ParseConfigFile() returned error: %v", err)
	}

	// The parser sets defaults when values are <= 0
	if config.Sandboxes["test-vm"].VCPUs != 2 {
		t.Errorf("expected default vcpus 2, got %d", config.Sandboxes["test-vm"].VCPUs)
	}

	if config.Sandboxes["test-vm"].MemoryMB != 1024 {
		t.Errorf("expected default memory_mb 1024, got %d", config.Sandboxes["test-vm"].MemoryMB)
	}

	if config.Sandboxes["test-vm"].DiskGB != 10 {
		t.Errorf("expected default disk_gb 10, got %d", config.Sandboxes["test-vm"].DiskGB)
	}
}
