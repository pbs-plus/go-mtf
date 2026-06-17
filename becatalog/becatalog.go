// Package becatalog parses the Backup Exec catalog payload that some MTF
// writers place inside the standard File/Directory Detail (FDD) stream
// envelope.
//
// The MTF spec (section 7) defines the FDD stream and its standard binary
// record layout. Backup Exec reuses the standard 'TFDD' stream ID but
// substitutes its own catalog format for the payload: a small binary prefix
// followed by an ASCII XML document (the Backup Exec "Catalog Image File",
// root element <CatImageFile>).
//
// When the parent [go-mtf] library detects a Backup Exec FDD, it populates
// [mtf.Catalog.BECatalog] automatically. Most callers should use that field
// directly rather than calling [ParseFDD] themselves. This package is only
// needed when parsing a raw Backup Exec FDD payload outside the reader loop.
//
// The parser is read-only and tolerant: unknown elements are ignored.
package becatalog

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
)

// Catalog is a parsed Backup Exec catalog image (one <CatImageFile>). It
// describes a single backed-up image within a Backup Exec media family: the
// backup metadata, the cartridges that make up the family, the synthetic-image
// table and the directory tree recorded by the catalog.
type Catalog struct {
	// Header is the catalog file header (format version and status).
	Header FileHeader
	// Image is the catalog image metadata: backup type, time, counts, GUIDs,
	// machine and resource names.
	Image Image
	// Cartridges lists the media cartridges referenced by the catalog's
	// <CatFragment> section. On a consolidated catalog this is only the one
	// cartridge the catalog file belongs to. For the full family cartridge
	// list, use [Catalog.AllCartridges].
	Cartridges []Cartridge
	// Images is the synthetic-image table (one entry per catalog image).
	Images []SynthImage
	// ImageExtras is the per-image cartridge reference table (one entry per
	// image×cartridge combination). This is where the full list of cartridges
	// that make up the media family is found — the top-level Cartridges slice
	// only contains the cartridge this catalog file belongs to.
	ImageExtras []SynthImageExtraInfo
	// Tree is the directory tree recorded by the catalog. Each node's RawIndex
	// (the XML "RI" record index) links it to its parent. The tree may be empty
	// when the catalog records no directory detail.
	Tree []Node
}

// AllCartridges returns the deduplicated set of all cartridges referenced by
// this catalog, derived from the <SynthImageExtraInfo> entries. This is the
// complete list of tapes needed to restore the entire media family, in contrast
// to [Catalog.Cartridges] which only lists the cartridge the catalog file was
// found on.
func (c *Catalog) AllCartridges() []string {
	seen := make(map[string]struct{}, len(c.ImageExtras))
	result := make([]string, 0, len(c.ImageExtras))
	for _, e := range c.ImageExtras {
		if e.CartridgeLabel == "" {
			continue
		}
		if _, ok := seen[e.CartridgeLabel]; !ok {
			seen[e.CartridgeLabel] = struct{}{}
			result = append(result, e.CartridgeLabel)
		}
	}
	return result
}

// FileHeader holds the <CatImageFileHeader> fields.
type FileHeader struct {
	MajorVersion      int   `xml:"MajorVersion"`
	MinorVersion      int   `xml:"MinorVersion"`
	FileType          int   `xml:"CatFileType"`
	FileStatus        int   `xml:"CatFileStatus"`
	NumImages         int   `xml:"NumOfImages"`
	LastAccessTimeUTC int64 `xml:"LastAccessTimeUTC"`
}

