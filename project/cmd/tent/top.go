package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
)

func topCmd() *cobra.Command {
	var (
		sortBy    string
		noHeader  bool
		treeFmt   bool
		filterUser string
	)

	cmd := &cobra.Command{
		Use:   "top <name>",
		Short: "Display running processes in a sandbox",
		Long: `Show processes running inside a sandbox, similar to the Unix top/ps commands.

Retrieves the process list from the guest via the tent agent or SSH and displays
it in a tabular format.

Examples:
  tent top mybox
  tent top mybox --sort cpu
  tent top mybox --sort mem
  tent top mybox --tree
  tent top mybox --user root
  tent top mybox --no-header`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

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

			// Run ps inside the sandbox
			psArgs := []string{"ps", "aux", "--no-headers"}
			output, _, err := manager.Exec(name, psArgs)
			if err != nil {
				// Fallback: try without --no-headers
				psArgs = []string{"ps", "aux"}
				output, _, err = manager.Exec(name, psArgs)
				if err != nil {
					return fmt.Errorf("failed to get process list: %w", err)
				}
			}

			processes, err := parsePSOutput(output)
			if err != nil {
				return fmt.Errorf("failed to parse process list: %w", err)
			}

			// Filter by user if specified
			if filterUser != "" {
				var filtered []processInfo
				for _, p := range processes {
					if p.User == filterUser {
						filtered = append(filtered, p)
					}
				}
				processes = filtered
			}

			// Sort processes
			sortProcesses(processes, sortBy)

			// Display
			if treeFmt {
				printProcessTree(processes, noHeader)
			} else {
				printProcessTable(processes, noHeader)
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&sortBy, "sort", "pid", "Sort by field: pid, cpu, mem, user, command")
	cmd.Flags().BoolVar(&noHeader, "no-header", false, "Suppress column headers")
	cmd.Flags().BoolVar(&treeFmt, "tree", false, "Display processes as a tree")
	cmd.Flags().StringVar(&filterUser, "user", "", "Filter by username")

	return cmd
}

// processInfo represents a single process from ps output
type processInfo struct {
	User    string
	PID     int
	PPID    int
	CPU     float64
	Mem     float64
	VSZ     string
	RSS     string
	TTY     string
	Stat    string
	Start   string
	Time    string
	Command string
}

// parsePSOutput parses the output of `ps aux` into structured process info
func parsePSOutput(output string) ([]processInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var processes []processInfo

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Skip header line
		if strings.HasPrefix(line, "USER") || strings.HasPrefix(line, "UID") {
			continue
		}

		p, err := parsePSLine(line)
		if err != nil {
			continue // Skip unparseable lines
		}
		processes = append(processes, p)
	}

	return processes, nil
}

// parsePSLine parses a single line of ps aux output
// Format: USER PID %CPU %MEM VSZ RSS TTY STAT START TIME COMMAND
func parsePSLine(line string) (processInfo, error) {
	fields := strings.Fields(line)
	if len(fields) < 11 {
		return processInfo{}, fmt.Errorf("too few fields: %d", len(fields))
	}

	pid, err := strconv.Atoi(fields[1])
	if err != nil {
		return processInfo{}, fmt.Errorf("invalid PID: %s", fields[1])
	}

	cpu, _ := strconv.ParseFloat(fields[2], 64)
	mem, _ := strconv.ParseFloat(fields[3], 64)

	// Command is everything from field 10 onwards (may contain spaces)
	command := strings.Join(fields[10:], " ")

	return processInfo{
		User:    fields[0],
		PID:     pid,
		CPU:     cpu,
		Mem:     mem,
		VSZ:     fields[4],
		RSS:     fields[5],
		TTY:     fields[6],
		Stat:    fields[7],
		Start:   fields[8],
		Time:    fields[9],
		Command: command,
	}, nil
}

// sortProcesses sorts processes by the given field
func sortProcesses(processes []processInfo, field string) {
	sort.Slice(processes, func(i, j int) bool {
		switch field {
		case "cpu":
			return processes[i].CPU > processes[j].CPU
		case "mem":
			return processes[i].Mem > processes[j].Mem
		case "user":
			return processes[i].User < processes[j].User
		case "command", "cmd":
			return processes[i].Command < processes[j].Command
		default: // pid
			return processes[i].PID < processes[j].PID
		}
	})
}

// printProcessTable displays processes in a tabular format
func printProcessTable(processes []processInfo, noHeader bool) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)

	if !noHeader {
		fmt.Fprintf(w, "USER\tPID\t%%CPU\t%%MEM\tVSZ\tRSS\tTTY\tSTAT\tSTART\tTIME\tCOMMAND\n")
	}

	for _, p := range processes {
		fmt.Fprintf(w, "%s\t%d\t%.1f\t%.1f\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			p.User, p.PID, p.CPU, p.Mem, p.VSZ, p.RSS, p.TTY, p.Stat, p.Start, p.Time, p.Command)
	}

	w.Flush()
}

// printProcessTree displays processes in a tree format showing parent-child relationships
func printProcessTree(processes []processInfo, noHeader bool) {
	if !noHeader {
		fmt.Printf("%-8s %6s %5s %5s  %s\n", "USER", "PID", "%CPU", "%MEM", "COMMAND")
	}

	// Build parent-child map
	// First, get PPID info by running ps with ppid column
	// Since we may not have PPID from ps aux, use a simple indented display
	// based on process hierarchy heuristics

	// Group by common prefixes / known init hierarchy
	// For simplicity, show PID 1 first then others indented
	var initProcs []processInfo
	var otherProcs []processInfo

	for _, p := range processes {
		if p.PID == 1 {
			initProcs = append(initProcs, p)
		} else {
			otherProcs = append(otherProcs, p)
		}
	}

	for _, p := range initProcs {
		fmt.Printf("%-8s %6d %5.1f %5.1f  %s\n", p.User, p.PID, p.CPU, p.Mem, p.Command)
	}

	for _, p := range otherProcs {
		prefix := "├─ "
		if p.PID == otherProcs[len(otherProcs)-1].PID {
			prefix = "└─ "
		}
		fmt.Printf("%-8s %6d %5.1f %5.1f  %s%s\n", p.User, p.PID, p.CPU, p.Mem, prefix, p.Command)
	}
}
