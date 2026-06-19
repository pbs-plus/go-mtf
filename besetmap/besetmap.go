// Package besetmap parses the Backup Exec proprietary Set Map stream.
//
// The MTF specification (§7.3.3) defines the standard Set Map with stream IDs
// 'TSMP' (Type 1) and 'MAP2' (Type 2), and the main go-mtf package parses those.
// Backup Exec (Veritas) instead writes a Set Map whose four-byte stream ID is
// 'SMP2' (bytes 53 4D 50 32, 0x32504D53 read little-endian). The MTF_100a
// specification does not mention 'SMP2' and no Backup Exec public reference
// documenting it has been located; this parser is therefore derived from
// empirical observation of produced media, not a vendor specification.
//
// The 'SMP2' payload uses the same Set Map header and Set Map Entry fixed
// fields as the spec, with one material difference: each entry's Number-Of-
// Volumes volume records follow the entry as separate records in the stream
// rather than being nested inside the entry's declared LENGTH.
//
// This package registers its parser with [mtf.RegisterSetMapParser] for the
// 'SMP2' stream ID on init, so importing it (usually as a blank import) opts
// the program into Backup Exec Set Map support:
//
//	import _ "github.com/pbs-plus/go-mtf/besetmap"
//
// After that import, [mtf.Reader.Catalog] and [mtf.ReadSetMap] return fully
// populated Set Maps from Backup Exec media. Without the import, the main
// package's spec-only parser is used as a best effort.
package besetmap

import (
	"encoding/binary"

	mtf "github.com/pbs-plus/go-mtf"
)

// StreamSMP2 is the four-byte Backup Exec Set Map stream ID, 'SMP2' (the ASCII
// bytes read as a little-endian uint32). It equals mtf.StreamSM2P / mtf.StreamSMP2.
const StreamSMP2 = mtf.StreamSMP2

func init() {
	mtf.RegisterSetMapParser(StreamSMP2, mtf.SetMapParserFunc(Parse))
}

// Parse decodes a Backup Exec 'SMP2' Set Map stream payload (the bytes after
// the 22-byte stream header) into an *mtf.SetMap. It reads the standard Set Map
// header, then for each entry decodes the fixed fields via the shared
// [mtf.ParseSetMapEntryFixed] helper and reads the entry's declared
// Number-Of-Volumes volume records as the following records in the stream.
// It is read-only and tolerant of truncation.
func Parse(raw []byte) *mtf.SetMap {
	mfm, count := mtf.ParseSetMapHeader(raw)
	sm := &mtf.SetMap{MediaFamilyID: mfm}
	off := mtf.SetMapHeaderSize
	for i := range count {
		entry, length, ok := mtf.ParseSetMapEntryFixed(raw[off:])
		if !ok {
			break
		}
		if off+length > len(raw) {
			break
		}
		off += length
		// The entry's declared numVol volume records follow as separate records.
		for v := 0; v < entry.NumVolumes && off+mtf.FDDCommonHeaderSize <= len(raw); v++ {
			vlen := int(binary.LittleEndian.Uint16(raw[off+mtf.FDDRecordLenOff:]))
			if vlen < mtf.FDDCommonHeaderSize || off+vlen > len(raw) {
				break
			}
			strType := raw[off+mtf.FDDRecordStrTypeOff]
			ve := mtf.ParseFDDVolume(raw[off:off+vlen], strType)
			entry.Volumes = append(entry.Volumes, *ve)
			off += vlen
		}
		sm.Entries = append(sm.Entries, entry)
		if off+mtf.SetMapHeaderSize > len(raw) && i+1 < count {
			break
		}
	}
	return sm
}
