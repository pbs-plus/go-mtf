package mtf

import (
	"encoding/binary"
	"unicode/utf16"
)

// parseReparsePoint extracts the symlink or mount-point target from an NTRP
// (NT_REPARSE_STREAM) payload. The reparse data follows the Windows
// REPARSE_DATA_BUFFER layout:
//
//	Offset  Size  Field
//	0       4     ReparseTag
//	4       2     ReparseDataLength
//	6       2     Reserved
//	— symlink-specific (IO_REPARSE_TAG_SYMLINK, 0xA000000C) —
//	8       2     SubstituteNameOffset
//	10      2     SubstituteNameLength
//	12      2     PrintNameOffset
//	14      2     PrintNameLength
//	16      4     Flags (1 = relative symlink)
//	20      var   PathBuffer (UTF-16LE)
//
// For mount points (IO_REPARSE_TAG_MOUNT_POINT, 0xA0000003), the layout is the
// same minus the Flags field at offset 16.
func parseReparsePoint(h *Header, data []byte) {
	if len(data) < 8 {
		return
	}
	tag := binary.LittleEndian.Uint32(data[0:4])
	switch tag {
	case ReparseTagSymlink:
		if len(data) < 20 {
			return
		}
		subOff := int(binary.LittleEndian.Uint16(data[8:10]))
		subLen := int(binary.LittleEndian.Uint16(data[10:12]))
		// Flags at offset 16: 0 = absolute, 1 = relative
		// pathBuffer starts at offset 20 for symlinks
		pathStart := 20 + subOff
		pathEnd := pathStart + subLen
		if pathEnd > len(data) {
			return
		}
		h.LinkTarget = decodeUTF16LE(data[pathStart:pathEnd])
		h.IsSymlink = true
	case ReparseTagMount:
		if len(data) < 16 {
			return
		}
		subOff := int(binary.LittleEndian.Uint16(data[8:10]))
		subLen := int(binary.LittleEndian.Uint16(data[10:12]))
		pathStart := 16 + subOff
		pathEnd := pathStart + subLen
		if pathEnd > len(data) {
			return
		}
		h.LinkTarget = decodeUTF16LE(data[pathStart:pathEnd])
		h.IsSymlink = true
	default:
		// Unknown reparse tag; store the raw tag for caller inspection
		// via Streams[NTRP].
	}
}

// parseLinkStream extracts the hard link target from a LINK
// (STRM_NTFS_LINK) stream payload. The payload is a UTF-16LE path string
// pointing to the original file that this entry is linked to.
func parseLinkStream(h *Header, data []byte) {
	if len(data) == 0 {
		return
	}
	h.LinkTarget = decodeUTF16LE(data)
	h.IsHardLink = true
}

// decodeUTF16LE decodes a UTF-16LE byte slice to a Go string, stripping any
// trailing NUL characters.
func decodeUTF16LE(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	// Ensure even length
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u16s := make([]uint16, len(b)/2)
	for i := range u16s {
		u16s[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	runes := utf16.Decode(u16s)
	// Strip trailing NUL
	for len(runes) > 0 && runes[len(runes)-1] == 0 {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}
