package mtf

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/pbs-plus/go-mtf/becatalog"
)

// Media Based Catalog (MBC). The MTF spec (section 7) defines a Media Based
// Catalog composed of two parts, written as data streams attached to the
// End-of-Set (ESET) descriptor block that closes a data set:
//
//   - File/Directory Detail (FDD): per-data-set index of every volume,
//     directory and file, each annotated with the media sequence number and
//     Format Logical Address of its descriptor block. Stream ID 'TFDD' (Type 1)
//     or 'FDD2' (Type 2). See MTF spec section 7.3.2.
//   - Set Map: cumulative index of every data set in the whole Media Family
//     (one entry per backup/host, each followed by its volume entries). Stream
//     ID 'TSMP' (Type 1) or 'MAP2' (Type 2). See MTF spec section 7.3.3.
//
// The Set Map is rewritten cumulatively as more data sets are appended, so the
// Set Map on the last cartridge of a Media Family is the most complete. It is
// the only structure that spans data sets, and therefore the one to consult for
// "which backups/hosts live in this media family and on which media".
//
// This implementation parses the standard Type 1 binary record layouts. A
// writer may carry a vendor-specific catalog payload inside the standard stream
// envelope (for example a Backup Exec XML catalog in a 'TFDD' stream); in that
// case the standard parser leaves the parsed fields empty and exposes the raw
// stream payload (RawFDD/RawSetMap) for a vendor-specific parser.

// CatalogData exposes the raw, uninterpreted catalog stream payloads captured
// from a data set's ESET block. It decouples vendor-specific catalog parsers
// (for example a Backup Exec XML parser) from this package's concrete [Catalog]
// type: a vendor parser accepts a CatalogData and works purely from the bytes.
//
// [Catalog] satisfies CatalogData via its [Catalog.Raw] method.
type CatalogData interface {
	// Raw returns the captured catalog stream payloads. FDD is the
	// File/Directory Detail ('TFDD'/'FDD2') payload and SetMap is the Set Map
	// ('TSMP'/'MAP2') payload; either may be nil if the stream was absent. For a
	// standard Type 1 catalog these are binary records; a writer may substitute a
	// vendor-specific payload (so a vendor parser takes over from Raw.FDD).
	Raw() CatalogRaw
}

// CatalogRaw holds the raw, uninterpreted catalog stream payloads.
type CatalogRaw struct {
	FDD    []byte
	SetMap []byte
}

// Catalog holds the parsed Media Based Catalog of the most recently completed
// data set, available via [Reader.Catalog]. It is nil when no MBC streams were
// present on the medium, or when the catalog was written in an unrecognized
// (vendor-specific) format that left no parseable standard records.
type Catalog struct {
	// SetMap is the cumulative Media Family summary parsed from the 'TSMP'
	// stream. It lists one entry per data set in the family. It may be nil when
	// no standard Set Map stream was present.
	SetMap *SetMap
	// FDD is the per-data-set File/Directory Detail parsed from the 'TFDD'
	// stream: every volume, directory and file, in archive order, each carrying
	// the location (MediaSeq + FLA) of its descriptor block. It is empty when no
	// standard FDD stream was present or when the FDD payload is a Backup Exec
	// XML catalog (see BECatalog).
	FDD []CatalogEntry
	// BECatalog holds the parsed Backup Exec catalog, auto-detected from the
	// FDD stream. It is nil when the FDD payload is a standard MTF binary
	// catalog (in which case FDD is populated) or when no FDD stream was present.
	BECatalog *becatalog.Catalog
	// RawFDD is the unparsed payload of the FDD data stream. It is populated
	// whenever an FDD stream was captured, including vendor-specific payloads
	// the standard parser does not understand (so a vendor parser can take over).
	RawFDD []byte
	// RawSetMap is the unparsed payload of the Set Map data stream, populated
	// whenever a Set Map stream was captured.
	RawSetMap []byte
}

