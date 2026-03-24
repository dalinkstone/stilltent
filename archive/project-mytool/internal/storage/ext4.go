// Package storage provides disk image management for tent sandboxes.
// This file implements a pure-Go ext4 filesystem formatter that creates
// minimal but valid ext4 filesystems without requiring mkfs.ext4 or root
// access. This is critical for macOS support where Linux filesystem tools
// are not available.
package storage

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"time"
)

// ext4 on-disk format constants
const (
	ext4SuperblockOffset = 1024
	ext4SuperblockSize   = 1024
	ext4Magic            = 0xEF53
	ext4BlockSize        = 4096
	ext4InodeSize        = 256
	ext4InodesPerGroup   = 8192
	ext4FirstInode       = 11 // first non-reserved inode
	ext4RootInode        = 2
	ext4LostFoundInode   = 11
	ext4GoodOldRevision  = 0
	ext4DynamicRevision  = 1

	// Feature flags
	ext4FeatureCompatDirIndex   uint32 = 0x0020
	ext4FeatureIncompatFiletype uint32 = 0x0002
	ext4FeatureIncompatExtents  uint32 = 0x0040
	ext4FeatureIncompatFlex     uint32 = 0x0200
	ext4FeatureRoCompatSparse   uint32 = 0x0001
	ext4FeatureRoCompatLargeFile uint32 = 0x0002
	ext4FeatureRoCompatHugeFile uint32 = 0x0008

	// Inode flags
	ext4InodeFlagExtents uint32 = 0x00080000

	// Directory entry file types
	ext4FtUnknown  = 0
	ext4FtRegFile  = 1
	ext4FtDir      = 2
	ext4FtChrDev   = 3
	ext4FtBlkDev   = 4
	ext4FtFifo     = 5
	ext4FtSock     = 6
	ext4FtSymlink  = 7
)

// ext4Superblock represents the ext4 on-disk superblock structure.
// Fields are written in little-endian byte order.
type ext4Superblock struct {
	InodesCount         uint32
	BlocksCountLo       uint32
	RBlocksCountLo      uint32
	FreeBlocksCountLo   uint32
	FreeInodesCount     uint32
	FirstDataBlock      uint32
	LogBlockSize        uint32
	LogClusterSize      uint32
	BlocksPerGroup      uint32
	ClustersPerGroup    uint32
	InodesPerGroup      uint32
	Mtime               uint32
	Wtime               uint32
	MntCount            uint16
	MaxMntCount         uint16
	Magic               uint16
	State               uint16
	Errors              uint16
	MinorRevLevel       uint16
	Lastcheck           uint32
	Checkinterval       uint32
	CreatorOS           uint32
	RevLevel            uint32
	DefResuid           uint16
	DefResgid           uint16
	// Dynamic revision fields
	FirstIno            uint32
	InodeSize           uint16
	BlockGroupNr        uint16
	FeatureCompat       uint32
	FeatureIncompat     uint32
	FeatureRoCompat     uint32
	UUID                [16]byte
	VolumeName          [16]byte
	LastMounted         [64]byte
	AlgorithmUsageBmp   uint32
	// Performance hints
	PreallocBlocks      uint8
	PreallocDirBlocks   uint8
	ReservedGdtBlocks   uint16
	// Journaling support (unused in our minimal fs)
	JournalUUID         [16]byte
	JournalInum         uint32
	JournalDev          uint32
	LastOrphan          uint32
	HashSeed            [4]uint32
	DefHashVersion      uint8
	JnlBackupType       uint8
	DescSize            uint16
	DefaultMountOpts    uint32
	FirstMetaBg         uint32
	MkfsTime            uint32
	JnlBlocks           [17]uint32
	// 64-bit support
	BlocksCountHi       uint32
	RBlocksCountHi      uint32
	FreeBlocksCountHi   uint32
	MinExtraIsize       uint16
	WantExtraIsize      uint16
	Flags               uint32
	RaidStride          uint16
	MmpInterval         uint16
	MmpBlock            uint64
	RaidStripeWidth     uint32
	LogGroupsPerFlex    uint8
	ChecksumType        uint8
	Pad0                uint16
	KbytesWritten       uint64
}

