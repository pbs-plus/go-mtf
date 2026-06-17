package mtf

import (
	"errors"
	"io"
)

// errSpanned signals that probeEOTM detected a genuine End Of Media boundary
// in the middle of a file's data stream.
var errSpanned = errors.New("mtf: stream spans to continuation medium")

// atFLBBoundary reports whether the reader is positioned exactly at a Format
// Logical Block boundary (all consumed bytes of the current block are spent).
func (r *Reader) atFLBBoundary() bool {
	if r.flbsize == 0 {
		return false
	}
	return r.flbread == 0 || r.flbread >= r.flbsize
}

// distToBoundary returns the number of bytes that can be read before the next
// Format Logical Block boundary. At a boundary this is flbsize (a full block).
func (r *Reader) distToBoundary() int64 {
	if r.flbsize == 0 {
		return 1 << 62
	}
	pos := r.flbread % r.flbsize
	if pos == 0 {
		return int64(r.flbsize)
	}
	return int64(r.flbsize - pos)
}

// probeEOTM checks, without consuming stream data, whether the bytes
// immediately ahead form an MTF_EOTM descriptor block. Such a block at a
// Format Logical Block boundary, while a stream still has data remaining,
// indicates the stream is split across media.
//
// It returns nil if the next bytes are stream data (the probe bytes are
// returned to the read-ahead buffer for normal delivery), or errSpanned if a
// valid EOTM block is present (the block is consumed).
func (r *Reader) probeEOTM() error {
	const probeSize = 52 // size of a common descriptor block header
	var hdr [probeSize]byte
	n, err := r.readFull(hdr[:])
	if n == probeSize {
		if blockType(hdr[:]) == dbEOTM && u64(hdr[:], dbFLAOff) == 0 && u32(hdr[:], dbCBIDOff) == 0 {
			return errSpanned
		}
	}
	// Not an EOTM: hand the probed bytes back as stream data.
	r.peek = append(r.peek[:0], hdr[:n]...)
	if err != nil {
		return err
	}
	return nil
}

// SetContinuation registers a function that supplies the next physical medium
// when the current one ends (an MTF_EOTM block is reached).
//
// This enables reading a single backup data set that spans multiple media
// (tapes or .bkf files). When the reader encounters an End Of Tape Marker
// (EOTM) — whether between entries or in the middle of a file's data stream —
// it calls next with a [Continuation] describing the medium that just ended,
// and resumes from the reader next returns.
//
// The callback is the natural place to prompt an operator to load the next
// tape, e.g.:
//
//	files := []string{"tape-1.bkf", "tape-2.bkf"}
//	r.SetContinuation(func(c mtf.Continuation) (io.Reader, error) {
//	    if c.Sequence >= len(files) {
//	        return nil, io.EOF // no more media
//	    }
//	    fmt.Printf("load %s (tape %d)\n", files[c.Sequence], c.Sequence+1)
//	    return os.Open(files[c.Sequence])
//	})
//
// If next is nil (the default), an EOTM ends the archive like io.EOF. If next
// returns io.EOF or a nil reader, the archive ends.
func (r *Reader) SetContinuation(next func(Continuation) (io.Reader, error)) {
	r.nextMedia = next
}

// switchMedium obtains and installs the continuation medium. It returns true if
// a new medium was provided, or false if no continuation is available (which
// means the archive has ended).
func (r *Reader) switchMedium() bool {
	if r.nextMedia == nil {
		return false
	}
	c := Continuation{
		Sequence: r.mediaSeq + 1, // the medium that just ended is the current one
	}
	if r.hasTape {
		c.Media = &r.tape
	}
	nr, err := r.nextMedia(c)
	if err != nil || nr == nil {
		return false
	}
	// Discard any pending read-ahead belonging to the previous medium.
	r.peek = r.peek[:0]
	r.r = nr
	r.abspos = 0 // new medium starts at its own offset 0
	// Re-bind seeking to the new medium, if it supports it; otherwise disable
	// seeking so data skips fall back to reading.
	if s, ok := nr.(io.Seeker); ok {
		r.seeker = s
	} else {
		r.seeker = nil
	}
	r.flbsize = 0 // re-discovered from the continuation TAPE block
	r.mediaSeq++
	return true
}

