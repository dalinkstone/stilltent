package vm

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// CheckpointManager handles full VM state checkpoints (memory + CPU registers + disk).
// Unlike snapshots which only capture disk state, checkpoints capture the entire
// VM execution context so it can be resumed exactly where it left off.
type CheckpointManager struct {
	baseDir string
}

// NewCheckpointManager creates a new checkpoint manager.
func NewCheckpointManager(baseDir string) *CheckpointManager {
	return &CheckpointManager{baseDir: baseDir}
}

// checkpointDir returns the path to the checkpoint directory for a VM.
func (cm *CheckpointManager) checkpointDir(vmName string) string {
	return filepath.Join(cm.baseDir, "vms", vmName, "checkpoints")
}

// checkpointPath returns the path for a specific checkpoint tag.
func (cm *CheckpointManager) checkpointPath(vmName, tag string) string {
	return filepath.Join(cm.checkpointDir(vmName), tag)
}

// checkpointMetadata holds internal checkpoint state persisted to disk.
type checkpointMetadata struct {
	Tag          string            `json:"tag"`
	Timestamp    string            `json:"timestamp"`
	MemoryMB     int               `json:"memory_mb"`
	VCPUs        int               `json:"vcpus"`
	VMStatus     string            `json:"vm_status"`
	DiskIncluded bool              `json:"disk_included"`
	Description  string            `json:"description,omitempty"`
	VMConfig     *models.VMConfig  `json:"vm_config,omitempty"`
	Checksum     string            `json:"checksum,omitempty"`
}

// CreateCheckpoint saves a full VM checkpoint including simulated memory state,
// CPU register state, and optionally the disk image.
func (cm *CheckpointManager) CreateCheckpoint(vmName string, tag string, description string, includeDisk bool, vmState *models.VMState, vmConfig *models.VMConfig) (*models.CheckpointInfo, error) {
	cpDir := cm.checkpointPath(vmName, tag)

	// Check if checkpoint already exists
	if _, err := os.Stat(cpDir); err == nil {
		return nil, fmt.Errorf("checkpoint %q already exists for VM %q", tag, vmName)
	}

	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	var totalSize int64

	// Save memory state dump (simulated — in a real hypervisor this would be the
	// guest physical memory pages dumped from the hypervisor backend)
	memState := buildMemoryState(vmState, vmConfig)
	memPath := filepath.Join(cpDir, "memory.bin")
	memSize, err := writeJSONFile(memPath, memState)
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return nil, fmt.Errorf("failed to save memory state: %w", err)
	}
	totalSize += memSize

	// Save CPU register state
	cpuState := buildCPUState(vmConfig)
	cpuPath := filepath.Join(cpDir, "cpu.bin")
	cpuSize, err := writeJSONFile(cpuPath, cpuState)
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return nil, fmt.Errorf("failed to save CPU state: %w", err)
	}
	totalSize += cpuSize

	// Save device state (virtio queues, network, console)
	devState := buildDeviceState(vmState)
	devPath := filepath.Join(cpDir, "devices.bin")
	devSize, err := writeJSONFile(devPath, devState)
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return nil, fmt.Errorf("failed to save device state: %w", err)
	}
	totalSize += devSize

	// Optionally copy the disk image
	if includeDisk && vmState.RootFSPath != "" {
		diskDst := filepath.Join(cpDir, "rootfs.img")
		diskSize, err := copyFile(vmState.RootFSPath, diskDst)
		if err != nil {
			_ = os.RemoveAll(cpDir)
			return nil, fmt.Errorf("failed to copy disk image: %w", err)
		}
		totalSize += diskSize
	}

	// Compute checksum of memory state for integrity verification
	checksum, err := fileChecksum(memPath)
	if err != nil {
		checksum = ""
	}

	// Write metadata
	meta := checkpointMetadata{
		Tag:          tag,
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		MemoryMB:     vmConfig.MemoryMB,
		VCPUs:        vmConfig.VCPUs,
		VMStatus:     string(vmState.Status),
		DiskIncluded: includeDisk,
		Description:  description,
		VMConfig:     vmConfig,
		Checksum:     checksum,
	}

	metaPath := filepath.Join(cpDir, "metadata.json")
	metaSize, err := writeJSONFile(metaPath, meta)
	if err != nil {
		_ = os.RemoveAll(cpDir)
		return nil, fmt.Errorf("failed to save checkpoint metadata: %w", err)
	}
	totalSize += metaSize

	return &models.CheckpointInfo{
		Tag:          tag,
		Timestamp:    meta.Timestamp,
		SizeMB:       totalSize / (1024 * 1024),
		MemoryMB:     vmConfig.MemoryMB,
		VCPUs:        vmConfig.VCPUs,
		VMStatus:     string(vmState.Status),
		DiskIncluded: includeDisk,
		Description:  description,
	}, nil
}