// Raw returns the captured catalog stream payloads, implementing [CatalogData].
// A vendor-specific catalog parser consumes [CatalogRaw.FDD] directly.
func (c *Catalog) Raw() CatalogRaw {
	return CatalogRaw{FDD: c.RawFDD, SetMap: c.RawSetMap}
}

// CatalogEntryType identifies the kind of object a [CatalogEntry] records.
type CatalogEntryType int

const (
	// EntryCatalogVolume is an FDD volume entry (corresponds to a VOLB DBLK).
	EntryCatalogVolume CatalogEntryType = iota
	// EntryCatalogDirectory is an FDD directory entry (corresponds to a DIRB
	// DBLK).
	EntryCatalogDirectory
	// EntryCatalogFile is an FDD file entry (corresponds to a FILE DBLK).
	EntryCatalogFile
)

// CatalogEntry describes one object (volume, directory or file) recorded in the
// File/Directory Detail. Each entry carries the location of its descriptor
// block on medium, allowing a reader to seek directly to the object.
type CatalogEntry struct {
	// Type is the kind of object.
	Type CatalogEntryType
	// MediaSeq is the MEDIA_SEQ_NUMBER: the 1-based sequence of the medium
	// within the Media Family that holds the object's descriptor block.
	MediaSeq uint16
	// FLA is the Format Logical Address of the object's descriptor block, the
	// byte offset within the medium at which the DBLK is written. Together with
	// MediaSeq it locates the object for random-access extraction.
	FLA uint64
	// Size is the DISPLAYABLE_SIZE copied from the descriptor block.
	Size uint64
	// Attributes is the type-specific attribute word (VOLB/DIRB/FILE
	// attributes) copied from the descriptor block.
	Attributes uint32
	// BlockAttributes is the MTF_DB_HDR common block attributes word.
	BlockAttributes uint32
	// Link is the FDD LINK field: for a directory, the offset of the next
	// sibling directory within the FDD; for a file, the stream offset of its
	// parent directory; for a volume, the offset of the next volume entry.
	Link int32
	// Name is the object name (directory/file name) or, for a volume, the
	// device name.
	Name string
	// VolumeLabel is the volume label (volume entries only).
	VolumeLabel string
	// MachineName is the source machine name (volume entries only). In a Set
	// Map it identifies the host that owns the data set.
	MachineName string
	// WriteTime is the media write date of a volume entry.
	WriteTime time.Time
	// ModTime, CreateTime, BackupTime and AccessTime are the object timestamps
	// copied from the descriptor block (directory and file entries).
	ModTime, CreateTime, BackupTime, AccessTime time.Time
}

// SetMap is the cumulative Media Family summary parsed from a 'TSMP' stream. It
// lists one [SetMapEntry] per data set in the family, in the order the data
// sets were written.
type SetMap struct {
	// MediaFamilyID is the MFMID of the Media Family this Set Map describes; it
	// matches TapeInfo.MFMID.
	MediaFamilyID uint32
	// Entries holds one entry per data set in the Media Family.
	Entries []SetMapEntry
}

