package becatalog

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// buildBEFDD constructs a minimal Backup Exec FDD payload: a 4-byte little-endian
// XML-offset prefix followed by the given ASCII XML body. It mirrors the
// on-tape framing so the parser sees realistic input.
func buildBEFDD(xml string) []byte {
	off := make([]byte, 4)
	binary.LittleEndian.PutUint32(off, uint32(4))
	return append(off, []byte(xml)...)
}

const mainXML = `<?xml version="1.0"?>
<CatImageFile>
<CatImageFileHeader>
<MajorVersion>4</MajorVersion>
<MinorVersion>6</MinorVersion>
<CatFileType>1</CatFileType>
<CatFileStatus>0</CatFileStatus>
<NumOfImages>1</NumOfImages>
<LastAccessTimeUTC>1717732800</LastAccessTimeUTC>
</CatImageFileHeader>

<CatImage>
<CatImageAttributes>
<PlugInType>16</PlugInType>
<ImageNumber>1</ImageNumber>
<BackupType>5</BackupType>
<OsId>14</OsId>
<NumDirs>2</NumDirs>
<NumFiles>1</NumFiles>
<NumBytes>4096</NumBytes>
<FamilyGuid>{AABBCCDD-0000-0000-0000-000000000001}</FamilyGuid>
<FamilyId>42</FamilyId>
<MachineName>HOST1</MachineName>
<ResourceName>\\HOST1\C:</ResourceName>
<HistoryFileName>{AABBCCDD-0000-0000-0000-000000000001}_1.fh</HistoryFileName>
</CatImageAttributes>

<CatFragmentEntries>
<CatFragment>
<MediaFamilyName>Family One</MediaFamilyName>
<CartridgeLabel>B2D0001</CartridgeLabel>
<Location>D:\BEData</Location>
<RelativeLocation>BEData</RelativeLocation>
<ContainerGuid>{11111111-1111-1111-1111-111111111111}</ContainerGuid>
</CatFragment>
<CatFragment>
<CartridgeLabel>B2D0002</CartridgeLabel>
<Location>D:\BEData</Location>
</CatFragment>
</CatFragmentEntries>

<SynthImageTableEntries>
<SynthImage>
<ImageGUID>{CCCCCCCC-CCCC-CCCC-CCCC-CCCCCCCCCCCC}</ImageGUID>
<ContentGUID>{DDDDDDDD-DDDD-DDDD-DDDD-DDDDDDDDDDDD}</ContentGUID>
<ObjectCount>3</ObjectCount>
</SynthImage>
</SynthImageTableEntries>

</CatImage>
</CatImageFile>`

const treeXML = `<?xml version="1.0" encoding="UTF-8" ?><ImageStrings ImageID="{EEEEEEEE-EEEE-EEEE-EEEE-EEEEEEEEEEEE}"><ET NM="/" RI="."/><ET OST="2" NM="dir1" RI="/"/><ET OST="2" NM="file1.txt" RI="0"/></ImageStrings>`

