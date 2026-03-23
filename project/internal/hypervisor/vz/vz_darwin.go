//go:build darwin && cgo
// +build darwin,cgo

// Package vz provides a macOS Virtualization.framework backend for tent.
// Virtualization.framework is Apple's high-level VM API that provides native
// virtio device support, shared directories via virtio-fs, and Rosetta
// translation for running x86_64 Linux binaries on Apple Silicon.
//
// Unlike the low-level Hypervisor.framework (HVF), VZ manages the full VM
// lifecycle including virtio device emulation, boot loader selection, and
// guest memory management — making it the preferred backend for production
// macOS deployments.
package vz

/*
#cgo darwin LDFLAGS: -framework Virtualization -framework Foundation
#cgo darwin CFLAGS: -x objective-c

#import <Virtualization/Virtualization.h>
#import <Foundation/Foundation.h>

// vzVMState mirrors VZVirtualMachine.state
typedef enum {
	VZStateUnknown   = 0,
	VZStateStopped   = 1,
	VZStateRunning   = 2,
	VZStatePaused    = 3,
	VZStateError     = 4,
	VZStateStarting  = 5,
	VZStatePausing   = 6,
	VZStateResuming  = 7,
	VZStateStopping  = 8,
} vzVMState;

// vzVMHandle holds references to the ObjC VM objects.
// We keep opaque pointers and interact via helper C functions.
typedef struct {
	void *config;      // VZVirtualMachineConfiguration*
	void *vm;          // VZVirtualMachine*
	void *queue;       // dispatch_queue_t
	int   state;       // last known state
	int   vcpuCount;
	unsigned long long memoryBytes;
	char  lastError[512]; // last error description
} vzVMHandle;

static vzVMHandle* vz_create_config(int vcpus, unsigned long long memoryBytes,
	const char *kernelPath, const char *initrdPath, const char *cmdline,
	const char *diskPath, int diskReadOnly, const char *rootfsDir) {

	@autoreleasepool {
		vzVMHandle *h = (vzVMHandle*)calloc(1, sizeof(vzVMHandle));
		if (!h) return NULL;

		h->vcpuCount = vcpus;
		h->memoryBytes = memoryBytes;

		// Create configuration
		VZVirtualMachineConfiguration *config = [[VZVirtualMachineConfiguration alloc] init];
		config.CPUCount = (NSUInteger)vcpus;
		config.memorySize = memoryBytes;

		// Boot loader — Linux kernel direct boot
		NSString *kPath = [NSString stringWithUTF8String:kernelPath];
		NSURL *kernelURL = [NSURL fileURLWithPath:kPath];

		VZLinuxBootLoader *bootLoader = [[VZLinuxBootLoader alloc] initWithKernelURL:kernelURL];

		if (cmdline && strlen(cmdline) > 0) {
			bootLoader.commandLine = [NSString stringWithUTF8String:cmdline];
		}

		if (initrdPath && strlen(initrdPath) > 0) {
			NSString *iPath = [NSString stringWithUTF8String:initrdPath];
			NSURL *initrdURL = [NSURL fileURLWithPath:iPath];
			bootLoader.initialRamdiskURL = initrdURL;
		}

		config.bootLoader = bootLoader;

		// Platform — generic platform for Linux guests
		VZGenericPlatformConfiguration *platform = [[VZGenericPlatformConfiguration alloc] init];
		config.platform = platform;

		// Serial console — virtio console device
		VZVirtioConsoleDeviceSerialPortConfiguration *consoleConfig =
			[[VZVirtioConsoleDeviceSerialPortConfiguration alloc] init];

		// Use file handle attachment for serial I/O (stdout/stdin)
		NSFileHandle *stdoutHandle = [NSFileHandle fileHandleWithStandardOutput];
		NSFileHandle *stdinHandle  = [NSFileHandle fileHandleWithStandardInput];
		VZFileHandleSerialPortAttachment *serialAttachment =
			[[VZFileHandleSerialPortAttachment alloc]
				initWithFileHandleForReading:stdinHandle
				fileHandleForWriting:stdoutHandle];
		consoleConfig.attachment = serialAttachment;
		config.serialPorts = @[consoleConfig];

		// Entropy device — virtio-rng
		VZVirtioEntropyDeviceConfiguration *entropy =
			[[VZVirtioEntropyDeviceConfiguration alloc] init];
		config.entropyDevices = @[entropy];

		// Memory balloon — virtio-balloon for dynamic memory
		VZVirtioTraditionalMemoryBalloonDeviceConfiguration *balloon =
			[[VZVirtioTraditionalMemoryBalloonDeviceConfiguration alloc] init];
		config.memoryBalloonDevices = @[balloon];

		// Storage — virtio-blk disk attachment
		if (diskPath && strlen(diskPath) > 0) {
			NSString *dPath = [NSString stringWithUTF8String:diskPath];
			NSURL *diskURL = [NSURL fileURLWithPath:dPath];
			NSError *diskErr = nil;
			VZDiskImageStorageDeviceAttachment *diskAttachment =
				[VZDiskImageStorageDeviceAttachment alloc];
			diskAttachment = [diskAttachment initWithURL:diskURL
				readOnly:(diskReadOnly != 0) error:&diskErr];
			if (diskAttachment) {
				VZVirtioBlockDeviceConfiguration *blockDev =
					[[VZVirtioBlockDeviceConfiguration alloc]
						initWithAttachment:diskAttachment];
				config.storageDevices = @[blockDev];
			}
		}

		// Shared rootfs directory via virtio-fs (macOS 12+)
		// This exposes the extracted OCI layer contents to the guest as a
		// virtiofs mount tagged "rootfs". The kernel cmdline uses
		// root=rootfs rootfstype=virtiofs to mount it as the root filesystem.
		if (rootfsDir && strlen(rootfsDir) > 0) {
			NSString *rPath = [NSString stringWithUTF8String:rootfsDir];
			NSURL *rootfsURL = [NSURL fileURLWithPath:rPath isDirectory:YES];
			VZSharedDirectory *sharedDir =
				[[VZSharedDirectory alloc] initWithURL:rootfsURL readOnly:NO];
			VZSingleDirectoryShare *share =
				[[VZSingleDirectoryShare alloc] initWithDirectory:sharedDir];
			VZVirtioFileSystemDeviceConfiguration *fsConfig =
				[[VZVirtioFileSystemDeviceConfiguration alloc] initWithTag:@"rootfs"];
			fsConfig.share = share;
			config.directorySharingDevices = @[fsConfig];
		}

		// Network — virtio-net with NAT
		VZVirtioNetworkDeviceConfiguration *netConfig =
			[[VZVirtioNetworkDeviceConfiguration alloc] init];
		VZNATNetworkDeviceAttachment *natAttachment =
			[[VZNATNetworkDeviceAttachment alloc] init];
		netConfig.attachment = natAttachment;
		config.networkDevices = @[netConfig];

		// Validate configuration
		NSError *validationErr = nil;
		BOOL valid = [config validateWithError:&validationErr];
		if (!valid) {
			if (validationErr) {
				strncpy(h->lastError,
					[[validationErr localizedDescription] UTF8String],
					sizeof(h->lastError) - 1);
			}
			free(h);
			return NULL;
		}

		h->config = (__bridge_retained void*)config;

		// Create dispatch queue for VM operations
		dispatch_queue_t queue = dispatch_queue_create("com.tent.vz.vm", DISPATCH_QUEUE_SERIAL);
		h->queue = (__bridge_retained void*)queue;

		// Create the virtual machine on the serial queue
		__block VZVirtualMachine *vm = nil;
		dispatch_sync(queue, ^{
			vm = [[VZVirtualMachine alloc]
				initWithConfiguration:config queue:queue];
		});

		h->vm = (__bridge_retained void*)vm;
		h->state = VZStateStopped;

		return h;
	}
}

static int vz_start(vzVMHandle *h) {
	if (!h || !h->vm) return -1;

	@autoreleasepool {
		VZVirtualMachine *vm = (__bridge VZVirtualMachine*)h->vm;
		dispatch_queue_t queue = (__bridge dispatch_queue_t)h->queue;

		__block int result = 0;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);

		dispatch_async(queue, ^{
			[vm startWithCompletionHandler:^(NSError *err) {
				if (err) {
					result = -1;
					const char *desc = [[err localizedDescription] UTF8String];
					if (desc) {
						strncpy(h->lastError, desc, sizeof(h->lastError) - 1);
						h->lastError[sizeof(h->lastError) - 1] = '\0';
					}
				} else {
					result = 0;
				}
				dispatch_semaphore_signal(sem);
			}];
		});

		// Wait up to 30 seconds for start
		dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, 30LL * NSEC_PER_SEC);
		if (dispatch_semaphore_wait(sem, timeout) != 0) {
			return -2; // timeout
		}

		if (result == 0) {
			h->state = VZStateRunning;
		}
		return result;
	}
}

static int vz_pause(vzVMHandle *h) {
	if (!h || !h->vm) return -1;

	@autoreleasepool {
		VZVirtualMachine *vm = (__bridge VZVirtualMachine*)h->vm;
		dispatch_queue_t queue = (__bridge dispatch_queue_t)h->queue;

		__block int result = 0;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);

		dispatch_async(queue, ^{
			[vm pauseWithCompletionHandler:^(NSError *err) {
				if (err) {
					result = -1;
				}
				dispatch_semaphore_signal(sem);
			}];
		});

		dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, 10LL * NSEC_PER_SEC);
		if (dispatch_semaphore_wait(sem, timeout) != 0) {
			return -2;
		}

		if (result == 0) {
			h->state = VZStatePaused;
		}
		return result;
	}
}

static int vz_resume(vzVMHandle *h) {
	if (!h || !h->vm) return -1;

	@autoreleasepool {
		VZVirtualMachine *vm = (__bridge VZVirtualMachine*)h->vm;
		dispatch_queue_t queue = (__bridge dispatch_queue_t)h->queue;

		__block int result = 0;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);

		dispatch_async(queue, ^{
			[vm resumeWithCompletionHandler:^(NSError *err) {
				if (err) {
					result = -1;
				}
				dispatch_semaphore_signal(sem);
			}];
		});

		dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, 10LL * NSEC_PER_SEC);
		if (dispatch_semaphore_wait(sem, timeout) != 0) {
			return -2;
		}

		if (result == 0) {
			h->state = VZStateRunning;
		}
		return result;
	}
}

static int vz_stop(vzVMHandle *h) {
	if (!h || !h->vm) return -1;

	@autoreleasepool {
		VZVirtualMachine *vm = (__bridge VZVirtualMachine*)h->vm;
		dispatch_queue_t queue = (__bridge dispatch_queue_t)h->queue;

		__block int result = 0;
		dispatch_semaphore_t sem = dispatch_semaphore_create(0);

		dispatch_async(queue, ^{
			NSError *err = nil;
			BOOL ok = [vm requestStopWithError:&err];
			if (!ok) {
				result = -1;
			}
			dispatch_semaphore_signal(sem);
		});

		dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, 10LL * NSEC_PER_SEC);
		if (dispatch_semaphore_wait(sem, timeout) != 0) {
			return -2;
		}

		if (result == 0) {
			h->state = VZStateStopped;
		}
		return result;
	}
}

static int vz_get_state(vzVMHandle *h) {
	if (!h || !h->vm) return VZStateUnknown;

	@autoreleasepool {
		VZVirtualMachine *vm = (__bridge VZVirtualMachine*)h->vm;
		__block int state = VZStateUnknown;
		dispatch_queue_t queue = (__bridge dispatch_queue_t)h->queue;
		dispatch_sync(queue, ^{
			state = (int)vm.state;
		});
		h->state = state;
		return state;
	}
}

static const char* vz_last_error(vzVMHandle *h) {
	if (!h || h->lastError[0] == '\0') return NULL;
	return h->lastError;
}

static void vz_destroy(vzVMHandle *h) {
	if (!h) return;

	@autoreleasepool {
		if (h->vm) {
			VZVirtualMachine *vm = (__bridge_transfer VZVirtualMachine*)h->vm;
			(void)vm; // release
		}
		if (h->config) {
			VZVirtualMachineConfiguration *config = (__bridge_transfer VZVirtualMachineConfiguration*)h->config;
			(void)config; // release
		}
		if (h->queue) {
			dispatch_queue_t queue = (__bridge_transfer dispatch_queue_t)h->queue;
			(void)queue; // release
		}
		free(h);
	}
}
*/
import "C"
import (
	"fmt"
	"io"
	"os"
	"sync"
	"unsafe"

	"github.com/dalinkstone/tent/internal/hypervisor"
	"github.com/dalinkstone/tent/pkg/models"
)

