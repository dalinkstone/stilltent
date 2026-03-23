package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/pkg/models"
)

// InspectOutput represents the full detailed view of a sandbox
type InspectOutput struct {
	Name          string              `json:"name"`
	Status        models.VMStatus     `json:"status"`
	Created       string              `json:"created,omitempty"`
	Updated       string              `json:"updated,omitempty"`
	PID           int                 `json:"pid,omitempty"`
	Image         string              `json:"image,omitempty"`
	Config        *InspectConfig      `json:"config"`
	State         *InspectState       `json:"state"`
	Network       *InspectNetwork     `json:"network"`
	Mounts        []models.MountConfig `json:"mounts,omitempty"`
	Env           map[string]string   `json:"env,omitempty"`
	Health        *models.HealthState `json:"health,omitempty"`
	RestartPolicy models.RestartPolicy  `json:"restart_policy,omitempty"`
	RestartCount  int                  `json:"restart_count,omitempty"`
	Hooks         *models.LifecycleHooks  `json:"hooks,omitempty"`
	Resources     *models.ResourceLimits  `json:"resources,omitempty"`
}

// InspectConfig holds the sandbox's resource configuration
type InspectConfig struct {
	VCPUs    int    `json:"vcpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Kernel   string `json:"kernel,omitempty"`
	RootFS   string `json:"rootfs,omitempty"`
}

// InspectState holds runtime state details
type InspectState struct {
	SocketPath string `json:"socket_path,omitempty"`
	RootFSPath string `json:"rootfs_path,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
}

// InspectNetwork holds network configuration and status
type InspectNetwork struct {
	Mode      string              `json:"mode"`
	Bridge    string              `json:"bridge,omitempty"`
	IP        string              `json:"ip,omitempty"`
	TAPDevice string              `json:"tap_device,omitempty"`
	Allow     []string            `json:"allow,omitempty"`
	Deny      []string            `json:"deny,omitempty"`
	Ports     []models.PortForward `json:"ports,omitempty"`
}

func inspectCmd() *cobra.Command {
	var formatFlag string

	cmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Display detailed information about a sandbox",
		Long: `Display detailed configuration and runtime state of a sandbox in JSON format.

Examples:
  tent inspect mybox
  tent inspect mybox --format pretty`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			// Load VM state
			stateMgr, err := newStateManager(baseDir)
			if err != nil {
				return fmt.Errorf("failed to initialize state manager: %w", err)
			}

			vmState, err := stateMgr.GetVM(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			// Load VM config from saved YAML
			vmConfig, _ := loadSavedConfig(baseDir, name)

			// Build inspect output
			output := buildInspectOutput(vmState, vmConfig)

			// Marshal and print
			var data []byte
			if formatFlag == "pretty" {
				data, err = json.MarshalIndent(output, "", "  ")
			} else {
				data, err = json.MarshalIndent(output, "", "  ")
			}
			if err != nil {
				return fmt.Errorf("failed to marshal output: %w", err)
			}

			fmt.Println(string(data))
			return nil
		},
	}

	cmd.Flags().StringVar(&formatFlag, "format", "json", "Output format (json, pretty)")

	return cmd
}

func newStateManager(baseDir string) (stateManagerReader, error) {
	// Import state package
	sm, err := newStateManagerFromDir(baseDir)
	if err != nil {
		return nil, err
	}
	return sm, nil
}

// stateManagerReader is a minimal interface for reading state
type stateManagerReader interface {
	GetVM(name string) (*models.VMState, error)
}

// stateManagerImpl wraps the state file directly for inspect use
type stateManagerImpl struct {
	state map[string]*models.VMState
}

func newStateManagerFromDir(baseDir string) (*stateManagerImpl, error) {
	statePath := filepath.Join(baseDir, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &stateManagerImpl{state: make(map[string]*models.VMState)}, nil
		}
		return nil, err
	}

	var stored struct {
		VMs map[string]*models.VMState `json:"vms"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}

	state := stored.VMs
	if state == nil {
		state = make(map[string]*models.VMState)
	}

	return &stateManagerImpl{state: state}, nil
}

func (s *stateManagerImpl) GetVM(name string) (*models.VMState, error) {
	vm, ok := s.state[name]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return vm, nil
}

func loadSavedConfig(baseDir, name string) (*models.VMConfig, error) {
	configPath := filepath.Join(baseDir, "configs", fmt.Sprintf("%s.yaml", name))
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var cfg models.VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func buildInspectOutput(vmState *models.VMState, vmConfig *models.VMConfig) *InspectOutput {
	output := &InspectOutput{
		Name:          vmState.Name,
		Status:        vmState.Status,
		PID:           vmState.PID,
		Image:         vmState.ImageRef,
		Health:        vmState.Health,
		RestartPolicy: vmState.RestartPolicy,
		RestartCount:  vmState.RestartCount,
		Config: &InspectConfig{
			VCPUs:    vmState.VCPUs,
			MemoryMB: vmState.MemoryMB,
			DiskGB:   vmState.DiskGB,
		},
		State: &InspectState{
			SocketPath: vmState.SocketPath,
			RootFSPath: vmState.RootFSPath,
			SSHKeyPath: vmState.SSHKeyPath,
		},
		Network: &InspectNetwork{
			IP:        vmState.IP,
			TAPDevice: vmState.TAPDevice,
		},
	}

	// Format timestamps
	if vmState.CreatedAt > 0 {
		output.Created = time.Unix(vmState.CreatedAt, 0).UTC().Format(time.RFC3339)
	}
	if vmState.UpdatedAt > 0 {
		output.Updated = time.Unix(vmState.UpdatedAt, 0).UTC().Format(time.RFC3339)
	}

	// Enrich from saved config if available
	if vmConfig != nil {
		output.Config.Kernel = vmConfig.Kernel
		output.Config.RootFS = vmConfig.RootFS
		output.Mounts = vmConfig.Mounts
		// Redact env var values — they may contain API keys and secrets.
		// Show keys only; use `tent env list <name>` to see values.
		if len(vmConfig.Env) > 0 {
			redacted := make(map[string]string, len(vmConfig.Env))
			for k := range vmConfig.Env {
				redacted[k] = "***"
			}
			output.Env = redacted
		}
		output.Network.Mode = vmConfig.Network.Mode
		output.Network.Bridge = vmConfig.Network.Bridge
		output.Network.Allow = vmConfig.Network.Allow
		output.Network.Deny = vmConfig.Network.Deny
		output.Network.Ports = vmConfig.Network.Ports

		if vmConfig.Hooks != nil {
			output.Hooks = vmConfig.Hooks
		}
		if vmConfig.RestartPolicy != "" {
			output.RestartPolicy = vmConfig.RestartPolicy
		}
		if vmConfig.Resources != nil {
			output.Resources = vmConfig.Resources
		}
		if vmConfig.HealthCheck != nil && output.Health == nil {
			output.Health = &models.HealthState{
				Status: "unknown",
			}
		}
	}

	return output
}
