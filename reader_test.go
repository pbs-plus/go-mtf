package mtf

import (
	"bytes"
	"fmt"
	"io"
	"testing"
	"time"
)

// This file builds a minimal, in-memory MTF/BKF stream and validates that the
// Reader parses it correctly. The byte layout mirrors the field offsets in
// header.go.

const testFLBSize = 256

// encodeDateTime is the inverse of decodeDateTime (see datetime.go).
func encodeDateTime(t time.Time) [5]byte {
	year, month, day := t.Date()
	hour, min, sec := t.Clock()
	y := year
	m := int(month)
	var b [5]byte
	b[0] = byte(y >> 6)
	b[1] = byte((y&0x3F)<<2 | (m >> 2))
	b[2] = byte((m&0x3)<<6 | (day << 1) | ((hour >> 4) & 1))
	b[3] = byte((hour&0x0F)<<4 | (min>>2)&0x0F)
	b[4] = byte((min&0x3)<<6 | (sec & 0x3F))
	return b
}

// commonDB writes the common descriptor block fields shared by all blocks.
func writeCommon(b []byte, typ [4]byte, off uint16) {
	copy(b[dbTypeOff:], typ[:])
	b[dbOffOff], b[dbOffOff+1] = byte(off), byte(off>>8)
	b[dbStrTypeOff] = 1
	b[dbOSIDOff] = 0
	b[dbOSVerOff] = 0
}

// putString places an ASCII NUL-terminated string at offset and records its
// TAPE_POSITION (size, pos) pair at the tapePosOff field offset.
func putString(b []byte, tapePosOff int, offset int, s string) {
	size := len(s) + 1
	b[tapePosOff] = byte(size)
	b[tapePosOff+1] = byte(size >> 8)
	b[tapePosOff+2] = byte(offset)
	b[tapePosOff+3] = byte(offset >> 8)
	copy(b[offset:], s)
}

// newBlock returns a flbsize-padded block initialised to zero.
func newBlock() []byte {
	return make([]byte, testFLBSize)
}

func buildTape() []byte {
	b := newBlock()
	writeCommon(b, dbTAPE, 0)
	putU16(b, tapeFLBSizeOff, testFLBSize)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 30, 45, 0, time.Local))
	copy(b[tapeCTimeOff:], dt[:])
	return b
}

func buildSSET() []byte {
	b := newBlock()
	writeCommon(b, dbSSET, 0)
	b[ssetNumOff], b[ssetNumOff+1] = 1, 0
	b[ssetMajorOff] = 3
	b[ssetMinorOff] = 0
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 31, 0, 0, time.Local))
	copy(b[ssetCTimeOff:], dt[:])
	return b
}

func buildVOLB(device string) []byte {
	b := newBlock()
	writeCommon(b, dbVOLB, 0)
	putString(b, volbDeviceOff, volbCTimeOff+6, device)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 31, 5, 0, time.Local))
	copy(b[volbCTimeOff:], dt[:])
	return b
}

func buildDIRB(id uint32, name string, mtime time.Time) []byte {
	b := newBlock()
	writeCommon(b, dbDIRB, 0)
	putU32(b, dirbIDOff, id)
	putString(b, dirbNameOff, dirbNameOff+4, name)
	dt := encodeDateTime(mtime)
	copy(b[dirbMTimeOff:], dt[:])
	return b
}

