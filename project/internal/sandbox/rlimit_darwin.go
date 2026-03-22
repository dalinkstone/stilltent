package vm

// applyLinuxCgroupLimits is a no-op on macOS — resource limits are advisory
// and enforced at the hypervisor framework level where supported.
func (rl *ResourceLimiter) applyLinuxCgroupLimits(name string, applied *AppliedLimits) error {
	// macOS uses Virtualization.framework resource controls
	// Limits are persisted and reported but not cgroup-enforced
	return nil
}

// cleanupLinuxCgroup is a no-op on macOS.
func (rl *ResourceLimiter) cleanupLinuxCgroup(name string) {}

// AssignProcess is a no-op on macOS.
func (rl *ResourceLimiter) AssignProcess(name string, pid int) error {
	return nil
}
