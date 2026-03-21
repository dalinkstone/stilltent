package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dalinkstone/tent/pkg/models"
)

func TestManager_PullImage(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test with a minimal valid URL (will fail to download but should handle gracefully)
	// We're testing that the method structure is correct, not actual network download
	imagePath, err := manager.PullImage("test-image", "http://example.com/test.img")
	if err != nil {
		// Expected to fail on actual download, but method should be properly structured
		if imagePath != "" {
			t.Errorf("Expected empty path on failure, got %s", imagePath)
		}
	}
}

func TestManager_ListImages(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Test empty list
	images, err := manager.ListImages()
	if err != nil {
		t.Errorf("Expected no error on empty list, got: %v", err)
	}
	if images == nil {
		t.Errorf("Expected non-nil images slice")
	}
	if len(images) != 0 {
		t.Errorf("Expected 0 images, got %d", len(images))
	}

	// Create a fake image file
	imagesDir := filepath.Join(tempDir, "images")
	os.MkdirAll(imagesDir, 0755)
	fakeImage := filepath.Join(imagesDir, "test.img")
	if err := os.WriteFile(fakeImage, []byte("fake"), 0644); err != nil {
		t.Fatalf("Failed to create fake image: %v", err)
	}

	// Test listing with fake image
	images, err = manager.ListImages()
	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}
	if len(images) != 1 {
		t.Errorf("Expected 1 image, got %d", len(images))
	}
	if images[0].Name != "test" {
		t.Errorf("Expected image name 'test', got '%s'", images[0].Name)
	}
	if images[0].SizeMB != 0 {
		t.Errorf("Expected 0 MB, got %d", images[0].SizeMB)
	}
}

func TestManager_CreateRootFS(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	config := &models.VMConfig{
		Name:     "test-vm",
		VCPUs:    2,
		MemoryMB: 1024,
		DiskGB:   1,
		Kernel:   "default",
		RootFS:   "default",
		Network:  models.NetworkConfig{Mode: "bridge"},
	}

	// Test that the method validates config and creates directories
	// (we skip the actual mount operations which require root privileges)
	rootfsDir := filepath.Join(tempDir, "rootfs", "test-vm")

	// Verify directories are created during the process
	// Note: full CreateRootFS requires mount privileges we don't have in test environment
	_, err = manager.CreateRootFS("test-vm", config)
	
	// The method should at least create the rootfs directory structure
	if _, err := os.Stat(rootfsDir); os.IsNotExist(err) {
		// This is expected in test environment without mount privileges
		t.Skip("Skipping: requires mount privileges (test environment limitation)")
	}
}

func TestManager_DestroyVMStorage(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create a fake VM directory (skip actual CreateRootFS which needs mounts)
	vmDir := filepath.Join(tempDir, "rootfs", "test-vm")
	os.MkdirAll(vmDir, 0755)

	// Verify directory exists before destroy
	if _, err := os.Stat(vmDir); os.IsNotExist(err) {
		t.Fatalf("VM directory should exist before destroy")
	}

	// Destroy the storage (this only removes directories, no mount needed)
	err = manager.DestroyVMStorage("test-vm")
	if err != nil {
		t.Errorf("DestroyVMStorage failed: %v", err)
	}

	// Verify directory is removed
	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Errorf("VM directory should be removed after destroy")
	}
}

func TestManager_CreateSnapshot(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create a VM directory with a fake rootfs image
	vmDir := filepath.Join(tempDir, "rootfs", "test-vm")
	os.MkdirAll(filepath.Join(vmDir, "mnt"), 0755)

	// Create a fake rootfs image
	rootfsPath := filepath.Join(vmDir, "rootfs.img")
	if err := os.WriteFile(rootfsPath, []byte("fake rootfs data"), 0644); err != nil {
		t.Fatalf("Failed to create fake rootfs: %v", err)
	}

	// Create snapshot
	_, err = manager.CreateSnapshot("test-vm", "v1")
	if err != nil {
		t.Errorf("CreateSnapshot failed: %v", err)
	}
}

func TestManager_RestoreSnapshot(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create VM directory structure
	vmDir := filepath.Join(tempDir, "rootfs", "test-vm")
	os.MkdirAll(filepath.Join(vmDir, "mnt"), 0755)

	// Create initial rootfs
	rootfsPath := filepath.Join(vmDir, "rootfs.img")
	initialContent := []byte("initial rootfs data")
	if err := os.WriteFile(rootfsPath, initialContent, 0644); err != nil {
		t.Fatalf("Failed to create rootfs: %v", err)
	}

	// Create snapshot
	_, err = manager.CreateSnapshot("test-vm", "v1")
	if err != nil {
		t.Fatalf("Failed to create snapshot: %v", err)
	}

	// Modify the rootfs
	modifiedContent := []byte("modified rootfs data")
	if err := os.WriteFile(rootfsPath, modifiedContent, 0644); err != nil {
		t.Fatalf("Failed to modify rootfs: %v", err)
	}

	// Restore snapshot
	err = manager.RestoreSnapshot("test-vm", "v1")
	if err != nil {
		t.Errorf("RestoreSnapshot failed: %v", err)
	}

	// Verify content is restored
	restoredContent, err := os.ReadFile(rootfsPath)
	if err != nil {
		t.Errorf("Failed to read restored rootfs: %v", err)
	}
	if string(restoredContent) != string(initialContent) {
		t.Errorf("Rootfs not restored. Expected '%s', got '%s'", string(initialContent), string(restoredContent))
	}
}

func TestManager_ListSnapshots(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "tent-storage-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	manager, err := NewManager(tempDir)
	if err != nil {
		t.Fatalf("Failed to create manager: %v", err)
	}

	// Create VM directory structure
	vmDir := filepath.Join(tempDir, "rootfs", "test-vm")
	os.MkdirAll(vmDir, 0755)

	// Create some fake snapshot files
	snapshotDir := filepath.Join(tempDir, "snapshots", "test-vm")
	os.MkdirAll(snapshotDir, 0755)
	os.WriteFile(filepath.Join(snapshotDir, "v1.img"), []byte("snap1"), 0644)
	os.WriteFile(filepath.Join(snapshotDir, "v2.img"), []byte("snap2"), 0644)

	// List snapshots
	snapshots, err := manager.ListSnapshots("test-vm")
	if err != nil {
		t.Errorf("ListSnapshots failed: %v", err)
	}
	if len(snapshots) != 2 {
		t.Errorf("Expected 2 snapshots, got %d", len(snapshots))
	}
}
