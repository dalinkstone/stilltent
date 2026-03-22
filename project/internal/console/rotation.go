// Package console provides VM console log capture and retrieval.
// This file implements size-based log rotation for sandbox console logs.
// When a log file exceeds the configured maximum size, it is rotated:
// the current file becomes .1, the previous .1 becomes .2, and so on,
// up to a configurable number of retained files. The oldest file beyond
// the retention limit is deleted.
package console

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RotationConfig controls log rotation behavior.
type RotationConfig struct {
	// MaxSizeBytes is the maximum log file size before rotation triggers.
	// Default: 10 MB.
	MaxSizeBytes int64
	// MaxFiles is the number of rotated files to keep (not counting the
	// active file). For example, MaxFiles=3 keeps foo.log, foo.log.1,
	// foo.log.2, foo.log.3. Default: 5.
	MaxFiles int
	// CompressRotated controls whether rotated files are gzip-compressed.
	// Currently a placeholder for future implementation.
	CompressRotated bool
}

// DefaultRotationConfig returns sensible defaults for log rotation.
func DefaultRotationConfig() RotationConfig {
	return RotationConfig{
		MaxSizeBytes: 10 * 1024 * 1024, // 10 MB
		MaxFiles:     5,
	}
}

// RotatingLogger wraps a console Logger with automatic size-based rotation.
type RotatingLogger struct {
	mu       sync.Mutex
	vmName   string
	logDir   string
	logPath  string
	file     *os.File
	writer   *bufio.Writer
	config   RotationConfig
	written  int64 // bytes written since last rotation check
	closed   bool
}

// NewRotatingLogger creates a rotating logger for the named sandbox.
func NewRotatingLogger(logDir, vmName string, cfg RotationConfig) (*RotatingLogger, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}

	if cfg.MaxSizeBytes <= 0 {
		cfg.MaxSizeBytes = DefaultRotationConfig().MaxSizeBytes
	}
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = DefaultRotationConfig().MaxFiles
	}

	logPath := filepath.Join(logDir, fmt.Sprintf("%s.log", vmName))

	rl := &RotatingLogger{
		vmName:  vmName,
		logDir:  logDir,
		logPath: logPath,
		config:  cfg,
	}

	if err := rl.openFile(); err != nil {
		return nil, err
	}

	return rl, nil
}

// openFile opens (or creates) the current log file for appending.
func (rl *RotatingLogger) openFile() error {
	f, err := os.OpenFile(rl.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	// Determine current file size so we know when to rotate.
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("failed to stat log file: %w", err)
	}

	rl.file = f
	rl.writer = bufio.NewWriterSize(f, 8192)
	rl.written = info.Size()

	// Write session header
	header := fmt.Sprintf("\n=== tent console session started at %s ===\n", time.Now().Format(time.RFC3339))
	n, _ := rl.writer.WriteString(header)
	rl.written += int64(n)

	return nil
}

// Write implements io.Writer. It writes data to the current log file and
// triggers rotation when the file exceeds the configured maximum size.
func (rl *RotatingLogger) Write(p []byte) (int, error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.closed {
		return 0, fmt.Errorf("logger is closed")
	}

	n, err := rl.writer.Write(p)
	if err != nil {
		return n, err
	}
	rl.writer.Flush()
	rl.written += int64(n)

	// Check if rotation is needed
	if rl.written >= rl.config.MaxSizeBytes {
		if rotErr := rl.rotate(); rotErr != nil {
			// Log rotation failure is non-fatal; continue writing
			fmt.Fprintf(os.Stderr, "tent: log rotation failed for %s: %v\n", rl.vmName, rotErr)
		}
	}

	return n, nil
}

// rotate closes the current file and rotates the log chain.
func (rl *RotatingLogger) rotate() error {
	// Flush and close the current file
	rl.writer.Flush()
	rl.file.Close()

	// Rotate existing files: .5 -> delete, .4 -> .5, .3 -> .4, ... .1 -> .2, current -> .1
	for i := rl.config.MaxFiles; i >= 1; i-- {
		src := rl.rotatedPath(i - 1)
		dst := rl.rotatedPath(i)

		if i == rl.config.MaxFiles {
			// Delete the oldest file that would exceed retention
			os.Remove(dst)
		}

		if _, err := os.Stat(src); err == nil {
			os.Rename(src, dst)
		}
	}

	// Rename current log to .1
	os.Rename(rl.logPath, rl.rotatedPath(1))

	// Open a fresh log file
	return rl.openFile()
}

// rotatedPath returns the path for a rotated log file at the given index.
// Index 0 is the current file, 1 is .1, 2 is .2, etc.
func (rl *RotatingLogger) rotatedPath(index int) string {
	if index == 0 {
		return rl.logPath
	}
	return fmt.Sprintf("%s.%d", rl.logPath, index)
}