// TestParseBECatalog parses a synthetic Backup Exec FDD payload (header + tree)
// and asserts every field decodes.
func TestParseBECatalog(t *testing.T) {
	// Build the full multi-section payload: main XML, CATALOG- binary blob,
	// then the trailing ImageStrings XML.
	main := buildBEFDD(mainXML + "\r\nCATALOG-6.0 BEWS\x00binary-index-not-parsed")
	// Append the tree section after the CATALOG- magic; splitSections locates
	// the trailing <?xml after the magic.
	raw := append(main, []byte(treeXML)...)
	cat, err := ParseFDD(raw)
	if err != nil {
		t.Fatalf("ParseFDD: %v", err)
	}

	if cat.Header.MajorVersion != 4 || cat.Header.MinorVersion != 6 {
		t.Errorf("Header version = %d.%d, want 4.6", cat.Header.MajorVersion, cat.Header.MinorVersion)
	}
	if cat.Header.NumImages != 1 {
		t.Errorf("Header NumImages = %d, want 1", cat.Header.NumImages)
	}

	if cat.Image.BackupType != 5 {
		t.Errorf("Image BackupType = %d, want 5", cat.Image.BackupType)
	}
	if cat.Image.NumDirectories != 2 || cat.Image.NumFiles != 1 || cat.Image.NumBytes != 4096 {
		t.Errorf("Image counts = dirs %d files %d bytes %d, want 2/1/4096",
			cat.Image.NumDirectories, cat.Image.NumFiles, cat.Image.NumBytes)
	}
	if cat.Image.FamilyGUID != "{AABBCCDD-0000-0000-0000-000000000001}" {
		t.Errorf("Image FamilyGUID = %q", cat.Image.FamilyGUID)
	}
	if cat.Image.MachineName != "HOST1" {
		t.Errorf("Image MachineName = %q, want HOST1", cat.Image.MachineName)
	}

	if len(cat.Cartridges) != 2 {
		t.Fatalf("Cartridges = %d, want 2", len(cat.Cartridges))
	}
	c0 := cat.Cartridges[0]
	if c0.Label != "B2D0001" || c0.MediaFamilyName != "Family One" || c0.Location != "D:\\BEData" {
		t.Errorf("Cartridge[0] = %+v, want B2D0001/Family One/D:\\BEData", c0)
	}
	if cat.Cartridges[1].Label != "B2D0002" {
		t.Errorf("Cartridge[1] Label = %q, want B2D0002", cat.Cartridges[1].Label)
	}

	if len(cat.Images) != 1 {
		t.Fatalf("SynthImages = %d, want 1", len(cat.Images))
	}
	if cat.Images[0].ObjectCount != 3 {
		t.Errorf("SynthImage ObjectCount = %d, want 3", cat.Images[0].ObjectCount)
	}

	if len(cat.Tree) != 3 {
		t.Fatalf("Tree nodes = %d, want 3", len(cat.Tree))
	}
	if cat.Tree[0].Name != "/" || cat.Tree[0].RawIndex != "." {
		t.Errorf("Tree[0] = %+v, want root /", cat.Tree[0])
	}
	if cat.Tree[1].Name != "dir1" || cat.Tree[1].OST != "2" {
		t.Errorf("Tree[1] = %+v, want dir1 OST=2", cat.Tree[1])
	}
	if cat.Tree[2].Name != "file1.txt" {
		t.Errorf("Tree[2] = %+v, want file1.txt", cat.Tree[2])
	}
}

// TestParseBECatalogSingleSection parses a payload with only the main XML
// section (no trailing ImageStrings) and confirms the tree stays empty while the
// rest decodes.
func TestParseBECatalogSingleSection(t *testing.T) {
	cat, err := ParseFDD(buildBEFDD(mainXML))
	if err != nil {
		t.Fatalf("ParseFDD: %v", err)
	}
	if cat.Image.MachineName != "HOST1" {
		t.Errorf("MachineName = %q", cat.Image.MachineName)
	}
	if len(cat.Cartridges) != 2 {
		t.Errorf("Cartridges = %d, want 2", len(cat.Cartridges))
	}
	if len(cat.Tree) != 0 {
		t.Errorf("Tree = %d nodes, want 0 (no ImageStrings section)", len(cat.Tree))
	}
}

// TestParseNotBackupExec verifies that non-Backup-Exec payloads (a standard MTF
// binary FDD, or empty) are rejected with ErrNotBackupExec.
func TestParseNotBackupExec(t *testing.T) {
	cases := map[string][]byte{
		"empty":      {},
		"standard":   {0x24, 0x00, 'V', 'O', 'L', 'B'},
		"too-short":  {0x01, 0x00},
		"bad-offset": {0xFF, 0xFF, 0xFF, 0xFF, 0x00},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFDD(raw)
			if !errors.Is(err, ErrNotBackupExec) {
				t.Fatalf("err = %v, want ErrNotBackupExec", err)
			}
		})
	}
}

// TestParseToleratesBinaryNoise confirms the parser survives stray bytes after
// the main document by stopping cleanly at the trailing ImageStrings section.
func TestParseToleratesBinaryNoise(t *testing.T) {
	// A malformed < inside a text element would break a naive parser; the section
	// split keeps the two XML documents isolated.
	noisy := buildBEFDD(mainXML + "\r\nCATALOG- noise with < stray byte")
	raw := append(noisy, []byte(treeXML)...)
	cat, err := ParseFDD(raw)
	if err != nil {
		t.Fatalf("ParseFDD: %v", err)
	}
	if len(cat.Tree) != 3 {
		t.Errorf("Tree = %d, want 3", len(cat.Tree))
	}
	if !strings.HasPrefix(cat.Image.FamilyGUID, "{AABBCCDD") {
		t.Errorf("FamilyGUID lost: %q", cat.Image.FamilyGUID)
	}
}
