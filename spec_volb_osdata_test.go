package mtf

// spec_volb_osdata_test.go verifies the MTF_VOLB OS-specific data for Windows
// NT (spec Structure 41 / Table 27): the File System Flags at OS-data offset 0
// and the NT Backup Set Attributes at offset 4, whose BIT0
// (NT_VOLB_IS_DR_CANDIDATE) marks a volume whose data is suitable for an NT
// system recovery.

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// TestSpecVOLBNTOSData verifies Structure 41: file-system flags at offset 0 and
// NT Backup Set Attributes at offset 4, with the DR-candidate bit (BIT0).
func TestSpecVOLBNTOSData(t *testing.T) {
	b := buildVOLB("C:")
	b[dbOSIDOff] = 14
	b[dbOSVerOff] = 1

	// OS-specific data area (Structure 41): fsFlags@0, NT Backup Set Attr@4.
	osData := make([]byte, 8)
	const fsFlags uint32 = 0x000000FF      // lpFileSystemFlags
	const ntBackupAttr = VOLBNTDRCandidate // DR candidate set
	binary.LittleEndian.PutUint32(osData[0:], fsFlags)
	binary.LittleEndian.PutUint32(osData[4:], ntBackupAttr)
	osOff := volbMachineOff + 4 // slack inside the block
	copy(b[osOff:], osData)
	b[dbOSDataOff] = byte(len(osData))
	b[dbOSDataOff+1] = byte(len(osData) >> 8)
	b[dbOSDataOff+2] = byte(osOff)
	b[dbOSDataOff+3] = byte(osOff >> 8)
	setChecksum(b)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(b)
	buf.Write(buildESET())

	r := NewReader(NewSliceTape(buf.Bytes()))
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryVolume {
			h := blk.Header
			if h.FileSystemFlags != fsFlags {
				t.Errorf("FileSystemFlags = %#x, want %#x (spec Structure 41 offset 0)", h.FileSystemFlags, fsFlags)
			}
			if !h.IsDRCandidate {
				t.Error("IsDRCandidate = false, want true (NT_VOLB_IS_DR_CANDIDATE BIT0 set, spec Table 27)")
			}
			return
		}
	}
	t.Fatal("no volume entry yielded")
}

// TestSpecVOLBDRCandidateBitValue verifies the NT_VOLB_IS_DR_CANDIDATE bit
// position (Table 27, BIT0).
func TestSpecVOLBDRCandidateBitValue(t *testing.T) {
	if VOLBNTDRCandidate != 0x00000001 {
		t.Errorf("VOLBNTDRCandidate = %#x, want 0x00000001 (BIT0, spec Table 27)", VOLBNTDRCandidate)
	}
}

// TestSpecVOLBOSDataAbsentIsNotDRCandidate verifies that a volume without NT
// OS-specific data (or a non-NT volume) is not flagged as a DR candidate and
// has zero file-system flags.
func TestSpecVOLBOSDataAbsentIsNotDRCandidate(t *testing.T) {
	// buildVOLB leaves OS ID 0 (writeCommon sets dbOSIDOff=0).
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("D:"))
	buf.Write(buildESET())

	r := NewReader(NewSliceTape(buf.Bytes()))
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryVolume {
			h := blk.Header
			if h.IsDRCandidate {
				t.Error("IsDRCandidate = true for non-NT volume, want false")
			}
			if h.FileSystemFlags != 0 {
				t.Errorf("FileSystemFlags = %#x, want 0 for non-NT volume", h.FileSystemFlags)
			}
			return
		}
	}
	t.Fatal("no volume entry yielded")
}

// TestSpecGoldenVOLBOSDataRealArchive asserts the VOLB OS-specific fields on the
// real B2D027089.bkf archive. Skipped when the fixture is absent.
func TestSpecGoldenVOLBOSDataRealArchive(t *testing.T) {
	if !goldenBKFExists() {
		t.Skipf("%s not present; skipping", goldenBKF)
	}
	r := openGolden(t)
	saw := false
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind != KindEntry || blk.Header.Type != EntryVolume {
			continue
		}
		h := blk.Header
		// The archive's first NT volume carries file-system flags; the DR bit
		// is documented but not asserted to a fixed value (varies by archive).
		if h.OSID == 14 && h.FileSystemFlags != 0 {
			saw = true
		}
	}
	if !saw {
		t.Error("no NT volume with file-system flags found on real archive")
	}
}
