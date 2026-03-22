// Package virtio provides virtio device emulation for microVMs.
// This file implements the tent guest agent protocol over virtio-vsock,
// enabling host-to-guest command execution, file transfer, and health checks.
//
// Protocol: JSON-framed messages over a vsock stream connection.
// Each message is: [4-byte length (little-endian)] [JSON payload]
package virtio

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Agent message types
const (
	AgentMsgExecRequest   = "exec_request"
	AgentMsgExecResponse  = "exec_response"
	AgentMsgExecStream    = "exec_stream"    // Streaming stdout/stderr
	AgentMsgPing          = "ping"
	AgentMsgPong          = "pong"
	AgentMsgFileWrite     = "file_write"
	AgentMsgFileWriteAck  = "file_write_ack"
	AgentMsgFileRead      = "file_read"
	AgentMsgFileReadResp  = "file_read_resp"
	AgentMsgSignal        = "signal"
	AgentMsgSignalAck     = "signal_ack"
)

// AgentMessage is the envelope for all guest agent protocol messages.
type AgentMessage struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp int64           `json:"ts"`
	Payload   json.RawMessage `json:"payload"`
}

// ExecRequest asks the guest agent to execute a command.
type ExecRequest struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"workdir,omitempty"`
	Stdin   []byte            `json:"stdin,omitempty"`
	TTY     bool              `json:"tty,omitempty"`
	Timeout int               `json:"timeout,omitempty"` // seconds, 0 = no timeout
}

// ExecResponse is the final result of a command execution.
type ExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ExecStream carries streaming output from a running command.
type ExecStream struct {
	Stream string `json:"stream"` // "stdout" or "stderr"
	Data   []byte `json:"data"`
	EOF    bool   `json:"eof,omitempty"`
}

// FileWriteRequest asks the guest to write a file.
type FileWriteRequest struct {
	Path    string `json:"path"`
	Data    []byte `json:"data"`
	Mode    uint32 `json:"mode"`
	Append  bool   `json:"append,omitempty"`
}

// FileWriteAck acknowledges a file write.
type FileWriteAck struct {
	BytesWritten int    `json:"bytes_written"`
	Error        string `json:"error,omitempty"`
}

// FileReadRequest asks the guest to read a file.
type FileReadRequest struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset,omitempty"`
	Limit  int64  `json:"limit,omitempty"` // 0 = entire file
}

// FileReadResponse returns file contents.
type FileReadResponse struct {
	Data  []byte `json:"data,omitempty"`
	Size  int64  `json:"size"`
	Error string `json:"error,omitempty"`
}

// SignalRequest sends a signal to a running process.
type SignalRequest struct {
	ProcessID string `json:"process_id"` // exec request ID
	Signal    int    `json:"signal"`     // Unix signal number
}

// SignalAck acknowledges a signal delivery.
type SignalAck struct {
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
}

// GuestAgent manages the host side of the guest agent protocol.
// It connects to the guest agent service via virtio-vsock and provides
// methods for executing commands and transferring files.
type GuestAgent struct {
	mu sync.Mutex

	vsock *VirtioVsock
	conn  *VsockConnection

	// Pending requests awaiting responses
	pending map[string]chan *AgentMessage

	// Request counter for generating unique IDs
	nextID atomic.Uint64

	// Agent state
	connected atomic.Bool
	guestReady atomic.Bool
}

// NewGuestAgent creates a new host-side guest agent client.
func NewGuestAgent(vsock *VirtioVsock) *GuestAgent {
	return &GuestAgent{
		vsock:   vsock,
		pending: make(map[string]chan *AgentMessage),
	}
}

// Connect establishes the vsock connection to the guest agent.
func (a *GuestAgent) Connect() error {
	if a.connected.Load() {
		return errors.New("agent: already connected")
	}

	// Allocate a host port (ephemeral range)
	hostPort := uint32(49152 + a.nextID.Add(1))

	conn, err := a.vsock.Connect(VsockPortGuestAgent, hostPort)
	if err != nil {
		return fmt.Errorf("agent: failed to connect to guest agent: %w", err)
	}

	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()

	a.connected.Store(true)
	return nil
}

// Disconnect closes the agent connection.
func (a *GuestAgent) Disconnect() error {
	if !a.connected.CompareAndSwap(true, false) {
		return nil
	}

	a.mu.Lock()
	conn := a.conn
	a.conn = nil

	// Cancel all pending requests
	for id, ch := range a.pending {
		close(ch)
		delete(a.pending, id)
	}
	a.mu.Unlock()

	if conn != nil {
		return a.vsock.CloseConnection(conn)
	}
	return nil
}

// Ping checks if the guest agent is responding.
func (a *GuestAgent) Ping() (time.Duration, error) {
	if !a.connected.Load() {
		return 0, errors.New("agent: not connected")
	}

	start := time.Now()

	msg, err := a.sendAndWait(AgentMsgPing, nil)
	if err != nil {
		return 0, fmt.Errorf("agent: ping failed: %w", err)
	}

	if msg.Type != AgentMsgPong {
		return 0, fmt.Errorf("agent: unexpected response type: %s", msg.Type)
	}

	return time.Since(start), nil
}

// Exec executes a command in the guest and returns the result.
func (a *GuestAgent) Exec(req ExecRequest) (*ExecResponse, error) {
	if !a.connected.Load() {
		return nil, errors.New("agent: not connected")
	}

	msg, err := a.sendAndWait(AgentMsgExecRequest, req)
	if err != nil {
		return nil, fmt.Errorf("agent: exec failed: %w", err)
	}

	if msg.Type != AgentMsgExecResponse {
		return nil, fmt.Errorf("agent: unexpected response type: %s", msg.Type)
	}

	var resp ExecResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		return nil, fmt.Errorf("agent: failed to decode exec response: %w", err)
	}

	return &resp, nil
}