// SetMapEntry summarizes one data set (one SSET, typically one host's backup)
// within a Media Family, and is followed by its volume entries.
type SetMapEntry struct {
	// MediaSeq is the Media Sequence Number of the medium this data set begins
	// on, copied from the TAPE block.
	MediaSeq uint16
	// FDDMediaSeq is the FDD Media Sequence Number from the ESET block.
	FDDMediaSeq uint16
	// SetNumber is the data-set number copied from the SSET block.
	SetNumber uint16
	// BlockAttributes is the MTF_DB_HDR common block attributes word.
	BlockAttributes uint32
	// SSETAttributes is the SSET attributes word.
	SSETAttributes uint32
	// SSETPBA is the Physical Block Address of the SSET block.
	SSETPBA uint64
	// FDDPBA is the Physical Block Address of this data set's FDD stream.
	FDDPBA uint64
	// FLA is the Format Logical Address of the SSET block.
	FLA uint64
	// NumDirectories is the count of directories in the data set.
	NumDirectories uint32
	// NumFiles is the count of files in the data set.
	NumFiles uint32
	// NumCorrupt is the count of corrupt files in the data set.
	NumCorrupt uint32
	// Size is the cumulative displayable size of the data set.
	Size uint64
	// Name is the data-set name.
	Name string
	// Description is the data-set description.
	Description string
	// Owner is the user name associated with the data set.
	Owner string
	// WriteTime is the media write date copied from the SSET block.
	WriteTime time.Time
	// TimeZone is the SSET time-zone offset (signed quarter-hours).
	TimeZone int8
	// Volumes are the volume entries (MTF_FDD_VOLB) following this Set Map
	// Entry, one per VOLB in the data set. Each carries the source device,
	// volume label and machine name.
	Volumes []CatalogEntry
}

// Catalog returns the Media Based Catalog of the most recently completed data
// set, or nil if no MBC streams were captured. It is meaningful once [Reader.Next]
// has advanced past the data set's end (its first ESET block) or reached
// end-of-archive.
//
// The catalog is parsed lazily on first call and cached.
func (r *Reader) Catalog() *Catalog {
	if r.catalog != nil {
		return r.catalog
	}
	if len(r.catFDDraw) == 0 && len(r.catSMPraw) == 0 {
		return nil
	}
	c := &Catalog{
		RawFDD:    r.catFDDraw,
		RawSetMap: r.catSMPraw,
	}
	c.FDD = parseFDD(r.catFDDraw)
	c.SetMap = parseSetMap(r.catSMPraw)
	// When the standard parser finds no entries (the FDD payload is vendor-
	// specific), attempt Backup Exec auto-detection.
	if len(c.FDD) == 0 && len(c.RawFDD) > 0 {
		if be, err := becatalog.ParseFDD(c.RawFDD); err == nil {
			c.BECatalog = be
		}
	}
	r.catalog = c
	return c
}

// captureCatalog walks the data streams of the current ESET block, capturing the
// raw payloads of any Media Based Catalog streams (TFDD/FDD2/TSMP/MAP2). Unknown
// streams are skipped and the walk ends at the terminal SPAD stream, whose
// padding data is consumed so the reader lands on the following block boundary.
//
// The ESET block header remains in r.blk on entry; this function may overwrite
// r.blk while walking streams. It is a no-op when the block has no streams.
func (r *Reader) captureCatalog() error {
	off := uint16(u16(r.blk, dbOffOff))
	// Stream headers are 4-byte aligned and begin past the common header. An
	// ESET that merely terminates the catalog (no FDD/Set Map of its own) may
	// record a non-aligned or sub-header offset; treat those as stream-less.
	if off < dbCommonSize || off%4 != 0 {
		return nil
	}
	if err := r.streamStart(); err != nil {
		return err
	}
	for {
		switch r.streamType {
		case StreamSPAD:
			// Terminal stream: consume its block-padding data so scanNext lands
			// exactly on the next block rather than skipping through it.
			if err := r.skipStreamData(r.streamLen); err != nil {
				return err
			}
			r.lastStream = true
			return nil
		case StreamTFDD, StreamFDD2:
			// Catalog streams enumerate every object in the data set and can reach
			// hundreds of MiB on large Backup Exec archives; use the uncapped
			// catalog read path rather than the per-file metadata cap.
			b, err := r.readCatalogStream(r.streamLen)
			if err != nil {
				return err
			}
			r.catFDDraw = append(r.catFDDraw[:0], b...)
			r.catalog = nil // invalidate cached parse
		case StreamTSMP, StreamMAP2, StreamSM2P:
			b, err := r.readCatalogStream(r.streamLen)
			if err != nil {
				return err
			}
			r.catSMPraw = append(r.catSMPraw[:0], b...)
			r.catalog = nil
		}
		if err := r.streamNext(); err != nil {
			return err
		}
	}
}

