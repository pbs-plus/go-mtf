// Package mtf provides a reader for the Microsoft Tape Format (MTF) used by
// NTBACKUP.EXE (.bkf) files and Windows tape backups.
//
// The implementation is a port of Ivo van Poorten / geocar's mtftar
// (https://github.com/geocar/mtftar) to idiomatic Go. It supports reading
// (listing and extracting file data) from MTF/BKF streams.
//
// Archives that span multiple physical media (tapes or .bkf files) are
// supported via [Reader.SetContinuation], which supplies the next medium when
// an End Of Tape Marker (EOTM) is encountered — whether between entries or in
// the middle of a file's data stream. See the MTF spec, section 8.
//
// The primary entry point is the [Reader]. [Reader.Next] advances entry by
// entry and transparently parses each object's data streams, materializing the
// metadata a faithful extraction needs (NTFS security descriptors, extended
// attributes, sparse maps) into the returned [Header] and positioning file
// content for [Reader.Read]:
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
//		// h.SecurityDescriptor holds the NTFS ACL (files and directories).
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
// archive. It is returned by [Reader.Next] via [Block.Header]. A single Header
// is reused across entries, so its fields are overwritten on the next call to
// Next; callers that retain an entry across iterations must copy the fields
// they need.
type Header struct {
	// Type is the kind of entry.
	Type EntryType
	// Name is the fully resolved path of the entry, formed from the source
	// volume, directory chain and the entry name using "/" separators.
	Name string
	// ModTime is the modification time of the entry.
	ModTime time.Time
	// AccessTime is the last access time, if recorded.
	AccessTime time.Time
	// CreateTime is the creation time, if recorded.
	CreateTime time.Time
	// BirthTime is the birth time of the entry, if recorded. Only emitted by
	// NT-based backups that populate the MTF birth-time field.
	BirthTime time.Time
	// Attributes is the block's type-specific attribute flags (the DBLK
	// attributes field that follows the common header).
	Attributes uint32
	// BlockAttributes is the MTF_DB_HDR common Block Attributes field, which
	// carries bits such as MTF_CONTINUATION and MTF_COMPRESSION. Use the
	// Attr* constants to test it.
	BlockAttributes uint32
	// OSID is the operating system identifier recorded in the block.
	OSID uint8
	// SetNumber is the backup data-set number this entry belongs to.
	SetNumber uint16
	// Volume is the name of the source volume/device the entry resides on.
	Volume string
	// VolumeLabel is the source volume's label, if recorded (volume entries).
	VolumeLabel string
	// MachineName is the name of the source machine, if recorded (volume
	// entries).
	MachineName string
	// FileID is the MTF object identifier of the file (files only).
	FileID uint32
	// DirID is the identifier of the directory containing the entry.
	DirID uint32

	// The following describe the file's standard (STAN) data stream and are
	// meaningful for file entries. They are populated by [Reader.Next].
	//
	// Size is the logical length in bytes of the file's content as delivered by
	// [Reader.Read]. For a plain file this is the STAN stream length; for a
	// sparse file it is the reconstructed (hole-filled) length; for a
	// compressed/encrypted file it is the on-media (stored) byte count (see
	// [Header.DisplayableSize] for the logical size in that case). It is zero
	// for non-file entries.
	Size int64
	// CompressionAlgorithm is the registered ID of the algorithm used to
	// compress the standard data stream, or zero if uncompressed.
	CompressionAlgorithm uint16
	// EncryptionAlgorithm is the registered ID of the algorithm used to
	// encrypt the standard data stream, or zero if unencrypted.
	EncryptionAlgorithm uint16
	// Compressed reports whether the standard data stream is compressed. The
	// bytes returned by [Reader.Read] are still compressed; decompression is
	// not performed.
	Compressed bool
	// Encrypted reports whether the standard data stream is encrypted. The
	// bytes returned by [Reader.Read] are still encrypted; decryption is not
	// performed.
	Encrypted bool
	// Sparse reports whether the file is sparse. For sparse files [Reader.Read]
	// transparently reconstructs the logical content (holes are zero-filled)
	// and [Header.SparseExtents] holds the parsed sparse map. The STREAM_IS_SPARSE
	// bit is documented in MTF spec section 6.1.
	Sparse bool
	// StreamChecksum is the checksum field of the standard (STAN) data stream
	// header (zero unless the stream is checksummed).
	StreamChecksum uint16
	// DisplayableSize is the object size recorded in the common descriptor
	// block's Displayable Size field. For uncompressed files it equals Size;
	// for compressed or sparse objects it reflects the logical (expanded) size.
	DisplayableSize uint64

	// SecurityDescriptor holds the raw NTFS security descriptor (NACL stream)
	// associated with the entry, if any. It is a self-relative security
	// descriptor as produced by the Win32 BackupRead API. Present on both file
	// and directory entries.
	SecurityDescriptor []byte
	// ExtendedAttributes holds the raw NT extended-attribute data (NTEA stream)
	// associated with the entry, if any.
	ExtendedAttributes []byte
	// SparseExtents describes the sparse layout of a sparse file (one entry per
	// SPAR stream), or nil for a non-sparse entry. Each extent carries the
	// non-hole bytes located at [SparseExtent.Offset] in the logical file;
	// [Reader.Read] fills the gaps with zero bytes. See MTF spec section 6.2.1.7.
	SparseExtents []SparseExtent
	// Streams holds every data stream associated with the entry that is not
	// carried by a named field above (e.g. NTOI object ids, NTQU quota, CSUM
	// checksums, ADAT alternate data, or vendor-specific streams). Each element
	// preserves the stream's four-byte type code and raw bytes so no metadata
	// is lost. Streams both preceding and following the standard (STAN) data
	// stream are captured. It is nil in [Reader.HeaderOnly] mode, which skips
	// stream data, and for entries that have no extra streams.
	Streams []StreamData
}

