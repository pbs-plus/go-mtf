package mtf

// spec_lzs_test.go verifies the LZS-221 (MTF_LZS221, 0x0ABE) decompression
// against hand-crafted compressed bitstreams, per ANSI X3.241-1994 as described
// in MTF spec Appendix C. The decoder is exercised directly (independent of the
// compression-frame / Reader machinery) so the token grammar is pinned:
//
//	<String> := 0 <8-bit literal> | 1 <Offset> <Length>
//	<Offset> := 1 <7-bit offset> | 0 <11-bit offset>   (offset 510 == end marker)
//	<EndMarker> := the 9-bit end marker (110000000)

import (
	"testing"
)

// bitStream packs a sequence of bit values (MSB first within each byte) into a
// byte slice, padding the final byte with zero bits.
type bitStream struct {
	buf []byte
	bit int // 0 = MSB .. 7 = LSB
}

func (s *bitStream) write(bit int) {
	if s.bit == 0 {
		s.buf = append(s.buf, 0)
	}
	if bit != 0 {
		s.buf[len(s.buf)-1] |= 1 << (7 - s.bit)
	}
	s.bit = (s.bit + 1) % 8
}

func (s *bitStream) writeBits(v int, n int) {
	for i := n - 1; i >= 0; i-- {
		s.write((v >> i) & 1)
	}
}

// writeLiteral emits a literal byte: 0 flag + 8 data bits.
func (s *bitStream) writeLiteral(b byte) {
	s.write(0)
	s.writeBits(int(b), 8)
}

// writeEndMarker emits the LZS end marker: flag 1, then the 11-bit form
// (prefix 0) with an all-zero 11-bit offset, which the decoder recognises as
// the end sentinel.
func (s *bitStream) writeEndMarker() {
	s.write(1) // flag
	s.write(0) // 11-bit offset prefix
	s.writeBits(0, 11)
}

// TestSpecLZSLiterals verifies a block of pure literals decompresses to the
// original bytes.
func TestSpecLZSLiterals(t *testing.T) {
	want := []byte("hello")
	var s bitStream
	for _, b := range want {
		s.writeLiteral(b)
	}
	s.writeEndMarker()

	d := newLZSDecoder()
	got, err := d.decode(s.buf, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("LZS literals = %q, want %q", got, want)
	}
}

// TestSpecLZSLiteralThenCopy verifies a back-reference copy: emit "ab" as
// literals then copy 2 bytes from offset 2 to produce "abab".
func TestSpecLZSLiteralThenCopy(t *testing.T) {
	var s bitStream
	s.writeLiteral('a')
	s.writeLiteral('b')
	// Match: flag 1. Offset 2 (<=127) is the short form: prefix bit 1 + a 7-bit
	// value of (offset-1) = 1. Length 2 is the Elias code "00".
	s.write(1)
	s.write(1)        // short offset prefix
	s.writeBits(1, 7) // 7-bit value 1 => offset 2
	s.writeLZSLength(2)
	s.writeEndMarker()

	d := newLZSDecoder()
	got, err := d.decode(s.buf, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := "abab"; string(got) != want {
		t.Errorf("LZS copy = %q, want %q", got, want)
	}
}

// TestSpecLZSRun verifies a longer match: emit one literal 'X' then copy 10
// bytes from offset 1 (an RLE-style run) to produce 11 'X's. This exercises
// the 7-bit offset form and the longer Elias length code (8 + gggg).
func TestSpecLZSRun(t *testing.T) {
	var s bitStream
	s.writeLiteral('X')
	// Match from offset 1: flag 1, short prefix 1, 7-bit value 0 (offset 1).
	s.write(1)
	s.write(1)
	s.writeBits(0, 7)    // offset 1
	s.writeLZSLength(10) // length 10 -> 8 + 2
	s.writeEndMarker()

	d := newLZSDecoder()
	got, err := d.decode(s.buf, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "XXXXXXXXXXX" // 1 literal + 10 copied
	if string(got) != want {
		t.Errorf("LZS run = %q, want %q", got, want)
	}
}

// writeLZSLength emits the Elias-style length code that follows an offset
// token, matching the decoder in lzs.go (X3.241-1994):
//
//	00          -> 2
//	01          -> 3
//	10          -> 4
//	1100        -> 5
//	1101        -> 6
//	1110        -> 7
//	1111 gggg   -> 8 + gggg  (gggg 0..14; 1111 is the escape prefix)
func (s *bitStream) writeLZSLength(length int) {
	switch length {
	case 2:
		s.writeBits(0, 2) // 00
	case 3:
		s.writeBits(1, 2) // 01
	case 4:
		s.writeBits(2, 2) // 10
	case 5:
		s.write(1)
		s.write(1)
		s.write(0)
		s.write(0) // 1100
	case 6:
		s.write(1)
		s.write(1)
		s.write(0)
		s.write(1) // 1101
	case 7:
		s.writeBits(7, 3) // 111
		s.write(0)        // 1110
	default:
		if length < 8 || length > 22 {
			panic("writeLZSLength: length out of test range [8,22]")
		}
		// 1111 gggg -> 8 + gggg
		s.writeBits(0xF, 4)
		s.writeBits(length-8, 4)
	}
}
