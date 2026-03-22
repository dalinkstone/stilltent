// Package virtio provides virtio device emulation for microVMs.
// This file implements virtio-fs — a shared filesystem device that exposes
// host directories to the guest using a FUSE-based protocol over virtio.
//
// The device presents a virtio transport that carries FUSE requests/responses,
// enabling the guest to mount host directories with near-native performance.
//
// Reference: virtio-fs specification (based on VIRTIO 1.1+ and FUSE protocol)
package virtio

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// DeviceTypeFS is the virtio device type for filesystem sharing
const DeviceTypeFS DeviceType = 26

// FUSE operation codes (subset needed for virtio-fs)
const (
	FuseOpLookup      uint32 = 1
	FuseOpForget      uint32 = 2
	FuseOpGetattr     uint32 = 3
	FuseOpSetattr     uint32 = 4
	FuseOpReadlink    uint32 = 5
	FuseOpMkdir       uint32 = 7
	FuseOpUnlink      uint32 = 10
	FuseOpRmdir       uint32 = 11
	FuseOpRename      uint32 = 12
	FuseOpOpen        uint32 = 14
	FuseOpRead        uint32 = 15
	FuseOpWrite       uint32 = 16
	FuseOpStatfs      uint32 = 17
	FuseOpRelease     uint32 = 18
	FuseOpFsync       uint32 = 20
	FuseOpFlush       uint32 = 25
	FuseOpInit        uint32 = 26
	FuseOpOpendir     uint32 = 27
	FuseOpReaddir     uint32 = 28
	FuseOpReleasedir  uint32 = 29
	FuseOpCreate      uint32 = 35
	FuseOpReaddirplus uint32 = 44
)

// FUSE error codes (negated errno values used in FUSE responses)
const (
	FuseErrOK     int32 = 0
	FuseErrNoent  int32 = -2  // ENOENT
	FuseErrIO     int32 = -5  // EIO
	FuseErrAccess int32 = -13 // EACCES
	FuseErrExist  int32 = -17 // EEXIST
	FuseErrNotdir int32 = -20 // ENOTDIR
	FuseErrIsdir  int32 = -21 // EISDIR
	FuseErrInval  int32 = -22 // EINVAL
	FuseErrNospc  int32 = -28 // ENOSPC
	FuseErrNosys  int32 = -38 // ENOSYS
	FuseErrEmpty  int32 = -39 // ENOTEMPTY
)

// FUSE protocol versions
const (
	FuseKernelVersion      = 7
	FuseKernelMinorVersion = 31
)

// FUSE open flags
const (
	FuseOpenFlagDirectIO  uint32 = 1 << 0
	FuseOpenFlagKeepCache uint32 = 1 << 1
)

// FuseInHeader is the header for every FUSE request from guest to host (40 bytes).
type FuseInHeader struct {
	Len     uint32
	Opcode  uint32
	Unique  uint64
	NodeID  uint64
	UID     uint32
	GID     uint32
	PID     uint32
	Padding uint32
}

const fuseInHeaderSize = 40

// FuseOutHeader is the header for every FUSE response from host to guest (16 bytes).
type FuseOutHeader struct {
	Len    uint32
	Error  int32
	Unique uint64
}

const fuseOutHeaderSize = 16

// FuseAttr holds file attributes returned to the guest.
type FuseAttr struct {
	Ino       uint64
	Size      uint64
	Blocks    uint64
	Atime     uint64
	Mtime     uint64
	Ctime     uint64
	AtimeNsec uint32
	MtimeNsec uint32
	CtimeNsec uint32
	Mode      uint32
	Nlink     uint32
	UID       uint32
	GID       uint32
	Rdev      uint32
	BlkSize   uint32
	Padding   uint32
}

const fuseAttrSize = 88

// FuseEntryOut is the response for FUSE_LOOKUP.
type FuseEntryOut struct {
	NodeID         uint64
	Generation     uint64
	EntryValid     uint64
	AttrValid      uint64
	EntryValidNsec uint32
	AttrValidNsec  uint32
	Attr           FuseAttr
}

// FuseAttrOut is the response for FUSE_GETATTR.
type FuseAttrOut struct {
	AttrValid     uint64
	AttrValidNsec uint32
	Padding       uint32
	Attr          FuseAttr
}

// FuseOpenOut is the response for FUSE_OPEN/FUSE_OPENDIR.
type FuseOpenOut struct {
	Fh        uint64
	OpenFlags uint32
	Padding   uint32
}

// FuseReadIn is the request body for FUSE_READ/FUSE_READDIR.
type FuseReadIn struct {
	Fh        uint64
	Offset    uint64
	Size      uint32
	ReadFlags uint32
}

// FuseWriteIn is the request body for FUSE_WRITE.
type FuseWriteIn struct {
	Fh         uint64
	Offset     uint64
	Size       uint32
	WriteFlags uint32
}

// FuseWriteOut is the response for FUSE_WRITE.
type FuseWriteOut struct {
	Size    uint32
	Padding uint32
}

// FuseInitIn is the request body for FUSE_INIT.
type FuseInitIn struct {
	Major        uint32
	Minor        uint32
	MaxReadahead uint32
	Flags        uint32
}

// FuseInitOut is the response for FUSE_INIT.
type FuseInitOut struct {
	Major                uint32
	Minor                uint32
	MaxReadahead         uint32
	Flags                uint32
	MaxBackground        uint16
	CongestionThreshold  uint16
	MaxWrite             uint32
	TimeGran             uint32
	MaxPages             uint16
	MapAlignment         uint16
	Padding              [8]uint32
}

