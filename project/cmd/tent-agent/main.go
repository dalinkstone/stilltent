// tent-agent is a lightweight daemon that runs inside a microVM guest.
// It listens on virtio-vsock port 1024 and handles the tent guest agent
// protocol: command execution, file transfer, health pings, and signal delivery.
//
// This binary is injected into the guest initramfs and started at boot.
// It communicates with the host via AF_VSOCK using length-prefixed JSON messages.
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	vsockPort    = 1024
	vsockCIDHost = 2
	maxMsgSize   = 16 * 1024 * 1024 // 16 MiB max message
)

// Agent message types — must match host-side protocol in virtio/agent.go
const (
	msgExecRequest  = "exec_request"
	msgExecResponse = "exec_response"
	msgExecStream   = "exec_stream"
	msgPing         = "ping"
	msgPong         = "pong"
	msgFileWrite    = "file_write"
	msgFileWriteAck = "file_write_ack"
	msgFileRead     = "file_read"
	msgFileReadResp = "file_read_resp"
	msgSignal       = "signal"
	msgSignalAck    = "signal_ack"
)

// agentMessage is the envelope for all protocol messages.
type agentMessage struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

type execRequest struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"workdir,omitempty"`
	Stdin   []byte            `json:"stdin,omitempty"`
	TTY     bool              `json:"tty,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
}

type execResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

type fileWriteRequest struct {
	Path   string `json:"path"`
	Data   []byte `json:"data"`
	Mode   uint32 `json:"mode"`
	Append bool   `json:"append,omitempty"`
}

type fileWriteAck struct {
	BytesWritten int    `json:"bytes_written"`
	Error        string `json:"error,omitempty"`
}

type fileReadRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"`
}

