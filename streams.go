package mtf

import (
	"errors"
	"io"
)

// Stream describes a single data stream of the current entry, as returned by
// [Reader.NextStream].
//
// MTF stores each object (file, directory, volume) as a sequence of typed
// streams following its descriptor block. The standard data stream (STAN)
// carries the file's bytes; the remaining stream types carry metadata such as
// NTFS security descriptors (NACL), extended attributes (NTEA), object IDs
// (NTOI), sparse maps (SPAR) and alternate-data streams (ADAT). A terminal
// padding stream (SPAD) marks the end of the sequence.
//
// Use [Reader.Read] to read the bytes of the stream most recently returned by
// NextStream.
type Stream struct {
	// Type is the 4-byte stream data type identifier (one of the Stream*
	// constants, e.g. [StreamSTAN], [StreamNACL]).
	Type uint32
	// Length is the number of data bytes carried by the stream.
	Length int64
	// SystemAttributes is the stream's file-system attribute flags
	// (e.g. [StreamFSSparse]).
	SystemAttributes uint16
	// MediaAttributes is the stream's media-format attribute flags
	// (e.g. [StreamMediaCompressed], [StreamMediaEncrypted]).
	MediaAttributes uint16
	// CompressionAlgorithm is the registered ID of the algorithm used to
	// compress the stream's data, or zero if uncompressed.
	CompressionAlgorithm uint16
	// EncryptionAlgorithm is the registered ID of the algorithm used to
	// encrypt the stream's data, or zero if unencrypted.
	EncryptionAlgorithm uint16
	// Checksum is the stream's checksum field, or zero if not checksummed.
	Checksum uint16
}

// EnumerateStreams configures the reader to expose every data stream of each
// entry via [Reader.NextStream], rather than auto-positioning file entries at
// their standard data stream.
//
// By default (EnumerateStreams not called, or called with false), [Reader.Next]
// positions a file entry at its STAN stream and pre-populates [Header.Size] and
// the stream-derived flags ([Header.Compressed], [Header.Encrypted],
// [Header.Sparse], [Header.CompressionAlgorithm], [Header.EncryptionAlgorithm],
// [Header.StreamChecksum]); directory and volume entries expose their streams
// via NextStream but are not otherwise special.
//
// When enabled, Next does not seek file entries to STAN, so the full stream
// sequence (including metadata streams that precede STAN, such as NACL/NTEA)
// is reachable through NextStream. In this mode Header.Size and the
// stream-derived flags are zero on the Header returned by Next; obtain them
// from the [Stream] whose Type is [StreamSTAN] instead.
//
// EnumerateStreams must be called before the first call to [Reader.Next].
func (r *Reader) EnumerateStreams(enable bool) {
	r.streamMode = enable
}

// startEntryStreams positions the reader at the first data stream of the
// descriptor block currently held in r.blk. It is used by entries (volume,
// directory) whose streams are not otherwise consumed during Next. It returns
// true when the object has streams (the reader is positioned at the first
// one), or false when the object carries no streams (the block has already
// been skipped and the entry should be reported as finished).
func (r *Reader) startEntryStreams() (bool, error) {
	r.lastStream = false
	r.streamPrimed = false
	off := uint32(u16(r.blk, dbOffOff))
	if off == 0 {
		// No streams recorded for this object: advance past the block.
		if err := r.scanNext(); err != nil {
			return false, err
		}
		return false, nil
	}
	if err := r.streamStart(); err != nil {
		// No streams reachable: advance past the block.
		if e2 := r.scanNext(); e2 != nil {
			return false, e2
		}
		return false, nil
	}
	return true, nil
}

// NextStream advances to the next data stream of the current entry (the entry
// most recently returned by [Reader.Next]) and returns a description of it. It
// returns io.EOF when all of the entry's streams have been enumerated.
//
// After a successful NextStream, the stream's bytes are available through
// [Reader.Read]. For a file whose standard data stream spans continuation
// media, Read transparently follows the spanning just as it does in the default
// mode.
//
// Calling Read on an entry before any NextStream is permitted only in the
// default mode (where it selects the standard data stream). In stream mode,
// call NextStream before Read.
//
// NextStream consumes (skips) any unread bytes of the stream it is leaving.
func (r *Reader) NextStream() (*Stream, error) {
	if r.cur == nil || r.entryDone {
		return nil, io.EOF
	}
	r.streamModeActive = true

	if !r.streamPrimed {
		// The first call after Next: the first stream header is already loaded.
		r.streamPrimed = true
	} else {
		if r.streamType == StreamSPAD || r.lastStream {
			return nil, io.EOF
		}
		if err := r.streamNext(); err != nil {
			return nil, err
		}
		if r.streamType == StreamSPAD || r.lastStream {
			r.lastStream = true
			return nil, io.EOF
		}
	}

	r.streamDid = 0
	r.streamRemain = r.streamLen
	return &Stream{
		Type:                 r.streamType,
		Length:               r.streamLen,
		SystemAttributes:     r.streamSysAttr,
		MediaAttributes:      r.streamMediaAttr,
		CompressionAlgorithm: r.streamCompAlgo,
		EncryptionAlgorithm:  r.streamEncAlgo,
		Checksum:             r.streamChecksum,
	}, nil
}

// readCurrentStream reads up to len(p) bytes of the stream most recently
// selected by NextStream (or STAN, after a lazy seek). It is the spanning-aware
// reader for an arbitrary current stream.
func (r *Reader) readCurrentStream(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for total < len(p) && r.streamRemain > 0 {
		if r.streamType == StreamSTAN && r.atFLBBoundary() {
			if err := r.probeEOTM(); err != nil {
				if errors.Is(err, errSpanned) {
					if err := r.advanceToContinuationStream(); err != nil {
						return total, err
					}
					r.streamRemain = r.streamLen
					continue
				}
				return total, err
			}
		}

		want := min(r.streamRemain, int64(len(p)-total))
		if r.streamType == StreamSTAN {
			if dist := r.distToBoundary(); dist > 0 && dist < want {
				want = dist
			}
		}
		if want == 0 {
			want = r.streamRemain
		}
		nr, err := r.readFull(p[total : total+int(want)])
		if nr > 0 {
			r.flbread += uint32(nr)
			r.abspos += int64(nr)
			r.streamDid += int64(nr)
			r.streamRemain -= int64(nr)
			total += nr
		}
		if err != nil {
			return total, err
		}
	}
	r.wrapFlbread()
	return total, nil
}
