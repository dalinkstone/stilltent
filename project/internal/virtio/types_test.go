package virtio

import (
	"testing"
)

func TestBlockDevice(t *testing.T) {
	dev := &BlockDevice{
		deviceID: "block-1",
		Path:     "/test/disk.img",
		SizeMB:   1024,
		ReadOnly: false,
	}

	if dev.Type() != DeviceTypeBlock {
		t.Errorf("expected DeviceTypeBlock, got %v", dev.Type())
	}

	if dev.ID() != "block-1" {
		t.Errorf("expected block-1, got %v", dev.ID())
	}

	if err := dev.Start(); err != nil {
		t.Errorf("Start() returned error: %v", err)
	}

	if err := dev.Stop(); err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}

	config := map[string]string{
		"path": "/test/disk.img",
	}
	if err := dev.Configure(config); err != nil {
		t.Errorf("Configure() returned error: %v", err)
	}
}

func TestNetworkDevice(t *testing.T) {
	dev := &NetworkDevice{
		deviceID:  "net-1",
		TAPDevice: "tap0",
		MAC:       "02:00:00:00:00:01",
	}

	if dev.Type() != DeviceTypeNet {
		t.Errorf("expected DeviceTypeNet, got %v", dev.Type())
	}

	if dev.ID() != "net-1" {
		t.Errorf("expected net-1, got %v", dev.ID())
	}

	if err := dev.Start(); err != nil {
		t.Errorf("Start() returned error: %v", err)
	}

	if err := dev.Stop(); err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}

	config := map[string]string{
		"tap": "tap0",
		"mac": "02:00:00:00:00:01",
	}
	if err := dev.Configure(config); err != nil {
		t.Errorf("Configure() returned error: %v", err)
	}
}

func TestConsoleDevice(t *testing.T) {
	dev := &ConsoleDevice{
		deviceID: "console-1",
		TTY:      "/dev/ttyS0",
		LogFile:  "/tmp/console.log",
	}

	if dev.Type() != DeviceTypeConsole {
		t.Errorf("expected DeviceTypeConsole, got %v", dev.Type())
	}

	if dev.ID() != "console-1" {
		t.Errorf("expected console-1, got %v", dev.ID())
	}

	if err := dev.Start(); err != nil {
		t.Errorf("Start() returned error: %v", err)
	}

	if err := dev.Stop(); err != nil {
		t.Errorf("Stop() returned error: %v", err)
	}

	config := map[string]string{
		"tty":     "/dev/ttyS0",
		"logfile": "/tmp/console.log",
	}
	if err := dev.Configure(config); err != nil {
		t.Errorf("Configure() returned error: %v", err)
	}
}

func TestDeviceTypes(t *testing.T) {
	tests := []struct {
		deviceType DeviceType
		name       string
	}{
		{DeviceTypeInvalid, "invalid"},
		{DeviceTypeNet, "network"},
		{DeviceTypeBlock, "block"},
		{DeviceTypeConsole, "console"},
		{DeviceTypeRng, "rng"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify the enum values are defined
			if tt.deviceType == 0 && tt.name != "invalid" {
				t.Errorf("DeviceTypeInvalid should be 0")
			}
		})
	}
}