// ext4GroupDesc represents a block group descriptor (32-byte version).
type ext4GroupDesc struct {
	BlockBitmapLo      uint32
	InodeBitmapLo      uint32
	InodeTableLo       uint32
	FreeBlocksCountLo  uint16
	FreeInodesCountLo  uint16
	UsedDirsCountLo    uint16
	Flags              uint16
	ExcludeBitmapLo    uint32
	BlockBitmapCsumLo  uint16
	InodeBitmapCsumLo  uint16
	ItableUnusedLo     uint16
	Checksum           uint16
}

// ext4Inode represents a minimal ext4 inode (128 bytes base + extra).
type ext4Inode struct {
	Mode        uint16
	Uid         uint16
	SizeLo      uint32
	Atime       uint32
	Ctime       uint32
	Mtime       uint32
	Dtime       uint32
	Gid         uint16
	LinksCount  uint16
	BlocksLo    uint32
	Flags       uint32
	Osd1        uint32
	Block       [60]byte // 15 x 4 bytes or extent tree
	Generation  uint32
	FileACLLo   uint32
	SizeHigh    uint32
	ObsoFaddr   uint32
	Osd2        [12]byte
}

// ext4ExtentHeader is the header of an extent tree node.
type ext4ExtentHeader struct {
	Magic      uint16
	Entries    uint16
	Max        uint16
	Depth      uint16
	Generation uint32
}

// ext4Extent represents a leaf node in the extent tree.
type ext4Extent struct {
	Block    uint32
	Len      uint16
	StartHi  uint16
	StartLo  uint32
}

// ext4DirEntry2 is an ext4 directory entry with file type.
type ext4DirEntry2 struct {
	Inode    uint32
	RecLen   uint16
	NameLen  uint8
	FileType uint8
}

// Ext4FormatConfig configures the ext4 formatter.
type Ext4FormatConfig struct {
	// Label is the volume label (max 16 chars).
	Label string
	// UUID is the filesystem UUID. If zero, one is generated.
	UUID [16]byte
}

