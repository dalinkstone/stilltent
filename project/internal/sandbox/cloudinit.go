// Package vm provides cross-platform VM management operations.
// This file implements cloud-init compatible provisioning for sandbox VMs.
// It generates NoCloud data sources (user-data, meta-data, network-config)
// that can be attached as a secondary disk to automate first-boot setup.
package vm

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/dalinkstone/tent/pkg/models"
)

// CloudInitConfig defines the provisioning configuration for a sandbox.
type CloudInitConfig struct {
	// Hostname sets the guest hostname (defaults to sandbox name)
	Hostname string `yaml:"hostname,omitempty" json:"hostname,omitempty"`
	// Users to create in the guest
	Users []CloudInitUser `yaml:"users,omitempty" json:"users,omitempty"`
	// Packages to install on first boot
	Packages []string `yaml:"packages,omitempty" json:"packages,omitempty"`
	// RunCmds are shell commands to execute on first boot (in order)
	RunCmds []string `yaml:"runcmds,omitempty" json:"runcmds,omitempty"`
	// WriteFiles creates files in the guest filesystem
	WriteFiles []CloudInitFile `yaml:"write_files,omitempty" json:"write_files,omitempty"`
	// SSHAuthorizedKeys adds SSH public keys for the default user
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys,omitempty" json:"ssh_authorized_keys,omitempty"`
	// Timezone sets the guest timezone (e.g. "UTC", "America/New_York")
	Timezone string `yaml:"timezone,omitempty" json:"timezone,omitempty"`
	// Locale sets the guest locale (e.g. "en_US.UTF-8")
	Locale string `yaml:"locale,omitempty" json:"locale,omitempty"`
	// FinalMessage is displayed when cloud-init finishes
	FinalMessage string `yaml:"final_message,omitempty" json:"final_message,omitempty"`
	// PhoneHome sends a callback when provisioning completes
	PhoneHome *CloudInitPhoneHome `yaml:"phone_home,omitempty" json:"phone_home,omitempty"`
	// GrowPart grows partitions to fill available disk space
	GrowPart bool `yaml:"growpart,omitempty" json:"growpart,omitempty"`
	// PowerState controls what happens after provisioning (e.g. reboot)
	PowerState *CloudInitPowerState `yaml:"power_state,omitempty" json:"power_state,omitempty"`
}

// CloudInitUser defines a user to create in the guest.
type CloudInitUser struct {
	Name              string   `yaml:"name" json:"name"`
	Groups            string   `yaml:"groups,omitempty" json:"groups,omitempty"`
	Shell             string   `yaml:"shell,omitempty" json:"shell,omitempty"`
	Sudo              string   `yaml:"sudo,omitempty" json:"sudo,omitempty"`
	SSHAuthorizedKeys []string `yaml:"ssh_authorized_keys,omitempty" json:"ssh_authorized_keys,omitempty"`
	LockPasswd        bool     `yaml:"lock_passwd" json:"lock_passwd"`
}

