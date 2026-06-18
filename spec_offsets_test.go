package mtf

// This file asserts that the package's internal field-offset constants match
// the Microsoft Tape Format Specification (MTF-100a) field-for-field. Each
// expected value is transcribed directly from the named spec structure so the
// test is anchored to the spec rather than to the implementation under test.
//
// If any of these fail, the implementation is reading (or writing) a field from
// the wrong byte offset and must be corrected. The real-world .bkf golden
// values in spec_golden_test.go provide an independent end-to-end check.

import "testing"

// specCommonOffsets are the MTF_DB_HDR (Common Block Header) field offsets, per
// MTF-100a "Structure 4. Common Block Header (MTF_DB_HDR)".
var specCommonOffsets = map[string]int{
	"DBLK Type":              0,  // UINT32
	"Block Attributes":       4,  // UINT32
	"Offset To First Event":  8,  // UINT16
	"OS ID":                  10, // UINT8
	"OS Version":             11, // UINT8
	"Displayable Size":       12, // UINT64
	"Format Logical Address": 20, // UINT64
	"Control Block ID":       36, // UINT32
	"OS Specific Data":       44, // MTF_TAPE_ADDRESS (size,offset)
	"String Type":            48, // UINT8
	"Header Checksum":        50, // UINT16
}

func TestSpecCommonBlockHeaderOffsets(t *testing.T) {
	got := map[string]int{
		"DBLK Type":              dbTypeOff,
		"Block Attributes":       dbAttrOff,
		"Offset To First Event":  dbOffOff,
		"OS ID":                  dbOSIDOff,
		"OS Version":             dbOSVerOff,
		"Displayable Size":       dbSizeOff,
		"Format Logical Address": dbFLAOff,
		"Control Block ID":       dbCBIDOff,
		"OS Specific Data":       dbOSDataOff,
		"String Type":            dbStrTypeOff,
		"Header Checksum":        dbChecksumOff,
	}
	for name, want := range specCommonOffsets {
		if got[name] != want {
			t.Errorf("%s offset = %d, want %d (spec Structure 4)", name, got[name], want)
		}
	}
	if dbCommonSize != 52 {
		t.Errorf("dbCommonSize = %d, want 52 (spec Structure 4: 52-byte common header)", dbCommonSize)
	}
}

// specTapeOffsets are the MTF_TAPE field offsets, per MTF-100a "Structure 5.
// Tape Header Descriptor Block (MTF_TAPE)". Fields begin after the 52-byte
// common header.
var specTapeOffsets = map[string]int{
	"Media Family ID":           52, // UINT32
	"TAPE Attributes":           56, // UINT32
	"Media Sequence Number":     60, // UINT16
	"Password Encryption Algo":  62, // UINT16
	"Soft Filemark Block Size":  64, // UINT16
	"Media Based Catalog Type":  66, // UINT16
	"Media Name":                68, // MTF_TAPE_ADDRESS
	"Media Description/Label":   72, // MTF_TAPE_ADDRESS
	"Media Password":            76, // MTF_TAPE_ADDRESS
	"Software Name":             80, // MTF_TAPE_ADDRESS
	"Format Logical Block Size": 84, // UINT16
	"Software Vendor ID":        86, // UINT16
	"Media Date":                88, // MTF_DATE_TIME (5 bytes)
	"MTF Major Version":         93, // UINT8
}

func TestSpecTapeBlockOffsets(t *testing.T) {
	got := map[string]int{
		"Media Family ID":           tapeMFMIDOff,
		"TAPE Attributes":           tapeAttrOff,
		"Media Sequence Number":     tapeSeqOff,
		"Password Encryption Algo":  tapeEncryptOff,
		"Soft Filemark Block Size":  tapeSFMSizeOff,
		"Media Based Catalog Type":  tapeCatTypeOff,
		"Media Name":                tapeNameOff,
		"Media Description/Label":   tapeLabelOff,
		"Media Password":            tapePasswdOff,
		"Software Name":             tapeSoftwareOff,
		"Format Logical Block Size": tapeFLBSizeOff,
		"Software Vendor ID":        tapeVendorIDOff,
		"Media Date":                tapeCTimeOff,
		"MTF Major Version":         tapeVersionOff,
	}
	for name, want := range specTapeOffsets {
		if got[name] != want {
			t.Errorf("TAPE %s offset = %d, want %d (spec Structure 5)", name, got[name], want)
		}
	}
}

