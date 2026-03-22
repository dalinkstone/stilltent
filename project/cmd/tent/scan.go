package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/internal/network"
	"github.com/dalinkstone/tent/internal/state"
	"github.com/dalinkstone/tent/pkg/models"
)

// ScanSeverity indicates how critical a finding is
type ScanSeverity string

const (
	ScanSeverityCritical ScanSeverity = "CRITICAL"
	ScanSeverityWarning  ScanSeverity = "WARNING"
	ScanSeverityInfo     ScanSeverity = "INFO"
)

// ScanFinding represents a single security finding
type ScanFinding struct {
	Severity    ScanSeverity `json:"severity"`
	Category    string       `json:"category"`
	Description string       `json:"description"`
	Suggestion  string       `json:"suggestion"`
}

// ScanReport contains all findings for a sandbox
type ScanReport struct {
	SandboxName string        `json:"sandbox_name"`
	Status      string        `json:"status"`
	Score       int           `json:"score"`
	MaxScore    int           `json:"max_score"`
	Findings    []ScanFinding `json:"findings"`
}

func scanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Security scanning for sandbox configurations",
		Long: `Scan sandbox configurations for security issues, misconfigurations,
and best-practice violations.

The scanner checks network policies, resource limits, mount configurations,
and other settings to identify potential security risks.`,
	}

	cmd.AddCommand(scanSandboxCmd())
	cmd.AddCommand(scanAllCmd())
	cmd.AddCommand(scanFileCmd())

	return cmd
}

func scanSandboxCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "sandbox <name>",
		Short: "Scan a specific sandbox for security issues",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			baseDir := getBaseDir()

			sm, err := state.NewStateManager(filepath.Join(baseDir, "state.json"))
			if err != nil {
				return fmt.Errorf("failed to open state: %w", err)
			}

			vmState, err := sm.GetVM(name)
			if err != nil {
				return fmt.Errorf("sandbox %q not found: %w", name, err)
			}

			cfg := loadSandboxConfig(baseDir, name)
			policyMgr, _ := network.NewPolicyManager(baseDir)

			report := scanSandbox(vmState, cfg, policyMgr)

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			printScanReport(report)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func scanAllCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "all",
		Short: "Scan all sandboxes for security issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()

			sm, err := state.NewStateManager(filepath.Join(baseDir, "state.json"))
			if err != nil {
				return fmt.Errorf("failed to open state: %w", err)
			}

			vms, err := sm.ListVMs()
			if err != nil {
				return fmt.Errorf("failed to list sandboxes: %w", err)
			}

			if len(vms) == 0 {
				fmt.Println("No sandboxes found.")
				return nil
			}

			policyMgr, _ := network.NewPolicyManager(baseDir)
			var reports []ScanReport

			for _, vmState := range vms {
				cfg := loadSandboxConfig(baseDir, vmState.Name)
				report := scanSandbox(vmState, cfg, policyMgr)
				reports = append(reports, report)
			}

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(reports)
			}

			// Print summary table
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SANDBOX\tSCORE\tSTATUS\tCRITICAL\tWARNING\tINFO")
			for _, r := range reports {
				crit, warn, info := countSeverities(r.Findings)
				fmt.Fprintf(w, "%s\t%d/%d\t%s\t%d\t%d\t%d\n",
					r.SandboxName, r.Score, r.MaxScore, r.Status,
					crit, warn, info)
			}
			w.Flush()

			fmt.Println()
			for _, r := range reports {
				if len(r.Findings) > 0 {
					printScanReport(r)
					fmt.Println()
				}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

func scanFileCmd() *cobra.Command {
	var outputJSON bool

	cmd := &cobra.Command{
		Use:   "file <config.yaml>",
		Short: "Scan a sandbox configuration file for security issues",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := args[0]

			data, err := os.ReadFile(cfgPath)
			if err != nil {
				return fmt.Errorf("failed to read config file: %w", err)
			}

			var cfg models.VMConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return fmt.Errorf("failed to parse config: %w", err)
			}

			if cfg.Name == "" {
				cfg.Name = cfgPath
			}

			report := scanConfig(cfg.Name, &cfg)

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(report)
			}

			printScanReport(report)
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output in JSON format")
	return cmd
}

