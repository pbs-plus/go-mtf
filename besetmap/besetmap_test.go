package besetmap

import (
	"encoding/binary"
	"testing"

	mtf "github.com/pbs-plus/go-mtf"
)

func le16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func le32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// tapeAddr builds an MTF_TAPE_ADDRESS (size u16, offset u16).
func tapeAddr(size, offset uint16) []byte { return append(le16(size), le16(offset)...) }

// mkVol builds a well-formed FDD VOLB record (Structure 27) matching the
// layout mbcFDDVolume uses in the mtf package tests, with its own LENGTH at 0.
func mkVol(dev, vol, mach string) []byte {
	d := append([]byte(dev), 0)
	v := append([]byte(vol), 0)
	m := append([]byte(mach), 0)
	e := make([]byte, 36)            // FDD common header
	copy(e[2:6], "VOLB")
	e[34] = 1                         // STRING_TYPE = ASCII
	e = append(e, le32(0x10)...)      // VOLB attrs @36
	// device/vol/machine MTF_TAPE_ADDRESS (size u16, offset u16), offsets absolute from entry start.
	e = append(e, tapeAddr(uint16(len(d)), 61)...)
	e = append(e, tapeAddr(uint16(len(v)), uint16(61+len(d)))...)
	e = append(e, tapeAddr(uint16(len(m)), uint16(61+len(d)+len(v)))...)
	e = append(e, tapeAddr(0, 0)...) // OS-specific (empty)
	e = append(e, []byte{0xE5, 0x07, 4, 15, 30}...) // 5-byte date
	e = append(e, append(append(d, v...), m...)...)
	binary.LittleEndian.PutUint16(e, uint16(len(e)))
	return e
}

// mkEntry builds a Set Map Entry's fixed fields (91 bytes), LENGTH=91 (no nested
// volume), numVol set as given.
func mkEntry(setNo uint16, numVol int) []byte {
	e := make([]byte, 91)
	binary.LittleEndian.PutUint16(e[0:], 91)   // LENGTH = fixed only
	binary.LittleEndian.PutUint16(e[30:], setNo)
	binary.LittleEndian.PutUint16(e[60:], uint16(numVol))
	binary.LittleEndian.PutUint64(e[44:], 10) // num files
	e[88] = 1                                  // STRING_TYPE
	return e
}

func TestParseSMP2SeparateVolumes(t *testing.T) {
	vol1 := mkVol("C:", "SYSVOL", "HOST1")
	vol2 := mkVol("D:", "DATA", "HOST2")

	var raw []byte
	raw = append(raw, le32(0xDEADBEEF)...) // Media Family ID
	raw = append(raw, le16(2)...)           // count = 2 Set Map Entries
	raw = append(raw, []byte{0, 0}...)      // pad to SetMapHeaderSize (8)
	raw = append(raw, mkEntry(1, 1)...)
	raw = append(raw, vol1...) // volume as a separate record
	raw = append(raw, mkEntry(2, 1)...)
	raw = append(raw, vol2...)

	sm := Parse(raw)
	if sm == nil {
		t.Fatal("Parse returned nil")
	}
	if sm.MediaFamilyID != 0xDEADBEEF {
		t.Errorf("MediaFamilyID=0x%X want 0xDEADBEEF", sm.MediaFamilyID)
	}
	if len(sm.Entries) != 2 {
		t.Fatalf("entries=%d want 2: %+v", len(sm.Entries), sm.Entries)
	}
	for i, e := range sm.Entries {
		if e.SetNumber != uint16(i+1) {
			t.Errorf("entry %d SetNumber=%d want %d", i, e.SetNumber, i+1)
		}
		if e.NumFiles != 10 {
			t.Errorf("entry %d NumFiles=%d want 10", i, e.NumFiles)
		}
		if len(e.Volumes) != 1 {
			t.Errorf("entry %d Volumes=%d want 1", i, len(e.Volumes))
		}
	}
}

// TestRegisteredDispatches ensures importing this package registered the SMP2
// parser with mtf so the main library routes 'SMP2' payloads here.
func TestRegisteredDispatches(t *testing.T) {
	// A payload with count=1, one entry, one trailing volume.
	var raw []byte
	raw = append(raw, le32(1)...)
	raw = append(raw, le16(1)...)
	raw = append(raw, []byte{0, 0}...)
	raw = append(raw, mkEntry(7, 1)...)
	raw = append(raw, mkVol("C:", "SYSVOL", "H")...)

	sm := mtf.SetMapParserFunc(Parse).ParseSetMap(raw)
	if sm == nil || len(sm.Entries) != 1 || sm.Entries[0].SetNumber != 7 {
		t.Fatalf("unexpected: %+v", sm)
	}
}
