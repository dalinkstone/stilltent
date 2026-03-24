package vm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const cgroupBase = "/sys/fs/cgroup"

// cgroupPath returns the cgroup directory for a sandbox.
func cgroupPath(name string) string {
	return filepath.Join(cgroupBase, "tent.sandbox."+name)
}

// applyLinuxCgroupLimits sets up cgroups v2 resource limits for a sandbox.
func (rl *ResourceLimiter) applyLinuxCgroupLimits(name string, applied *AppliedLimits) error {
	cgDir := cgroupPath(name)

	// Create cgroup directory
	if err := os.MkdirAll(cgDir, 0o755); err != nil {
		return fmt.Errorf("failed to create cgroup dir: %w", err)
	}

	// Enable controllers
	controllers := "+cpu +memory +io +pids"
	_ = os.WriteFile(filepath.Join(cgroupBase, "cgroup.subtree_control"), []byte(controllers), 0o644)

	// CPU weight (cgroup v2 uses cpu.weight range 1-10000)
	if applied.CPUWeight > 0 {
		if err := writeCgroupFile(cgDir, "cpu.weight", strconv.Itoa(applied.CPUWeight)); err != nil {
			return fmt.Errorf("failed to set cpu.weight: %w", err)
		}
	}

	// CPU max (cpu.max format: "$MAX $PERIOD" in microseconds)
	if applied.CPUMaxPercent > 0 {
		period := 100000 // 100ms
		quota := (applied.CPUMaxPercent * period) / 100
		if quota < 1000 {
			quota = 1000
		}
		val := fmt.Sprintf("%d %d", quota, period)
		if err := writeCgroupFile(cgDir, "cpu.max", val); err != nil {
			return fmt.Errorf("failed to set cpu.max: %w", err)
		}
	}

	// Memory max
	if applied.MemoryMaxBytes > 0 {
		if err := writeCgroupFile(cgDir, "memory.max", strconv.FormatInt(applied.MemoryMaxBytes, 10)); err != nil {
			return fmt.Errorf("failed to set memory.max: %w", err)
		}
	}

	// Swap max
	if applied.SwapMaxBytes > 0 {
		if err := writeCgroupFile(cgDir, "memory.swap.max", strconv.FormatInt(applied.SwapMaxBytes, 10)); err != nil {
			return fmt.Errorf("failed to set memory.swap.max: %w", err)
		}
	} else {
		// Disable swap by default for VMs
		_ = writeCgroupFile(cgDir, "memory.swap.max", "0")
	}

	// PIDs max
	if applied.PidsMax > 0 {
		if err := writeCgroupFile(cgDir, "pids.max", strconv.Itoa(applied.PidsMax)); err != nil {
			return fmt.Errorf("failed to set pids.max: %w", err)
		}
	}

	// I/O limits (io.max format: "$MAJ:$MIN rbps=$READ wbps=$WRITE riops=$RIOPS wiops=$WIOPS")
	if applied.IOReadBPS > 0 || applied.IOWriteBPS > 0 || applied.IOReadIOPS > 0 || applied.IOWriteIOPS > 0 {
		// Use 253:0 as default device major:minor (virtio-blk)
		ioLine := "253:0"
		if applied.IOReadBPS > 0 {
			ioLine += fmt.Sprintf(" rbps=%d", applied.IOReadBPS)
		}
		if applied.IOWriteBPS > 0 {
			ioLine += fmt.Sprintf(" wbps=%d", applied.IOWriteBPS)
		}
		if applied.IOReadIOPS > 0 {
			ioLine += fmt.Sprintf(" riops=%d", applied.IOReadIOPS)
		}
		if applied.IOWriteIOPS > 0 {
			ioLine += fmt.Sprintf(" wiops=%d", applied.IOWriteIOPS)
		}
		_ = writeCgroupFile(cgDir, "io.max", ioLine)
	}

	return nil
}

// cleanupLinuxCgroup removes the cgroup directory for a sandbox.
func (rl *ResourceLimiter) cleanupLinuxCgroup(name string) {
	cgDir := cgroupPath(name)
	os.Remove(cgDir) // rmdir — only works if empty (processes moved out)
}

// AssignProcess moves a process into the sandbox's cgroup.
func (rl *ResourceLimiter) AssignProcess(name string, pid int) error {
	cgDir := cgroupPath(name)
	return writeCgroupFile(cgDir, "cgroup.procs", strconv.Itoa(pid))
}

func writeCgroupFile(cgDir, file, value string) error {
	return os.WriteFile(filepath.Join(cgDir, file), []byte(value), 0o644)
}