// scanSandbox performs a full security scan on a running/stopped sandbox
func scanSandbox(vmState *models.VMState, cfg *models.VMConfig, policyMgr *network.PolicyManager) ScanReport {
	var findings []ScanFinding

	// Check network policy
	findings = append(findings, scanNetworkPolicy(vmState, cfg, policyMgr)...)

	// Check resource limits
	findings = append(findings, scanResourceLimits(vmState, cfg)...)

	// Check mount security
	findings = append(findings, scanMounts(cfg)...)

	// Check health monitoring
	findings = append(findings, scanHealthConfig(cfg)...)

	// Check general configuration
	findings = append(findings, scanGeneralConfig(vmState, cfg)...)

	score, maxScore := calculateScore(findings)
	status := "PASS"
	if score < maxScore/2 {
		status = "FAIL"
	} else if score < maxScore*3/4 {
		status = "WARN"
	}

	return ScanReport{
		SandboxName: vmState.Name,
		Status:      status,
		Score:       score,
		MaxScore:    maxScore,
		Findings:    findings,
	}
}

// scanConfig scans a config file without requiring a running sandbox
func scanConfig(name string, cfg *models.VMConfig) ScanReport {
	vmState := &models.VMState{
		Name:     name,
		VCPUs:    cfg.VCPUs,
		MemoryMB: cfg.MemoryMB,
		DiskGB:   cfg.DiskGB,
	}
	var findings []ScanFinding

	findings = append(findings, scanNetworkPolicy(vmState, cfg, nil)...)
	findings = append(findings, scanResourceLimits(vmState, cfg)...)
	findings = append(findings, scanMounts(cfg)...)
	findings = append(findings, scanHealthConfig(cfg)...)
	findings = append(findings, scanGeneralConfig(vmState, cfg)...)

	score, maxScore := calculateScore(findings)
	status := "PASS"
	if score < maxScore/2 {
		status = "FAIL"
	} else if score < maxScore*3/4 {
		status = "WARN"
	}

	return ScanReport{
		SandboxName: name,
		Status:      status,
		Score:       score,
		MaxScore:    maxScore,
		Findings:    findings,
	}
}

func scanNetworkPolicy(vmState *models.VMState, cfg *models.VMConfig, policyMgr *network.PolicyManager) []ScanFinding {
	var findings []ScanFinding

	// Check if network allow list is too broad
	if cfg != nil && len(cfg.Network.Allow) > 0 {
		for _, endpoint := range cfg.Network.Allow {
			ep := strings.TrimSpace(endpoint)
			// Wildcard allows are dangerous
			if ep == "*" || ep == "0.0.0.0/0" || ep == "::/0" {
				findings = append(findings, ScanFinding{
					Severity:    ScanSeverityCritical,
					Category:    "network",
					Description: fmt.Sprintf("Wildcard egress allow rule: %s — defeats sandbox isolation", ep),
					Suggestion:  "Replace with specific endpoint allowlist entries",
				})
			}
			// Broad CIDR ranges
			if strings.Contains(ep, "/") {
				parts := strings.Split(ep, "/")
				if len(parts) == 2 {
					if parts[1] == "0" || parts[1] == "1" || parts[1] == "2" ||
						parts[1] == "3" || parts[1] == "4" || parts[1] == "8" {
						findings = append(findings, ScanFinding{
							Severity:    ScanSeverityWarning,
							Category:    "network",
							Description: fmt.Sprintf("Overly broad CIDR range in allow list: %s", ep),
							Suggestion:  "Use more specific CIDR ranges or individual endpoints",
						})
					}
				}
			}
		}

		if len(cfg.Network.Allow) > 20 {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityWarning,
				Category:    "network",
				Description: fmt.Sprintf("Large allow list with %d entries", len(cfg.Network.Allow)),
				Suggestion:  "Review and consolidate network allow rules to minimize attack surface",
			})
		}
	} else if cfg != nil && len(cfg.Network.Allow) == 0 && len(cfg.Network.Deny) == 0 {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityInfo,
			Category:    "network",
			Description: "No explicit network allow/deny rules configured (default: block all egress)",
			Suggestion:  "This is secure by default — add allow rules only as needed",
		})
	}

	// Check for privileged port forwards
	if cfg != nil {
		for _, pf := range cfg.Network.Ports {
			if pf.Host < 1024 {
				findings = append(findings, ScanFinding{
					Severity:    ScanSeverityWarning,
					Category:    "network",
					Description: fmt.Sprintf("Privileged host port %d forwarded to guest port %d", pf.Host, pf.Guest),
					Suggestion:  "Use unprivileged ports (>=1024) when possible",
				})
			}
		}
	}

	// Check egress policy from policy manager
	if policyMgr != nil {
		policy, err := policyMgr.GetPolicy(vmState.Name)
		if err == nil && policy != nil {
			// Check for wildcard entries in the policy allowed list
			for _, ep := range policy.Allowed {
				if ep == "*" || ep == "0.0.0.0/0" || ep == "::/0" {
					findings = append(findings, ScanFinding{
						Severity:    ScanSeverityCritical,
						Category:    "network",
						Description: fmt.Sprintf("Wildcard entry in active egress policy: %s", ep),
						Suggestion:  "Remove wildcard entries and use specific endpoint allow rules",
					})
				}
			}
		}
	}

	return findings
}