// FuseDirent is a directory entry in FUSE_READDIR responses.
type FuseDirent struct {
	Ino     uint64
	Off     uint64
	NameLen uint32
	Type    uint32
	Name    string
}

// fsInode tracks a host file/directory mapped to a FUSE node ID.
type fsInode struct {
	hostPath string
	nodeID   uint64
	isDir    bool
	refCount int64
}

// fsFileHandle tracks an open file or directory.
type fsFileHandle struct {
	inode    *fsInode
	file     *os.File
	isDir    bool
	dirEnts  []os.DirEntry // cached directory entries for readdir
}

// VirtioFS implements a virtio-fs device that shares host directories with the guest.
type VirtioFS struct {
	mu sync.Mutex

	deviceID string
	tag      string // filesystem tag (mount tag visible in guest)
	hostPath string // root directory on the host to share
	readOnly bool

	// Virtqueues: hiprio (high priority) and request
	hiprioVq  *Virtqueue
	requestVq *Virtqueue

	// Inode management
	inodes     map[uint64]*fsInode // nodeID -> inode
	pathToNode map[string]uint64   // host path -> nodeID
	nextNodeID uint64

	// File handle management
	handles    map[uint64]*fsFileHandle
	nextHandle uint64

	// Device state
	running atomic.Bool
	stopCh  chan struct{}

	// Stats
	opsCompleted atomic.Uint64
	bytesRead    atomic.Uint64
	bytesWritten atomic.Uint64
}

// VirtioFSOpts holds options for creating a VirtioFS device.
type VirtioFSOpts struct {
	DeviceID  string
	Tag       string // mount tag (e.g., "myshare")
	HostPath  string // host directory to share
	ReadOnly  bool
	HiprioQueue  *Virtqueue
	RequestQueue *Virtqueue
}

// NewVirtioFS creates a new virtio-fs device sharing a host directory.
func NewVirtioFS(opts VirtioFSOpts) (*VirtioFS, error) {
	if opts.HostPath == "" {
		return nil, errors.New("virtio-fs: host path is required")
	}
	if opts.Tag == "" {
		return nil, errors.New("virtio-fs: tag is required")
	}

	// Resolve and validate host path
	absPath, err := filepath.Abs(opts.HostPath)
	if err != nil {
		return nil, fmt.Errorf("virtio-fs: failed to resolve host path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("virtio-fs: host path error: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("virtio-fs: host path %s is not a directory", absPath)
	}

	dev := &VirtioFS{
		deviceID:   opts.DeviceID,
		tag:        opts.Tag,
		hostPath:   absPath,
		readOnly:   opts.ReadOnly,
		hiprioVq:   opts.HiprioQueue,
		requestVq:  opts.RequestQueue,
		inodes:     make(map[uint64]*fsInode),
		pathToNode: make(map[string]uint64),
		nextNodeID: 2, // 1 is reserved for root
		handles:    make(map[uint64]*fsFileHandle),
		nextHandle: 1,
		stopCh:     make(chan struct{}),
	}

	// Register root inode (nodeID=1)
	rootInode := &fsInode{
		hostPath: absPath,
		nodeID:   1,
		isDir:    true,
		refCount: 1,
	}
	dev.inodes[1] = rootInode
	dev.pathToNode[absPath] = 1

	return dev, nil
}

// Type returns DeviceTypeFS.
func (d *VirtioFS) Type() DeviceType {
	return DeviceTypeFS
}

// ID returns the device identifier.
func (d *VirtioFS) ID() string {
	return d.deviceID
}

// Tag returns the filesystem mount tag.
func (d *VirtioFS) Tag() string {
	return d.tag
}

// HostPath returns the shared host directory path.
func (d *VirtioFS) HostPath() string {
	return d.hostPath
}

// ReadOnly returns whether the share is read-only.
func (d *VirtioFS) ReadOnly() bool {
	return d.readOnly
}

// Start begins processing FUSE requests.
func (d *VirtioFS) Start() error {
	if d.running.Load() {
		return errors.New("virtio-fs: already running")
	}
	d.running.Store(true)
	return nil
}

// Stop halts request processing and closes all open file handles.
func (d *VirtioFS) Stop() error {
	if !d.running.CompareAndSwap(true, false) {
		return nil
	}
	close(d.stopCh)

	d.mu.Lock()
	defer d.mu.Unlock()

	// Close all open file handles
	for id, fh := range d.handles {
		if fh.file != nil {
			fh.file.Close()
		}
		delete(d.handles, id)
	}

	return nil
}

// Configure applies configuration parameters.
func (d *VirtioFS) Configure(config map[string]string) error {
	if tag, ok := config["tag"]; ok {
		d.tag = tag
	}
	return nil
}

// ProcessRequest handles a FUSE request from the guest.
// Returns the serialized FUSE response bytes.
func (d *VirtioFS) ProcessRequest(reqBuf []byte) ([]byte, error) {
	if !d.running.Load() {
		return nil, errors.New("virtio-fs: device not running")
	}

	if len(reqBuf) < fuseInHeaderSize {
		return nil, fmt.Errorf("virtio-fs: request too small (%d bytes)", len(reqBuf))
	}

	hdr := decodeFuseInHeader(reqBuf[:fuseInHeaderSize])
	body := reqBuf[fuseInHeaderSize:]

	d.opsCompleted.Add(1)

	d.mu.Lock()
	defer d.mu.Unlock()

	switch hdr.Opcode {
	case FuseOpInit:
		return d.handleInit(hdr, body)
	case FuseOpLookup:
		return d.handleLookup(hdr, body)
	case FuseOpForget:
		d.handleForget(hdr, body)
		return nil, nil // FORGET has no response
	case FuseOpGetattr:
		return d.handleGetattr(hdr)
	case FuseOpOpen:
		return d.handleOpen(hdr)
	case FuseOpRead:
		return d.handleRead(hdr, body)
	case FuseOpWrite:
		return d.handleWrite(hdr, body)
	case FuseOpRelease:
		return d.handleRelease(hdr, body)
	case FuseOpOpendir:
		return d.handleOpendir(hdr)
	case FuseOpReaddir:
		return d.handleReaddir(hdr, body)
	case FuseOpReaddirplus:
		return d.handleReaddirplus(hdr, body)
	case FuseOpReleasedir:
		return d.handleRelease(hdr, body)
	case FuseOpCreate:
		return d.handleCreate(hdr, body)
	case FuseOpMkdir:
		return d.handleMkdir(hdr, body)
	case FuseOpUnlink:
		return d.handleUnlink(hdr, body)
	case FuseOpRmdir:
		return d.handleRmdir(hdr, body)
	case FuseOpStatfs:
		return d.handleStatfs(hdr)
	case FuseOpFlush:
		return d.handleFlush(hdr, body)
	case FuseOpFsync:
		return d.handleFsync(hdr, body)
	case FuseOpSetattr:
		return d.handleSetattr(hdr, body)
	case FuseOpReadlink:
		return d.handleReadlink(hdr)
	case FuseOpRename:
		return d.handleRename(hdr, body)
	default:
		return d.errorResponse(hdr.Unique, FuseErrNosys), nil
	}
}

// handleInit processes FUSE_INIT negotiation.
func (d *VirtioFS) handleInit(hdr FuseInHeader, body []byte) ([]byte, error) {
	initOut := FuseInitOut{
		Major:                FuseKernelVersion,
		Minor:                FuseKernelMinorVersion,
		MaxReadahead:         131072, // 128 KiB
		MaxBackground:        16,
		CongestionThreshold:  12,
		MaxWrite:             1 << 20, // 1 MiB
		TimeGran:             1,       // nanosecond granularity
		MaxPages:             256,
	}

	resp := make([]byte, fuseOutHeaderSize+80)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})

	off := fuseOutHeaderSize
	binary.LittleEndian.PutUint32(resp[off:], initOut.Major)
	binary.LittleEndian.PutUint32(resp[off+4:], initOut.Minor)
	binary.LittleEndian.PutUint32(resp[off+8:], initOut.MaxReadahead)
	binary.LittleEndian.PutUint32(resp[off+12:], initOut.Flags)
	binary.LittleEndian.PutUint16(resp[off+16:], initOut.MaxBackground)
	binary.LittleEndian.PutUint16(resp[off+18:], initOut.CongestionThreshold)
	binary.LittleEndian.PutUint32(resp[off+20:], initOut.MaxWrite)
	binary.LittleEndian.PutUint32(resp[off+24:], initOut.TimeGran)
	binary.LittleEndian.PutUint16(resp[off+28:], initOut.MaxPages)
	binary.LittleEndian.PutUint16(resp[off+30:], initOut.MapAlignment)

	return resp, nil
}