// RestoreCheckpoint loads checkpoint metadata and verifies integrity.
// Returns the metadata needed for the hypervisor backend to restore VM state.
func (cm *CheckpointManager) RestoreCheckpoint(vmName, tag string) (*checkpointMetadata, error) {
	cpDir := cm.checkpointPath(vmName, tag)
	metaPath := filepath.Join(cpDir, "metadata.json")

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("checkpoint %q not found for VM %q", tag, vmName)
		}
		return nil, fmt.Errorf("failed to read checkpoint metadata: %w", err)
	}

	var meta checkpointMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse checkpoint metadata: %w", err)
	}

	// Verify memory state integrity
	if meta.Checksum != "" {
		memPath := filepath.Join(cpDir, "memory.bin")
		currentChecksum, err := fileChecksum(memPath)
		if err != nil {
			return nil, fmt.Errorf("failed to verify memory state: %w", err)
		}
		if currentChecksum != meta.Checksum {
			return nil, fmt.Errorf("checkpoint integrity check failed: memory state corrupted")
		}
	}

	// Verify all required files exist
	requiredFiles := []string{"memory.bin", "cpu.bin", "devices.bin"}
	for _, f := range requiredFiles {
		path := filepath.Join(cpDir, f)
		if _, err := os.Stat(path); err != nil {
			return nil, fmt.Errorf("checkpoint file %q missing: %w", f, err)
		}
	}

	if meta.DiskIncluded {
		diskPath := filepath.Join(cpDir, "rootfs.img")
		if _, err := os.Stat(diskPath); err != nil {
			return nil, fmt.Errorf("checkpoint disk image missing: %w", err)
		}
	}

	return &meta, nil
}

// ListCheckpoints returns all checkpoints for a VM sorted by timestamp.
func (cm *CheckpointManager) ListCheckpoints(vmName string) ([]*models.CheckpointInfo, error) {
	cpDir := cm.checkpointDir(vmName)

	entries, err := os.ReadDir(cpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read checkpoint directory: %w", err)
	}

	var checkpoints []*models.CheckpointInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		metaPath := filepath.Join(cpDir, entry.Name(), "metadata.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}

		var meta checkpointMetadata
		if err := json.Unmarshal(data, &meta); err != nil {
			continue
		}

		// Calculate total size
		var totalSize int64
		cpPath := filepath.Join(cpDir, entry.Name())
		files, _ := os.ReadDir(cpPath)
		for _, f := range files {
			info, err := f.Info()
			if err == nil {
				totalSize += info.Size()
			}
		}

		checkpoints = append(checkpoints, &models.CheckpointInfo{
			Tag:          meta.Tag,
			Timestamp:    meta.Timestamp,
			SizeMB:       totalSize / (1024 * 1024),
			MemoryMB:     meta.MemoryMB,
			VCPUs:        meta.VCPUs,
			VMStatus:     meta.VMStatus,
			DiskIncluded: meta.DiskIncluded,
			Description:  meta.Description,
		})
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].Timestamp < checkpoints[j].Timestamp
	})

	return checkpoints, nil
}

// DeleteCheckpoint removes a specific checkpoint.
func (cm *CheckpointManager) DeleteCheckpoint(vmName, tag string) error {
	cpDir := cm.checkpointPath(vmName, tag)

	if _, err := os.Stat(cpDir); os.IsNotExist(err) {
		return fmt.Errorf("checkpoint %q not found for VM %q", tag, vmName)
	}

	if err := os.RemoveAll(cpDir); err != nil {
		return fmt.Errorf("failed to delete checkpoint: %w", err)
	}

	return nil
}

// DeleteAllCheckpoints removes all checkpoints for a VM.
func (cm *CheckpointManager) DeleteAllCheckpoints(vmName string) (int, error) {
	cpDir := cm.checkpointDir(vmName)

	entries, err := os.ReadDir(cpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("failed to read checkpoint directory: %w", err)
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(cpDir, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return count, fmt.Errorf("failed to delete checkpoint %q: %w", entry.Name(), err)
		}
		count++
	}

	return count, nil
}

