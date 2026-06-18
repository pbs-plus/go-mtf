package mtf

// spec_behavior_test.go verifies spec-mandated reader *behaviours* with
// fixtures constructed purely from the spec (MTF_TAPE_ADDRESS Structure 2,
// implied precedence Table 1, DIRB path separators section 5.2.4, name-in-stream
// section 6.2.1.2/6.2.1.3). It complements the constant/offset tests, which only
// check that the right byte is read.

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// TestSpecMTFTapeAddress verifies Structure 2: an MTF_TAPE_ADDRESS is a 4-byte
// pair of (UINT16 Size, UINT16 Offset). tapepos must decode it as such.
func TestSpecMTFTapeAddress(t *testing.T) {
	b := make([]byte, 8)
	// At offset 0: size=0x1234, offset=0x5678.
	b[0], b[1] = 0x34, 0x12
	b[2], b[3] = 0x78, 0x56
	size, off := tapepos(b, 0)
	if size != 0x1234 {
		t.Errorf("tapepos size = %#x, want 0x1234 (spec Structure 2)", size)
	}
	if off != 0x5678 {
		t.Errorf("tapepos offset = %#x, want 0x5678 (spec Structure 2)", off)
	}
}

// TestSpecImpliedPrecedence verifies Table 1 (Implied Precedence within a Data
// Set): a new MTF_VOLB re-parents all following directories/files, and a new
// MTF_DIRB re-parents all following files. The reader's volume/device and
// directory path context must track the most recent VOLB/DIRB.
func TestSpecImpliedPrecedence(t *testing.T) {
	dirMT := time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local)
	fileMT := time.Date(2005, 1, 2, 0, 0, 0, 0, time.Local)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	// Volume D: with dir D1 and file f1.
	buf.Write(buildVOLB("D:"))
	buf.Write(buildDIRB(1, "D1", dirMT))
	buf.Write(buildFILE(10, 1, "f1", fileMT, []byte("a")))
	// A second VOLB (E:) re-parents subsequent entries.
	buf.Write(buildVOLB("E:"))
	buf.Write(buildDIRB(2, "E1", dirMT))
	buf.Write(buildFILE(11, 2, "f2", fileMT, []byte("b")))
	// A second DIRB under E: re-parents the next file.
	buf.Write(buildDIRB(3, "E2", dirMT))
	buf.Write(buildFILE(12, 3, "f3", fileMT, []byte("c")))
	buf.Write(buildESET())

	r := NewReader(NewSliceTape(buf.Bytes()))
	type entry struct {
		typ  EntryType
		name string
	}
	var got []entry
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
		got = append(got, entry{blk.Header.Type, blk.Header.Name})
	}

	want := []entry{
		{EntryVolume, "D:"},
		{EntryDirectory, "D:/D1"},
		{EntryFile, "D:/D1/f1"},
		{EntryVolume, "E:"},
		{EntryDirectory, "E:/E1"},
		{EntryFile, "E:/E1/f2"},
		{EntryDirectory, "E:/E2"},
		{EntryFile, "E:/E2/f3"},
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("entry %d = %+v, want %+v (spec Table 1 implied precedence)", i, got[i], w)
		}
	}
}

// TestSpecDirectoryPathSeparators verifies section 5.2.4: a directory name is
// stored with NUL path separators (e.g. "apps\0fred\0bloggs\0") and the reader
// must turn them into the display separator. The synthetic putString helper
// already NUL-terminates; here we encode a multi-segment DIRB name explicitly
// and check the joined path.
func TestSpecDirectoryPathSeparators(t *testing.T) {
	b := buildDIRB(1, "apps\x00fred\x00bloggs", time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local))
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(b)
	buf.Write(buildESET())

	r := NewReader(NewSliceTape(buf.Bytes()))
	for {
		blk, err := r.Next()
		if err != nil {
			break
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryDirectory {
			// NUL separators become '/', joined under the volume.
			if want := "C:/apps/fred/bloggs"; blk.Header.Name != want {
				t.Errorf("dir path = %q, want %q (spec 5.2.4 NUL->'/' separators)", blk.Header.Name, want)
			}
			return
		}
	}
	t.Fatal("no directory entry found")
}

// TestSpecRootDirectory verifies section 5.2.4: the root directory is stored as
// a single NUL character. The reader must surface it (joined to the volume)
// without error.
func TestSpecRootDirectory(t *testing.T) {
	b := buildDIRB(1, "", time.Date(2005, 1, 1, 0, 0, 0, 0, time.Local))
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	buf.Write(b)
	buf.Write(buildESET())

	r := NewReader(NewSliceTape(buf.Bytes()))
	for {
		blk, err := r.Next()
		if err != nil {
			break
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryDirectory {
			if want := "C:"; blk.Header.Name != want {
				t.Errorf("root dir path = %q, want %q", blk.Header.Name, want)
			}
			return
		}
	}
	t.Fatal("no directory entry found")
}

// TestSpecPadStreamIsTerminal verifies section 6.2.1.6: the SPAD stream is
// always the LAST data stream of a DBLK. A stream sequence STAN, NTEA, SPAD
// must materialize STAN as the file content and NTEA as metadata, then stop.
func TestSpecPadStreamIsTerminal(t *testing.T) {
	// A FILE with NTEA then STAN then SPAD; SPAD terminates. STAN is the content.
	ea := []byte("EA-DATA")
	content := []byte("file-body")
	file := buildFileWithStreams("a.txt",
		streamDescriptor(StreamNTEA, ea),
		streamDescriptor(StreamSTAN, content),
	)
	r := NewReader(NewSliceTape(streamArchive(file)))
	var h *Header
	for {
		blk, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryFile {
			h = blk.Header
			break
		}
	}
	if string(h.ExtendedAttributes) != "EA-DATA" {
		t.Errorf("NTEA = %q, want %q", h.ExtendedAttributes, ea)
	}
	if got := readAll(t, r); string(got) != "file-body" {
		t.Errorf("STAN content = %q, want %q", got, content)
	}
}

// TestSpecStreamAlignment verifies section 3.5.2: data streams are aligned on a
// 4-byte Stream Alignment Factor. A stream whose data length is not a multiple
// of 4 must be followed by the right pad so the next stream header is aligned.
func TestSpecStreamAlignment(t *testing.T) {
	// Two STAN-like metadata streams with non-aligned data lengths, then a SPAD.
	// buildStreams pads each descriptor to a 4-byte boundary (streamDescriptor).
	odd := []byte{1, 2, 3, 4, 5, 6, 7} // length 7 -> needs 1 pad byte
	file := buildFileWithStreams("odd.bin",
		streamDescriptor(StreamNTEA, odd),
		streamDescriptor(StreamSTAN, odd),
	)
	r := NewReader(NewSliceTape(streamArchive(file)))
	for {
		blk, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryFile {
			h := blk.Header
			if !bytes.Equal(h.ExtendedAttributes, odd) {
				t.Errorf("NTEA = %v, want %v (4-byte alignment must preserve odd-length data)", h.ExtendedAttributes, odd)
			}
			if got := readAll(t, r); !bytes.Equal(got, odd) {
				t.Errorf("STAN = %v, want %v", got, odd)
			}
			return
		}
	}
}
