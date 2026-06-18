package mtf

// spec_ntosdata_test.go verifies the Windows NT OS-specific data structures
// from MTF-100a Appendix A (OS ID 14):
//
//   - OS Version 0:
//       Structure 39 (DIRB): Directory Attributes @0 (UINT32, 4 bytes total).
//       Structure 40 (FILE):  File Attributes @0 (UINT32),
//                              Short name offset @4 (UINT16),
//                              link flag @6 (BOOLEAN, 2 bytes),
//                              Reserved @8 (UINT16).   NO "NT File Flags".
//   - OS Version 1:
//       Structure 42 (DIRB):  Directory Attributes @0 (UINT32),
//                              Short name offset @4 (UINT16),
//                              Short name size @6 (UINT16).   NO "NT File Flags".
//       Structure 43 (FILE):  File Attributes @0 (UINT32),
//                              Short name offset @4 (UINT16),
//                              Short name size @6 (UINT16),
//                              NT File Flags @8 (UINT32, Table 28).
//
// The dwFileAttributes (offset 0) is common to every NT version and DBLK type,
// so it is always read. The NT File Flags (Table 28: NT_FILE_LINK_FLAG_BIT =
// BIT0, NT_FILE_POSIX_BIT = BIT16) exist ONLY in a FILE block at OS Version 1;
// at OS Version 0 the same offset 8 is Reserved and must not be interpreted as
// file flags.

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// setNTOSData writes an OS-specific data area into a FILE/DIRB block buffer,
// placing the raw area in block slack and recording its (size,offset) in the
// common-header OS Specific Data MTF_TAPE_ADDRESS. The OS ID is set to 14
// (Windows NT) and version is recorded at the common-header OS Version field.
func setNTOSData(b []byte, version uint8, area []byte) {
	b[dbOSIDOff] = 14
	b[dbOSVerOff] = version
	osOff := len(b) - len(area) - 2
	copy(b[osOff:], area)
	b[dbOSDataOff] = byte(len(area))
	b[dbOSDataOff+1] = byte(len(area) >> 8)
	b[dbOSDataOff+2] = byte(osOff)
	b[dbOSDataOff+3] = byte(osOff >> 8)
	setChecksum(b)
}

// ntFileBlock returns a FILE block (with a terminal SPAD stream) named name.
func ntFileBlock(name string) []byte { return buildFileWithStreams(name) }

// ntDirbBlock returns a DIRB block (with a terminal SPAD stream).
func ntDirbBlock(id uint32, name string) []byte { return buildDirbWithStreams(id, name) }

// nextEntryNamed advances r until it returns an entry whose Name ends with the
// given suffix, returning the header (nil if not found before EOF).
func nextEntryNamed(r *Reader, suffix string) *Header {
	for {
		blk, err := r.Next()
		if err != nil {
			return nil
		}
		if blk.Kind != KindEntry {
			continue
		}
		if s := blk.Header.Name; len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
			return blk.Header
		}
	}
}

// fileArchive wraps an entry block in a minimal TAPE/SSET/VOLB/DIRB preamble
// and ESET trailer so the reader walks to it.
func fileArchive(entry []byte) []byte {
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(buildDIRB(1, "d", time.Date(2005, 6, 1, 12, 0, 0, 0, time.Local)))
	buf.Write(entry)
	buf.Write(buildESET())
	return buf.Bytes()
}

// TestSpecNTFileOSDataVersion1 verifies Structure 43 (FILE, OS Version 1):
// dwFileAttributes @0 and NT File Flags @8 are both read.
func TestSpecNTFileOSDataVersion1(t *testing.T) {
	const winAttr uint32 = 0x00000020  // FILE_ATTRIBUTE_ARCHIVE
	const ntFlags uint32 = NTFilePOSIX // BIT16, Table 28

	area := make([]byte, 12)
	binary.LittleEndian.PutUint32(area[0:], winAttr)
	// offsets 4 (short name offset) and 6 (short name size) left zero
	binary.LittleEndian.PutUint32(area[8:], ntFlags)

	b := ntFileBlock("ntv1.txt")
	setNTOSData(b, 1, area)

	r := NewReader(NewSliceTape(fileArchive(b)))
	h := nextOfType(r, EntryFile)

	if h.WinAttributes != winAttr {
		t.Errorf("WinAttributes = %#x, want %#x (spec Structure 43 offset 0)", h.WinAttributes, winAttr)
	}
	if h.NTFileFlags != ntFlags {
		t.Errorf("NTFileFlags = %#x, want %#x (spec Structure 43 offset 8, OS Version 1)", h.NTFileFlags, ntFlags)
	}
}