// WriteFile writes a file in the guest.
func (a *GuestAgent) WriteFile(path string, data []byte, mode uint32) error {
	if !a.connected.Load() {
		return errors.New("agent: not connected")
	}

	req := FileWriteRequest{
		Path: path,
		Data: data,
		Mode: mode,
	}

	msg, err := a.sendAndWait(AgentMsgFileWrite, req)
	if err != nil {
		return fmt.Errorf("agent: file write failed: %w", err)
	}

	if msg.Type != AgentMsgFileWriteAck {
		return fmt.Errorf("agent: unexpected response type: %s", msg.Type)
	}

	var ack FileWriteAck
	if err := json.Unmarshal(msg.Payload, &ack); err != nil {
		return fmt.Errorf("agent: failed to decode write ack: %w", err)
	}

	if ack.Error != "" {
		return errors.New(ack.Error)
	}

	return nil
}

// ReadFile reads a file from the guest.
func (a *GuestAgent) ReadFile(path string) ([]byte, error) {
	if !a.connected.Load() {
		return nil, errors.New("agent: not connected")
	}

	req := FileReadRequest{
		Path: path,
	}

	msg, err := a.sendAndWait(AgentMsgFileRead, req)
	if err != nil {
		return nil, fmt.Errorf("agent: file read failed: %w", err)
	}

	if msg.Type != AgentMsgFileReadResp {
		return nil, fmt.Errorf("agent: unexpected response type: %s", msg.Type)
	}

	var resp FileReadResponse
	if err := json.Unmarshal(msg.Payload, &resp); err != nil {
		return nil, fmt.Errorf("agent: failed to decode read response: %w", err)
	}

	if resp.Error != "" {
		return nil, errors.New(resp.Error)
	}

	return resp.Data, nil
}

// SendSignal sends a signal to a running process in the guest.
func (a *GuestAgent) SendSignal(processID string, signal int) error {
	if !a.connected.Load() {
		return errors.New("agent: not connected")
	}

	req := SignalRequest{
		ProcessID: processID,
		Signal:    signal,
	}

	msg, err := a.sendAndWait(AgentMsgSignal, req)
	if err != nil {
		return fmt.Errorf("agent: signal failed: %w", err)
	}

	var ack SignalAck
	if err := json.Unmarshal(msg.Payload, &ack); err != nil {
		return fmt.Errorf("agent: failed to decode signal ack: %w", err)
	}

	if ack.Error != "" {
		return errors.New(ack.Error)
	}

	return nil
}

// HandleResponse processes an incoming message from the guest agent.
// This should be called when data is received on the agent's vsock connection.
func (a *GuestAgent) HandleResponse(data []byte) error {
	if len(data) < 4 {
		return errors.New("agent: message too short")
	}

	// Decode length-prefixed message
	msgLen := binary.LittleEndian.Uint32(data[0:4])
	if int(msgLen) > len(data)-4 {
		return fmt.Errorf("agent: message length %d exceeds buffer (%d)", msgLen, len(data)-4)
	}

	var msg AgentMessage
	if err := json.Unmarshal(data[4:4+msgLen], &msg); err != nil {
		return fmt.Errorf("agent: failed to decode message: %w", err)
	}

	// Route to pending request
	a.mu.Lock()
	ch, ok := a.pending[msg.ID]
	if ok {
		delete(a.pending, msg.ID)
	}
	a.mu.Unlock()

	if ok {
		ch <- &msg
	}

	return nil
}

// sendAndWait sends a request and waits for the response.
func (a *GuestAgent) sendAndWait(msgType string, payload interface{}) (*AgentMessage, error) {
	id := fmt.Sprintf("req-%d", a.nextID.Add(1))

	var payloadJSON json.RawMessage
	if payload != nil {
		var err error
		payloadJSON, err = json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("agent: failed to marshal payload: %w", err)
		}
	}

	msg := AgentMessage{
		Type:      msgType,
		ID:        id,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payloadJSON,
	}

	encoded, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("agent: failed to marshal message: %w", err)
	}

	// Length-prefix the message
	frame := make([]byte, 4+len(encoded))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(encoded)))
	copy(frame[4:], encoded)

	// Register pending response channel
	respCh := make(chan *AgentMessage, 1)
	a.mu.Lock()
	a.pending[id] = respCh
	conn := a.conn
	a.mu.Unlock()

	if conn == nil {
		return nil, errors.New("agent: no connection")
	}

	// Send via vsock
	if err := a.vsock.SendData(conn, frame); err != nil {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		return nil, fmt.Errorf("agent: failed to send: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp, ok := <-respCh:
		if !ok {
			return nil, errors.New("agent: connection closed while waiting for response")
		}
		return resp, nil
	case <-time.After(30 * time.Second):
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		return nil, errors.New("agent: request timed out")
	}
}

// EncodeAgentMessage serializes an agent message with length prefix.
func EncodeAgentMessage(msg *AgentMessage) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}

	frame := make([]byte, 4+len(data))
	binary.LittleEndian.PutUint32(frame[0:4], uint32(len(data)))
	copy(frame[4:], data)

	return frame, nil
}

// DecodeAgentMessage deserializes a length-prefixed agent message.
func DecodeAgentMessage(data []byte) (*AgentMessage, error) {
	if len(data) < 4 {
		return nil, errors.New("agent: buffer too short")
	}

	msgLen := binary.LittleEndian.Uint32(data[0:4])
	if int(msgLen) > len(data)-4 {
		return nil, fmt.Errorf("agent: message length %d exceeds buffer", msgLen)
	}

	var msg AgentMessage
	if err := json.Unmarshal(data[4:4+msgLen], &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}