// GetCheckpointDiskPath returns the path to the disk image in a checkpoint, if it exists.
func (cm *CheckpointManager) GetCheckpointDiskPath(vmName, tag string) (string, error) {
	diskPath := filepath.Join(cm.checkpointPath(vmName, tag), "rootfs.img")
	if _, err := os.Stat(diskPath); err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("no disk image in checkpoint %q", tag)
		}
		return "", err
	}
	return diskPath, nil
}

// memoryState represents the serialized guest memory state.
type memoryState struct {
	TotalMB    int               `json:"total_mb"`
	PageSizeKB int               `json:"page_size_kb"`
	Regions    []memoryRegion    `json:"regions"`
	Timestamp  string            `json:"timestamp"`
}

type memoryRegion struct {
	StartAddr uint64 `json:"start_addr"`
	SizeBytes uint64 `json:"size_bytes"`
	Type      string `json:"type"` // "ram", "mmio", "reserved"
}

// cpuState represents serialized CPU register state for all vCPUs.
type cpuState struct {
	NumCPUs    int        `json:"num_cpus"`
	Registers  []vcpuRegs `json:"registers"`
	Timestamp  string     `json:"timestamp"`
}

type vcpuRegs struct {
	VCPUID  int    `json:"vcpu_id"`
	RAX     uint64 `json:"rax"`
	RBX     uint64 `json:"rbx"`
	RCX     uint64 `json:"rcx"`
	RDX     uint64 `json:"rdx"`
	RSI     uint64 `json:"rsi"`
	RDI     uint64 `json:"rdi"`
	RSP     uint64 `json:"rsp"`
	RBP     uint64 `json:"rbp"`
	RIP     uint64 `json:"rip"`
	RFLAGS  uint64 `json:"rflags"`
	CR0     uint64 `json:"cr0"`
	CR3     uint64 `json:"cr3"`
	CR4     uint64 `json:"cr4"`
}

// deviceState represents serialized virtio device state.
type deviceState struct {
	VirtioBlk    []virtioDevState `json:"virtio_blk"`
	VirtioNet    []virtioDevState `json:"virtio_net"`
	VirtioConsole []virtioDevState `json:"virtio_console"`
	Timestamp    string           `json:"timestamp"`
}

type virtioDevState struct {
	ID           string `json:"id"`
	QueueSize    int    `json:"queue_size"`
	QueueReady   bool   `json:"queue_ready"`
	InterruptNum int    `json:"interrupt_num"`
}

func buildMemoryState(vmState *models.VMState, vmConfig *models.VMConfig) *memoryState {
	memMB := vmConfig.MemoryMB
	if memMB == 0 {
		memMB = vmState.MemoryMB
	}

	return &memoryState{
		TotalMB:    memMB,
		PageSizeKB: 4,
		Regions: []memoryRegion{
			{StartAddr: 0, SizeBytes: uint64(memMB) * 1024 * 1024, Type: "ram"},
			{StartAddr: 0xFEC00000, SizeBytes: 0x1000, Type: "mmio"},
			{StartAddr: 0xFEE00000, SizeBytes: 0x1000, Type: "mmio"},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func buildCPUState(vmConfig *models.VMConfig) *cpuState {
	numCPUs := vmConfig.VCPUs
	if numCPUs == 0 {
		numCPUs = 1
	}

	regs := make([]vcpuRegs, numCPUs)
	for i := 0; i < numCPUs; i++ {
		regs[i] = vcpuRegs{
			VCPUID: i,
			CR0:    0x80050033, // PE | MP | ET | NE | WP | PG
			CR4:    0x000006F0, // PAE | MCE | PGE | OSFXSR | OSXMMEXCPT
		}
	}

	return &cpuState{
		NumCPUs:   numCPUs,
		Registers: regs,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func buildDeviceState(vmState *models.VMState) *deviceState {
	return &deviceState{
		VirtioBlk: []virtioDevState{
			{ID: "blk0", QueueSize: 256, QueueReady: true, InterruptNum: 1},
		},
		VirtioNet: []virtioDevState{
			{ID: "net0", QueueSize: 256, QueueReady: vmState.IP != "", InterruptNum: 2},
		},
		VirtioConsole: []virtioDevState{
			{ID: "console0", QueueSize: 64, QueueReady: true, InterruptNum: 3},
		},
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}
}

func writeJSONFile(path string, v interface{}) (int64, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return 0, err
	}
	return int64(len(data)), nil
}

func copyFile(src, dst string) (int64, error) {
	srcFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	defer dstFile.Close()

	return io.Copy(dstFile, srcFile)
}

func fileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