// handleLookup resolves a name within a directory node.
func (d *VirtioFS) handleLookup(hdr FuseInHeader, body []byte) ([]byte, error) {
	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	// Name is null-terminated in body
	name := cstring(body)
	if name == "" {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	// Security: prevent path traversal
	if strings.Contains(name, "/") || name == ".." || name == "." {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	childPath := filepath.Join(parent.hostPath, name)

	// Ensure the resolved path is within the shared root
	if !d.isPathSafe(childPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	info, err := os.Lstat(childPath)
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	inode := d.getOrCreateInode(childPath, info.IsDir())

	attr := d.fileInfoToAttr(info, inode.nodeID)
	entry := FuseEntryOut{
		NodeID:         inode.nodeID,
		Generation:     1,
		EntryValid:     1, // 1 second cache
		AttrValid:      1,
		EntryValidNsec: 0,
		AttrValidNsec:  0,
		Attr:           attr,
	}

	return d.encodeEntryOut(hdr.Unique, entry), nil
}

// handleForget decrements the reference count for a node.
func (d *VirtioFS) handleForget(hdr FuseInHeader, body []byte) {
	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return
	}

	nlookup := uint64(1)
	if len(body) >= 8 {
		nlookup = binary.LittleEndian.Uint64(body[:8])
	}

	inode.refCount -= int64(nlookup)
	if inode.refCount <= 0 && hdr.NodeID != 1 {
		delete(d.pathToNode, inode.hostPath)
		delete(d.inodes, hdr.NodeID)
	}
}

// handleGetattr returns file attributes for a node.
func (d *VirtioFS) handleGetattr(hdr FuseInHeader) ([]byte, error) {
	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	info, err := os.Lstat(inode.hostPath)
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	attr := d.fileInfoToAttr(info, inode.nodeID)

	resp := make([]byte, fuseOutHeaderSize+16+fuseAttrSize)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})

	off := fuseOutHeaderSize
	binary.LittleEndian.PutUint64(resp[off:], 1) // attr_valid
	binary.LittleEndian.PutUint32(resp[off+8:], 0) // attr_valid_nsec
	// 4 bytes padding
	encodeAttr(resp[off+16:], attr)

	return resp, nil
}

