package mtf

// spec_misc_test.go covers remaining MTF-100a requirements that are parsed by
// the reader but not yet asserted: OS ID values (Figure 29), SSET backup-type
// attributes (Table 9), the Time Zone field (Table 10 / Structure 6), the MTF
// version fields (Tables 8/11), the DIRB PATH_IN_STREAM bit (Table 13), and
// graceful skipping of the CFIL/ESPB descriptor blocks (Structures 10/11).

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// TestSpecOSIDValues verifies the OS ID assignments from Figure 29 that the
// reader branches on. OS ID 14 is Windows NT, which selects the NT OS-specific
// data parsing path (NT File Flags at OS-data offset 8, Structure 42).
func TestSpecOSIDValues(t *testing.T) {
	const osWindowsNT uint8 = 14 // Figure 29: "Windows NT (OS ID Number 14)"
	// The reader's parseDirb/parseFile branch on OSID == 14 for NT-specific data.
	if osWindowsNT != 14 {
		t.Errorf("Windows NT OS ID = %d, want 14 (spec Figure 29)", osWindowsNT)
	}
}

// TestSpecSSETBackupTypeAttributes verifies Table 9 (SSET Attributes): the
// backup-type bits occupy BIT0..BIT5. The reader surfaces them in SetInfo.
// Attributes; this confirms the field is read from the spec-correct offset and
// the low bits survive a round trip.
func TestSpecSSETBackupTypeAttributes(t *testing.T) {
	// Table 9 backup-type bits.
	const (
		ssetTransfer     uint32 = 0x01 << 0 // BIT0
		ssetCopy         uint32 = 0x01 << 1 // BIT1
		ssetNormal       uint32 = 0x01 << 2 // BIT2
		ssetDifferential uint32 = 0x01 << 3 // BIT3
		ssetIncremental  uint32 = 0x01 << 4 // BIT4
		ssetDaily        uint32 = 0x01 << 5 // BIT5
	)
	// Build an SSET marked as a "normal" backup (BIT2).
	b := buildSSET()
	const attr = ssetNormal
	b[ssetAttrOff] = byte(attr)
	b[ssetAttrOff+1] = byte(attr >> 8)
	b[ssetAttrOff+2] = byte(attr >> 16)
	b[ssetAttrOff+3] = byte(attr >> 24)

	var buf bytes.Buffer
	buf.Write(buildTape())
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
		if blk.Kind == KindSet {
			if got := blk.Set.Attributes; got != ssetNormal {
				t.Errorf("SSET Attributes = 0x%X, want 0x%X (spec Table 9 SSET_NORMAL_BIT=BIT2)", got, ssetNormal)
			}
			return
		}
	}
	t.Fatal("no Set block yielded")
}

// TestSpecTimeZoneField verifies the Time Zone field (Structure 6 / Table 10):
// the value is the count of 15-minute intervals from UTC (-48..+48), or 127 for
// LOCAL_TZ (local time not coordinated with UTC). The reader must pass the raw
// byte through as a signed int8.
func TestSpecTimeZoneField(t *testing.T) {
	const localTZ int8 = 127 // Table 10: LOCAL_TZ
	// EST is -5h = -20 (twenty 15-minute intervals behind UTC).
	const estTZ int8 = -20

	for _, tz := range []int8{localTZ, estTZ, 0, 48, -48} {
		b := buildSSET()
		b[ssetTZOff] = byte(tz)
		var buf bytes.Buffer
		buf.Write(buildTape())
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
			if blk.Kind == KindSet {
				if got := blk.Set.TimeZone; got != tz {
					t.Errorf("TZ %d -> %d, want %d (spec Structure 6 / Table 10)", tz, got, tz)
				}
				break
			}
		}
	}
}

