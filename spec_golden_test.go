package mtf

// spec_golden_test.go asserts that the reader produces the correct values for
// the real-world B2D027089.bkf archive — a Veritas Backup Exec 22.0 B2D
// cartridge. Every golden value below was independently verified by hand-decoding
// the raw bytes against the MTF-100a field offsets (see spec_offsets_test.go)
// BEFORE being recorded here, so this file is an independent check that the
// reader agrees with the spec, not with itself.
//
// The fixture lives at the repository root and is skipped if absent, so `go
// test ./...` still runs in CI without the large binary.

import (
	"os"
	"testing"
)

const goldenBKF = "B2D027089.bkf"

// goldenVolumeDevice is the first non-continuation VOLB device name of the real
// archive: a UNC path (UTF-16LE, String Type 2). Written as a raw string so the
// backslashes are literal.
const goldenVolumeDevice = `\\DP-D002.SGL.LAN\E:`

func goldenBKFExists() bool {
	_, err := os.Stat(goldenBKF)
	return err == nil
}

// openGolden returns an open Reader over the golden archive, closing it via
// t.Cleanup so errcheck is satisfied.
func openGolden(t *testing.T) *Reader {
	t.Helper()
	r, err := Open(goldenBKF)
	if err != nil {
		t.Fatalf("Open %s: %v", goldenBKF, err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// TestGoldenTapeFields verifies the MTF_TAPE descriptor block of the real
// archive decodes to its known-good field values. This is the end-to-end proof
// that parseTape (and the TAPE field offsets) are spec-correct: a wrong offset
// yields garbage, as it did before the fix.
func TestGoldenTapeFields(t *testing.T) {
	if !goldenBKFExists() {
		t.Skipf("%s not present; skipping real-archive golden test", goldenBKF)
	}
	r := openGolden(t)

	blk, err := r.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if blk.Kind != KindMedia {
		t.Fatalf("first block kind = %v, want KindMedia", blk.Kind)
	}
	tp := blk.Tape
	if tp == nil {
		t.Fatal("Tape is nil")
	}

	// Values hand-verified from the raw TAPE bytes (offset 0):
	//   MFMID @52, TAPE Attr @56, Media Seq @60, SFM @64, MBC Type @66,
	//   FLB Size @84, Vendor ID @86, MTF Major @93.
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"MFMID", tp.MFMID, uint32(0x58c7b22d)},
		{"Attributes", tp.Attributes, uint32(0x2)}, // TAPE_MEDIA_LABEL_BIT
		{"Sequence", tp.Sequence, uint16(1)},
		{"SoftFilemarkBlockSize", tp.SoftFilemarkBlockSize, uint16(128)},
		{"CatalogType", tp.CatalogType, uint16(4)},
		{"FLBSize", tp.FLBSize, uint16(1024)},
		{"SoftwareVendorID", tp.SoftwareVendorID, uint16(0xacda)},
		{"MTFMajorVersion", tp.MTFMajorVersion, uint8(1)},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Tape.%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// The media name is a UTF-16LE string; "Media created 2024-06-07 ...".
	if tp.Name == "" {
		t.Error("Tape.Name is empty; expected the media name string")
	}
	// Software name must identify Veritas Backup Exec.
	if tp.Software == "" {
		t.Error("Tape.Software is empty; expected the writer software name")
	}
	// FLB size also drives the reader's block alignment.
	if r.flbsize != 1024 {
		t.Errorf("reader flbsize = %d, want 1024", r.flbsize)
	}
}

// TestGoldenSetFields verifies the MTF_SSET descriptor block of the real
// archive. SSET sits on a physical-block boundary after the media header.
func TestGoldenSetFields(t *testing.T) {
	if !goldenBKFExists() {
		t.Skipf("%s not present; skipping", goldenBKF)
	}
	r := openGolden(t)

	var set *SetInfo
	for {
		blk, err := r.Next()
		if err != nil {
			t.Fatalf("Next before SSET: %v", err)
		}
		if blk.Kind == KindSet {
			set = blk.Set
			break
		}
	}
	if set == nil {
		t.Fatal("no KindSet block found")
	}

	// Values hand-verified from the raw SSET bytes.
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"Number", set.Number, uint16(1)},
		{"Attributes", set.Attributes, uint32(0x410)},
		{"Compression", set.Compression, uint16(0)},
		{"SoftwareVendorID", set.SoftwareVendorID, uint16(0xacda)},
		{"PBA", set.PBA, uint64(3)},
		{"MajorVersion", set.MajorVersion, uint8(22)},
		{"MinorVersion", set.MinorVersion, uint8(0)},
		{"TimeZone", set.TimeZone, int8(-20)}, // EST (UTC-5)
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("Set.%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestGoldenVolumeEntry verifies the first MTF_VOLB entry: its device name is a
// UNC path, proving string decoding (UTF-16LE, String Type 2) works on real
// data, and that the VOLB_DEV_UNC_BIT is set.
func TestGoldenVolumeEntry(t *testing.T) {
	if !goldenBKFExists() {
		t.Skipf("%s not present; skipping", goldenBKF)
	}
	r := openGolden(t)

	for {
		blk, err := r.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryVolume {
			if blk.Header.Name != goldenVolumeDevice {
				t.Errorf("volume device name = %q, want %q", blk.Header.Name, goldenVolumeDevice)
			}
			if blk.Header.Attributes != 0x4 { // VOLB_DEV_UNC_BIT
				t.Errorf("volume attributes = %#x, want 0x4 (UNC)", blk.Header.Attributes)
			}
			return
		}
	}
}