// FDD common header field offsets (MTF_FDD_HDR, 36 bytes). See MTF spec section
// 7.3.2.2 / Structure 26.
const (
	fddLenOff     = 0  // LENGTH (uint16): size of this entry plus appended strings
	fddTypeOff    = 2  // TYPE (4 bytes): VOLB/DIRB/FILE/FEND
	fddSeqOff     = 6  // MEDIA_SEQ_NUMBER (uint16)
	fddAttrOff    = 8  // COMMON_BLOCK_ATTRIBUTES (uint32)
	fddFLAOff     = 12 // FORMAT_LOGICAL_ADDRESS (uint64)
	fddSizeOff    = 20 // DISPLAYABLE_SIZE (uint64)
	fddLinkOff    = 28 // LINK (int32)
	fddOSIDOff    = 32 // OS_ID (uint8)
	fddStrTypeOff = 34 // STRING_TYPE (uint8)
	fddHdrSize    = 36
)

// Type-specific offsets within an FDD entry (relative to the entry start, i.e.
// including the 36-byte common header). See MTF spec Structures 27-29.
const (
	fddVolAttrOff    = 36 // VOLB attributes
	fddVolDeviceOff  = 40
	fddVolLabelOff   = 44
	fddVolMachineOff = 48
	fddVolDateOff    = 57 // media write date

	fddObjModOff  = 36 // directory/file last modification date
	fddObjCrOff   = 41 // creation date
	fddObjBakOff  = 46 // backup date
	fddObjAccOff  = 51 // last access date
	fddObjAttrOff = 56 // DIRB/FILE attributes
	fddObjNameOff = 60 // directory/file name
)

// FDD entry type identifiers (the TYPE field, stored little-endian).
var (
	fddVOLB = [4]byte{'V', 'O', 'L', 'B'}
	fddDIRB = [4]byte{'D', 'I', 'R', 'B'}
	fddFILE = [4]byte{'F', 'I', 'L', 'E'}
	fddFEND = [4]byte{'F', 'E', 'N', 'D'}
)

// parseFDD walks the FDD stream payload, decoding Type 1 FDD entries until the
// FEND marker or the end of the payload. It tolerates a vendor-specific payload
// (no recognizable entries) by returning an empty slice.
func parseFDD(raw []byte) []CatalogEntry {
	var entries []CatalogEntry
	for off := 0; off+fddHdrSize <= len(raw); {
		e, next, ok := parseFDDEntry(raw[off:])
		if !ok {
			break
		}
		if e != nil {
			entries = append(entries, *e)
		}
		if next <= 0 {
			break
		}
		off += next
	}
	return entries
}

// parseFDDEntry decodes one FDD entry starting at the beginning of rec. It
// returns the decoded entry (nil for FEND), the byte length to advance to the
// next entry (entry LENGTH), and ok=false if rec is not a recognizable FDD entry.
func parseFDDEntry(rec []byte) (e *CatalogEntry, length int, ok bool) {
	if len(rec) < fddHdrSize {
		return nil, 0, false
	}
	typ := [4]byte{rec[2], rec[3], rec[4], rec[5]}
	length = int(u16(rec, fddLenOff))
	// An absurd or non-increasing length signals a vendor-specific payload.
	if length < fddHdrSize {
		return nil, 0, false
	}
	if length > len(rec) {
		// Allow the trailing entry to be clipped only if it is FEND-sized; a
		// larger declared length than remaining bytes means non-standard data.
		return nil, 0, false
	}
	entry := rec[:length]
	strType := u8(rec, fddStrTypeOff)

	switch typ {
	case fddVOLB:
		e = parseFDDVolume(entry, strType)
	case fddDIRB:
		e = parseFDDObject(entry, strType, EntryCatalogDirectory)
	case fddFILE:
		e = parseFDDObject(entry, strType, EntryCatalogFile)
	case fddFEND:
		return nil, length, true
	default:
		return nil, 0, false
	}
	return e, length, true
}

