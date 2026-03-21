package firecracker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/dalinkstone/tent/pkg/models"
)

// FirecrackerClient manages Firecracker API communication
type Client struct {
	socketPath string
}

// NewClient creates a new Firecracker client
func NewClient(socketPath string) (*Client, error) {
	if socketPath == "" {
		// Use default socket path
		socketPath = "/var/run/firecracker.socket"
	}

	return &Client{
		socketPath: socketPath,
	}, nil
}

// NewClientWithSocket creates a new Firecracker client with specified socket path
func NewClientWithSocket(socketPath string) (*Client, error) {
	return &Client{socketPath: socketPath}, nil
}

// ConfigureVM configures a VM via Firecracker API
func (c *Client) ConfigureVM(socketPath string, config *models.VMConfig) error {
	// Create the API client
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Create Unix socket client
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	client.Transport = transport

	// 1. Configure boot source
	kernelPath := "/boot/vmlinux" // Default - should be configurable
	if config.Kernel != "default" && config.Kernel != "" {
		kernelPath = config.Kernel
	}

	if err := c.sendRequest(client, "PUT", socketPath, "/boot-source", map[string]interface{}{
		"kernel_image_path": kernelPath,
		"boot_args":         "console=ttyS0 reboot=k panic=1",
	}); err != nil {
		return fmt.Errorf("failed to configure boot source: %w", err)
	}

	// 2. Configure drives
	rootfsPath := config.RootFS
	if rootfsPath == "" {
		rootfsPath = filepath.Join("/var/lib/tent/rootfs", fmt.Sprintf("%s.img", config.Name))
	}

	if err := c.sendRequest(client, "PUT", socketPath, "/drives/rootfs", map[string]interface{}{
		"drive_id":      "rootfs",
		"path_on_host":  rootfsPath,
		"is_root_device": true,
		"is_read_only":  false,
	}); err != nil {
		return fmt.Errorf("failed to configure drives: %w", err)
	}

	// 3. Configure network interfaces
	if err := c.configureNetwork(client, socketPath, config); err != nil {
		return fmt.Errorf("failed to configure network: %w", err)
	}

	// 4. Configure machine config
	if err := c.sendRequest(client, "PUT", socketPath, "/machine-config", map[string]interface{}{
		"vcpu_count":  config.VCPUs,
		"memory_size_mib": config.MemoryMB,
	}); err != nil {
		return fmt.Errorf("failed to configure machine config: %w", err)
	}

	return nil
}

func (c *Client) configureNetwork(client *http.Client, socketPath string, config *models.VMConfig) error {
	// Default network config
	iface := models.NetworkConfig{
		Mode:   "bridge",
		Bridge: "tent0",
	}

	if config.Network.Mode != "" {
		iface = config.Network
	}

	// Configure network interface
	// Use MAC address based on VM name for determinism
	mac := generateMAC(config.Name)

	ifaceConfig := map[string]interface{}{
		"interface_id":  "eth0",
		"host_dev_name": config.Name + "-tap", // This should match the TAP device
		"mac_address":   mac,
		"allow_mdns":    true,
	}

	if err := c.sendRequest(nil, "PUT", socketPath, "/network-interfaces/eth0", ifaceConfig); err != nil {
		return fmt.Errorf("failed to configure network interface: %w", err)
	}

	// Configure port forwarding if needed
	for _, port := range iface.Ports {
		// Note: Port forwarding would typically be handled by iptables,
		// not by Firecracker itself
		_ = port // placeholder for future implementation
	}

	return nil
}

func generateMAC(name string) string {
	// Generate a deterministic MAC address based on VM name
	// Format: 02:00:00:XX:XX:XX
	hash := fnv.New32a()
	hash.Write([]byte(name))
	hashVal := hash.Sum32()

	return fmt.Sprintf("02:00:00:%02x:%02x:%02x",
		(hashVal>>16)&0xFF,
		(hashVal>>8)&0xFF,
		hashVal&0xFF,
	)
}

// StartVM starts the VM
func (c *Client) StartVM(socketPath string) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	client.Transport = transport

	return c.sendRequest(client, "PUT", socketPath, "/actions", map[string]interface{}{
		"action_type": "InstanceStart",
	})
}

// ShutdownVM gracefully shuts down the VM
func (c *Client) ShutdownVM(socketPath string) error {
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	client.Transport = transport

	return c.sendRequest(client, "PUT", socketPath, "/actions", map[string]interface{}{
		"action_type": "SendCtrlAltDel",
	})
}

// sendRequest sends a request to the Firecracker API
func (c *Client) sendRequest(client *http.Client, method, socketPath, path string, body map[string]interface{}) error {
	// Create client with Unix socket transport for Firecracker API
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return net.Dial("unix", socketPath)
		},
	}
	
	client = &http.Client{
		Timeout:   10 * time.Second,
		Transport: transport,
	}

	// Serialize body
	var reqBody io.Reader
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to marshal body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonBody)
	}

	// Build request - use file:// URL scheme for Unix socket
	reqURL := "http://localhost" + path
	req, err := http.NewRequest(method, reqURL, reqBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	// Send request
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API request failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// GenerateRandomMAC generates a random MAC address
func GenerateRandomMAC() string {
	b := make([]byte, 6)
	_, err := rand.Read(b)
	if err != nil {
		return "02:00:00:00:00:00"
	}
	// Set the local bit
	b[0] |= 0x02
	return strings.ToUpper(hex.EncodeToString(b))
}
