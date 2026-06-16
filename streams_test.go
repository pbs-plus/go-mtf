package mtf

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

func streamDescriptor(typ uint32, data []byte) []byte {
	total := streamHeaderSize + len(data)
	if m := total % 4; m != 0 {
		total += 4 - m
	}
	b := make([]byte, total)
	putU32(b, stTypeOff, typ)
	putU64(b, stLengthOff, uint64(len(data)))
	copy(b[streamHeaderSize:], data)
	return b
}

// sparseStanDescriptor emits a STAN stream header carrying the STREAM_IS_SPARSE
// bit with a length of zero, as specified in MTF section 6.2.1.7.
func sparseStanDescriptor() []byte {
	b := streamDescriptor(StreamSTAN, nil)
	putU16(b, stSysAttrOff, int(StreamFSSparse))
	return b
}

// sparDescriptor emits a SPAR stream whose data is an 8-byte sparse frame
// header (offset) followed by the non-hole byte content.
func sparDescriptor(offset uint64, data []byte) []byte {
	payload := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint64(payload[:8], offset)
	copy(payload[8:], data)
	return streamDescriptor(StreamSPAR, payload)
}

// buildStreams emits a sequence of stream descriptors followed by a terminal
// SPAD whose data pads the object (preamble + streams) up to the next FLB
// block boundary. preambleLen is the byte length of the descriptor block that
// precedes the streams.
func buildStreams(preambleLen int, streams ...[]byte) []byte {
	var out bytes.Buffer
	for _, s := range streams {
		out.Write(s)
	}
	spadHeaderEnd := preambleLen + out.Len() + streamHeaderSize
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

func buildFileWithStreams(name string, streams ...[]byte) []byte {
	nameOff := fileNameOff + 4
	start := nameOff + len(name) + 1
	if m := start % 4; m != 0 {
		start += 4 - m
	}
	preamble := make([]byte, start)
	writeCommon(preamble, dbFILE, uint16(start))
	putU32(preamble, fileIDOff, 1)
	putU32(preamble, fileDirIDOff, 0)
	putString(preamble, fileNameOff, nameOff, name)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 0, 0, 0, time.Local))
	copy(preamble[fileMTimeOff:], dt[:])
	var out bytes.Buffer
	out.Write(preamble)
	out.Write(buildStreams(len(preamble), streams...))
	return out.Bytes()
}

func buildDirbWithStreams(id uint32, name string, streams ...[]byte) []byte {
	nameOff := dirbNameOff + 4
	start := nameOff + len(name) + 1
	if m := start % 4; m != 0 {
		start += 4 - m
	}
	preamble := make([]byte, start)
	writeCommon(preamble, dbDIRB, uint16(start))
	putU32(preamble, dirbIDOff, id)
	putString(preamble, dirbNameOff, nameOff, name)
	var out bytes.Buffer
	out.Write(preamble)
	out.Write(buildStreams(len(preamble), streams...))
	return out.Bytes()
}

func streamArchive(entries ...[]byte) []byte {
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	for _, e := range entries {
		buf.Write(e)
	}
	buf.Write(buildESET())
	return buf.Bytes()
}

// nextOfType advances r until it returns an entry of the given type.
func nextOfType(r *Reader, want EntryType) *Header {
	for {
		h, err := r.Next()
		if err != nil {
			panic(err)
		}
		if h.Type == want {
			return h
		}
	}
}

func readAll(t *testing.T, r *Reader) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMaterializeDirectoryMetadata(t *testing.T) {
	nacl := []byte{0x01, 0x00, 0x04, 0x80, 0x14, 0x00, 0x00, 0x00}
	ntoi := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	dir := buildDirbWithStreams(1, "Users",
		streamDescriptor(StreamNACL, nacl),
		streamDescriptor(StreamNTOI, ntoi),
	)
	r := NewReader(bytes.NewReader(streamArchive(dir)))
	h := nextOfType(r, EntryDirectory)

	if !bytes.Equal(h.SecurityDescriptor, nacl) {
		t.Fatalf("dir SecurityDescriptor = % x, want % x", h.SecurityDescriptor, nacl)
	}
	// NTOI has no Header field and is not materialized; it is simply skipped.
	// Read on a directory yields no content.
	if got := readAll(t, r); len(got) != 0 {
		t.Fatalf("directory Read = %d bytes, want 0", len(got))
	}
}

func TestMaterializeFileMetadataAndContent(t *testing.T) {
	ntea := []byte("EA-DATA")
	content := bytes.Repeat([]byte("X"), 300)
	file := buildFileWithStreams("doc.txt",
		streamDescriptor(StreamNTEA, ntea),
		streamDescriptor(StreamSTAN, content),
	)
	r := NewReader(bytes.NewReader(streamArchive(file)))
	h := nextOfType(r, EntryFile)

	if !bytes.Equal(h.ExtendedAttributes, ntea) {
		t.Fatalf("ExtendedAttributes = %q, want %q", h.ExtendedAttributes, ntea)
	}
	if h.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", h.Size, len(content))
	}
	if got := readAll(t, r); !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

func TestMaterializeAdvancesAcrossEntries(t *testing.T) {
	content := []byte("ABC")
	file1 := buildFileWithStreams("a.txt", streamDescriptor(StreamSTAN, content))
	file2 := buildFileWithStreams("b.txt", streamDescriptor(StreamSTAN, content))
	r := NewReader(bytes.NewReader(streamArchive(file1, file2)))

	seen := 0
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if h.Type != EntryFile {
			continue
		}
		if h.Size != int64(len(content)) {
			t.Fatalf("%s: Size = %d, want %d", h.Name, h.Size, len(content))
		}
		if got := readAll(t, r); !bytes.Equal(got, content) {
			t.Fatalf("%s: content mismatch", h.Name)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("saw %d file entries, want 2", seen)
	}
}

func TestMaterializeSparseFile(t *testing.T) {
	// Logical file: [0..3] = "ABCD", [4..7] = hole, [8..11] = "EFGH"
	block0 := []byte("ABCD")
	block1 := []byte("EFGH")
	want := []byte("ABCD\x00\x00\x00\x00EFGH")

	file := buildFileWithStreams("sparse.bin",
		sparseStanDescriptor(),
		sparDescriptor(0, block0),
		sparDescriptor(8, block1),
	)
	r := NewReader(bytes.NewReader(streamArchive(file)))
	h := nextOfType(r, EntryFile)

	if !h.Sparse {
		t.Fatal("Sparse flag not set")
	}
	if len(h.SparseExtents) != 2 {
		t.Fatalf("SparseExtents len = %d, want 2", len(h.SparseExtents))
	}
	if h.SparseExtents[0].Offset != 0 || !bytes.Equal(h.SparseExtents[0].Data, block0) {
		t.Fatalf("extent 0 = {Off %d, %q}, want {0, %q}", h.SparseExtents[0].Offset, h.SparseExtents[0].Data, block0)
	}
	if h.SparseExtents[1].Offset != 8 || !bytes.Equal(h.SparseExtents[1].Data, block1) {
		t.Fatalf("extent 1 = {Off %d, %q}, want {8, %q}", h.SparseExtents[1].Offset, h.SparseExtents[1].Data, block1)
	}
	if h.Size != int64(len(want)) {
		t.Fatalf("Size = %d, want %d", h.Size, len(want))
	}
	if got := readAll(t, r); !bytes.Equal(got, want) {
		t.Fatalf("sparse content = %q, want %q", got, want)
	}
}
