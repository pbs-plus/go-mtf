package mtf

import "time"

// decodeDateTime decodes an MTF date/time field. The field is a packed 5-byte
// value (within a 6-byte region) laid out as in mtfheader.c::mtf_header_datetime.
//
// The original backup times are stored in the local time of the machine that
// produced the archive, so the result is returned in the local time zone
// (matching the mktime() behaviour of mtftar).
func decodeDateTime(b []byte, off int) time.Time {
	if off+5 > len(b) {
		return time.Time{}
	}
	x0, x1, x2, x3, x4 := b[off], b[off+1], b[off+2], b[off+3], b[off+4]

	year := int(uint16(x0)<<6 | uint16(x1)>>2)
	month := int((x1&0x03)<<2 | x2>>6)
	day := int((x2 & 0x3E) >> 1)
	hour := int((x2&0x01)<<4 | x3>>4)
	minute := int((x3&0x0F)<<2 | x4>>6)
	second := int(x4 & 0x3F)

	if month < 1 || month > 12 || day < 1 || day > 31 {
		// Not a valid date; fall back so time.Date does not normalise garbage.
		return time.Time{}
	}
	return time.Date(year, time.Month(month), day, hour, minute, second, 0, time.Local)
}
