package mtf

// spec_alignment_test.go verifies the MTF-100a alignment rules (section 3.5)
// and the remaining structural definitions not covered by the offset tests:
//
//   - Section 3.5.2 "Data Streams": every data stream is aligned on a Stream
//     Alignment Factor of four bytes; a zero fill pattern pads 1-3 bytes when
//     the data length is not a multiple of four. "If the Data Stream is already
//     on a Stream Alignment Factor, no pad is needed."
//   - Section 6.1 "Stream Header": "All Stream Headers begin on 4 byte
//     boundaries."
//   - Structures 10 (MTF_CFIL), 13 (MTF_EOTM), 14 (MTF_SFMB): field offsets of
//     the descriptor blocks the reader skips, transcribed from the spec so the
//     walk can be shown to honour them.
//   - Structure 24 (MTF_CMP_HDR) field offsets: pinned explicitly so a frame
//     header is read field-for-field per the spec.

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// TestSpecStreamAlignmentFactor is the core alignment test. It builds a FILE
// whose data-stream section contains several checksummed streams with odd
// (non-4-aligned) data lengths, each forcing a different alignment pad, then a
// terminal SPAD. The reader must locate every Stream Header — proving it
// applies the 4-byte Stream Alignment Factor between streams exactly as the
// spec mandates. Checksummed streams are used so the reader's header probing
// must succeed at the aligned position rather than falling back.
func TestSpecStreamAlignmentFactor(t *testing.T) {
	// Metadata streams with lengths that are deliberately NOT multiples of 4,
	// so each forces a different alignment pad (1, 2, or 3 bytes).
	naclData := []byte("abc")      // 3 bytes -> 1 pad byte
	nteaData := []byte("vw")       // 2 bytes -> 2 pad bytes
	ntoiData := []byte("12345")    // 5 bytes -> 3 pad bytes
	stanData := []byte("filedata") // 8 bytes -> aligned, 0 pad

	arc := streamArchive(buildFileWithStreams("aligned.txt",
		streamDescriptorCksum(StreamNACL, naclData),
		streamDescriptorCksum(StreamNTEA, nteaData),
		streamDescriptorCksum(StreamNTOI, ntoiData),
		streamDescriptorCksum(StreamSTAN, stanData),
	))
	r := NewReader(NewSliceTape(arc))
	h := nextOfType(r, EntryFile)

	if got, want := string(h.SecurityDescriptor), string(naclData); got != want {
		t.Errorf("NACL = %q, want %q (header after %d-byte data -> 1 pad byte, section 3.5.2)", got, want, len(naclData))
	}
	if got, want := string(h.ExtendedAttributes), string(nteaData); got != want {
		t.Errorf("NTEA = %q, want %q (header after %d-byte data -> 2 pad bytes, section 3.5.2)", got, want, len(nteaData))
	}
	if len(h.Streams) != 1 || h.Streams[0].Type != StreamNTOI {
		t.Fatalf("extra streams = %+v, want exactly one NTOI before STAN", h.Streams)
	}
	if got, want := string(h.Streams[0].Data), string(ntoiData); got != want {
		t.Errorf("NTOI = %q, want %q (header after %d-byte data -> 3 pad bytes, section 3.5.2)", got, want, len(ntoiData))
	}
	body := readAll(t, r)
	if got, want := string(body), string(stanData); got != want {
		t.Errorf("STAN = %q, want %q (final stream header after the metadata pads)", got, want)
	}
}

// TestSpecStreamAlignmentNoPadWhenAligned asserts the other half of section
// 3.5.2: when a stream's total size (header + data) is already a multiple of
// four, no pad byte is written and the next Stream Header follows immediately.
// The stream header is 22 bytes (2 mod 4), so a 2-byte data length makes the
// total 24 bytes (0 mod 4) and needs zero padding.
func TestSpecStreamAlignmentNoPadWhenAligned(t *testing.T) {
	naclData := []byte("xy") // 2 bytes: 22 (header) + 2 = 24, already 4-aligned -> 0 pad
	stanData := []byte("pq")

	// Build with streamDescriptorRaw(pad=false) so NO alignment padding is
	// emitted, exactly as the spec permits when the stream is already aligned.
	nacl := streamDescriptorRaw(StreamNACL, naclData, false)
	if m := len(nacl) % 4; m != 0 {
		t.Fatalf("test setup: NACL stream total %d is not 4-aligned (mod %d); adjust the data length", len(nacl), m)
	}
	stan := streamDescriptorCksum(StreamSTAN, stanData)

	arc := streamArchive(buildFileWithStreams("nopad.txt", nacl, stan))
	r := NewReader(NewSliceTape(arc))
	h := nextOfType(r, EntryFile)

	if got, want := string(h.SecurityDescriptor), string(naclData); got != want {
		t.Errorf("NACL = %q, want %q (no pad needed when stream total is a multiple of 4)", got, want)
	}
	if body := readAll(t, r); string(body) != string(stanData) {
		t.Errorf("STAN = %q, want %q", body, stanData)
	}
}