// TestSpecNTFileOSDataVersion0 verifies Structure 40 (FILE, OS Version 0):
// dwFileAttributes @0 is read, but the bytes at offset 8 (Reserved in version 0)
// must NOT be surfaced as NT File Flags. Setting a non-zero value there must not
// flip IsHardLink, which derives from the version-1-only link flag.
//
// The area is sized to 12 bytes (>= the 12-byte guard) so a naive reader that
// ignores the OS Version would read offset 8 as NT File Flags; the spec forbids
// that for version 0.
func TestSpecNTFileOSDataVersion0(t *testing.T) {
	const winAttr uint32 = 0x00000021 // READONLY | ARCHIVE
	// Structure 40 layout: attr@0, short-name-off@4, link-boolean@6, reserved@8.
	area := make([]byte, 12)
	binary.LittleEndian.PutUint32(area[0:], winAttr)
	binary.LittleEndian.PutUint16(area[6:], 1) // link boolean = 1 (2 bytes)
	// offset 8 Reserved: place bytes whose little-endian low word has BIT0 set,
	// which would wrongly flag a hard link if read as NT File Flags.
	binary.LittleEndian.PutUint32(area[8:], NTFileLinkFlag|0x5A5A0000)

	b := ntFileBlock("ntv0.txt")
	setNTOSData(b, 0, area)

	r := NewReader(NewSliceTape(fileArchive(b)))
	h := nextOfType(r, EntryFile)

	if h.WinAttributes != winAttr {
		t.Errorf("WinAttributes = %#x, want %#x (spec Structure 40 offset 0)", h.WinAttributes, winAttr)
	}
	// NTFileFlags must be zero for OS Version 0: offset 8 is Reserved, not the
	// version-1 NT File Flags field.
	if h.NTFileFlags != 0 {
		t.Errorf("NTFileFlags = %#x, want 0 (OS Version 0 has no NT File Flags; offset 8 is Reserved per spec Structure 40)", h.NTFileFlags)
	}
	if h.IsHardLink {
		t.Error("IsHardLink = true for OS Version 0 FILE, want false (NT_FILE_LINK_FLAG_BIT is a Version-1-only field)")
	}
}

// TestSpecNTDirbOSDataHasNoFileFlags verifies Structure 42 (DIRB, OS Version 1):
// the directory OS-data area holds dwFileAttributes @0, short-name offset @4 and
// short-name size @6 — it has no NT File Flags field. Whatever sits at offset 8
// (beyond the area) must not be reported as NTFileFlags.
func TestSpecNTDirbOSDataHasNoFileFlags(t *testing.T) {
	const winAttr uint32 = 0x00000010 // FILE_ATTRIBUTE_DIRECTORY
	// Structure 42 DIRB area is only 8 bytes: attr@0, shortname-off@4, size@6.
	area := make([]byte, 8)
	binary.LittleEndian.PutUint32(area[0:], winAttr)

	b := ntDirbBlock(7, "ntdir")
	setNTOSData(b, 1, area)

	r := NewReader(NewSliceTape(fileArchive(b)))
	h := nextEntryNamed(r, "ntdir")
	if h == nil {
		t.Fatal("ntdir entry not found")
	}
	if h.WinAttributes != winAttr {
		t.Errorf("WinAttributes = %#x, want %#x (spec Structure 42 offset 0)", h.WinAttributes, winAttr)
	}
	if h.NTFileFlags != 0 {
		t.Errorf("NTFileFlags = %#x, want 0 (DIRB OS-data has no NT File Flags field, spec Structure 42)", h.NTFileFlags)
	}
}