// parseFDDCommon decodes the shared MTF_FDD_HDR fields.
func parseFDDCommon(rec []byte, e *CatalogEntry) {
	e.MediaSeq = u16(rec, fddSeqOff)
	e.BlockAttributes = u32(rec, fddAttrOff)
	e.FLA = u64(rec, fddFLAOff)
	e.Size = u64(rec, fddSizeOff)
	e.Link = int32(u32(rec, fddLinkOff))
}

// parseFDDVolume decodes an MTF_FDD_VOLB entry (Structure 27).
func parseFDDVolume(rec []byte, strType uint8) *CatalogEntry {
	e := &CatalogEntry{Type: EntryCatalogVolume}
	parseFDDCommon(rec, e)
	e.Attributes = u32(rec, fddVolAttrOff)
	e.Name = fddString(rec, fddVolDeviceOff, strType)
	e.VolumeLabel = fddString(rec, fddVolLabelOff, strType)
	e.MachineName = fddString(rec, fddVolMachineOff, strType)
	e.WriteTime = decodeDateTime(rec, fddVolDateOff)
	return e
}

// parseFDDObject decodes an MTF_FDD_DIRB or MTF_FDD_FILE entry (Structures
// 28/29). The two share an identical field layout: four dates, attributes, name
// and OS-specific data. (The spec's FILE table lists the attributes offset one
// byte low; it is an off-by-one error — both use 56, matching the DIRB table.)
func parseFDDObject(rec []byte, strType uint8, typ CatalogEntryType) *CatalogEntry {
	e := &CatalogEntry{Type: typ}
	parseFDDCommon(rec, e)
	e.Attributes = u32(rec, fddObjAttrOff)
	e.ModTime = decodeDateTime(rec, fddObjModOff)
	e.CreateTime = decodeDateTime(rec, fddObjCrOff)
	e.BackupTime = decodeDateTime(rec, fddObjBakOff)
	e.AccessTime = decodeDateTime(rec, fddObjAccOff)
	e.Name = fddString(rec, fddObjNameOff, strType)
	return e
}

// fddString reads the string referenced by the MTF_TAPE_ADDRESS (size,offset)
// pair at off within rec. The offset is relative to the start of rec (the FDD
// entry), per MTF spec section 7.3.2.3.
func fddString(rec []byte, off int, strType uint8) string {
	size, pos := tapepos(rec, off)
	return decodeString(rec, int(pos), int(size), strType, 0)
}

// Set Map field offsets. See MTF spec section 7.3.3 / Structures 31-32.
const (
	smMFMIDOff = 0 // header: Media Family ID (uint32)
	smCountOff = 4 // header: Number Of Set Map Entries (uint16)
	smHdrSize  = 8

	smeLenOff      = 0  // entry: Length (uint16)
	smeMediaSeqOff = 2  // Media Sequence Number (uint16)
	smeAttrOff     = 4  // Common Block Attributes (uint32)
	smeSSETAttrOff = 8  // SSET Attributes (uint32)
	smeSSETPBAOff  = 12 // SSET PBA (uint64)
	smeFDDPBAOff   = 20 // FDD PBA (uint64)
	smeFDDSeqOff   = 28 // FDD Media Sequence Number (uint16)
	smeSetNumOff   = 30 // Data Set Number (uint16)
	smeFLAOff      = 32 // Format Logical Address (uint64)
	smeNumDirOff   = 40 // Number Of Directories (uint32)
	smeNumFileOff  = 44 // Number Of Files (uint32)
	smeNumCorrOff  = 48 // Number Of Corrupt Files (uint32)
	smeSizeOff     = 52 // Data Set Displayable Size (uint64)
	smeNumVolOff   = 60 // Number Of Volumes (uint16)
	smeNameOff     = 64 // Data Set Name (MTF_TAPE_ADDRESS)
	smeDescOff     = 72 // Data Set Description (MTF_TAPE_ADDRESS)
	smeUserOff     = 76 // User Name (MTF_TAPE_ADDRESS)
	smeDateOff     = 80 // Media Write Date (MTF_DATE_TIME)
	smeTZOff       = 85 // Time Zone (int8)
	smeStrTypeOff  = 88 // STRING_TYPE (uint8)
	smeMinSize     = 91 // minimum fixed size of a Set Map Entry
)