// handleOpen opens a file for reading/writing.
func (d *VirtioFS) handleOpen(hdr FuseInHeader) ([]byte, error) {
	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	flags := os.O_RDONLY
	if !d.readOnly {
		flags = os.O_RDWR
	}

	f, err := os.OpenFile(inode.hostPath, flags, 0)
	if err != nil {
		// Fall back to read-only if read-write fails
		f, err = os.OpenFile(inode.hostPath, os.O_RDONLY, 0)
		if err != nil {
			return d.errorResponse(hdr.Unique, FuseErrAccess), nil
		}
	}

	fh := &fsFileHandle{
		inode: inode,
		file:  f,
		isDir: false,
	}

	handle := d.nextHandle
	d.nextHandle++
	d.handles[handle] = fh

	return d.encodeOpenOut(hdr.Unique, handle, FuseOpenFlagKeepCache), nil
}

// handleRead reads data from an open file.
func (d *VirtioFS) handleRead(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) < 24 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	readIn := FuseReadIn{
		Fh:     binary.LittleEndian.Uint64(body[0:8]),
		Offset: binary.LittleEndian.Uint64(body[8:16]),
		Size:   binary.LittleEndian.Uint32(body[16:20]),
	}

	fh, ok := d.handles[readIn.Fh]
	if !ok || fh.file == nil {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	buf := make([]byte, readIn.Size)
	n, err := fh.file.ReadAt(buf, int64(readIn.Offset))
	if err != nil && err != io.EOF {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	d.bytesRead.Add(uint64(n))

	resp := make([]byte, fuseOutHeaderSize+n)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	copy(resp[fuseOutHeaderSize:], buf[:n])

	return resp, nil
}

// handleWrite writes data to an open file.
func (d *VirtioFS) handleWrite(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	if len(body) < 24 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	writeIn := FuseWriteIn{
		Fh:     binary.LittleEndian.Uint64(body[0:8]),
		Offset: binary.LittleEndian.Uint64(body[8:16]),
		Size:   binary.LittleEndian.Uint32(body[16:20]),
	}

	fh, ok := d.handles[writeIn.Fh]
	if !ok || fh.file == nil {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	// Data follows the write header (after 40 bytes total of WriteIn struct)
	dataOffset := 40 // FuseWriteIn is actually 40 bytes with all fields
	if len(body) < dataOffset {
		dataOffset = 24
	}
	data := body[dataOffset:]
	if uint32(len(data)) > writeIn.Size {
		data = data[:writeIn.Size]
	}

	n, err := fh.file.WriteAt(data, int64(writeIn.Offset))
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	d.bytesWritten.Add(uint64(n))

	resp := make([]byte, fuseOutHeaderSize+8)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	binary.LittleEndian.PutUint32(resp[fuseOutHeaderSize:], uint32(n))

	return resp, nil
}

// handleRelease closes an open file or directory handle.
func (d *VirtioFS) handleRelease(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) < 8 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	fhID := binary.LittleEndian.Uint64(body[0:8])
	fh, ok := d.handles[fhID]
	if ok {
		if fh.file != nil {
			fh.file.Close()
		}
		delete(d.handles, fhID)
	}

	return d.emptyResponse(hdr.Unique), nil
}

// handleOpendir opens a directory for reading.
func (d *VirtioFS) handleOpendir(hdr FuseInHeader) ([]byte, error) {
	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	entries, err := os.ReadDir(inode.hostPath)
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	fh := &fsFileHandle{
		inode:   inode,
		isDir:   true,
		dirEnts: entries,
	}

	handle := d.nextHandle
	d.nextHandle++
	d.handles[handle] = fh

	return d.encodeOpenOut(hdr.Unique, handle, 0), nil
}

// handleReaddir returns directory entries.
func (d *VirtioFS) handleReaddir(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) < 24 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	readIn := FuseReadIn{
		Fh:     binary.LittleEndian.Uint64(body[0:8]),
		Offset: binary.LittleEndian.Uint64(body[8:16]),
		Size:   binary.LittleEndian.Uint32(body[16:20]),
	}

	fh, ok := d.handles[readIn.Fh]
	if !ok || !fh.isDir {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	var buf []byte
	startIdx := int(readIn.Offset)

	for i := startIdx; i < len(fh.dirEnts); i++ {
		entry := fh.dirEnts[i]
		dirent := d.encodeDirent(entry.Name(), uint64(i+1), entry.Type())
		if uint32(len(buf)+len(dirent)) > readIn.Size {
			break
		}
		buf = append(buf, dirent...)
	}

	resp := make([]byte, fuseOutHeaderSize+len(buf))
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	copy(resp[fuseOutHeaderSize:], buf)

	return resp, nil
}

// handleReaddirplus returns directory entries with attributes.
func (d *VirtioFS) handleReaddirplus(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) < 24 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	readIn := FuseReadIn{
		Fh:     binary.LittleEndian.Uint64(body[0:8]),
		Offset: binary.LittleEndian.Uint64(body[8:16]),
		Size:   binary.LittleEndian.Uint32(body[16:20]),
	}

	fh, ok := d.handles[readIn.Fh]
	if !ok || !fh.isDir {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	var buf []byte
	startIdx := int(readIn.Offset)

	for i := startIdx; i < len(fh.dirEnts); i++ {
		entry := fh.dirEnts[i]
		childPath := filepath.Join(parent.hostPath, entry.Name())

		info, err := entry.Info()
		if err != nil {
			continue
		}

		childInode := d.getOrCreateInode(childPath, info.IsDir())
		attr := d.fileInfoToAttr(info, childInode.nodeID)

		entryOut := FuseEntryOut{
			NodeID:     childInode.nodeID,
			Generation: 1,
			EntryValid: 1,
			AttrValid:  1,
			Attr:       attr,
		}

		direntPlus := d.encodeDirentPlus(entry.Name(), uint64(i+1), entryOut)
		if uint32(len(buf)+len(direntPlus)) > readIn.Size {
			break
		}
		buf = append(buf, direntPlus...)
	}

	resp := make([]byte, fuseOutHeaderSize+len(buf))
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	copy(resp[fuseOutHeaderSize:], buf)

	return resp, nil
}

// handleCreate creates and opens a new file.
func (d *VirtioFS) handleCreate(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	// CreateIn: flags(4) + mode(4) + umask(4) + padding(4) + name
	if len(body) < 16 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}
	mode := binary.LittleEndian.Uint32(body[4:8])
	name := cstring(body[16:])
	if name == "" || strings.Contains(name, "/") {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	childPath := filepath.Join(parent.hostPath, name)
	if !d.isPathSafe(childPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	f, err := os.OpenFile(childPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, os.FileMode(mode&0777))
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	info, err := f.Stat()
	if err != nil {
		f.Close()
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	inode := d.getOrCreateInode(childPath, false)
	attr := d.fileInfoToAttr(info, inode.nodeID)

	handle := d.nextHandle
	d.nextHandle++
	d.handles[handle] = &fsFileHandle{
		inode: inode,
		file:  f,
		isDir: false,
	}

	entry := FuseEntryOut{
		NodeID:     inode.nodeID,
		Generation: 1,
		EntryValid: 1,
		AttrValid:  1,
		Attr:       attr,
	}

	// Response: EntryOut + OpenOut
	entryBuf := d.marshalEntryOut(entry)
	openBuf := make([]byte, 16)
	binary.LittleEndian.PutUint64(openBuf[0:8], handle)
	binary.LittleEndian.PutUint32(openBuf[8:12], FuseOpenFlagKeepCache)

	resp := make([]byte, fuseOutHeaderSize+len(entryBuf)+16)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	copy(resp[fuseOutHeaderSize:], entryBuf)
	copy(resp[fuseOutHeaderSize+len(entryBuf):], openBuf)

	return resp, nil
}

// handleMkdir creates a new directory.
func (d *VirtioFS) handleMkdir(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	// MkdirIn: mode(4) + umask(4) + name
	if len(body) < 8 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}
	mode := binary.LittleEndian.Uint32(body[0:4])
	name := cstring(body[8:])
	if name == "" || strings.Contains(name, "/") {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	childPath := filepath.Join(parent.hostPath, name)
	if !d.isPathSafe(childPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	if err := os.Mkdir(childPath, os.FileMode(mode&0777)); err != nil {
		if os.IsExist(err) {
			return d.errorResponse(hdr.Unique, FuseErrExist), nil
		}
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	info, err := os.Lstat(childPath)
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	inode := d.getOrCreateInode(childPath, true)
	attr := d.fileInfoToAttr(info, inode.nodeID)

	entry := FuseEntryOut{
		NodeID:     inode.nodeID,
		Generation: 1,
		EntryValid: 1,
		AttrValid:  1,
		Attr:       attr,
	}

	return d.encodeEntryOut(hdr.Unique, entry), nil
}

// handleUnlink removes a file.
func (d *VirtioFS) handleUnlink(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	name := cstring(body)
	if name == "" || strings.Contains(name, "/") {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	childPath := filepath.Join(parent.hostPath, name)
	if !d.isPathSafe(childPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	if err := os.Remove(childPath); err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	// Clean up inode
	if nodeID, ok := d.pathToNode[childPath]; ok {
		delete(d.inodes, nodeID)
		delete(d.pathToNode, childPath)
	}

	return d.emptyResponse(hdr.Unique), nil
}

// handleRmdir removes a directory.
func (d *VirtioFS) handleRmdir(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	name := cstring(body)
	if name == "" || strings.Contains(name, "/") {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	childPath := filepath.Join(parent.hostPath, name)
	if !d.isPathSafe(childPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	if err := os.Remove(childPath); err != nil {
		if os.IsNotExist(err) {
			return d.errorResponse(hdr.Unique, FuseErrNoent), nil
		}
		return d.errorResponse(hdr.Unique, FuseErrEmpty), nil
	}

	if nodeID, ok := d.pathToNode[childPath]; ok {
		delete(d.inodes, nodeID)
		delete(d.pathToNode, childPath)
	}

	return d.emptyResponse(hdr.Unique), nil
}

// handleStatfs returns filesystem statistics.
func (d *VirtioFS) handleStatfs(hdr FuseInHeader) ([]byte, error) {
	// Return generic filesystem stats
	resp := make([]byte, fuseOutHeaderSize+80)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})

	off := fuseOutHeaderSize
	binary.LittleEndian.PutUint64(resp[off:], 1<<20)    // blocks
	binary.LittleEndian.PutUint64(resp[off+8:], 1<<19)  // bfree
	binary.LittleEndian.PutUint64(resp[off+16:], 1<<19) // bavail
	binary.LittleEndian.PutUint64(resp[off+24:], 1<<20) // files
	binary.LittleEndian.PutUint64(resp[off+32:], 1<<19) // ffree
	binary.LittleEndian.PutUint32(resp[off+40:], 4096)  // bsize
	binary.LittleEndian.PutUint32(resp[off+44:], 255)   // namelen
	binary.LittleEndian.PutUint32(resp[off+48:], 4096)  // frsize

	return resp, nil
}

// handleFlush flushes a file handle.
func (d *VirtioFS) handleFlush(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) >= 8 {
		fhID := binary.LittleEndian.Uint64(body[0:8])
		if fh, ok := d.handles[fhID]; ok && fh.file != nil {
			fh.file.Sync()
		}
	}
	return d.emptyResponse(hdr.Unique), nil
}

// handleFsync syncs a file to disk.
func (d *VirtioFS) handleFsync(hdr FuseInHeader, body []byte) ([]byte, error) {
	if len(body) >= 8 {
		fhID := binary.LittleEndian.Uint64(body[0:8])
		if fh, ok := d.handles[fhID]; ok && fh.file != nil {
			fh.file.Sync()
		}
	}
	return d.emptyResponse(hdr.Unique), nil
}

// handleSetattr modifies file attributes.
func (d *VirtioFS) handleSetattr(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	// Apply setattr operations based on valid mask
	if len(body) >= 4 {
		valid := binary.LittleEndian.Uint32(body[0:4])

		// FATTR_SIZE = 0x8 — truncate
		if valid&0x8 != 0 && len(body) >= 16 {
			size := binary.LittleEndian.Uint64(body[8:16])
			if err := os.Truncate(inode.hostPath, int64(size)); err != nil {
				return d.errorResponse(hdr.Unique, FuseErrIO), nil
			}
		}

		// FATTR_MODE = 0x1
		if valid&0x1 != 0 && len(body) >= 8 {
			mode := binary.LittleEndian.Uint32(body[4:8])
			os.Chmod(inode.hostPath, os.FileMode(mode&0777))
		}
	}

	// Return current attributes
	return d.handleGetattr(hdr)
}

// handleReadlink reads a symbolic link target.
func (d *VirtioFS) handleReadlink(hdr FuseInHeader) ([]byte, error) {
	inode, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	target, err := os.Readlink(inode.hostPath)
	if err != nil {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	resp := make([]byte, fuseOutHeaderSize+len(target))
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: hdr.Unique,
	})
	copy(resp[fuseOutHeaderSize:], target)

	return resp, nil
}

// handleRename renames/moves a file or directory.
func (d *VirtioFS) handleRename(hdr FuseInHeader, body []byte) ([]byte, error) {
	if d.readOnly {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	parent, ok := d.inodes[hdr.NodeID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	// RenameIn: newdir(8) + oldname + \0 + newname + \0
	if len(body) < 8 {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	newDirID := binary.LittleEndian.Uint64(body[0:8])
	newParent, ok := d.inodes[newDirID]
	if !ok {
		return d.errorResponse(hdr.Unique, FuseErrNoent), nil
	}

	names := body[8:]
	oldName := cstring(names)
	if oldName == "" {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	remaining := names[len(oldName)+1:]
	newName := cstring(remaining)
	if newName == "" {
		return d.errorResponse(hdr.Unique, FuseErrInval), nil
	}

	oldPath := filepath.Join(parent.hostPath, oldName)
	newPath := filepath.Join(newParent.hostPath, newName)

	if !d.isPathSafe(oldPath) || !d.isPathSafe(newPath) {
		return d.errorResponse(hdr.Unique, FuseErrAccess), nil
	}

	if err := os.Rename(oldPath, newPath); err != nil {
		return d.errorResponse(hdr.Unique, FuseErrIO), nil
	}

	// Update inode paths
	if nodeID, ok := d.pathToNode[oldPath]; ok {
		delete(d.pathToNode, oldPath)
		d.pathToNode[newPath] = nodeID
		if inode, ok := d.inodes[nodeID]; ok {
			inode.hostPath = newPath
		}
	}

	return d.emptyResponse(hdr.Unique), nil
}

// Helper methods

// getOrCreateInode finds or creates an inode for a host path.
func (d *VirtioFS) getOrCreateInode(hostPath string, isDir bool) *fsInode {
	if nodeID, ok := d.pathToNode[hostPath]; ok {
		inode := d.inodes[nodeID]
		inode.refCount++
		return inode
	}

	nodeID := d.nextNodeID
	d.nextNodeID++

	inode := &fsInode{
		hostPath: hostPath,
		nodeID:   nodeID,
		isDir:    isDir,
		refCount: 1,
	}

	d.inodes[nodeID] = inode
	d.pathToNode[hostPath] = nodeID

	return inode
}

// isPathSafe ensures the path is within the shared root directory.
func (d *VirtioFS) isPathSafe(path string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	// Resolve symlinks to prevent traversal via symlinks
	resolved, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		// Parent dir might not exist yet (for create operations)
		// Check if the directory portion is within root
		return strings.HasPrefix(absPath, d.hostPath)
	}
	return strings.HasPrefix(resolved, d.hostPath) || resolved == d.hostPath
}

// fileInfoToAttr converts os.FileInfo to FuseAttr.
func (d *VirtioFS) fileInfoToAttr(info os.FileInfo, nodeID uint64) FuseAttr {
	mode := uint32(info.Mode().Perm())
	if info.IsDir() {
		mode |= 0o40000 // S_IFDIR
	} else if info.Mode()&os.ModeSymlink != 0 {
		mode |= 0o120000 // S_IFLNK
	} else {
		mode |= 0o100000 // S_IFREG
	}

	nlink := uint32(1)
	if info.IsDir() {
		nlink = 2
	}

	mtime := info.ModTime()
	blocks := (uint64(info.Size()) + 511) / 512

	return FuseAttr{
		Ino:       nodeID,
		Size:      uint64(info.Size()),
		Blocks:    blocks,
		Atime:     uint64(mtime.Unix()),
		Mtime:     uint64(mtime.Unix()),
		Ctime:     uint64(mtime.Unix()),
		AtimeNsec: uint32(mtime.Nanosecond()),
		MtimeNsec: uint32(mtime.Nanosecond()),
		CtimeNsec: uint32(mtime.Nanosecond()),
		Mode:      mode,
		Nlink:     nlink,
		BlkSize:   4096,
	}
}

// errorResponse creates a FUSE error response.
func (d *VirtioFS) errorResponse(unique uint64, errno int32) []byte {
	resp := make([]byte, fuseOutHeaderSize)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    fuseOutHeaderSize,
		Error:  errno,
		Unique: unique,
	})
	return resp
}

// emptyResponse creates a success response with no body.
func (d *VirtioFS) emptyResponse(unique uint64) []byte {
	resp := make([]byte, fuseOutHeaderSize)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    fuseOutHeaderSize,
		Error:  FuseErrOK,
		Unique: unique,
	})
	return resp
}

// encodeEntryOut creates a FUSE_LOOKUP response.
func (d *VirtioFS) encodeEntryOut(unique uint64, entry FuseEntryOut) []byte {
	entryBuf := d.marshalEntryOut(entry)
	resp := make([]byte, fuseOutHeaderSize+len(entryBuf))
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: unique,
	})
	copy(resp[fuseOutHeaderSize:], entryBuf)
	return resp
}

// marshalEntryOut serializes a FuseEntryOut.
func (d *VirtioFS) marshalEntryOut(entry FuseEntryOut) []byte {
	buf := make([]byte, 40+fuseAttrSize) // 40 bytes header + attr
	binary.LittleEndian.PutUint64(buf[0:8], entry.NodeID)
	binary.LittleEndian.PutUint64(buf[8:16], entry.Generation)
	binary.LittleEndian.PutUint64(buf[16:24], entry.EntryValid)
	binary.LittleEndian.PutUint64(buf[24:32], entry.AttrValid)
	binary.LittleEndian.PutUint32(buf[32:36], entry.EntryValidNsec)
	binary.LittleEndian.PutUint32(buf[36:40], entry.AttrValidNsec)
	encodeAttr(buf[40:], entry.Attr)
	return buf
}

// encodeOpenOut creates a FUSE_OPEN response.
func (d *VirtioFS) encodeOpenOut(unique uint64, handle uint64, flags uint32) []byte {
	resp := make([]byte, fuseOutHeaderSize+16)
	encodeFuseOutHeader(resp, FuseOutHeader{
		Len:    uint32(len(resp)),
		Error:  FuseErrOK,
		Unique: unique,
	})
	off := fuseOutHeaderSize
	binary.LittleEndian.PutUint64(resp[off:], handle)
	binary.LittleEndian.PutUint32(resp[off+8:], flags)
	return resp
}

// encodeDirent creates a FUSE directory entry.
func (d *VirtioFS) encodeDirent(name string, offset uint64, fileType fs.FileMode) []byte {
	nameBytes := []byte(name)
	// dirent: ino(8) + off(8) + namelen(4) + type(4) + name + padding to 8-byte align
	entryLen := 24 + len(nameBytes)
	padded := (entryLen + 7) &^ 7 // 8-byte alignment

	buf := make([]byte, padded)
	binary.LittleEndian.PutUint64(buf[0:8], offset)   // ino (use offset as placeholder)
	binary.LittleEndian.PutUint64(buf[8:16], offset)   // off
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(nameBytes)))
	binary.LittleEndian.PutUint32(buf[20:24], fuseFileType(fileType))
	copy(buf[24:], nameBytes)

	return buf
}

