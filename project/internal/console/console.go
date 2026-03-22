// Package console provides VM console log capture and retrieval.
// It manages per-VM log files that capture serial/boot output.
package console

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Logger captures console output for a VM to a log file.
type Logger struct {
	vmName  string
	logFile *os.File
	writer  *bufio.Writer
	mu      sync.Mutex
	closed  bool
}

// Manager manages console loggers for all VMs.
type Manager struct {
	logDir  string
	loggers map[string]*Logger
	mu      sync.Mutex
}

// NewManager creates a new console log manager.
func NewManager(baseDir string) (*Manager, error) {
	logDir := filepath.Join(baseDir, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log directory: %w", err)
	}
	return &Manager{
		logDir:  logDir,
		loggers: make(map[string]*Logger),
	}, nil
}

// CreateLogger creates a new console logger for a VM.
// It opens (or creates) the log file and returns a Logger that can be used
// as an io.Writer for capturing console output.
func (m *Manager) CreateLogger(vmName string) (*Logger, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Close existing logger if any
	if existing, ok := m.loggers[vmName]; ok {
		existing.Close()
	}

	logPath := filepath.Join(m.logDir, fmt.Sprintf("%s.log", vmName))
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file: %w", err)
	}

	// Write session header
	header := fmt.Sprintf("\n=== tent console session started at %s ===\n", time.Now().Format(time.RFC3339))
	f.WriteString(header)

	logger := &Logger{
		vmName:  vmName,
		logFile: f,
		writer:  bufio.NewWriterSize(f, 4096),
	}

	m.loggers[vmName] = logger
	return logger, nil
}

// CloseLogger closes and removes the logger for a VM.
func (m *Manager) CloseLogger(vmName string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if logger, ok := m.loggers[vmName]; ok {
		logger.Close()
		delete(m.loggers, vmName)
	}
}

// GetLogPath returns the path to a VM's log file.
func (m *Manager) GetLogPath(vmName string) string {
	return filepath.Join(m.logDir, fmt.Sprintf("%s.log", vmName))
}

// ReadLogs reads the full log content for a VM.
func (m *Manager) ReadLogs(vmName string) (string, error) {
	logPath := m.GetLogPath(vmName)
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to read log file: %w", err)
	}
	return string(data), nil
}

// TailLogs reads the last n lines from a VM's log file.
func (m *Manager) TailLogs(vmName string, n int) (string, error) {
	logPath := m.GetLogPath(vmName)
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	return tailFile(f, n)
}

// FollowLogs streams log output to the given writer, blocking until ctx is done.
// It tails the last `tailLines` lines first, then follows new output.
func (m *Manager) FollowLogs(vmName string, tailLines int, out io.Writer, done <-chan struct{}) error {
	logPath := m.GetLogPath(vmName)

	// First, output existing tail
	if tailLines > 0 {
		tail, err := m.TailLogs(vmName, tailLines)
		if err != nil {
			return err
		}
		if tail != "" {
			fmt.Fprint(out, tail)
		}
	}

	// Open file for following
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Wait for file to appear
			for {
				select {
				case <-done:
					return nil
				case <-time.After(200 * time.Millisecond):
					f, err = os.Open(logPath)
					if err == nil {
						goto follow
					}
				}
			}
		}
		return fmt.Errorf("failed to open log file: %w", err)
	}

follow:
	defer f.Close()

	// Seek to end
	f.Seek(0, io.SeekEnd)

	buf := make([]byte, 4096)
	for {
		select {
		case <-done:
			return nil
		default:
			n, err := f.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
			}
			if err != nil {
				// EOF - wait and retry
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
}

// ClearLogs removes the log file for a VM.
func (m *Manager) ClearLogs(vmName string) error {
	logPath := m.GetLogPath(vmName)
	err := os.Remove(logPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove log file: %w", err)
	}
	return nil
}

// Write implements io.Writer for the Logger.
func (l *Logger) Write(p []byte) (n int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return 0, fmt.Errorf("logger is closed")
	}

	n, err = l.writer.Write(p)
	if err != nil {
		return n, err
	}

	// Flush periodically to ensure logs are readable
	l.writer.Flush()
	return n, nil
}

// Close flushes and closes the logger.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Write session footer
	footer := fmt.Sprintf("=== tent console session ended at %s ===\n", time.Now().Format(time.RFC3339))
	l.writer.WriteString(footer)
	l.writer.Flush()

	return l.logFile.Close()
}

// tailFile reads the last n lines from a file.
func tailFile(f *os.File, n int) (string, error) {
	// Get file size
	stat, err := f.Stat()
	if err != nil {
		return "", err
	}

	size := stat.Size()
	if size == 0 {
		return "", nil
	}

	// Read from end in chunks to find last n lines
	const chunkSize = 8192
	lines := make([]string, 0, n)
	offset := size
	scanner_done := false

	for offset > 0 && !scanner_done {
		readSize := int64(chunkSize)
		if readSize > offset {
			readSize = offset
		}
		offset -= readSize

		chunk := make([]byte, readSize)
		_, err := f.ReadAt(chunk, offset)
		if err != nil && err != io.EOF {
			return "", err
		}

		// Prepend to collected data and re-scan
		// Simple approach: read entire file if small, else read from end
		break
	}

	// For simplicity and correctness, scan the whole file if under 1MB,
	// otherwise read last 1MB
	if size > 1024*1024 {
		f.Seek(size-1024*1024, io.SeekStart)
	} else {
		f.Seek(0, io.SeekStart)
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}

	_ = scanner_done // suppress unused warning

	if len(lines) == 0 {
		return "", nil
	}

	result := ""
	for _, line := range lines {
		result += line + "\n"
	}
	return result, nil
}
