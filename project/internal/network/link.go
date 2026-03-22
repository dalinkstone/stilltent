package network

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// Link represents a point-to-point network link between two sandboxes.
// Unlike compose networks (shared L2 segment for a group), links are
// explicit bidirectional connections between exactly two sandboxes,
// useful for service-mesh topologies and micro-segmented architectures.
type Link struct {
	ID        string    `yaml:"id" json:"id"`
	SandboxA  string    `yaml:"sandbox_a" json:"sandbox_a"`
	SandboxB  string    `yaml:"sandbox_b" json:"sandbox_b"`
	Network   string    `yaml:"network" json:"network"`       // CIDR for the link (e.g. "10.100.0.0/30")
	AddressA  string    `yaml:"address_a" json:"address_a"`   // IP assigned to sandbox A
	AddressB  string    `yaml:"address_b" json:"address_b"`   // IP assigned to sandbox B
	MTU       int       `yaml:"mtu,omitempty" json:"mtu,omitempty"`
	Encrypted bool      `yaml:"encrypted,omitempty" json:"encrypted,omitempty"`
	Labels    map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// LinkManager manages point-to-point network links between sandboxes.
type LinkManager struct {
	baseDir string
	links   map[string]*Link // keyed by link ID
	mu      sync.Mutex
	nextNet uint32 // counter for allocating /30 subnets
}

// NewLinkManager creates a new link manager.
func NewLinkManager(baseDir string) (*LinkManager, error) {
	lm := &LinkManager{
		baseDir: baseDir,
		links:   make(map[string]*Link),
		nextNet: 0,
	}

	if err := lm.load(); err != nil {
		return nil, fmt.Errorf("failed to load links: %w", err)
	}

	return lm, nil
}

// linksDir returns the directory for storing link definitions.
func (lm *LinkManager) linksDir() string {
	return filepath.Join(lm.baseDir, "network-links")
}

// load reads all link definitions from disk.
func (lm *LinkManager) load() error {
	dir := lm.linksDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	var maxNet uint32
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var link Link
		if err := yaml.Unmarshal(data, &link); err != nil {
			continue
		}
		lm.links[link.ID] = &link

		// Track highest subnet to avoid collisions
		_, ipNet, err := net.ParseCIDR(link.Network)
		if err == nil {
			ip := ipNet.IP.To4()
			if ip != nil {
				idx := (uint32(ip[1]) << 16) | (uint32(ip[2]) << 8) | uint32(ip[3])
				subnet := idx / 4
				if subnet >= maxNet {
					maxNet = subnet + 1
				}
			}
		}
	}
	lm.nextNet = maxNet

	return nil
}

// save writes a link definition to disk.
func (lm *LinkManager) save(link *Link) error {
	dir := lm.linksDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := yaml.Marshal(link)
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(dir, link.ID+".yaml"), data, 0644)
}

// allocateSubnet allocates a /30 subnet for a new link from the 10.100.0.0/16 range.
func (lm *LinkManager) allocateSubnet() (cidr, addrA, addrB string, err error) {
	idx := lm.nextNet
	lm.nextNet++

	// Each /30 has 4 IPs: network, host A, host B, broadcast
	base := idx * 4
	if base > 65532 { // 10.100.x.x range exhausted
		return "", "", "", fmt.Errorf("link subnet pool exhausted")
	}

	b2 := byte((base >> 8) & 0xFF)
	b3 := byte(base & 0xFF)

	cidr = fmt.Sprintf("10.100.%d.%d/30", b2, b3)
	addrA = fmt.Sprintf("10.100.%d.%d", b2, b3+1)
	addrB = fmt.Sprintf("10.100.%d.%d", b2, b3+2)

	return cidr, addrA, addrB, nil
}

// CreateLink creates a new point-to-point network link between two sandboxes.
func (lm *LinkManager) CreateLink(sandboxA, sandboxB string, opts LinkOptions) (*Link, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if sandboxA == sandboxB {
		return nil, fmt.Errorf("cannot link a sandbox to itself")
	}

	// Check for existing link between the same pair
	for _, link := range lm.links {
		if (link.SandboxA == sandboxA && link.SandboxB == sandboxB) ||
			(link.SandboxA == sandboxB && link.SandboxB == sandboxA) {
			return nil, fmt.Errorf("link already exists between %q and %q (id: %s)", sandboxA, sandboxB, link.ID)
		}
	}

	cidr, addrA, addrB, err := lm.allocateSubnet()
	if err != nil {
		return nil, err
	}

	id := fmt.Sprintf("link-%s-%s", sandboxA, sandboxB)

	link := &Link{
		ID:        id,
		SandboxA:  sandboxA,
		SandboxB:  sandboxB,
		Network:   cidr,
		AddressA:  addrA,
		AddressB:  addrB,
		MTU:       opts.MTU,
		Encrypted: opts.Encrypted,
		Labels:    opts.Labels,
		CreatedAt: time.Now().UTC(),
	}

	if link.MTU == 0 {
		link.MTU = 1500
	}

	if err := lm.save(link); err != nil {
		return nil, fmt.Errorf("failed to save link: %w", err)
	}

	lm.links[link.ID] = link
	return link, nil
}

// LinkOptions holds optional parameters for link creation.
type LinkOptions struct {
	MTU       int
	Encrypted bool
	Labels    map[string]string
}

// RemoveLink removes a link by ID.
func (lm *LinkManager) RemoveLink(id string) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if _, exists := lm.links[id]; !exists {
		return fmt.Errorf("link %q not found", id)
	}

	delete(lm.links, id)

	path := filepath.Join(lm.linksDir(), id+".yaml")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove link file: %w", err)
	}

	return nil
}

// GetLink returns a link by ID.
func (lm *LinkManager) GetLink(id string) (*Link, error) {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	link, exists := lm.links[id]
	if !exists {
		return nil, fmt.Errorf("link %q not found", id)
	}
	return link, nil
}

// ListLinks returns all links, optionally filtered by sandbox name.
func (lm *LinkManager) ListLinks(sandboxFilter string) []*Link {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	var result []*Link
	for _, link := range lm.links {
		if sandboxFilter != "" && link.SandboxA != sandboxFilter && link.SandboxB != sandboxFilter {
			continue
		}
		result = append(result, link)
	}
	return result
}

// FindLinkBetween returns the link between two sandboxes if it exists.
func (lm *LinkManager) FindLinkBetween(a, b string) *Link {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	for _, link := range lm.links {
		if (link.SandboxA == a && link.SandboxB == b) ||
			(link.SandboxA == b && link.SandboxB == a) {
			return link
		}
	}
	return nil
}

// LinksForSandbox returns all links involving a specific sandbox.
func (lm *LinkManager) LinksForSandbox(name string) []*Link {
	return lm.ListLinks(name)
}

// Peer returns the name of the other sandbox in the link.
func (l *Link) Peer(sandbox string) string {
	if l.SandboxA == sandbox {
		return l.SandboxB
	}
	if l.SandboxB == sandbox {
		return l.SandboxA
	}
	return ""
}

// AddressFor returns the IP address assigned to a specific sandbox in this link.
func (l *Link) AddressFor(sandbox string) string {
	if l.SandboxA == sandbox {
		return l.AddressA
	}
	if l.SandboxB == sandbox {
		return l.AddressB
	}
	return ""
}