// encodeDirentPlus creates a FUSE directory entry with attributes (READDIRPLUS).
func (d *VirtioFS) encodeDirentPlus(name string, offset uint64, entry FuseEntryOut) []byte {
	entryBuf := d.marshalEntryOut(entry)
	nameBytes := []byte(name)

	// direntplus: entry_out + ino(8) + off(8) + namelen(4) + type(4) + name + padding
	direntLen := 24 + len(nameBytes)
	direntPadded := (direntLen + 7) &^ 7
	totalLen := len(entryBuf) + direntPadded

	buf := make([]byte, totalLen)
	copy(buf, entryBuf)

	off := len(entryBuf)
	binary.LittleEndian.PutUint64(buf[off:off+8], entry.Attr.Ino)
	binary.LittleEndian.PutUint64(buf[off+8:off+16], offset)
	binary.LittleEndian.PutUint32(buf[off+16:off+20], uint32(len(nameBytes)))
	if entry.Attr.Mode&0o40000 != 0 {
		binary.LittleEndian.PutUint32(buf[off+20:off+24], 4) // DT_DIR
	} else {
		binary.LittleEndian.PutUint32(buf[off+20:off+24], 8) // DT_REG
	}
	copy(buf[off+24:], nameBytes)

	return buf
}

// fuseFileType converts os.FileMode to FUSE dirent type.
func fuseFileType(mode fs.FileMode) uint32 {
	switch {
	case mode.IsDir():
		return 4 // DT_DIR
	case mode&os.ModeSymlink != 0:
		return 10 // DT_LNK
	default:
		return 8 // DT_REG
	}
}

