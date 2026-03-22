package network

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CustomNetwork represents a user-created bridge network that sandboxes can
// connect to. Each custom network has its own subnet and acts as an isolated
// L2 bridge — sandboxes on the same network can communicate, but sandboxes
// on different networks are isolated.
type CustomNetwork struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"` // "bridge" (default)
	Subnet     string            `json:"subnet"`
	Gateway    string            `json:"gateway"`
	Labels     map[string]string `json:"labels,omitempty"`
	Sandboxes  []string          `json:"sandboxes"`          // connected sandbox names
	Internal   bool              `json:"internal"`            // if true, no external connectivity
	CreatedAt  int64             `json:"created_at"`
}

// NetworkStore manages persistence of custom networks.
type NetworkStore struct {
	baseDir  string
	networks map[string]*CustomNetwork
	mu       sync.RWMutex
}

// NewNetworkStore loads or creates the custom network store.
func NewNetworkStore(baseDir string) (*NetworkStore, error) {
	ns := &NetworkStore{
		baseDir:  baseDir,
		networks: make(map[string]*CustomNetwork),
	}
	if err := ns.load(); err != nil {
		return nil, err
	}
	return ns, nil
}

func (ns *NetworkStore) storeDir() string {
	return filepath.Join(ns.baseDir, "networks")
}

func (ns *NetworkStore) load() error {
	dir := ns.storeDir()
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return os.MkdirAll(dir, 0755)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("failed to read networks dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var n CustomNetwork
		if err := json.Unmarshal(data, &n); err != nil {
			continue
		}
		ns.networks[n.Name] = &n
	}
	return nil
}

func (ns *NetworkStore) save(n *CustomNetwork) error {
	dir := ns.storeDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, n.Name+".json"), data, 0644)
}

func (ns *NetworkStore) remove(name string) error {
	return os.Remove(filepath.Join(ns.storeDir(), name+".json"))
}

// CreateNetwork creates a new custom bridge network.
func (ns *NetworkStore) CreateNetwork(name, subnet, gateway string, internal bool, labels map[string]string) (*CustomNetwork, error) {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("network name is required")
	}
	if name == "tent0" || name == "vmnet" || name == "bridge" || name == "host" || name == "none" {
		return nil, fmt.Errorf("network name %q is reserved", name)
	}
	if _, exists := ns.networks[name]; exists {
		return nil, fmt.Errorf("network %q already exists", name)
	}

	// Default subnet allocation if not specified
	if subnet == "" {
		subnet = ns.allocateSubnet()
	}

	// Validate subnet CIDR
	_, ipNet, err := net.ParseCIDR(subnet)
	if err != nil {
		return nil, fmt.Errorf("invalid subnet %q: %w", subnet, err)
	}

	// Default gateway = first usable IP in the subnet
	if gateway == "" {
		gw := make(net.IP, len(ipNet.IP))
		copy(gw, ipNet.IP)
		gw[len(gw)-1]++
		gateway = gw.String()
	}

	// Check for subnet overlap with existing networks
	for _, existing := range ns.networks {
		_, existingNet, err := net.ParseCIDR(existing.Subnet)
		if err != nil {
			continue
		}
		if ipNet.Contains(existingNet.IP) || existingNet.Contains(ipNet.IP) {
			return nil, fmt.Errorf("subnet %s overlaps with network %q (%s)", subnet, existing.Name, existing.Subnet)
		}
	}

	n := &CustomNetwork{
		Name:      name,
		Driver:    "bridge",
		Subnet:    subnet,
		Gateway:   gateway,
		Labels:    labels,
		Sandboxes: []string{},
		Internal:  internal,
		CreatedAt: time.Now().Unix(),
	}

	if err := ns.save(n); err != nil {
		return nil, fmt.Errorf("failed to save network: %w", err)
	}
	ns.networks[name] = n
	return n, nil
}

// DeleteNetwork removes a custom network. It must have no connected sandboxes.
func (ns *NetworkStore) DeleteNetwork(name string) error {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	n, exists := ns.networks[name]
	if !exists {
		return fmt.Errorf("network %q not found", name)
	}
	if len(n.Sandboxes) > 0 {
		return fmt.Errorf("network %q has %d connected sandbox(es) — disconnect them first", name, len(n.Sandboxes))
	}

	if err := ns.remove(name); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove network file: %w", err)
	}
	delete(ns.networks, name)
	return nil
}

// GetNetwork returns a custom network by name.
func (ns *NetworkStore) GetNetwork(name string) (*CustomNetwork, error) {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	n, exists := ns.networks[name]
	if !exists {
		return nil, fmt.Errorf("network %q not found", name)
	}
	return n, nil
}

// ListNetworks returns all custom networks.
func (ns *NetworkStore) ListNetworks() []*CustomNetwork {
	ns.mu.RLock()
	defer ns.mu.RUnlock()

	result := make([]*CustomNetwork, 0, len(ns.networks))
	for _, n := range ns.networks {
		result = append(result, n)
	}
	return result
}

// ConnectSandbox adds a sandbox to a network.
func (ns *NetworkStore) ConnectSandbox(networkName, sandboxName string) error {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	n, exists := ns.networks[networkName]
	if !exists {
		return fmt.Errorf("network %q not found", networkName)
	}

	for _, s := range n.Sandboxes {
		if s == sandboxName {
			return fmt.Errorf("sandbox %q is already connected to network %q", sandboxName, networkName)
		}
	}

	n.Sandboxes = append(n.Sandboxes, sandboxName)
	return ns.save(n)
}

// DisconnectSandbox removes a sandbox from a network.
func (ns *NetworkStore) DisconnectSandbox(networkName, sandboxName string) error {
	ns.mu.Lock()
	defer ns.mu.Unlock()

	n, exists := ns.networks[networkName]
	if !exists {
		return fmt.Errorf("network %q not found", networkName)
	}

	found := false
	filtered := make([]string, 0, len(n.Sandboxes))
	for _, s := range n.Sandboxes {
		if s == sandboxName {
			found = true
		} else {
			filtered = append(filtered, s)
		}
	}
	if !found {
		return fmt.Errorf("sandbox %q is not connected to network %q", sandboxName, networkName)
	}

	n.Sandboxes = filtered
	return ns.save(n)
}

// allocateSubnet picks the next available 172.18.X.0/24 subnet.
func (ns *NetworkStore) allocateSubnet() string {
	used := make(map[int]bool)
	for _, n := range ns.networks {
		_, ipNet, err := net.ParseCIDR(n.Subnet)
		if err != nil {
			continue
		}
		if ipNet.IP[0] == 172 && ipNet.IP[1] == 18 {
			used[int(ipNet.IP[2])] = true
		}
	}
	for i := 1; i < 255; i++ {
		if !used[i] {
			return fmt.Sprintf("172.18.%d.0/24", i)
		}
	}
	return "172.19.0.0/24"
}
