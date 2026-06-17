package mtf

import (
	"errors"
	"io"
)

// CartridgeRole classifies the role a medium plays within its Media Family,
// inferred from the catalog type recorded on its TAPE block.
type CartridgeRole int

const (
	// RoleUnknown is used when no TAPE block was seen or the catalog type is
	// unrecognized.
	RoleUnknown CartridgeRole = iota
	// RoleData is a cartridge that primarily carries file data
	// (Backup Exec CatalogType 64).
	RoleData
	// RoleCatalog is a consolidated catalog cartridge
	// (Backup Exec CatalogType 128).
	RoleCatalog
)

// Census summarizes the shape of a single cartridge (one MTF stream, i.e. one
// .bkf file or one tape) at a glance, without reading file content. It is
// produced by [Reader.Census], which walks every block but drains file data
// streams instead of delivering their bytes, so it is suitable for cheaply
// classifying large archives.
//
// Census reads every block header and stream descriptor, so its file/byte
// counts are authoritative for the cartridge; only the content bytes are not
// materialized.
type Census struct {
	// Tape is the metadata from the cartridge's TAPE block. It is nil when the
	// stream did not begin with a TAPE block (for example a bare continuation).
	Tape *TapeInfo
	// Set is the metadata from the data-set start (SSET) block, or nil.
	Set *SetInfo
	// Role is the inferred cartridge role.
	Role CartridgeRole
	// CatalogType is the catalog type recorded on the TAPE block
	// (0 when absent).
	CatalogType uint16
	// MediaSequence is the media sequence number (1-based within a family).
	// Derived from the TAPE Sequence field; a standalone cartridge reports 1.
	MediaSequence uint16
	// SetsClosed is the number of ESET blocks seen (data sets that end on this
	// cartridge). A mid-span continuation with no data-set end reports 0.
	SetsClosed int
	// HasCatalog reports whether any end-of-set block carried catalog streams
	// (Media Based Catalog: TFDD/FDD2/TSMP/MAP2).
	HasCatalog bool
	// CatalogBytes is the total catalog payload captured (TFDD + FDD2 +
	// SetMap), regardless of whether a standard parser could decode it.
	CatalogBytes int64

	// Volumes is the number of volume (VOLB) entries.
	Volumes int
	// Directories is the number of directory (DIRB) entries.
	Directories int
	// Files is the number of file (FILE) entries, including empty ones.
	Files int
	// EmptyFiles is the number of file entries with no data stream.
	EmptyFiles int
	// FileBytes is the sum of every file's on-media (stored) data size. For
	// uncompressed cartridges this equals the logical size; for compressed or
	// sparse ones it reflects the stored byte count.
	FileBytes int64
	// SparseFiles is the number of sparse file entries.
	SparseFiles int
	// CompressedFiles is the number of file entries whose data stream is
	// compressed.
	CompressedFiles int
	// EncryptedFiles is the number of file entries whose data stream is
	// encrypted.
	EncryptedFiles int
}

// HasData reports whether the cartridge carries any file content.
func (c *Census) HasData() bool { return c.Files > c.EmptyFiles }

// Census walks the cartridge, classifying it into a [Census] (returned by
// value, so it need not heap-allocate). File content is discarded rather than
// returned. It consumes the reader: after it returns the stream is exhausted
// (or stopped at the first read error). Use a fresh reader to extract content.
//
// Census runs in header-only mode, so it performs zero per-entry allocations;
// the only allocations are the [Reader] itself and its block buffer.
//
// Even when err is non-nil, the returned Census is populated with whatever was
// read before the error; this lets a caller classify partially readable
// cartridges.
func (r *Reader) Census() (Census, error) {
	r.HeaderOnly()
	var c Census
	for {
		blk, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return c, err
		}
		switch blk.Kind {
		case KindMedia:
			c.Tape = blk.Tape
			c.CatalogType = blk.Tape.CatalogType
			c.Role = classifyRole(blk.Tape.CatalogType)
			if blk.Tape.Sequence > 0 {
				c.MediaSequence = blk.Tape.Sequence
			} else {
				c.MediaSequence = 1
			}
		case KindSet:
			c.Set = blk.Set
		case KindSetEnd:
			c.SetsClosed++
			if blk.Catalog != nil {
				if len(blk.Catalog.RawFDD) > 0 || len(blk.Catalog.RawSetMap) > 0 {
					c.HasCatalog = true
				}
				c.CatalogBytes += int64(len(blk.Catalog.RawFDD))
				c.CatalogBytes += int64(len(blk.Catalog.RawSetMap))
			}
		case KindEntry:
			h := blk.Header
			switch h.Type {
			case EntryVolume:
				c.Volumes++
			case EntryDirectory:
				c.Directories++
			case EntryFile:
				c.Files++
				if h.Sparse {
					c.SparseFiles++
				}
				if h.Compressed {
					c.CompressedFiles++
				}
				if h.Encrypted {
					c.EncryptedFiles++
				}
				if h.Size == 0 {
					c.EmptyFiles++
				}
				c.FileBytes += h.Size
			}
		}
	}
	return c, nil
}

// classifyRole maps a catalog type to a cartridge role. Backup Exec records 64
// for data media and 128 for consolidated catalog media; other values fall back
// to RoleUnknown so the caller can decide.
func classifyRole(catalogType uint16) CartridgeRole {
	switch catalogType {
	case 64:
		return RoleData
	case 128:
		return RoleCatalog
	default:
		return RoleUnknown
	}
}
