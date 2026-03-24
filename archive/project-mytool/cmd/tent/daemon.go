package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

// DaemonConfig holds daemon runtime configuration.
type DaemonConfig struct {
	PidFile       string
	ListenAddr    string
	SocketPath    string
	PollInterval  time.Duration
	LogFile       string
	MaxRestarts   int
	RestartDelay  time.Duration
}

// DaemonState tracks per-sandbox restart state.
type DaemonState struct {
	mu           sync.Mutex
	restartCount map[string]int
	lastRestart  map[string]time.Time
}

func newDaemonState() *DaemonState {
	return &DaemonState{
		restartCount: make(map[string]int),
		lastRestart:  make(map[string]time.Time),
	}
}

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run tent as a background daemon with health monitoring and auto-restart",
		Long: `Run tent as a long-running daemon process that monitors sandbox health,
enforces restart policies, and optionally serves the REST API.

The daemon periodically polls all sandboxes and:
  - Restarts sandboxes with restart_policy "always" when they stop
  - Restarts sandboxes with restart_policy "on-failure" on non-zero exit
  - Writes a PID file for process management
  - Exposes an optional HTTP API for remote control

Examples:
  tent daemon start
  tent daemon start --listen :8080
  tent daemon start --poll-interval 10s
  tent daemon status
  tent daemon stop`,
	}

	cmd.AddCommand(daemonStartCmd())
	cmd.AddCommand(daemonStatusCmd())
	cmd.AddCommand(daemonStopCmd())

	return cmd
}

func daemonStartCmd() *cobra.Command {
	var (
		listenAddr   string
		socketPath   string
		pollInterval string
		logFile      string
		maxRestarts  int
		restartDelay string
		foreground   bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the tent daemon",
		Long: `Start the tent daemon process. The daemon monitors all sandboxes and
enforces restart policies. It can also serve the REST API.

By default, the daemon runs in the foreground (useful for debugging and
container deployments). Use process managers like launchd (macOS) or
systemd (Linux) for background operation.

Examples:
  tent daemon start
  tent daemon start --listen :8080
  tent daemon start --poll-interval 5s --max-restarts 10
  tent daemon start --log /var/log/tent-daemon.log`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			interval, err := time.ParseDuration(pollInterval)
			if err != nil {
				return fmt.Errorf("invalid poll interval: %w", err)
			}

			delay, err := time.ParseDuration(restartDelay)
			if err != nil {
				return fmt.Errorf("invalid restart delay: %w", err)
			}

			baseDir := getBaseDir()
			pidFile := filepath.Join(baseDir, "daemon.pid")

			// Check if daemon is already running
			if running, pid := isDaemonRunning(pidFile); running {
				return fmt.Errorf("daemon already running (pid %d)", pid)
			}

			cfg := &DaemonConfig{
				PidFile:      pidFile,
				ListenAddr:   listenAddr,
				SocketPath:   socketPath,
				PollInterval: interval,
				LogFile:      logFile,
				MaxRestarts:  maxRestarts,
				RestartDelay: delay,
			}

			_ = foreground // always foreground for now
			return runDaemon(baseDir, cfg)
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", "", "HTTP API listen address (e.g., :8080)")
	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix socket path for API")
	cmd.Flags().StringVar(&pollInterval, "poll-interval", "15s", "How often to check sandbox health")
	cmd.Flags().StringVar(&logFile, "log", "", "Log file path (default: stderr)")
	cmd.Flags().IntVar(&maxRestarts, "max-restarts", 5, "Max restart attempts per sandbox before giving up (0 = unlimited)")
	cmd.Flags().StringVar(&restartDelay, "restart-delay", "3s", "Delay between restart attempts")
	cmd.Flags().BoolVar(&foreground, "foreground", true, "Run in foreground (default)")

	return cmd
}

func daemonStatusCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			pidFile := filepath.Join(baseDir, "daemon.pid")

			running, pid := isDaemonRunning(pidFile)

			status := struct {
				Running bool   `json:"running"`
				PID     int    `json:"pid,omitempty"`
				PIDFile string `json:"pid_file"`
			}{
				Running: running,
				PID:     pid,
				PIDFile: pidFile,
			}

			if jsonOutput {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(status)
			}

			if running {
				fmt.Printf("Daemon is running (pid %d)\n", pid)
				fmt.Printf("PID file: %s\n", pidFile)
			} else {
				fmt.Println("Daemon is not running")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")
	return cmd
}

func daemonStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			baseDir := getBaseDir()
			pidFile := filepath.Join(baseDir, "daemon.pid")

			running, pid := isDaemonRunning(pidFile)
			if !running {
				fmt.Println("Daemon is not running")
				return nil
			}

			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("failed to find process %d: %w", pid, err)
			}

			// Send SIGTERM for graceful shutdown
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				// Process may have already exited
				os.Remove(pidFile)
				fmt.Println("Daemon process not found, cleaned up PID file")
				return nil
			}

			fmt.Printf("Sent SIGTERM to daemon (pid %d)\n", pid)

			// Wait briefly for shutdown
			for i := 0; i < 10; i++ {
				time.Sleep(500 * time.Millisecond)
				if r, _ := isDaemonRunning(pidFile); !r {
					fmt.Println("Daemon stopped")
					return nil
				}
			}

			fmt.Println("Daemon is still shutting down (sent SIGTERM)")
			return nil
		},
	}

	return cmd
}

