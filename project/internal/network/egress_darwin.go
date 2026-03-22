//go:build darwin
// +build darwin

package network

import (
	"fmt"
	"os/exec"
	"strings"
)

// pfBackend implements EgressBackend using macOS PF (Packet Filter)
type pfBackend struct {
	anchorName string
}

func newEgressBackend() EgressBackend {
	return &pfBackend{
		anchorName: "com.tent",
	}
}

// Init creates the PF anchor for tent rules
func (b *pfBackend) Init() error {
	// Check if PF is enabled; if not, we operate in dry-run mode
	// (rules are generated but not applied until PF is enabled)
	// Load our anchor into the main PF ruleset
	// We use a sub-anchor so tent rules don't interfere with system rules
	rule := fmt.Sprintf("anchor \"%s\"\n", b.anchorName)

	// Check if anchor already exists in pf.conf
	cmd := exec.Command("pfctl", "-sr")
	output, _ := cmd.Output()
	if strings.Contains(string(output), b.anchorName) {
		return nil // Already configured
	}

	// Try to load the anchor rule
	cmd = exec.Command("pfctl", "-f", "-")
	cmd.Stdin = strings.NewReader(rule)
	// Ignore errors — PF may not be enabled, which is fine
	// Rules will take effect once PF is enabled
	_ = cmd.Run()

	return nil
}

// ApplyRules generates PF rules for a sandbox and loads them into the anchor
func (b *pfBackend) ApplyRules(rules *SandboxRules) error {
	if rules.SandboxIP == "" {
		return nil // No IP assigned yet, skip
	}

	pfRules := b.generateRules(rules)

	// Load rules into sandbox-specific sub-anchor
	anchorPath := fmt.Sprintf("%s/%s", b.anchorName, rules.SandboxName)
	cmd := exec.Command("pfctl", "-a", anchorPath, "-f", "-")
	cmd.Stdin = strings.NewReader(pfRules)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pfctl load failed: %s: %w", string(output), err)
	}

	return nil
}

// generateRules creates PF rule text for a sandbox
func (b *pfBackend) generateRules(sr *SandboxRules) string {
	var sb strings.Builder
	srcIP := sr.SandboxIP

	// Allow loopback and inter-sandbox traffic on the bridge subnet
	sb.WriteString(fmt.Sprintf("pass out quick from %s to 172.16.0.0/24\n", srcIP))

	// Allow DNS to the host gateway so allowlist hostnames can resolve
	sb.WriteString(fmt.Sprintf("pass out quick proto {tcp, udp} from %s to 172.16.0.1 port 53\n", srcIP))

	// Generate allow rules for each endpoint
	for _, rule := range sr.Rules {
		b.writeAllowRule(&sb, srcIP, rule)
	}

	// Default deny all outbound from this sandbox
	sb.WriteString(fmt.Sprintf("block out quick from %s to any\n", srcIP))

	return sb.String()
}

func (b *pfBackend) writeAllowRule(sb *strings.Builder, srcIP string, rule *EgressRule) {
	proto := "{tcp, udp}"
	if rule.Proto != "" {
		proto = rule.Proto
	}

	portClause := ""
	if rule.Port != 0 {
		portClause = fmt.Sprintf(" port %d", rule.Port)
	}

	if rule.CIDR != nil {
		sb.WriteString(fmt.Sprintf("pass out quick proto %s from %s to %s%s\n",
			proto, srcIP, rule.CIDR.String(), portClause))
		return
	}

	for _, ip := range rule.ResolvedIPs {
		sb.WriteString(fmt.Sprintf("pass out quick proto %s from %s to %s%s\n",
			proto, srcIP, ip.String(), portClause))
	}
}

// RemoveRules flushes the sandbox-specific sub-anchor
func (b *pfBackend) RemoveRules(sandboxName string) error {
	anchorPath := fmt.Sprintf("%s/%s", b.anchorName, sandboxName)
	cmd := exec.Command("pfctl", "-a", anchorPath, "-F", "all")
	_ = cmd.Run() // Ignore errors if anchor doesn't exist
	return nil
}

// Cleanup flushes the entire tent anchor
func (b *pfBackend) Cleanup() error {
	cmd := exec.Command("pfctl", "-a", b.anchorName, "-F", "all")
	_ = cmd.Run()
	return nil
}