func scanResourceLimits(vmState *models.VMState, cfg *models.VMConfig) []ScanFinding {
	var findings []ScanFinding

	vcpus := vmState.VCPUs
	memMB := vmState.MemoryMB

	// Check for excessive resource allocation
	if vcpus > 8 {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityWarning,
			Category:    "resources",
			Description: fmt.Sprintf("High vCPU allocation: %d vCPUs", vcpus),
			Suggestion:  "Consider whether all vCPUs are needed — high allocations consume host resources",
		})
	}

	if memMB > 8192 {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityWarning,
			Category:    "resources",
			Description: fmt.Sprintf("High memory allocation: %d MB", memMB),
			Suggestion:  "Consider whether all memory is needed — reduces available host resources",
		})
	}

	// Check for missing resource limits
	if cfg != nil && cfg.Resources == nil {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityWarning,
			Category:    "resources",
			Description: "No resource limits configured — sandbox can consume unbounded host resources",
			Suggestion:  "Set CPU, memory, and I/O limits via resources config to prevent resource exhaustion",
		})
	} else if cfg != nil && cfg.Resources != nil {
		if cfg.Resources.PidsMax == 0 {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityInfo,
				Category:    "resources",
				Description: "No PID limit set — sandbox can create unlimited processes",
				Suggestion:  "Set pids_max to prevent fork bombs (e.g., 4096)",
			})
		}
		if cfg.Resources.IOReadBPS == 0 && cfg.Resources.IOWriteBPS == 0 {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityInfo,
				Category:    "resources",
				Description: "No I/O bandwidth limits set",
				Suggestion:  "Set io_read_bps and io_write_bps to prevent disk I/O saturation",
			})
		}
	}

	return findings
}

func scanMounts(cfg *models.VMConfig) []ScanFinding {
	var findings []ScanFinding
	if cfg == nil {
		return findings
	}

	sensitivePathPrefixes := []string{
		"/etc", "/var", "/root", "/home",
		"/private/etc", "/private/var",
		"/Users",
	}

	for _, m := range cfg.Mounts {
		// Check for writable mounts to sensitive host paths
		hostPath := m.Host
		for _, prefix := range sensitivePathPrefixes {
			if strings.HasPrefix(hostPath, prefix) {
				sev := ScanSeverityWarning
				if !m.Readonly {
					sev = ScanSeverityCritical
				}
				findings = append(findings, ScanFinding{
					Severity:    sev,
					Category:    "mounts",
					Description: fmt.Sprintf("Mount from sensitive host path %q (readonly=%v)", hostPath, m.Readonly),
					Suggestion:  "Avoid mounting sensitive host directories; use readonly if necessary",
				})
				break
			}
		}

		// Check for root filesystem mount
		if hostPath == "/" {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityCritical,
				Category:    "mounts",
				Description: "Host root filesystem mounted into sandbox",
				Suggestion:  "Never mount the host root filesystem — mount only specific directories",
			})
		}

		// Recommend readonly for all mounts
		if !m.Readonly {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityInfo,
				Category:    "mounts",
				Description: fmt.Sprintf("Writable mount: %s -> %s", m.Host, m.Guest),
				Suggestion:  "Use readonly mounts when the sandbox only needs to read data",
			})
		}
	}

	return findings
}