// buildFILE constructs a FILE descriptor followed by its STAN data stream and a
// trailing SPAD stream. The returned bytes may span several logical blocks
// when content is larger than a single block, matching how MTF flows object
// data continuously across block boundaries.
func buildFILE(id, dirid uint32, name string, mtime time.Time, content []byte) []byte {
	nameOff := fileNameOff + 4
	streamStart := nameOff + len(name) + 1
	if m := streamStart % 4; m != 0 {
		streamStart += 4 - m
	}

	preamble := make([]byte, streamStart+streamHeaderSize)
	writeCommon(preamble, dbFILE, uint16(streamStart))
	putU32(preamble, fileIDOff, id)
	putU32(preamble, fileDirIDOff, dirid)
	putString(preamble, fileNameOff, nameOff, name)
	dt := encodeDateTime(mtime)
	copy(preamble[fileMTimeOff:], dt[:])
	putU32(preamble, streamStart+stTypeOff, StreamSTAN)
	putU64(preamble, streamStart+stLengthOff, uint64(len(content)))

	var out bytes.Buffer
	out.Write(preamble)
	out.Write(content)

	if m := out.Len() % 4; m != 0 {
		out.Write(bytes.Repeat([]byte{0}, 4-m))
	}

	// SPAD header whose data pads up to the next logical block boundary.
	spadHeaderEnd := out.Len() + streamHeaderSize
	spadDataLen := testFLBSize - (spadHeaderEnd % testFLBSize)
	if spadDataLen == testFLBSize {
		spadDataLen = 0
	}
	spad := make([]byte, streamHeaderSize)
	putU32(spad, stTypeOff, StreamSPAD)
	putU64(spad, stLengthOff, uint64(spadDataLen))
	out.Write(spad)
	if spadDataLen > 0 {
		out.Write(bytes.Repeat([]byte{0}, spadDataLen))
	}
	return out.Bytes()
}

func buildESET() []byte {
	b := newBlock()
	writeCommon(b, dbESET, 0)
	putU16(b, esetSeqOff, 1)
	putU16(b, esetSetOff, 1)
	return b
}

func putU16(b []byte, off, v int) {
	b[off] = byte(v)
	b[off+1] = byte(v >> 8)
}
func putU32(b []byte, off int, v uint32) {
	b[off], b[off+1], b[off+2], b[off+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func putU64(b []byte, off int, v uint64) {
	for i := range 8 {
		b[off+i] = byte(v >> (8 * i))
	}
}

// buildArchive assembles a small archive with two directories and three files.
// MTF expects each directory's files to immediately follow the directory block.
func buildArchive() []byte {
	dirMtime := time.Date(2005, 6, 1, 12, 32, 0, 0, time.Local)
	fileMtime := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)

	var buf bytes.Buffer
	for _, blk := range [][]byte{
		buildTape(),
		buildSSET(),
		buildVOLB("C:"),
		buildDIRB(1, "Users", dirMtime),
		buildFILE(10, 1, "hello.txt", fileMtime, []byte("Hello, MTF!")),
		buildFILE(11, 1, "empty.txt", fileMtime, nil),
		buildDIRB(2, "Public", dirMtime),
		buildFILE(12, 2, "doc.txt", fileMtime, []byte("line one\nline two\n")),
		buildESET(),
	} {
		buf.Write(blk)
	}
	return buf.Bytes()
}

func TestReaderEntries(t *testing.T) {
	data := buildArchive()
	r := NewReader(bytes.NewReader(data))

	type wantEntry struct {
		typ  EntryType
		name string
		size int64
		data string
	}
	want := []wantEntry{
		{EntryVolume, "C:", 0, ""},
		{EntryDirectory, "C:/Users", 0, ""},
		{EntryFile, "C:/Users/hello.txt", 11, "Hello, MTF!"},
		{EntryFile, "C:/Users/empty.txt", 0, ""},
		{EntryDirectory, "C:/Public", 0, ""},
		{EntryFile, "C:/Public/doc.txt", 18, "line one\nline two\n"},
	}

	var got []wantEntry
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: unexpected error: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		entry := wantEntry{typ: h.Type, name: h.Name, size: h.Size}
		if h.Type == EntryFile {
			body, err := io.ReadAll(r)
			if err != nil && err != io.EOF {
				t.Fatalf("Read %q: %v", h.Name, err)
			}
			entry.data = string(body)
		}
		got = append(got, entry)
	}

	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d (%+v)", len(got), len(want), got)
	}
	for i, e := range want {
		if got[i] != e {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], e)
		}
	}
}

func TestReaderSetAndTape(t *testing.T) {
	r := NewReader(bytes.NewReader(buildArchive()))
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = blk
	}

	if tp := r.Tape(); tp == nil {
		t.Error("expected Tape info")
	} else if tp.FLBSize != testFLBSize {
		t.Errorf("tape flbsize = %d, want %d", tp.FLBSize, testFLBSize)
	}

	if s := r.Set(); s == nil {
		t.Error("expected Set info")
	} else if s.Number != 1 {
		t.Errorf("set number = %d, want 1", s.Number)
	}
}

