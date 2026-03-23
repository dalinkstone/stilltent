// Package firecracker implements the hypervisor.Backend interface using
// Firecracker as an external VMM process. Firecracker is communicated with
// via its Unix socket API (/PUT, /GET requests over HTTP-over-UDS).
package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/internal/storage"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend for Firecracker on Linux.
type Backend struct {
	baseDir    string
	binaryPath string
	vms        map[string]*VM
	mu         sync.Mutex
}

// VM represents a Firecracker microVM managed by tent.
type VM struct {
	config        *models.VMConfig
	backend       *Backend
	socketPath    string
	process       *exec.Cmd
	pid           int
	running       bool
	paused        bool
	ip            string
	tapDevice     string
	consoleOutput io.Writer
	mounts        []hypervisor.MountTag
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
}

// firecrackerMachineConfig is the JSON body for PUT /machine-config.
type firecrackerMachineConfig struct {
	VCPUCount  int  `json:"vcpu_count"`
	MemSizeMiB int  `json:"mem_size_mib"`
	Smt        bool `json:"smt"`
}

// firecrackerBootSource is the JSON body for PUT /boot-source.
type firecrackerBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	InitrdPath      string `json:"initrd_path,omitempty"`
	BootArgs        string `json:"boot_args"`
}

// firecrackerDrive is the JSON body for PUT /drives/{drive_id}.
type firecrackerDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

// firecrackerNetworkInterface is the JSON body for PUT /network-interfaces/{iface_id}.
type firecrackerNetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	HostDevName string `json:"host_dev_name"`
}

// firecrackerAction is the JSON body for PUT /actions.
type firecrackerAction struct {
	ActionType string `json:"action_type"`
}

// NewBackend creates a new Firecracker backend. It looks for the firecracker
// binary in PATH or at /usr/local/bin/firecracker.
func NewBackend(baseDir string) (*Backend, error) {
	// Check KVM is available (Firecracker requires it)
	if _, err := os.Stat("/dev/kvm"); err != nil {
		return nil, fmt.Errorf("KVM not available (required by Firecracker): %w", err)
	}

	// Find firecracker binary
	binaryPath, err := exec.LookPath("firecracker")
	if err != nil {
		// Try well-known locations
		for _, p := range []string{"/usr/local/bin/firecracker", "/usr/bin/firecracker"} {
			if _, statErr := os.Stat(p); statErr == nil {
				binaryPath = p
				break
			}
		}
		if binaryPath == "" {
			return nil, fmt.Errorf("firecracker binary not found in PATH or /usr/local/bin")
		}
	}

	return &Backend{
		baseDir:    baseDir,
		binaryPath: binaryPath,
		vms:        make(map[string]*VM),
	}, nil
}

// CreateVM sets up a Firecracker microVM but does not start it yet.
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	socketDir := filepath.Join(b.baseDir, "firecracker", config.Name)
	if err := os.MkdirAll(socketDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create socket directory: %w", err)
	}

	socketPath := filepath.Join(socketDir, "api.sock")

	// Clean up stale socket
	_ = os.Remove(socketPath)

	vm := &VM{
		config:     config,
		backend:    b,
		socketPath: socketPath,
	}

	b.vms[config.Name] = vm
	return vm, nil
}

// ListVMs returns all tracked VMs.
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	vms := make([]hypervisor.VM, 0, len(b.vms))
	for _, vm := range b.vms {
		vms = append(vms, vm)
	}
	return vms, nil
}

// DestroyVM stops and removes a VM.
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	fcVM, ok := vm.(*VM)
	if !ok {
		return fmt.Errorf("invalid VM type for Firecracker backend")
	}

	if fcVM.running {
		_ = fcVM.Kill()
	}

	b.mu.Lock()
	delete(b.vms, fcVM.config.Name)
	b.mu.Unlock()

	// Clean up socket directory
	socketDir := filepath.Join(b.baseDir, "firecracker", fcVM.config.Name)
	_ = os.RemoveAll(socketDir)

	return nil
}

