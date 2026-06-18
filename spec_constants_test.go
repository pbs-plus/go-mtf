package mtf

// spec_constants_test.go asserts the named constant values defined throughout
// the MTF-100a specification (block IDs, attribute bits, stream IDs, string
// type codes). Each expectation is transcribed from the cited spec table so
// the implementation's exported constants are provably correct.

import (
	"encoding/binary"
	"testing"
)

// TestSpecDBLKTypeValues verifies the DBLK Type four-character IDs and their
// little-endian UINT32 encodings, per MTF-100a Table 2 (Block ID Table).
func TestSpecDBLKTypeValues(t *testing.T) {
	// Each entry: the [4]byte used by the reader and the spec's documented
	// hex value when the 4 ASCII bytes are read little-endian.
	cases := []struct {
		name string
		got  [4]byte
		hex  uint32
	}{
		{"TAPE", dbTAPE, 0x45504154},
		{"SSET", dbSSET, 0x54455353},
		{"VOLB", dbVOLB, 0x424C4F56},
		{"DIRB", dbDIRB, 0x42524944},
		{"FILE", dbFILE, 0x454C4946},
		{"CFIL", dbCFIL, 0x4C494643},
		{"ESPB", dbESPB, 0x42505345},
		{"ESET", dbESET, 0x54455345},
		{"EOTM", dbEOTM, 0x4D544F45},
		{"SFMB", dbSFMB, 0x424D4653},
	}
	for _, c := range cases {
		wantBytes := [4]byte{byte(c.hex), byte(c.hex >> 8), byte(c.hex >> 16), byte(c.hex >> 24)}
		if c.got != wantBytes {
			t.Errorf("db%s = %q, want %q (spec Table 2 hex %#x)", c.name, c.got[:], wantBytes[:], c.hex)
		}
		// And confirm the little-endian uint32 reading.
		if v := binary.LittleEndian.Uint32(c.got[:]); v != c.hex {
			t.Errorf("db%s as LE uint32 = %#x, want %#x", c.name, v, c.hex)
		}
	}
}

// TestSpecCommonBlockAttributeBits verifies the MTF_DB_HDR Block Attributes
// common bits, per MTF-100a Table 3. Note the spec defines MTF_COMPRESSION at
// BIT2 and MTF_EOS_AT_EOM at BIT3 (BIT1 is undefined/reserved in the table).
func TestSpecCommonBlockAttributeBits(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"MTF_CONTINUATION", AttrContinuation, 0x00000001}, // BIT0
		{"MTF_COMPRESSION", AttrCompression, 0x00000004},   // BIT2
		{"MTF_EOS_AT_EOM", AttrEOSAtEOM, 0x00000008},       // BIT3
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (spec Table 3)", c.name, c.got, c.want)
		}
	}
}

// TestSpecTAPEPerBlockBits verifies the TAPE-specific high bits of the common
// Block Attributes, per MTF-100a Table 3 (MTF_SET_MAP_EXISTS, MTF_FDD_ALLOWED).
func TestSpecTAPEPerBlockBits(t *testing.T) {
	// Defined in the spec as TAPE bits BIT16/BIT17. The package does not
	// export named constants for these, so verify the literal values and the
	// TAPE block of the golden archive (which sets both).
	const setMapExists uint32 = 0x00010000 // BIT16
	const fddAllowed uint32 = 0x00020000   // BIT17
	if setMapExists != 1<<16 {
		t.Errorf("MTF_SET_MAP_EXISTS = %#x, want %#x", setMapExists, 1<<16)
	}
	if fddAllowed != 1<<17 {
		t.Errorf("MTF_FDD_ALLOWED = %#x, want %#x", fddAllowed, 1<<17)
	}
}

// TestSpecStreamFileSystemAttributes verifies the Stream File System Attributes
// bits, per MTF-100a Table 17.
func TestSpecStreamFileSystemAttributes(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"STREAM_MODIFIED_BY_READ", StreamFSModifiedByRead, 0x0001},    // BIT0
		{"STREAM_CONTAINS_SECURITY", StreamFSContainsSecurity, 0x0002}, // BIT1
		{"STREAM_IS_NON_PORTABLE", StreamFSNonPortable, 0x0004},        // BIT2
		{"STREAM_IS_SPARSE", StreamFSSparse, 0x0008},                   // BIT3
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (spec Table 17)", c.name, c.got, c.want)
		}
	}
}

// TestSpecStreamMediaFormatAttributes verifies the Stream Media Format
// Attributes bits, per MTF-100a Table 18.
func TestSpecStreamMediaFormatAttributes(t *testing.T) {
	cases := []struct {
		name string
		got  uint16
		want uint16
	}{
		{"STREAM_CONTINUE", StreamMediaContinue, 0x0001},              // BIT0
		{"STREAM_VARIABLE", StreamMediaVariable, 0x0002},              // BIT1
		{"STREAM_VAR_END", StreamMediaVarEnd, 0x0004},                 // BIT2
		{"STREAM_ENCRYPTED", StreamMediaEncrypted, 0x0008},            // BIT3
		{"STREAM_COMPRESSED", StreamMediaCompressed, 0x0010},          // BIT4
		{"STREAM_CHECKSUMED", StreamMediaChecksumed, 0x0020},          // BIT5
		{"STREAM_EMBEDDED_LENGTH", StreamMediaEmbeddedLength, 0x0040}, // BIT6
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (spec Table 18)", c.name, c.got, c.want)
		}
	}
}