// FormatExt4 creates a valid ext4 filesystem on the file at the given path.
// The file must already exist and be at least 4 MB in size.
// This is a pure-Go implementation that does not require mkfs.ext4 or root.
func FormatExt4(path string, cfg *Ext4FormatConfig) error {
	if cfg == nil {
		cfg = &Ext4FormatConfig{}
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open image: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat image: %w", err)
	}

	totalBytes := fi.Size()
	if totalBytes < 4*1024*1024 {
		return fmt.Errorf("image too small (%d bytes), minimum 4 MB", totalBytes)
	}

	totalBlocks := uint32(totalBytes / ext4BlockSize)
	blocksPerGroup := uint32(8 * ext4BlockSize) // 1 block bitmap covers this many blocks
	numGroups := (totalBlocks + blocksPerGroup - 1) / blocksPerGroup

	// Recalculate total blocks to not exceed file size
	if uint64(numGroups)*uint64(blocksPerGroup) > uint64(totalBlocks) {
		// last group may be smaller
	}

	inodesPerGroup := uint32(ext4InodesPerGroup)
	// Ensure we don't allocate more inode table blocks than fit in a group
	inodeTableBlocks := (inodesPerGroup * ext4InodeSize) / ext4BlockSize
	if inodeTableBlocks > blocksPerGroup/4 {
		inodesPerGroup = (blocksPerGroup / 4) * (ext4BlockSize / ext4InodeSize)
	}
	totalInodes := numGroups * inodesPerGroup

	now := uint32(time.Now().Unix())

	// Generate UUID if not provided
	uuid := cfg.UUID
	if uuid == [16]byte{} {
		uuid = generateUUID(now)
	}

	// Build superblock
	sb := &ext4Superblock{
		InodesCount:       totalInodes,
		BlocksCountLo:     totalBlocks,
		RBlocksCountLo:    totalBlocks / 20, // 5% reserved
		FreeBlocksCountLo: totalBlocks,      // updated below
		FreeInodesCount:   totalInodes - ext4FirstInode,
		FirstDataBlock:    0, // block size 4096 -> first data block is 0
		LogBlockSize:      2, // log2(4096/1024) = 2
		LogClusterSize:    2,
		BlocksPerGroup:    blocksPerGroup,
		ClustersPerGroup:  blocksPerGroup,
		InodesPerGroup:    inodesPerGroup,
		Mtime:             0,
		Wtime:             now,
		MntCount:          0,
		MaxMntCount:       0xFFFF,
		Magic:             ext4Magic,
		State:             1, // EXT4_VALID_FS
		Errors:            1, // EXT4_ERRORS_CONTINUE
		Lastcheck:         now,
		CreatorOS:         0, // EXT2_OS_LINUX
		RevLevel:          ext4DynamicRevision,
		DefResuid:         0,
		DefResgid:         0,
		FirstIno:          uint32(ext4FirstInode),
		InodeSize:         ext4InodeSize,
		FeatureCompat:     ext4FeatureCompatDirIndex,
		FeatureIncompat:   ext4FeatureIncompatFiletype | ext4FeatureIncompatExtents,
		FeatureRoCompat:   ext4FeatureRoCompatSparse | ext4FeatureRoCompatLargeFile | ext4FeatureRoCompatHugeFile,
		MkfsTime:          now,
		DescSize:          0, // 32-byte group descriptors
	}

	copy(sb.UUID[:], uuid[:])
	if cfg.Label != "" {
		lbl := cfg.Label
		if len(lbl) > 16 {
			lbl = lbl[:16]
		}
		copy(sb.VolumeName[:], lbl)
	}

	// Hash seed for dir indexing
	sb.HashSeed[0] = now ^ 0xDEADBEEF
	sb.HashSeed[1] = now ^ 0xCAFEBABE
	sb.HashSeed[2] = now ^ 0x12345678
	sb.HashSeed[3] = now ^ 0x87654321
	sb.DefHashVersion = 1 // DX_HASH_HALF_MD4

	// Layout the block groups
	// For each group: superblock backup (if sparse), group descriptors,
	// block bitmap (1 block), inode bitmap (1 block), inode table (N blocks)
	gdtBlocks := (numGroups*32 + ext4BlockSize - 1) / ext4BlockSize

	type groupLayout struct {
		blockBitmap  uint32
		inodeBitmap  uint32
		inodeTable   uint32
		dataStart    uint32
		freeBlocks   uint32
		totalBlocks  uint32 // blocks in this group
	}

	groups := make([]groupLayout, numGroups)
	totalUsed := uint32(0)

	for g := uint32(0); g < numGroups; g++ {
		groupStart := g * blocksPerGroup
		groupEnd := groupStart + blocksPerGroup
		if groupEnd > totalBlocks {
			groupEnd = totalBlocks
		}
		groupBlks := groupEnd - groupStart

		offset := groupStart

		// Superblock + GDT backup in groups 0, 1, and powers of 3, 5, 7
		hasSuperblock := isSparseGroup(g)
		if hasSuperblock {
			offset++ // superblock block
			offset += gdtBlocks
		}

		groups[g].blockBitmap = offset
		offset++
		groups[g].inodeBitmap = offset
		offset++
		groups[g].inodeTable = offset
		offset += inodeTableBlocks

		groups[g].dataStart = offset
		groups[g].totalBlocks = groupBlks

		usedInGroup := offset - groupStart
		if usedInGroup > groupBlks {
			usedInGroup = groupBlks
		}
		groups[g].freeBlocks = groupBlks - usedInGroup
		totalUsed += usedInGroup
	}

	sb.FreeBlocksCountLo = totalBlocks - totalUsed

	// Write the superblock at offset 1024 (in block 0)
	sbBuf := make([]byte, ext4SuperblockSize)
	encodeSuperblock(sbBuf, sb)
	if _, err := f.WriteAt(sbBuf, ext4SuperblockOffset); err != nil {
		return fmt.Errorf("write superblock: %w", err)
	}

	// Write group descriptors starting at block 1 (offset 4096)
	gdtBuf := make([]byte, gdtBlocks*ext4BlockSize)
	for g := uint32(0); g < numGroups; g++ {
		gd := ext4GroupDesc{
			BlockBitmapLo:     groups[g].blockBitmap,
			InodeBitmapLo:     groups[g].inodeBitmap,
			InodeTableLo:      groups[g].inodeTable,
			FreeBlocksCountLo: uint16(groups[g].freeBlocks),
			FreeInodesCountLo: uint16(inodesPerGroup),
			UsedDirsCountLo:   0,
		}
		if g == 0 {
			// Root dir + lost+found use inodes in group 0
			gd.FreeInodesCountLo = uint16(inodesPerGroup) - uint16(ext4FirstInode)
			gd.UsedDirsCountLo = 2 // root + lost+found
			// Subtract data blocks used for root dir and lost+found
			if gd.FreeBlocksCountLo >= 2 {
				gd.FreeBlocksCountLo -= 2
			}
		}
		off := g * 32
		encodeGroupDesc(gdtBuf[off:off+32], &gd)
	}
	if _, err := f.WriteAt(gdtBuf, int64(ext4BlockSize)); err != nil {
		return fmt.Errorf("write group descriptors: %w", err)
	}

	// Write superblock backups and GDT copies to sparse backup groups
	for g := uint32(1); g < numGroups; g++ {
		if !isSparseGroup(g) {
			continue
		}
		backupOff := int64(g*blocksPerGroup) * ext4BlockSize
		// Copy superblock with updated BlockGroupNr
		sbCopy := make([]byte, ext4SuperblockSize)
		copy(sbCopy, sbBuf)
		binary.LittleEndian.PutUint16(sbCopy[88:90], uint16(g)) // BlockGroupNr
		if _, err := f.WriteAt(sbCopy, backupOff+ext4SuperblockOffset); err != nil {
			return fmt.Errorf("write superblock backup (group %d): %w", g, err)
		}
		// Copy GDT
		if _, err := f.WriteAt(gdtBuf, backupOff+int64(ext4BlockSize)); err != nil {
			return fmt.Errorf("write GDT backup (group %d): %w", g, err)
		}
	}

	// Write block bitmaps
	for g := uint32(0); g < numGroups; g++ {
		bitmap := make([]byte, ext4BlockSize)
		usedBits := groups[g].dataStart - (g * blocksPerGroup)
		setBitmapBits(bitmap, 0, usedBits)

		// Mark blocks beyond the group boundary as used (last group may be partial)
		if groups[g].totalBlocks < blocksPerGroup {
			setBitmapBits(bitmap, groups[g].totalBlocks, blocksPerGroup)
		}

		// Group 0: also mark the root dir block and lost+found block
		if g == 0 {
			rootDirBlock := groups[g].dataStart
			setBitmapBits(bitmap, rootDirBlock-(g*blocksPerGroup), rootDirBlock-(g*blocksPerGroup)+2)
		}

		off := int64(groups[g].blockBitmap) * ext4BlockSize
		if _, err := f.WriteAt(bitmap, off); err != nil {
			return fmt.Errorf("write block bitmap (group %d): %w", g, err)
		}
	}

	// Write inode bitmaps
	for g := uint32(0); g < numGroups; g++ {
		bitmap := make([]byte, ext4BlockSize)
		if g == 0 {
			// Mark reserved inodes (1..10) plus lost+found as used
			setBitmapBits(bitmap, 0, uint32(ext4FirstInode))
		}
		// Mark inodes beyond inodesPerGroup as used if bitmap has trailing bits
		if inodesPerGroup < uint32(ext4BlockSize*8) {
			setBitmapBits(bitmap, inodesPerGroup, uint32(ext4BlockSize*8))
		}
		off := int64(groups[g].inodeBitmap) * ext4BlockSize
		if _, err := f.WriteAt(bitmap, off); err != nil {
			return fmt.Errorf("write inode bitmap (group %d): %w", g, err)
		}
	}

	// Write root directory inode (inode 2) and lost+found (inode 11)
	inodeTableOff := int64(groups[0].inodeTable) * ext4BlockSize
	rootDirDataBlock := groups[0].dataStart
	lostFoundDataBlock := groups[0].dataStart + 1

	// Root directory inode
	rootInode := makeDirectoryInode(now, ext4BlockSize, rootDirDataBlock, 3) // . + .. + lost+found
	rootInodeBuf := make([]byte, ext4InodeSize)
	encodeInode(rootInodeBuf, &rootInode)
	rootInodeOff := inodeTableOff + int64(ext4RootInode-1)*ext4InodeSize
	if _, err := f.WriteAt(rootInodeBuf, rootInodeOff); err != nil {
		return fmt.Errorf("write root inode: %w", err)
	}

	// lost+found inode
	lfInode := makeDirectoryInode(now, ext4BlockSize, lostFoundDataBlock, 2) // . + ..
	lfInodeBuf := make([]byte, ext4InodeSize)
	encodeInode(lfInodeBuf, &lfInode)
	lfInodeOff := inodeTableOff + int64(ext4LostFoundInode-1)*ext4InodeSize
	if _, err := f.WriteAt(lfInodeBuf, lfInodeOff); err != nil {
		return fmt.Errorf("write lost+found inode: %w", err)
	}

	// Write root directory data block
	rootDirBuf := make([]byte, ext4BlockSize)
	off := 0
	off += writeDirEntry(rootDirBuf[off:], ext4RootInode, ".", ext4FtDir, 12)
	off += writeDirEntry(rootDirBuf[off:], ext4RootInode, "..", ext4FtDir, 12)
	// lost+found entry — reclen extends to end of block
	writeDirEntry(rootDirBuf[off:], ext4LostFoundInode, "lost+found", ext4FtDir, uint16(ext4BlockSize-off))

	rootDirOff := int64(rootDirDataBlock) * ext4BlockSize
	if _, err := f.WriteAt(rootDirBuf, rootDirOff); err != nil {
		return fmt.Errorf("write root dir block: %w", err)
	}

	// Write lost+found directory data block
	lfDirBuf := make([]byte, ext4BlockSize)
	off = 0
	off += writeDirEntry(lfDirBuf[off:], ext4LostFoundInode, ".", ext4FtDir, 12)
	writeDirEntry(lfDirBuf[off:], ext4RootInode, "..", ext4FtDir, uint16(ext4BlockSize-off))

	lfDirOff := int64(lostFoundDataBlock) * ext4BlockSize
	if _, err := f.WriteAt(lfDirBuf, lfDirOff); err != nil {
		return fmt.Errorf("write lost+found dir block: %w", err)
	}

	return f.Sync()
}