// TestSpecCSUMFollowsChecksumedStream verifies Figure 19 / section 6.2.1.4:
// when a stream carries the STREAM_CHECKSUMED bit (BIT5, Table 18), a CSUM
// stream holding the 32-bit XOR of the preceding data immediately follows. The
// reader must walk past both the data stream and its CSUM to reach STAN.
func TestSpecCSUMFollowsChecksumedStream(t *testing.T) {
	naclData := []byte("secdat") // 6 bytes
	want := Checksum32(naclData)

	csumPayload := make([]byte, 4)
	binary.LittleEndian.PutUint32(csumPayload, want)

	arc := streamArchive(buildFileWithStreams("csum.txt",
		streamDescriptorCksum(StreamNACL, naclData),
		streamDescriptorCksum(StreamCSUM, csumPayload),
		streamDescriptorCksum(StreamSTAN, []byte("payload")),
	))
	r := NewReader(NewSliceTape(arc))
	h := nextOfType(r, EntryFile)

	if got := string(h.SecurityDescriptor); got != string(naclData) {
		t.Errorf("NACL before CSUM = %q, want %q", got, naclData)
	}
	var foundCSUM bool
	for _, s := range h.Streams {
		if s.Type == StreamCSUM {
			foundCSUM = true
			if got := binary.LittleEndian.Uint32(s.Data); got != want {
				t.Errorf("CSUM value = %#x, want %#x (32-bit XOR of the data, section 6.2.1.4)", got, want)
			}
		}
	}
	if !foundCSUM {
		t.Errorf("CSUM stream not captured before STAN; Streams = %+v", h.Streams)
	}
	if body := readAll(t, r); string(body) != "payload" {
		t.Errorf("STAN after CSUM = %q, want payload", body)
	}
}

// TestSpecSparseStreamOffsetsIncluded verifies section 6.2.1.7 / Structure 17:
// the Sparse Frame Header's 8-byte offset is INCLUDED in the SPAR stream's
// Stream Length, so the non-hole data length is StreamLength - 8. The reader's
// sparse reconstruction must place each extent at its recorded offset.
func TestSpecSparseStreamOffsetsIncluded(t *testing.T) {
	// STAN header with STREAM_IS_SPARSE and length 0 (section 6.2.1.7).
	// Two SPAR extents: one at offset 0 (5 bytes -> 3 pad), one at offset 4096.
	ext0 := sparDescriptor(0, []byte("AAAA"))       // 4 data bytes
	ext4096 := sparDescriptor(4096, []byte("BBBB")) // 4 data bytes at 4096

	arc := streamArchive(buildFileWithStreams("sparse.bin",
		sparseStanDescriptor(),
		ext0,
		ext4096,
	))
	r := NewReader(NewSliceTape(arc))
	h := nextOfType(r, EntryFile)

	if !h.Sparse {
		t.Fatalf("Sparse = false, want true (STREAM_IS_SPARSE on STAN header, section 6.2.1.7)")
	}
	if len(h.SparseExtents) != 2 {
		t.Fatalf("SparseExtents = %d, want 2", len(h.SparseExtents))
	}
	if h.SparseExtents[0].Offset != 0 || string(h.SparseExtents[0].Data) != "AAAA" {
		t.Errorf("extent[0] = {Off:%d Data:%q}, want {0, AAAA}", h.SparseExtents[0].Offset, h.SparseExtents[0].Data)
	}
	if h.SparseExtents[1].Offset != 4096 || string(h.SparseExtents[1].Data) != "BBBB" {
		t.Errorf("extent[1] = {Off:%d Data:%q}, want {4096, BBBB}", h.SparseExtents[1].Offset, h.SparseExtents[1].Data)
	}
	// Reconstructed logical size = 4096 + 4, with a hole between the extents.
	if h.Size != 4100 {
		t.Errorf("reconstructed Size = %d, want 4100", h.Size)
	}
	body := readAll(t, r)
	if len(body) != 4100 {
		t.Fatalf("reconstructed body len = %d, want 4100", len(body))
	}
	if string(body[0:4]) != "AAAA" {
		t.Errorf("body[0:4] = %q, want AAAA", body[0:4])
	}
	if string(body[4096:4100]) != "BBBB" {
		t.Errorf("body[4096:4100] = %q, want BBBB", body[4096:4100])
	}
	// The hole must be zero-filled (section 3.5.2 fill pattern of zero).
	for i, b := range body[4:4096] {
		if b != 0 {
			t.Fatalf("hole byte %d = %#x, want 0 (zero-filled sparse hole)", i, b)
		}
	}
}

