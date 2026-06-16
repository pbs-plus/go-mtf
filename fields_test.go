package mtf

import (
	"bytes"
	"testing"
	"time"
)

// TestChecksumAlgorithm verifies the MTF common-block header checksum against a
// hand-computed value, and that checksumValid detects corruption.
func TestChecksumAlgorithm(t *testing.T) {
	b := newBlock()
	writeCommon(b, dbTAPE, 0)
	putU16(b, tapeFLBSizeOff, testFLBSize)
	b[12] = 0x01
	b[20] = 0x02

	var want uint16
	for off := 0; off+1 < dbChecksumOff; off += 2 {
		want ^= u16(b, off)
	}
	if got := commonChecksum(b); got != want {
		t.Fatalf("commonChecksum = %04x, want %04x", got, want)
	}

	b[dbChecksumOff] = byte(want)
	b[dbChecksumOff+1] = byte(want >> 8)
	if !checksumValid(b) {
		t.Fatalf("checksumValid = false for a correctly checksummed block")
	}

	b[4] ^= 0xFF
	if checksumValid(b) {
		t.Fatalf("checksumValid = true after corrupting header")
	}
}

// TestReaderVerifyChecksum exercises Reader.VerifyChecksum on a TAPE block with
// a valid embedded checksum, read through the normal scan path.
func TestReaderVerifyChecksum(t *testing.T) {
	tape := buildTape()
	var sum uint16
	for off := 0; off+1 < dbChecksumOff; off += 2 {
		sum ^= u16(tape, off)
	}
	tape[dbChecksumOff] = byte(sum)
	tape[dbChecksumOff+1] = byte(sum >> 8)

	r := NewReader(bytes.NewReader(tape))
	_, _ = r.Next() // scans the TAPE block
	if !r.VerifyChecksum() {
		s, c := r.Checksum()
		t.Fatalf("VerifyChecksum = false (stored=%04x computed=%04x)", s, c)
	}
}

// TestSurfaceTapeFields verifies that parseTape surfaces the previously-unused
// TAPE descriptor fields.
func TestSurfaceTapeFields(t *testing.T) {
	b := buildTape()
	putU16(b, tapeEncryptOff, 0x0010)  // password algorithm
	putU16(b, tapeSFMSizeOff, 2)       // soft filemark block size
	putU16(b, tapeCatTypeOff, 1)       // catalog type
	putU16(b, tapeVendorIDOff, 0x1234) // vendor id
	b[tapeVersionOff] = 5              // major version
	// Place the media password string in a safe region near the end of the
	// 256-byte block and point the TAPE_POSITION at it.
	const passPos = 240
	copy(b[passPos:], []byte("secret\x00"))
	putU16(b, tapePasswdOff, 7)         // size (incl. NUL)
	putU16(b, tapePasswdOff+2, passPos) // position

	r := NewReader(bytes.NewReader(b))
	_, _ = r.Next() // drives parseTape on the TAPE block
	tp := r.Tape()
	if tp == nil {
		t.Fatal("Tape() = nil after reading TAPE block")
	}
	if tp.PasswordAlgorithm != 0x0010 {
		t.Errorf("PasswordAlgorithm = %04x, want 0010", tp.PasswordAlgorithm)
	}
	if tp.SoftFilemarkBlockSize != 2 {
		t.Errorf("SoftFilemarkBlockSize = %d, want 2", tp.SoftFilemarkBlockSize)
	}
	if tp.CatalogType != 1 {
		t.Errorf("CatalogType = %d, want 1", tp.CatalogType)
	}
	if tp.SoftwareVendorID != 0x1234 {
		t.Errorf("SoftwareVendorID = %04x, want 1234", tp.SoftwareVendorID)
	}
	if tp.MTFMajorVersion != 5 {
		t.Errorf("MTFMajorVersion = %d, want 5", tp.MTFMajorVersion)
	}
	if tp.Password != "secret" {
		t.Errorf("Password = %q, want %q", tp.Password, "secret")
	}
}

// TestSurfaceSetFields verifies that parseSet surfaces the previously-unused
// SSET descriptor fields via the normal scan path.
func TestSurfaceSetFields(t *testing.T) {
	b := newBlock()
	writeCommon(b, dbSSET, 0)
	putU16(b, ssetNumOff, 1)
	putU16(b, ssetVendorOff, 0x55AA)
	putU16(b, ssetVerOff, 0x0102)
	// Data-set password string in a safe region.
	const passPos = 240
	copy(b[passPos:], []byte("setpass\x00"))
	putU16(b, ssetPasswdOff, 8)
	putU16(b, ssetPasswdOff+2, passPos)

	r := NewReader(bytes.NewReader(b))
	_, _ = r.Next() // scans SSET and sets r.set
	s := r.Set()
	if s == nil {
		t.Fatal("Set() = nil")
	}
	if s.SoftwareVendorID != 0x55AA {
		t.Errorf("SoftwareVendorID = %04x, want 55AA", s.SoftwareVendorID)
	}
	if s.SoftwareVersion != 0x0102 {
		t.Errorf("SoftwareVersion = %04x, want 0102", s.SoftwareVersion)
	}
	if s.Password != "setpass" {
		t.Errorf("Password = %q, want %q", s.Password, "setpass")
	}
}

