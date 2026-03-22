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
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dalinkstone/tent/internal/image"
	vm "github.com/dalinkstone/tent/internal/sandbox"
	"github.com/dalinkstone/tent/pkg/models"
)

func apiCmd() *cobra.Command {
	var (
		listenAddr string
		socketPath string
		readonly   bool
	)

	cmd := &cobra.Command{
		Use:   "api",
		Short: "Start the REST API server for programmatic sandbox control",
		Long: `Start an HTTP API server that exposes sandbox management over REST endpoints.
Useful for AI agent integration, automation, and building custom UIs.

The API can listen on a TCP address or a Unix domain socket.

Examples:
  tent api --listen :8080
  tent api --listen 127.0.0.1:9090
  tent api --socket /tmp/tent.sock
  tent api --listen :8080 --readonly`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if listenAddr == "" && socketPath == "" {
				listenAddr = "127.0.0.1:8080"
			}

			baseDir := getBaseDir()

			hvBackend, err := vm.NewPlatformBackend(baseDir)
			if err != nil {
				return fmt.Errorf("failed to create hypervisor backend: %w", err)
			}

			manager, err := vm.NewManager(baseDir, nil, hvBackend, nil, nil)
			if err != nil {
				return fmt.Errorf("failed to create VM manager: %w", err)
			}

			if err := manager.Setup(); err != nil {
				return fmt.Errorf("failed to setup VM manager: %w", err)
			}

			srv := &apiServer{
				manager:  manager,
				baseDir:  baseDir,
				readonly: readonly,
			}

			mux := srv.routes()

			httpSrv := &http.Server{
				Handler:      mux,
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 60 * time.Second,
				IdleTimeout:  120 * time.Second,
			}

			var listener net.Listener

			if socketPath != "" {
				// Clean up stale socket
				os.Remove(socketPath)
				listener, err = net.Listen("unix", socketPath)
				if err != nil {
					return fmt.Errorf("failed to listen on socket %s: %w", socketPath, err)
				}
				fmt.Printf("API server listening on unix://%s\n", socketPath)
			} else {
				listener, err = net.Listen("tcp", listenAddr)
				if err != nil {
					return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
				}
				fmt.Printf("API server listening on http://%s\n", listenAddr)
			}

			if readonly {
				fmt.Println("Mode: read-only (mutating operations disabled)")
			}

			// Graceful shutdown
			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

			go func() {
				if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
					fmt.Fprintf(os.Stderr, "API server error: %v\n", err)
					os.Exit(1)
				}
			}()

			<-stop
			fmt.Println("\nShutting down API server...")

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			if err := httpSrv.Shutdown(ctx); err != nil {
				return fmt.Errorf("server shutdown error: %w", err)
			}

			if socketPath != "" {
				os.Remove(socketPath)
			}

			fmt.Println("API server stopped.")
			return nil
		},
	}

	cmd.Flags().StringVar(&listenAddr, "listen", "", "TCP address to listen on (e.g. :8080, 127.0.0.1:9090)")
	cmd.Flags().StringVar(&socketPath, "socket", "", "Unix domain socket path")
	cmd.Flags().BoolVar(&readonly, "readonly", false, "Enable read-only mode (disable mutating operations)")

	return cmd
}

// apiServer holds the state for the REST API server
type apiServer struct {
	manager  *vm.VMManager
	baseDir  string
	readonly bool
}