// advanceToContinuationStream is invoked when a file's data stream is
// interrupted by EOTM mid-read. It switches to the continuation medium and
// re-synchronizes onto the continuation FILE block's STAN stream, leaving the
// reader positioned to deliver the remaining data. It returns nil on success,
// io.EOF if no continuation medium is available, or another error.
func (r *Reader) advanceToContinuationStream() error {
	for {
		if !r.switchMedium() {
			r.hitEOTM = true
			return io.EOF
		}
		if err := r.resyncContinuation(); err != nil {
			if errors.Is(err, errNoContinuation) {
				continue // medium had no continuation FILE; try the next one
			}
			return err
		}
		return nil
	}
}

// errNoContinuation signals that a continuation medium did not contain the
// expected continuation FILE block (it may be an inter-set continuation, or the
// set may have fully ended on the previous medium).
var errNoContinuation = errors.New("mtf: no continuation file on continuation medium")

// resyncContinuation walks the continuation medium's leading blocks: the
// continuation TAPE header and the repeated (continuation-bit) SSET/VOLB/DIRB
// blocks that restore context, then locates the continuation FILE block. It
// parses that FILE's STAN stream and positions the reader to deliver its data.
func (r *Reader) resyncContinuation() error {
	for {
		if err := r.scanStart(); err != nil {
			return err
		}
		bt := blockType(r.blk)

		switch bt {
		case dbTAPE:
			if err := r.ensure(tapeFLBSizeOff + 2); err != nil {
				return err
			}
			r.flbsize = uint32(u16(r.blk, tapeFLBSizeOff))
		case dbSFMB, dbESPB:
			// Soft filemark / end-of-set padding emulation: ignore.
		case dbSSET:
			// Continuation set marker (same data set): refresh set metadata.
			if err := r.parseSet(); err != nil {
				return err
			}
		case dbVOLB:
			if _, err := r.restoreVolb(); err != nil {
				return err
			}
		case dbDIRB:
			if _, err := r.restoreDirb(); err != nil {
				return err
			}
		case dbFILE:
			if err := r.continuationFile(); err != nil {
				return err
			}
			return nil
		case dbEOTM:
			// This medium is itself exhausted without a continuation file.
			return errNoContinuation
		default:
			// Any unexpected block: skip it.
		}

		if err := r.scanNext(); err != nil {
			return err
		}
	}
}

// continuationFile parses a FILE block found on a continuation medium and
// positions the reader at its STAN data stream so the in-progress entry can
// continue receiving data. The stream's declared length is the remaining
// (unwritten) portion; if STREAM_CONTINUE is set the data begins at the next
// Format Logical Block boundary rather than immediately after the header.
func (r *Reader) continuationFile() error {
	if err := r.streamStart(); err != nil {
		return err
	}
	// Skip any non-STAN streams preceding the data (mirrors normal FILE handling).
	for r.streamType != StreamSTAN &&
		r.streamType != StreamSPAD &&
		!r.lastStream {
		if err := r.streamNext(); err != nil {
			return err
		}
	}
	if r.streamType != StreamSTAN {
		// No data stream on the continuation: the file's data ended on the
		// previous medium. Nothing more to deliver.
		r.inData = false
		r.dataRem = 0
		return nil
	}

	r.inData = true
	r.streamDid = 0
	r.dataRem = r.streamLen
	r.streamContinued = true

	// STREAM_CONTINUE streams place their data at the next FLB boundary.
	if r.streamMediaAttr&StreamMediaContinue != 0 && r.flbsize > 0 {
		if err := r.scanNext(); err != nil {
			return err
		}
	}
	return nil
}