// Close flushes and closes the rotating logger.
func (rl *RotatingLogger) Close() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.closed {
		return nil
	}
	rl.closed = true

	footer := fmt.Sprintf("=== tent console session ended at %s ===\n", time.Now().Format(time.RFC3339))
	rl.writer.WriteString(footer)
	rl.writer.Flush()

	return rl.file.Close()
}

// CurrentSize returns the current log file size in bytes.
func (rl *RotatingLogger) CurrentSize() int64 {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.written
}

// Config returns the rotation configuration.
func (rl *RotatingLogger) Config() RotationConfig {
	return rl.config
}

// ListRotatedFiles returns information about all log files (current + rotated)
// for this logger's sandbox.
func (rl *RotatingLogger) ListRotatedFiles() []RotatedFileInfo {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var files []RotatedFileInfo

	for i := 0; i <= rl.config.MaxFiles; i++ {
		path := rl.rotatedPath(i)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		files = append(files, RotatedFileInfo{
			Path:    path,
			Index:   i,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}

	return files
}

// RotatedFileInfo describes a single log file in the rotation chain.
type RotatedFileInfo struct {
	// Path is the absolute path to the log file.
	Path string `json:"path"`
	// Index is the rotation index (0 = current, 1 = most recent rotated, etc.)
	Index int `json:"index"`
	// Size is the file size in bytes.
	Size int64 `json:"size"`
	// ModTime is the file's last modification time.
	ModTime time.Time `json:"mod_time"`
}

// TotalSize returns the total bytes used by all log files (current + rotated).
func (rl *RotatingLogger) TotalSize() int64 {
	files := rl.ListRotatedFiles()
	var total int64
	for _, f := range files {
		total += f.Size
	}
	return total
}

// Purge removes all rotated log files, keeping only the current active file.
func (rl *RotatingLogger) Purge() (int, int64, error) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	var removed int
	var freed int64

	for i := 1; i <= rl.config.MaxFiles; i++ {
		path := rl.rotatedPath(i)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		freed += info.Size()
		if err := os.Remove(path); err != nil {
			return removed, freed, fmt.Errorf("failed to remove %s: %w", path, err)
		}
		removed++
	}

	return removed, freed, nil
}

// RotatingManager extends the console Manager with rotation support.
type RotatingManager struct {
	*Manager
	config  RotationConfig
	rloggers map[string]*RotatingLogger
	mu       sync.Mutex
}

// NewRotatingManager creates a console manager with log rotation enabled.
func NewRotatingManager(baseDir string, cfg RotationConfig) (*RotatingManager, error) {
	base, err := NewManager(baseDir)
	if err != nil {
		return nil, err
	}

	return &RotatingManager{
		Manager:  base,
		config:   cfg,
		rloggers: make(map[string]*RotatingLogger),
	}, nil
}

// CreateRotatingLogger creates a rotating logger for a sandbox.
func (rm *RotatingManager) CreateRotatingLogger(vmName string) (*RotatingLogger, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Close existing rotating logger if any
	if existing, ok := rm.rloggers[vmName]; ok {
		existing.Close()
	}

	rl, err := NewRotatingLogger(rm.logDir, vmName, rm.config)
	if err != nil {
		return nil, err
	}

	rm.rloggers[vmName] = rl
	return rl, nil
}

// CloseRotatingLogger closes and removes the rotating logger for a VM.
func (rm *RotatingManager) CloseRotatingLogger(vmName string) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if rl, ok := rm.rloggers[vmName]; ok {
		rl.Close()
		delete(rm.rloggers, vmName)
	}
}

// GetRotatingLogger returns the rotating logger for a VM, or nil if not active.
func (rm *RotatingManager) GetRotatingLogger(vmName string) *RotatingLogger {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	return rm.rloggers[vmName]
}

// RotationStats returns aggregate statistics about log rotation across all sandboxes.
func (rm *RotatingManager) RotationStats() RotationStatsInfo {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	stats := RotationStatsInfo{
		Config: rm.config,
	}

	for name, rl := range rm.rloggers {
		files := rl.ListRotatedFiles()
		var totalSize int64
		for _, f := range files {
			totalSize += f.Size
		}
		stats.Sandboxes = append(stats.Sandboxes, SandboxLogStats{
			Name:         name,
			CurrentSize:  rl.written,
			TotalSize:    totalSize,
			RotatedFiles: len(files) - 1, // exclude current
		})
		stats.TotalBytes += totalSize
		stats.TotalFiles += len(files)
	}

	return stats
}

// RotationStatsInfo holds aggregate rotation statistics.
type RotationStatsInfo struct {
	Config     RotationConfig   `json:"config"`
	TotalBytes int64            `json:"total_bytes"`
	TotalFiles int              `json:"total_files"`
	Sandboxes  []SandboxLogStats `json:"sandboxes"`
}

// SandboxLogStats holds per-sandbox log statistics.
type SandboxLogStats struct {
	Name         string `json:"name"`
	CurrentSize  int64  `json:"current_size"`
	TotalSize    int64  `json:"total_size"`
	RotatedFiles int    `json:"rotated_files"`
}