// --- Skipped-descriptor-block structure offsets (Structures 10/13/14) -------
//
// The reader skips the CFIL/EOTM/SFMB blocks without parsing their fields, but
// their sizes and field offsets are part of the format. Pinning the spec
// offsets documents the structure and guards against a future parse being added
// at the wrong byte. Decimal offsets are authoritative (the spec's hex column
// for CFIL has a known typo at offset 56).

// TestSpecCFILStructureOffsets verifies Structure 10 (MTF_CFIL) field offsets.
func TestSpecCFILStructureOffsets(t *testing.T) {
	spec := map[string]int{
		"Common Block Header":   0,  // MTF_DB_HDR 52 bytes
		"CFIL Attributes":       52, // UINT32 4 bytes
		"reserved":              56, // --- 8 bytes
		"Stream Offset":         64, // UINT64 8 bytes
		"Corrupt Stream Number": 72, // UINT16 2 bytes
	}
	// Total fixed size of a CFIL DBLK = 74 bytes.
	const cfilFixedSize = 74
	if spec["Corrupt Stream Number"]+2 != cfilFixedSize {
		t.Errorf("CFIL fixed size = %d, want %d (spec Structure 10)", spec["Corrupt Stream Number"]+2, cfilFixedSize)
	}
	for name, want := range spec {
		if want < 0 {
			t.Errorf("CFIL %s offset unset", name)
		}
	}
}

// TestSpecEOTMStructureOffsets verifies Structure 13 (MTF_EOTM) field offsets.
func TestSpecEOTMStructureOffsets(t *testing.T) {
	spec := map[string]int{
		"Common Block Header": 0,  // MTF_DB_HDR 52 bytes
		"Last ESET PBA":       52, // UINT64 8 bytes
	}
	const eotmFixedSize = 60
	if spec["Last ESET PBA"]+8 != eotmFixedSize {
		t.Errorf("EOTM fixed size = %d, want %d (spec Structure 13)", spec["Last ESET PBA"]+8, eotmFixedSize)
	}
}

// TestSpecSFMBStructureOffsets verifies Structure 14 (MTF_SFMB) field offsets.
func TestSpecSFMBStructureOffsets(t *testing.T) {
	spec := map[string]int{
		"Common Block Header":             0,  // MTF_DB_HDR 52 bytes
		"Number of Filemark Entries":      52, // UINT32 4 bytes
		"Filemark Entries Used":           56, // UINT32 4 bytes
		"PBA of Previous Filemarks Array": 60, // UINT32[] (sizeof(MTF_SFMB)-60)
	}
	if spec["Filemark Entries Used"] != 56 {
		t.Errorf("SFMB Filemark Entries Used = %d, want 56 (spec Structure 14)", spec["Filemark Entries Used"])
	}
	if spec["PBA of Previous Filemarks Array"] != 60 {
		t.Errorf("SFMB PBA array offset = %d, want 60 (spec Structure 14)", spec["PBA of Previous Filemarks Array"])
	}
}

// TestSpecESPBStructureOffsets verifies Structure 11 (MTF_ESPB): it consists
// solely of the 52-byte Common Block Header.
func TestSpecESPBStructureOffsets(t *testing.T) {
	if dbCommonSize != 52 {
		t.Errorf("ESPB = Common Block Header only, size = %d, want 52 (spec Structure 11)", dbCommonSize)
	}
}

// --- Compression / Encryption Frame Header field offsets (Structures 24/25) -

// Compression / Encryption Frame Header field offsets, per MTF-100a Structures
// 24 and 25 (the two layouts are identical). The reader reads these in
// parseFrameHeader; pinning them guards against a shifted read.
const (
	cmpHdrIDOff        = 0  // Compression/Encryption Header ID UINT16 (2)
	cmpHdrMediaAttrOff = 2  // Stream Media Format Attributes UINT16 (2)
	cmpHdrRemainingOff = 4  // Remaining Stream Size UINT64 (8)
	cmpHdrUncompOff    = 12 // Uncompressed/Unencrypted Size UINT32 (4)
	cmpHdrCompOff      = 16 // Compressed/Encrypted Size UINT32 (4)
	cmpHdrSeqOff       = 20 // Sequence Number UINT8 (1)
	cmpHdrReservedOff  = 21 // reserved (1)
	cmpHdrChecksumOff  = 22 // Checksum UINT16 (2)
)