// Image holds the <CatImageAttributes> metadata for one backed-up image. Field
// names mirror the Backup Exec XML element names.
type Image struct {
	PlugInType      int    `xml:"PlugInType"`
	ImageVersion    int    `xml:"ImageVersion"`
	ImageNumber     int    `xml:"ImageNumber"`
	BackupType      int    `xml:"BackupType"`
	OsID            int    `xml:"OsId"`
	OsVersion       int    `xml:"OsVersion"`
	Status          int64  `xml:"Status"`
	ExtStatus       int64  `xml:"ExtStatus"`
	NumDirectories  int    `xml:"NumDirs"`
	NumFiles        int    `xml:"NumFiles"`
	NumBytes        int64  `xml:"NumBytes"`
	NumCorruptFiles int    `xml:"NumCorruptFiles"`
	NumFilesInUse   int    `xml:"NumFilesInUse"`
	FDDSeqNum       int    `xml:"FDDSeqNum"`
	FDDPBA          int64  `xml:"FDDPBA"`
	FDDVersion      int    `xml:"FDDVersion"`
	VendorID        int    `xml:"VendorId"`
	JobInstanceGUID string `xml:"JobInstanceGuid"`
	FamilyGUID      string `xml:"FamilyGuid"`
	FamilyID        int64  `xml:"FamilyId"`
	MachineName     string `xml:"MachineName"`
	ResourceName    string `xml:"ResourceName"`
	EngineName      string `xml:"EngineName"`
	UserName        string `xml:"UserName"`
	BackupTimeUTC   int64  `xml:"BackupTimeUTC"`
	ImageGUID       string `xml:"ImageGUID"`
	ContentGUID     string `xml:"ContentGUID"`
	HistoryFileName string `xml:"HistoryFileName"`
	ResourceLabel   string `xml:"ResourceLabel"`
}

// Cartridge describes one media cartridge referenced by the catalog (a
// <CatFragment> element).
type Cartridge struct {
	// Label is the cartridge label (for example "B2D027089").
	Label string `xml:"CartridgeLabel"`
	// Location is the storage location path.
	Location string `xml:"Location"`
	// RelativeLocation is the location relative to the media root.
	RelativeLocation string `xml:"RelativeLocation"`
	// ContainerGUID is the container GUID, if any.
	ContainerGUID string `xml:"ContainerGuid"`
	// MediaFamilyName is the human-readable media family name.
	MediaFamilyName string `xml:"MediaFamilyName"`
}

// SynthImage is one entry in the <SynthImageTableEntries> table.
type SynthImage struct {
	ImageGUID   string `xml:"ImageGUID"`
	ContentGUID string `xml:"ContentGUID"`
	CatalogGUID string `xml:"CatalogGUID"`
	ObjectCount int64  `xml:"ObjectCount"`
	Status      int64  `xml:"Status"`
}

// SynthImageExtraInfo is one entry in the <SynthImageExtraInfo> section. Each
// entry links an image to the cartridge it was written to, giving the full
// cross-reference that the top-level CatFragment alone does not provide.
//
// There can be many more ExtraInfo entries than SynthImage entries: a single
// logical image that spans multiple cartridges produces one ExtraInfo per
// cartridge, all sharing the same ImageNumber.
type SynthImageExtraInfo struct {
	Size           int64  `xml:"Size"`
	BackupTimeUTC  int64  `xml:"BackupTimeUTC"`
	ImageNumber    int    `xml:"ImageNumber"`
	MediaNumber    int    `xml:"MediaNumber"`
	BackupType     int    `xml:"BackupType"`
	ImageName      string `xml:"ImageName"`
	CartridgeLabel string `xml:"CartridgeLabel"`
}

// Node is one directory-tree entry (an <ET> element) in the catalog's
// <ImageStrings>. Nodes form a tree via the RawIndex (the XML "RI") which names
// the parent.
type Node struct {
	// Name is the NM attribute (the directory/file name).
	Name string
	// RawIndex is the RI attribute as recorded: the record index that names the
	// parent. The root uses "."; other nodes carry a numeric index.
	RawIndex string
	// OST is the OS type attribute (may be empty).
	OST string
}