// makeDirectoryInode creates a directory inode using an extent tree pointing
// to the given data block.
func makeDirectoryInode(now uint32, size uint32, dataBlock uint32, links uint16) ext4Inode {
	inode := ext4Inode{
		Mode:       0040755,  // S_IFDIR | 0755
		SizeLo:     size,
		Atime:      now,
		Ctime:      now,
		Mtime:      now,
		LinksCount: links,
		BlocksLo:   ext4BlockSize / 512, // disk sectors
		Flags:      ext4InodeFlagExtents,
	}

	// Build inline extent tree in inode.Block
	// Header: magic(2) + entries(2) + max(2) + depth(2) + generation(4) = 12 bytes
	binary.LittleEndian.PutUint16(inode.Block[0:2], 0xF30A)  // extent magic
	binary.LittleEndian.PutUint16(inode.Block[2:4], 1)       // 1 extent
	binary.LittleEndian.PutUint16(inode.Block[4:6], 4)       // max 4 extents in inode
	binary.LittleEndian.PutUint16(inode.Block[6:8], 0)       // depth 0 (leaf)
	binary.LittleEndian.PutUint32(inode.Block[8:12], 0)      // generation

	// Extent: block(4) + len(2) + start_hi(2) + start_lo(4) = 12 bytes
	binary.LittleEndian.PutUint32(inode.Block[12:16], 0)           // logical block 0
	binary.LittleEndian.PutUint16(inode.Block[16:18], 1)           // 1 block
	binary.LittleEndian.PutUint16(inode.Block[18:20], 0)           // start hi
	binary.LittleEndian.PutUint32(inode.Block[20:24], dataBlock)   // start lo

	return inode
}