// Backend implements hypervisor.Backend using Apple's Virtualization.framework.
// VZ provides higher-level VM management with native virtio device support,
// VZLinuxBootLoader for direct kernel boot, and NAT networking.
type Backend struct {
	baseDir string
	vms     map[string]*VM
	mu      sync.Mutex
}

// VM represents a Virtualization.framework virtual machine instance.
type VM struct {
	config        *models.VMConfig
	backend       *Backend
	handle        *C.vzVMHandle
	ip            string
	tapDevice     string
	consoleOutput io.Writer
	mounts        []hypervisor.MountTag
	mu            sync.Mutex
}

// NewBackend creates a new Virtualization.framework backend.
func NewBackend(baseDir string) (*Backend, error) {
	return &Backend{
		baseDir: baseDir,
		vms:     make(map[string]*VM),
	}, nil
}

// CreateVM allocates a new VZ virtual machine with the given configuration.
// The VM is created in a stopped state — call Start() to boot it.
func (b *Backend) CreateVM(config *models.VMConfig) (hypervisor.VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.vms[config.Name]; exists {
		return nil, fmt.Errorf("VM %s already exists", config.Name)
	}

	vm := &VM{
		config:  config,
		backend: b,
	}

	b.vms[config.Name] = vm
	return vm, nil
}