// Start launches the Firecracker process, configures the VM via the API, and
// issues an InstanceStart action.
func (v *VM) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.running {
		return fmt.Errorf("VM %s is already running", v.config.Name)
	}

	v.ctx, v.cancel = context.WithCancel(context.Background())

	// Launch firecracker process
	v.process = exec.CommandContext(v.ctx, v.backend.binaryPath,
		"--api-sock", v.socketPath,
		"--id", v.config.Name,
	)

	// Redirect stderr/stdout if console output is set
	if v.consoleOutput != nil {
		v.process.Stdout = v.consoleOutput
		v.process.Stderr = v.consoleOutput
	}

	if err := v.process.Start(); err != nil {
		return fmt.Errorf("failed to start firecracker process: %w", err)
	}
	v.pid = v.process.Process.Pid

	// Wait for the API socket to become available
	if err := v.waitForSocket(5 * time.Second); err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("firecracker API socket not ready: %w", err)
	}

	// Configure machine
	machineConfig := firecrackerMachineConfig{
		VCPUCount:  v.config.VCPUs,
		MemSizeMiB: v.config.MemoryMB,
		Smt:        false,
	}
	if machineConfig.VCPUCount < 1 {
		machineConfig.VCPUCount = 1
	}
	if machineConfig.MemSizeMiB < 128 {
		machineConfig.MemSizeMiB = 128
	}

	if err := v.apiPut("/machine-config", machineConfig); err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to configure machine: %w", err)
	}

	// Resolve kernel and initrd from rootfs
	rootfsPath := filepath.Join(v.backend.baseDir, "rootfs", v.config.Name, "rootfs.img")
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		_ = v.process.Process.Kill()
		return fmt.Errorf("rootfs not found: %s", rootfsPath)
	}

	storageMgr, err := storage.NewManager(v.backend.baseDir)
	if err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to create storage manager: %w", err)
	}

	kernelInfo, err := storageMgr.ExtractKernel(rootfsPath)
	if err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to extract kernel: %w", err)
	}

	// Configure boot source
	bootSource := firecrackerBootSource{
		KernelImagePath: kernelInfo.KernelPath,
		BootArgs:        kernelInfo.Cmdline,
	}
	if kernelInfo.InitrdPath != "" {
		bootSource.InitrdPath = kernelInfo.InitrdPath
	}

	if err := v.apiPut("/boot-source", bootSource); err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to configure boot source: %w", err)
	}

	// Configure root drive
	rootDrive := firecrackerDrive{
		DriveID:      "rootfs",
		PathOnHost:   rootfsPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}

	if err := v.apiPut("/drives/rootfs", rootDrive); err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to configure root drive: %w", err)
	}

	// Configure network interface if TAP device is set
	if v.tapDevice != "" {
		netIface := firecrackerNetworkInterface{
			IfaceID:     "eth0",
			HostDevName: v.tapDevice,
		}

		if err := v.apiPut("/network-interfaces/eth0", netIface); err != nil {
			_ = v.process.Process.Kill()
			return fmt.Errorf("failed to configure network: %w", err)
		}
	}

	// Start the VM
	startAction := firecrackerAction{ActionType: "InstanceStart"}
	if err := v.apiPut("/actions", startAction); err != nil {
		_ = v.process.Process.Kill()
		return fmt.Errorf("failed to start VM instance: %w", err)
	}

	v.running = true

	// Monitor process in background
	go func() {
		_ = v.process.Wait()
		v.mu.Lock()
		v.running = false
		v.mu.Unlock()
	}()

	return nil
}

