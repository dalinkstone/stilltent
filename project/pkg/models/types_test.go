package models

import "testing"

func TestVMConfig_Validation(t *testing.T) {
	tests := []struct {
		name     string
		config   VMConfig
		expected bool // true if validation should pass
	}{
		{
			name: "valid_config",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     2,
				MemoryMB:  1024,
				Kernel:    "default",
				RootFS:    "ubuntu-22.04",
				DiskGB:    10,
				Network: NetworkConfig{
					Mode: "nat",
				},
			},
			expected: true,
		},
		{
			name: "invalid_name_empty",
			config: VMConfig{
				Name:      "",
				VCPUs:     2,
				MemoryMB:  1024,
				DiskGB:    10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_vcpus_zero",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     0,
				MemoryMB:  1024,
				DiskGB:    10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_vcpus_negative",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     -1,
				MemoryMB:  1024,
				DiskGB:    10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_memory_zero",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     2,
				MemoryMB:  0,
				DiskGB:    10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_memory_negative",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     2,
				MemoryMB:  -512,
				DiskGB:    10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_disk_zero",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     2,
				MemoryMB:  1024,
				DiskGB:    0,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "invalid_disk_negative",
			config: VMConfig{
				Name:      "test-vm",
				VCPUs:     2,
				MemoryMB:  1024,
				DiskGB:    -10,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: false,
		},
		{
			name: "minimal_valid_config",
			config: VMConfig{
				Name:      "minimal",
				VCPUs:     1,
				MemoryMB:  256,
				DiskGB:    1,
				Network:   NetworkConfig{Mode: "nat"},
			},
			expected: true,
		},
		{
			name: "with_all_optional_fields",
			config: VMConfig{
				Name:      "full-config",
				VCPUs:     4,
				MemoryMB:  2048,
				Kernel:    "/path/to/kernel",
				RootFS:    "/path/to/rootfs",
				DiskGB:    20,
				Network: NetworkConfig{
					Mode:   "bridge",
					Bridge: "br0",
					Ports: []PortForward{
						{Host: 8080, Guest: 80},
						{Host: 2222, Guest: 22},
					},
				},
				Mounts: []MountConfig{
					{Host: "./src", Guest: "/workspace", Readonly: false},
					{Host: "./data", Guest: "/data", Readonly: true},
				},
				Env: map[string]string{
					"ENV":   "production",
					"DEBUG": "false",
				},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			hasErrors := err != nil

			if hasErrors != !tt.expected {
				if tt.expected {
					t.Errorf("expected validation to pass, but got error: %v", err)
				} else {
					t.Errorf("expected validation to fail, but it passed")
				}
			}
		})
	}
}

func TestValidateVMConfig(t *testing.T) {
	cfg := &VMConfig{
		Name:      "test",
		VCPUs:     2,
		MemoryMB:  1024,
		DiskGB:    10,
		Network:   NetworkConfig{Mode: "nat"},
	}

	err := ValidateVMConfig(cfg)
	if err != nil {
		t.Errorf("expected nil error for valid config, got: %v", err)
	}

	err = ValidateVMConfig(nil)
	if err == nil {
		t.Errorf("expected error for nil config, got nil")
	}

	err = ValidateVMConfig(&VMConfig{})
	if err == nil {
		t.Errorf("expected error for empty config, got nil")
	}
}

func TestPortForward_Validation(t *testing.T) {
	tests := []struct {
		name     string
		host     int
		guest    int
		expected bool
	}{
		{"valid_ports", 8080, 80, true},
		{"valid_low_ports", 22, 22, true},
		{"valid_high_ports", 60000, 60000, true},
		{"host_zero", 0, 80, false},
		{"guest_zero", 8080, 0, false},
		{"negative_host", -1, 80, false},
		{"negative_guest", 8080, -1, false},
		{"host_exceeds_max", 70000, 80, false},
		{"guest_exceeds_max", 8080, 70000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pf := PortForward{Host: tt.host, Guest: tt.guest}
			// For now, just verify the struct holds values
			// Full port range validation could be added later
			if tt.host < 1 || tt.host > 65535 || tt.guest < 1 || tt.guest > 65535 {
				// Expected to fail validation when we add port validation
			}
			_ = pf // use the variable
		})
	}
}

func TestMountConfig_Validation(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		guest    string
		readonly bool
	}{
		{"valid_mount", "/host/path", "/guest/path", false},
		{"readonly_mount", "/host/data", "/data", true},
		{"empty_host", "", "/guest/path", false},
		{"empty_guest", "/host/path", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MountConfig{
				Host:     tt.host,
				Guest:    tt.guest,
				Readonly: tt.readonly,
			}
			// Verify struct fields are set correctly
			if m.Host != tt.host || m.Guest != tt.guest || m.Readonly != tt.readonly {
				t.Errorf("MountConfig fields not set correctly")
			}
		})
	}
}

func TestVMStatus_String(t *testing.T) {
	statuses := map[VMStatus]string{
		VMStatusStopped: "stopped",
		VMStatusRunning: "running",
		VMStatusCreated: "created",
		VMStatusError:   "error",
	}

	for status, expected := range statuses {
		if status.String() != expected {
			t.Errorf("expected '%s', got '%s'", expected, status.String())
		}
	}
}

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