// ListVMs returns all VMs managed by this backend.
func (b *Backend) ListVMs() ([]hypervisor.VM, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	vms := make([]hypervisor.VM, 0, len(b.vms))
	for _, vm := range b.vms {
		vms = append(vms, vm)
	}
	return vms, nil
}

// DestroyVM releases all resources for the given VM.
func (b *Backend) DestroyVM(vm hypervisor.VM) error {
	vzVM, ok := vm.(*VM)
	if !ok {
		return fmt.Errorf("invalid VM type for VZ backend")
	}

	if vzVM.handle != nil {
		_ = vzVM.Stop()
	}

	b.mu.Lock()
	delete(b.vms, vzVM.config.Name)
	b.mu.Unlock()

	return nil
}

// Start boots the VM using Virtualization.framework.
// This creates the VZ configuration with a Linux boot loader, virtio devices
// (block, network, console, entropy, balloon), and starts the VM.
func (v *VM) Start() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle != nil {
		return fmt.Errorf("VM %s is already running", v.config.Name)
	}

	// Prepare C strings for configuration
	kernelPath := v.config.Kernel
	if kernelPath == "" {
		return fmt.Errorf("kernel path is required for VZ backend")
	}

	cKernel := C.CString(kernelPath)
	defer C.free(unsafe.Pointer(cKernel))

	var cInitrd *C.char
	if v.config.Initrd != "" {
		cInitrd = C.CString(v.config.Initrd)
		defer C.free(unsafe.Pointer(cInitrd))
	}

	// Determine rootfs directory for virtiofs sharing.
	// The OCI layer contents are preserved as <image>_rootfs alongside the disk image.
	var rootfsDir string
	if v.config.RootFS != "" {
		candidate := v.config.RootFS + "_rootfs"
		// Also check without .img extension
		if _, err := os.Stat(candidate); err == nil {
			rootfsDir = candidate
		}
	}

	// Set kernel cmdline — prefer virtiofs root if rootfs dir exists
	cmdline := v.config.KernelCmdline
	if cmdline == "" {
		if rootfsDir != "" {
			// Boot from virtiofs shared directory
			cmdline = "console=hvc0 root=rootfs rootfstype=virtiofs rw"
		} else {
			// Fall back to block device
			cmdline = "console=hvc0 root=/dev/vda rw"
		}
	}
	cCmdline := C.CString(cmdline)
	defer C.free(unsafe.Pointer(cCmdline))

	var cDisk *C.char
	diskReadOnly := 0
	if v.config.RootFS != "" {
		cDisk = C.CString(v.config.RootFS)
		defer C.free(unsafe.Pointer(cDisk))
	}

	var cRootfsDir *C.char
	if rootfsDir != "" {
		cRootfsDir = C.CString(rootfsDir)
		defer C.free(unsafe.Pointer(cRootfsDir))
	}

	vcpus := v.config.VCPUs
	if vcpus < 1 {
		vcpus = 1
	}
	memBytes := uint64(v.config.MemoryMB) * 1024 * 1024
	if memBytes == 0 {
		memBytes = 512 * 1024 * 1024 // default 512 MB
	}

	handle := C.vz_create_config(
		C.int(vcpus),
		C.ulonglong(memBytes),
		cKernel,
		cInitrd,
		cCmdline,
		cDisk,
		C.int(diskReadOnly),
		cRootfsDir,
	)
	if handle == nil {
		errMsg := "check kernel path and resources"
		// Try to get validation error from a temporary handle
		return fmt.Errorf("failed to create VZ configuration for VM %s — %s", v.config.Name, errMsg)
	}

	ret := C.vz_start(handle)
	if ret != 0 {
		errMsg := ""
		if cErr := C.vz_last_error(handle); cErr != nil {
			errMsg = C.GoString(cErr)
		}
		C.vz_destroy(handle)
		if ret == -2 {
			return fmt.Errorf("VM %s start timed out (30s)", v.config.Name)
		}
		if errMsg != "" {
			return fmt.Errorf("VM %s failed to start: %s", v.config.Name, errMsg)
		}
		return fmt.Errorf("VM %s failed to start (error %d)", v.config.Name, ret)
	}

	v.handle = handle
	return nil
}

