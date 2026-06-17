package mtf

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeString decodes an MTF string located in b at pos with the given byte
// size.
//
// If strType bit 0 is clear the string is stored as UTF-16LE, otherwise it is
// stored as ASCII (MS-DOS CP 646). Embedded NUL code units (except a trailing
// terminator) are replaced with sep, which turns the MTF path separator (NUL)
// into '/'.
//
// This standalone form allocates working buffers; the Reader hot path uses
// [Reader.decodeStringInto] which reuses pooled buffers. It is retained for the
// cold catalog-parsing path (catalog.go).
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

// decodeStringInto is the zero-allocation-working-buffer form of decodeString
// used by the Reader hot path. It decodes into the Reader's reusable buffers
// (r.strU16 for UTF-16, r.scratchBuf for ASCII/UTF-8) and returns the decoded
// string. The returned string is a fresh allocation (a Go string is immutable
// and must be copied from the working buffer), but the working buffers are not
// reallocated.
func (r *Reader) decodeStringInto(b []byte, pos, size int, strType uint8, sep byte) string {
	if size <= 0 || pos < 0 || pos+size > len(b) {
		return ""
	}
	data := b[pos : pos+size]

	var s string
	if strType&1 == 0 {
		u := r.strU16[:0]
		for i := 0; i+1 < len(data); i += 2 {
			c := uint16(data[i]) | uint16(data[i+1])<<8
			if c == 0 && i+2 < len(data) {
				c = uint16(sep)
			}
			u = append(u, c)
		}
		s = string(utf16.Decode(u))
		r.strU16 = u
	} else {
		out := append(r.scratchBuf[:0], data...)
		for i := 0; i+1 < len(out); i++ {
			if out[i] == 0 {
				out[i] = sep
			}
		}
		s = string(out)
		r.scratchBuf = out
	}

	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	return s
}

// joinPathInto joins the non-empty parts of a path with '/' separators, writing
// into the Reader's reusable byte buffer and returning the string built from it.
// Working storage is reused; only the final string() conversion allocates.
func (r *Reader) joinPathInto(parts ...string) string {
	const sep = "/"
	out := r.strBuf[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] != sep[0] {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	r.strBuf = out
	return string(out)
}

// joinPathDecode builds a path from zero or more string prefix parts followed
// by a raw MTF string field at (sz,po) in r.blk, decoding that field's bytes
// directly into the same reusable buffer. This composes the full entry path in a
// single pass so that only the final string() conversion allocates.
func (r *Reader) joinPathDecode(prefix1, prefix2 string, sz, po uint16) string {
	const sep = "/"
	out := r.strBuf[:0]
	for _, p := range []string{prefix1, prefix2} {
		if p == "" {
			continue
		}
		if len(out) > 0 && out[len(out)-1] != sep[0] {
			out = append(out, sep...)
		}
		out = append(out, p...)
	}
	if sz > 0 {
		if len(out) > 0 && out[len(out)-1] != sep[0] {
			out = append(out, sep...)
		}
		out = appendDecodeString(out, r.blk, int(po), int(sz), r.strType, '/')
	}
	r.strBuf = out
	return string(out)
}

// appendDecodeString decodes an MTF string located in b at pos with the given
// byte size, appending the UTF-8 result to dst. It is the zero-extra-allocation
// building block for composing a string (e.g. joining a path) from several raw
// MTF string fields in one pass: only the single final string() conversion on
// the fully-built buffer allocates. sep replaces embedded (non-terminator) NULs.
func appendDecodeString(dst, b []byte, pos, size int, strType uint8, sep byte) []byte {
	if size <= 0 || pos < 0 || pos+size > len(b) {
		return dst
	}
	data := b[pos : pos+size]
	var buf [utf8.UTFMax]byte
	if strType&1 == 0 {
		// UTF-16LE: decode each code unit (and surrogate pairs) to a rune and
		// append its UTF-8 encoding. A trailing NUL unit terminates; embedded
		// NULs are turned into sep (the MTF path separator -> '/' rule).
		for i := 0; i+1 < len(data); i += 2 {
			c := uint16(data[i]) | uint16(data[i+1])<<8
			if c == 0 && i+2 >= len(data) {
				break // terminator
			}
			if c == 0 {
				c = uint16(sep)
			}
			r := rune(c)
			if utf16.IsSurrogate(r) && i+3 < len(data) {
				c2 := uint16(data[i+2]) | uint16(data[i+3])<<8
				if rr := utf16.DecodeRune(r, rune(c2)); rr != utf8.RuneError {
					r = rr
					i += 2
				}
			}
			n := utf8.EncodeRune(buf[:], r)
			dst = append(dst, buf[:n]...)
		}
	} else {
		// ASCII (CP 646): a trailing NUL terminates; embedded NULs -> sep.
		for i := range data {
			c := data[i]
			if c == 0 && i+1 >= len(data) {
				break
			}
			if c == 0 {
				c = sep
			}
			dst = append(dst, c)
		}
	}
	return dst
}