// parseSetMap decodes a Type 1 'TSMP' stream payload into a Set Map. It returns
// nil for a payload too short or non-standard to contain a Set Map header.
func parseSetMap(raw []byte) *SetMap {
	if len(raw) < smHdrSize {
		return nil
	}
	sm := &SetMap{
		MediaFamilyID: u32(raw, smMFMIDOff),
	}
	count := int(u16(raw, smCountOff))
	sm.Entries = parseSetMapEntries(raw, smHdrSize, count)
	return sm
}

// parseSetMapEntries walks the Set Map payload starting at off, decoding up to
// count Set Map Entries. Each entry's volume entries may be either nested
// inside the entry's declared LENGTH (spec §7.3.3) or carried as separate
// records that follow the entry (the Backup Exec 'SMP2' variant). The two are
// distinguished by whether numVol volume records fit inside the entry's LENGTH:
// if they do, the entry is self-contained; otherwise the volumes are read as
// the following records. This makes the walk robust to both layouts.
func parseSetMapEntries(raw []byte, off, count int) []SetMapEntry {
	var out []SetMapEntry
	for i := 0; i < count && off+smeMinSize <= len(raw); i++ {
		entry, length, ok := parseSetMapEntry(raw[off:])
		if !ok {
			break
		}
		// If the entry declared volumes but none fit within its LENGTH, the
		// volumes follow as separate records (Backup Exec SMP2 layout). Read
		// them now so the next iteration lands on the next Set Map Entry.
		numVol := int(u16(raw, off+smeNumVolOff))
		if numVol > len(entry.Volumes) {
			cur := off + length
			for v := len(entry.Volumes); v < numVol && cur+fddHdrSize <= len(raw); v++ {
				vlen := int(u16(raw, cur+fddLenOff))
				if vlen < fddHdrSize || cur+vlen > len(raw) {
					break
				}
				strType := u8(raw, cur+fddStrTypeOff)
				ve := parseFDDVolume(raw[cur:cur+vlen], strType)
				entry.Volumes = append(entry.Volumes, *ve)
				cur += vlen
			}
			off = cur
		} else {
			off += length
		}
		out = append(out, entry)
	}
	return out
}