func runDaemon(baseDir string, cfg *DaemonConfig) error {
	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return fmt.Errorf("failed to create base directory: %w", err)
	}

	// Write PID file
	pid := os.Getpid()
	if err := os.WriteFile(cfg.PidFile, []byte(strconv.Itoa(pid)), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}
	defer os.Remove(cfg.PidFile)

	// Set up log file if specified
	if cfg.LogFile != "" {
		logDir := filepath.Dir(cfg.LogFile)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		defer f.Close()
		os.Stderr = f
		os.Stdout = f
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	daemonState := newDaemonState()

	fmt.Fprintf(os.Stderr, "[%s] tent daemon starting (pid %d, poll every %s)\n",
		time.Now().Format(time.RFC3339), pid, cfg.PollInterval)

	// Start HTTP API server if configured
	var httpServer *http.Server
	if cfg.ListenAddr != "" || cfg.SocketPath != "" {
		mux := buildDaemonAPI(baseDir, daemonState)
		httpServer = &http.Server{Handler: mux}

		go func() {
			var listener net.Listener
			var err error

			if cfg.SocketPath != "" {
				os.Remove(cfg.SocketPath)
				listener, err = net.Listen("unix", cfg.SocketPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%s] failed to listen on socket %s: %v\n",
						time.Now().Format(time.RFC3339), cfg.SocketPath, err)
					return
				}
				fmt.Fprintf(os.Stderr, "[%s] API listening on unix://%s\n",
					time.Now().Format(time.RFC3339), cfg.SocketPath)
			} else {
				listener, err = net.Listen("tcp", cfg.ListenAddr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "[%s] failed to listen on %s: %v\n",
						time.Now().Format(time.RFC3339), cfg.ListenAddr, err)
					return
				}
				fmt.Fprintf(os.Stderr, "[%s] API listening on %s\n",
					time.Now().Format(time.RFC3339), cfg.ListenAddr)
			}

			if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "[%s] API server error: %v\n",
					time.Now().Format(time.RFC3339), err)
			}
		}()
	}

	// Main monitoring loop
	ticker := time.NewTicker(cfg.PollInterval)
	defer ticker.Stop()

	// Run initial check immediately
	monitorSandboxes(baseDir, cfg, daemonState)

	for {
		select {
		case <-ticker.C:
			monitorSandboxes(baseDir, cfg, daemonState)
		case sig := <-sigCh:
			fmt.Fprintf(os.Stderr, "[%s] received %s, shutting down\n",
				time.Now().Format(time.RFC3339), sig)
			cancel()

			if httpServer != nil {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				httpServer.Shutdown(shutdownCtx)
				shutdownCancel()
			}

			fmt.Fprintf(os.Stderr, "[%s] daemon stopped\n", time.Now().Format(time.RFC3339))
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// monitorSandboxes checks all sandboxes and enforces restart policies.
func monitorSandboxes(baseDir string, cfg *DaemonConfig, ds *DaemonState) {
	hvBackend, err := vm.NewPlatformBackend(baseDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] monitor: failed to create backend: %v\n",
			time.Now().Format(time.RFC3339), err)
		return
	}

	manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] monitor: failed to create manager: %v\n",
			time.Now().Format(time.RFC3339), err)
		return
	}

	if err := manager.Setup(); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] monitor: setup failed: %v\n",
			time.Now().Format(time.RFC3339), err)
		return
	}

	sandboxes, err := manager.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] monitor: failed to list sandboxes: %v\n",
			time.Now().Format(time.RFC3339), err)
		return
	}

	for _, sb := range sandboxes {
		shouldRestart := false

		switch sb.RestartPolicy {
		case models.RestartPolicyAlways:
			if sb.Status == models.VMStatusStopped || sb.Status == models.VMStatusError {
				shouldRestart = true
			}
		case models.RestartPolicyOnFailure:
			if sb.Status == models.VMStatusError {
				shouldRestart = true
			}
		default:
			continue
		}

		if !shouldRestart {
			continue
		}

		ds.mu.Lock()
		count := ds.restartCount[sb.Name]
		lastRestart := ds.lastRestart[sb.Name]
		ds.mu.Unlock()

		// Check max restart limit
		if cfg.MaxRestarts > 0 && count >= cfg.MaxRestarts {
			// Reset counter if enough time has passed (10 minutes)
			if time.Since(lastRestart) > 10*time.Minute {
				ds.mu.Lock()
				ds.restartCount[sb.Name] = 0
				count = 0
				ds.mu.Unlock()
			} else {
				fmt.Fprintf(os.Stderr, "[%s] monitor: %s exceeded max restarts (%d), waiting for cooldown\n",
					time.Now().Format(time.RFC3339), sb.Name, cfg.MaxRestarts)
				continue
			}
		}

		// Enforce restart delay
		if !lastRestart.IsZero() && time.Since(lastRestart) < cfg.RestartDelay {
			continue
		}

		fmt.Fprintf(os.Stderr, "[%s] monitor: restarting %s (policy=%s, status=%s, attempt=%d)\n",
			time.Now().Format(time.RFC3339), sb.Name, sb.RestartPolicy, sb.Status, count+1)

		if err := manager.Start(sb.Name); err != nil {
			fmt.Fprintf(os.Stderr, "[%s] monitor: failed to restart %s: %v\n",
				time.Now().Format(time.RFC3339), sb.Name, err)
		} else {
			fmt.Fprintf(os.Stderr, "[%s] monitor: successfully restarted %s\n",
				time.Now().Format(time.RFC3339), sb.Name)
		}

		ds.mu.Lock()
		ds.restartCount[sb.Name] = count + 1
		ds.lastRestart[sb.Name] = time.Now()
		ds.mu.Unlock()
	}
}

