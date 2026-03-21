package models

import "testing"

func TestVMConfig_YAML(t *testing.T) {
	cfg := VMConfig{
		Name:      "test-vm",
		VCPUs:     2,
		MemoryMB:  1024,
		Kernel:    "default",
		RootFS:    "ubuntu-22.04",
		DiskGB:    10,
		Network: NetworkConfig{
			Mode:   "bridge",
			Bridge: "tent0",
			Ports: []PortForward{
				{Host: 8080, Guest: 80},
				{Host: 2222, Guest: 22},
			},
		},
		Mounts: []MountConfig{
			{Host: "./src", Guest: "/workspace", Readonly: false},
		},
		Env: map[string]string{
			"EDITOR": "vim",
			"LANG":   "en_US.UTF-8",
		},
	}

	if cfg.Name != "test-vm" {
		t.Errorf("expected name 'test-vm', got '%s'", cfg.Name)
	}
	if cfg.VCPUs != 2 {
		t.Errorf("expected vcpus 2, got %d", cfg.VCPUs)
	}
	if len(cfg.Network.Ports) != 2 {
		t.Errorf("expected 2 port forwards, got %d", len(cfg.Network.Ports))
	}
}

func TestVMStatus_Values(t *testing.T) {
	statuses := []VMStatus{VMStatusStopped, VMStatusRunning, VMStatusCreated, VMStatusError}
	expected := []string{"stopped", "running", "created", "error"}

	for i, status := range statuses {
		if string(status) != expected[i] {
			t.Errorf("expected status '%s', got '%s'", expected[i], status)
		}
	}
}

func TestSnapshot_Empty(t *testing.T) {
	snap := Snapshot{}
	if snap.Tag != "" {
		t.Errorf("expected empty tag, got '%s'", snap.Tag)
	}
}