// parseSetMapEntry decodes one Set Map Entry followed by its Number-Of-Volumes
// volume entries. It returns the decoded entry (with Volumes populated), the
// total byte length consumed (entry + volume entries), and ok=false if rec is
// not a recognizable Set Map Entry.
func parseSetMapEntry(rec []byte) (entry SetMapEntry, length int, ok bool) {
	if len(rec) < smeMinSize {
		return entry, 0, false
	}
	entryLen := int(u16(rec, smeLenOff))
	if entryLen < smeMinSize {
		return entry, 0, false
	}
	strType := u8(rec, smeStrTypeOff)

	entry.MediaSeq = u16(rec, smeMediaSeqOff)
	entry.BlockAttributes = u32(rec, smeAttrOff)
	entry.SSETAttributes = u32(rec, smeSSETAttrOff)
	entry.SSETPBA = u64(rec, smeSSETPBAOff)
	entry.FDDPBA = u64(rec, smeFDDPBAOff)
	entry.FDDMediaSeq = u16(rec, smeFDDSeqOff)
	entry.SetNumber = u16(rec, smeSetNumOff)
	entry.FLA = u64(rec, smeFLAOff)
	entry.NumDirectories = u32(rec, smeNumDirOff)
	entry.NumFiles = u32(rec, smeNumFileOff)
	entry.NumCorrupt = u32(rec, smeNumCorrOff)
	entry.Size = u64(rec, smeSizeOff)
	entry.Name = fddString(rec, smeNameOff, strType)
	entry.Description = fddString(rec, smeDescOff, strType)
	entry.Owner = fddString(rec, smeUserOff, strType)
	entry.WriteTime = decodeDateTime(rec, smeDateOff)
	entry.TimeZone = int8(u8(rec, smeTZOff))

	// Volume entries follow the fixed fields, packed within this entry's
	// declared LENGTH (spec §7.3.3: the entry's LENGTH covers the fixed fields,
	// the volume entries, and appended name/description strings). Parse the
	// numVol volumes that fit inside [smeMinSize, entryLen).
	numVol := int(u16(rec, smeNumVolOff))
	cur := smeMinSize
	vols := make([]CatalogEntry, 0, numVol)
	for range numVol {
		if cur+fddHdrSize > entryLen || cur+fddHdrSize > len(rec) {
			break
		}
		vlen := int(u16(rec, cur+fddLenOff))
		if vlen < fddHdrSize || cur+vlen > entryLen || cur+vlen > len(rec) {
			break
		}
		ve := parseFDDVolume(rec[cur:cur+vlen], strType)
		vols = append(vols, *ve)
		cur += vlen
	}
	entry.Volumes = vols
	length = entryLen
	return entry, length, true
}

// Field offsets used by catalog-directed access (spec §5.2.9 EOTM, §5.2.8 ESET).
const (
	eotmLastESETPBAOff = 0x34 // UINT64: Last ESET PBA
)

// isSetMapStream reports whether a 4-byte stream ID denotes a Set Map stream
// (Type 1 TSMP, Type 2 MAP2, or the Backup Exec SM2P variant).
func isSetMapStream(id uint32) bool {
	return id == StreamTSMP || id == StreamMAP2 || id == StreamSM2P
}