type fileReadResponse struct {
	Data  []byte `json:"data,omitempty"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

type signalRequest struct {
	ProcessID string `json:"process_id"`
	Signal    int    `json:"signal"`
}

type signalAck struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

// processTracker keeps track of running exec processes for signal delivery.
type processTracker struct {
	mu    sync.Mutex
	procs map[string]*os.Process
}

func newProcessTracker() *processTracker {
	return &processTracker{
		procs: make(map[string]*os.Process),
	}
}

func (pt *processTracker) add(id string, proc *os.Process) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.procs[id] = proc
}

func (pt *processTracker) remove(id string) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	delete(pt.procs, id)
}

func (pt *processTracker) get(id string) *os.Process {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return pt.procs[id]
}

var tracker = newProcessTracker()

func main() {
	log.SetPrefix("tent-agent: ")
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	log.Println("starting guest agent on vsock port", vsockPort)

	// Listen on AF_VSOCK
	listener, err := listenVsock(vsockPort)
	if err != nil {
		log.Fatalf("failed to listen on vsock port %d: %v", vsockPort, err)
	}
	defer listener.Close()

	log.Println("listening for host connections")

	// Handle shutdown signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down", sig)
		listener.Close()
		os.Exit(0)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("accept error: %v", err)
			continue
		}
		go handleConnection(conn)
	}
}

// listenVsock creates a VSOCK listener. On Linux, this uses AF_VSOCK directly.
// On other platforms (for compilation), it falls back to a TCP listener on localhost.
func listenVsock(port int) (net.Listener, error) {
	// Try AF_VSOCK first (Linux with vsock support)
	fd, err := syscall.Socket(40, syscall.SOCK_STREAM, 0) // AF_VSOCK = 40
	if err == nil {
		// AF_VSOCK sockaddr: family(2) + reserved(2) + port(4) + cid(4)
		sa := make([]byte, 16)
		binary.LittleEndian.PutUint16(sa[0:2], 40) // AF_VSOCK
		// reserved bytes are zero
		binary.LittleEndian.PutUint32(sa[4:8], uint32(port))
		// CID_ANY = 0xFFFFFFFF — accept from any CID
		binary.LittleEndian.PutUint32(sa[8:12], 0xFFFFFFFF)

		// Use RawSyscall for bind
		_, _, errno := syscall.RawSyscall(syscall.SYS_BIND, uintptr(fd), uintptr(ptrFromSlice(sa)), 16)
		if errno != 0 {
			syscall.Close(fd)
			return nil, fmt.Errorf("vsock bind failed: %v", errno)
		}

		_, _, errno = syscall.RawSyscall(syscall.SYS_LISTEN, uintptr(fd), 8, 0)
		if errno != 0 {
			syscall.Close(fd)
			return nil, fmt.Errorf("vsock listen failed: %v", errno)
		}

		file := os.NewFile(uintptr(fd), "vsock-listener")
		listener, err := net.FileListener(file)
		file.Close() // FileListener dups the fd
		if err != nil {
			return nil, fmt.Errorf("vsock FileListener: %w", err)
		}

		log.Println("using AF_VSOCK transport")
		return listener, nil
	}

	// Fallback: TCP on localhost (for testing or non-vsock platforms)
	log.Println("AF_VSOCK unavailable, falling back to TCP localhost:", port)
	return net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	log.Printf("new connection from %s", conn.RemoteAddr())

	for {
		msg, err := readMessage(conn)
		if err != nil {
			if err != io.EOF {
				log.Printf("read error: %v", err)
			}
			return
		}

		resp, err := handleMessage(msg)
		if err != nil {
			log.Printf("handle error for %s/%s: %v", msg.Type, msg.ID, err)
			continue
		}

		if resp != nil {
			if err := writeMessage(conn, resp); err != nil {
				log.Printf("write error: %v", err)
				return
			}
		}
	}
}

// readMessage reads a length-prefixed JSON message from the connection.
func readMessage(r io.Reader) (*agentMessage, error) {
	// Read 4-byte length prefix
	var lenBuf [4]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, err
	}

	msgLen := binary.LittleEndian.Uint32(lenBuf[:])
	if msgLen > maxMsgSize {
		return nil, fmt.Errorf("message too large: %d bytes", msgLen)
	}

	// Read message body
	body := make([]byte, msgLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("short read: %w", err)
	}

	var msg agentMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	return &msg, nil
}

// writeMessage writes a length-prefixed JSON message to the connection.
func writeMessage(w io.Writer, msg *agentMessage) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	var lenBuf [4]byte
	binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(body)))

	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func handleMessage(msg *agentMessage) (*agentMessage, error) {
	switch msg.Type {
	case msgPing:
		return &agentMessage{
			Type:      msgPong,
			ID:        msg.ID,
			Timestamp: time.Now().UnixMilli(),
		}, nil

	case msgExecRequest:
		return handleExec(msg)

	case msgFileWrite:
		return handleFileWrite(msg)

	case msgFileRead:
		return handleFileRead(msg)

	case msgSignal:
		return handleSignal(msg)

	default:
		return nil, fmt.Errorf("unknown message type: %s", msg.Type)
	}
}

func handleExec(msg *agentMessage) (*agentMessage, error) {
	var req execRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return makeExecError(msg.ID, fmt.Sprintf("invalid exec request: %v", err)), nil
	}

	cmd := exec.Command(req.Command, req.Args...)

	// Set working directory
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}

	// Set environment
	if len(req.Env) > 0 {
		env := os.Environ()
		for k, v := range req.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	// Set stdin
	if len(req.Stdin) > 0 {
		cmd.Stdin = bytes.NewReader(req.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	// Track the process for signal delivery
	tracker.add(msg.ID, nil) // placeholder until started

	// Apply timeout
	var timer *time.Timer
	if req.Timeout > 0 {
		timer = time.AfterFunc(time.Duration(req.Timeout)*time.Second, func() {
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
		})
	}

	err := cmd.Start()
	if err != nil {
		tracker.remove(msg.ID)
		if timer != nil {
			timer.Stop()
		}
		return makeExecError(msg.ID, fmt.Sprintf("failed to start: %v", err)), nil
	}

	// Update tracker with real process
	tracker.add(msg.ID, cmd.Process)

	err = cmd.Wait()
	tracker.remove(msg.ID)
	if timer != nil {
		timer.Stop()
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return makeExecError(msg.ID, fmt.Sprintf("exec error: %v", err)), nil
		}
	}

	resp := execResponse{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
	}

	payload, _ := json.Marshal(resp)
	return &agentMessage{
		Type:      msgExecResponse,
		ID:        msg.ID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}, nil
}

func makeExecError(id string, errMsg string) *agentMessage {
	resp := execResponse{
		ExitCode: -1,
		Error:    errMsg,
	}
	payload, _ := json.Marshal(resp)
	return &agentMessage{
		Type:      msgExecResponse,
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func handleFileWrite(msg *agentMessage) (*agentMessage, error) {
	var req fileWriteRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return makeFileWriteError(msg.ID, fmt.Sprintf("invalid request: %v", err)), nil
	}

	// Sanitize path — must be absolute
	if !filepath.IsAbs(req.Path) {
		return makeFileWriteError(msg.ID, "path must be absolute"), nil
	}

	// Ensure parent directory exists
	dir := filepath.Dir(req.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return makeFileWriteError(msg.ID, fmt.Sprintf("mkdir failed: %v", err)), nil
	}

	mode := os.FileMode(req.Mode)
	if mode == 0 {
		mode = 0644
	}

	flags := os.O_WRONLY | os.O_CREATE
	if req.Append {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}

	f, err := os.OpenFile(req.Path, flags, mode)
	if err != nil {
		return makeFileWriteError(msg.ID, fmt.Sprintf("open failed: %v", err)), nil
	}
	defer f.Close()

	n, err := f.Write(req.Data)
	if err != nil {
		return makeFileWriteError(msg.ID, fmt.Sprintf("write failed: %v", err)), nil
	}

	ack := fileWriteAck{BytesWritten: n}
	payload, _ := json.Marshal(ack)
	return &agentMessage{
		Type:      msgFileWriteAck,
		ID:        msg.ID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}, nil
}

func makeFileWriteError(id string, errMsg string) *agentMessage {
	ack := fileWriteAck{Error: errMsg}
	payload, _ := json.Marshal(ack)
	return &agentMessage{
		Type:      msgFileWriteAck,
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func handleFileRead(msg *agentMessage) (*agentMessage, error) {
	var req fileReadRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return makeFileReadError(msg.ID, fmt.Sprintf("invalid request: %v", err)), nil
	}

	if !filepath.IsAbs(req.Path) {
		return makeFileReadError(msg.ID, "path must be absolute"), nil
	}

	info, err := os.Stat(req.Path)
	if err != nil {
		return makeFileReadError(msg.ID, fmt.Sprintf("stat failed: %v", err)), nil
	}

	f, err := os.Open(req.Path)
	if err != nil {
		return makeFileReadError(msg.ID, fmt.Sprintf("open failed: %v", err)), nil
	}
	defer f.Close()

	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			return makeFileReadError(msg.ID, fmt.Sprintf("seek failed: %v", err)), nil
		}
	}

	var reader io.Reader = f
	if req.Limit > 0 {
		reader = io.LimitReader(f, req.Limit)
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return makeFileReadError(msg.ID, fmt.Sprintf("read failed: %v", err)), nil
	}

	resp := fileReadResponse{
		Data: data,
		Size: info.Size(),
	}
	payload, _ := json.Marshal(resp)
	return &agentMessage{
		Type:      msgFileReadResp,
		ID:        msg.ID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}, nil
}

func makeFileReadError(id string, errMsg string) *agentMessage {
	resp := fileReadResponse{Error: errMsg}
	payload, _ := json.Marshal(resp)
	return &agentMessage{
		Type:      msgFileReadResp,
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func handleSignal(msg *agentMessage) (*agentMessage, error) {
	var req signalRequest
	if err := json.Unmarshal(msg.Payload, &req); err != nil {
		return makeSignalError(msg.ID, fmt.Sprintf("invalid request: %v", err)), nil
	}

	proc := tracker.get(req.ProcessID)
	if proc == nil {
		return makeSignalError(msg.ID, "process not found: "+req.ProcessID), nil
	}

	sig := syscall.Signal(req.Signal)
	err := proc.Signal(sig)
	if err != nil {
		return makeSignalError(msg.ID, fmt.Sprintf("signal failed: %v", err)), nil
	}

	ack := signalAck{Delivered: true}
	payload, _ := json.Marshal(ack)
	return &agentMessage{
		Type:      msgSignalAck,
		ID:        msg.ID,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}, nil
}

func makeSignalError(id string, errMsg string) *agentMessage {
	ack := signalAck{Error: errMsg}
	payload, _ := json.Marshal(ack)
	return &agentMessage{
		Type:      msgSignalAck,
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

// ptrFromSlice returns an unsafe.Pointer to the first element of a slice.
// Used for syscall sockaddr arguments.
func ptrFromSlice(b []byte) unsafe.Pointer {
	return unsafe.Pointer(&b[0])
}

// portFromEnv reads the agent port from TENT_AGENT_PORT env var, defaulting to vsockPort.
func portFromEnv() int {
	s := os.Getenv("TENT_AGENT_PORT")
	if s == "" {
		return vsockPort
	}
	p, err := strconv.Atoi(s)
	if err != nil {
		return vsockPort
	}
	return p
}