// Stop gracefully shuts down the Firecracker VM via the SendCtrlAltDel action.
func (v *VM) Stop() error {
	v.mu.Lock()
	if !v.running {
		v.mu.Unlock()
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}
	v.mu.Unlock()

	// Try graceful shutdown via Ctrl+Alt+Del
	shutdownAction := firecrackerAction{ActionType: "SendCtrlAltDel"}
	_ = v.apiPut("/actions", shutdownAction)

	// Give the guest a few seconds to shut down
	done := make(chan struct{})
	go func() {
		for i := 0; i < 30; i++ {
			v.mu.Lock()
			running := v.running
			v.mu.Unlock()
			if !running {
				close(done)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		close(done)
	}()
	<-done

	// Force kill if still running
	v.mu.Lock()
	if v.running && v.process != nil && v.process.Process != nil {
		_ = v.process.Process.Kill()
		v.running = false
	}
	v.mu.Unlock()

	return nil
}

// Pause suspends the Firecracker VM by sending SIGUSR1 (Firecracker's pause signal).
func (v *VM) Pause() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.running {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}
	if v.paused {
		return fmt.Errorf("VM %s is already paused", v.config.Name)
	}

	// Firecracker supports pause via the /vm endpoint
	pauseBody := map[string]string{"state": "Paused"}
	if err := v.apiPatch("/vm", pauseBody); err != nil {
		return fmt.Errorf("failed to pause VM: %w", err)
	}

	v.paused = true
	return nil
}

// Unpause resumes the Firecracker VM by sending SIGUSR2 (Firecracker's resume signal).
func (v *VM) Unpause() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.running {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}
	if !v.paused {
		return fmt.Errorf("VM %s is not paused", v.config.Name)
	}

	// Firecracker supports resume via the /vm endpoint
	resumeBody := map[string]string{"state": "Resumed"}
	if err := v.apiPatch("/vm", resumeBody); err != nil {
		return fmt.Errorf("failed to resume VM: %w", err)
	}

	v.paused = false
	return nil
}

// Kill forcefully terminates the Firecracker process.
func (v *VM) Kill() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.running {
		return nil
	}

	if v.process != nil && v.process.Process != nil {
		_ = v.process.Process.Signal(syscall.SIGKILL)
	}
	if v.cancel != nil {
		v.cancel()
	}

	v.running = false
	v.paused = false
	return nil
}

// Status returns the current VM state.
func (v *VM) Status() (models.VMStatus, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.paused {
		return models.VMStatusPaused, nil
	}
	if v.running {
		return models.VMStatusRunning, nil
	}
	return models.VMStatusStopped, nil
}

// GetConfig returns the VM's configuration.
func (v *VM) GetConfig() *models.VMConfig {
	return v.config
}

// GetIP returns the VM's network IP address.
func (v *VM) GetIP() string {
	return v.ip
}

// SetIP sets the VM's network IP address.
func (v *VM) SetIP(ip string) {
	v.ip = ip
}

// SetNetwork configures the VM's network interface.
func (v *VM) SetNetwork(tapDevice string, ip string) {
	v.tapDevice = tapDevice
	v.ip = ip
}

// GetPID returns the Firecracker process ID.
func (v *VM) GetPID() int {
	return v.pid
}

// SetConsoleOutput sets the writer for capturing console/serial output.
func (v *VM) SetConsoleOutput(w io.Writer) {
	v.consoleOutput = w
}

// AddMounts stores mount tags. Firecracker does not natively support virtio-9p
// or virtiofs, so mounts are tracked for informational purposes and can be
// handled via external tools or custom rootfs setup.
func (v *VM) AddMounts(mounts []hypervisor.MountTag) {
	v.mounts = append(v.mounts, mounts...)
}

// Cleanup releases all VM resources.
func (v *VM) Cleanup() error {
	if v.running {
		_ = v.Kill()
	}

	// Remove API socket
	_ = os.Remove(v.socketPath)

	v.process = nil
	v.ctx = nil
	v.cancel = nil

	return nil
}

// waitForSocket waits until the Firecracker API socket is available.
func (v *VM) waitForSocket(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", v.socketPath, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for socket %s", v.socketPath)
}

// apiPut sends an HTTP PUT request to the Firecracker API over the Unix socket.
func (v *VM) apiPut(path string, body interface{}) error {
	return v.apiRequest(http.MethodPut, path, body)
}

// apiPatch sends an HTTP PATCH request to the Firecracker API over the Unix socket.
func (v *VM) apiPatch(path string, body interface{}) error {
	return v.apiRequest(http.MethodPatch, path, body)
}

// apiRequest sends an HTTP request to the Firecracker API over the Unix socket.
func (v *VM) apiRequest(method, path string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.DialTimeout("unix", v.socketPath, 5*time.Second)
			},
		},
		Timeout: 10 * time.Second,
	}

	url := fmt.Sprintf("http://localhost%s", path)
	req, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	return nil
}