// CloudInitFile defines a file to write in the guest.
type CloudInitFile struct {
	Path        string `yaml:"path" json:"path"`
	Content     string `yaml:"content" json:"content"`
	Permissions string `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Owner       string `yaml:"owner,omitempty" json:"owner,omitempty"`
	Encoding    string `yaml:"encoding,omitempty" json:"encoding,omitempty"`
	Append      bool   `yaml:"append,omitempty" json:"append,omitempty"`
}

// CloudInitPhoneHome sends a POST when provisioning completes.
type CloudInitPhoneHome struct {
	URL     string `yaml:"url" json:"url"`
	Post    string `yaml:"post,omitempty" json:"post,omitempty"`
	Tries   int    `yaml:"tries,omitempty" json:"tries,omitempty"`
}

// CloudInitPowerState controls post-provisioning power behavior.
type CloudInitPowerState struct {
	Mode      string `yaml:"mode" json:"mode"`           // "poweroff", "reboot", "halt"
	Message   string `yaml:"message,omitempty" json:"message,omitempty"`
	Timeout   int    `yaml:"timeout,omitempty" json:"timeout,omitempty"` // seconds
	Condition string `yaml:"condition,omitempty" json:"condition,omitempty"`
}

// CloudInitGenerator generates cloud-init NoCloud data source files.
type CloudInitGenerator struct {
	baseDir string
}

// NewCloudInitGenerator creates a new cloud-init generator.
func NewCloudInitGenerator(baseDir string) *CloudInitGenerator {
	return &CloudInitGenerator{baseDir: baseDir}
}

// GenerateForVM creates cloud-init data source files for a sandbox VM.
// Returns the path to the directory containing the generated files.
func (g *CloudInitGenerator) GenerateForVM(vmName string, vmConfig *models.VMConfig, ciConfig *CloudInitConfig) (string, error) {
	outputDir := g.cloudInitDir(vmName)
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", fmt.Errorf("cloudinit: failed to create directory: %w", err)
	}

	// Generate instance-id
	instanceID, err := generateInstanceID(vmName)
	if err != nil {
		return "", fmt.Errorf("cloudinit: failed to generate instance-id: %w", err)
	}

	// Write meta-data
	metaData := g.buildMetaData(vmName, instanceID, ciConfig)
	if err := os.WriteFile(filepath.Join(outputDir, "meta-data"), metaData, 0o644); err != nil {
		return "", fmt.Errorf("cloudinit: failed to write meta-data: %w", err)
	}

	// Write user-data
	userData, err := g.buildUserData(vmName, vmConfig, ciConfig)
	if err != nil {
		return "", fmt.Errorf("cloudinit: failed to build user-data: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "user-data"), userData, 0o644); err != nil {
		return "", fmt.Errorf("cloudinit: failed to write user-data: %w", err)
	}

	// Write network-config
	networkConfig := g.buildNetworkConfig(vmConfig)
	if err := os.WriteFile(filepath.Join(outputDir, "network-config"), networkConfig, 0o644); err != nil {
		return "", fmt.Errorf("cloudinit: failed to write network-config: %w", err)
	}

	return outputDir, nil
}

// CleanupForVM removes cloud-init data for a sandbox VM.
func (g *CloudInitGenerator) CleanupForVM(vmName string) error {
	dir := g.cloudInitDir(vmName)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// cloudInitDir returns the cloud-init data directory for a VM.
func (g *CloudInitGenerator) cloudInitDir(vmName string) string {
	return filepath.Join(g.baseDir, "vms", vmName, "cloud-init")
}

// buildMetaData generates the NoCloud meta-data file.
func (g *CloudInitGenerator) buildMetaData(vmName, instanceID string, ciConfig *CloudInitConfig) []byte {
	hostname := vmName
	if ciConfig != nil && ciConfig.Hostname != "" {
		hostname = ciConfig.Hostname
	}

	md := map[string]interface{}{
		"instance-id":    instanceID,
		"local-hostname": hostname,
	}

	data, _ := yaml.Marshal(md)
	return data
}

// buildUserData generates the NoCloud user-data file in cloud-config format.
func (g *CloudInitGenerator) buildUserData(vmName string, vmConfig *models.VMConfig, ciConfig *CloudInitConfig) ([]byte, error) {
	ud := make(map[string]interface{})

	hostname := vmName
	if ciConfig != nil && ciConfig.Hostname != "" {
		hostname = ciConfig.Hostname
	}
	ud["hostname"] = hostname
	ud["manage_etc_hosts"] = true

	// Users
	if ciConfig != nil && len(ciConfig.Users) > 0 {
		users := make([]map[string]interface{}, 0, len(ciConfig.Users))
		for _, u := range ciConfig.Users {
			user := map[string]interface{}{
				"name":        u.Name,
				"lock_passwd": u.LockPasswd,
			}
			if u.Groups != "" {
				user["groups"] = u.Groups
			}
			if u.Shell != "" {
				user["shell"] = u.Shell
			}
			if u.Sudo != "" {
				user["sudo"] = u.Sudo
			}
			if len(u.SSHAuthorizedKeys) > 0 {
				user["ssh_authorized_keys"] = u.SSHAuthorizedKeys
			}
			users = append(users, user)
		}
		ud["users"] = users
	} else {
		// Default: create a tent user with sudo access
		ud["users"] = []map[string]interface{}{
			{
				"name":        "tent",
				"groups":      "sudo",
				"shell":       "/bin/bash",
				"sudo":        "ALL=(ALL) NOPASSWD:ALL",
				"lock_passwd": true,
			},
		}
	}

	// SSH keys
	sshKeys := make([]string, 0)
	if ciConfig != nil && len(ciConfig.SSHAuthorizedKeys) > 0 {
		sshKeys = append(sshKeys, ciConfig.SSHAuthorizedKeys...)
	}
	if len(sshKeys) > 0 {
		ud["ssh_authorized_keys"] = sshKeys
	}

	// Packages
	if ciConfig != nil && len(ciConfig.Packages) > 0 {
		ud["packages"] = ciConfig.Packages
		ud["package_update"] = true
		ud["package_upgrade"] = false
	}

	// Write files — merge env vars from VMConfig
	writeFiles := make([]map[string]interface{}, 0)
	if ciConfig != nil {
		for _, f := range ciConfig.WriteFiles {
			wf := map[string]interface{}{
				"path":    f.Path,
				"content": f.Content,
			}
			if f.Permissions != "" {
				wf["permissions"] = f.Permissions
			}
			if f.Owner != "" {
				wf["owner"] = f.Owner
			}
			if f.Encoding != "" {
				wf["encoding"] = f.Encoding
			}
			if f.Append {
				wf["append"] = true
			}
			writeFiles = append(writeFiles, wf)
		}
	}

	// Inject environment variables from VMConfig as /etc/environment entries
	if len(vmConfig.Env) > 0 {
		var envLines strings.Builder
		for k, v := range vmConfig.Env {
			envLines.WriteString(fmt.Sprintf("%s=%s\n", k, v))
		}
		writeFiles = append(writeFiles, map[string]interface{}{
			"path":        "/etc/tent/environment",
			"content":     envLines.String(),
			"permissions": "0644",
			"owner":       "root:root",
		})
	}

	if len(writeFiles) > 0 {
		ud["write_files"] = writeFiles
	}

	// Run commands
	runcmds := make([]interface{}, 0)

	// If env vars were written, source them in profile
	if len(vmConfig.Env) > 0 {
		runcmds = append(runcmds, "mkdir -p /etc/tent")
		runcmds = append(runcmds, `echo 'set -a; [ -f /etc/tent/environment ] && . /etc/tent/environment; set +a' >> /etc/profile.d/tent-env.sh`)
		runcmds = append(runcmds, "chmod 644 /etc/profile.d/tent-env.sh")
	}

	if ciConfig != nil {
		for _, cmd := range ciConfig.RunCmds {
			runcmds = append(runcmds, cmd)
		}
	}

	if len(runcmds) > 0 {
		ud["runcmd"] = runcmds
	}

	// Timezone
	if ciConfig != nil && ciConfig.Timezone != "" {
		ud["timezone"] = ciConfig.Timezone
	}

	// Locale
	if ciConfig != nil && ciConfig.Locale != "" {
		ud["locale"] = ciConfig.Locale
	}

	// GrowPart
	if ciConfig != nil && ciConfig.GrowPart {
		ud["growpart"] = map[string]interface{}{
			"mode":    "auto",
			"devices": []string{"/"},
		}
		ud["resize_rootfs"] = true
	}

	// Final message
	if ciConfig != nil && ciConfig.FinalMessage != "" {
		ud["final_message"] = ciConfig.FinalMessage
	} else {
		ud["final_message"] = fmt.Sprintf("tent sandbox '%s' provisioned in $UPTIME seconds", vmName)
	}

	// Phone home
	if ciConfig != nil && ciConfig.PhoneHome != nil {
		ph := map[string]interface{}{
			"url": ciConfig.PhoneHome.URL,
		}
		if ciConfig.PhoneHome.Post != "" {
			ph["post"] = ciConfig.PhoneHome.Post
		}
		if ciConfig.PhoneHome.Tries > 0 {
			ph["tries"] = ciConfig.PhoneHome.Tries
		}
		ud["phone_home"] = ph
	}

	// Power state
	if ciConfig != nil && ciConfig.PowerState != nil {
		ps := map[string]interface{}{
			"mode": ciConfig.PowerState.Mode,
		}
		if ciConfig.PowerState.Message != "" {
			ps["message"] = ciConfig.PowerState.Message
		}
		if ciConfig.PowerState.Timeout > 0 {
			ps["timeout"] = ciConfig.PowerState.Timeout
		}
		if ciConfig.PowerState.Condition != "" {
			ps["condition"] = ciConfig.PowerState.Condition
		}
		ud["power_state"] = ps
	}

	data, err := yaml.Marshal(ud)
	if err != nil {
		return nil, fmt.Errorf("cloudinit: failed to marshal user-data: %w", err)
	}

	// cloud-config files must start with #cloud-config
	return append([]byte("#cloud-config\n"), data...), nil
}

// buildNetworkConfig generates a network-config v2 file.
func (g *CloudInitGenerator) buildNetworkConfig(vmConfig *models.VMConfig) []byte {
	// Use DHCP by default — the tent DHCP server handles IP assignment
	nc := map[string]interface{}{
		"version": 2,
		"ethernets": map[string]interface{}{
			"eth0": map[string]interface{}{
				"dhcp4": true,
			},
		},
	}

	data, _ := yaml.Marshal(nc)
	return data
}

// generateInstanceID creates a unique instance identifier.
func generateInstanceID(vmName string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	ts := time.Now().Unix()
	return fmt.Sprintf("tent-%s-%d-%s", vmName, ts, hex.EncodeToString(b)), nil
}

// BuildCloudInitISO creates a minimal ISO9660 image containing the cloud-init
// data source files. This ISO can be attached as a secondary drive to the VM.
// Uses a pure-Go ISO builder — no external tools required.
func (g *CloudInitGenerator) BuildCloudInitISO(vmName string) (string, error) {
	srcDir := g.cloudInitDir(vmName)
	isoPath := filepath.Join(g.baseDir, "vms", vmName, "cloud-init.iso")

	files := []string{"meta-data", "user-data", "network-config"}
	entries := make([]isoFileEntry, 0, len(files))

	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			return "", fmt.Errorf("cloudinit: failed to read %s: %w", name, err)
		}
		entries = append(entries, isoFileEntry{name: name, data: data})
	}

	iso, err := buildMinimalISO9660("cidata", entries)
	if err != nil {
		return "", fmt.Errorf("cloudinit: failed to build ISO: %w", err)
	}

	if err := os.WriteFile(isoPath, iso, 0o644); err != nil {
		return "", fmt.Errorf("cloudinit: failed to write ISO: %w", err)
	}

	return isoPath, nil
}

// isoFileEntry represents a file to include in the ISO image.
type isoFileEntry struct {
	name string
	data []byte
}

// buildMinimalISO9660 creates a minimal ISO9660 image with the given files.
// This is a pure-Go implementation that produces a valid NoCloud data source.
// The image uses ISO9660 Level 1 with the volume label set to identify it
// as a cloud-init data source (typically "cidata" or "CIDATA").
func buildMinimalISO9660(volumeLabel string, files []isoFileEntry) ([]byte, error) {
	const sectorSize = 2048

	// Calculate layout:
	// Sectors 0-15: System Area (unused, zeroed)
	// Sector 16: Primary Volume Descriptor
	// Sector 17: Volume Descriptor Set Terminator
	// Sector 18: Root Directory Record (path table + root dir entries)
	// Sector 19: Path Table (L)
	// Sector 20+: File data

	// Calculate file data sectors
	fileDataStart := 20
	fileLocations := make([]int, len(files))
	currentSector := fileDataStart
	for i, f := range files {
		fileLocations[i] = currentSector
		sectors := (len(f.data) + sectorSize - 1) / sectorSize
		if sectors == 0 {
			sectors = 1
		}
		currentSector += sectors
	}
	totalSectors := currentSector

	iso := make([]byte, totalSectors*sectorSize)

	// Primary Volume Descriptor (sector 16)
	pvd := iso[16*sectorSize : 17*sectorSize]
	pvd[0] = 1    // Type: Primary
	copy(pvd[1:6], "CD001") // Standard Identifier
	pvd[6] = 1    // Version

	// System Identifier (bytes 8-39)
	writeISOString(pvd[8:40], "LINUX")
	// Volume Identifier (bytes 40-71)
	writeISOString(pvd[40:72], strings.ToUpper(volumeLabel))

	// Volume Space Size (bytes 80-87, both-endian)
	putBothEndian32(pvd[80:88], uint32(totalSectors))

	// Volume Set Size (bytes 120-123)
	putBothEndian16(pvd[120:124], 1)
	// Volume Sequence Number (bytes 124-127)
	putBothEndian16(pvd[124:128], 1)
	// Logical Block Size (bytes 128-131)
	putBothEndian16(pvd[128:132], uint16(sectorSize))

	// Path Table Size (bytes 132-139)
	pathTableSize := uint32(10 + 0) // root entry only: 1(len) + 1(ext) + 4(loc) + 2(parent) + 1(name) + 1(pad)
	putBothEndian32(pvd[132:140], pathTableSize)

	// L Path Table Location (bytes 140-143)
	putLE32(pvd[140:144], uint32(19))
	// M Path Table Location (bytes 148-151)
	putBE32(pvd[148:152], uint32(19))

	// Root Directory Record (bytes 156-189)
	buildDirectoryRecord(pvd[156:190], 18, sectorSize, true)

	// Volume Set Identifier
	writeISOString(pvd[190:318], "")
	// Publisher Identifier
	writeISOString(pvd[318:446], "TENT")
	// Data Preparer Identifier
	writeISOString(pvd[446:574], "TENT CLOUD-INIT")
	// Application Identifier
	writeISOString(pvd[574:702], "TENT MICROVM SANDBOX")

	// Volume Creation Date (bytes 813-829)
	writeISODateTime(pvd[813:830])

	// File Structure Version
	pvd[881] = 1

	// Volume Descriptor Set Terminator (sector 17)
	vdst := iso[17*sectorSize : 18*sectorSize]
	vdst[0] = 255 // Type: Terminator
	copy(vdst[1:6], "CD001")
	vdst[6] = 1

	// Root Directory (sector 18)
	rootDir := iso[18*sectorSize : 19*sectorSize]
	offset := 0

	// "." entry (self)
	offset += writeDirEntry(rootDir[offset:], 18, sectorSize, "\x00")
	// ".." entry (parent)
	offset += writeDirEntry(rootDir[offset:], 18, sectorSize, "\x01")

	// File entries
	for i, f := range files {
		isoName := toISO9660Name(f.name)
		offset += writeFileEntry(rootDir[offset:], fileLocations[i], len(f.data), isoName)
	}

	// Path Table (sector 19)
	pathTable := iso[19*sectorSize : 20*sectorSize]
	pathTable[0] = 1    // Name length
	pathTable[1] = 0    // Extended attribute record length
	putLE32(pathTable[2:6], uint32(18)) // Location of extent
	putLE16(pathTable[6:8], 1) // Parent directory number
	pathTable[8] = 0x01 // Root directory name (single byte)

	// File data
	for i, f := range files {
		start := fileLocations[i] * sectorSize
		copy(iso[start:], f.data)
	}

	return iso, nil
}

// writeISOString writes a padded ISO string (space-filled).
func writeISOString(dst []byte, s string) {
	for i := range dst {
		dst[i] = 0x20 // space
	}
	copy(dst, []byte(s))
}

// writeISODateTime writes the current time in ISO9660 17-byte format.
func writeISODateTime(dst []byte) {
	t := time.Now().UTC()
	s := t.Format("20060102150405") + "00"
	copy(dst, []byte(s))
	dst[16] = 0 // UTC offset
}

// buildDirectoryRecord writes a root directory record.
func buildDirectoryRecord(dst []byte, location, size int, isRoot bool) {
	dst[0] = 34 // Length of directory record
	dst[1] = 0  // Extended attribute record length
	putBothEndian32(dst[2:10], uint32(location))  // Location of extent
	putBothEndian32(dst[10:18], uint32(size))      // Data length
	// Recording date (7 bytes at offset 18)
	t := time.Now().UTC()
	dst[18] = byte(t.Year() - 1900)
	dst[19] = byte(t.Month())
	dst[20] = byte(t.Day())
	dst[21] = byte(t.Hour())
	dst[22] = byte(t.Minute())
	dst[23] = byte(t.Second())
	dst[24] = 0 // UTC offset
	dst[25] = 2 // File flags: directory
	dst[26] = 0 // File unit size
	dst[27] = 0 // Interleave gap size
	putBothEndian16(dst[28:32], 1) // Volume sequence number
	dst[32] = 1 // Length of file identifier
	dst[33] = 0 // File identifier (root)
}

// writeDirEntry writes a "." or ".." directory entry, returns bytes written.
func writeDirEntry(dst []byte, location, size int, name string) int {
	recLen := 33 + len(name)
	if recLen%2 != 0 {
		recLen++
	}
	dst[0] = byte(recLen)
	dst[1] = 0
	putBothEndian32(dst[2:10], uint32(location))
	putBothEndian32(dst[10:18], uint32(size))
	t := time.Now().UTC()
	dst[18] = byte(t.Year() - 1900)
	dst[19] = byte(t.Month())
	dst[20] = byte(t.Day())
	dst[21] = byte(t.Hour())
	dst[22] = byte(t.Minute())
	dst[23] = byte(t.Second())
	dst[24] = 0
	dst[25] = 2 // directory
	dst[26] = 0
	dst[27] = 0
	putBothEndian16(dst[28:32], 1)
	dst[32] = byte(len(name))
	copy(dst[33:], []byte(name))
	return recLen
}

// writeFileEntry writes a file directory entry, returns bytes written.
func writeFileEntry(dst []byte, location, size int, name string) int {
	recLen := 33 + len(name)
	if recLen%2 != 0 {
		recLen++
	}
	dst[0] = byte(recLen)
	dst[1] = 0
	putBothEndian32(dst[2:10], uint32(location))
	putBothEndian32(dst[10:18], uint32(size))
	t := time.Now().UTC()
	dst[18] = byte(t.Year() - 1900)
	dst[19] = byte(t.Month())
	dst[20] = byte(t.Day())
	dst[21] = byte(t.Hour())
	dst[22] = byte(t.Minute())
	dst[23] = byte(t.Second())
	dst[24] = 0
	dst[25] = 0 // file (not directory)
	dst[26] = 0
	dst[27] = 0
	putBothEndian16(dst[28:32], 1)
	dst[32] = byte(len(name))
	copy(dst[33:], []byte(name))
	return recLen
}

// toISO9660Name converts a filename to ISO9660 Level 1 format.
func toISO9660Name(name string) string {
	// cloud-init expects lowercase filenames; use Rock Ridge-style names
	// For NoCloud, the names are case-insensitive, but we uppercase for Level 1
	upper := strings.ToUpper(name)
	// Replace hyphens with underscores for strict ISO9660
	upper = strings.ReplaceAll(upper, "-", "_")
	// Add version number
	if !strings.Contains(upper, ";") {
		upper += ";1"
	}
	return upper
}

// putBothEndian32 writes a uint32 in both little-endian and big-endian (8 bytes total).
func putBothEndian32(dst []byte, v uint32) {
	putLE32(dst[0:4], v)
	putBE32(dst[4:8], v)
}

// putBothEndian16 writes a uint16 in both little-endian and big-endian (4 bytes total).
func putBothEndian16(dst []byte, v uint16) {
	putLE16(dst[0:2], v)
	putBE16(dst[2:4], v)
}

// putLE32 writes a little-endian uint32.
func putLE32(dst []byte, v uint32) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
	dst[2] = byte(v >> 16)
	dst[3] = byte(v >> 24)
}

// putBE32 writes a big-endian uint32.
func putBE32(dst []byte, v uint32) {
	dst[0] = byte(v >> 24)
	dst[1] = byte(v >> 16)
	dst[2] = byte(v >> 8)
	dst[3] = byte(v)
}

// putLE16 writes a little-endian uint16.
func putLE16(dst []byte, v uint16) {
	dst[0] = byte(v)
	dst[1] = byte(v >> 8)
}

// putBE16 writes a big-endian uint16.
func putBE16(dst []byte, v uint16) {
	dst[0] = byte(v >> 8)
	dst[1] = byte(v)
}