// ReadSetMap returns the cumulative Media Based Catalog Set Map from the final
// data set on the medium, using the spec's catalog-directed access path
// (§3.3.2.2 / §5.2.9 / §7.3.1): the trailing MTF_EOTM block stores the PBA of
// the last MTF_ESET; the Set Map stream is physical-block-aligned and is the
// last catalog stream preceding that ESET (Figure 24). This lets a caller
// enumerate every data set (name, SSETPBA, FLA, file count, size) in a handful
// of block reads near end-of-media instead of walking every file forward.
//
// If the tape implements EOM() (e.g. [DriveTape]), ReadSetMap positions at
// end-of-recorded media first; otherwise the caller must have positioned it
// there. The tape's final position is unspecified on return — callers should
// Rewind/SeekBlock before further sequential use.
//
// Returns (nil, nil) if no Set Map is present — e.g. a data-only continuation
// cartridge (the EOTM's MTF_NO_ESET_PBA / MTF_INVALID_ESET_PBA attribute bits
// are set, the medium has no EOTM, or no Set Map stream header is found in the
// trailing blocks). Callers should fall back to a forward walk (Reader.Next)
// in that case.
func ReadSetMap(tape Tape) (*SetMap, error) {
	// Position at end of recorded media if the source supports it.
	if e, ok := tape.(interface{ EOM() error }); ok {
		if err := e.EOM(); err != nil {
			return nil, fmt.Errorf("mtf: ReadSetMap: position at EOM: %w", err)
		}
	}
	end, err := tape.TellBlock()
	if err != nil {
		return nil, fmt.Errorf("mtf: ReadSetMap: tape must report position: %w", err)
	}
	if end <= 2 {
		return nil, nil // nothing to read
	}

	// 1. EOTM is the data block immediately before the trailing filemark + EOD.
	eotmPBA := end - 2
	if err := tape.SeekBlock(eotmPBA); err != nil {
		return nil, fmt.Errorf("mtf: ReadSetMap: seek EOTM %d: %w", eotmPBA, err)
	}
	eotm := make([]byte, maxTapeBlock)
	n, err := readFullBlock(tape, eotm)
	if err != nil {
		return nil, fmt.Errorf("mtf: ReadSetMap: read EOTM: %w", err)
	}
	eotm = eotm[:n]
	if blockType(eotm) != dbEOTM {
		return nil, fmt.Errorf("mtf: ReadSetMap: block at %d is %q, not EOTM", eotmPBA, blockType(eotm))
	}
	// EOTM attribute bits 16/17 flag an absent/invalid ESET PBA (spec Table 3).
	attrs := u32(eotm, 0x04)
	const (
		noESETPBABit      = 1 << 16 // MTF_NO_ESET_PBA
		invalidESETPBABit = 1 << 17 // MTF_INVALID_ESET_PBA
	)
	if attrs&(noESETPBABit|invalidESETPBABit) != 0 {
		return nil, nil // no usable catalog on this medium
	}
	lastESET := int64(u64(eotm, eotmLastESETPBAOff))

	// 2. The EOTM's Last ESET PBA names the filemark trailing the last ESET
	// (spec Figure 24: [ESET DBLK][filemark]). The ESET DBLK is the preceding
	// block. The Set Map stream is the last catalog stream before that ESET,
	// physical-block-aligned. Scan back a few blocks for a Set Map stream header.
	for delta := int64(1); delta <= 4; delta++ {
		p := lastESET - delta
		if p < 0 {
			break
		}
		if err := tape.SeekBlock(p); err != nil {
			continue
		}
		b := make([]byte, maxTapeBlock)
		nn, rerr := readFullBlock(tape, b)
		if rerr != nil || nn < streamHeaderSize {
			continue
		}
		b = b[:nn]
		id := u32(b, 0)
		if !isSetMapStream(id) {
			continue
		}
		// Found the Set Map stream header. Parse its payload.
		streamLen := int64(u64(b, stLengthOff))
		payload, perr := readStreamPayload(tape, b, streamLen)
		if perr != nil {
			return nil, fmt.Errorf("mtf: ReadSetMap: read Set Map stream at %d: %w", p, perr)
		}
		return parseSetMap(payload), nil
	}
	return nil, nil // no Set Map stream header in range
}

// readFullBlock reads one physical block into dst via the Tape interface,
// returning the record length. It surfaces filemark (ErrFilemark) and
// end-of-data (io.EOF) as errors since neither yields data.
func readFullBlock(tape Tape, dst []byte) (int, error) {
	n, err := tape.ReadBlock(dst)
	if err != nil {
		if errors.Is(err, ErrFilemark) || errors.Is(err, io.EOF) {
			return 0, err
		}
		return 0, err
	}
	return n, nil
}

// readStreamPayload assembles a stream's data payload given the first block
// (containing the 22-byte stream header) and the declared stream length. It
// reads additional blocks as needed. The payload is the stream data after the
// header; on tape the stream header is physical-block-aligned and its data
// follows (possibly across blocks).
func readStreamPayload(tape Tape, first []byte, streamLen int64) ([]byte, error) {
	if streamLen <= 0 {
		return nil, nil
	}
	// Skip the 22-byte stream header in the first block; the rest of that
	// block (if any) is the start of the payload.
	hdr := int64(streamHeaderSize)
	payload := make([]byte, 0, streamLen)
	if int64(len(first)) > hdr {
		payload = append(payload, first[hdr:]...)
	}
	for int64(len(payload)) < streamLen {
		b := make([]byte, maxTapeBlock)
		n, err := readFullBlock(tape, b)
		if err != nil {
			// Use whatever we have; parseSetMap tolerates short input.
			break
		}
		payload = append(payload, b[:n]...)
	}
	if int64(len(payload)) > streamLen {
		payload = payload[:streamLen]
	}
	return payload, nil
}
