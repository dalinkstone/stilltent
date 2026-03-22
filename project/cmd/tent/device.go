package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func deviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Manage device passthrough for sandboxes",
		Long: `Manage host device passthrough (PCI, USB, GPU, VFIO) for sandbox VMs.

Device passthrough allows sandboxes to directly access host hardware,
which is essential for GPU-accelerated AI workloads, USB peripherals,
and other hardware-dependent tasks.

Examples:
  tent device list-host --type gpu
  tent device attach mybox --type gpu --address 0000:01:00.0
  tent device detach mybox --address 0000:01:00.0
  tent device ls mybox`,
	}

	cmd.AddCommand(deviceListHostCmd())
	cmd.AddCommand(deviceAttachCmd())
	cmd.AddCommand(deviceDetachCmd())
	cmd.AddCommand(deviceListCmd())

	return cmd
}

func deviceListHostCmd() *cobra.Command {
	var (
		deviceType string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "list-host",
		Short: "List host devices available for passthrough",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				baseDir = "/var/lib/tent"
			}

			dm := vm.NewDeviceManager(baseDir)
			devices, err := dm.ListHostDevices(deviceType)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(devices)
			}

			if len(devices) == 0 {
				fmt.Println("No devices found.")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "ADDRESS\tTYPE\tDESCRIPTION\tDRIVER\tIN USE")
			for _, d := range devices {
				inUse := "no"
				if d.InUse {
					inUse = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					d.Address, d.Type, d.Description, d.Driver, inUse)
			}
			return w.Flush()
		},
	}

	cmd.Flags().StringVar(&deviceType, "type", "", "Filter by device type (pci, usb, gpu, vfio)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func deviceAttachCmd() *cobra.Command {
	var (
		deviceType string
		address    string
		devName    string
		readonly   bool
		options    []string
	)

	cmd := &cobra.Command{
		Use:   "attach <sandbox>",
		Short: "Attach a host device to a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				baseDir = "/var/lib/tent"
			}

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			opts := make(map[string]string)
			for _, o := range options {
				parts := strings.SplitN(o, "=", 2)
				if len(parts) == 2 {
					opts[parts[0]] = parts[1]
				}
			}

			dev := models.DeviceConfig{
				Name:     devName,
				Type:     models.DeviceType(deviceType),
				Address:  address,
				Readonly: readonly,
				Options:  opts,
			}

			if err := manager.AttachDevice(sandboxName, dev); err != nil {
				return err
			}

			status := "attached"
			state, _ := manager.Status(sandboxName)
			if state != nil && state.Status == models.VMStatusRunning {
				status = "pending (will take effect on next restart)"
			}

			fmt.Printf("Device %s (%s) %s to sandbox %q\n", address, deviceType, status, sandboxName)
			return nil
		},
	}

	cmd.Flags().StringVar(&deviceType, "type", "", "Device type (pci, usb, gpu, vfio)")
	cmd.Flags().StringVar(&address, "address", "", "Device address (e.g., 0000:01:00.0 for PCI)")
	cmd.Flags().StringVar(&devName, "name", "", "Human-readable device label")
	cmd.Flags().BoolVar(&readonly, "readonly", false, "Mount device in read-only mode")
	cmd.Flags().StringSliceVar(&options, "opt", nil, "Device-specific options (key=value)")

	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("address")

	return cmd
}

func deviceDetachCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detach <sandbox> --address <addr>",
		Short: "Detach a device from a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]
			address, _ := cmd.Flags().GetString("address")

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				baseDir = "/var/lib/tent"
			}

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			if err := manager.DetachDevice(sandboxName, address); err != nil {
				return err
			}

			fmt.Printf("Device %s detached from sandbox %q\n", address, sandboxName)
			return nil
		},
	}

	cmd.Flags().String("address", "", "Device address to detach")
	_ = cmd.MarkFlagRequired("address")

	return cmd
}

func deviceListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "ls <sandbox>",
		Short: "List devices attached to a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxName := args[0]

			baseDir := os.Getenv("TENT_BASE_DIR")
			if baseDir == "" {
				baseDir = "/var/lib/tent"
			}

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}
			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			devices, err := manager.ListDevices(sandboxName)
			if err != nil {
				return err
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(devices)
			}

			if len(devices) == 0 {
				fmt.Printf("No devices attached to sandbox %q\n", sandboxName)
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTYPE\tADDRESS\tSTATUS\tREADONLY")
			for _, d := range devices {
				ro := "no"
				if d.Readonly {
					ro = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
					d.Name, d.Type, d.Address, d.Status, ro)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}
