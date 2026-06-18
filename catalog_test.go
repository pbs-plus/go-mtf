package mtf

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

// The helpers below assemble a minimal MTF archive carrying a Type 1 Media
// Based Catalog, exercising the FDD and Set Map parsers. They are
// self-contained so the catalog logic stays regression-tested in-package.

const mbcStrASCII = uint8(1)

func mbcLe16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func mbcLe32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

func mbcTapeAddr(size, offset uint16) []byte { return append(mbcLe16(size), mbcLe16(offset)...) }

func mbcCStr(s string) []byte { return append([]byte(s), 0) }

// mbcDate is a fixed 5-byte MTF_DATE_TIME (2021-04-15).
func mbcDate() []byte { return []byte{0xE5, 0x07, 04, 15, 30} }

// mbcFDDCommon writes a 36-byte MTF_FDD_HDR; the LENGTH is patched by the caller.
func mbcFDDCommon(typ string, mediaSeq uint16, fla, size uint64) []byte {
	b := make([]byte, 36)
	copy(b[2:6], typ)
	binary.LittleEndian.PutUint16(b[6:], mediaSeq)
	binary.LittleEndian.PutUint64(b[12:], fla)
	binary.LittleEndian.PutUint64(b[20:], size)
	b[34] = mbcStrASCII
	return b
}

func mbcFDDVolume(mediaSeq uint16, fla uint64, dev, vol, mach string) []byte {
	d, v, m := mbcCStr(dev), mbcCStr(vol), mbcCStr(mach)
	e := mbcFDDCommon("VOLB", mediaSeq, fla, 0)
	e = append(e, mbcLe32(0x10)...) // VOLB attrs @36
	e = append(e, mbcTapeAddr(uint16(len(d)), 61)...)
	e = append(e, mbcTapeAddr(uint16(len(v)), uint16(61+len(d)))...)
	e = append(e, mbcTapeAddr(uint16(len(m)), uint16(61+len(d)+len(v)))...)
	e = append(e, mbcTapeAddr(0, 0)...)
	e = append(e, mbcDate()...)
	e = append(e, append(append(d, v...), m...)...)
	binary.LittleEndian.PutUint16(e[0:], uint16(len(e)))
	return e
}

func mbcFDDObject(typ, name string, mediaSeq uint16, fla, size uint64, attr uint32) []byte {
	n := mbcCStr(name)
	e := mbcFDDCommon(typ, mediaSeq, fla, size)
	e = append(e, mbcDate()...) // mod @36
	e = append(e, mbcDate()...) // create @41
	e = append(e, mbcDate()...) // backup @46
	e = append(e, mbcDate()...) // access @51
	e = append(e, mbcLe32(attr)...)
	e = append(e, mbcTapeAddr(uint16(len(n)), 68)...)
	e = append(e, mbcTapeAddr(0, 0)...)
	e = append(e, n...)
	binary.LittleEndian.PutUint16(e[0:], uint16(len(e)))
	return e
}

