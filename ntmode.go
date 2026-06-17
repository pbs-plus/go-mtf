package mtf

import "strings"

// UnixMode returns a best-effort Unix permission mode derived from the Windows
// file attributes stored in [Header.Attributes]. The mapping follows common
// conventions:
//
//   - Directories get mode 0755 (readable/traversable by all, writable by owner).
//   - Regular files get mode 0644 (readable by all, writable by owner), or 0755
//     if the archive bit is set (a heuristic for executables on Unix-origin backups).
//   - The readonly bit clears the owner-write bit.
//   - The system bit is not mapped (Unix has no equivalent).
//
// For precise permissions, parse [Header.SecurityDescriptor] and extract the
// DACL. The mode returned here is a reasonable default for migration tools that
// need a starting point.
func (h *Header) UnixMode() uint32 {
	switch h.Type {
	case EntryDirectory:
		m := uint32(0o755)
		if h.Attributes&WinAttrReadOnly != 0 {
			m &^= 0o200 // clear owner-write
		}
		return m
	case EntryFile:
		m := uint32(0o644)
		if h.Attributes&WinAttrArchive != 0 {
			m = 0o755 // heuristic: archive bit → executable
		}
		if h.Attributes&WinAttrReadOnly != 0 {
			m &^= 0o200
		}
		return m
	default:
		return 0o644
	}
}

// OwnerSID returns the owner SID from the security descriptor stored in
// [Header.SecurityDescriptor], or nil if no descriptor is present or it is too
// short to contain an owner. The SID is returned in raw binary form
// (self-relative SECURITY_DESCRIPTOR format).
//
// To convert a raw SID to string form (S-1-5-21-...), use [FormatSID].
func (h *Header) OwnerSID() []byte {
	return extractOwnerSID(h.SecurityDescriptor)
}

// GroupSID returns the group SID from the security descriptor stored in
// [Header.SecurityDescriptor], or nil if no descriptor is present or it is too
// short to contain a group.
func (h *Header) GroupSID() []byte {
	return extractGroupSID(h.SecurityDescriptor)
}

// FormatSID converts a raw binary SID to its string representation
// (e.g. "S-1-5-21-..."). It returns an empty string if the SID is invalid.
func FormatSID(sid []byte) string {
	if len(sid) < 8 {
		return ""
	}
	revision := sid[0]
	subAuthCount := sid[1]
	if len(sid) < 8+int(subAuthCount)*4 {
		return ""
	}
	// Authority is 6 bytes big-endian
	var authority uint64
	for i := range 6 {
		authority = (authority << 8) | uint64(sid[2+i])
	}
	var result strings.Builder
	result.WriteString("S-1")
	// The revision field gives the "-1-" part
	result.WriteString("-")
	// Authority
	_ = revision
	result.WriteString(formatAuth(authority))
	for i := 0; i < int(subAuthCount); i++ {
		off := 8 + i*4
		sub := uint32(sid[off]) | uint32(sid[off+1])<<8 | uint32(sid[off+2])<<16 | uint32(sid[off+3])<<24
		result.WriteString("-" + formatSub(sub))
	}
	return result.String()
}

func formatAuth(a uint64) string {
	if a == 0 {
		return "0"
	}
	return uint64Str(a)
}

func formatSub(v uint32) string {
	return uint32Str(v)
}

func uint64Str(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func uint32Str(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// extractOwnerSID extracts the owner SID from a self-relative SECURITY_DESCRIPTOR.
// Layout: revision(1), pad(1), control(2), ownerOffset(4), groupOffset(4), ...
func extractOwnerSID(sd []byte) []byte {
	if len(sd) < 12 {
		return nil
	}
	ownerOff := uint32(sd[4]) | uint32(sd[5])<<8 | uint32(sd[6])<<16 | uint32(sd[7])<<24
	if ownerOff == 0 || int(ownerOff) >= len(sd) {
		return nil
	}
	return extractSID(sd[ownerOff:])
}

// extractGroupSID extracts the group SID from a self-relative SECURITY_DESCRIPTOR.
func extractGroupSID(sd []byte) []byte {
	if len(sd) < 12 {
		return nil
	}
	groupOff := uint32(sd[8]) | uint32(sd[9])<<8 | uint32(sd[10])<<16 | uint32(sd[11])<<24
	if groupOff == 0 || int(groupOff) >= len(sd) {
		return nil
	}
	return extractSID(sd[groupOff:])
}

// extractSID returns the raw bytes of one SID starting at p. The SID layout is:
// revision(1), subAuthCount(1), authority(6), subAuths(4 each).
func extractSID(p []byte) []byte {
	if len(p) < 8 {
		return nil
	}
	subCount := int(p[1])
	end := 8 + subCount*4
	if end > len(p) {
		return nil
	}
	return p[:end]
}