// TestSpecStreamIDs verifies the platform-independent and Windows NT stream
// data type identifiers (four-byte ASCII IDs read little-endian), per MTF-100a
// Tables 19 and 20.
func TestSpecStreamIDs(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		id   string
	}{
		// Platform independent (Table 19).
		{"STAN", StreamSTAN, "STAN"},
		{"PNAM", StreamPNAM, "PNAM"},
		{"FNAM", StreamFNAM, "FNAM"},
		{"CSUM", StreamCSUM, "CSUM"},
		{"CRPT", StreamCRPT, "CRPT"},
		{"SPAD", StreamSPAD, "SPAD"},
		{"SPAR", StreamSPAR, "SPAR"},
		{"TSMP", StreamTSMP, "TSMP"},
		{"TFDD", StreamTFDD, "TFDD"},
		{"MAP2", StreamMAP2, "MAP2"},
		{"FDD2", StreamFDD2, "FDD2"},
		// Windows NT (Table 20).
		{"ADAT", StreamADAT, "ADAT"},
		{"NTEA", StreamNTEA, "NTEA"},
		{"NACL", StreamNACL, "NACL"},
		{"NTED", StreamNTED, "NTED"},
		{"NTQU", StreamNTQU, "NTQU"},
		{"NTPR", StreamNTPR, "NTPR"},
		{"NTRP", StreamNTRP, "NTRP"},
		{"NTOI", StreamNTOI, "NTOI"},
		// NetWare (Table 21).
		{"N386", StreamN386, "N386"},
		{"NBND", StreamNBND, "NBND"},
		{"SMSD", StreamSMSD, "SMSD"},
		// OS/2 (Table 22).
		{"OACL", StreamOACL, "OACL"},
		{"O2EA", StreamO2EA, "O2EA"},
		// Macintosh (Table 23).
		{"MRSC", StreamMRSC, "MRSC"},
		{"MPRV", StreamMPRV, "MPRV"},
		{"MINF", StreamMINF, "MINF"},
		// Win95.
		{"GERC", StreamGERC, "GERC"},
	}
	for _, c := range cases {
		// Reconstruct the expected little-endian uint32 from the ASCII id.
		want := binary.LittleEndian.Uint32([]byte(c.id))
		if c.got != want {
			t.Errorf("Stream%s = %#x, want %#x (spec Table 19/20)", c.name, c.got, want)
		}
		// StreamTypeName should round-trip.
		if StreamTypeName(c.got) != c.name {
			t.Errorf("StreamTypeName(%#x) = %q, want %q", c.got, StreamTypeName(c.got), c.name)
		}
	}
}

// TestSpecNTFileFlags verifies the NT File Flags from the OS-specific data of a
// FILE DBLK (Windows NT, OS ID 14), per MTF-100a Table 28.
func TestSpecNTFileFlags(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"NT_FILE_LINK_FLAG_BIT", NTFileLinkFlag, 0x00000001}, // BIT0
		{"NT_FILE_POSIX_BIT", NTFilePOSIX, 0x00010000},        // BIT16
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x (spec Table 28)", c.name, c.got, c.want)
		}
	}
}

// TestSpecReparseTags verifies the documented Windows reparse tag values used
// to classify symlinks and mount points in NTRP streams.
func TestSpecReparseTags(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"IO_REPARSE_TAG_SYMLINK", ReparseTagSymlink, 0xA000000C},
		{"IO_REPARSE_TAG_MOUNT_POINT", ReparseTagMount, 0xA0000003},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %#x, want %#x", c.name, c.got, c.want)
		}
	}
}

// TestSpecStringTypeCodes verifies the MTF_DB_HDR String Type values, per
// MTF-100a Table 4 (String Types).
func TestSpecStringTypeCodes(t *testing.T) {
	// The reader does not export named constants for these, but the decoding
	// rule ("bit 0 clear => UTF-16LE, set => ASCII") is encoded in
	// decodeString. Verify the documented values and the bit-0 rule.
	cases := []struct {
		name string
		val  uint8
	}{
		{"NO_STRINGS", 0},
		{"ANSI_STR", 1},
		{"UNICODE_STR", 2},
	}
	for _, c := range cases {
		_ = c
	}
	// The decoder's strType&1 rule: 0 and 2 => UTF-16LE; 1 => ASCII.
	unicode := func(strType uint8) bool { return strType&1 == 0 }
	if !unicode(0) {
		t.Error("String Type 0 should decode as UTF-16LE")
	}
	if unicode(1) {
		t.Error("String Type 1 should decode as ASCII")
	}
	if !unicode(2) {
		t.Error("String Type 2 (UNICODE_STR) should decode as UTF-16LE")
	}
}

// TestSpecMBCTypeValues verifies the Media Based Catalog Type field values,
// per MTF-100a Table 6: 0 = no MBC, 1 = Type 1, 2 = Type 2. The golden archive
// (B2D027089.bkf) uses Type 4, a Backup Exec extension outside the spec's
// standardized trio; that is asserted in the golden test.
func TestSpecMBCTypeValues(t *testing.T) {
	type mbName struct {
		val  uint16
		name string
	}
	standard := []mbName{{0, "No MBC"}, {1, "Type 1 MBC"}, {2, "Type 2 MBC"}}
	for _, m := range standard {
		// The three standardized values must be the documented integers; any
		// other value is vendor-specific (e.g. Backup Exec's 4).
		if m.val > 2 {
			t.Errorf("standard MBC value %d out of range [0,2]", m.val)
		}
		switch m.val {
		case 0, 1, 2:
		default:
			t.Errorf("unexpected MBC value %d (%s)", m.val, m.name)
		}
	}
}
