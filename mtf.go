// Package mtf provides a reader for the Microsoft Tape Format (MTF) used by
// NTBACKUP.EXE (.bkf) files and Windows tape backups.
//
// The implementation is a port of Ivo van Poorten / geocar's mtftar
// (https://github.com/geocar/mtftar) to idiomatic Go. It supports reading
// (listing and extracting file data) from MTF/BKF streams.
//
// The primary entry point is the [Reader], which mirrors the archive/tar API:
//
//	r, err := mtf.Open("backup.bkf")
//	if err != nil { log.Fatal(err) }
//	defer r.Close()
//	for {
//		h, err := r.Next()
//		if err == io.EOF { break }
//		if err != nil { log.Fatal(err) }
//		fmt.Println(h.Name)
//		if h.Type == mtf.EntryFile {
//			io.Copy(os.Stdout, r)
//		}
//	}
package mtf

import "time"

// EntryType describes the kind of entry a [Header] represents.
type EntryType int

const (
	// EntryFile is a regular file. Its contents are available via [Reader.Read].
	EntryFile EntryType = iota
	// EntryDirectory is a directory entry.
	EntryDirectory
	// EntryVolume is a source volume (device) the backup was taken from.
	EntryVolume
)

// Header describes a single entry (file, directory or volume) within an MTF
// archive. It is returned by [Reader.Next].
type Header struct {
	// Type is the kind of entry.
	Type EntryType
	// Name is the fully resolved path of the entry, formed from the source
	// volume, directory chain and the entry name using "/" separators.
	Name string
	// Size is the length in bytes of the entry's standard (STAN) data stream.
	// It is only non-zero for files.
	Size int64
	// ModTime is the modification time of the entry.
	ModTime time.Time
	// AccessTime is the last access time, if recorded.
	AccessTime time.Time
	// CreateTime is the creation time, if recorded.
	CreateTime time.Time
	// Attributes is the block's type-specific attribute flags.
	Attributes uint32
	// OSID is the operating system identifier recorded in the block.
	OSID uint8
	// SetNumber is the backup data-set number this entry belongs to.
	SetNumber uint16
	// Volume is the name of the source volume/device the entry resides on.
	Volume string
	// FileID is the MTF object identifier of the file (files only).
	FileID uint32
	// DirID is the identifier of the directory containing the entry.
	DirID uint32
}

// TapeInfo holds metadata from the MTF TAPE descriptor block.
type TapeInfo struct {
	Software   string // generator software string
	Name       string // tape name
	Label      string // tape label
	MFMID      uint32
	Attributes uint32
	Sequence   uint16
	FLBSize    uint16 // logical block size used by the archive
	CreateTime time.Time
}

// SetInfo holds metadata from the MTF start-of-data-set (SSET) block.
type SetInfo struct {
	Name         string // set name
	Label        string // set label
	Owner        string // set owner/user
	Number       uint16 // data-set number
	Attributes   uint32
	Compression  uint16
	Encryption   uint16
	MajorVersion uint8
	MinorVersion uint8
	TimeZone     int8
	CreateTime   time.Time
}

// Block descriptor types (the first four bytes of a common descriptor block).
var (
	dbTAPE = [4]byte{'T', 'A', 'P', 'E'}
	dbSSET = [4]byte{'S', 'S', 'E', 'T'}
	dbVOLB = [4]byte{'V', 'O', 'L', 'B'}
	dbDIRB = [4]byte{'D', 'I', 'R', 'B'}
	dbFILE = [4]byte{'F', 'I', 'L', 'E'}
	dbCFIL = [4]byte{'C', 'F', 'I', 'L'}
	dbESPB = [4]byte{'E', 'S', 'P', 'B'}
	dbESET = [4]byte{'E', 'S', 'E', 'T'}
	dbEOTM = [4]byte{'E', 'O', 'T', 'M'}
	dbSFMB = [4]byte{'S', 'F', 'M', 'B'}
)

// Stream data type identifiers. These are the four-byte stream type codes read
// as little-endian uint32 values (e.g. "STAN" -> 0x4E415453). They are exported
// so callers can interpret the stream types associated with an entry.
const (
	StreamSTAN uint32 = 0x4E415453 // standard data
	StreamPNAM uint32 = 0x4D414E50 // path
	StreamFNAM uint32 = 0x4D414E46 // file name
	StreamCSUM uint32 = 0x4D555343 // checksum
	StreamCRPT uint32 = 0x54505243 // corrupt
	StreamSPAD uint32 = 0x44415053 // padding (marks the last stream of an object)
	StreamSPAR uint32 = 0x52415053 // sparse

	StreamTSMP uint32 = 0x504D5354 // set map, media based catalog, type 1
	StreamTFDD uint32 = 0x44444654 // fdd, media based catalog, type 1
	StreamMAP2 uint32 = 0x3250414D // set map, media based catalog, type 2
	StreamFDD2 uint32 = 0x32444446 // fdd, media based catalog, type 2

	StreamADAT uint32 = 0x54414441 // NT data
	StreamNTEA uint32 = 0x4145544E // NT extended attributes
	StreamNACL uint32 = 0x4C43414E // NT ACL
	StreamNTED uint32 = 0x4445544E // NT EData
	StreamNTQU uint32 = 0x5551544E // NT quota
	StreamNTPR uint32 = 0x5250544E // NT property
	StreamNTOI uint32 = 0x494F544E // NT object id

	StreamGERC uint32 = 0x43524547 // Win9x

	StreamN386 uint32 = 0x3638334E // Netware
	StreamNBND uint32 = 0x444E424E // Netware
	StreamSMSD uint32 = 0x44534D53 // Netware

	StreamOACL uint32 = 0x4C43414F // OS/2 ACL

	StreamMRSC uint32 = 0x4353524D // Macintosh resource
	StreamMPRV uint32 = 0x5652504D // Macintosh private
	StreamMINF uint32 = 0x464E494D // Macintosh info
)

// StreamTypeName returns a human-readable name for a stream data type
// identifier.
func StreamTypeName(t uint32) string {
	switch t {
	case StreamSTAN:
		return "STAN"
	case StreamPNAM:
		return "PNAM"
	case StreamFNAM:
		return "FNAM"
	case StreamCSUM:
		return "CSUM"
	case StreamCRPT:
		return "CRPT"
	case StreamSPAD:
		return "SPAD"
	case StreamSPAR:
		return "SPAR"
	case StreamTSMP:
		return "TSMP"
	case StreamTFDD:
		return "TFDD"
	case StreamMAP2:
		return "MAP2"
	case StreamFDD2:
		return "FDD2"
	case StreamADAT:
		return "ADAT"
	case StreamNTEA:
		return "NTEA"
	case StreamNACL:
		return "NACL"
	case StreamNTED:
		return "NTED"
	case StreamNTQU:
		return "NTQU"
	case StreamNTPR:
		return "NTPR"
	case StreamNTOI:
		return "NTOI"
	case StreamGERC:
		return "GERC"
	case StreamN386:
		return "N386"
	case StreamNBND:
		return "NBND"
	case StreamSMSD:
		return "SMSD"
	case StreamOACL:
		return "OACL"
	case StreamMRSC:
		return "MRSC"
	case StreamMPRV:
		return "MPRV"
	case StreamMINF:
		return "MINF"
	}
	return "UNKNOWN"
}
