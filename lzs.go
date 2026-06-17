package mtf

import "errors"

// errCorruptLZS is returned when an LZS compressed block is malformed.
var errCorruptLZS = errors.New("mtf: corrupt LZS compressed block")

// LZS (Lempel-Ziv-Stac) decompression, implementing ANSI X3.241-1994 as used
// by the MTF MTF_LZS221 (0x0ABE) software compression algorithm. The format is
// described in MTF spec Appendix C and in RFC 1967 / RFC 1974.
//
// The compressed stream is a sequence of variable-bit-width tokens terminated
// by a 9-bit end marker (110000000). Each token is either a literal byte or an
// offset/length back-reference into a 2048-byte sliding history window:
//
//	<Stream> := [<String>] <EndMarker>
//	<String> := 0 <8-bit literal> | 1 <Offset> <Length>
//	<Offset> := 1 <7-bit offset>          // offset 1..127
//	          | 0 <11-bit offset>         // offset 128..2047
//	<EndMarker> := 110000000
//
// Offset 1 refers to the most recently output byte. The minimum match length
// is 2. The length is Elias-style coded (see lzsLength).
//
// LZS221 streams in MTF compression frames are independent: the history window
// is reset at the start of each frame.

const lzsHistorySize = 2048

// lzsDecoder decompresses a single LZS block. Call decode with the compressed
// bytes; it appends the uncompressed output to out and returns it. The history
// window is held in win and is zeroed at construction.
type lzsDecoder struct {
	win     [lzsHistorySize]byte
	pos     int // next write position in win (mod lzsHistorySize)
	written int // total bytes written to win (for offset wraparound)
}

func newLZSDecoder() *lzsDecoder { return &lzsDecoder{} }

// lzsBitReader walks the compressed bitstream MSB-first within each byte.
type lzsBitReader struct {
	buf []byte
	byt int // current byte index
	bit int // bit index within current byte (0 = MSB .. 7 = LSB)
}

func (b *lzsBitReader) readBit() (int, error) {
	if b.byt >= len(b.buf) {
		return 0, errCorruptLZS
	}
	v := (b.buf[b.byt] >> (7 - b.bit)) & 1
	b.bit++
	if b.bit == 8 {
		b.bit = 0
		b.byt++
	}
	return int(v), nil
}

// readBits reads n bits MSB-first and returns them as an integer (n <= 16).
func (b *lzsBitReader) readBits(n int) (int, error) {
	v := 0
	for range n {
		bit, err := b.readBit()
		if err != nil {
			return 0, err
		}
		v = (v << 1) | bit
	}
	return v, nil
}

// decode decompresses one LZS block (compressed) into out. Per X3.241-1994 a
// trailing zero byte may have been stripped by the compressor, and the
// receiver must treat the end marker as the authoritative terminator.
func (d *lzsDecoder) decode(compressed []byte, out []byte) ([]byte, error) {
	br := &lzsBitReader{buf: compressed}
	for {
		flag, err := br.readBit()
		if err != nil {
			// Running out of bits before an end marker can be legitimate if the
			// final padding zero byte was stripped. Stop cleanly.
			return out, nil
		}
		if flag == 0 {
			// Literal byte.
			b, err := br.readBits(8)
			if err != nil {
				return out, nil
			}
			out = d.put(out, byte(b))
			continue
		}
		// Match: read offset, then length. First detect the end marker.
		offset, err := d.readOffset(br)
		if err != nil {
			return out, nil
		}
		if offset == lzsEndOffset {
			return out, nil
		}
		length, err := lzsLength(br)
		if err != nil {
			return out, nil
		}
		out = d.copyMatch(out, offset, length)
	}
}

// readOffset reads an offset token. Offset encoding:
//
//	1 bbbbbbb            -> 7-bit offset (1..127)
//	0 bbbbbbbbbbb        -> 11-bit offset (128..2047)
//	110000000 (9 bits)   -> end marker
//
// The leading flag bit (1) was already consumed. The next bit distinguishes a
// 7-bit offset (1) from an 11-bit offset (0). The end marker is the bit
// pattern that, after the leading 1, begins 1 0 (i.e. 11-bit form) followed by
// eight more zeros: 1 0 000000000 == end. We handle this inline.
func (d *lzsDecoder) readOffset(br *lzsBitReader) (int, error) {
	first, err := br.readBit()
	if err != nil {
		return 0, err
	}
	if first == 1 {
		// 7-bit offset.
		off, err := br.readBits(7)
		if err != nil {
			return 0, err
		}
		off++
		if off > 127 {
			return 0, errCorruptLZS
		}
		return off, nil
	}
	// 11-bit offset, but the all-zero 11-bit value is the end marker.
	off, err := br.readBits(11)
	if err != nil {
		return 0, err
	}
	if off == 0 {
		return lzsEndOffset, nil
	}
	off += 127
	if off > 2047 {
		return 0, errCorruptLZS
	}
	return off, nil
}

const lzsEndOffset = -1

// lzsLength decodes the Elias-style length code that follows an offset token.
// The minimum match length is 2. The prefix code (X3.241-1994) is:
//
//	00       -> 2
//	01       -> 3
//	10       -> 4
//	1100     -> 5
//	1101     -> 6
//	1110     -> 7
//	1111 gggg -> 8 + gggg        (gggg < 1111)
//	1111 1111 ... -> escape: each all-ones 4-bit group adds 15, then 8 + total.
func lzsLength(br *lzsBitReader) (int, error) {
	a, err := br.readBits(2)
	if err != nil {
		return 0, err
	}
	if a < 3 {
		return a + 2, nil
	}
	// a == 3 (bits 11): read two more bits to distinguish 5,6,7 from escape.
	b, err := br.readBits(2)
	if err != nil {
		return 0, err
	}
	if b < 3 {
		return b + 5, nil
	}
	// b == 3 (bits 1111): read 4-bit groups. Each all-ones group (1111) adds
	// 15 and continues; the first non-all-ones group g yields 8 + 15*runs + g.
	length := 8
	for {
		g, err := br.readBits(4)
		if err != nil {
			return length, nil
		}
		if g < 0xF {
			return length + g, nil
		}
		length += 15
	}
}

// put appends a literal byte to out and to the sliding window.
func (d *lzsDecoder) put(out []byte, b byte) []byte {
	d.win[d.pos] = b
	d.pos = (d.pos + 1) % lzsHistorySize
	d.written++
	return append(out, b)
}

// copyMatch copies `length` bytes from `offset` back in the history window,
// handling overlap (run-length) byte by byte.
func (d *lzsDecoder) copyMatch(out []byte, offset, length int) []byte {
	avail := d.written
	if offset > avail {
		offset = avail
	}
	for range length {
		src := (d.pos - offset + lzsHistorySize) % lzsHistorySize
		b := d.win[src]
		out = d.put(out, b)
	}
	return out
}
