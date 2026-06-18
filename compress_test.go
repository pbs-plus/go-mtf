package mtf

import (
	"bytes"
	"encoding/binary"
	"io"
	"strings"
	"testing"
)

// cmpFrame builds one MTF_CMP_HDR (24 bytes) + payload. The stream-media-format
// attributes field is left zero and the checksum is the word-XOR of the first
// 22 bytes. If compSize >= uncompSize the frame is emitted in "stored plain"
// form (payload is uncompressed), matching the MTF anti-expansion rule.
func cmpFrame(uncomp []byte) []byte {
	comp := lzsCompress(uncomp)
	storedPlain := len(comp) >= len(uncomp)
	payload := comp
	compLen := uint32(len(comp))
	if storedPlain {
		payload = uncomp
		compLen = uint32(len(uncomp))
	}
	hdr := make([]byte, cmpHeaderSize)
	binary.LittleEndian.PutUint16(hdr[0:2], cmpID)
	binary.LittleEndian.PutUint64(hdr[4:12], uint64(len(uncomp))) // remaining stream size
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(uncomp)))
	binary.LittleEndian.PutUint32(hdr[16:20], compLen)
	hdr[20] = 1 // sequence number
	sum := wordXORChecksum(hdr[:22])
	binary.LittleEndian.PutUint16(hdr[22:24], sum)
	return append(hdr, payload...)
}

// encFrame builds one MTF_ENC_HDR (24 bytes) + encrypted payload. The encrypted
// payload is whatever the encryptor produced; here we use a trivial XOR-with-key
// cipher for the round-trip. compLen holds the encrypted size, uncompLen the
// plaintext size.
func encFrame(plain []byte, key byte) []byte {
	ct := make([]byte, len(plain))
	for i := range plain {
		ct[i] = plain[i] ^ key
	}
	hdr := make([]byte, cmpHeaderSize)
	binary.LittleEndian.PutUint16(hdr[0:2], encID)
	binary.LittleEndian.PutUint64(hdr[4:12], uint64(len(plain)))
	binary.LittleEndian.PutUint32(hdr[12:16], uint32(len(plain)))
	binary.LittleEndian.PutUint32(hdr[16:20], uint32(len(ct)))
	hdr[20] = 1
	sum := wordXORChecksum(hdr[:22])
	binary.LittleEndian.PutUint16(hdr[22:24], sum)
	return append(hdr, ct...)
}

// compressedStan returns a STAN stream descriptor whose data is the given raw
// (frame-wrapped) bytes and whose media attributes mark it compressed.
func compressedStan(raw []byte, comp, enc bool) []byte {
	b := streamDescriptor(StreamSTAN, raw)
	attr := uint16(0)
	if comp {
		attr |= StreamMediaCompressed
	}
	if enc {
		attr |= StreamMediaEncrypted
	}
	putU16(b, stMediaAttrOff, int(attr))
	if comp {
		putU16(b, stCompressOff, int(AlgLZS221))
	}
	setStreamChecksum(b)
	return b
}

// TestReadCompressedStream builds a file whose STAN is LZS-compressed inside a
// CMP frame and verifies Read returns the original uncompressed bytes.
func TestReadCompressedStream(t *testing.T) {
	content := bytes.Repeat([]byte("compressed-data-payload-"), 40) // ~1KB
	raw := cmpFrame(content)
	file := buildFileWithStreams("c.bin", compressedStan(raw, true, false))
	r := NewReader(bytes.NewReader(streamArchive(file)))
	b := nextBlock(t, r)
	if !b.Header.Compressed {
		t.Fatalf("Compressed = false, want true")
	}
	got := readAll(t, r)
	if !bytes.Equal(got, content) {
		t.Errorf("decompressed content mismatch: got %d bytes, want %d", len(got), len(content))
	}
}

// TestReadStoredPlainStream covers the anti-expansion path: data that does not
// compress is stored uncompressed inside a CMP frame (compLen >= uncompLen).
func TestReadStoredPlainStream(t *testing.T) {
	content := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06} // incompressible
	raw := cmpFrame(content)
	file := buildFileWithStreams("s.bin", compressedStan(raw, true, false))
	r := NewReader(bytes.NewReader(streamArchive(file)))
	nextBlock(t, r)
	got := readAll(t, r)
	if !bytes.Equal(got, content) {
		t.Errorf("stored-plain mismatch: got %v, want %v", got, content)
	}
}

// TestReadEncryptedStream registers a decryptor (trivial XOR) and verifies an
// encrypted-only stream decrypts through Read.
func TestReadEncryptedStream(t *testing.T) {
	const key byte = 0x5A
	content := []byte("secret backup data that is not compressed")
	raw := encFrame(content, key)
	file := buildFileWithStreams("e.bin", compressedStan(raw, false, true))
	r := NewReader(bytes.NewReader(streamArchive(file)))
	r.SetDecryptor(func(algo uint16, ct []byte) ([]byte, error) {
		pt := make([]byte, len(ct))
		for i := range ct {
			pt[i] = ct[i] ^ key
		}
		return pt, nil
	})
	nextBlock(t, r)
	got := readAll(t, r)
	if !bytes.Equal(got, content) {
		t.Errorf("decrypted content mismatch: got %q, want %q", got, content)
	}
}

// TestReadEncryptedNoDecryptor verifies that an encrypted stream with no
// registered decryptor surfaces ErrEncrypted.
func TestReadEncryptedNoDecryptor(t *testing.T) {
	content := []byte("locked")
	raw := encFrame(content, 0x11)
	file := buildFileWithStreams("l.bin", compressedStan(raw, false, true))
	r := NewReader(bytes.NewReader(streamArchive(file)))
	nextBlock(t, r)
	buf := make([]byte, 32)
	_, err := r.Read(buf)
	if err != ErrEncrypted {
		t.Errorf("Read error = %v, want ErrEncrypted", err)
	}
}

func nextBlock(t *testing.T, r *Reader) *Block {
	t.Helper()
	for {
		b, err := r.Next()
		if err == io.EOF {
			t.Fatal("no entry block")
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if b.Kind == KindEntry && b.Header.Type == EntryFile {
			return b
		}
	}
}

// TestSkipCompressedEntry verifies that advancing Next() past a compressed file
// (without reading it) correctly skips its raw frame-wrapped bytes and lands on
// the following entry.
func TestSkipCompressedEntry(t *testing.T) {
	c1 := bytes.Repeat([]byte("AAAA-BBBB-CCCC-"), 30)
	c2 := []byte("second file plain content")
	file1 := buildFileWithStreams("first.bin", compressedStan(cmpFrame(c1), true, false))
	file2 := buildFileWithStreams("second.txt", streamDescriptor(StreamSTAN, c2))
	r := NewReader(bytes.NewReader(streamArchive(file1, file2)))

	// Skip the first (compressed) entry.
	b1 := nextBlock(t, r)
	if !strings.HasSuffix(b1.Header.Name, "first.bin") {
		t.Fatalf("first = %q", b1.Header.Name)
	}
	b2 := nextBlock(t, r)
	if !strings.HasSuffix(b2.Header.Name, "second.txt") {
		t.Fatalf("second = %q, want ...second.txt", b2.Header.Name)
	}
	got := readAll(t, r)
	if !bytes.Equal(got, c2) {
		t.Errorf("second file mismatch: got %q, want %q", got, c2)
	}
}
