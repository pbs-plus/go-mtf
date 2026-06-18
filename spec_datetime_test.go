package mtf

// spec_datetime_test.go verifies the MTF_DATE_TIME (Structure 3) decoding
// against the spec's own worked example (Figure 16) and the bit layout from
// Figure 15, independent of the package's encoder.

import (
	"testing"
	"time"
)

// mtfDateTimeBytes builds the 5-byte MTF_DATE_TIME for the given civil time
// directly from the spec's bit layout (Figure 15): a 40-bit value, MSB-first
// across 5 bytes, with the fields packed as
//
//	YEAR(14)  MONTH(4)  DAY(5)  HOUR(5)  MINUTE(6)  SECOND(6)
//	bits 39..26                 ..17  ..12  ..6  ..0
//
// This deliberately does NOT reuse encodeDateTime, so the test exercises the
// decoder against an independently constructed byte sequence.
func mtfDateTimeBytes(t *testing.T, year, month, day, hour, min, sec int) [5]byte {
	t.Helper()
	y := uint64(year)
	val := (y << 26) |
		(uint64(month) << 22) |
		(uint64(day) << 17) |
		(uint64(hour) << 12) |
		(uint64(min) << 6) |
		uint64(sec)
	var b [5]byte
	b[0] = byte(val >> 32) // bits 39..32
	b[1] = byte(val >> 24) // bits 31..24
	b[2] = byte(val >> 16) // bits 23..16
	b[3] = byte(val >> 8)  // bits 15..8
	b[4] = byte(val)       // bits 7..0
	return b
}

// TestSpecDateTimeFigure16 is the canonical worked example from MTF-100a
// Figure 16: "Date = 12/31/1996, Time = 20:07:30". It builds the bytes from the
// spec's documented bit layout and asserts decodeDateTime recovers exactly that
// instant. If the decoder's bit shifts disagree with the spec, this fails.
func TestSpecDateTimeFigure16(t *testing.T) {
	b := mtfDateTimeBytes(t, 1996, 12, 31, 20, 7, 30)
	want := time.Date(1996, 12, 31, 20, 7, 30, 0, time.Local)
	got := decodeDateTime(b[:], 0)
	if !got.Equal(want) {
		t.Errorf("decodeDateTime(Figure16) = %v, want %v (bytes % x)", got, want, b)
	}
}

// TestSpecDateTimeFields covers each field at its range boundaries, built from
// the spec bit layout, to guarantee every field decodes from the correct bits.
func TestSpecDateTimeFields(t *testing.T) {
	cases := []struct {
		y, mo, d, h, mi, s int
	}{
		{2000, 1, 1, 0, 0, 0},     // epoch-ish
		{1980, 2, 28, 23, 59, 59}, // end of day / leap-ish
		{2024, 6, 7, 22, 17, 15},  // golden-archive media date (UTC)
		{2099, 12, 31, 12, 30, 45},
		{16383, 12, 31, 23, 59, 59}, // max 14-bit year
	}
	for _, c := range cases {
		b := mtfDateTimeBytes(t, c.y, c.mo, c.d, c.h, c.mi, c.s)
		want := time.Date(c.y, time.Month(c.mo), c.d, c.h, c.mi, c.s, 0, time.Local)
		got := decodeDateTime(b[:], 0)
		if !got.Equal(want) {
			t.Errorf("decodeDateTime(%04d-%02d-%02d %02d:%02d:%02d) = %v, want %v (bytes % x)",
				c.y, c.mo, c.d, c.h, c.mi, c.s, got, want, b)
		}
	}
}

// TestSpecDateTimeAllZeroIsUnknown verifies the spec rule: "An unknown or
// undefined date and time is represented by using zero for all five bytes."
func TestSpecDateTimeAllZeroIsUnknown(t *testing.T) {
	got := decodeDateTime([]byte{0, 0, 0, 0, 0}, 0)
	if !got.IsZero() {
		t.Errorf("decodeDateTime(all-zero) = %v, want the zero time (spec: zero = unknown)", got)
	}
}

// TestSpecDateTimeEncodeDecodeRoundTrip confirms the package's own encoder
// agrees with the spec-derived builder for a range of values (so encodeDateTime
// is a faithful inverse and safe to use in fixtures).
func TestSpecDateTimeEncodeDecodeRoundTrip(t *testing.T) {
	cases := []time.Time{
		time.Date(1996, 12, 31, 20, 7, 30, 0, time.Local),
		time.Date(2024, 6, 7, 22, 17, 15, 0, time.Local),
		time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local),
	}
	for _, want := range cases {
		spec := mtfDateTimeBytes(t, want.Year(), int(want.Month()), want.Day(),
			want.Hour(), want.Minute(), want.Second())
		enc := encodeDateTime(want)
		if spec != enc {
			t.Errorf("encodeDateTime(%v) = % x, spec layout = % x", want, enc, spec)
		}
		got := decodeDateTime(enc[:], 0)
		if !got.Equal(want) {
			t.Errorf("round trip %v -> %v", want, got)
		}
	}
}