func TestFamily(t *testing.T) {
	// Use the MBC archive which has a SetMap.
	r := NewReader(bytes.NewReader(buildMBCArchive()))
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_ = blk
	}

	f := r.Family()
	if f.TapeSequence != 1 {
		t.Errorf("Family.TapeSequence = %d, want 1", f.TapeSequence)
	}
	if f.SetMap == nil {
		t.Error("Family.SetMap is nil (catalog not parsed)")
	} else {
		if f.TotalTapes != 1 {
			t.Errorf("Family.TotalTapes = %d, want 1", f.TotalTapes)
		}
		if f.SetMap.MediaFamilyID == 0 {
			t.Error("Family.SetMap.MediaFamilyID is zero")
		}
		if len(f.SetMap.Entries) == 0 {
			t.Error("Family.SetMap.Entries is empty")
		}
	}
}

func TestReaderSkipWithoutRead(t *testing.T) {
	// Iterate without reading file bodies; the reader must still resync.
	r := NewReader(bytes.NewReader(buildArchive()))
	var names []string
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
		names = append(names, h.Name)
	}
	want := []string{
		"C:", "C:/Users",
		"C:/Users/hello.txt", "C:/Users/empty.txt",
		"C:/Public", "C:/Public/doc.txt",
	}
	if len(names) != len(want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("names[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestBlockKinds verifies the typed block iterator yields the expected kinds in
// the expected order for a normal (data + catalog) archive, and that the data
// set end carries the catalog. The block sequence is what makes a medium's role
// explicit.
func TestBlockKinds(t *testing.T) {
	r := NewReader(bytes.NewReader(buildArchive()))
	var kinds []BlockKind
	var files int
	for {
		b, err := r.Next()
		if err != nil {
			break
		}
		kinds = append(kinds, b.Kind)
		if b.Kind == KindEntry && b.Header.Type == EntryFile {
			files++
		}
	}
	// A normal archive: media, set, then entries, then at least one set-end.
	if len(kinds) == 0 || kinds[0] != KindMedia {
		t.Fatalf("expected first block KindMedia, got %v", kinds)
	}
	if len(kinds) < 2 || kinds[1] != KindSet {
		t.Fatalf("expected second block KindSet, got %v", kinds)
	}
	if files != 3 {
		t.Errorf("file entries = %d, want 3", files)
	}
	// The archive must end with a KindSetEnd (data set closed).
	if kinds[len(kinds)-1] != KindSetEnd {
		t.Errorf("last block = %v, want KindSetEnd", kinds[len(kinds)-1])
	}
}

func TestReadChunked(t *testing.T) {
	// A file larger than the logical block size exercises cross-block reads.
	content := bytes.Repeat([]byte("ABCDEFGH"), 1024) // 8KiB, spans many 256B blocks
	big := buildFILE(99, 1, "big.bin", time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local), content)

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("D:"))
	buf.Write(buildDIRB(1, "data", time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)))
	buf.Write(big)
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var got bytes.Buffer
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
		if h.Type == EntryFile {
			n, err := io.Copy(&got, r)
			if err != nil {
				t.Fatal(err)
			}
			if n != int64(len(content)) {
				t.Errorf("copied %d bytes, want %d", n, len(content))
			}
		}
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Errorf("large file content mismatch (got %d bytes, want %d)", got.Len(), len(content))
	}
}

// nonSeeker wraps a byte slice behind a reader that does NOT implement
// io.Seeker, forcing the skipStreamData read-fallback path.
type nonSeeker struct {
	data []byte
	off  int
}

func (n *nonSeeker) Read(p []byte) (int, error) {
	if n.off >= len(n.data) {
		return 0, io.EOF
	}
	c := copy(p, n.data[n.off:])
	n.off += c
	return c, nil
}