// writeDirEntry writes an ext4 directory entry and returns its size.
func writeDirEntry(buf []byte, inode uint32, name string, fileType uint8, recLen uint16) int {
	nameBytes := []byte(name)
	binary.LittleEndian.PutUint32(buf[0:4], inode)
	binary.LittleEndian.PutUint16(buf[4:6], recLen)
	buf[6] = uint8(len(nameBytes))
	buf[7] = fileType
	copy(buf[8:], nameBytes)
	return int(recLen)
}

// encodeSuperblock serializes the superblock to a byte buffer.
func encodeSuperblock(buf []byte, sb *ext4Superblock) {
	binary.LittleEndian.PutUint32(buf[0:4], sb.InodesCount)
	binary.LittleEndian.PutUint32(buf[4:8], sb.BlocksCountLo)
	binary.LittleEndian.PutUint32(buf[8:12], sb.RBlocksCountLo)
	binary.LittleEndian.PutUint32(buf[12:16], sb.FreeBlocksCountLo)
	binary.LittleEndian.PutUint32(buf[16:20], sb.FreeInodesCount)
	binary.LittleEndian.PutUint32(buf[20:24], sb.FirstDataBlock)
	binary.LittleEndian.PutUint32(buf[24:28], sb.LogBlockSize)
	binary.LittleEndian.PutUint32(buf[28:32], sb.LogClusterSize)
	binary.LittleEndian.PutUint32(buf[32:36], sb.BlocksPerGroup)
	binary.LittleEndian.PutUint32(buf[36:40], sb.ClustersPerGroup)
	binary.LittleEndian.PutUint32(buf[40:44], sb.InodesPerGroup)
	binary.LittleEndian.PutUint32(buf[44:48], sb.Mtime)
	binary.LittleEndian.PutUint32(buf[48:52], sb.Wtime)
	binary.LittleEndian.PutUint16(buf[52:54], sb.MntCount)
	binary.LittleEndian.PutUint16(buf[54:56], sb.MaxMntCount)
	binary.LittleEndian.PutUint16(buf[56:58], sb.Magic)
	binary.LittleEndian.PutUint16(buf[58:60], sb.State)
	binary.LittleEndian.PutUint16(buf[60:62], sb.Errors)
	binary.LittleEndian.PutUint16(buf[62:64], sb.MinorRevLevel)
	binary.LittleEndian.PutUint32(buf[64:68], sb.Lastcheck)
	binary.LittleEndian.PutUint32(buf[68:72], sb.Checkinterval)
	binary.LittleEndian.PutUint32(buf[72:76], sb.CreatorOS)
	binary.LittleEndian.PutUint32(buf[76:80], sb.RevLevel)
	binary.LittleEndian.PutUint16(buf[80:82], sb.DefResuid)
	binary.LittleEndian.PutUint16(buf[82:84], sb.DefResgid)
	// Dynamic revision fields
	binary.LittleEndian.PutUint32(buf[84:88], sb.FirstIno)
	binary.LittleEndian.PutUint16(buf[88:90], sb.InodeSize)
	binary.LittleEndian.PutUint16(buf[90:92], sb.BlockGroupNr)
	binary.LittleEndian.PutUint32(buf[92:96], sb.FeatureCompat)
	binary.LittleEndian.PutUint32(buf[96:100], sb.FeatureIncompat)
	binary.LittleEndian.PutUint32(buf[100:104], sb.FeatureRoCompat)
	copy(buf[104:120], sb.UUID[:])
	copy(buf[120:136], sb.VolumeName[:])
	copy(buf[136:200], sb.LastMounted[:])
	binary.LittleEndian.PutUint32(buf[200:204], sb.AlgorithmUsageBmp)
	buf[204] = sb.PreallocBlocks
	buf[205] = sb.PreallocDirBlocks
	binary.LittleEndian.PutUint16(buf[206:208], sb.ReservedGdtBlocks)
	// Journal fields
	copy(buf[208:224], sb.JournalUUID[:])
	binary.LittleEndian.PutUint32(buf[224:228], sb.JournalInum)
	binary.LittleEndian.PutUint32(buf[228:232], sb.JournalDev)
	binary.LittleEndian.PutUint32(buf[232:236], sb.LastOrphan)
	for i := 0; i < 4; i++ {
		binary.LittleEndian.PutUint32(buf[236+i*4:240+i*4], sb.HashSeed[i])
	}
	buf[252] = sb.DefHashVersion
	buf[253] = sb.JnlBackupType
	binary.LittleEndian.PutUint16(buf[254:256], sb.DescSize)
	binary.LittleEndian.PutUint32(buf[256:260], sb.DefaultMountOpts)
	binary.LittleEndian.PutUint32(buf[260:264], sb.FirstMetaBg)
	binary.LittleEndian.PutUint32(buf[264:268], sb.MkfsTime)
	// jnl_blocks at 268..335
	// 64-bit fields
	binary.LittleEndian.PutUint32(buf[336:340], sb.BlocksCountHi)
	binary.LittleEndian.PutUint32(buf[340:344], sb.RBlocksCountHi)
	binary.LittleEndian.PutUint32(buf[344:348], sb.FreeBlocksCountHi)
	binary.LittleEndian.PutUint16(buf[348:350], sb.MinExtraIsize)
	binary.LittleEndian.PutUint16(buf[350:352], sb.WantExtraIsize)
}