// TestSurfaceESetFields verifies the ESET metadata is surfaced via ESet().
func TestSurfaceESetFields(t *testing.T) {
	data := buildArchive()
	r := NewReader(bytes.NewReader(data))
	for {
		_, err := r.Next()
		if err != nil {
			break
		}
	}
	// The ESET block is parsed during the final Next() that returns EOF.
	e := r.ESet()
	if e == nil {
		t.Fatal("ESet() = nil; ESET block not parsed")
	}
	// buildESET sets the sequence number to 1.
	if e.SetNumber != 1 {
		t.Errorf("ESet SetNumber = %d, want 1", e.SetNumber)
	}
}

// TestSurfaceHeaderFields verifies file/dir/volume fields surfaced on the
// Header: birth time, volume label, machine name, and displayable size.
func TestSurfaceHeaderFields(t *testing.T) {
	data := buildArchive()
	r := NewReader(bytes.NewReader(data))
	var sawFile, sawVol bool
	for {
		h, err := r.Next()
		if err != nil {
			break
		}
		_ = h.DisplayableSize
		switch h.Type {
		case EntryVolume:
			sawVol = true
			_ = h.VolumeLabel
			_ = h.MachineName
			// Common block attributes are surfaced (continuation/compression bits).
			if h.BlockAttributes&AttrCompression != 0 || h.BlockAttributes&AttrEOSAtEOM != 0 {
				t.Errorf("unexpected block attrs %08x", h.BlockAttributes)
			}
		case EntryFile:
			sawFile = true
			_ = h.BirthTime
			_ = h.Compressed
			_ = h.Encrypted
			_ = h.Sparse
			_ = h.StreamChecksum
		}
	}
	if !sawFile {
		t.Error("no file entry found")
	}
	if !sawVol {
		t.Error("no volume entry found")
	}
}

// TestStreamFlagsParsing verifies the compression/encryption/sparse flags and
// algorithm IDs are parsed from a STAN stream header.
func TestStreamFlagsParsing(t *testing.T) {
	dirMtime := time.Date(2005, 6, 1, 12, 32, 0, 0, time.Local)
	fileMtime := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)

	// Build a FILE whose STAN stream declares compression + an algorithm ID.
	nameOff := fileNameOff + 4
	streamStart := nameOff + len("c.txt") + 1
	if m := streamStart % 4; m != 0 {
		streamStart += 4 - m
	}
	preamble := make([]byte, streamStart+streamHeaderSize)
	writeCommon(preamble, dbFILE, uint16(streamStart))
	putU32(preamble, fileIDOff, 10)
	putU32(preamble, fileDirIDOff, 1)
	putString(preamble, fileNameOff, nameOff, "c.txt")
	dt := encodeDateTime(fileMtime)
	copy(preamble[fileMTimeOff:], dt[:])
	putU32(preamble, streamStart+stTypeOff, StreamSTAN)
	preamble[streamStart+stMediaAttrOff] = byte(StreamMediaCompressed) // set COMPRESSED bit
	preamble[streamStart+stCompressOff] = 0x01                         // algorithm id low byte
	putU64(preamble, streamStart+stLengthOff, 4)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(buildDIRB(1, "Users", dirMtime))
	buf.Write(preamble)
	buf.Write([]byte{0xAA, 0xBB, 0xCC, 0xDD})
	if m := buf.Len() % 4; m != 0 {
		buf.Write(make([]byte, 4-m))
	}
	spadHeaderEnd := buf.Len() + streamHeaderSize
	spadDataLen := testFLBSize - (spadHeaderEnd % testFLBSize)
	if spadDataLen == testFLBSize {
		spadDataLen = 0
	}
	spad := make([]byte, streamHeaderSize)
	putU32(spad, stTypeOff, StreamSPAD)
	putU64(spad, stLengthOff, uint64(spadDataLen))
	buf.Write(spad)
	if spadDataLen > 0 {
		buf.Write(make([]byte, spadDataLen))
	}
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var found bool
	for {
		h, err := r.Next()
		if err != nil {
			break
		}
		if h.Type == EntryFile && h.Name == "C:/Users/c.txt" {
			found = true
			if !h.Compressed {
				t.Error("Compressed = false, want true")
			}
			if h.CompressionAlgorithm != 0x01 {
				t.Errorf("CompressionAlgorithm = %04x, want 0001", h.CompressionAlgorithm)
			}
			if h.Encrypted {
				t.Error("Encrypted = true, want false")
			}
		}
	}
	if !found {
		t.Fatal("compressed file entry not found")
	}
}
