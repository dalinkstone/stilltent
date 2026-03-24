//go:build linux
// +build linux

package network

import (
	"fmt"
	"os/exec"
	"strings"
)

const (
	tentChain    = "TENT_EGRESS"
	tentFwdChain = "TENT_FWD"
)

// iptablesBackend implements EgressBackend using Linux iptables
type iptablesBackend struct{}

func newEgressBackend() EgressBackend {
	return &iptablesBackend{}
}

// Init creates the iptables chains for tent egress filtering
func (b *iptablesBackend) Init() error {
	// Create custom chains (ignore errors if they already exist)
	exec.Command("iptables", "-N", tentChain).Run()
	exec.Command("iptables", "-N", tentFwdChain).Run()

	// Insert jump rules into FORWARD chain if not present
	if !b.chainHasJump("FORWARD", tentFwdChain) {
		if err := exec.Command("iptables", "-I", "FORWARD", "-j", tentFwdChain).Run(); err != nil {
			return fmt.Errorf("failed to insert FORWARD jump: %w", err)
		}
	}

	return nil
}

// ApplyRules generates iptables rules for a sandbox
func (b *iptablesBackend) ApplyRules(rules *SandboxRules) error {
	if rules.SandboxIP == "" {
		return nil
	}

	sandboxChain := b.sandboxChain(rules.SandboxName)

	// Flush or create the per-sandbox chain
	exec.Command("iptables", "-N", sandboxChain).Run()
	exec.Command("iptables", "-F", sandboxChain).Run()

	// Add jump from TENT_FWD to sandbox chain for this source IP
	b.removeJumpsForIP(rules.SandboxIP)
	if err := exec.Command("iptables", "-A", tentFwdChain,
		"-s", rules.SandboxIP, "-j", sandboxChain).Run(); err != nil {
		return fmt.Errorf("failed to add sandbox jump rule: %w", err)
	}

	// Allow established/related connections back
	exec.Command("iptables", "-A", sandboxChain,
		"-m", "state", "--state", "ESTABLISHED,RELATED",
		"-j", "ACCEPT").Run()

	// Allow inter-sandbox traffic on the bridge subnet
	exec.Command("iptables", "-A", sandboxChain,
		"-s", rules.SandboxIP, "-d", "172.16.0.0/24",
		"-j", "ACCEPT").Run()

	// Allow DNS to host gateway
	exec.Command("iptables", "-A", sandboxChain,
		"-s", rules.SandboxIP, "-d", "172.16.0.1",
		"-p", "udp", "--dport", "53",
		"-j", "ACCEPT").Run()
	exec.Command("iptables", "-A", sandboxChain,
		"-s", rules.SandboxIP, "-d", "172.16.0.1",
		"-p", "tcp", "--dport", "53",
		"-j", "ACCEPT").Run()

	// Add allow rules for each endpoint
	for _, rule := range rules.Rules {
		b.addAllowRule(sandboxChain, rules.SandboxIP, rule)
	}

	// Default deny at the end of the chain
	exec.Command("iptables", "-A", sandboxChain,
		"-s", rules.SandboxIP,
		"-j", "DROP").Run()

	return nil
}

func (b *iptablesBackend) addAllowRule(chain, srcIP string, rule *EgressRule) {
	protos := []string{"tcp", "udp"}
	if rule.Proto != "" {
		protos = []string{rule.Proto}
	}

	destinations := []string{}
	if rule.CIDR != nil {
		destinations = append(destinations, rule.CIDR.String())
	} else {
		for _, ip := range rule.ResolvedIPs {
			destinations = append(destinations, ip.String())
		}
	}

	for _, dst := range destinations {
		for _, proto := range protos {
			args := []string{"-A", chain,
				"-s", srcIP, "-d", dst,
				"-p", proto}
			if rule.Port != 0 {
				args = append(args, "--dport", fmt.Sprintf("%d", rule.Port))
			}
			args = append(args, "-j", "ACCEPT")
			exec.Command("iptables", args...).Run()
		}
	}
}

// RemoveRules removes all rules for a sandbox
func (b *iptablesBackend) RemoveRules(sandboxName string) error {
	sandboxChain := b.sandboxChain(sandboxName)

	// Remove jump rules pointing to this chain
	b.removeJumpsToChain(tentFwdChain, sandboxChain)

	// Flush and delete the per-sandbox chain
	exec.Command("iptables", "-F", sandboxChain).Run()
	exec.Command("iptables", "-X", sandboxChain).Run()

	return nil
}

// Cleanup tears down all tent iptables state
func (b *iptablesBackend) Cleanup() error {
	// Remove jump from FORWARD
	b.removeJumpsToChain("FORWARD", tentFwdChain)

	// Flush and delete tent chains
	exec.Command("iptables", "-F", tentFwdChain).Run()
	exec.Command("iptables", "-X", tentFwdChain).Run()
	exec.Command("iptables", "-F", tentChain).Run()
	exec.Command("iptables", "-X", tentChain).Run()

	return nil
}

func (b *iptablesBackend) sandboxChain(name string) string {
	// iptables chain names max 28 chars; truncate if needed
	chain := "TENT_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
	if len(chain) > 28 {
		chain = chain[:28]
	}
	return chain
}

func (b *iptablesBackend) chainHasJump(chain, target string) bool {
	cmd := exec.Command("iptables", "-L", chain, "-n")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), target)
}

func (b *iptablesBackend) removeJumpsToChain(fromChain, targetChain string) {
	// List rules with line numbers and remove matches in reverse order
	cmd := exec.Command("iptables", "-L", fromChain, "--line-numbers", "-n")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	var lineNums []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, targetChain) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				lineNums = append(lineNums, fields[0])
			}
		}
	}

	// Delete in reverse order so line numbers stay valid
	for i := len(lineNums) - 1; i >= 0; i-- {
		exec.Command("iptables", "-D", fromChain, lineNums[i]).Run()
	}
}

func (b *iptablesBackend) removeJumpsForIP(ip string) {
	cmd := exec.Command("iptables", "-L", tentFwdChain, "--line-numbers", "-n")
	output, err := cmd.Output()
	if err != nil {
		return
	}

	var lineNums []string
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, ip) {
			fields := strings.Fields(line)
			if len(fields) > 0 {
				lineNums = append(lineNums, fields[0])
			}
		}
	}

	for i := len(lineNums) - 1; i >= 0; i-- {
		exec.Command("iptables", "-D", tentFwdChain, lineNums[i]).Run()
	}
}
