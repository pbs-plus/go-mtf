package mtf

// spec_attributes_test.go verifies the two distinct attribute fields defined by
// the MTF spec, which the reader must not conflate:
//
//   - The MTF DBLK Attributes field (Table 13 DIRB / Table 14 FILE), a
//     BIT8-based layout stored at the DBLK attribute offset. Always present.
//     Exposed as Header.Attributes; bits are the MTFAttr* constants.
//   - The Windows dwFileAttributes (FILE_ATTRIBUTE_*), stored in the
//     OS-specific data area for Windows NT entries (OS ID 14, OS version 0/1;
//     spec Structures 42/43, offset 0). Exposed as Header.WinAttributes; bits
//     are the WinAttr* constants.
//
// Header.UnixMode must derive from Header.Attributes (the always-present MTF
// field), not from WinAttributes, because WinAttributes is only populated for
// NT OS version 0/1 (many real archives, e.g. Backup Exec OS version 4, leave
// the OS-specific area empty).

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

// TestSpecMTFDBLKAttributeBits verifies the MTF DBLK attribute bit positions
// (Tables 13/14): READ_ONLY=BIT8, HIDDEN=BIT9, SYSTEM=BIT10, MODIFIED=BIT11,
// EMPTY/IN_USE=BIT16, PATH/NAME_IN_STREAM=BIT17, CORRUPT=BIT18.
func TestSpecMTFDBLKAttributeBits(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32 // BITn => 1<<n
	}{
		{"READ_ONLY_BIT", MTFAttrReadOnly, 1 << 8},
		{"HIDDEN_BIT", MTFAttrHidden, 1 << 9},
		{"SYSTEM_BIT", MTFAttrSystem, 1 << 10},
		{"MODIFIED_BIT", MTFAttrModified, 1 << 11},
		{"EMPTY/IN_USE_BIT", MTFAttrEmpty, 1 << 16},
		{"PATH/NAME_IN_STREAM_BIT", MTFAttrPathInStream, 1 << 17},
		{"CORRUPT_BIT", MTFAttrCorrupt, 1 << 18},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("MTFAttr %s = %#x, want %#x (spec Table 13/14)", c.name, c.got, c.want)
		}
	}
}

// TestSpecWinAttributeValues verifies the Win32 FILE_ATTRIBUTE_* values used
// for Header.WinAttributes (OS-specific dwFileAttributes, Structures 42/43).
func TestSpecWinAttributeValues(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"FILE_ATTRIBUTE_READONLY", WinAttrReadOnly, 0x00000001},
		{"FILE_ATTRIBUTE_HIDDEN", WinAttrHidden, 0x00000002},
		{"FILE_ATTRIBUTE_SYSTEM", WinAttrSystem, 0x00000004},
		{"FILE_ATTRIBUTE_DIRECTORY", WinAttrDirectory, 0x00000010},
		{"FILE_ATTRIBUTE_ARCHIVE", WinAttrArchive, 0x00000020},
		{"FILE_ATTRIBUTE_NORMAL", WinAttrNormal, 0x00000080},
		{"FILE_ATTRIBUTE_COMPRESSED", WinAttrCompressed, 0x00000800},
		{"FILE_ATTRIBUTE_ENCRYPTED", WinAttrEncrypted, 0x00004000},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("WinAttr %s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestSpecWinAttributesFromOSData verifies that for a Windows NT entry (OS ID
// 14, OS version 1) the dwFileAttributes at OS-specific offset 0 are loaded
// into Header.WinAttributes and the NT File Flags at offset 8 into
// Header.NTFileFlags (spec Structures 42/43).
func TestSpecWinAttributesFromOSData(t *testing.T) {
	const osidNT uint8 = 14
	const osverNT1 uint8 = 1

	mt := time.Date(2005, 6, 1, 12, 0, 0, 0, time.Local)
	b := buildDIRB(1, "ntdir", mt)
	b[dbOSIDOff] = osidNT
	b[dbOSVerOff] = osverNT1

	// Append an OS-specific data area holding Structure 42:
	//   @0 dwFileAttributes (READONLY|HIDDEN) = 0x03
	//   @4 short name offset = 0
	//   @6 short name size = 0
	//   @8 NT File Flags (POSIX) = NTFilePOSIX
	osData := make([]byte, 12)
	binary.LittleEndian.PutUint32(osData[0:], 0x03) // READONLY|HIDDEN
	binary.LittleEndian.PutUint32(osData[8:], NTFilePOSIX)
	osOff := dirbNameOff + 4 // place it in the slack of the block
	copy(b[osOff:], osData)
	// Point the common header's OS Data Area pointer at it.
	b[dbOSDataOff] = byte(len(osData))
	b[dbOSDataOff+1] = byte(len(osData) >> 8)
	b[dbOSDataOff+2] = byte(osOff)
	b[dbOSDataOff+3] = byte(osOff >> 8)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(b)
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryDirectory {
			h := blk.Header
			if h.WinAttributes != 0x03 {
				t.Errorf("WinAttributes = %#x, want 0x03 (OS-data offset 0, spec Structure 42)", h.WinAttributes)
			}
			if h.NTFileFlags != NTFilePOSIX {
				t.Errorf("NTFileFlags = %#x, want %#x (OS-data offset 8, spec Structure 42)", h.NTFileFlags, NTFilePOSIX)
			}
			return
		}
	}
	t.Fatal("no directory entry yielded")
}