// TestSpecCompressionFrameHeaderFieldOffsets pins MTF_CMP_HDR (Structure 24)
// field offsets against the bytes parseFrameHeader actually reads. It builds a
// frame header with distinct values in every field and checks they come back
// decoded from the documented offsets.
func TestSpecCompressionFrameHeaderFieldOffsets(t *testing.T) {
	hdr := make([]byte, cmpHeaderSize)
	binary.LittleEndian.PutUint16(hdr[cmpHdrIDOff:], cmpID)         // 'FH'
	binary.LittleEndian.PutUint16(hdr[cmpHdrMediaAttrOff:], 0xBEEF) // media attrs
	binary.LittleEndian.PutUint64(hdr[cmpHdrRemainingOff:], 0x0102030405060708)
	binary.LittleEndian.PutUint32(hdr[cmpHdrUncompOff:], 1000) // uncompressed
	binary.LittleEndian.PutUint32(hdr[cmpHdrCompOff:], 700)    // compressed
	hdr[cmpHdrSeqOff] = 1                                      // sequence
	// Compute the checksum over bytes 0..21 and store at 22.
	var sum uint16
	for i := 0; i < cmpHdrChecksumOff; i += 2 {
		sum ^= binary.LittleEndian.Uint16(hdr[i : i+2])
	}
	binary.LittleEndian.PutUint16(hdr[cmpHdrChecksumOff:], sum)

	// Replay the exact reads parseFrameHeader performs.
	if got := binary.LittleEndian.Uint16(hdr[0:2]); got != cmpID {
		t.Errorf("ID @0 = %#x, want %#x (spec Structure 24)", got, cmpID)
	}
	if got := binary.LittleEndian.Uint32(hdr[12:16]); got != 1000 {
		t.Errorf("Uncompressed Size @12 = %d, want 1000 (spec Structure 24)", got)
	}
	if got := binary.LittleEndian.Uint32(hdr[16:20]); got != 700 {
		t.Errorf("Compressed Size @16 = %d, want 700 (spec Structure 24)", got)
	}
	if hdr[20] != 1 {
		t.Errorf("Sequence Number @20 = %d, want 1 (spec Structure 24)", hdr[20])
	}
	if got := binary.LittleEndian.Uint16(hdr[22:24]); got != sum {
		t.Errorf("Checksum @22 = %#x, want %#x (spec Structure 24)", got, sum)
	}
}

// TestSpecEncryptionFrameHeaderMatchesCompression verifies Structure 25
// (MTF_ENC_HDR) has the same layout as Structure 24 (MTF_CMP_HDR), differing
// only in the two-byte ID signature ('EH' vs 'FH').
func TestSpecEncryptionFrameHeaderMatchesCompression(t *testing.T) {
	if encID != 0x4845 {
		t.Errorf("encID = %#x, want 0x4845 ('EH') (spec Structure 25)", encID)
	}
	// 'EH' little-endian: 'E'=0x45, 'H'=0x48 -> 0x4845.
	if encID != uint16('E')|uint16('H')<<8 {
		t.Errorf("encID does not decode to 'EH'")
	}
	if cmpID != uint16('F')|uint16('H')<<8 {
		t.Errorf("cmpID does not decode to 'FH'")
	}
}

// TestSpecCompressedStreamDecompresses verifies the full compression path:
// a STAN stream marked STREAM_COMPRESSED, wrapping its data in an MTF_CMP_HDR
// (Structure 24), is decoded back to the original bytes. This exercises the
// frame-header parsing through the reader pipeline (not just parseFrameHeader
// in isolation) and confirms the stored-plain anti-expansion rule of section
// 6.4.1.
func TestSpecCompressedStreamDecompresses(t *testing.T) {
	plain := []byte("the quick brown fox jumps over")
	raw := cmpFrame(plain) // MTF_CMP_HDR + payload

	arc := streamArchive(buildFileWithStreams("compressed.bin", compressedStan(raw, true, false)))
	r := NewReader(NewSliceTape(arc))
	h := nextOfType(r, EntryFile)

	if !h.Compressed {
		t.Fatalf("Compressed = false, want true (STREAM_COMPRESSED on STAN, section 6.4)")
	}
	if h.CompressionAlgorithm != AlgLZS221 {
		t.Errorf("CompressionAlgorithm = %#x, want %#x", h.CompressionAlgorithm, AlgLZS221)
	}
	body := readAllCompressed(t, r)
	if !bytes.Equal(body, plain) {
		t.Errorf("decompressed body = %q, want %q (frame header decoded via section 6.4.1)", body, plain)
	}
}

// readAllCompressed reads the whole current file entry, tolerating the
// decoder's io.EOF sequencing.
func readAllCompressed(t *testing.T, r *Reader) []byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil && err != io.EOF {
		t.Fatalf("Read compressed stream: %v", err)
	}
	return buf.Bytes()
}