// Stop gracefully requests the VM to shut down via ACPI power button.
func (v *VM) Stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle == nil {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}

	ret := C.vz_stop(v.handle)
	if ret != 0 && ret != -2 {
		// Force cleanup even if graceful stop failed
		C.vz_destroy(v.handle)
		v.handle = nil
		return fmt.Errorf("VM %s stop failed (error %d), resources released", v.config.Name, ret)
	}

	C.vz_destroy(v.handle)
	v.handle = nil
	return nil
}

// Pause freezes the VM's execution without releasing resources.
func (v *VM) Pause() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle == nil {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}

	ret := C.vz_pause(v.handle)
	if ret != 0 {
		return fmt.Errorf("VM %s pause failed (error %d)", v.config.Name, ret)
	}
	return nil
}

// Unpause resumes a paused VM.
func (v *VM) Unpause() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle == nil {
		return fmt.Errorf("VM %s is not running", v.config.Name)
	}

	ret := C.vz_resume(v.handle)
	if ret != 0 {
		return fmt.Errorf("VM %s resume failed (error %d)", v.config.Name, ret)
	}
	return nil
}

// Kill forcefully terminates the VM, releasing all resources immediately.
func (v *VM) Kill() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle == nil {
		return nil
	}

	C.vz_destroy(v.handle)
	v.handle = nil
	return nil
}

