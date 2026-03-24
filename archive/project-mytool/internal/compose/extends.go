package compose

import "fmt"

// resolveExtends processes the "extends" field on each sandbox, merging
// configuration from the referenced parent sandbox. The child's explicitly
// set fields take precedence over the parent's. Extends chains are supported
// (A extends B extends C) but cycles are detected and rejected.
func (c *ComposeConfig) resolveExtends() error {
	if c.Sandboxes == nil {
		return nil
	}

	// Check if any sandbox uses extends
	hasExtends := false
	for _, sb := range c.Sandboxes {
		if sb.Extends != "" {
			hasExtends = true
			break
		}
	}
	if !hasExtends {
		return nil
	}

	// Resolve in order, tracking visited nodes to detect cycles
	resolved := make(map[string]bool)
	resolving := make(map[string]bool)

	var resolve func(name string) error
	resolve = func(name string) error {
		if resolved[name] {
			return nil
		}
		if resolving[name] {
			return fmt.Errorf("extends cycle detected involving sandbox %q", name)
		}

		sb := c.Sandboxes[name]
		if sb == nil {
			return fmt.Errorf("sandbox %q not found", name)
		}

		if sb.Extends == "" {
			resolved[name] = true
			return nil
		}

		// Validate the extends reference
		parent, ok := c.Sandboxes[sb.Extends]
		if !ok {
			return fmt.Errorf("sandbox %q extends unknown sandbox %q", name, sb.Extends)
		}
		if sb.Extends == name {
			return fmt.Errorf("sandbox %q cannot extend itself", name)
		}

		// Resolve the parent first (handles chained extends)
		resolving[name] = true
		if err := resolve(sb.Extends); err != nil {
			return err
		}
		delete(resolving, name)

		// Merge parent into child (child takes precedence)
		mergeSandboxConfig(sb, parent)

		// Clear extends after resolution
		sb.Extends = ""
		resolved[name] = true
		return nil
	}

	for name := range c.Sandboxes {
		if err := resolve(name); err != nil {
			return err
		}
	}

	return nil
}

// mergeSandboxConfig merges parent config into child. The child's explicitly
// set fields are preserved; only zero-value/nil/empty fields inherit from parent.
func mergeSandboxConfig(child, parent *SandboxConfig) {
	// Scalar fields: inherit if child has zero value
	if child.From == "" {
		child.From = parent.From
	}
	if child.VCPUs == 0 {
		child.VCPUs = parent.VCPUs
	}
	if child.MemoryMB == 0 {
		child.MemoryMB = parent.MemoryMB
	}
	if child.DiskGB == 0 {
		child.DiskGB = parent.DiskGB
	}
	if child.RestartPolicy == "" {
		child.RestartPolicy = parent.RestartPolicy
	}

	// Network: inherit if child has no network config
	if child.Network == nil && parent.Network != nil {
		netCopy := *parent.Network
		if parent.Network.Allow != nil {
			netCopy.Allow = make([]string, len(parent.Network.Allow))
			copy(netCopy.Allow, parent.Network.Allow)
		}
		if parent.Network.Deny != nil {
			netCopy.Deny = make([]string, len(parent.Network.Deny))
			copy(netCopy.Deny, parent.Network.Deny)
		}
		child.Network = &netCopy
	}

	// Env: merge maps (child keys take precedence)
	if len(parent.Env) > 0 {
		if child.Env == nil {
			child.Env = make(map[string]string)
		}
		for k, v := range parent.Env {
			if _, exists := child.Env[k]; !exists {
				child.Env[k] = v
			}
		}
	}

	// Slices: inherit if child has empty slice
	if len(child.Mounts) == 0 && len(parent.Mounts) > 0 {
		child.Mounts = make([]Mount, len(parent.Mounts))
		copy(child.Mounts, parent.Mounts)
	}
	if len(child.Volumes) == 0 && len(parent.Volumes) > 0 {
		child.Volumes = make([]VolumeMount, len(parent.Volumes))
		copy(child.Volumes, parent.Volumes)
	}
	if len(child.EnvFile) == 0 && len(parent.EnvFile) > 0 {
		child.EnvFile = make([]string, len(parent.EnvFile))
		copy(child.EnvFile, parent.EnvFile)
	}

	// Health check: inherit if child has none
	if child.HealthCheck == nil && parent.HealthCheck != nil {
		hcCopy := *parent.HealthCheck
		if parent.HealthCheck.Command != nil {
			hcCopy.Command = make([]string, len(parent.HealthCheck.Command))
			copy(hcCopy.Command, parent.HealthCheck.Command)
		}
		child.HealthCheck = &hcCopy
	}

	// Hooks: inherit if child has none
	if child.Hooks == nil && parent.Hooks != nil {
		hooksCopy := *parent.Hooks
		if parent.Hooks.PostStart != nil {
			hooksCopy.PostStart = make([]string, len(parent.Hooks.PostStart))
			copy(hooksCopy.PostStart, parent.Hooks.PostStart)
		}
		if parent.Hooks.PreStop != nil {
			hooksCopy.PreStop = make([]string, len(parent.Hooks.PreStop))
			copy(hooksCopy.PreStop, parent.Hooks.PreStop)
		}
		if parent.Hooks.PostCreate != nil {
			hooksCopy.PostCreate = make([]string, len(parent.Hooks.PostCreate))
			copy(hooksCopy.PostCreate, parent.Hooks.PostCreate)
		}
		if parent.Hooks.PreDestroy != nil {
			hooksCopy.PreDestroy = make([]string, len(parent.Hooks.PreDestroy))
			copy(hooksCopy.PreDestroy, parent.Hooks.PreDestroy)
		}
		child.Hooks = &hooksCopy
	}

	// Watch: inherit if child has none
	if child.Watch == nil && parent.Watch != nil {
		watchCopy := *parent.Watch
		if parent.Watch.Paths != nil {
			watchCopy.Paths = make([]string, len(parent.Watch.Paths))
			copy(watchCopy.Paths, parent.Watch.Paths)
		}
		if parent.Watch.Ignore != nil {
			watchCopy.Ignore = make([]string, len(parent.Watch.Ignore))
			copy(watchCopy.Ignore, parent.Watch.Ignore)
		}
		child.Watch = &watchCopy
	}

	// Note: DependsOn and Profiles are NOT inherited — these are
	// identity/relationship fields specific to the child sandbox.
}
