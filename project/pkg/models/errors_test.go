package models

import "testing"

func TestValidationError_AddError(t *testing.T) {
	ve := &ValidationError{}

	if len(ve.Errors) != 0 {
		t.Errorf("expected 0 errors initially, got %d", len(ve.Errors))
	}

	ve.AddError("name", "name is required")

	if len(ve.Errors) != 1 {
		t.Errorf("expected 1 error after AddError, got %d", len(ve.Errors))
	}

	if ve.Errors[0].Field != "name" {
		t.Errorf("expected field 'name', got '%s'", ve.Errors[0].Field)
	}

	if ve.Errors[0].Message != "name is required" {
		t.Errorf("expected message 'name is required', got '%s'", ve.Errors[0].Message)
	}

	ve.AddError("vcpus", "vcpus must be positive")

	if len(ve.Errors) != 2 {
		t.Errorf("expected 2 errors after second AddError, got %d", len(ve.Errors))
	}

	if ve.Errors[1].Field != "vcpus" {
		t.Errorf("expected field 'vcpus', got '%s'", ve.Errors[1].Field)
	}

	if ve.Errors[1].Message != "vcpus must be positive" {
		t.Errorf("expected message 'vcpus must be positive', got '%s'", ve.Errors[1].Message)
	}
}

func TestValidationError_HasErrors(t *testing.T) {
	ve := &ValidationError{}

	if ve.HasErrors() {
		t.Errorf("expected HasErrors() to return false for empty ValidationError")
	}

	ve.AddError("name", "name is required")

	if !ve.HasErrors() {
		t.Errorf("expected HasErrors() to return true after adding an error")
	}

	ve.Errors = []ConfigError{}

	if ve.HasErrors() {
		t.Errorf("expected HasErrors() to return false after clearing errors")
	}
}