// Status returns the current VM state by querying Virtualization.framework.
func (v *VM) Status() (models.VMStatus, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.handle == nil {
		return models.VMStatusStopped, nil
	}

	state := C.vz_get_state(v.handle)
	switch state {
	case C.VZStateRunning, C.VZStateStarting:
		return models.VMStatusRunning, nil
	case C.VZStatePaused, C.VZStatePausing:
		return models.VMStatusPaused, nil
	case C.VZStateStopped, C.VZStateStopping:
		return models.VMStatusStopped, nil
	case C.VZStateError:
		return models.VMStatusError, nil
	default:
		return models.VMStatusUnknown, nil
	}
}

// GetConfig returns the VM's configuration.
func (v *VM) GetConfig() *models.VMConfig {
	return v.config
}

// GetIP returns the VM's network IP address.
func (v *VM) GetIP() string {
	return v.ip
}

// SetIP sets the VM's IP address.
func (v *VM) SetIP(ip string) {
	v.ip = ip
}

// SetNetwork configures the VM's network interface identifiers.
func (v *VM) SetNetwork(tapDevice string, ip string) {
	v.tapDevice = tapDevice
	v.ip = ip
}

// GetPID returns 0 since VZ runs in-process (no child process).
func (v *VM) GetPID() int {
	return 0
}

// SetConsoleOutput sets the writer for capturing serial console output.
func (v *VM) SetConsoleOutput(w io.Writer) {
	v.consoleOutput = w
}

// AddMounts registers host directory shares to be exposed to the guest.
// VZ supports virtio-fs for high-performance shared directories.
func (v *VM) AddMounts(mounts []hypervisor.MountTag) {
	v.mounts = append(v.mounts, mounts...)
}

// Cleanup releases all VM resources.
func (v *VM) Cleanup() error {
	return v.Kill()
}
