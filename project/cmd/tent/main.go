package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "tent",
		Short: "tent - MicroVM management tool",
		Long: `tent is a command-line tool for creating, managing, and running microVMs
as lightweight, isolated development environments.

On macOS, tent uses Apple's Virtualization.framework. On Linux, tent uses KVM.
Sandboxes boot in seconds and provide full network isolation with fine-grained
egress control via allow-lists.

Quick start:
  tent create mybox --from ubuntu:22.04     Create a sandbox from an OCI image
  tent start mybox                          Boot the sandbox
  tent shell mybox                          Open an interactive shell
  tent exec mybox -- ls /                   Run a command inside the sandbox
  tent stop mybox                           Gracefully shut down
  tent destroy mybox                        Remove the sandbox and its resources

One-liner workflow:
  tent run --from ubuntu:22.04 --rm -- echo hello

Configuration is stored under ~/.tent by default. Override with the
TENT_BASE_DIR environment variable.

Use "tent <command> --help" for more information about a command.`,
		Run: func(cmd *cobra.Command, args []string) {
			_ = cmd.Usage()
		},
	}

	// -- Command groups for organized help output --
	rootCmd.AddGroup(
		&cobra.Group{ID: "lifecycle", Title: "Sandbox Lifecycle:"},
		&cobra.Group{ID: "access", Title: "Sandbox Access:"},
		&cobra.Group{ID: "inspection", Title: "Inspection & Monitoring:"},
		&cobra.Group{ID: "images", Title: "Image Management:"},
		&cobra.Group{ID: "storage", Title: "Storage & Data:"},
		&cobra.Group{ID: "networking", Title: "Networking:"},
		&cobra.Group{ID: "orchestration", Title: "Orchestration:"},
		&cobra.Group{ID: "advanced", Title: "Advanced:"},
		&cobra.Group{ID: "system", Title: "System & Configuration:"},
	)

	// Lifecycle commands
	create := createCmd()
	create.GroupID = "lifecycle"
	rootCmd.AddCommand(create)

	start := startCmd()
	start.GroupID = "lifecycle"
	rootCmd.AddCommand(start)

	stop := stopCmd()
	stop.GroupID = "lifecycle"
	rootCmd.AddCommand(stop)

	restart := restartCmd()
	restart.GroupID = "lifecycle"
	rootCmd.AddCommand(restart)

	destroy := destroyCmd()
	destroy.GroupID = "lifecycle"
	rootCmd.AddCommand(destroy)

	run := runCmd()
	run.GroupID = "lifecycle"
	rootCmd.AddCommand(run)

	clone := cloneCmd()
	clone.GroupID = "lifecycle"
	rootCmd.AddCommand(clone)

	pause := pauseCmd()
	pause.GroupID = "lifecycle"
	rootCmd.AddCommand(pause)

	unpause := unpauseCmd()
	unpause.GroupID = "lifecycle"
	rootCmd.AddCommand(unpause)

	rename := renameCmd()
	rename.GroupID = "lifecycle"
	rootCmd.AddCommand(rename)

	wait := waitCmd()
	wait.GroupID = "lifecycle"
	rootCmd.AddCommand(wait)

	signal := signalCmd()
	signal.GroupID = "lifecycle"
	rootCmd.AddCommand(signal)

	// Access commands
	shell := shellCmd()
	shell.GroupID = "access"
	rootCmd.AddCommand(shell)

	ssh := sshCmd()
	ssh.GroupID = "access"
	rootCmd.AddCommand(ssh)

	ex := execCmd()
	ex.GroupID = "access"
	rootCmd.AddCommand(ex)

	attach := attachCmd()
	attach.GroupID = "access"
	rootCmd.AddCommand(attach)

	cp := cpCmd()
	cp.GroupID = "access"
	rootCmd.AddCommand(cp)

	// Inspection and monitoring commands
	list := listCmd()
	list.GroupID = "inspection"
	rootCmd.AddCommand(list)

	status := statusCmd()
	status.GroupID = "inspection"
	rootCmd.AddCommand(status)

	logs := logsCmd()
	logs.GroupID = "inspection"
	rootCmd.AddCommand(logs)

	inspect := inspectCmd()
	inspect.GroupID = "inspection"
	rootCmd.AddCommand(inspect)

	stats := statsCmd()
	stats.GroupID = "inspection"
	rootCmd.AddCommand(stats)

	top := topCmd()
	top.GroupID = "inspection"
	rootCmd.AddCommand(top)

	events := eventsCmd()
	events.GroupID = "inspection"
	rootCmd.AddCommand(events)

	health := healthCmd()
	health.GroupID = "inspection"
	rootCmd.AddCommand(health)

	diff := diffCmd()
	diff.GroupID = "inspection"
	rootCmd.AddCommand(diff)

	watch := watchCmd()
	watch.GroupID = "inspection"
	rootCmd.AddCommand(watch)

	history := historyCmd()
	history.GroupID = "inspection"
	rootCmd.AddCommand(history)

	metrics := metricsCmd()
	metrics.GroupID = "inspection"
	rootCmd.AddCommand(metrics)

	// Image management commands
	img := imageCmd()
	img.GroupID = "images"
	rootCmd.AddCommand(img)

	commit := commitCmd()
	commit.GroupID = "images"
	rootCmd.AddCommand(commit)

	registry := registryCmd()
	registry.GroupID = "images"
	rootCmd.AddCommand(registry)

	// Storage and data commands
	snapshot := snapshotCmd()
	snapshot.GroupID = "storage"
	rootCmd.AddCommand(snapshot)

	checkpoint := checkpointCmd()
	checkpoint.GroupID = "storage"
	rootCmd.AddCommand(checkpoint)

	backup := backupCmd()
	backup.GroupID = "storage"
	rootCmd.AddCommand(backup)

	mount := mountCmd()
	mount.GroupID = "storage"
	rootCmd.AddCommand(mount)

	volume := volumeCmd()
	volume.GroupID = "storage"
	rootCmd.AddCommand(volume)

	disk := diskCmd()
	disk.GroupID = "storage"
	rootCmd.AddCommand(disk)

	export := exportCmd()
	export.GroupID = "storage"
	rootCmd.AddCommand(export)

	imp := importCmd()
	imp.GroupID = "storage"
	rootCmd.AddCommand(imp)

	// Networking commands
	net := networkCmd()
	net.GroupID = "networking"
	rootCmd.AddCommand(net)

	tunnel := tunnelCmd()
	tunnel.GroupID = "networking"
	rootCmd.AddCommand(tunnel)

	port := portCmd()
	port.GroupID = "networking"
	rootCmd.AddCommand(port)

	// Orchestration commands
	comp := composeCmd()
	comp.GroupID = "orchestration"
	rootCmd.AddCommand(comp)

	pool := poolCmd()
	pool.GroupID = "orchestration"
	rootCmd.AddCommand(pool)

	tmpl := templateCmd()
	tmpl.GroupID = "orchestration"
	rootCmd.AddCommand(tmpl)

	group := groupCmd()
	group.GroupID = "orchestration"
	rootCmd.AddCommand(group)

	workspace := workspaceCmd()
	workspace.GroupID = "orchestration"
	rootCmd.AddCommand(workspace)

	dep := dependCmd()
	dep.GroupID = "orchestration"
	rootCmd.AddCommand(dep)

	sched := scheduleCmd()
	sched.GroupID = "orchestration"
	rootCmd.AddCommand(sched)

	// Advanced commands
	label := labelCmd()
	label.GroupID = "advanced"
	rootCmd.AddCommand(label)

	env := envCmd()
	env.GroupID = "advanced"
	rootCmd.AddCommand(env)

	update := updateCmd()
	update.GroupID = "advanced"
	rootCmd.AddCommand(update)

	prune := pruneCmd()
	prune.GroupID = "advanced"
	rootCmd.AddCommand(prune)

	rollback := rollbackCmd()
	rollback.GroupID = "advanced"
	rootCmd.AddCommand(rollback)

	migrate := migrateCmd()
	migrate.GroupID = "advanced"
	rootCmd.AddCommand(migrate)

	provision := provisionCmd()
	provision.GroupID = "advanced"
	rootCmd.AddCommand(provision)

	secret := secretCmd()
	secret.GroupID = "advanced"
	rootCmd.AddCommand(secret)

	scan := scanCmd()
	scan.GroupID = "advanced"
	rootCmd.AddCommand(scan)

	secProfile := secProfileCmd()
	secProfile.GroupID = "advanced"
	rootCmd.AddCommand(secProfile)

	device := deviceCmd()
	device.GroupID = "advanced"
	rootCmd.AddCommand(device)

	ttl := ttlCmd()
	ttl.GroupID = "advanced"
	rootCmd.AddCommand(ttl)

	lock := lockCmd()
	lock.GroupID = "advanced"
	rootCmd.AddCommand(lock)

	resources := resourcesCmd()
	resources.GroupID = "advanced"
	rootCmd.AddCommand(resources)

	quota := quotaCmd()
	quota.GroupID = "advanced"
	rootCmd.AddCommand(quota)

	link := linkCmd()
	link.GroupID = "advanced"
	rootCmd.AddCommand(link)

	// System and configuration commands
	sys := systemCmd()
	sys.GroupID = "system"
	rootCmd.AddCommand(sys)

	cfg := configCmd()
	cfg.GroupID = "system"
	rootCmd.AddCommand(cfg)

	kernel := kernelCmd()
	kernel.GroupID = "system"
	rootCmd.AddCommand(kernel)

	initC := initCmd()
	initC.GroupID = "system"
	rootCmd.AddCommand(initC)

	ctx := contextCmd()
	ctx.GroupID = "system"
	rootCmd.AddCommand(ctx)

	debug := debugCmd()
	debug.GroupID = "system"
	rootCmd.AddCommand(debug)

	api := apiCmd()
	api.GroupID = "system"
	rootCmd.AddCommand(api)

	daemon := daemonCmd()
	daemon.GroupID = "system"
	rootCmd.AddCommand(daemon)

	plugin := pluginCmd()
	plugin.GroupID = "system"
	rootCmd.AddCommand(plugin)

	bench := benchCmd()
	bench.GroupID = "system"
	rootCmd.AddCommand(bench)

	audit := auditCmd()
	audit.GroupID = "system"
	rootCmd.AddCommand(audit)

	webhook := webhookCmd()
	webhook.GroupID = "system"
	rootCmd.AddCommand(webhook)

	replay := replayCmd()
	replay.GroupID = "system"
	rootCmd.AddCommand(replay)

	usage := usageCmd()
	usage.GroupID = "system"
	rootCmd.AddCommand(usage)

	version := versionCmd()
	version.GroupID = "system"
	rootCmd.AddCommand(version)

	completion := completionCmd()
	completion.GroupID = "system"
	rootCmd.AddCommand(completion)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