// apiResponse is the standard JSON response envelope
type apiResponse struct {
	OK      bool        `json:"ok"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Message string      `json:"message,omitempty"`
}

func (s *apiServer) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Health / info
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/version", s.handleVersion)

	// Sandboxes
	mux.HandleFunc("/v1/sandboxes", s.handleSandboxes)
	mux.HandleFunc("/v1/sandboxes/", s.handleSandbox)

	// Images
	mux.HandleFunc("/v1/images", s.handleImages)

	// Events
	mux.HandleFunc("/v1/events", s.handleEvents)

	return mux
}

func (s *apiServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w)
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK: true,
		Data: map[string]interface{}{
			"status":  "healthy",
			"time":    time.Now().UTC().Format(time.RFC3339),
			"baseDir": s.baseDir,
		},
	})
}

func (s *apiServer) handleVersion(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w)
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK: true,
		Data: map[string]string{
			"version": "0.1.0",
			"api":     "v1",
		},
	})
}

func (s *apiServer) handleSandboxes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listSandboxes(w, r)
	case http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.createSandbox(w, r)
	default:
		s.methodNotAllowed(w)
	}
}

func (s *apiServer) handleSandbox(w http.ResponseWriter, r *http.Request) {
	// Extract sandbox name and optional action from path
	// /v1/sandboxes/{name}
	// /v1/sandboxes/{name}/{action}
	path := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
	parts := strings.SplitN(path, "/", 2)

	if len(parts) == 0 || parts[0] == "" {
		s.writeError(w, http.StatusBadRequest, "sandbox name required")
		return
	}

	name := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.getSandbox(w, r, name)
	case action == "" && r.Method == http.MethodDelete:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.destroySandbox(w, r, name)
	case action == "start" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.startSandbox(w, r, name)
	case action == "stop" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.stopSandbox(w, r, name)
	case action == "restart" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.restartSandbox(w, r, name)
	case action == "pause" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.pauseSandbox(w, r, name)
	case action == "unpause" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.unpauseSandbox(w, r, name)
	case action == "exec" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.execSandbox(w, r, name)
	case action == "logs" && r.Method == http.MethodGet:
		s.getSandboxLogs(w, r, name)
	case action == "stats" && r.Method == http.MethodGet:
		s.getSandboxStats(w, r, name)
	case action == "snapshots" && r.Method == http.MethodGet:
		s.listSnapshots(w, r, name)
	case action == "snapshots" && r.Method == http.MethodPost:
		if s.readonly {
			s.readonlyError(w)
			return
		}
		s.createSnapshot(w, r, name)
	default:
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("unknown endpoint: %s %s", r.Method, r.URL.Path))
	}
}

func (s *apiServer) listSandboxes(w http.ResponseWriter, r *http.Request) {
	vms, err := s.manager.List()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: vms})
}

// createSandboxRequest is the JSON body for creating a sandbox
type createSandboxRequest struct {
	Name     string            `json:"name"`
	From     string            `json:"from"`
	VCPUs    int               `json:"vcpus,omitempty"`
	MemoryMB int               `json:"memory_mb,omitempty"`
	DiskGB   int               `json:"disk_gb,omitempty"`
	Allow    []string          `json:"allow,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
}

func (s *apiServer) createSandbox(w http.ResponseWriter, r *http.Request) {
	var req createSandboxRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.Name == "" {
		s.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.From == "" {
		s.writeError(w, http.StatusBadRequest, "from (image reference) is required")
		return
	}

	cfg := &models.VMConfig{
		Name:     req.Name,
		From:     req.From,
		VCPUs:    req.VCPUs,
		MemoryMB: req.MemoryMB,
		DiskGB:   req.DiskGB,
		Env:      req.Env,
	}

	if cfg.VCPUs == 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemoryMB == 0 {
		cfg.MemoryMB = 512
	}
	if cfg.DiskGB == 0 {
		cfg.DiskGB = 10
	}

	if len(req.Allow) > 0 {
		cfg.Network.Allow = req.Allow
	}

	if err := s.manager.Create(req.Name, cfg); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	state, _ := s.manager.Status(req.Name)
	s.writeJSON(w, http.StatusCreated, apiResponse{
		OK:      true,
		Data:    state,
		Message: fmt.Sprintf("sandbox %q created", req.Name),
	})
}

func (s *apiServer) getSandbox(w http.ResponseWriter, r *http.Request, name string) {
	state, err := s.manager.Status(name)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: state})
}

func (s *apiServer) destroySandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Destroy(name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Message: fmt.Sprintf("sandbox %q destroyed", name),
	})
}

func (s *apiServer) startSandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Start(name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state, _ := s.manager.Status(name)
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Data:    state,
		Message: fmt.Sprintf("sandbox %q started", name),
	})
}