// Stats returns device I/O statistics.
func (d *VirtioFS) Stats() VirtioFSStats {
	return VirtioFSStats{
		OpsCompleted: d.opsCompleted.Load(),
		BytesRead:    d.bytesRead.Load(),
		BytesWritten: d.bytesWritten.Load(),
	}
}

// VirtioFSStats holds filesystem device statistics.
type VirtioFSStats struct {
	OpsCompleted uint64
	BytesRead    uint64
	BytesWritten uint64
}

// MountInfo returns information about this filesystem share.
type MountInfo struct {
	Tag      string `json:"tag"`
	HostPath string `json:"host_path"`
	ReadOnly bool   `json:"read_only"`
	Active   bool   `json:"active"`
}

// GetMountInfo returns mount information for this device.
func (d *VirtioFS) GetMountInfo() MountInfo {
	return MountInfo{
		Tag:      d.tag,
		HostPath: d.hostPath,
		ReadOnly: d.readOnly,
		Active:   d.running.Load(),
	}
}

// OpenHandleCount returns the number of open file handles.
func (d *VirtioFS) OpenHandleCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.handles)
}

// InodeCount returns the number of tracked inodes.
func (d *VirtioFS) InodeCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.inodes)
}

// Encoding helpers

func decodeFuseInHeader(buf []byte) FuseInHeader {
	return FuseInHeader{
		Len:     binary.LittleEndian.Uint32(buf[0:4]),
		Opcode:  binary.LittleEndian.Uint32(buf[4:8]),
		Unique:  binary.LittleEndian.Uint64(buf[8:16]),
		NodeID:  binary.LittleEndian.Uint64(buf[16:24]),
		UID:     binary.LittleEndian.Uint32(buf[24:28]),
		GID:     binary.LittleEndian.Uint32(buf[28:32]),
		PID:     binary.LittleEndian.Uint32(buf[32:36]),
		Padding: binary.LittleEndian.Uint32(buf[36:40]),
	}
}

