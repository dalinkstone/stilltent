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

func TestNewBackend_UnsupportedPlatform(t *testing.T) {
	_, err := NewBackend()
	if err != ErrUnsupportedPlatform {
		t.Errorf("Expected ErrUnsupportedPlatform, got %v", err)
	}
}

func TestBackendOption_WithBaseDir(t *testing.T) {
	opt := WithBaseDir("/test/dir")
	var cfg backendConfig
	err := opt(&cfg)
	if err != nil {
		t.Fatalf("WithBaseDir failed: %v", err)
	}
	if cfg.baseDir != "/test/dir" {
		t.Errorf("Expected baseDir '/test/dir', got '%s'", cfg.baseDir)
	}
}