// specSSETOffsets, per MTF-100a "Structure 6. Start of Set Descriptor Block
// (MTF_SSET)".
var specSSETOffsets = map[string]int{
	"SSET Attributes":              52, // UINT32
	"Password Encryption Algo":     56, // UINT16
	"Software Compression Algo":    58, // UINT16
	"Software Vendor ID":           60, // UINT16
	"Data Set Number":              62, // UINT16
	"Data Set Name":                64, // MTF_TAPE_ADDRESS
	"Data Set Description":         68, // MTF_TAPE_ADDRESS
	"Data Set Password":            72, // MTF_TAPE_ADDRESS
	"User Name":                    76, // MTF_TAPE_ADDRESS
	"Physical Block Address (PBA)": 80, // UINT64
	"Media Write Date":             88, // MTF_DATE_TIME
	"Software Major Version":       93, // UINT8
	"Software Minor Version":       94, // UINT8
	"Time Zone":                    95, // INT8
	"MTF Minor Version":            96, // UINT8
	"Media Catalog Version":        97, // UINT8
}

func TestSpecSSETOffsets(t *testing.T) {
	got := map[string]int{
		"SSET Attributes":              ssetAttrOff,
		"Password Encryption Algo":     ssetEncryptOff,
		"Software Compression Algo":    ssetCompOff,
		"Software Vendor ID":           ssetVendorOff,
		"Data Set Number":              ssetNumOff,
		"Data Set Name":                ssetNameOff,
		"Data Set Description":         ssetLabelOff,
		"Data Set Password":            ssetPasswdOff,
		"User Name":                    ssetUserOff,
		"Physical Block Address (PBA)": ssetPBAOff,
		"Media Write Date":             ssetCTimeOff,
		"Software Major Version":       ssetMajorOff,
		"Software Minor Version":       ssetMinorOff,
		"Time Zone":                    ssetTZOff,
		"MTF Minor Version":            ssetVerOff,
		"Media Catalog Version":        ssetCatVerOff,
	}
	for name, want := range specSSETOffsets {
		if got[name] != want {
			t.Errorf("SSET %s offset = %d, want %d (spec Structure 6)", name, got[name], want)
		}
	}
}

// specVOLBOffsets, per MTF-100a "Structure 7. Volume Descriptor Block
// (MTF_VOLB)".
var specVOLBOffsets = map[string]int{
	"VOLB Attributes":  52, // UINT32
	"Device Name":      56, // MTF_TAPE_ADDRESS
	"Volume Name":      60, // MTF_TAPE_ADDRESS
	"Machine Name":     64, // MTF_TAPE_ADDRESS
	"Media Write Date": 68, // MTF_DATE_TIME
}

func TestSpecVOLBOffsets(t *testing.T) {
	got := map[string]int{
		"VOLB Attributes":  volbAttrOff,
		"Device Name":      volbDeviceOff,
		"Volume Name":      volbVolumeOff,
		"Machine Name":     volbMachineOff,
		"Media Write Date": volbCTimeOff,
	}
	for name, want := range specVOLBOffsets {
		if got[name] != want {
			t.Errorf("VOLB %s offset = %d, want %d (spec Structure 7)", name, got[name], want)
		}
	}
}

// specDIRBOffsets, per MTF-100a "Structure 8. Directory Descriptor Block
// (MTF_DIRB)".
var specDIRBOffsets = map[string]int{
	"DIRB Attributes":        52, // UINT32
	"Last Modification Date": 56, // MTF_DATE_TIME
	"Creation Date":          61, // MTF_DATE_TIME
	"Backup Date":            66, // MTF_DATE_TIME
	"Last Access Date":       71, // MTF_DATE_TIME
	"Directory ID":           76, // UINT32
	"Directory Name":         80, // MTF_TAPE_ADDRESS
}