// buildMBCArchive produces a complete Type 1 MBC archive in memory with the
// given FDD stream payload (and the standard synthetic Set Map).
func buildMBCArchiveWithFDD(fdd []byte) []byte {
	var out []byte

	// TAPE (Type 1 catalog, FLB 512).
	tape := make([]byte, 512)
	copy(tape[0:4], "TAPE")
	tape[44] = mbcStrASCII
	binary.LittleEndian.PutUint16(tape[64:], 1)
	binary.LittleEndian.PutUint16(tape[84:], 512)
	setChecksum(tape)
	out = append(out, tape...)

	// SSET.
	sset := make([]byte, 512)
	copy(sset[0:4], "SSET")
	sset[44] = mbcStrASCII
	binary.LittleEndian.PutUint16(sset[62:], 1)
	binary.LittleEndian.PutUint64(sset[80:], 512)
	sset[97] = 1
	setChecksum(sset)
	out = append(out, sset...)

	// FDD payload is supplied by the caller.

	// Set Map payload: header + one entry + one volume + name.
	dsetName := mbcCStr("Backup Set 1")
	volEntry := mbcFDDVolume(1, 1024, "C:", "SYSVOL", "HOST1")
	strBase := 91 + len(volEntry)
	entry := make([]byte, 91)
	binary.LittleEndian.PutUint16(entry[0:], uint16(91+len(volEntry)+len(dsetName)))
	binary.LittleEndian.PutUint16(entry[2:], 1)
	binary.LittleEndian.PutUint64(entry[12:], 512)  // SSET PBA
	binary.LittleEndian.PutUint64(entry[20:], 9999) // FDD PBA
	binary.LittleEndian.PutUint16(entry[30:], 1)    // set number
	binary.LittleEndian.PutUint32(entry[40:], 1)    // num dirs
	binary.LittleEndian.PutUint32(entry[44:], 1)    // num files
	binary.LittleEndian.PutUint64(entry[52:], 5)    // size
	binary.LittleEndian.PutUint16(entry[60:], 1)    // num vols
	binary.LittleEndian.PutUint16(entry[64:], uint16(len(dsetName)))
	binary.LittleEndian.PutUint16(entry[66:], uint16(strBase))
	copy(entry[80:], mbcDate())
	entry[85] = 8 // TZ
	entry[88] = mbcStrASCII

	smp := []byte{}
	smp = append(smp, mbcLe32(0xDEADBEEF)...)
	smp = append(smp, mbcLe16(1)...)
	smp = append(smp, []byte{0, 0}...)
	smp = append(smp, entry...)
	smp = append(smp, volEntry...)
	smp = append(smp, dsetName...)

	// ESET + TFDD + TSMP + SPAD.
	eset := make([]byte, 88)
	copy(eset[0:4], "ESET")
	eset[44] = mbcStrASCII
	binary.LittleEndian.PutUint16(eset[8:], 88)
	binary.LittleEndian.PutUint16(eset[78:], 1)
	setChecksum(eset)
	tfddHdr := make([]byte, 22)
	binary.LittleEndian.PutUint32(tfddHdr[0:], StreamTFDD)
	binary.LittleEndian.PutUint64(tfddHdr[8:], uint64(len(fdd)))
	setStreamChecksum(tfddHdr)
	eset = append(eset, tfddHdr...)
	eset = append(eset, fdd...)
	for len(eset)%4 != 0 {
		eset = append(eset, 0)
	}
	tsmpHdr := make([]byte, 22)
	binary.LittleEndian.PutUint32(tsmpHdr[0:], StreamTSMP)
	binary.LittleEndian.PutUint64(tsmpHdr[8:], uint64(len(smp)))
	setStreamChecksum(tsmpHdr)
	eset = append(eset, tsmpHdr...)
	eset = append(eset, smp...)
	for len(eset)%4 != 0 {
		eset = append(eset, 0)
	}
	spad := make([]byte, 22)
	binary.LittleEndian.PutUint32(spad[0:], StreamSPAD)
	setStreamChecksum(spad)
	eset = append(eset, spad...)
	for len(eset)%512 != 0 {
		eset = append(eset, 0)
	}
	out = append(out, eset...)

	sfmb := make([]byte, 512)
	copy(sfmb[0:4], "SFMB")
	out = append(out, sfmb...)
	return out
}

// buildMBCArchive produces a complete Type 1 MBC archive with a standard FDD
// (VOLB, DIRB, FILE, FEND).
func buildMBCArchive() []byte {
	var fdd []byte
	fdd = append(fdd, mbcFDDVolume(1, 1024, "C:", "SYSVOL", "HOST1")...)
	fdd = append(fdd, mbcFDDObject("DIRB", "mydir", 1, 2048, 0, 0x20)...)
	fdd = append(fdd, mbcFDDObject("FILE", "hello.txt", 1, 3072, 5, 0x80)...)
	fdd = append(fdd, mbcFDDCommon("FEND", 1, 0, 0)...)
	return buildMBCArchiveWithFDD(fdd)
}

