package main

import (
	"github.com/dalinkstone/tent/internal/sandbox"
)

// CommonCmdOptions holds optional dependencies for CLI commands
type CommonCmdOptions struct {
	StateManager  vm.StateManager
	Hypervisor    vm.HypervisorBackend
	NetworkMgr    vm.NetworkManager
	StorageMgr    vm.StorageManager
}

// CommonCmdOption is a functional option for ConfigureCmd
type CommonCmdOption func(*CommonCmdOptions)

// WithStateManager sets the state manager dependency
func WithStateManager(sm vm.StateManager) CommonCmdOption {
	return func(opts *CommonCmdOptions) {
		opts.StateManager = sm
	}
}

// WithHypervisor sets the hypervisor backend dependency
func WithHypervisor(hv vm.HypervisorBackend) CommonCmdOption {
	return func(opts *CommonCmdOptions) {
		opts.Hypervisor = hv
	}
}

// WithNetworkMgr sets the network manager dependency
func WithNetworkMgr(nm vm.NetworkManager) CommonCmdOption {
	return func(opts *CommonCmdOptions) {
		opts.NetworkMgr = nm
	}
}

// WithStorageMgr sets the storage manager dependency
func WithStorageMgr(sm vm.StorageManager) CommonCmdOption {
	return func(opts *CommonCmdOptions) {
		opts.StorageMgr = sm
	}
}

// WithVMManager sets the VM manager directly (for testing)
func WithVMManager(mgr *vm.VMManager) CommonCmdOption {
	return func(opts *CommonCmdOptions) {
		// When VMManager is provided, extract its dependencies
		// Note: This requires exporting fields or adding accessors to VMManager
		// For now, we'll skip this - direct manager injection needs API changes
	}
}