func TestSpecDIRBOffsets(t *testing.T) {
	got := map[string]int{
		"DIRB Attributes":        dirbAttrOff,
		"Last Modification Date": dirbMTimeOff,
		"Creation Date":          dirbCTimeOff,
		"Backup Date":            dirbBTimeOff,
		"Last Access Date":       dirbATimeOff,
		"Directory ID":           dirbIDOff,
		"Directory Name":         dirbNameOff,
	}
	for name, want := range specDIRBOffsets {
		if got[name] != want {
			t.Errorf("DIRB %s offset = %d, want %d (spec Structure 8)", name, got[name], want)
		}
	}
}

// specFILEOffsets, per MTF-100a "Structure 9. File Descriptor Block
// (MTF_FILE)".
var specFILEOffsets = map[string]int{
	"FILE Attributes":        52, // UINT32
	"Last Modification Date": 56, // MTF_DATE_TIME
	"Creation Date":          61, // MTF_DATE_TIME
	"Backup Date":            66, // MTF_DATE_TIME
	"Last Access Date":       71, // MTF_DATE_TIME
	"Directory ID":           76, // UINT32
	"File ID":                80, // UINT32
	"File Name":              84, // MTF_TAPE_ADDRESS
}

func TestSpecFILEOffsets(t *testing.T) {
	got := map[string]int{
		"FILE Attributes":        fileAttrOff,
		"Last Modification Date": fileMTimeOff,
		"Creation Date":          fileCTimeOff,
		"Backup Date":            fileBTimeOff,
		"Last Access Date":       fileATimeOff,
		"Directory ID":           fileDirIDOff,
		"File ID":                fileIDOff,
		"File Name":              fileNameOff,
	}
	for name, want := range specFILEOffsets {
		if got[name] != want {
			t.Errorf("FILE %s offset = %d, want %d (spec Structure 9)", name, got[name], want)
		}
	}
}

// specESETOffsets, per MTF-100a "Structure 12. End of Data Set Descriptor
// Block".
var specESETOffsets = map[string]int{
	"ESET Attributes":           52, // UINT32
	"Number Of Corrupt Files":   56, // UINT32
	"FDD Media Sequence Number": 76, // UINT16
	"Data Set Number":           78, // UINT16
	"Media Write Date":          80, // MTF_DATE_TIME
}

func TestSpecESETOffsets(t *testing.T) {
	got := map[string]int{
		"ESET Attributes":           esetAttrOff,
		"Number Of Corrupt Files":   esetCorruptOff,
		"FDD Media Sequence Number": esetSeqOff,
		"Data Set Number":           esetSetOff,
		"Media Write Date":          esetCTimeOff,
	}
	for name, want := range specESETOffsets {
		if got[name] != want {
			t.Errorf("ESET %s offset = %d, want %d (spec Structure 12)", name, got[name], want)
		}
	}
}

// specStreamHeaderOffsets, per MTF-100a "Structure 15. Stream Header
// (MTF_STREAM_HDR)".
var specStreamHeaderOffsets = map[string]int{
	"Stream ID":                      0,  // UINT32
	"Stream File System Attributes":  4,  // UINT16
	"Stream Media Format Attributes": 6,  // UINT16
	"Stream Length":                  8,  // UINT64
	"Data Encryption Algorithm":      16, // UINT16
	"Data Compression Algorithm":     18, // UINT16
	"Checksum":                       20, // UINT16
}

func TestSpecStreamHeaderOffsets(t *testing.T) {
	got := map[string]int{
		"Stream ID":                      stTypeOff,
		"Stream File System Attributes":  stSysAttrOff,
		"Stream Media Format Attributes": stMediaAttrOff,
		"Stream Length":                  stLengthOff,
		"Data Encryption Algorithm":      stEncryptOff,
		"Data Compression Algorithm":     stCompressOff,
		"Checksum":                       stChecksumOff,
	}
	for name, want := range specStreamHeaderOffsets {
		if got[name] != want {
			t.Errorf("stream header %s offset = %d, want %d (spec Structure 15)", name, got[name], want)
		}
	}
	if streamHeaderSize != 22 {
		t.Errorf("streamHeaderSize = %d, want 22 (spec Structure 15)", streamHeaderSize)
	}
}