func encodeFuseOutHeader(buf []byte, hdr FuseOutHeader) {
	binary.LittleEndian.PutUint32(buf[0:4], hdr.Len)
	binary.LittleEndian.PutUint32(buf[4:8], uint32(hdr.Error))
	binary.LittleEndian.PutUint64(buf[8:16], hdr.Unique)
}

func encodeAttr(buf []byte, attr FuseAttr) {
	binary.LittleEndian.PutUint64(buf[0:8], attr.Ino)
	binary.LittleEndian.PutUint64(buf[8:16], attr.Size)
	binary.LittleEndian.PutUint64(buf[16:24], attr.Blocks)
	binary.LittleEndian.PutUint64(buf[24:32], attr.Atime)
	binary.LittleEndian.PutUint64(buf[32:40], attr.Mtime)
	binary.LittleEndian.PutUint64(buf[40:48], attr.Ctime)
	binary.LittleEndian.PutUint32(buf[48:52], attr.AtimeNsec)
	binary.LittleEndian.PutUint32(buf[52:56], attr.MtimeNsec)
	binary.LittleEndian.PutUint32(buf[56:60], attr.CtimeNsec)
	binary.LittleEndian.PutUint32(buf[60:64], attr.Mode)
	binary.LittleEndian.PutUint32(buf[64:68], attr.Nlink)
	binary.LittleEndian.PutUint32(buf[68:72], attr.UID)
	binary.LittleEndian.PutUint32(buf[72:76], attr.GID)
	binary.LittleEndian.PutUint32(buf[76:80], attr.Rdev)
	binary.LittleEndian.PutUint32(buf[80:84], attr.BlkSize)
	binary.LittleEndian.PutUint32(buf[84:88], attr.Padding)
}

