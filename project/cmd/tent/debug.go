package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/network"
	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// DebugBundle contains all diagnostic information for a sandbox
type DebugBundle struct {
	Timestamp   string            `json:"timestamp"`
	Platform    DebugPlatformInfo `json:"platform"`
	Sandbox     DebugSandboxInfo  `json:"sandbox"`
	Config      *models.VMConfig  `json:"config,omitempty"`
	Network     *DebugNetworkInfo `json:"network,omitempty"`
	Events      []vm.Event        `json:"recent_events,omitempty"`
	Logs        string            `json:"logs,omitempty"`
	FileSystem  *DebugFSInfo      `json:"filesystem,omitempty"`
	Diagnostics []DiagnosticCheck `json:"diagnostics"`
}

// DebugPlatformInfo holds host platform details
type DebugPlatformInfo struct {
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	GoVersion  string `json:"go_version"`
	NumCPU     int    `json:"num_cpu"`
	Hypervisor string `json:"hypervisor"`
}

// DebugSandboxInfo holds sandbox state for debugging
type DebugSandboxInfo struct {
	Name      string            `json:"name"`
	Status    models.VMStatus   `json:"status"`
	PID       int               `json:"pid,omitempty"`
	CreatedAt int64             `json:"created_at,omitempty"`
	ImageRef  string            `json:"image_ref,omitempty"`
	VCPUs     int               `json:"vcpus"`
	MemoryMB  int               `json:"memory_mb"`
	DiskGB    int               `json:"disk_gb"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// DebugNetworkInfo holds network diagnostics
type DebugNetworkInfo struct {
	Mode         string `json:"mode"`
	IP           string `json:"ip,omitempty"`
	TAPDevice    string `json:"tap_device,omitempty"`
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	DeniedHosts  []string `json:"denied_hosts,omitempty"`
}

// DebugFSInfo holds filesystem diagnostics
type DebugFSInfo struct {
	RootFSPath   string `json:"rootfs_path"`
	RootFSExists bool   `json:"rootfs_exists"`
	RootFSSizeMB int64  `json:"rootfs_size_mb,omitempty"`
	KernelPath   string `json:"kernel_path,omitempty"`
	KernelExists bool   `json:"kernel_exists"`
	SocketPath   string `json:"socket_path,omitempty"`
	SocketExists bool   `json:"socket_exists"`
	SSHKeyPath   string `json:"ssh_key_path,omitempty"`
	SSHKeyExists bool   `json:"ssh_key_exists"`
}

// DiagnosticCheck represents a single diagnostic check result
type DiagnosticCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "ok", "warn", "error"
	Message string `json:"message"`
}

func debugCmd() *cobra.Command {
	var (
		outputFile  string
		includeLogs bool
		maxEvents   int
	)

	cmd := &cobra.Command{
		Use:   "debug <name>",
		Short: "Collect diagnostic information for a sandbox",
		Long: `Collect comprehensive diagnostic information about a sandbox for
troubleshooting. Outputs a JSON diagnostic bundle to stdout or writes
a .tar.gz archive containing all debug artifacts.

The debug bundle includes:
  - Platform and hypervisor information
  - Sandbox configuration and state
  - Network configuration and policy
  - Recent lifecycle events
  - Console/boot logs (with --logs)
  - Filesystem integrity checks
  - Diagnostic checks and warnings

Examples:
  tent debug mybox
  tent debug mybox --output debug-bundle.tar.gz
  tent debug mybox --logs --events 50`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			manager, err := vm.NewManager(baseDir, nil, nil, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return err
			}

			bundle, err := collectDebugBundle(manager, baseDir, name, includeLogs, maxEvents)
			if err != nil {
				return err
			}

			if outputFile != "" {
				return writeDebugArchive(bundle, name, baseDir, outputFile, includeLogs)
			}

			// Output JSON to stdout
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(bundle)
		},
	}

	cmd.Flags().StringVarP(&outputFile, "output", "o", "", "Write debug bundle to a .tar.gz archive")
	cmd.Flags().BoolVar(&includeLogs, "logs", false, "Include console/boot logs in the bundle")
	cmd.Flags().IntVar(&maxEvents, "events", 20, "Maximum number of recent events to include")

	return cmd
}

func collectDebugBundle(manager *vm.VMManager, baseDir, name string, includeLogs bool, maxEvents int) (*DebugBundle, error) {
	// Get sandbox state
	state, err := manager.Status(name)
	if err != nil {
		return nil, fmt.Errorf("sandbox %q not found: %w", name, err)
	}

	bundle := &DebugBundle{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Platform:  collectDebugPlatformInfo(),
	}

	// Sandbox info
	bundle.Sandbox = DebugSandboxInfo{
		Name:      name,
		Status:    state.Status,
		PID:       state.PID,
		CreatedAt: state.CreatedAt,
		ImageRef:  state.ImageRef,
		VCPUs:     state.VCPUs,
		MemoryMB:  state.MemoryMB,
		DiskGB:    state.DiskGB,
		Labels:    state.Labels,
	}

	// Load config
	bundle.Config = debugLoadConfig(baseDir, name)

	// Network info
	bundle.Network = collectDebugNetworkInfo(baseDir, name, state)

	// Events
	bundle.Events = collectDebugEvents(baseDir, name, maxEvents)

	// Logs
	if includeLogs {
		bundle.Logs = collectDebugLogs(baseDir, name)
	}

	// Filesystem checks
	bundle.FileSystem = debugCheckFilesystem(baseDir, name)

	// Run diagnostics
	bundle.Diagnostics = runDebugDiagnostics(bundle)

	return bundle, nil
}

func collectDebugPlatformInfo() DebugPlatformInfo {
	hvName := "unknown"
	switch runtime.GOOS {
	case "darwin":
		hvName = "Hypervisor.framework (HVF)"
	case "linux":
		hvName = "KVM"
	}

	return DebugPlatformInfo{
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		GoVersion:  runtime.Version(),
		NumCPU:     runtime.NumCPU(),
		Hypervisor: hvName,
	}
}

func debugLoadConfig(baseDir, name string) *models.VMConfig {
	configPath := filepath.Join(baseDir, "sandboxes", name, "config.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil
	}
	var cfg models.VMConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}

func collectDebugNetworkInfo(baseDir, name string, state *models.VMState) *DebugNetworkInfo {
	info := &DebugNetworkInfo{
		Mode:      "nat",
		IP:        state.IP,
		TAPDevice: state.TAPDevice,
	}

	// Load egress policy
	policyMgr, err := network.NewPolicyManager(baseDir)
	if err == nil {
		if policy, err := policyMgr.GetPolicy(name); err == nil {
			info.AllowedHosts = policy.Allowed
			info.DeniedHosts = policy.Denied
		}
	}

	return info
}

func collectDebugEvents(baseDir, name string, maxEvents int) []vm.Event {
	logger := vm.NewEventLogger(baseDir)
	events, err := logger.Query(vm.EventFilter{
		Sandbox: name,
		Limit:   maxEvents,
	})
	if err != nil {
		return nil
	}
	return events
}

func collectDebugLogs(baseDir, name string) string {
	logPath := filepath.Join(baseDir, "sandboxes", name, "console.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		// Try boot log as fallback
		logPath = filepath.Join(baseDir, "sandboxes", name, "boot.log")
		data, err = os.ReadFile(logPath)
		if err != nil {
			return ""
		}
	}
	// Limit to last 10KB
	if len(data) > 10240 {
		data = data[len(data)-10240:]
	}
	return string(data)
}

func debugCheckFilesystem(baseDir, name string) *DebugFSInfo {
	sandboxDir := filepath.Join(baseDir, "sandboxes", name)
	info := &DebugFSInfo{}

	// RootFS
	info.RootFSPath = filepath.Join(sandboxDir, "rootfs.img")
	if fi, err := os.Stat(info.RootFSPath); err == nil {
		info.RootFSExists = true
		info.RootFSSizeMB = fi.Size() / (1024 * 1024)
	}

	// Kernel
	info.KernelPath = filepath.Join(sandboxDir, "vmlinuz")
	if _, err := os.Stat(info.KernelPath); err == nil {
		info.KernelExists = true
	}

	// Socket
	info.SocketPath = filepath.Join(sandboxDir, "tent.sock")
	if _, err := os.Stat(info.SocketPath); err == nil {
		info.SocketExists = true
	}

	// SSH key
	info.SSHKeyPath = filepath.Join(sandboxDir, "id_ed25519")
	if _, err := os.Stat(info.SSHKeyPath); err == nil {
		info.SSHKeyExists = true
	}

	return info
}

func runDebugDiagnostics(bundle *DebugBundle) []DiagnosticCheck {
	var checks []DiagnosticCheck

	// Check 1: sandbox status
	switch bundle.Sandbox.Status {
	case models.VMStatusRunning:
		checks = append(checks, DiagnosticCheck{
			Name:    "sandbox_status",
			Status:  "ok",
			Message: "Sandbox is running",
		})
	case models.VMStatusStopped:
		checks = append(checks, DiagnosticCheck{
			Name:    "sandbox_status",
			Status:  "ok",
			Message: "Sandbox is stopped",
		})
	case models.VMStatusError:
		checks = append(checks, DiagnosticCheck{
			Name:    "sandbox_status",
			Status:  "error",
			Message: "Sandbox is in error state",
		})
	default:
		checks = append(checks, DiagnosticCheck{
			Name:    "sandbox_status",
			Status:  "warn",
			Message: fmt.Sprintf("Sandbox is in %s state", bundle.Sandbox.Status),
		})
	}

	// Check 2: rootfs exists
	if bundle.FileSystem != nil {
		if bundle.FileSystem.RootFSExists {
			checks = append(checks, DiagnosticCheck{
				Name:    "rootfs_present",
				Status:  "ok",
				Message: fmt.Sprintf("Root filesystem exists (%d MB)", bundle.FileSystem.RootFSSizeMB),
			})
		} else {
			checks = append(checks, DiagnosticCheck{
				Name:    "rootfs_present",
				Status:  "error",
				Message: "Root filesystem not found at " + bundle.FileSystem.RootFSPath,
			})
		}

		// Check 3: kernel exists
		if bundle.FileSystem.KernelExists {
			checks = append(checks, DiagnosticCheck{
				Name:    "kernel_present",
				Status:  "ok",
				Message: "Kernel image found",
			})
		} else {
			checks = append(checks, DiagnosticCheck{
				Name:    "kernel_present",
				Status:  "warn",
				Message: "No kernel image found (may use embedded kernel)",
			})
		}

		// Check 4: SSH key exists
		if bundle.FileSystem.SSHKeyExists {
			checks = append(checks, DiagnosticCheck{
				Name:    "ssh_key_present",
				Status:  "ok",
				Message: "SSH key found",
			})
		} else {
			checks = append(checks, DiagnosticCheck{
				Name:    "ssh_key_present",
				Status:  "warn",
				Message: "No SSH key found — 'tent ssh' may not work",
			})
		}

		// Check 5: stale socket (running sandbox should have socket)
		if bundle.Sandbox.Status == models.VMStatusRunning && !bundle.FileSystem.SocketExists {
			checks = append(checks, DiagnosticCheck{
				Name:    "socket_present",
				Status:  "warn",
				Message: "Sandbox is running but no control socket found",
			})
		} else if bundle.Sandbox.Status == models.VMStatusStopped && bundle.FileSystem.SocketExists {
			checks = append(checks, DiagnosticCheck{
				Name:    "socket_stale",
				Status:  "warn",
				Message: "Sandbox is stopped but stale socket exists — may need cleanup",
			})
		}
	}

	// Check 6: PID for running sandbox
	if bundle.Sandbox.Status == models.VMStatusRunning {
		if bundle.Sandbox.PID > 0 {
			if proc, err := os.FindProcess(bundle.Sandbox.PID); err == nil {
				// On Unix, FindProcess always succeeds; send signal 0 to check
				if err := proc.Signal(nil); err != nil {
					checks = append(checks, DiagnosticCheck{
						Name:    "process_alive",
						Status:  "error",
						Message: fmt.Sprintf("PID %d is not running — sandbox state is stale", bundle.Sandbox.PID),
					})
				} else {
					checks = append(checks, DiagnosticCheck{
						Name:    "process_alive",
						Status:  "ok",
						Message: fmt.Sprintf("Hypervisor process PID %d is alive", bundle.Sandbox.PID),
					})
				}
			}
		} else {
			checks = append(checks, DiagnosticCheck{
				Name:    "process_alive",
				Status:  "warn",
				Message: "No PID recorded for running sandbox",
			})
		}
	}

	// Check 7: resource allocation
	if bundle.Sandbox.MemoryMB > 0 && bundle.Sandbox.MemoryMB < 128 {
		checks = append(checks, DiagnosticCheck{
			Name:    "memory_allocation",
			Status:  "warn",
			Message: fmt.Sprintf("Low memory allocation: %d MB (recommended >= 128 MB)", bundle.Sandbox.MemoryMB),
		})
	}

	// Check 8: network policy
	if bundle.Network != nil {
		if len(bundle.Network.AllowedHosts) == 0 && len(bundle.Network.DeniedHosts) == 0 {
			checks = append(checks, DiagnosticCheck{
				Name:    "network_policy",
				Status:  "ok",
				Message: "Default egress policy (block all external traffic)",
			})
		} else {
			checks = append(checks, DiagnosticCheck{
				Name:    "network_policy",
				Status:  "ok",
				Message: fmt.Sprintf("Custom egress policy: %d allowed, %d denied endpoints",
					len(bundle.Network.AllowedHosts), len(bundle.Network.DeniedHosts)),
			})
		}
	}

	// Check 9: platform compatibility
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		checks = append(checks, DiagnosticCheck{
			Name:    "platform_support",
			Status:  "error",
			Message: fmt.Sprintf("Unsupported platform: %s", runtime.GOOS),
		})
	} else {
		checks = append(checks, DiagnosticCheck{
			Name:    "platform_support",
			Status:  "ok",
			Message: fmt.Sprintf("Platform %s/%s is supported", runtime.GOOS, runtime.GOARCH),
		})
	}

	return checks
}

func writeDebugArchive(bundle *DebugBundle, name, baseDir, outputFile string, includeLogs bool) error {
	if !strings.HasSuffix(outputFile, ".tar.gz") && !strings.HasSuffix(outputFile, ".tgz") {
		outputFile += ".tar.gz"
	}

	f, err := os.Create(outputFile)
	if err != nil {
		return fmt.Errorf("failed to create archive: %w", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	defer gw.Close()

	tw := tar.NewWriter(gw)
	defer tw.Close()

	prefix := fmt.Sprintf("tent-debug-%s-%s", name, time.Now().Format("20060102-150405"))

	// Write bundle JSON
	bundleJSON, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal bundle: %w", err)
	}
	if err := debugAddToTar(tw, prefix+"/debug.json", bundleJSON); err != nil {
		return err
	}

	// Write config if present
	configPath := filepath.Join(baseDir, "sandboxes", name, "config.json")
	if data, err := os.ReadFile(configPath); err == nil {
		if err := debugAddToTar(tw, prefix+"/config.json", data); err != nil {
			return err
		}
	}

	// Write state file
	statePath := filepath.Join(baseDir, "sandboxes", name, "state.json")
	if data, err := os.ReadFile(statePath); err == nil {
		if err := debugAddToTar(tw, prefix+"/state.json", data); err != nil {
			return err
		}
	}

	// Write logs if requested
	if includeLogs {
		for _, logFile := range []string{"console.log", "boot.log"} {
			logPath := filepath.Join(baseDir, "sandboxes", name, logFile)
			if data, err := os.ReadFile(logPath); err == nil {
				if err := debugAddToTar(tw, prefix+"/"+logFile, data); err != nil {
					return err
				}
			}
		}
	}

	// Write events
	if len(bundle.Events) > 0 {
		eventsJSON, err := json.MarshalIndent(bundle.Events, "", "  ")
		if err == nil {
			if err := debugAddToTar(tw, prefix+"/events.json", eventsJSON); err != nil {
				return err
			}
		}
	}

	// Write network policy
	policyPath := filepath.Join(baseDir, "network", name+".json")
	if data, err := os.ReadFile(policyPath); err == nil {
		if err := debugAddToTar(tw, prefix+"/network-policy.json", data); err != nil {
			return err
		}
	}

	// Write human-readable diagnostics summary
	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("Tent Debug Report: %s\n", name))
	summary.WriteString(fmt.Sprintf("Generated: %s\n", bundle.Timestamp))
	summary.WriteString(fmt.Sprintf("Platform: %s/%s\n", bundle.Platform.OS, bundle.Platform.Arch))
	summary.WriteString(fmt.Sprintf("Status: %s\n\n", bundle.Sandbox.Status))
	summary.WriteString("Diagnostics:\n")
	for _, check := range bundle.Diagnostics {
		icon := "  "
		switch check.Status {
		case "ok":
			icon = "OK"
		case "warn":
			icon = "!!"
		case "error":
			icon = "XX"
		}
		summary.WriteString(fmt.Sprintf("  [%s] %s: %s\n", icon, check.Name, check.Message))
	}
	if err := debugAddToTar(tw, prefix+"/SUMMARY.txt", []byte(summary.String())); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "Debug bundle written to %s\n", outputFile)
	return nil
}

func debugAddToTar(tw *tar.Writer, name string, data []byte) error {
	header := &tar.Header{
		Name:    name,
		Size:    int64(len(data)),
		Mode:    0644,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("failed to write tar header for %s: %w", name, err)
	}
	if _, err := io.Copy(tw, bytes.NewReader(data)); err != nil {
		return fmt.Errorf("failed to write tar data for %s: %w", name, err)
	}
	return nil
}
