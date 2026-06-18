package mtf

const (
	dbTypeOff  = 0  // block type (4 bytes)
	dbAttrOff  = 4  // common attributes (uint32)
	dbOffOff   = 8  // offset to first stream (uint16)
	dbOSIDOff  = 10 // operating system id (uint8)
	dbOSVerOff = 11 // operating system version (uint8)
	dbSizeOff  = 12 // displayable size (uint64)
	dbFLAOff   = 20 // format logical address (uint64)
	dbCBIDOff  = 36 // control block id (uint32)
	// Per MTF spec (MTF_DB_HDR, Structure 4): offset 40 is a 4-byte Reserved
	// field, OS Specific Data is at offset 44, String Type at 48, and the
	// Header Checksum at 50 — the common header is 52 bytes total.
	dbOSDataOff   = 44 // os-specific data (tape_pos: size, pos)
	dbStrTypeOff  = 48 // string type (uint8): bit0 clear=UTFC16LE, set=ASCII
	dbChecksumOff = 50 // header checksum (uint16)

	dbCommonSize = 52 // size of the common descriptor block (MTF_DB_HDR)

	streamHeaderSize = 22 // size of a stream descriptor header
)

// DB field offsets, per block type (offsets beyond the 52-byte common header).

// TAPE
const (
	tapeMFMIDOff    = 52
	tapeAttrOff     = 56
	tapeSeqOff      = 60
	tapeEncryptOff  = 62
	tapeSFMSizeOff  = 64
	tapeCatTypeOff  = 66
	tapeNameOff     = 68
	tapeLabelOff    = 72
	tapePasswdOff   = 76
	tapeSoftwareOff = 80
	tapeFLBSizeOff  = 84
	tapeVendorIDOff = 86
	tapeCTimeOff    = 88
	tapeVersionOff  = 93
)

// SSET
const (
	ssetAttrOff    = 52
	ssetEncryptOff = 56
	ssetCompOff    = 58
	ssetVendorOff  = 60
	ssetNumOff     = 62
	ssetNameOff    = 64
	ssetLabelOff   = 68
	ssetPasswdOff  = 72
	ssetUserOff    = 76
	ssetPBAOff     = 80
	ssetCTimeOff   = 88
	ssetMajorOff   = 93
	ssetMinorOff   = 94
	ssetTZOff      = 95
	ssetVerOff     = 96
	ssetCatVerOff  = 97
)

// VOLB
const (
	volbAttrOff    = 52
	volbDeviceOff  = 56
	volbVolumeOff  = 60
	volbMachineOff = 64
	volbCTimeOff   = 68
)

// DIRB
const (
	dirbAttrOff  = 52
	dirbMTimeOff = 56
	dirbCTimeOff = 61
	dirbBTimeOff = 66
	dirbATimeOff = 71
	dirbIDOff    = 76
	dirbNameOff  = 80
)

// FILE
const (
	fileAttrOff  = 52
	fileMTimeOff = 56
	fileCTimeOff = 61
	fileBTimeOff = 66
	fileATimeOff = 71
	fileDirIDOff = 76
	fileIDOff    = 80
	fileNameOff  = 84
)

// ESET
const (
	esetAttrOff    = 52
	esetCorruptOff = 56
	esetSeqOff     = 76 // FDD media sequence number
	esetSetOff     = 78 // data set number
	esetCTimeOff   = 80 // media write date
)

// Stream header field offsets (relative to the start of the stream header).
const (
	stTypeOff      = 0  // uint32
	stSysAttrOff   = 4  // uint16
	stMediaAttrOff = 6  // uint16
	stLengthOff    = 8  // uint64
	stEncryptOff   = 16 // uint16
	stCompressOff  = 18 // uint16
	stChecksumOff  = 20 // uint16
)

func u8(b []byte, off int) uint8 { return b[off] }

func u16(b []byte, off int) uint16 {
	return uint16(b[off]) | uint16(b[off+1])<<8
}

func u32(b []byte, off int) uint32 {
	return uint32(b[off]) | uint32(b[off+1])<<8 | uint32(b[off+2])<<16 | uint32(b[off+3])<<24
}

func u64(b []byte, off int) uint64 {
	return uint64(b[off]) | uint64(b[off+1])<<8 | uint64(b[off+2])<<16 | uint64(b[off+3])<<24 |
		uint64(b[off+4])<<32 | uint64(b[off+5])<<40 | uint64(b[off+6])<<48 | uint64(b[off+7])<<56
}

// tapepos returns the MTF TAPE_POSITION (size, offset) pair at off: a uint16
// size followed by a uint16 offset into the block buffer.
func tapepos(b []byte, off int) (size, pos uint16) {
	return u16(b, off), u16(b, off+2)
}

func blockType(b []byte) [4]byte { return [4]byte{b[0], b[1], b[2], b[3]} }

// ntOSData parses the Windows NT OS-specific data area of a descriptor block
// (OS ID 14, spec Structures 42/43). It returns the dwFileAttributes (offset
// 0 of the OS-specific area) and the NT File Flags (offset 8). ok is false when
// the block is not NT or the OS-specific area is too short.
func ntOSData(b []byte) (winAttr, ntFlags uint32, ok bool) {
	if u8(b, dbOSIDOff) != 14 {
		return 0, 0, false
	}
	osSize, osOff := tapepos(b, dbOSDataOff)
	base := int(osOff)
	if osSize >= 4 && base+4 <= len(b) {
		winAttr = u32(b, base)
	}
	if osSize >= 12 && base+12 <= len(b) {
		ntFlags = u32(b, base+8)
	}
	return winAttr, ntFlags, true
}

// loadNTOSData ensures the reader's block buffer covers the OS-specific data
// area, then parses the Windows NT OS-specific fields (OS ID 14, spec
// Structures 42/43) via ntOSData. The descriptor block's fields may extend past
// the minimum the caller already ensured, so the buffer is grown to cover the
// OS-data area before it is read.
func (r *Reader) loadNTOSData() (winAttr, ntFlags uint32, ok bool) {
	if u8(r.blk, dbOSIDOff) != 14 {
		return 0, 0, false
	}
	osSize, osOff := tapepos(r.blk, dbOSDataOff)
	if need := int(osOff) + int(osSize); need > len(r.blk) {
		if err := r.ensure(need); err != nil {
			return 0, 0, false
		}
	}
	return ntOSData(r.blk)
}

// commonChecksum returns the MTF common-block header checksum for the given
// block: a 16-bit word-wise XOR over all MTF_DB_HDR fields except the checksum
// field itself (bytes 0..49, i.e. 25 little-endian words). See MTF spec,
// "Header Checksum".
func commonChecksum(b []byte) uint16 {
	if len(b) < dbChecksumOff {
		return 0
	}
	var sum uint16
	for off := 0; off+1 < dbChecksumOff; off += 2 {
		sum ^= u16(b, off)
	}
	return sum
}

// checksumValid reports whether the MTF_DB_HDR checksum field of b matches the
// computed word-wise XOR over the remaining header fields. It returns true if
// the block buffer is too short to contain a checksum (nothing to verify).
func checksumValid(b []byte) bool {
	if len(b) < dbChecksumOff+2 {
		return true
	}
	return commonChecksum(b) == u16(b, dbChecksumOff)
}
