package models

import (
	"testing"
)

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