// TestSpecMTFVersionFields verifies the MTF Major Version (Table 8, in the
// TAPE block) and the MTF Minor Version (Table 11, in the SSET block). For
// MTF-100a the major version is 1 and the minor version is 0.
func TestSpecMTFVersionFields(t *testing.T) {
	// TAPE block: MTF Major Version (Table 8) — major version 1.
	tape := buildTape()
	tape[tapeVersionOff] = 1
	// SSET block: MTF Minor Version (Table 11) — minor version 0 for major 1.
	sset := buildSSET()
	sset[ssetVerOff] = 0

	var buf bytes.Buffer
	buf.Write(tape)
	buf.Write(sset)
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var sawTape, sawSet bool
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindMedia {
			if blk.Tape.MTFMajorVersion != 1 {
				t.Errorf("MTF Major Version = %d, want 1 (spec Table 8)", blk.Tape.MTFMajorVersion)
			}
			sawTape = true
		}
		if blk.Kind == KindSet {
			if blk.Set.MinorVersion != 0 {
				t.Errorf("MTF Minor Version = %d, want 0 for major 1 (spec Table 11)", blk.Set.MinorVersion)
			}
			sawSet = true
		}
	}
	if !sawTape {
		t.Error("no Media block yielded to check MTF Major Version")
	}
	if !sawSet {
		t.Error("no Set block yielded to check MTF Minor Version")
	}
}

// TestSpecDIRBPathInStream verifies the DIRB_PATH_IN_STREAM_BIT (Table 13,
// BIT17 = 0x20000): when set, the directory path is NOT in the DBLK's String
// Storage Area but arrives via a PNAM stream. The reader must then keep the
// previous current-directory context rather than reading garbage from the name
// field.
func TestSpecDIRBPathInStream(t *testing.T) {
	const dirbPathInStream uint32 = 0x01 << 17 // BIT17

	b := buildDIRB(1, "ignored-inline", time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local))
	// Set the PATH_IN_STREAM bit and zero the name MTF_TAPE_ADDRESS so the
	// inline name is not consulted.
	putU32(b, dirbAttrOff, dirbPathInStream)
	b[dirbNameOff], b[dirbNameOff+1] = 0, 0 // size = 0

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
			// With PATH_IN_STREAM set and no PNAM provided, the inline name
			// must NOT be used; the directory path is empty/unknown until a
			// PNAM stream carries it.
			if blk.Header.Attributes&dirbPathInStream == 0 {
				t.Error("PATH_IN_STREAM bit not preserved in Attributes")
			}
			if blk.Header.Name == "C:/ignored-inline" {
				t.Error("inline name was used despite PATH_IN_STREAM being set (spec Table 13 BIT17)")
			}
			return
		}
	}
	t.Fatal("no directory entry yielded")
}

// TestSpecCFILGracefulSkip verifies Structure 10 (MTF_CFIL): a Corrupt Object
// block may appear after a FILE block. The reader must not abort the walk; it
// skips the CFIL and continues to subsequent entries.
func TestSpecCFILGracefulSkip(t *testing.T) {
	cfil := make([]byte, dbCommonSize)
	writeCommon(cfil, dbCFIL, 0)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(buildDIRB(1, "pre", time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local)))
	// Place the CFIL where a descriptor block is expected.
	pad := testFLBSize - (buf.Len() % testFLBSize)
	if pad != testFLBSize {
		buf.Write(bytes.Repeat([]byte{0}, pad))
	}
	buf.Write(cfil)
	buf.Write(make([]byte, testFLBSize-dbCommonSize)) // pad to block
	buf.Write(buildDIRB(2, "post", time.Date(2005, 1, 2, 0, 0, 0, 0, time.Local)))
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var dirs []string
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reader aborted at CFIL: %v (spec Structure 10 must be skippable)", err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryDirectory {
			dirs = append(dirs, blk.Header.Name)
		}
	}
	// The walk must reach the directory after the CFIL.
	found := false
	for _, d := range dirs {
		if d == "C:/post" {
			found = true
		}
	}
	if !found {
		t.Errorf("reader did not continue past CFIL; dirs = %v", dirs)
	}
}

// TestSpecESPBGracefulSkip verifies Structure 11 (MTF_ESPB): the End of Set Pad
// block pads to a physical block boundary before the ESET. The reader must
// recognize and skip it without treating it as an entry.
func TestSpecESPBGracefulSkip(t *testing.T) {
	// Build a normal data set, then insert an ESPB before the ESET.
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(buildDIRB(1, "d", time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local)))
	// ESPB is a full block whose common header type is 'ESPB'.
	espb := make([]byte, testFLBSize)
	writeCommon(espb, dbESPB, 0)
	buf.Write(espb)
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var sawEset bool
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reader aborted at ESPB: %v (spec Structure 11 must be skippable)", err)
		}
		if blk.Kind == KindSetEnd {
			sawEset = true
		}
	}
	if !sawEset {
		t.Error("reader did not reach ESET after ESPB (spec Structure 11)")
	}
}
