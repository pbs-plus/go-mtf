package mtf

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
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
	setStreamChecksum(b)
	return b
}

// setStreamChecksum computes and stores the MTF_STREAM_HDR Checksum (word-wise
// XOR over bytes 0..19, stored at bytes 20..21) on the stream header buffer.
func setStreamChecksum(b []byte) {
	var sum uint16
	for i := 0; i < stChecksumOff; i += 2 {
		sum ^= u16(b, i)
	}
	putU16(b, stChecksumOff, int(sum))
}

// sparseStanDescriptor emits a STAN stream header carrying the STREAM_IS_SPARSE
// bit with a length of zero, as specified in MTF section 6.2.1.7.
func sparseStanDescriptor() []byte {
	b := streamDescriptor(StreamSTAN, nil)
	putU16(b, stSysAttrOff, int(StreamFSSparse))
	setStreamChecksum(b)
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
	setStreamChecksum(spad)
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
	setChecksum(preamble)
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
	setChecksum(preamble)
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

// streamDescriptorCksum emits a stream descriptor with a correct word-wise XOR
// header checksum, matching what real writers (Backup Exec, ntbackup) record.
// Real archives always set this checksum, and the reader's stream-location
// logic relies on it to disambiguate the next header position.
func streamDescriptorCksum(typ uint32, data []byte) []byte {
	b := streamDescriptor(typ, data)
	var sum uint16
	for i := 0; i < stChecksumOff; i += 2 {
		sum ^= uint16(b[i]) | uint16(b[i+1])<<8
	}
	putU16(b, stChecksumOff, int(sum))
	return b
}

// streamDescriptorRaw lays out a stream descriptor with exact control over the
// data length and whether trailing 4-byte alignment padding is added. It is
// used to reproduce writer quirks (e.g. Backup Exec placing the next stream
// header immediately at the data end without alignment padding).
func streamDescriptorRaw(typ uint32, data []byte, pad bool) []byte {
	total := streamHeaderSize + len(data)
	if pad {
		if m := total % 4; m != 0 {
			total += 4 - m
		}
	}
	b := make([]byte, total)
	putU32(b, stTypeOff, typ)
	putU64(b, stLengthOff, uint64(len(data)))
	copy(b[streamHeaderSize:], data)
	var sum uint16
	for i := 0; i < stChecksumOff; i += 2 {
		sum ^= uint16(b[i]) | uint16(b[i+1])<<8
	}
	putU16(b, stChecksumOff, int(sum))
	return b
}

// TestVendorPadStreamThenSTAN reproduces a Backup Exec LTO tape layout where a
// FILE block's first stream is an unknown vendor pad stream (the writer uses a
// private 4-byte type, not a spec-defined SPAD) whose data length is chosen so
// the following real data stream (STAN) begins at a *non*-4-byte-aligned
// offset. The spec mandates 4-byte-aligned stream headers, but Backup Exec
// places the next header immediately at the data end with no alignment padding.
// The reader must locate the STAN via its checksum rather than assuming the
// aligned position, otherwise it desyncs into the file data.
func TestVendorPadStreamThenSTAN(t *testing.T) {
	const vendorPad uint32 = 0x44415043 // "CPAD" - Backup Exec private pad stream
	fileData := []byte("MP3-CONTENT")

	// Pad-stream data length is even but not 4-aligned (no trailing pad), so the
	// following STAN header lands at an offset that is 2 mod 4.
	padStream := streamDescriptorRaw(vendorPad, make([]byte, 6), false)
	stan := streamDescriptorCksum(StreamSTAN, fileData)

	arc := streamArchive(buildFileWithStreams("song.mp3", padStream, stan))
	r := NewReader(bytes.NewReader(arc))
	var sawFile bool
	for {
		b, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("walk desynced: %v", err)
		}
		if b.Kind == KindEntry && b.Header.Type == EntryFile {
			sawFile = true
			if !strings.HasSuffix(b.Header.Name, "song.mp3") {
				t.Errorf("name = %q, want suffix song.mp3", b.Header.Name)
			}
			if b.Header.Size != int64(len(fileData)) {
				t.Errorf("size = %d, want %d", b.Header.Size, len(fileData))
			}
		}
	}
	if !sawFile {
		t.Fatal("file entry not found; reader likely desynced on the pad stream")
	}
}

