package mtf

import (
	"encoding/binary"
	"errors"
	"io"
)

// MTF software-compression and data-encryption frame support.
//
// When a standard data stream (STAN) is compressed (STREAM_COMPRESSED) or
// encrypted (STREAM_ENCRYPTED), every stream data byte is wrapped in a sequence
// of Compression Frame Headers (MTF_CMP_HDR, §6.4.1) or Encryption Frame
// Headers (MTF_ENC_HDR, §6.5.1). Each 24-byte frame header gives the
// compressed/encrypted and uncompressed/unencrypted sizes for that frame.
//
// The only compression algorithm defined by the MTF spec is MTF_LZS221
// (0x0ABE, Appendix C). No data-encryption cipher is defined by the spec
// ("Data Encryption has not been defined", Appendix D): the MTF_ENC_HDR layout
// is parsed and the encrypted bytes are handed to a caller-supplied decryptor.
// When a stream is both compressed and encrypted, data was compressed first
// then encrypted; decoding therefore decrypts first, then decompresses (§6.4).

const (
	cmpHeaderSize = 24
	cmpID         = uint16(0x4846) // 'FH'
	encID         = uint16(0x4845) // 'EH'

	// MTF_LZS221: the single registered software compression algorithm (Appendix C).
	AlgLZS221 = uint16(0x0ABE)
)

// compressionFrame is a parsed MTF_CMP_HDR / MTF_ENC_HDR.
type compressionFrame struct {
	id              uint16
	uncompressedLen uint32 // uncompressed/unencrypted size for this frame
	compressedLen   uint32 // compressed/encrypted size for this frame
	storedPlain     bool   // frame holds uncompressed (anti-expansion) data
}

func wordXORChecksum(b []byte) uint16 {
	var sum uint16
	for i := 0; i+1 < len(b); i += 2 {
		sum ^= binary.LittleEndian.Uint16(b[i : i+2])
	}
	return sum
}

// parseFrameHeader reads and validates a 24-byte frame header from fr. The
// header checksum (word-wise XOR of the first 22 bytes) is verified; a mismatch
// is treated as corruption. The kind of frame ('FH'/'EH') must match wantEnc.
func parseFrameHeader(fr *frameReader, wantEnc bool) (compressionFrame, error) {
	var hdr [cmpHeaderSize]byte
	if _, err := io.ReadFull(fr, hdr[:]); err != nil {
		return compressionFrame{}, err
	}
	f := compressionFrame{
		id:              binary.LittleEndian.Uint16(hdr[0:2]),
		uncompressedLen: binary.LittleEndian.Uint32(hdr[12:16]),
		compressedLen:   binary.LittleEndian.Uint32(hdr[16:20]),
	}
	want := cmpID
	if wantEnc {
		want = encID
	}
	if f.id != want {
		return compressionFrame{}, errCorruptFrame
	}
	if s := wordXORChecksum(hdr[:22]); s != binary.LittleEndian.Uint16(hdr[22:24]) {
		return compressionFrame{}, errCorruptFrame
	}
	// Anti-expansion: when the compressed form is no smaller, the frame stores
	// the uncompressed bytes and compressedLen == uncompressedLen.
	if !wantEnc && f.compressedLen >= f.uncompressedLen {
		f.storedPlain = true
	}
	return f, nil
}

var errCorruptFrame = errors.New("mtf: corrupt compression/encryption frame header")

// frameReader is a minimal byte source over the stream's raw (still
// compressed/encrypted) bytes. It is fed by the Reader's readStreamData so that
// frames are streamed lazily rather than buffered whole.
type frameReader struct {
	r  *Reader
	n  int64 // raw stream bytes remaining
	br []byte
	bi int
}

func (f *frameReader) Read(p []byte) (int, error) {
	if f.bi < len(f.br) {
		n := copy(p, f.br[f.bi:])
		f.bi += n
		return n, nil
	}
	if f.n <= 0 {
		return 0, io.EOF
	}
	want := min(int64(len(p)), f.n)
	nr, err := f.r.readStreamData(p[:want])
	f.n -= int64(nr)
	if nr > 0 {
		return nr, nil
	}
	if err == nil {
		err = io.EOF
	}
	return 0, err
}

// decoder streams the decompressed (and, if needed, decrypted) bytes of a
// compressed/encrypted STAN. It pulls raw bytes from the underlying Reader via a
// frameReader, peels frame headers, optionally decrypts (per r.decryptor), and
// decompresses (LZS) — yielding the original logical stream content.
type decoder struct {
	r    *Reader
	fr   *frameReader
	out  []byte // decoded bytes pending delivery
	oi   int
	eof  bool
	lzs  *lzsDecoder
	enc  bool // stream is encrypted
	comp bool // stream is compressed
}

func newDecoder(r *Reader, enc, comp bool, rawLen int64) *decoder {
	return &decoder{
		r:    r,
		fr:   &frameReader{r: r, n: rawLen},
		enc:  enc,
		comp: comp,
		lzs:  newLZSDecoder(),
	}
}

// Read implements io.Reader over the decoded stream.
func (d *decoder) Read(p []byte) (int, error) {
	if d.oi < len(d.out) {
		n := copy(p, d.out[d.oi:])
		d.oi += n
		return n, nil
	}
	if d.eof {
		return 0, io.EOF
	}
	d.out = d.out[:0]
	d.oi = 0
	if err := d.fill(); err != nil {
		d.eof = true
		if len(d.out) > 0 {
			return d.deliver(p)
		}
		return 0, err
	}
	return d.deliver(p)
}

func (d *decoder) deliver(p []byte) (int, error) {
	n := copy(p, d.out)
	d.oi = n
	return n, nil
}

// fill decodes the next frame into d.out. For an encrypted stream the raw frame
// bytes are decrypted via the registered decryptor first; if none is registered
// this returns [ErrEncrypted]. Compression is then reversed with LZS.
func (d *decoder) fill() error {
	if d.fr.n <= 0 {
		return io.EOF
	}
	f, err := parseFrameHeader(d.fr, d.enc)
	if err != nil {
		return err
	}
	payload := make([]byte, f.compressedLen)
	if _, err := io.ReadFull(d.fr, payload); err != nil {
		return err
	}
	// Encryption, when active, wraps the whole stream (frame headers included
	// when both codecs are used, but a frame's *payload* is always the
	// encrypted unit here).
	if d.enc {
		if d.r.decryptor == nil {
			return ErrEncrypted
		}
		dec, err := d.r.decryptor(d.r.streamEncAlgo, payload)
		if err != nil {
			return err
		}
		payload = dec
	}
	if !d.comp || f.storedPlain {
		// Stored uncompressed (or encryption-only): payload is already plain.
		d.out = append(d.out, payload...)
		return nil
	}
	// LZS decompression (per-frame independent history).
	d.lzs = newLZSDecoder()
	out, err := d.lzs.decode(payload, d.out[:0])
	if err != nil {
		return err
	}
	d.out = out
	return nil
}

// ErrEncrypted is returned when a stream is encrypted but no decryptor has been
// registered with [Reader.SetDecryptor].
var ErrEncrypted = errors.New("mtf: stream is encrypted; register a decryptor with SetDecryptor")

// Decryptor reverses the encryption of one frame's payload. algo is the stream's
// Data Encryption Algorithm ID (from the STAN header); encrypted holds the raw
// encrypted bytes. It must return the plaintext of exactly those bytes. The MTF
// spec defines no cipher, so the implementation is vendor-specific.
type Decryptor func(algo uint16, encrypted []byte) ([]byte, error)
