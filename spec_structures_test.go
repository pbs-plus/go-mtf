package mtf

// spec_structures_test.go verifies the structural definitions from MTF-100a
// that are consumed but not already covered by the offset/constants tests:
// the Sparse Frame Header (Structure 17), Compression/Encryption Frame Headers
// (Structures 24/25), and the word-wise XOR checksum algorithms used by the
// common header (Structure 4), stream header (Structure 15) and frame headers.

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// TestSpecSparseFrameHeader verifies Structure 17: a SPAR stream begins with an
// 8-byte UINT64 offset that is INCLUDED in the Stream Length, so the non-hole
// data length is StreamLength - 8. parseSparseExtent must split on exactly that
// boundary.
func TestSpecSparseFrameHeader(t *testing.T) {
	// Build a SPAR payload: 8-byte offset + N data bytes.
	offset := int64(4096)
	data := []byte("hello sparse")
	payload := make([]byte, 8+len(data))
	binary.LittleEndian.PutUint64(payload[0:8], uint64(offset))
	copy(payload[8:], data)

	ext := parseSparseExtent(payload)
	if ext.Offset != offset {
		t.Errorf("sparse offset = %d, want %d (spec Structure 17)", ext.Offset, offset)
	}
	if !bytes.Equal(ext.Data, data) {
		t.Errorf("sparse data = %q, want %q (offset taken from the leading 8 bytes)", ext.Data, data)
	}
}

// TestSpecSparseFrameHeaderShortPayload confirms a SPAR payload shorter than the
// 8-byte frame header degrades gracefully rather than panicking.
func TestSpecSparseFrameHeaderShortPayload(t *testing.T) {
	ext := parseSparseExtent([]byte{0x01, 0x02, 0x03})
	if ext.Offset != 0 {
		t.Errorf("short payload offset = %d, want 0", ext.Offset)
	}
	if len(ext.Data) != 3 {
		t.Errorf("short payload data len = %d, want 3", len(ext.Data))
	}
}

// TestSpecCompressionFrameHeaderOffsets verifies MTF_CMP_HDR (Structure 24) field
// offsets as read by parseFrameHeader: ID @0, Uncompressed Size @12, Compressed
// Size @16, Sequence @20, Checksum @22, total 24 bytes.
func TestSpecCompressionFrameHeaderOffsets(t *testing.T) {
	if cmpHeaderSize != 24 {
		t.Errorf("cmpHeaderSize = %d, want 24 (spec Structure 24)", cmpHeaderSize)
	}
	if cmpID != 0x4846 {
		t.Errorf("cmpID = %#x, want 0x4846 ('FH') (spec Structure 24)", cmpID)
	}
	if encID != 0x4845 {
		t.Errorf("encID = %#x, want 0x4845 ('EH') (spec Structure 25)", encID)
	}
	// MTF_LZS221 is the single registered compression algorithm (Appendix C).
	if AlgLZS221 != 0x0ABE {
		t.Errorf("AlgLZS221 = %#x, want 0x0ABE (spec Appendix C)", AlgLZS221)
	}
}

// TestSpecCompressionFrameRoundTrip builds a Compression Frame Header per spec
// Structure 24 (with a valid checksum), wraps an uncompressed "stored" payload,
// and asserts the decoder yields the original bytes. The 'stored plain' path is
// the anti-expansion case the spec describes: compressed size >= uncompressed
// size means the frame stores the raw bytes.
func TestSpecCompressionFrameRoundTrip(t *testing.T) {
	plain := []byte("the quick brown fox")
	// A stored frame: uncompressed == compressed == len(plain).
	hdr := make([]byte, cmpHeaderSize)
	binary.LittleEndian.PutUint16(hdr[0:2], cmpID)                // 'FH'
	binary.LittleEndian.PutUint64(hdr[4:12], uint64(len(plain)))  // remaining stream size
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(plain))) // uncompressed size
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(plain))) // compressed size
	hdr[20] = 1                                                   // sequence number starts at 1
	chk := wordXORChecksum(hdr[:22])
	binary.LittleEndian.PutUint16(hdr[22:24], chk)

	dec := newDecoder(nil, false, true, int64(len(hdr)+len(plain)))
	// Feed the frame + payload through the decoder's frameReader buffer.
	dec.fr = &frameReader{r: nil, n: int64(len(hdr) + len(plain))}
	dec.fr.br = append(append([]byte{}, hdr...), plain...)
	out, err := bytesReadN(dec, len(plain))
	if err != nil {
		t.Fatalf("decode stored frame: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("stored frame decoded = %q, want %q", out, plain)
	}
}

// bytesReadN reads exactly n bytes from r (helper for the frame test).
func bytesReadN(r interface{ Read([]byte) (int, error) }, n int) ([]byte, error) {
	buf := make([]byte, n)
	got := 0
	for got < n {
		k, err := r.Read(buf[got:])
		got += k
		if err != nil {
			return buf[:got], err
		}
	}
	return buf, nil
}

// TestSpecHeaderChecksum verifies the common-header word-wise XOR checksum
// (Structure 4 "Header Checksum"): the checksum field is a 16-bit word-wise XOR
// over all MTF_DB_HDR fields except the checksum field itself (bytes 0..49).
func TestSpecHeaderChecksum(t *testing.T) {
	// A 52-byte block with a known pattern in bytes 0..49.
	b := make([]byte, dbCommonSize)
	for i := range dbChecksumOff {
		b[i] = byte(i * 3)
	}
	want := wordXORBytes(b[:dbChecksumOff])
	binary.LittleEndian.PutUint16(b[dbChecksumOff:], want)
	if !checksumValid(b) {
		t.Errorf("checksumValid = false for a correctly checksummed header")
	}
	// Corruption of any pre-checksum byte must invalidate it.
	b[5] ^= 0xFF
	if checksumValid(b) {
		t.Error("checksumValid = true after corrupting a header byte")
	}
}

// TestSpecStreamHeaderChecksum verifies the stream-header checksum (Structure 15
// "Checksum"): word-wise XOR over Stream ID..Compression Algorithm (bytes 0..19)
// stored at offset 20.
func TestSpecStreamHeaderChecksum(t *testing.T) {
	b := make([]byte, streamHeaderSize)
	for i := range stChecksumOff {
		b[i] = byte(0x11 * (i + 1))
	}
	want := wordXORBytes(b[:stChecksumOff])
	binary.LittleEndian.PutUint16(b[stChecksumOff:], want)
	if !streamChecksumValid(b, 0) {
		t.Error("streamChecksumValid = false for a correctly checksummed stream header")
	}
	b[3] ^= 0xFF
	if streamChecksumValid(b, 0) {
		t.Error("streamChecksumValid = true after corrupting a stream-header byte")
	}
}

// wordXORBytes is the spec's word-wise XOR over pairs of bytes (little-endian
// 16-bit words), re-implemented here so the checksum tests are independent of
// the package's own wordXORChecksum / commonChecksum helpers.
func wordXORBytes(b []byte) uint16 {
	var sum uint16
	for i := 0; i+1 < len(b); i += 2 {
		sum ^= uint16(b[i]) | uint16(b[i+1])<<8
	}
	return sum
}