// SparseExtent describes one contiguous block of non-hole data within a sparse
// file, as parsed from a SPAR data stream. The logical file is reconstructed by
// placing each extent's Data at Offset and zero-filling the gaps between
// extents.
type SparseExtent struct {
	// Offset is the logical byte offset within the file where Data begins.
	Offset int64
	// Data is the non-hole byte content located at Offset.
	Data []byte
}

// StreamData holds the raw bytes of a data stream not otherwise exposed by a
// named field on [Header]. Type is the four-byte stream type code (compare
// against the Stream* constants, e.g. [StreamNTOI]).
type StreamData struct {
	Type uint32
	Data []byte
}

// TapeInfo holds metadata from the MTF TAPE descriptor block.
type TapeInfo struct {
	Software   string // generator software string
	Name       string // tape name
	Label      string // tape label
	Password   string // media password, if recorded
	MFMID      uint32 // media family id
	Attributes uint32 // TAPE attributes
	Sequence   uint16 // media sequence number within the media family
	FLBSize    uint16 // logical block size used by the archive
	// PasswordAlgorithm is the registered ID of the password-encryption
	// algorithm used to protect the media password, or zero.
	PasswordAlgorithm uint16
	// SoftFilemarkBlockSize is the soft filemark (SFMB) block size in units of
	// 512 bytes; only meaningful when soft filemarks are used.
	SoftFilemarkBlockSize uint16
	// CatalogType is the Media Based Catalog format type recorded on the tape.
	CatalogType uint16
	// SoftwareVendorID is the registered vendor ID of the writing software.
	SoftwareVendorID uint16
	// MTFMajorVersion is the MTF major revision recorded in the TAPE block.
	MTFMajorVersion uint8
	CreateTime      time.Time
}

// SetInfo holds metadata from the MTF start-of-data-set (SSET) block.
type SetInfo struct {
	Name   string // set name
	Label  string // set label
	Owner  string // set owner/user
	Number uint16 // data-set number
	// Password is the data-set password, if recorded.
	Password string
	// PBA is the physical block address of the SSET block, used for seek
	// indexing in conjunction with a Media Based Catalog.
	PBA uint64
	// SoftwareVendorID is the registered vendor ID of the writing software.
	SoftwareVendorID uint16
	// SoftwareVersion is the writer's software version number.
	SoftwareVersion uint16
	Attributes      uint32
	Compression     uint16
	Encryption      uint16
	MajorVersion    uint8
	MinorVersion    uint8
	TimeZone        int8
	CreateTime      time.Time
}

