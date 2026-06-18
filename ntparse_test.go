package mtf

import "testing"

func TestFormatSID(t *testing.T) {
	// S-1-5-18 (LOCAL_SYSTEM)
	sid := []byte{1, 1, 0, 0, 0, 0, 0, 5, 18, 0, 0, 0}
	got := FormatSID(sid)
	want := "S-1-5-18"
	if got != want {
		t.Errorf("FormatSID = %q, want %q", got, want)
	}

	// S-1-5-21-... (domain SID)
	sid2 := []byte{1, 5, 0, 0, 0, 0, 0, 5, 21, 0, 0, 0, 0x34, 0x12, 0, 0, 0xAB, 0xCD, 0, 0, 0xEF, 0x01, 0, 0, 0x34, 0x12, 0, 0}
	got2 := FormatSID(sid2)
	if got2 == "" {
		t.Error("FormatSID returned empty for domain SID")
	}
	if got2[:6] != "S-1-5-" {
		t.Errorf("FormatSID = %q, want S-1-5-...", got2)
	}

	// Too short
	got3 := FormatSID([]byte{1, 1, 0})
	if got3 != "" {
		t.Errorf("FormatSID(short) = %q, want empty", got3)
	}
}

func TestUnixMode(t *testing.T) {
	tests := []struct {
		name string
		typ  EntryType
		attr uint32
		want uint32
	}{
		{"dir default", EntryDirectory, 0, 0o755},
		{"dir readonly", EntryDirectory, MTFAttrReadOnly, 0o555},
		{"file default", EntryFile, 0, 0o644},
		{"file modified", EntryFile, MTFAttrModified, 0o755},
		{"file readonly", EntryFile, MTFAttrReadOnly, 0o444},
		{"file modified+readonly", EntryFile, MTFAttrModified | MTFAttrReadOnly, 0o555},
	}
	for _, tt := range tests {
		h := &Header{Type: tt.typ, Attributes: tt.attr}
		got := h.UnixMode()
		if got != tt.want {
			t.Errorf("UnixMode(%s attr=0x%08X) = 0o%o, want 0o%o", tt.name, tt.attr, got, tt.want)
		}
	}
}

func TestParseReparsePoint(t *testing.T) {
	// IO_REPARSE_TAG_SYMLINK with a target path "C:\Users"
	h := &Header{}

	// Build a REPARSE_DATA_BUFFER for a symlink:
	// ReparseTag = 0xA000000C
	// ReparseDataLength = ...
	// SubstituteNameOffset = 0
	// SubstituteNameLength = len(path in UTF-16LE)
	// PrintNameOffset, PrintNameLength = 0
	// Flags = 0 (absolute)
	// PathBuffer = UTF-16LE path
	target := "C:\\Users"
	targetUTF16 := utf16LE(target)
	pathBufLen := len(targetUTF16)
	data := make([]byte, 20+pathBufLen)
	putU32LE(data[0:], 0xA000000C)           // ReparseTag
	putU16LE(data[4:], uint16(pathBufLen+4)) // ReparseDataLength (approx)
	putU16LE(data[6:], 0)                    // Reserved
	putU16LE(data[8:], 0)                    // SubstituteNameOffset
	putU16LE(data[10:], uint16(pathBufLen))  // SubstituteNameLength
	putU16LE(data[12:], uint16(pathBufLen))  // PrintNameOffset
	putU16LE(data[14:], 0)                   // PrintNameLength
	putU32LE(data[16:], 0)                   // Flags (absolute)
	copy(data[20:], targetUTF16)

	parseReparsePoint(h, data)
	if !h.IsSymlink {
		t.Error("expected IsSymlink = true")
	}
	if h.LinkTarget != target {
		t.Errorf("LinkTarget = %q, want %q", h.LinkTarget, target)
	}
}

func TestParseLinkStream(t *testing.T) {
	h := &Header{}
	target := "C:\\Users\\admin\\file.txt"
	linkData := utf16LE(target)

	parseLinkStream(h, linkData)
	if !h.IsHardLink {
		t.Error("expected IsHardLink = true")
	}
	if h.LinkTarget != target {
		t.Errorf("LinkTarget = %q, want %q", h.LinkTarget, target)
	}
}

func utf16LE(s string) []byte {
	u16s := make([]uint16, len(s)+1) // +1 for NUL
	for i, r := range s {
		u16s[i] = uint16(r)
	}
	result := make([]byte, len(u16s)*2)
	for i, v := range u16s {
		result[i*2] = byte(v)
		result[i*2+1] = byte(v >> 8)
	}
	return result
}

func putU32LE(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putU16LE(b []byte, v uint16) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
}
