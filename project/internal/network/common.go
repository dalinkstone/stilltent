// Package network provides cross-platform networking for microVMs
package network

// NetworkResource represents a network resource
type NetworkResource struct {
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	IP         string   `json:"ip"`
	Interfaces []string `json:"interfaces,omitempty"`
}