// buildDaemonAPI creates the HTTP mux for the daemon's embedded API.
func buildDaemonAPI(baseDir string, ds *DaemonState) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/daemon/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		ds.mu.Lock()
		restarts := make(map[string]int, len(ds.restartCount))
		for k, v := range ds.restartCount {
			restarts[k] = v
		}
		ds.mu.Unlock()

		resp := struct {
			Status   string         `json:"status"`
			PID      int            `json:"pid"`
			Uptime   string         `json:"uptime"`
			Restarts map[string]int `json:"restarts"`
		}{
			Status:   "running",
			PID:      os.Getpid(),
			Restarts: restarts,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	mux.HandleFunc("/v1/daemon/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		hvBackend, err := vm.NewPlatformBackend(baseDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := manager.Setup(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		sandboxes, err := manager.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sandboxes)
	})

	mux.HandleFunc("/v1/daemon/restart/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/v1/daemon/restart/")
		if name == "" {
			http.Error(w, "sandbox name required", http.StatusBadRequest)
			return
		}

		hvBackend, err := vm.NewPlatformBackend(baseDir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := manager.Setup(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Reset restart counter for this sandbox
		ds.mu.Lock()
		ds.restartCount[name] = 0
		ds.mu.Unlock()

		if err := manager.Start(name); err != nil {
			http.Error(w, fmt.Sprintf("failed to restart %s: %v", name, err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "restarted",
			"sandbox": name,
		})
	})

	mux.HandleFunc("/v1/daemon/reset-restarts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(r.URL.Path, "/v1/daemon/reset-restarts/")
		if name == "" {
			// Reset all
			ds.mu.Lock()
			ds.restartCount = make(map[string]int)
			ds.lastRestart = make(map[string]time.Time)
			ds.mu.Unlock()

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "all restart counters reset"})
			return
		}

		ds.mu.Lock()
		delete(ds.restartCount, name)
		delete(ds.lastRestart, name)
		ds.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status":  "restart counter reset",
			"sandbox": name,
		})
	})

	return mux
}

// isDaemonRunning checks if a daemon process is active via PID file.
func isDaemonRunning(pidFile string) (bool, int) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return false, 0
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return false, 0
	}

	// Check if process exists by sending signal 0
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}

	err = proc.Signal(syscall.Signal(0))
	if err != nil {
		// Process not running, clean up stale PID file
		os.Remove(pidFile)
		return false, 0
	}

	return true, pid
}
