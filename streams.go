package mtf

import (
	"io"
)

// materializeStreams walks the data streams of the current descriptor block,
// collecting the metadata streams that matter for faithful extraction into h
// and locating the standard data stream (STAN). When STAN is reached the reader
// is positioned at its content and the function returns; h.Size and the
// stream-derived flags are populated. If no STAN is present (e.g. directory or
// volume entries) the walk consumes all streams up to the terminal padding
// stream (SPAD).
//
// For a sparse file the STAN header carries the STREAM_IS_SPARSE bit and a
// length of zero; the actual content is carried by one or more following SPAR
// streams (MTF spec section 6.2.1.7). Those are collected into h.SparseExtents
// and the logical size is computed from them.
func (r *Reader) materializeStreams(h *Header) error {
	for {
		switch r.streamType {
		case StreamSPAD:
			r.lastStream = true
			return nil
		case StreamNACL:
			if r.headerOnly {
				break
			}
			b, err := r.readStreamBytes(r.streamLen)
			if err != nil {
				return err
			}
			h.SecurityDescriptor = b
		case StreamNTEA:
			if r.headerOnly {
				break
			}
			b, err := r.readStreamBytes(r.streamLen)
			if err != nil {
				return err
			}
			h.ExtendedAttributes = b
		case StreamSPAR:
			if r.headerOnly {
				break
			}
			b, err := r.readStreamBytes(r.streamLen)
			if err != nil {
				return err
			}
			h.SparseExtents = append(h.SparseExtents, parseSparseExtent(b))
		case StreamSTAN:
			h.CompressionAlgorithm = r.streamCompAlgo
			h.EncryptionAlgorithm = r.streamEncAlgo
			h.Compressed = r.streamMediaAttr&StreamMediaCompressed != 0
			h.Encrypted = r.streamMediaAttr&StreamMediaEncrypted != 0
			h.StreamChecksum = r.streamChecksum
			// A sparse file is signalled by STREAM_IS_SPARSE on a STAN header
			// whose Stream Length is zero; its content is carried by following
			// SPAR streams (MTF spec section 6.2.1.7).
			if r.streamSysAttr&StreamFSSparse != 0 && r.streamLen == 0 {
				h.Sparse = true
				r.sparse = true
			} else {
				r.inData = true
				r.dataRem = r.streamLen
				h.Size = r.streamLen
				return nil
			}
		}
		if r.lastStream {
			return nil
		}
		if err := r.streamNext(); err != nil {
			return err
		}
	}
}

// finishSparse computes the logical (hole-filled) size of a sparse file from
// its collected extents. It is called after materializeStreams returns for a
// sparse entry.
func (r *Reader) finishSparse(h *Header) {
	for _, e := range h.SparseExtents {
		if end := e.Offset + int64(len(e.Data)); end > h.Size {
			h.Size = end
		}
	}
}

// readStreamBytes reads exactly n bytes of the current stream's data into a
// freshly allocated slice, advancing the stream and block accounting. Stream
// data flows continuously across logical-block boundaries (it is not capped to
// the FLB size), so this reads straight through. Metadata streams never span
// media, so no EOTM probing is performed.
func (r *Reader) readStreamBytes(n int64) ([]byte, error) {
	buf := make([]byte, n)
	nr, err := r.readFull(buf)
	if nr > 0 {
		r.flbread += uint32(nr)
		r.abspos += int64(nr)
		r.streamDid += int64(nr)
	}
	r.wrapFlbread()
	return buf[:nr], err
}

// parseSparseExtent parses one SPAR stream's data into a SparseExtent. A SPAR
// stream begins with an 8-byte Sparse Frame Header holding the logical offset
// of the following data (MTF spec, Structure 17); the remainder is the
// non-hole byte content at that offset.
func parseSparseExtent(b []byte) SparseExtent {
	if len(b) < 8 {
		return SparseExtent{Data: append([]byte(nil), b...)}
	}
	return SparseExtent{
		Offset: int64(u64(b, 0)),
		Data:   append([]byte(nil), b[8:]...),
	}
}

// readSparse reconstructs the logical content of a sparse file into p: it
// zero-fills the holes between extents and copies each extent's data at its
// offset. extents are emitted in offset order.
func (r *Reader) readSparse(p []byte) (int, error) {
	total := 0
	extents := r.cur.SparseExtents
	for total < len(p) {
		if r.sparseIdx >= len(extents) {
			return total, io.EOF
		}
		ext := extents[r.sparseIdx]

		if r.sparseCursor < ext.Offset {
			n := min(int(ext.Offset-r.sparseCursor), len(p)-total)
			clearZero(p[total : total+n])
			total += n
			r.sparseCursor += int64(n)
			continue
		}

		avail := int64(len(ext.Data)) - r.sparsePos
		if avail > 0 {
			n := min(int(avail), len(p)-total)
			copy(p[total:total+n], ext.Data[r.sparsePos:int(r.sparsePos)+n])
			total += n
			r.sparsePos += int64(n)
			r.sparseCursor += int64(n)
		}
		if r.sparsePos >= int64(len(ext.Data)) {
			r.sparseIdx++
			r.sparsePos = 0
		}
	}
	return total, nil
}

func clearZero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
