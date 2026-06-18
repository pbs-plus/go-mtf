package mtf

// spec_csum_test.go verifies the MTF data-stream checksum (spec Figure 19 /
// section 6.2.1.4): a 32-bit XOR of the linear stream data that is independent
// of how the data is segmented.

import (
	"encoding/binary"
	"testing"
)

// naiveChecksum32 computes the 32-bit XOR over 4-byte words the obvious way,
// with a separate (final-word) handling for the tail. It is a deliberately
// independent reference for Checksum32.
func naiveChecksum32(b []byte) uint32 {
	var sum uint32
	full := len(b) / 4
	for i := range full {
		sum ^= binary.LittleEndian.Uint32(b[i*4 : i*4+4])
	}
	rem := b[full*4:]
	var tail uint32
	for k := range rem {
		tail |= uint32(rem[k]) << (8 * uint(k))
	}
	return sum ^ tail
}

// TestSpecChecksum32MatchesReference verifies Checksum32 against an independent
// naive word-XOR implementation.
func TestSpecChecksum32MatchesReference(t *testing.T) {
	cases := [][]byte{
		nil,
		{0x01},
		{0x01, 0x02},
		{0x01, 0x02, 0x03},
		{0x01, 0x02, 0x03, 0x04},
		[]byte("the quick brown fox jumps over the lazy dog"),
		bytes0To(257), // spans many 4-byte words + a tail
	}
	for _, c := range cases {
		if got, want := Checksum32(c), naiveChecksum32(c); got != want {
			t.Errorf("Checksum32(% x) = %#x, want %#x", c, got, want)
		}
	}
}

// TestSpecChecksum32SegmentationIndependent verifies the spec's defining
// property (Figure 19): the checksum is identical regardless of segmentation.
// The segmentation-independent formulation is that each byte b[i] is XORed
// into the 32-bit sum at bit position (i % 4) * 8. Checksum32 (word-wise XOR
// with the tail in the low bytes) must equal that byte-wise reference.
func TestSpecChecksum32SegmentationIndependent(t *testing.T) {
	data := []byte("Segmentation must not change the MTF checksum!.")
	var byteWise uint32
	for i := range data {
		byteWise ^= uint32(data[i]) << (8 * uint(i%4))
	}
	if got := Checksum32(data); got != byteWise {
		t.Errorf("Checksum32 = %#x, want byte-wise %#x (spec Figure 19 independence)", got, byteWise)
	}
	// The same identity must hold for any sub-slice (a writer that began at an
	// arbitrary stream offset still aligns bytes to the 4-byte phase).
	for start := 0; start <= 3 && start < len(data); start++ {
		sub := data[start:]
		var ref uint32
		for i := range sub {
			ref ^= uint32(sub[i]) << (8 * uint(i%4))
		}
		if got := Checksum32(sub); got != ref {
			t.Errorf("Checksum32(offset=%d) = %#x, want %#x", start, got, ref)
		}
	}
}

// TestSpecChecksum32KnownValue verifies a hand-computed value for a 4-byte
// input so the bit order (little-endian words) is pinned.
func TestSpecChecksum32KnownValue(t *testing.T) {
	b := []byte{0x11, 0x22, 0x33, 0x44} // single LE word 0x44332211
	if got := Checksum32(b); got != 0x44332211 {
		t.Errorf("Checksum32 = %#x, want 0x44332211 (single LE word, spec Figure 19)", got)
	}
	// Two words XOR together.
	b2 := []byte{0x11, 0x22, 0x33, 0x44, 0xFF, 0x00, 0xFF, 0x00}
	if got := Checksum32(b2); got != 0x44332211^0x00FF00FF {
		t.Errorf("Checksum32(2 words) = %#x, want %#x", got, 0x44332211^0x00FF00FF)
	}
}

// bytes0To returns a slice [0, 1, 2, ..., n-1] (mod 256).
func bytes0To(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}