// TestStreamLessPackedDBLK reproduces a Backup Exec data set where the
// structural DBLKs (SSET/VOLB/DIRB) carry no data streams and their Offset To
// First Event points at the next DBLK in the packed descriptor block, per the
// MTF spec ("Offset To First Event"). The reader must recognise the next bytes
// as a DBLK header and treat the current block as stream-less rather than
// trying to parse the next DBLK as a stream.
func TestStreamLessPackedDBLK(t *testing.T) {
	// Build SSET/VOLB/DIRB packed at FLB boundaries with dbOff pointing to the
	// next DBLK (no SPAD), then a normal FILE with a STAN the reader extracts.
	const flb = testFLBSize
	sset := buildSSET()
	putU16(sset, dbOffOff, flb) // points at VOLB
	setChecksum(sset)
	volb := buildVOLB("D:")
	putU16(volb, dbOffOff, flb) // points at DIRB
	setChecksum(volb)
	dirb := buildDirbWithStreams(1, "dir")
	putU16(dirb, dbOffOff, flb) // points at FILE
	setChecksum(dirb)

	content := []byte("hello")
	file := buildFileWithStreams("f.txt", streamDescriptorCksum(StreamSTAN, content))

	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(sset)
	buf.Write(volb)
	buf.Write(dirb)
	buf.Write(file)
	buf.Write(buildESET())

	r := NewReader(bytes.NewReader(buf.Bytes()))
	var gotFile bool
	for {
		b, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("walk desynced on packed stream-less DBLKs: %v", err)
		}
		if b.Kind == KindEntry && b.Header.Type == EntryFile {
			gotFile = true
			if !strings.HasSuffix(b.Header.Name, "f.txt") {
				t.Errorf("name = %q, want suffix f.txt", b.Header.Name)
			}
		}
	}
	if !gotFile {
		t.Fatal("file after stream-less packed DBLKs not reached")
	}
}