func TestMediaBasedCatalog(t *testing.T) {
	r := NewReader(NewSliceTape(buildMBCArchive()))
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}

	c := r.Catalog()
	if c == nil {
		t.Fatal("Catalog() returned nil; expected a parsed Type 1 MBC catalog")
	}
	if c.SetMap == nil {
		t.Fatal("SetMap nil; expected one Set Map")
	}
	if got, want := c.SetMap.MediaFamilyID, uint32(0xDEADBEEF); got != want {
		t.Errorf("SetMap MFMID = %#x, want %#x", got, want)
	}
	if len(c.SetMap.Entries) != 1 {
		t.Fatalf("SetMap entries = %d, want 1", len(c.SetMap.Entries))
	}
	se := c.SetMap.Entries[0]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MediaSeq", se.MediaSeq, uint16(1)},
		{"SetNumber", se.SetNumber, uint16(1)},
		{"SSETPBA", se.SSETPBA, uint64(512)},
		{"FDDPBA", se.FDDPBA, uint64(9999)},
		{"NumDirectories", se.NumDirectories, uint32(1)},
		{"NumFiles", se.NumFiles, uint32(1)},
		{"Size", se.Size, uint64(5)},
		{"Name", se.Name, "Backup Set 1"},
		{"TimeZone", se.TimeZone, int8(8)},
	}
	for _, ck := range checks {
		if ck.got != ck.want {
			t.Errorf("SetMap entry %s = %v, want %v", ck.name, ck.got, ck.want)
		}
	}
	if len(se.Volumes) != 1 {
		t.Fatalf("SetMap entry Volumes = %d, want 1", len(se.Volumes))
	}
	v := se.Volumes[0]
	if v.Name != "C:" || v.VolumeLabel != "SYSVOL" || v.MachineName != "HOST1" {
		t.Errorf("SetMap volume = %+v, want C:/SYSVOL/HOST1", v)
	}

	if len(c.FDD) != 3 {
		t.Fatalf("FDD entries = %d, want 3 (VOLB, DIRB, FILE)", len(c.FDD))
	}
	// VOLB
	if e := c.FDD[0]; e.Type != EntryCatalogVolume || e.MediaSeq != 1 || e.FLA != 1024 ||
		e.Name != "C:" || e.VolumeLabel != "SYSVOL" || e.MachineName != "HOST1" {
		t.Errorf("FDD VOLB = %+v", e)
	}
	// DIRB
	if e := c.FDD[1]; e.Type != EntryCatalogDirectory || e.FLA != 2048 || e.Name != "mydir" || e.Attributes != 0x20 {
		t.Errorf("FDD DIRB = %+v", e)
	}
	// FILE (exercises the spec's off-by-one attributes offset: must read @56).
	if e := c.FDD[2]; e.Type != EntryCatalogFile || e.FLA != 3072 || e.Size != 5 ||
		e.Name != "hello.txt" || e.Attributes != 0x80 {
		t.Errorf("FDD FILE = %+v", e)
	}
}

// TestCatalogVendorPayload ensures an FDD stream carrying a non-standard
// (vendor-specific) payload is captured as raw bytes but yields zero parsed
// FDD entries, rather than corrupting the reader or crashing.
func TestCatalogVendorPayload(t *testing.T) {
	// Vendor XML (e.g. a Backup Exec catalog) in the TFDD stream: no FEND/VOLB.
	xml := []byte("<CatImageFile>" + string(make([]byte, 200)) + "</CatImageFile>")
	archive := buildMBCArchiveWithFDD(xml)

	r := NewReader(NewSliceTape(archive))
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	c := r.Catalog()
	if c == nil {
		t.Fatal("Catalog() nil; vendor payload should still be captured as raw")
	}
	if len(c.FDD) != 0 {
		t.Errorf("vendor FDD parsed %d entries, want 0", len(c.FDD))
	}
	if !bytes.Equal(c.RawFDD, xml) {
		t.Errorf("RawFDD = %q, want the vendor payload verbatim", string(c.RawFDD))
	}
}

func TestCatalogBEAutoDetection(t *testing.T) {
	// Build a Backup Exec FDD payload: uint32 offset + XML starting with <CatImageFile>.
	mainXML := `<?xml version="1.0"?>
<CatImageFile>
<CatImageFileHeader><MajorVersion>4</MajorVersion><MinorVersion>6</MinorVersion><CatFileType>1</CatFileType><CatFileStatus>0</CatFileStatus><NumOfImages>1</NumOfImages></CatImageFileHeader>
<CatImage><CatImageAttributes><MachineName>TESTHOST</MachineName><FamilyId>42</FamilyId><BackupType>5</BackupType></CatImageAttributes>
<CatFragmentEntries><CatFragment><CartridgeLabel>B2D0001</CartridgeLabel><MediaFamilyName>TestFamily</MediaFamilyName></CatFragment></CatFragmentEntries>
</CatImage>
</CatImageFile>`
	var buf bytes.Buffer
	off := make([]byte, 4)
	binary.LittleEndian.PutUint32(off, uint32(4))
	buf.Write(off)
	buf.WriteString(mainXML)
	archive := buildMBCArchiveWithFDD(buf.Bytes())

	r := NewReader(NewSliceTape(archive))
	for {
		_, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
	}
	c := r.Catalog()
	if c == nil {
		t.Fatal("Catalog() nil")
	}
	if c.BECatalog == nil {
		t.Fatal("BECatalog nil; Backup Exec FDD should be auto-detected")
	}
	if c.BECatalog.Image.MachineName != "TESTHOST" {
		t.Errorf("BECatalog.Image.MachineName = %q, want TESTHOST", c.BECatalog.Image.MachineName)
	}
	if len(c.BECatalog.Cartridges) == 0 || c.BECatalog.Cartridges[0].Label != "B2D0001" {
		t.Errorf("BECatalog.Cartridges = %v, want B2D0001", c.BECatalog.Cartridges)
	}
}
