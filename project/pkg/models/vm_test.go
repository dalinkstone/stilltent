package models

import (
	"testing"
)

func TestVMConfig_Validation(t *testing.T) {
	tests := []struct {
		name     string
		config   *VMConfig
		valid    bool
	}{
		{
			name: "valid config",
			config: &VMConfig{
				Name:     "test-vm",
				VCPUs:    2,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: true,
		},
		{
			name: "invalid name empty",
			config: &VMConfig{
				Name:     "",
				VCPUs:    2,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: false,
		},
		{
			name: "invalid vcpus zero",
			config: &VMConfig{
				Name:     "test-vm",
				VCPUs:    0,
				MemoryMB: 1024,
				DiskGB:   10,
			},
			valid: false,
		},
		{
			name: "invalid memory zero",
			config: &VMConfig{
				Name:     "test-vm",
				VCPUs:    2,
				MemoryMB: 0,
				DiskGB:   10,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateVMConfig(tt.config)
			if tt.valid && err != nil {
				t.Errorf("Expected valid config, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("Expected validation error, got nil")
			}
		})
	}
}

func TestValidateVMConfig(t *testing.T) {
	// Test with nil config
	err := ValidateVMConfig(nil)
	if err == nil {
		t.Error("Expected error for nil config")
	}

	// Test with empty config
	err = ValidateVMConfig(&VMConfig{})
	if err == nil {
		t.Error("Expected error for empty config")
	}
}

func TestVMStatus_String(t *testing.T) {
	tests := []struct {
		status   VMStatus
		expected string
	}{
		{VMStatusCreated, "created"},
		{VMStatusRunning, "running"},
		{VMStatusStopped, "stopped"},
		{VMStatusError, "error"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			if tt.status.String() != tt.expected {
				t.Errorf("Expected '%s', got '%s'", tt.expected, tt.status.String())
			}
		})
	}
}

func TestValidationError_Error(t *testing.T) {
	// Test with no errors
	ve := &ValidationError{Errors: []ConfigError{}}
	if ve.Error() != "validation error" {
		t.Errorf("Expected 'validation error', got '%s'", ve.Error())
	}

	// Test with single error
	ve = &ValidationError{Errors: []ConfigError{
		{Field: "name", Message: "name is required"},
	}}
	if ve.Error() != "name is required" {
		t.Errorf("Expected 'name is required', got '%s'", ve.Error())
	}
}

func TestConfigError_Error(t *testing.T) {
	e := &ConfigError{Field: "name", Message: "name is required"}
	if e.Error() != "name is required" {
		t.Errorf("Expected 'name is required', got '%s'", e.Error())
	}
}
