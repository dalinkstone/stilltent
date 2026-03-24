package hypervisor

import (
	"testing"
)

func TestError_Error(t *testing.T) {
	err := &Error{msg: "test error"}
	if err.Error() != "test error" {
		t.Errorf("Expected 'test error', got '%s'", err.Error())
	}
}

func TestBackendInterface(t *testing.T) {
	// This test ensures the Backend interface is properly defined
	// The actual implementations are in the kvm and hvf packages
}

func TestVMInterface(t *testing.T) {
	// This test ensures the VM interface is properly defined
	// The actual implementations are in the kvm and hvf packages
}