// cstring extracts a null-terminated string from a byte slice.
func cstring(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// SetupMounts creates VirtioFS devices for all mount configurations.
func SetupMounts(mounts []MountConfig, readOnly bool) ([]*VirtioFS, error) {
	var devices []*VirtioFS

	for i, m := range mounts {
		tag := fmt.Sprintf("mount%d", i)
		if m.Tag != "" {
			tag = m.Tag
		}

		ro := readOnly || m.ReadOnly
		dev, err := NewVirtioFS(VirtioFSOpts{
			DeviceID: fmt.Sprintf("fs-%s", tag),
			Tag:      tag,
			HostPath: m.HostPath,
			ReadOnly: ro,
		})
		if err != nil {
			// Clean up already created devices
			for _, d := range devices {
				d.Stop()
			}
			return nil, fmt.Errorf("virtio-fs: mount %d (%s): %w", i, m.HostPath, err)
		}

		devices = append(devices, dev)
	}

	return devices, nil
}

// MountConfig describes a host-to-guest directory mount.
type MountConfig struct {
	HostPath  string `yaml:"host" json:"host"`
	GuestPath string `yaml:"guest" json:"guest"`
	ReadOnly  bool   `yaml:"readonly" json:"readonly"`
	Tag       string `yaml:"tag,omitempty" json:"tag,omitempty"`
}