// ESetInfo holds metadata from the most recent end-of-data-set (ESET) block.
// It is available via [Reader.ESet] after a data set has ended.
type ESetInfo struct {
	Attributes       uint32 // ESET block attributes
	CorruptObjects   uint32 // number of corrupt files in the data set
	FDDMediaSequence uint16 // FDD media sequence number
	SetNumber        uint16 // data-set number being closed
	CreateTime       time.Time
}

// Block is a single structural element yielded by [Reader.Next]. Its Kind
// discriminates which field is populated. A single Block (and Header) are
// reused across calls to Next, so all fields are overwritten on the next call;
// callers should copy any values they need to retain.
type Block struct {
	Kind    BlockKind
	Tape    *TapeInfo // populated when Kind == KindMedia
	Set     *SetInfo  // populated when Kind == KindSet
	Header  *Header   // populated when Kind == KindEntry
	ESet    *ESetInfo // populated when Kind == KindSetEnd
	Catalog *Catalog  // populated when Kind == KindSetEnd (nil if the set had no catalog)
}

// BlockKind identifies the kind of MTF structure a [Block] represents.
type BlockKind uint8

const (
	// KindMedia is yielded for an MTF_TAPE descriptor block: the start of a
	// physical medium. With media spanning enabled, one iteration yields a
	// KindMedia block for each medium as it is consumed.
	KindMedia BlockKind = iota
	// KindSet is yielded for an MTF_SSET descriptor block: the start of a data
	// set (one backup operation).
	KindSet
	// KindEntry is yielded for an extractable object descriptor (MTF_VOLB,
	// MTF_DIRB or MTF_FILE). The header is fully materialized; call [Reader.Read]
	// to stream the entry's standard data.
	KindEntry
	// KindSetEnd is yielded for an MTF_ESET descriptor block: a data set ended.
	// Any Media Based Catalog carried by the set's streams is attached as
	// Catalog (nil when the set recorded no catalog).
	KindSetEnd
)

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

// Common Block Attributes (MTF_DB_HDR Block Attributes field, offset 4).
// These apply to the common header of any descriptor block.
const (
	// AttrContinuation (MTF_CONTINUATION, BIT0) is set on descriptor blocks that
	// are repeated on a continuation medium to restore context after an End of
	// Media (EOTM). See MTF spec section 8 (End Of Media Processing).
	AttrContinuation uint32 = 0x00000001
	// AttrCompression indicates compression may be active.
	AttrCompression uint32 = 0x00000002
	// AttrEOSAtEOM indicates End Of Medium was hit during end-of-set processing.
	AttrEOSAtEOM uint32 = 0x00000004
)

// Stream File System Attributes (MTF_STREAM_HDR Stream File System
// Attributes field, stream header offset 4).
const (
	// StreamFSSparse (STREAM_IS_SPARSE, BIT3) marks a stream whose data is
	// sparse. See MTF spec section 6.1.
	StreamFSSparse uint16 = 0x0008
)

// Stream Media Format Attributes (MTF_STREAM_HDR Stream Media Format
// Attributes field, stream header offset 6).
const (
	// StreamMediaContinue (STREAM_CONTINUE, BIT0) marks a stream whose data is a
	// continuation of a stream split across media at EOM. Its Stream Length holds
	// only the remaining (unwritten) portion and its data begins at the next
	// Format Logical Block boundary. See MTF spec section 6.1.
	StreamMediaContinue uint16 = 0x0001
	// StreamMediaEncrypted (STREAM_ENCRYPTED, BIT3) marks an encrypted stream.
	StreamMediaEncrypted uint16 = 0x0008
	// StreamMediaCompressed (STREAM_COMPRESSED, BIT4) marks a compressed stream.
	StreamMediaCompressed uint16 = 0x0010
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
	StreamSM2P uint32 = 0x32504D53 // set map, media based catalog (variant)

	StreamADAT uint32 = 0x54414441 // NT data
	StreamNTEA uint32 = 0x4145544E // NT extended attributes
	StreamNACL uint32 = 0x4C43414E // NT ACL
	StreamNTED uint32 = 0x4445544E // NT EData
	StreamNTQU uint32 = 0x5551544E // NT quota
	StreamNTPR uint32 = 0x5250544E // NT property
	StreamNTOI uint32 = 0x494F544E // NT object id
	StreamNTQP uint32 = 0x5051544E // NT quota/property (vendor variant)

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
	case StreamSM2P:
		return "SM2P"
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
	case StreamNTQP:
		return "NTQP"
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
