package config

import (
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	
	if cfg.VM.DefaultVCPUs != 2 {
		t.Errorf("Expected default VCPUs 2, got %d", cfg.VM.DefaultVCPUs)
	}
	
	if cfg.VM.DefaultMemoryMB != 1024 {
		t.Errorf("Expected default memory 1024MB, got %d", cfg.VM.DefaultMemoryMB)
	}
	
	if cfg.Storage.DataDir != "~/.tent" {
		t.Errorf("Expected default data dir '~/.tent', got '%s'", cfg.Storage.DataDir)
	}
	
	if cfg.Network.DefaultBridge != "tent0" {
		t.Errorf("Expected default bridge 'tent0', got '%s'", cfg.Network.DefaultBridge)
	}
}

func TestToVMConfig(t *testing.T) {
	cfg := DefaultConfig()
	vmCfg := cfg.ToVMConfig("test-vm")
	
	if vmCfg.Name != "test-vm" {
		t.Errorf("Expected name 'test-vm', got '%s'", vmCfg.Name)
	}
	
	if vmCfg.VCPUs != 2 {
		t.Errorf("Expected VCPUs 2, got %d", vmCfg.VCPUs)
	}
	
	if vmCfg.MemoryMB != 1024 {
		t.Errorf("Expected memory 1024MB, got %d", vmCfg.MemoryMB)
	}
}

func TestLoad(t *testing.T) {
	tmpFile := t.TempDir() + "/config.yaml"
	
	cfg := &Config{
		VM: VMConfig{
			DefaultVCPUs:    4,
			DefaultMemoryMB: 2048,
			DefaultDiskGB:   20,
		},
	}
	
	data, err := cfg.MarshalYAML()
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}
	
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}
	
	loaded, err := Load(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}
	
	if loaded.VM.DefaultVCPUs != 4 {
		t.Errorf("Expected loaded VCPUs 4, got %d", loaded.VM.DefaultVCPUs)
	}
}

// Helper to marshal to YAML for testing
func (c *Config) MarshalYAML() ([]byte, error) {
	return yaml.Marshal(c)
}