func (s *apiServer) stopSandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Stop(name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state, _ := s.manager.Status(name)
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Data:    state,
		Message: fmt.Sprintf("sandbox %q stopped", name),
	})
}

func (s *apiServer) restartSandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Restart(name, 30); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	state, _ := s.manager.Status(name)
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Data:    state,
		Message: fmt.Sprintf("sandbox %q restarted", name),
	})
}

func (s *apiServer) pauseSandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Pause(name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Message: fmt.Sprintf("sandbox %q paused", name),
	})
}

func (s *apiServer) unpauseSandbox(w http.ResponseWriter, r *http.Request, name string) {
	if err := s.manager.Unpause(name); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{
		OK:      true,
		Message: fmt.Sprintf("sandbox %q unpaused", name),
	})
}

// execRequest is the JSON body for executing a command
type execRequest struct {
	Command []string `json:"command"`
}

func (s *apiServer) execSandbox(w http.ResponseWriter, r *http.Request, name string) {
	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if len(req.Command) == 0 {
		s.writeError(w, http.StatusBadRequest, "command is required")
		return
	}

	output, exitCode, err := s.manager.Exec(name, req.Command)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, apiResponse{
		OK: true,
		Data: map[string]interface{}{
			"output":    output,
			"exit_code": exitCode,
		},
	})
}

func (s *apiServer) getSandboxLogs(w http.ResponseWriter, r *http.Request, name string) {
	tail := 100
	if v := r.URL.Query().Get("tail"); v != "" {
		fmt.Sscanf(v, "%d", &tail)
	}

	logs, err := s.manager.TailLogs(name, tail)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, apiResponse{
		OK: true,
		Data: map[string]interface{}{
			"logs": logs,
		},
	})
}

func (s *apiServer) getSandboxStats(w http.ResponseWriter, r *http.Request, name string) {
	stats, err := s.manager.GetStats(name)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: stats})
}

func (s *apiServer) listSnapshots(w http.ResponseWriter, r *http.Request, name string) {
	snaps, err := s.manager.ListSnapshots(name)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: snaps})
}

// createSnapshotRequest is the JSON body for creating a snapshot
type createSnapshotRequest struct {
	Tag string `json:"tag"`
}

func (s *apiServer) createSnapshot(w http.ResponseWriter, r *http.Request, name string) {
	var req createSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.Tag == "" {
		s.writeError(w, http.StatusBadRequest, "tag is required")
		return
	}

	path, err := s.manager.CreateSnapshot(name, req.Tag)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusCreated, apiResponse{
		OK: true,
		Data: map[string]string{
			"tag":  req.Tag,
			"path": path,
		},
		Message: fmt.Sprintf("snapshot %q created for sandbox %q", req.Tag, name),
	})
}

func (s *apiServer) handleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w)
		return
	}

	imgMgr, err := image.NewManager(filepath.Join(s.baseDir, "images"))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	images, err := imgMgr.ListImages()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: images})
}

func (s *apiServer) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.methodNotAllowed(w)
		return
	}

	logger := s.manager.EventLog()
	filter := vm.EventFilter{
		Sandbox: r.URL.Query().Get("sandbox"),
		Type:    vm.EventType(r.URL.Query().Get("type")),
		Limit:   50,
	}

	if v := r.URL.Query().Get("limit"); v != "" {
		fmt.Sscanf(v, "%d", &filter.Limit)
	}

	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			filter.Since = time.Now().UTC().Add(-d)
		}
	}

	events, err := logger.Query(filter)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: events})
}

// Helper methods

func (s *apiServer) writeJSON(w http.ResponseWriter, status int, resp apiResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}

func (s *apiServer) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, apiResponse{OK: false, Error: msg})
}

func (s *apiServer) methodNotAllowed(w http.ResponseWriter) {
	s.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (s *apiServer) readonlyError(w http.ResponseWriter) {
	s.writeError(w, http.StatusForbidden, "server is in read-only mode")
}