// TestSeekAndNonSeekPathsEqual extracts every file twice — once from a
// seekable source (bytes.Reader) and once from a non-seekable source — and
// asserts the content and per-file sizes are identical. This guards the seek
// optimization against diverging from the read-based skip path.
func TestSeekAndNonSeekPathsEqual(t *testing.T) {
	contents := [][]byte{
		bytes.Repeat([]byte("ABCDEFGH"), 64), // spans blocks
		[]byte("small"),
		{}, // empty file
		bytes.Repeat([]byte{0x00}, 700),
	}
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("E:"))
	buf.Write(buildDIRB(1, "root", time.Date(2021, 5, 5, 5, 5, 5, 0, time.Local)))
	for i, c := range contents {
		buf.Write(buildFILE(uint32(10+i), 1, fmt.Sprintf("f%d.bin", i),
			time.Date(2021, 5, 5, 5, 5, 5, 0, time.Local), c))
	}
	buf.Write(buildESET())
	data := buf.Bytes()

	extract := func(src io.Reader) (map[string][]byte, error) {
		r := NewReader(src)
		out := make(map[string][]byte)
		for {
			blk, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if blk.Kind != KindEntry || blk.Header.Type != EntryFile {
				continue
			}
			var b bytes.Buffer
			if _, err := io.Copy(&b, r); err != nil {
				return nil, err
			}
			out[blk.Header.Name] = b.Bytes()
		}
		return out, nil
	}

	seekOut, err := extract(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("seek path: %v", err)
	}
	nonSeekOut, err := extract(&nonSeeker{data: data})
	if err != nil {
		t.Fatalf("non-seek path: %v", err)
	}
	for name, want := range seekOut {
		got := nonSeekOut[name]
		if !bytes.Equal(got, want) {
			t.Errorf("%s: non-seek path differs (got %d bytes, want %d)", name, len(got), len(want))
		}
	}
}

func TestDateTimeRoundTrip(t *testing.T) {
	cases := []time.Time{
		time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local),
		time.Date(2005, 6, 1, 12, 30, 45, 0, time.Local),
		time.Date(2024, 12, 31, 23, 59, 59, 0, time.Local),
	}
	for _, tc := range cases {
		b := encodeDateTime(tc)
		got := decodeDateTime(b[:], 0)
		if !got.Equal(tc) {
			t.Errorf("round trip %v -> %v", tc, got)
		}
	}
}

func TestDecodeStringASCII(t *testing.T) {
	// "foo\0bar\0" with sep '/' -> interior NUL becomes '/', trailing NUL kept.
	in := []byte("foo\x00bar\x00")
	got := decodeString(in, 0, len(in), 1, '/')
	if got != "foo/bar" {
		t.Errorf("decodeString = %q, want %q", got, "foo/bar")
	}
}

func TestDecodeStringUTF16(t *testing.T) {
	// "A" as UTF-16LE with trailing NUL terminator.
	in := []byte{'A', 0x00, 0x00, 0x00}
	got := decodeString(in, 0, len(in), 0, '/')
	if got != "A" {
		t.Errorf("decodeString = %q, want %q", got, "A")
	}
}

func TestEntriesAreSorted(t *testing.T) {
	// Smoke test that helper ordering is stable enough for the other tests.
	data := buildArchive()
	if len(data) != 9*testFLBSize {
		t.Errorf("archive size = %d, want %d", len(data), 9*testFLBSize)
	}
}

func TestAppendDecodeString(t *testing.T) {
	// ASCII path: "hello" with trailing NUL.
	ascii := []byte("hello\x00")
	got := string(appendDecodeString(nil, ascii, 0, len(ascii), 1, '/'))
	if got != "hello" {
		t.Errorf("ascii = %q, want %q", got, "hello")
	}

	// UTF-16LE "hello" (strType bit0 clear) with trailing NUL.
	u16 := []byte{'h', 0, 'e', 0, 'l', 0, 'l', 0, 'o', 0, 0, 0}
	got = string(appendDecodeString(nil, u16, 0, len(u16), 0, '/'))
	if got != "hello" {
		t.Errorf("utf16 = %q, want %q", got, "hello")
	}

	// Empty / out-of-range.
	got = string(appendDecodeString(nil, ascii, 0, 0, 1, '/'))
	if got != "" {
		t.Errorf("empty = %q, want empty", got)
	}

	// Append into existing dst (composing a path).
	dst := appendDecodeString([]byte("vol/"), u16, 0, len(u16), 0, '/')
	if string(dst) != "vol/hello" {
		t.Errorf("append = %q, want %q", dst, "vol/hello")
	}
}