// encodeGroupDesc serializes a group descriptor.
func encodeGroupDesc(buf []byte, gd *ext4GroupDesc) {
	binary.LittleEndian.PutUint32(buf[0:4], gd.BlockBitmapLo)
	binary.LittleEndian.PutUint32(buf[4:8], gd.InodeBitmapLo)
	binary.LittleEndian.PutUint32(buf[8:12], gd.InodeTableLo)
	binary.LittleEndian.PutUint16(buf[12:14], gd.FreeBlocksCountLo)
	binary.LittleEndian.PutUint16(buf[14:16], gd.FreeInodesCountLo)
	binary.LittleEndian.PutUint16(buf[16:18], gd.UsedDirsCountLo)
	binary.LittleEndian.PutUint16(buf[18:20], gd.Flags)
	binary.LittleEndian.PutUint32(buf[20:24], gd.ExcludeBitmapLo)
	binary.LittleEndian.PutUint16(buf[24:26], gd.BlockBitmapCsumLo)
	binary.LittleEndian.PutUint16(buf[26:28], gd.InodeBitmapCsumLo)
	binary.LittleEndian.PutUint16(buf[28:30], gd.ItableUnusedLo)
	binary.LittleEndian.PutUint16(buf[30:32], gd.Checksum)
}

// encodeInode serializes an inode into a buffer.
func encodeInode(buf []byte, inode *ext4Inode) {
	binary.LittleEndian.PutUint16(buf[0:2], inode.Mode)
	binary.LittleEndian.PutUint16(buf[2:4], inode.Uid)
	binary.LittleEndian.PutUint32(buf[4:8], inode.SizeLo)
	binary.LittleEndian.PutUint32(buf[8:12], inode.Atime)
	binary.LittleEndian.PutUint32(buf[12:16], inode.Ctime)
	binary.LittleEndian.PutUint32(buf[16:20], inode.Mtime)
	binary.LittleEndian.PutUint32(buf[20:24], inode.Dtime)
	binary.LittleEndian.PutUint16(buf[24:26], inode.Gid)
	binary.LittleEndian.PutUint16(buf[26:28], inode.LinksCount)
	binary.LittleEndian.PutUint32(buf[28:32], inode.BlocksLo)
	binary.LittleEndian.PutUint32(buf[32:36], inode.Flags)
	binary.LittleEndian.PutUint32(buf[36:40], inode.Osd1)
	copy(buf[40:100], inode.Block[:])
	binary.LittleEndian.PutUint32(buf[100:104], inode.Generation)
	binary.LittleEndian.PutUint32(buf[104:108], inode.FileACLLo)
	binary.LittleEndian.PutUint32(buf[108:112], inode.SizeHigh)
	binary.LittleEndian.PutUint32(buf[112:116], inode.ObsoFaddr)
	copy(buf[116:128], inode.Osd2[:])
}

