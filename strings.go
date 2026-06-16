package mtf

import (
	"strings"
	"unicode/utf16"
)

// decodeString decodes an MTF string located in b at pos with the given byte
// size. It mirrors mtfscan.c::mtfscan_string.
//
// If strType bit 0 is clear the string is stored as UTF-16LE, otherwise it is
// stored as ASCII (MS-DOS CP 646). Embedded NUL code units (except a trailing
// terminator) are replaced with sep, which is how mtftar turns the MTF path
// separator (NUL) into '/'.
func decodeString(b []byte, pos, size int, strType uint8, sep byte) string {
	if size <= 0 || pos < 0 || pos+size > len(b) {
		return ""
	}
	data := b[pos : pos+size]

	var s string
	if strType&1 == 0 {
		u := make([]uint16, 0, len(data)/2+1)
		for i := 0; i+1 < len(data); i += 2 {
			c := uint16(data[i]) | uint16(data[i+1])<<8
			if c == 0 && i+2 < len(data) {
				c = uint16(sep)
			}
			u = append(u, c)
		}
		s = string(utf16.Decode(u))
	} else {
		out := make([]byte, len(data))
		copy(out, data)
		for i := 0; i+1 < len(out); i++ {
			if out[i] == 0 {
				out[i] = sep
			}
		}
		s = string(out)
	}

	// MTF strings are NUL-terminated; cut at the first NUL to match the
	// effective length a C program would see via strlen().
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return s
}

// joinPath joins the non-empty parts of a path with '/' separators.
func joinPath(parts ...string) string {
	const sep = "/"
	out := make([]byte, 0, 64)
	for _, p := range parts {
		if p == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] != sep[0] {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	return string(out)
}
