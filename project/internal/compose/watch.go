package compose

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// WatchConfig defines which paths to watch for a sandbox service.
type WatchConfig struct {
	// Paths are file or directory paths to watch for changes.
	// Relative paths are resolved from the compose file directory.
	Paths []string `yaml:"paths"`
	// Ignore is a list of glob patterns to ignore (e.g. "*.log", ".git").
	Ignore []string `yaml:"ignore,omitempty"`
	// Action to take on change: "restart" (default) or "rebuild".
	Action string `yaml:"action,omitempty"`
}

// WatchEvent represents a detected file change.
type WatchEvent struct {
	Service  string
	Path     string
	Time     time.Time
	Action   string // "restart" or "rebuild"
}

// FileWatcher monitors files for changes and triggers sandbox restarts.
type FileWatcher struct {
	mu           sync.Mutex
	services     map[string]*watchedService
	interval     time.Duration
	onChange     func(event WatchEvent)
	stopCh       chan struct{}
	stopped      bool
	composeDir   string
}

type watchedService struct {
	config    *WatchConfig
	hashes    map[string]string // path -> sha256
	resolved  []string          // resolved absolute paths
}

// FileWatcherOpts configures a FileWatcher.
type FileWatcherOpts struct {
	// Interval between polling scans. Default: 2 seconds.
	Interval time.Duration
	// ComposeDir is the base directory for resolving relative paths.
	ComposeDir string
	// OnChange is called when a file change is detected.
	OnChange func(event WatchEvent)
}

// NewFileWatcher creates a polling-based file watcher.
func NewFileWatcher(opts FileWatcherOpts) *FileWatcher {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &FileWatcher{
		services:   make(map[string]*watchedService),
		interval:   interval,
		onChange:    opts.OnChange,
		stopCh:     make(chan struct{}),
		composeDir: opts.ComposeDir,
	}
}

// AddService registers a service and its watch configuration.
func (fw *FileWatcher) AddService(name string, cfg *WatchConfig) error {
	if cfg == nil || len(cfg.Paths) == 0 {
		return nil
	}

	action := cfg.Action
	if action == "" {
		action = "restart"
	}

	ws := &watchedService{
		config: cfg,
		hashes: make(map[string]string),
	}

	// Resolve paths
	for _, p := range cfg.Paths {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(fw.composeDir, abs)
		}
		abs = filepath.Clean(abs)
		ws.resolved = append(ws.resolved, abs)
	}

	// Take initial snapshot
	files, err := fw.collectFiles(ws)
	if err != nil {
		return fmt.Errorf("failed to scan watch paths for %s: %w", name, err)
	}

	for _, f := range files {
		h, err := hashFile(f)
		if err != nil {
			continue
		}
		ws.hashes[f] = h
	}

	fw.mu.Lock()
	fw.services[name] = ws
	fw.mu.Unlock()
	return nil
}

// Start begins polling for file changes.
func (fw *FileWatcher) Start() {
	go fw.poll()
}

// Stop halts the file watcher.
func (fw *FileWatcher) Stop() {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	if !fw.stopped {
		fw.stopped = true
		close(fw.stopCh)
	}
}

// WatchedServices returns the names of all watched services.
func (fw *FileWatcher) WatchedServices() []string {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	names := make([]string, 0, len(fw.services))
	for name := range fw.services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (fw *FileWatcher) poll() {
	ticker := time.NewTicker(fw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-fw.stopCh:
			return
		case <-ticker.C:
			fw.scan()
		}
	}
}

func (fw *FileWatcher) scan() {
	fw.mu.Lock()
	services := make(map[string]*watchedService, len(fw.services))
	for k, v := range fw.services {
		services[k] = v
	}
	fw.mu.Unlock()

	for name, ws := range services {
		files, err := fw.collectFiles(ws)
		if err != nil {
			continue
		}

		newHashes := make(map[string]string, len(files))
		changed := false

		for _, f := range files {
			h, err := hashFile(f)
			if err != nil {
				continue
			}
			newHashes[f] = h

			prev, exists := ws.hashes[f]
			if !exists || prev != h {
				changed = true
				if fw.onChange != nil {
					action := ws.config.Action
					if action == "" {
						action = "restart"
					}
					fw.onChange(WatchEvent{
						Service: name,
						Path:    f,
						Time:    time.Now(),
						Action:  action,
					})
				}
				break // one event per scan per service
			}
		}

		// Check for deleted files
		if !changed {
			for f := range ws.hashes {
				if _, ok := newHashes[f]; !ok {
					changed = true
					if fw.onChange != nil {
						action := ws.config.Action
						if action == "" {
							action = "restart"
						}
						fw.onChange(WatchEvent{
							Service: name,
							Path:    f,
							Time:    time.Now(),
							Action:  action,
						})
					}
					break
				}
			}
		}

		if changed {
			fw.mu.Lock()
			ws.hashes = newHashes
			fw.mu.Unlock()
		}
	}
}

func (fw *FileWatcher) collectFiles(ws *watchedService) ([]string, error) {
	var files []string

	for _, root := range ws.resolved {
		info, err := os.Stat(root)
		if err != nil {
			continue
		}

		if !info.IsDir() {
			if !fw.isIgnored(ws, root) {
				files = append(files, root)
			}
			continue
		}

		// Walk directory
		err = filepath.Walk(root, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if fi.IsDir() {
				base := filepath.Base(path)
				// Skip common hidden/build directories
				if base == ".git" || base == "node_modules" || base == "__pycache__" || base == ".cache" {
					return filepath.SkipDir
				}
				return nil
			}
			if !fw.isIgnored(ws, path) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			continue
		}
	}

	sort.Strings(files)
	return files, nil
}

func (fw *FileWatcher) isIgnored(ws *watchedService, path string) bool {
	if ws.config == nil {
		return false
	}
	base := filepath.Base(path)
	for _, pattern := range ws.config.Ignore {
		if matched, _ := filepath.Match(pattern, base); matched {
			return true
		}
		// Also try matching against relative path from compose dir
		rel, err := filepath.Rel(fw.composeDir, path)
		if err == nil {
			if matched, _ := filepath.Match(pattern, rel); matched {
				return true
			}
		}
	}
	return false
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// ExtractWatchConfigs extracts WatchConfig from sandbox configs that have watch defined.
// Returns a map of service name -> WatchConfig.
func ExtractWatchConfigs(config *ComposeConfig) map[string]*WatchConfig {
	result := make(map[string]*WatchConfig)
	for name, sb := range config.Sandboxes {
		if sb.Watch != nil && len(sb.Watch.Paths) > 0 {
			result[name] = sb.Watch
		}
	}
	return result
}

// FormatWatchSummary returns a human-readable summary of watch configurations.
func FormatWatchSummary(watches map[string]*WatchConfig) string {
	if len(watches) == 0 {
		return "No watch configurations found."
	}

	var b strings.Builder
	names := make([]string, 0, len(watches))
	for name := range watches {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		w := watches[name]
		action := w.Action
		if action == "" {
			action = "restart"
		}
		fmt.Fprintf(&b, "  %s (action: %s)\n", name, action)
		for _, p := range w.Paths {
			fmt.Fprintf(&b, "    - %s\n", p)
		}
		if len(w.Ignore) > 0 {
			fmt.Fprintf(&b, "    ignore: %s\n", strings.Join(w.Ignore, ", "))
		}
	}
	return b.String()
}
