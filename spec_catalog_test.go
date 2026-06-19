package mtf

// spec_catalog_test.go asserts the Media Based Catalog (Type 1) field offsets
// against MTF-100a Structures 26-32 (FDD Common Header, FDD Volume/Directory/
// File entries, Set Map Header and Set Map Entry). As with spec_offsets_test.go,
// each expected value is transcribed from the named spec structure.

import "testing"

// TestSpecFDDCommonHeaderOffsets verifies MTF_FDD_HDR (Structure 26, 36 bytes).
func TestSpecFDDCommonHeaderOffsets(t *testing.T) {
	want := map[string]int{
		"LENGTH":                  0,  // UINT16
		"TYPE":                    2,  // UINT8[4]
		"MEDIA_SEQ_NUMBER":        6,  // UINT16
		"COMMON_BLOCK_ATTRIBUTES": 8,  // UINT32
		"FORMAT_LOGICAL_ADDRESS":  12, // UINT64
		"DISPLAYABLE_SIZE":        20, // UINT64
		"LINK":                    28, // INT32
		"OS_ID":                   32, // UINT8
		"OS_VERSION":              33, // UINT8
		"STRING_TYPE":             34, // UINT8
		"PAD":                     35, // UINT8
	}
	got := map[string]int{
		"LENGTH":                  fddLenOff,
		"TYPE":                    fddTypeOff,
		"MEDIA_SEQ_NUMBER":        fddSeqOff,
		"COMMON_BLOCK_ATTRIBUTES": fddAttrOff,
		"FORMAT_LOGICAL_ADDRESS":  fddFLAOff,
		"DISPLAYABLE_SIZE":        fddSizeOff,
		"LINK":                    fddLinkOff,
		"OS_ID":                   fddOSIDOff,
		"STRING_TYPE":             fddStrTypeOff,
	}
	if fddHdrSize != 36 {
		t.Errorf("fddHdrSize = %d, want 36 (spec Structure 26)", fddHdrSize)
	}
	for name, w := range want {
		if g, ok := got[name]; ok && g != w {
			t.Errorf("FDD HDR %s offset = %d, want %d (spec Structure 26)", name, g, w)
		}
	}
}

// TestSpecFDDVolumeEntryOffsets verifies MTF_FDD_VOLB (Structure 27).
func TestSpecFDDVolumeEntryOffsets(t *testing.T) {
	want := map[string]int{
		"VOLB Attributes":  36, // UINT32
		"Device Name":      40, // MTF_TAPE_ADDRESS
		"Volume Name":      44, // MTF_TAPE_ADDRESS
		"Machine Name":     48, // MTF_TAPE_ADDRESS
		"OS_SPECIFIC_DATA": 52, // MTF_TAPE_ADDRESS
		"Media Write Date": 57, // MTF_DATE_TIME
	}
	got := map[string]int{
		"VOLB Attributes":  fddVolAttrOff,
		"Device Name":      fddVolDeviceOff,
		"Volume Name":      fddVolLabelOff,
		"Machine Name":     fddVolMachineOff,
		"Media Write Date": fddVolDateOff,
	}
	for name, w := range want {
		if g, ok := got[name]; ok && g != w {
			t.Errorf("FDD VOLB %s offset = %d, want %d (spec Structure 27)", name, g, w)
		}
	}
}

// TestSpecFDDObjectEntryOffsets verifies MTF_FDD_DIRB (Structure 28) and
// MTF_FDD_FILE (Structure 29) which share a layout: four dates, attributes,
// name, OS-specific data.
//
// Note on the spec's FILE attributes offset: Structure 29 lists FILE Attributes
// at offset 55 while Structure 28 (DIRB) lists DIRB Attributes at 56 with the
// name at 60 in both. Offset 55 would leave an unexplained gap byte (55-59 for
// a 4-byte field) and break the otherwise identical layout, so the spec's 55 is
// treated as a typo and both use 56 — the packed, consistent interpretation.
func TestSpecFDDObjectEntryOffsets(t *testing.T) {
	want := map[string]int{
		"Last Modification Date": 36, // MTF_DATE_TIME
		"Creation Date":          41, // MTF_DATE_TIME
		"Backup Date":            46, // MTF_DATE_TIME
		"Last Access Date":       51, // MTF_DATE_TIME
		"Attributes":             56, // UINT32 (DIRB=56; FILE table typo 55)
		"Name":                   60, // MTF_TAPE_ADDRESS
	}
	got := map[string]int{
		"Last Modification Date": fddObjModOff,
		"Creation Date":          fddObjCrOff,
		"Backup Date":            fddObjBakOff,
		"Last Access Date":       fddObjAccOff,
		"Attributes":             fddObjAttrOff,
		"Name":                   fddObjNameOff,
	}
	for name, w := range want {
		if g := got[name]; g != w {
			t.Errorf("FDD DIRB/FILE %s offset = %d, want %d (spec Structure 28/29)", name, g, w)
		}
	}
}

