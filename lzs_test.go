package mtf

import (
	"bytes"
	"math/rand"
	"testing"
)

// lzsCompress is a minimal, correct-by-construction LZS encoder used only by
// tests to produce decoder ground truth. It emits literals and back-references
// into a 2 KB window with a trailing end marker. It is not optimised.
type lzsWriter struct {
	bits []int
}

func (w *lzsWriter) writeBits(v, n int) {
	for i := n - 1; i >= 0; i-- {
		w.bits = append(w.bits, (v>>i)&1)
	}
}

func (w *lzsWriter) literal(b byte) {
	w.bits = append(w.bits, 0)
	w.writeBits(int(b), 8)
}

func (w *lzsWriter) match(offset, length int) {
	w.bits = append(w.bits, 1) // match flag
	if offset <= 127 {
		w.bits = append(w.bits, 1)
		w.writeBits(offset-1, 7)
	} else {
		w.bits = append(w.bits, 0)
		w.writeBits(offset-127, 11)
	}
	w.writeLength(length)
}

func (w *lzsWriter) writeLength(length int) {
	switch {
	case length == 2:
		w.writeBits(0b00, 2)
	case length == 3:
		w.writeBits(0b01, 2)
	case length == 4:
		w.writeBits(0b10, 2)
	case length <= 7:
		w.writeBits(0b11, 2)
		w.writeBits(length-5, 2)
	default:
		w.writeBits(0b11, 2)
		w.writeBits(0b11, 2)
		length -= 8
		for length >= 15 {
			w.writeBits(0b1111, 4)
			length -= 15
		}
		w.writeBits(length, 4)
	}
}

func (w *lzsWriter) endMarker() {
	w.bits = append(w.bits, 1, 0) // offset form 0 + 11 zero bits = end
	w.writeBits(0, 11)
}

func (w *lzsWriter) bytes() []byte {
	out := make([]byte, (len(w.bits)+7)/8)
	for i, b := range w.bits {
		out[i/8] |= byte(b) << (7 - i%8)
	}
	return out
}

// lzsCompress greedily encodes src into the LZS bitstream. Match search is O(n*w)
// with a 2 KB window and 2..2048 byte lengths.
func lzsCompress(src []byte) []byte {
	w := &lzsWriter{}
	const win = 2048
	const minMatch = 2
	i := 0
	for i < len(src) {
		bestLen, bestOff := 0, 0
		start := max(i-(win-1), 0) // keep max offset <= 2047 (11-bit max)
		for j := start; j < i; j++ {
			l := 0
			for i+l < len(src) && src[j+l] == src[i+l] {
				l++
			}
			if l > bestLen {
				bestLen, bestOff = l, i-j
			}
		}
		if bestLen >= minMatch {
			w.match(bestOff, bestLen)
			i += bestLen
		} else {
			w.literal(src[i])
			i++
		}
	}
	w.endMarker()
	return w.bytes()
}

// TestLZSRoundTrip compresses random and structured inputs with a minimal LZS
// compressor and verifies the decoder reproduces them exactly. Because MTF
// compression is absent from the production dataset, this self-contained
// round-trip is the ground truth for the LZS decoder.
func TestLZSRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"single", []byte{0x41}},
		{"literal-short", []byte("hello")},
		{"no-match", bytes.Repeat([]byte{0xFF, 0x00, 0x55, 0xAA}, 50)},
		{"rle", bytes.Repeat([]byte("A"), 5000)},
		{"highly-repetitive", []byte("ABCDEFGHABCDEFGHABCDEFGHABCDEFGH")},
		{"mixed", []byte("The quick brown fox. The quick brown fox. The quick brown fox jumps.")},
	}
	// Deterministic pseudo-random cases.
	rng := rand.New(rand.NewSource(1))
	for range 8 {
		n := rng.Intn(4000) + 1
		data := make([]byte, n)
		// Bias toward a small alphabet so back-references appear.
		alpha := "ABCDE"
		for j := range data {
			data[j] = alpha[rng.Intn(len(alpha))]
		}
		cases = append(cases, struct {
			name string
			data []byte
		}{name: "rand-smallalpha", data: data})
	}
	for range 4 {
		n := rng.Intn(4000) + 1
		data := make([]byte, n)
		rng.Read(data) // full-range bytes: mostly literals
		cases = append(cases, struct {
			name string
			data []byte
		}{name: "rand-fullrange", data: data})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			compressed := lzsCompress(tc.data)
			dec := newLZSDecoder()
			out, err := dec.decode(compressed, nil)
			if err != nil {
				t.Fatalf("decode error: %v", err)
			}
			if !bytes.Equal(out, tc.data) {
				t.Errorf("round-trip mismatch: got %d bytes, want %d", len(out), len(tc.data))
			}
		})
	}
}

// TestLZSKnownVector verifies the decoder against a hand-built compressed
// stream, pinning the literal and match token semantics independently of the
// test compressor.
func TestLZSKnownVector(t *testing.T) {
	// One literal 'A' (0 01000001), one end marker (1 0 000000000).
	// Bits: 0_01000001 1_000000000
	// = 0010000011 000000000  -> pad to bytes:
	//   0010 0000 | 1100 0000 | 00xx xxxx  -> but let's build via bits helper.
	var bits []int
	addBits := func(v int, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1)
		}
	}
	// literal 'A' = 0x41
	bits = append(bits, 0)
	addBits(0x41, 8)
	// end marker: flag 1, then 0 + 11 zero bits = 1 0 000000000
	bits = append(bits, 1, 0)
	addBits(0, 11)
	// pack to bytes
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		out[i/8] |= byte(b) << (7 - i%8)
	}
	dec := newLZSDecoder()
	got, err := dec.decode(out, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(got, []byte{'A'}) {
		t.Errorf("got %q, want \"A\"", got)
	}
}

// TestLZSMatchOverlap verifies the run-length (overlap) path: a match whose
// offset is 1 and length N reproduces N copies of the preceding byte.
func TestLZSMatchOverlap(t *testing.T) {
	// Build: literal 'X', then a match offset=1 length=4 -> "XXXXX".
	var bits []int
	addBits := func(v int, n int) {
		for i := n - 1; i >= 0; i-- {
			bits = append(bits, (v>>i)&1)
		}
	}
	bits = append(bits, 0) // literal flag
	addBits('X', 8)        // literal 'X'
	// match: flag 1, offset=1 (7-bit form: 1 + 0000000), length=4 (bits 10)
	bits = append(bits, 1) // match flag
	// offset 1: first=1 (7-bit), then 0000000
	bits = append(bits, 1)
	addBits(0, 7)
	// length 4: code "10"
	addBits(0b10, 2)
	// end marker: 1 0 000000000
	bits = append(bits, 1, 0)
	addBits(0, 11)
	out := make([]byte, (len(bits)+7)/8)
	for i, b := range bits {
		out[i/8] |= byte(b) << (7 - i%8)
	}
	dec := newLZSDecoder()
	got, err := dec.decode(out, nil)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if want := []byte("XXXXX"); !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