func scanHealthConfig(cfg *models.VMConfig) []ScanFinding {
	var findings []ScanFinding
	if cfg == nil {
		return findings
	}

	if cfg.HealthCheck == nil {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityInfo,
			Category:    "health",
			Description: "No health check configured",
			Suggestion:  "Configure a health check to detect sandbox failures automatically",
		})
	} else {
		if cfg.HealthCheck.TimeoutSec > 60 {
			findings = append(findings, ScanFinding{
				Severity:    ScanSeverityWarning,
				Category:    "health",
				Description: fmt.Sprintf("Health check timeout is very long: %ds", cfg.HealthCheck.TimeoutSec),
				Suggestion:  "Use a shorter timeout (5-30s) for faster failure detection",
			})
		}
	}

	return findings
}

func scanGeneralConfig(vmState *models.VMState, cfg *models.VMConfig) []ScanFinding {
	var findings []ScanFinding

	// Check for default/weak sandbox names
	weakNames := map[string]bool{"test": true, "tmp": true, "temp": true, "sandbox": true, "default": true}
	if weakNames[vmState.Name] {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityInfo,
			Category:    "config",
			Description: fmt.Sprintf("Generic sandbox name %q", vmState.Name),
			Suggestion:  "Use descriptive names to identify sandbox purpose",
		})
	}

	// Check restart policy
	if cfg != nil && cfg.RestartPolicy == "always" {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityWarning,
			Category:    "config",
			Description: "Restart policy set to 'always' — sandbox restarts indefinitely on failure",
			Suggestion:  "Use 'on-failure' with a max retry count to prevent restart loops",
		})
	}

	// Check for missing labels
	if vmState.Labels == nil || len(vmState.Labels) == 0 {
		findings = append(findings, ScanFinding{
			Severity:    ScanSeverityInfo,
			Category:    "config",
			Description: "No labels set on sandbox",
			Suggestion:  "Add labels for easier management and identification (e.g., owner, purpose, environment)",
		})
	}

	return findings
}

func calculateScore(findings []ScanFinding) (int, int) {
	maxScore := 100
	score := maxScore

	for _, f := range findings {
		switch f.Severity {
		case ScanSeverityCritical:
			score -= 20
		case ScanSeverityWarning:
			score -= 10
		case ScanSeverityInfo:
			score -= 2
		}
	}

	if score < 0 {
		score = 0
	}

	return score, maxScore
}

func countSeverities(findings []ScanFinding) (critical, warning, info int) {
	for _, f := range findings {
		switch f.Severity {
		case ScanSeverityCritical:
			critical++
		case ScanSeverityWarning:
			warning++
		case ScanSeverityInfo:
			info++
		}
	}
	return
}

func printScanReport(report ScanReport) {
	scoreLabel := "PASS"
	if report.Status == "FAIL" {
		scoreLabel = "FAIL"
	} else if report.Status == "WARN" {
		scoreLabel = "WARN"
	}

	fmt.Printf("=== Scan Report: %s [%s %d/%d] ===\n",
		report.SandboxName, scoreLabel, report.Score, report.MaxScore)

	if len(report.Findings) == 0 {
		fmt.Println("  No issues found.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	for _, f := range report.Findings {
		fmt.Fprintf(w, "  [%s]\t%s\t%s\n", f.Severity, f.Category, f.Description)
		fmt.Fprintf(w, "  \t\t  -> %s\n", f.Suggestion)
	}
	w.Flush()
}

func loadSandboxConfig(baseDir, name string) *models.VMConfig {
	cfgPath := filepath.Join(baseDir, "vms", name, "config.yaml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return nil
	}

	var cfg models.VMConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	return &cfg
}