// TestSpecFDDTypeValues verifies the FDD entry TYPE codes (Table 26).
func TestSpecFDDTypeValues(t *testing.T) {
	cases := []struct {
		name string
		got  [4]byte
		id   string
	}{
		{"VOLB", fddVOLB, "VOLB"},
		{"DIRB", fddDIRB, "DIRB"},
		{"FILE", fddFILE, "FILE"},
		{"FEND", fddFEND, "FEND"},
	}
	for _, c := range cases {
		want := [4]byte{c.id[0], c.id[1], c.id[2], c.id[3]}
		if c.got != want {
			t.Errorf("FDD type %s = %q, want %q (spec Table 26)", c.name, c.got[:], want[:])
		}
	}
}

// TestSpecSetMapHeaderOffsets verifies MTF_SM_HDR (Structure 31, 8 bytes).
func TestSpecSetMapHeaderOffsets(t *testing.T) {
	if smHdrSize != 8 {
		t.Errorf("smHdrSize = %d, want 8 (spec Structure 31)", smHdrSize)
	}
	if smMFMIDOff != 0 {
		t.Errorf("smMFMIDOff = %d, want 0 (spec Structure 31)", smMFMIDOff)
	}
	if smCountOff != 4 {
		t.Errorf("smCountOff = %d, want 4 (spec Structure 31)", smCountOff)
	}
}

// TestSpecSetMapEntryOffsets verifies MTF_SM_ENTRY (Structure 32).
func TestSpecSetMapEntryOffsets(t *testing.T) {
	want := map[string]int{
		"Length":                    0,  // UINT16
		"Media Sequence Number":     2,  // UINT16
		"Common Block Attributes":   4,  // UINT32
		"SSET Attributes":           8,  // UINT32
		"SSET PBA":                  12, // UINT64
		"FDD PBA":                   20, // UINT64
		"FDD Media Sequence Number": 28, // UINT16
		"Data Set Number":           30, // UINT16
		"Number Of Directories":     32, // UINT32
		"Number Of Files":           36, // UINT32
		"Number Of Corrupt Files":   40, // UINT32
		"Data Set Displayable Size": 44, // UINT64
		"Number Of Volumes":         52, // UINT16
		"Data Set Name":             56, // MTF_TAPE_ADDRESS
		"Data Set Description":      64, // MTF_TAPE_ADDRESS
		"User Name":                 68, // MTF_TAPE_ADDRESS
		"Media Write Date":          72, // MTF_DATE_TIME
		"Time Zone":                 77, // INT8
		"STRING_TYPE":               80, // UINT8
	}
	got := map[string]int{
		"Length":                    smeLenOff,
		"Media Sequence Number":     smeMediaSeqOff,
		"Common Block Attributes":   smeAttrOff,
		"SSET Attributes":           smeSSETAttrOff,
		"SSET PBA":                  smeSSETPBAOff,
		"FDD PBA":                   smeFDDPBAOff,
		"FDD Media Sequence Number": smeFDDSeqOff,
		"Data Set Number":           smeSetNumOff,
		"Number Of Directories":     smeNumDirOff,
		"Number Of Files":           smeNumFileOff,
		"Number Of Corrupt Files":   smeNumCorrOff,
		"Data Set Displayable Size": smeSizeOff,
		"Number Of Volumes":         smeNumVolOff,
		"Data Set Name":             smeNameOff,
		"Data Set Description":      smeDescOff,
		"User Name":                 smeUserOff,
		"Media Write Date":          smeDateOff,
		"Time Zone":                 smeTZOff,
		"STRING_TYPE":               smeStrTypeOff,
	}
	for name, w := range want {
		if g := got[name]; g != w {
			t.Errorf("SM_ENTRY %s offset = %d, want %d (spec Structure 32)", name, g, w)
		}
	}
	// The minimum fixed size spans through Media Catalog Version @82 (1 byte).
	if smeMinSize < 83 {
		t.Errorf("smeMinSize = %d, want >= 83 (spec Structure 32 ends at offset 82)", smeMinSize)
	}
}