// nextOfType advances r until it returns an entry of the given type.
func nextOfType(r *Reader, want EntryType) *Header {
	for {
		blk, err := r.Next()
		if err != nil {
			panic(err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
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

// TestExtraStreamsCaptured verifies that streams without a named Header field
// (NTOI before STAN, CSUM after STAN) are preserved in Header.Streams instead
// of being dropped, and that the file content still reads correctly.
func TestExtraStreamsCaptured(t *testing.T) {
	objectID := []byte("OBJ-ID-12345678")
	preCSUM := []byte("PRECSUM")
	postCSUM := []byte("POSTCSUM")
	content := bytes.Repeat([]byte("Z"), 400)
	file := buildFileWithStreams("f.bin",
		streamDescriptor(StreamNTOI, objectID),
		streamDescriptor(StreamCSUM, preCSUM),
		streamDescriptor(StreamSTAN, content),
		streamDescriptor(StreamCSUM, postCSUM),
	)
	r := NewReader(bytes.NewReader(streamArchive(file)))
	h := nextOfType(r, EntryFile)

	if got := readAll(t, r); !bytes.Equal(got, content) {
		t.Fatalf("content mismatch: got %d bytes, want %d", len(got), len(content))
	}

	// Pre-STAN and post-STAN extra streams must both be captured.
	var ntoi, csums []byte
	for _, s := range h.Streams {
		if s.Type == StreamNTOI {
			ntoi = s.Data
		}
		if s.Type == StreamCSUM {
			csums = append(csums, s.Data...)
		}
	}
	if !bytes.Equal(ntoi, objectID) {
		t.Errorf("NTOI = %q, want %q", ntoi, objectID)
	}
	if want := append(append([]byte(nil), preCSUM...), postCSUM...); !bytes.Equal(csums, want) {
		t.Errorf("CSUM streams = %q, want %q", csums, want)
	}
}

// TestHeaderOnlySkipsStreamData verifies that in header-only mode the stream
// bytes are not materialized (Streams is nil) while the file still parses and
// reads back correctly when not in header-only mode.
func TestHeaderOnlySkipsStreamData(t *testing.T) {
	content := bytes.Repeat([]byte("Q"), 500)
	file := buildFileWithStreams("f.bin",
		streamDescriptor(StreamNTOI, []byte("OBJ")),
		streamDescriptor(StreamSTAN, content),
		streamDescriptor(StreamCSUM, []byte("C")),
	)

	build := func() *Reader { return NewReader(bytes.NewReader(streamArchive(file))) }

	// header-only: Streams must be empty and content skipped without error.
	r := build()
	r.HeaderOnly()
	h := nextOfType(r, EntryFile)
	if len(h.Streams) != 0 {
		t.Errorf("header-only Streams = %d entries, want 0", len(h.Streams))
	}

	// normal: extra streams captured and content readable.
	r2 := build()
	h2 := nextOfType(r2, EntryFile)
	if len(h2.Streams) == 0 {
		t.Errorf("normal Streams empty, want NTOI/CSUM captured")
	}
	if got := readAll(t, r2); !bytes.Equal(got, content) {
		t.Errorf("content mismatch: got %d, want %d", len(got), len(content))
	}
}

func TestMaterializeAdvancesAcrossEntries(t *testing.T) {
	content := []byte("ABC")
	file1 := buildFileWithStreams("a.txt", streamDescriptor(StreamSTAN, content))
	file2 := buildFileWithStreams("b.txt", streamDescriptor(StreamSTAN, content))
	r := NewReader(bytes.NewReader(streamArchive(file1, file2)))

	seen := 0
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
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

// TestLargeCatalogStreamNotCapped is the regression test for the Backup Exec
// archive whose ESET carried a 154 MiB TFDD Media Based Catalog. Before the
// fix, captureCatalog routed catalog streams through readStreamBytes, which
// rejects any stream above maxMetadataStreamSize (64 MiB) and aborted the whole
// conversion at end-of-set. Catalog streams must instead use the higher
// maxCatalogStreamSize bound.
func TestLargeCatalogStreamNotCapped(t *testing.T) {
	// A TFDD catalog stream whose length exceeds the per-file metadata cap but
	// is well within the catalog cap.
	catLen := int64(maxMetadataStreamSize + 1)
	if catLen > maxCatalogStreamSize {
		t.Skip("catalog cap smaller than metadata cap; test invalid")
	}

	// Build the raw bytes the reader will consume: the stream header (its
	// declared length drives the cap check) followed by catLen payload bytes.
	// We avoid materialising a >64 MiB payload by giving the reader a real
	// declared length but a short backing stream and asserting the failure is
	// NOT the "out of range" guard (i.e. the cap is no longer the blocker).
	hdr := make([]byte, streamHeaderSize)
	binary.LittleEndian.PutUint32(hdr[stTypeOff:], StreamTFDD)
	binary.LittleEndian.PutUint64(hdr[stLengthOff:], uint64(catLen))
	// Only a few payload bytes are actually available; the read will then hit
	// io.ErrUnexpectedEOF, which is the expected outcome (NOT "out of range").
	feed := append(hdr, []byte("only-a-few-bytes")...)

	r := NewReader(&nonSeeker{data: feed})
	r.blk = append(r.blk[:0], hdr...) // stream header already buffered
	r.streamOff = 0
	r.streamType = StreamTFDD
	r.streamLen = catLen
	r.streamDid = 0
	// Simulate the metadata-stream path (the old behaviour) rejecting the size.
	if _, err := r.readStreamBytes(catLen); err == nil ||
		!containsStr(err.Error(), "out of range") {
		t.Fatalf("readStreamBytes should reject catalog-sized stream, got: %v", err)
	}
	// The catalog path must NOT reject it on the size guard: it should fail
	// only because the backing stream is short (ErrUnexpectedEOF), proving the
	// cap no longer blocks large catalogs.
	_, err := r.readCatalogStream(catLen)
	if err != nil && containsStr(err.Error(), "out of range") {
		t.Fatalf("readCatalogStream rejected a valid-sized catalog stream: %v", err)
	}
	_ = r
}

// TestLargeCatalogStreamEndToEnd builds an ESET whose TFDD catalog stream is a
// real payload slightly larger than maxMetadataStreamSize, embedded in a full
// archive, and asserts captureCatalog reads it whole without an "out of range"
// error. Kept small (cap+1 is ~64 MiB) — tagged so it can be skipped under
// memory pressure.
func TestLargeCatalogStreamEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates ~64 MiB catalog payload")
	}
	catLen := int(maxMetadataStreamSize) + 1
	payload := make([]byte, catLen) // zero-filled; only length matters here

	// ESET (88-byte common) + TFDD header + payload + 4-align + SPAD + 512-align.
	eset := make([]byte, 88)
	copy(eset[0:4], "ESET")
	eset[44] = 1 // ASCII string type
	binary.LittleEndian.PutUint16(eset[8:], 88)
	setChecksum(eset)

	tfddHdr := make([]byte, streamHeaderSize)
	binary.LittleEndian.PutUint32(tfddHdr[stTypeOff:], StreamTFDD)
	binary.LittleEndian.PutUint64(tfddHdr[stLengthOff:], uint64(len(payload)))
	setStreamChecksum(tfddHdr)
	eset = append(eset, tfddHdr...)
	eset = append(eset, payload...)
	for len(eset)%4 != 0 {
		eset = append(eset, 0)
	}
	spad := make([]byte, streamHeaderSize)
	binary.LittleEndian.PutUint32(spad[stTypeOff:], StreamSPAD)
	setStreamChecksum(spad)
	eset = append(eset, spad...)
	for len(eset)%512 != 0 {
		eset = append(eset, 0)
	}

	var out bytes.Buffer
	out.Write(buildTape())
	out.Write(buildSSET())
	out.Write(eset)
	out.Write(bytes.Repeat([]byte{0}, 512)) // trailing zero block / clean EOF

	r := NewReader(bytes.NewReader(out.Bytes()))
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next over large TFDD catalog: %v", err)
		}
	}
	c := r.Catalog()
	if c == nil {
		t.Fatal("Catalog() nil after large TFDD stream")
	}
	if got, want := len(c.RawFDD), catLen; got != want {
		t.Errorf("captured catalog payload = %d bytes, want %d", got, want)
	}
}

func containsStr(haystack, needle string) bool {
	return bytes.Contains([]byte(haystack), []byte(needle))
}

var _ = fmt.Sprintf