// TestSpecUnixModeFromMTFAttributes verifies Header.UnixMode derives its mode
// from the MTF DBLK attributes (always present), not from WinAttributes. A
// directory with the MTF read-only bit (BIT8) must lose owner-write even when
// WinAttributes is zero (as it is for many real OS-version-4 archives).
func TestSpecUnixModeFromMTFAttributes(t *testing.T) {
	h := &Header{Type: EntryDirectory, Attributes: MTFAttrReadOnly}
	// WinAttributes deliberately zero, as on Backup Exec OS-version-4 archives.
	if got := h.UnixMode(); got != 0o555 {
		t.Errorf("UnixMode(dir, MTF read-only, WinAttr=0) = 0o%o, want 0o555", got)
	}
	h = &Header{Type: EntryFile, Attributes: MTFAttrModified}
	if got := h.UnixMode(); got != 0o755 {
		t.Errorf("UnixMode(file, MTF modified/archive, WinAttr=0) = 0o%o, want 0o755", got)
	}
}

// TestSpecGoldenMTFAttributesRealArchive asserts the MTF DBLK attribute layout
// against hand-verified values from B2D027089.bkf. The reader is skipped when
// the fixture is absent.
func TestSpecGoldenMTFAttributesRealArchive(t *testing.T) {
	if !goldenBKFExists() {
		t.Skipf("%s not present; skipping", goldenBKF)
	}
	r := openGolden(t)
	sawHiddenSystem, sawEmpty := false, false
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		// $RECYCLE.BIN is hidden+system: in MTF layout that is BIT9|BIT10
		// (0x600). In a (hypothetical) Win32 misread it would be 0x06.
		if h.Attributes == MTFAttrHidden|MTFAttrSystem {
			sawHiddenSystem = true
		}
		// Empty directories carry BIT16 (DIRB_EMPTY_BIT), e.g. ".git/branches".
		if h.Attributes == MTFAttrEmpty {
			sawEmpty = true
		}
		if sawHiddenSystem && sawEmpty {
			break
		}
	}
	if !sawHiddenSystem {
		t.Error("no hidden+system entry (MTF 0x600) found; MTF layout not observed on real archive")
	}
	if !sawEmpty {
		t.Error("no empty-directory entry (MTF 0x10000) found; MTF DIRB_EMPTY bit not observed")
	}
}