// ErrNotBackupExec is returned by [ParseFDD] when the payload is not a Backup
// Exec catalog (for example a standard MTF binary FDD, or an unrecognized format).
var ErrNotBackupExec = errors.New("becatalog: not a Backup Exec FDD payload")

// ParseFDD decodes raw Backup Exec FDD bytes into a [Catalog]. It is the same as
// [Parse] but operates directly on the raw byte slice.
func ParseFDD(raw []byte) (*Catalog, error) {
	mainXML, treeXML, ok := splitSections(raw)
	if !ok {
		return nil, ErrNotBackupExec
	}
	cat := &Catalog{}
	if err := decodeXML(cat, mainXML); err != nil {
		return nil, err
	}
	if len(treeXML) > 0 {
		if err := decodeXML(cat, treeXML); err != nil {
			return nil, err
		}
	}
	return cat, nil
}

// catalogMagic marks the start of the Backup Exec binary index that follows the
// main XML document and precedes the trailing <ImageStrings> tree.
const catalogMagic = "CATALOG-"

// splitSections divides a Backup Exec FDD payload into its two ASCII XML
// sections. The payload is a concatenation of:
//
//   - the main catalog XML: <CatImageFile>...</CatImageFile> (header, image
//     attributes, fragments/cartridges, synthetic-image table), beginning at
//     the offset held in the leading uint32;
//   - a binary index beginning with the 'CATALOG-' magic (not XML; skipped);
//   - a trailing XML document: <?xml?><ImageStrings>...<ET>...</ImageStrings>
//     containing the directory tree.
//
// It returns ok=false when the payload does not look like a Backup Exec FDD.
func splitSections(raw []byte) (mainXML, treeXML []byte, ok bool) {
	if len(raw) < 4 {
		return nil, nil, false
	}
	off := int(binary.LittleEndian.Uint32(raw[:4]))
	if off < 4 || off >= len(raw) {
		return nil, nil, false
	}
	rest := raw[off:]
	if len(rest) == 0 || rest[0] != '<' {
		return nil, nil, false
	}
	before, after, ok0 := bytes.Cut(rest, []byte(catalogMagic))
	if !ok0 {
		// No binary section: the whole body is one XML document.
		return rest, nil, true
	}
	mainXML = bytes.TrimRight(before, " \t\r\n")
	tail := after
	xmlStart := bytes.Index(tail, []byte("<?xml"))
	if xmlStart < 0 {
		return mainXML, nil, true
	}
	treeXML = tail[xmlStart:]
	return mainXML, treeXML, true
}

// decodeXML parses one ASCII XML section of a Backup Exec catalog into cat.
// Either the main <CatImageFile> document or the trailing <ImageStrings>
// document may be passed. It is intentionally lenient: only well-known elements
// are read and the decoder runs in non-strict mode.
func decodeXML(cat *Catalog, section []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(section))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("becatalog: decoding XML: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "CatImageFileHeader":
			if err := dec.DecodeElement(&cat.Header, &se); err != nil {
				return err
			}
		case "CatImageAttributes":
			if err := dec.DecodeElement(&cat.Image, &se); err != nil {
				return err
			}
		case "CatFragment":
			var frag Cartridge
			if err := dec.DecodeElement(&frag, &se); err != nil {
				return err
			}
			cat.Cartridges = append(cat.Cartridges, frag)
		case "SynthImage":
			var si SynthImage
			if err := dec.DecodeElement(&si, &se); err != nil {
				return err
			}
			cat.Images = append(cat.Images, si)
		case "SynthImageExtraInfo":
			var ei SynthImageExtraInfo
			if err := dec.DecodeElement(&ei, &se); err != nil {
				return err
			}
			cat.ImageExtras = append(cat.ImageExtras, ei)
		case "ET":
			var n Node
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "NM":
					n.Name = a.Value
				case "RI":
					n.RawIndex = a.Value
				case "OST":
					n.OST = a.Value
				}
			}
			cat.Tree = append(cat.Tree, n)
		}
	}
}