// setBitmapBits sets bits from 'start' (inclusive) to 'end' (exclusive) in a bitmap.
func setBitmapBits(bitmap []byte, start, end uint32) {
	for i := start; i < end && i/8 < uint32(len(bitmap)); i++ {
		bitmap[i/8] |= 1 << (i % 8)
	}
}

// isSparseGroup returns true if the given group number should contain
// a superblock backup (ext4 sparse_super feature).
// Group 0 always has the superblock. Groups 1 and powers of 3, 5, 7 get backups.
func isSparseGroup(g uint32) bool {
	if g == 0 || g == 1 {
		return true
	}
	return isPowerOf(g, 3) || isPowerOf(g, 5) || isPowerOf(g, 7)
}

// isPowerOf returns true if n is a power of base.
func isPowerOf(n, base uint32) bool {
	if n == 0 {
		return false
	}
	p := base
	for p < n {
		if p > math.MaxUint32/base {
			return false // overflow
		}
		p *= base
	}
	return p == n
}

// generateUUID creates a pseudo-random UUID v4 based on the timestamp.
// Not cryptographically random but sufficient for filesystem identification.
func generateUUID(seed uint32) [16]byte {
	var uuid [16]byte
	// LCG-based simple PRNG seeded with timestamp
	state := uint64(seed) ^ 0x5DEECE66D
	for i := range uuid {
		state = state*6364136223846793005 + 1442695040888963407
		uuid[i] = byte(state >> 33)
	}
	// Set version 4 and variant bits
	uuid[6] = (uuid[6] & 0x0F) | 0x40
	uuid[8] = (uuid[8] & 0x3F) | 0x80
	return uuid
}
