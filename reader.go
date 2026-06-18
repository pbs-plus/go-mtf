package mtf

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// Reader provides sequential access to the entries of an MTF/BKF stream.
//
// Use [Next] to advance to the next entry and [Read] to read the standard data
// of the current file entry. The API intentionally mirrors archive/tar.
// BlockSkipper is implemented by sources that can advance the read position by
// a byte count without transferring the skipped data to the host — most
// importantly a SCSI-generic tape reader that uses a block LOCATE command. When
// the source implements it, the Reader skips file data via LOCATE instead of
// reading it, the fast-retrieval path MTF is designed for (spec §3.4.3). The
// method is matched by name so a concrete type (e.g. gotape.Reader) satisfies it
// without this package importing it.
type BlockSkipper interface {
	SkipForward(n int64) error
}

type Reader struct {
	r            io.Reader
	seeker       io.Seeker    // non-nil when the source supports seeking; skipStreamData seeks
	blockSkipper BlockSkipper // non-nil when the source supports block LOCATE (e.g. SCSI tape); skipStreamData skips via LOCATE
	closer       io.Closer

	blk     []byte // header / stream-header buffer for the current block
	flbsize uint32 // logical block size (from the TAPE descriptor)
	flbread uint32 // bytes consumed within the current logical block
	abspos  int64  // absolute position in the underlying stream
	strType uint8  // string encoding for the current block

	streamOff       uint32 // offset of the current stream header within blk
	streamLen       int64  // total length of the current stream's data
	streamDid       int64  // bytes of current stream data consumed so far
	streamType      uint32 // current stream data type
	streamSysAttr   uint16 // current stream's file system attributes
	streamMediaAttr uint16 // current stream's media format attributes
	streamEncAlgo   uint16 // current stream's data encryption algorithm
	streamCompAlgo  uint16 // current stream's data compression algorithm
	streamChecksum  uint16 // current stream's checksum field
	lastStream      bool   // a SPAD stream has been seen (end of object streams)
	streamContinued bool   // current data stream is a cross-media continuation

	decryptor Decryptor // optional cipher for encrypted streams (§6.5)
	dec       *decoder  // active compressed/encrypted-stream decoder

	cur       *Header
	inData    bool  // positioned within the file's STAN data stream
	dataRem   int64 // remaining bytes of the file's STAN data
	entryDone bool  // current entry has been fully consumed

	// sparse reconstruction state (when the current STAN is STREAM_IS_SPARSE)
	sparse       bool  // current entry's content is sparse
	sparseIdx    int   // current SparseExtent index
	sparsePos    int64 // offset within the current extent's Data
	sparseCursor int64 // logical offset produced so far

	volume string
	cwd    string
	cwdID  uint32

	nextMedia func(Continuation) (io.Reader, error) // supplies the next physical medium
	mediaSeq  int                                   // number of continuation media consumed
	peek      []byte                                // read-ahead bytes pending delivery (probe buffer)

	// hitEOTM is set when an End-Of-Tape-Media marker is encountered but no
	// continuation medium is registered, meaning the logical stream was
	// truncated. Exposed via TruncatedByEOTM so callers can warn the user.
	hitEOTM bool

	// headerOnly skips the *data* of metadata streams (NACL/NTEA/SPAR) instead of
	// reading them into the Header, and leaves STAN content undelivered. Set by
	// [Reader.HeaderOnly]; used by [Reader.Census] for cheap, allocation-light
	// cartridge classification. Stream headers are still parsed, so flags such
	// as Compressed/Encrypted/Sparse remain accurate.
	headerOnly bool

	strU16     []uint16 // reusable UTF-16 decode buffer (decodeStringInto)
	strBuf     []byte   // reusable byte buffer for the entry Name path (joinPathDecode/joinPathInto)
	scratchBuf []byte   // reusable byte buffer for other decoded strings (decodeStringInto ASCII path)

	block  Block  // reused across Next calls (returned by pointer)
	header Header // reused across entries; block.Header points here

	// reusable slice backing arrays for Header metadata fields (truncated to
	// length 0 each entry, keeping capacity).
	secBuf  []byte         // SecurityDescriptor backing array
	eaBuf   []byte         // ExtendedAttributes backing array
	streams []StreamData   // Streams backing array
	sparses []SparseExtent // SparseExtents backing array

	tape TapeInfo
	set  SetInfo
	eset ESetInfo

	hasTape bool
	hasSet  bool
	hasEset bool

	sawESET bool

	corrupt uint32

	// Media Based Catalog: raw payloads captured from the ESET's TFDD/TSMP (or
	// FDD2/MAP2) streams, and the lazily-parsed catalog built from them.
	catFDDraw []byte
	catSMPraw []byte
	catalog   *Catalog

	scratch [4096]byte
}

// Open opens the named MTF/BKF file for reading.
func Open(name string) (*Reader, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return NewReader(f), nil
}

// NewReader returns a new Reader reading from r. When r implements io.Seeker
// (for example an *os.File), the reader skips data streams by seeking rather
// than reading, which is dramatically faster when only block headers are
// needed (listing, cataloging) on large archives. When r implements BlockSkipper
// (for example a SCSI-generic tape reader), the skip uses a block LOCATE so file
// data is not read at all — the fast-retrieval path on tape (spec §3.4.3).
func NewReader(r io.Reader) *Reader {
	rd := &Reader{r: r}
	if s, ok := r.(io.Seeker); ok {
		rd.seeker = s
	}
	if bs, ok := r.(BlockSkipper); ok {
		rd.blockSkipper = bs
	}
	return rd
}

// HeaderOnly puts the reader into header-only mode: metadata stream *data*
// (NTFS security descriptors, extended attributes, sparse maps) is skipped
// rather than read into [Header] fields, and standard (STAN) file content is
// never delivered. Entry *names* are also skipped (Header.Name is left empty)
// since string construction is the dominant per-entry allocation; block and
// stream *headers* are still parsed, so sizes and flags remain accurate.
// Combined with a seekable source this lets a caller walk a cartridge reading
// essentially only headers, with zero per-entry allocations. It is used by
// [Reader.Census].
func (r *Reader) HeaderOnly() { r.headerOnly = true }

// SetDecryptor registers a callback used to decrypt encrypted data streams
// (§6.5). The MTF specification defines no data-encryption cipher, so the
// algorithm is vendor-specific; supply a Decryptor matching the writer. If a
// stream is encrypted and no decryptor is registered, [Read] returns
// [ErrEncrypted].
func (r *Reader) SetDecryptor(d Decryptor) { r.decryptor = d }

// resetHeader clears the reusable Header for the next entry. Slice fields
// reuse their backing arrays (truncated to length 0); scalars are reassigned.
func (r *Reader) resetHeader() {
	h := &r.header
	*h = Header{
		SecurityDescriptor: r.secBuf[:0],
		ExtendedAttributes: r.eaBuf[:0],
		Streams:            r.streams[:0],
		SparseExtents:      r.sparses[:0],
	}
}

// setBlock populates the reusable Block for a non-entry kind and returns it.
// The entry case uses [Reader.entryBlock].
func (r *Reader) setBlock(kind BlockKind) *Block {
	b := &r.block
	b.Kind = kind
	b.Header = nil
	if r.hasTape {
		b.Tape = &r.tape
	} else {
		b.Tape = nil
	}
	if r.hasSet {
		b.Set = &r.set
	} else {
		b.Set = nil
	}
	if r.hasEset {
		b.ESet = &r.eset
	} else {
		b.ESet = nil
	}
	if kind == KindSetEnd {
		b.Catalog = r.Catalog()
	} else {
		b.Catalog = nil
	}
	return b
}

// entryBlock populates the reusable Block for an entry and returns it. The
// Header is the Reader's reusable header, populated by the parse functions.
func (r *Reader) entryBlock() *Block {
	b := &r.block
	b.Kind = KindEntry
	b.Header = &r.header
	b.Tape = nil
	b.Set = nil
	b.ESet = nil
	b.Catalog = nil
	return b
}

// Close closes the underlying reader if it was opened by Open.
func (r *Reader) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}

// Tape returns metadata from the most recent TAPE descriptor block, or nil if
// none has been encountered yet.
func (r *Reader) Tape() *TapeInfo {
	if !r.hasTape {
		return nil
	}
	return &r.tape
}

// Set returns metadata from the current (most recent) start-of-data-set block,
// or nil if none has been encountered yet.
func (r *Reader) Set() *SetInfo {
	if !r.hasSet {
		return nil
	}
	return &r.set
}

// Position reports the byte offset already consumed from the underlying
// stream. It is intended for diagnostics and for building seek indexes for
// random access extraction.
func (r *Reader) Position() int64 { return r.abspos }

// MediaSequence reports the 1-based index of the current physical medium.
// It is 1 for the initial medium and increments each time a continuation
// medium supplied via [Reader.SetContinuation] is switched to. It is always 1
// for non-spanned archives.
func (r *Reader) MediaSequence() int { return r.mediaSeq + 1 }

// TruncatedByEOTM reports whether the archive ended prematurely because an
// End-Of-Tape-Media marker was reached without a continuation medium being
// registered via [Reader.SetContinuation]. When true, the data set spans
// further media that were not supplied, and the returned snapshot is
// incomplete. Callers should warn the operator.
func (r *Reader) TruncatedByEOTM() bool { return r.hitEOTM }

// Family returns what is known about the media family from the current
// cartridge. It combines the TAPE descriptor and the Set Map (if present) to
// answer questions like "which media family is this tape?" and "how many tapes
// do I need for a full restore?"
//
// Call after the first [KindMedia] block has been processed (which fills
// [TapeInfo]) and, for the most complete family information, after the first
// [KindSetEnd] (which fills the Set Map).
//
// The Set Map is cumulative — the one on the last cartridge of the family is
// the most complete. On a data-only cartridge (CatalogType 64) the Set Map
// may be nil; on a catalog cartridge (CatalogType 128) it is typically present.
func (r *Reader) Family() MediaFamily {
	f := MediaFamily{TapeSequence: r.mediaSeq + 1}
	if r.hasTape {
		f.ID = r.tape.MFMID
		f.TapeName = r.tape.Name
	}
	cat := r.Catalog()
	if cat != nil && cat.SetMap != nil {
		f.SetMap = cat.SetMap
		// Derive total tapes from the Set Map: the maximum MediaSeq across
		// all data-set entries tells us how many cartridges the family spans.
		maxSeq := uint16(0)
		for _, e := range cat.SetMap.Entries {
			if e.MediaSeq > maxSeq {
				maxSeq = e.MediaSeq
			}
		}
		if maxSeq > 0 {
			f.TotalTapes = int(maxSeq)
		}
	}
	// When the FDD is a Backup Exec XML catalog, the SynthImageExtraInfo
	// entries reference all cartridges in the family — a more authoritative
	// count than the Set Map (which only reflects the MTF-level media
	// sequences, not the BE-level family).
	if cat != nil && cat.BECatalog != nil {
		if n := len(cat.BECatalog.AllCartridges()); n > f.TotalTapes {
			f.TotalTapes = n
		}
	}
	return f
}

// VerifyChecksum reports whether the common-block header (MTF_DB_HDR)
// checksum of the current block matches the recomputed word-wise XOR over the
// remaining header fields (MTF spec, "Header Checksum"). This may be used to
// detect media corruption. It should be called immediately after [Next]
// returns (before [Read] consumes the entry). It always returns true when the
// current block buffer is too short to contain a checksum (nothing to verify).
//
// Note: some writers emit a zero checksum; such blocks verify as valid only
// when every other header word is also zero, so a false "invalid" is possible
// for such archives. Treat the result as advisory.
func (r *Reader) VerifyChecksum() bool {
	return checksumValid(r.blk)
}

// Checksum returns the MTF common-block header checksum field of the current
// block together with the value recomputed from the remaining header fields.
// Equal values indicate an intact header.
func (r *Reader) Checksum() (stored, computed uint16) {
	if len(r.blk) < dbChecksumOff+2 {
		return 0, commonChecksum(r.blk)
	}
	return u16(r.blk, dbChecksumOff), commonChecksum(r.blk)
}

// read drains any pending read-ahead (probe bytes) then reads from the
// underlying stream into p. It returns the number of bytes read and any error.
// Callers are responsible for accounting (flbread/abspos) for the returned bytes.
func (r *Reader) read(p []byte) (int, error) {
	var n int
	if len(r.peek) > 0 {
		n = copy(p, r.peek)
		r.peek = r.peek[n:]
	}
	if n == len(p) {
		return n, nil
	}
	nr, err := r.r.Read(p[n:])
	return n + nr, err
}

// readFull reads exactly len(p) bytes, draining read-ahead first. It mirrors
// io.ReadFull but routes through read so probe bytes are delivered in order.
func (r *Reader) readFull(p []byte) (int, error) {
	var total int
	for total < len(p) {
		n, err := r.read(p[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

// ensure reads from the underlying stream until blk holds at least n bytes.
// It never reads past n buffered bytes (it reads exactly the deficit).
func (r *Reader) ensure(n int) error {
	for len(r.blk) < n {
		want := n - len(r.blk)
		if want <= len(r.scratch) {
			// Common case: read into the fixed scratch buffer and append.
			buf := r.scratch[:want]
			nr, err := r.read(buf)
			if nr > 0 {
				r.blk = append(r.blk, buf[:nr]...)
				r.flbread += uint32(nr)
				r.abspos += int64(nr)
			}
			if err != nil {
				if len(r.blk) >= n {
					return nil
				}
				if errors.Is(err, io.EOF) {
					return io.ErrUnexpectedEOF
				}
				return err
			}
			continue
		}
		// Large read: extend blk directly and read into the new tail to avoid a
		// temporary buffer allocation.
		pre := len(r.blk)
		r.blk = append(r.blk, make([]byte, want)...)[:pre]
		nr, err := r.read(r.blk[pre : pre+want])
		r.blk = r.blk[:pre+nr]
		if nr > 0 {
			r.flbread += uint32(nr)
			r.abspos += int64(nr)
		}
		if err != nil {
			if len(r.blk) >= n {
				return nil
			}
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
	return nil
}

// wrapFlbread keeps flbread within [0, flbsize] after reads that can cross
// logical block boundaries.
func (r *Reader) wrapFlbread() {
	for r.flbsize > 0 && r.flbread > r.flbsize {
		r.flbread -= r.flbsize
	}
}

// skipStreamData discards n bytes of stream data. Stream data flows
// continuously across logical block boundaries, so (unlike block alignment)
// it is not capped to flbsize. When the source is seekable the skip is done
// with a single Seek rather than reading the bytes, which makes header-only
// walks (listing, Census) fast on large file-backed archives. When the source
// implements BlockSkipper (e.g. a SCSI-generic tape reader) the skip uses a
// block LOCATE so file data is not transferred to the host — the fast path for
// header-only walks on tape (MTF §3.4.3 fast retrieval).
func (r *Reader) skipStreamData(n int64) error {
	if r.blockSkipper != nil {
		if os.Getenv("MTF_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "[mtf] skip n=%d abspos=%d\n", n, r.abspos)
		}
		if err := r.blockSkipper.SkipForward(n); err != nil {
			return err
		}
		r.peek = r.peek[:0]
		r.flbread += uint32(n)
		r.abspos += n
		r.streamDid += n
		r.wrapFlbread()
		return nil
	}
	if r.seeker != nil {
		target := r.abspos + n
		if _, err := r.seeker.Seek(target, io.SeekStart); err != nil {
			return err
		}
		// Discard read-ahead: those bytes were read from the pre-seek position.
		r.peek = r.peek[:0]
		r.flbread += uint32(n)
		r.abspos = target
		r.streamDid += n
		r.wrapFlbread()
		return nil
	}
	for n > 0 {
		chunk := min(n, int64(len(r.scratch)))
		nr, err := r.readFull(r.scratch[:chunk])
		if nr > 0 {
			r.flbread += uint32(nr)
			r.abspos += int64(nr)
			r.streamDid += int64(nr)
			n -= int64(nr)
		}
		if err != nil {
			return err
		}
	}
	r.wrapFlbread()
	return nil
}

// readStreamData reads up to len(p) bytes of the current stream's data into p.
func (r *Reader) readStreamData(p []byte) (int, error) {
	total := 0
	for total < len(p) && r.dataRem > 0 {
		if r.atFLBBoundary() {
			if err := r.probeEOTM(); err != nil {
				// A genuine medium boundary was hit mid-stream: re-sync.
				if err == errSpanned {
					if err := r.advanceToContinuationStream(); err != nil {
						return total, err
					}
					continue // dataRem now reflects the continuation stream
				}
				return total, err
			}
		}

		dist := r.distToBoundary()
		want := min(r.dataRem, int64(len(p)-total))
		if dist > 0 && dist < want {
			want = dist
		}
		if want == 0 {
			want = r.dataRem
		}
		nr, err := r.readFull(p[total : total+int(want)])
		if nr > 0 {
			r.flbread += uint32(nr)
			r.abspos += int64(nr)
			r.streamDid += int64(nr)
			r.dataRem -= int64(nr)
			total += nr
		}
		if err != nil {
			return total, err
		}
	}
	r.wrapFlbread()
	return total, nil
}

// scanStart reads the common descriptor block of the next logical block.
// It returns io.EOF on a clean end-of-stream.
func (r *Reader) scanStart() error {
	r.blk = r.blk[:0]
	r.flbread = 0
	for len(r.blk) < dbCommonSize {
		want := dbCommonSize - len(r.blk)
		nr, err := r.r.Read(r.scratch[:want])
		if nr > 0 {
			r.blk = append(r.blk, r.scratch[:nr]...)
			r.flbread += uint32(nr)
			r.abspos += int64(nr)
		}
		if err != nil {
			if len(r.blk) == 0 {
				return io.EOF
			}
			if len(r.blk) >= dbCommonSize {
				break
			}
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}

	if blockType(r.blk) == dbTAPE {
		if err := r.ensure(tapeFLBSizeOff + 2); err != nil {
			return err
		}
		r.flbsize = uint32(u16(r.blk, tapeFLBSizeOff))
	}
	r.strType = u8(r.blk, dbStrTypeOff)
	return nil
}

// scanNext skips any remaining bytes of the current logical block so that the
// reader is positioned at the start of the next one.
func (r *Reader) scanNext() error {
	r.wrapFlbread()
	if r.flbsize > 0 && r.flbread < r.flbsize {
		remaining := int64(r.flbsize - r.flbread)
		for remaining > 0 {
			chunk := min(remaining, int64(len(r.scratch)))
			nr, err := io.ReadFull(r.r, r.scratch[:chunk])
			if nr > 0 {
				r.flbread += uint32(nr)
				r.abspos += int64(nr)
				remaining -= int64(nr)
			}
			if err != nil {
				return err
			}
		}
	}
	r.blk = r.blk[:0]
	r.flbread = 0
	return nil
}

// readStreamHeader parses the stream descriptor header at streamOff.
func (r *Reader) readStreamHeader() {
	off := int(r.streamOff)
	r.streamType = u32(r.blk, off+stTypeOff)
	r.streamSysAttr = u16(r.blk, off+stSysAttrOff)
	r.streamMediaAttr = u16(r.blk, off+stMediaAttrOff)
	r.streamLen = int64(u64(r.blk, off+stLengthOff))
	r.streamEncAlgo = u16(r.blk, off+stEncryptOff)
	r.streamCompAlgo = u16(r.blk, off+stCompressOff)
	r.streamChecksum = u16(r.blk, off+stChecksumOff)
	r.streamDid = 0
}

// streamStart positions the reader at the first stream of the current block.
func (r *Reader) streamStart() error {
	r.lastStream = false
	r.streamOff = uint32(u16(r.blk, dbOffOff))
	if err := r.ensure(int(r.streamOff) + streamHeaderSize); err != nil {
		return err
	}
	// Per MTF spec ("Offset To First Event"): when a DBLK has no data streams,
	// the Offset To First Event points to the *next* DBLK rather than to a
	// stream header. Backup Exec writes stream-less SSET/VOLB/DIRB/ESPB blocks
	// this way. Disambiguate using the spec's method: if the bytes at the
	// offset form a known DBLK type with a valid header checksum, the current
	// block has no streams. Treat it as already-finished so materializeStreams
	// returns immediately and the caller advances to the next block.
	if r.streamOff >= dbCommonSize && isDBLKHeader(r.blk[r.streamOff:]) {
		r.lastStream = true
		r.streamType = StreamSPAD
		r.streamLen = 0
		r.streamDid = 0
		return nil
	}
	r.readStreamHeader()
	return nil
}

// isDBLKHeader reports whether b begins with a known MTF DBLK type whose common
// header checksum validates. It is the spec's way of telling a descriptor-block
// header apart from a stream header at a given offset (MTF spec, "Offset To
// First Event"). The slice must hold at least dbCommonSize bytes.
func isDBLKHeader(b []byte) bool {
	if len(b) < 4 {
		return false
	}
	bt := blockType(b)
	switch bt {
	case dbTAPE, dbSSET, dbVOLB, dbDIRB, dbFILE, dbCFIL, dbESPB, dbESET, dbEOTM, dbSFMB:
		return checksumValid(b)
	}
	return false
}

// streamChecksumValid reports whether the 22-byte stream header at b[off]
// carries a correct word-wise XOR checksum (bytes off..off+19 vs off+20). Per
// the MTF spec ("Checksum" under MTF_STREAM_HDR) this verifies a valid stream
// descriptor is being processed.
func streamChecksumValid(b []byte, off int) bool {
	if len(b) < off+streamHeaderSize {
		return false
	}
	var sum uint16
	for i := off; i < off+stChecksumOff; i += 2 {
		sum ^= u16(b, i)
	}
	return sum == u16(b, off+stChecksumOff)
}

// probeStreamHeader returns the offset within b of the next stream header,
// given that the stream data ended at the start of b and that aligned is the
// 4-byte-alignment pad the spec mandates. Real writers always set the stream
// checksum, so the header is whichever of the immediate position (offset 0) or
// the aligned position validates. If neither validates the aligned position is
// returned to preserve the historical 4-aligned assumption (some synthetic
// fixtures omit checksums).
func probeStreamHeader(b []byte, aligned uint32) uint32 {
	if streamChecksumValid(b, 0) {
		return 0
	}
	if aligned != 0 && streamChecksumValid(b, int(aligned)) {
		return aligned
	}
	return aligned
}

// streamNext skips the remainder of the current stream's data and loads the
// next stream header (4-byte aligned), unless the current stream was SPAD, in
// which case lastStream is set.
func (r *Reader) streamNext() error {
	if rem := r.streamLen - r.streamDid; rem > 0 {
		if err := r.skipStreamData(rem); err != nil {
			return err
		}
	}
	r.wrapFlbread()
	if r.streamType == StreamSPAD {
		r.lastStream = true
		return nil
	}

	// Load the next stream header. Per the spec ("Stream Header"), stream
	// headers begin on 4-byte boundaries and a valid header carries a
	// word-wise XOR checksum. The stream data just consumed ends at the
	// current reader position. Most writers pad the data so the next header
	// lands on a 4-byte boundary, but some (notably Backup Exec) place the
	// next header immediately at the data end with no alignment padding.
	//
	// Locate the header by probing with its checksum: read enough bytes to
	// cover both the immediate position and the 4-aligned position, and use
	// whichever validates. If neither validates (header without a checksum,
	// as in some synthetic fixtures, or the natural end of the stream list)
	// fall back to the spec's 4-aligned position so existing behaviour is
	// preserved.
	r.blk = r.blk[:0]
	var aligned uint32
	if m := r.flbread % 4; m != 0 {
		aligned = 4 - m
	}
	if err := r.ensure(int(aligned) + streamHeaderSize); err != nil {
		return err
	}
	r.streamOff = probeStreamHeader(r.blk, aligned)
	r.readStreamHeader()
	if os.Getenv("MTF_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[mtf] stream type=%d len=%d off=%d abspos=%d flbread=%d did=%d\n", r.streamType, r.streamLen, r.streamOff, r.abspos, r.flbread, r.streamDid)
	}
	return nil
}

// Next advances through the MTF stream and returns the next structural block.
// The returned [Block].Kind tells you what was encountered:
//
//   - KindMedia: a medium (MTF_TAPE) started. Block.Tape holds its metadata.
//   - KindSet: a data set (MTF_SSET) started. Block.Set holds its metadata.
//   - KindEntry: an extractable object (MTF_VOLB/DIRB/FILE). Block.Header is
//     fully materialized; call [Reader.Read] to stream its standard data.
//   - KindSetEnd: a data set (MTF_ESET) ended. Block.ESet holds its metadata
//     and Block.Catalog carries any Media Based Catalog (nil if none).
//
// The medium's role is self-evident from the block sequence: a medium with
// KindEntry blocks but no trailing KindSetEnd is data-only (its data set
// continues on the next medium); one whose KindSetEnd carries a Catalog with no
// file-data entries is catalog-heavy; one with both is the normal case.
//
// Media spanning is handled transparently: when a continuation is registered
// via [Reader.SetContinuation], each consumed medium yields its own KindMedia.
// Next returns io.EOF when the archive is fully consumed.
func (r *Reader) Next() (*Block, error) {
	if r.cur != nil && !r.entryDone {
		if err := r.finishEntry(); err != nil {
			return nil, err
		}
	}
	r.inData = false
	r.sparse = false
	r.sparseIdx = 0
	r.sparsePos = 0
	r.sparseCursor = 0
	r.dec = nil

	r.dataRem = 0

	for {
		if err := r.scanStart(); err != nil {
			if errors.Is(err, io.EOF) {
				return nil, io.EOF
			}
			if errors.Is(err, io.ErrUnexpectedEOF) && r.sawESET {
				return nil, io.EOF
			}
			return nil, err
		}

		switch blockType(r.blk) {
		case dbTAPE:
			// A TAPE block seen mid-stream is a continuation media header when
			// spanning; otherwise the initial header. Adopt its logical block
			// size, advance past it, and expose it as a new medium.
			if err := r.parseTape(); err != nil {
				return nil, err
			}
			if err := r.scanNext(); err != nil {
				return nil, r.endOrError(err)
			}
			return r.setBlock(KindMedia), nil
		case dbSSET:
			if err := r.parseSet(); err != nil {
				return nil, err
			}
			if err := r.scanNext(); err != nil {
				return nil, r.endOrError(err)
			}
			return r.setBlock(KindSet), nil
		case dbESET:
			r.sawESET = true
			if err := r.parseEset(); err != nil {
				return nil, err
			}
			// The ESET may carry the Media Based Catalog as attached streams
			// (TFDD/TSMP). Capture any catalog streams now so they are not lost;
			// the walk ends on the terminal SPAD.
			if err := r.captureCatalog(); err != nil {
				return nil, r.endOrError(err)
			}
			if err := r.scanNext(); err != nil {
				return nil, r.endOrError(err)
			}
			return r.setBlock(KindSetEnd), nil
		case dbEOTM:
			// End of medium between entries: switch to the continuation medium,
			// whose leading TAPE block will be exposed as the next KindMedia.
			if err := r.scanNext(); err != nil {
				return nil, r.endOrError(err)
			}
			if r.switchMedium() {
				continue
			}
			r.hitEOTM = true
			return nil, io.EOF
		case dbSFMB, dbESPB, dbCFIL:
			// filemark / padding / corrupt-placeholder blocks: nothing to expose
		case dbVOLB:
			if u32(r.blk, dbAttrOff)&AttrContinuation != 0 {
				// Continuation volume context: restore silently, no entry.
				if _, err := r.restoreVolb(); err != nil {
					return nil, err
				}
			} else {
				h, err := r.parseVolb()
				if err != nil {
					return nil, err
				}
				if err := r.beginEntry(h); err != nil {
					return nil, r.endOrError(err)
				}
				return r.entryBlock(), nil
			}
		case dbDIRB:
			if u32(r.blk, dbAttrOff)&AttrContinuation != 0 {
				// Continuation directory context: restore silently, no entry.
				if _, err := r.restoreDirb(); err != nil {
					return nil, err
				}
			} else {
				h, err := r.parseDirb()
				if err != nil {
					return nil, err
				}
				if err := r.beginEntry(h); err != nil {
					return nil, r.endOrError(err)
				}
				return r.entryBlock(), nil
			}

		case dbFILE:
			h, err := r.parseFile()
			if err != nil {
				return nil, err
			}
			if err := r.streamStart(); err != nil {
				return nil, err
			}
			r.cur = h
			if err := r.materializeStreams(h); err != nil {
				return nil, err
			}
			if r.sparse {
				r.finishSparse(h)
			}
			if !r.inData && !r.sparse {
				// No standard data stream: nothing to read. Advance to the next
				// block boundary so the reader is positioned cleanly.
				r.entryDone = true
				if err := r.scanNext(); err != nil {
					return nil, r.endOrError(err)
				}
			} else {
				r.entryDone = false
			}
			return r.entryBlock(), nil

		default:
			// unknown or empty (dead) block: skip and continue
		}

		if err := r.scanNext(); err != nil {
			return nil, r.endOrError(err)
		}
	}
}

// endOrError converts a trailing read error into io.EOF once a data-set end has
// been seen (archives may omit trailing block padding), otherwise returns err.
func (r *Reader) endOrError(err error) error {
	if r.sawESET && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return io.EOF
	}
	return err
}

// beginEntry positions the reader at the first data stream of the current
// descriptor block (held in r.blk) and materializes the entry's metadata
// streams into h. An entry with no streams (stream offset 0) is reported as
// done. It is used by volume and directory entries, whose content — if any —
// is purely metadata.
func (r *Reader) beginEntry(h *Header) error {
	off := uint32(u16(r.blk, dbOffOff))
	if off == 0 {
		// No streams recorded for this object: advance past the block.
		if err := r.scanNext(); err != nil {
			return err
		}
		r.cur = h
		r.entryDone = true
		return nil
	}
	if err := r.streamStart(); err != nil {
		// No streams reachable: advance past the block.
		if e2 := r.scanNext(); e2 != nil {
			return e2
		}
		r.cur = h
		r.entryDone = true
		return nil
	}
	r.cur = h
	if err := r.materializeStreams(h); err != nil {
		return err
	}
	if r.sparse {
		r.finishSparse(h)
	}
	// Volume and directory entries never carry readable content. Once their
	// metadata streams have been materialized, advance to the next block
	// boundary so the reader is positioned cleanly for the following entry.
	if !r.inData && !r.sparse {
		r.entryDone = true
		return r.scanNext()
	}
	r.entryDone = false
	return nil
}

// finishEntry consumes any remaining data and trailing streams of the current
// entry and advances to the next block boundary.
func (r *Reader) finishEntry() error {
	defer func() { r.entryDone = true }()

	if r.sparse {
		// Sparse content was fully materialized during Next; just advance.
		r.sparse = false
		return r.scanNext()
	}

	if r.inData && r.dataRem > 0 {
		// Skip the remaining STAN data. This must be media-spanning aware: a
		// caller may discard (not read) a file whose data is split across media.
		if err := r.skipRemainingData(); err != nil {
			return err
		}
		r.dataRem = 0
	}
	// A cross-media continuation stream has FLB-aligned data with no trailing
	// SPAD: just advance to the next block boundary.
	if r.streamContinued {
		r.streamContinued = false
		return r.scanNext()
	}
	// The STAN data has been consumed (read or skipped). Walk the streams that
	// follow it (typically a CSUM, possibly ADAT/NTED for alternate/encrypted
	// data) up to the terminal SPAD, capturing their bytes onto the current
	// entry so no metadata is lost. This appends to r.cur.Streams; in
	// header-only mode the bytes are skipped.
	for !r.lastStream {
		if err := r.streamNext(); err != nil {
			return err
		}
		if r.streamType != StreamSPAD {
			if err := r.captureExtra(r.cur); err != nil {
				return err
			}
		}
	}
	if err := r.scanNext(); err != nil {
		return err
	}
	return nil
}

// skipRemainingData discards the remaining bytes of the current STAN data
// stream, transparently spanning continuation media if the data is split.
func (r *Reader) skipRemainingData() error {
	if r.dataRem <= 0 {
		return nil
	}
	// Fast path for seekable sources: if the remaining stream data fits before
	// end-of-file, skip it in a single Seek. Mid-stream EOTM (media spanning)
	// only shortens the data available on this medium, so when the data would
	// cross EOF we fall back to the careful per-boundary probe path that
	// detects and follows the EOTM. This avoids one probe read per Format
	// Logical Block boundary on large single-medium files.
	//
	// Block-skip sources (tape) do not support a cheap SeekEnd (that would force
	// a full pass to end-of-data), so they take the forward skip directly.
	if r.blockSkipper == nil && r.seeker != nil {
		end, err := r.seeker.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if r.abspos+r.dataRem <= end {
			if err := r.skipStreamData(r.dataRem); err != nil {
				return err
			}
			r.dataRem = 0
			return nil
		}
		// Data crosses EOF: re-position to where we were and probe carefully.
		if _, err := r.seeker.Seek(r.abspos, io.SeekStart); err != nil {
			return err
		}
	}
	if r.blockSkipper != nil {
		// Forward skip the whole remaining data via block LOCATE. Spanning is not
		// handled here (single-medium tape walk); a mid-stream EOTM would surface
		// as a read error on the next descriptor.
		if err := r.skipStreamData(r.dataRem); err != nil {
			return err
		}
		r.dataRem = 0
		return nil
	}
	for r.dataRem > 0 {
		if r.atFLBBoundary() {
			if err := r.probeEOTM(); err != nil {
				if err == errSpanned {
					if err := r.advanceToContinuationStream(); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
		dist := r.distToBoundary()
		want := r.dataRem
		if dist > 0 && dist < want {
			want = dist
		}
		if want == 0 {
			want = r.dataRem
		}
		before := r.streamDid
		if err := r.skipStreamData(want); err != nil {
			return err
		}
		r.dataRem -= r.streamDid - before
	}
	return nil
}

// Read reads data from the current file entry into p. It returns the
// reconstructed file content: for a plain file this is the standard data
// (STAN) stream, transparently followed across continuation media; for a
// sparse file the holes are zero-filled according to the parsed sparse map.
//
// Decompression and decryption are not performed: for a compressed or
// encrypted stream the raw stored bytes are returned.
//
// Read returns io.EOF when the entry's content is exhausted. Calling Read on
// a non-file entry returns io.EOF immediately.
func (r *Reader) Read(p []byte) (int, error) {
	if r.cur == nil || r.entryDone {
		return 0, io.EOF
	}
	if r.cur.Type != EntryFile {
		return 0, io.EOF
	}

	if r.sparse {
		nr, err := r.readSparse(p)
		if r.sparseIdx >= len(r.cur.SparseExtents) && err == nil {
			if ferr := r.finishEntry(); ferr != nil {
				return nr, ferr
			}
			err = io.EOF
		}
		return nr, err
	}

	// Compressed/encrypted stream: serve decoded bytes from the frame decoder.
	if r.dec != nil {
		nr, err := r.dec.Read(p)
		if err == io.EOF {
			r.inData = false
			if ferr := r.finishEntry(); ferr != nil {
				return nr, ferr
			}
			if nr == 0 {
				return 0, io.EOF
			}
			err = nil
		}
		return nr, err
	}

	if !r.inData || r.dataRem <= 0 {
		if err := r.finishEntry(); err != nil {
			return 0, err
		}
		return 0, io.EOF
	}

	n := len(p)
	if int64(n) > r.dataRem {
		n = int(r.dataRem)
	}
	nr, err := r.readStreamData(p[:n])
	if r.dataRem <= 0 {
		if ferr := r.finishEntry(); ferr != nil {
			return nr, ferr
		}
		if err == nil {
			err = io.EOF
		}
	}
	return nr, err
}

// stringAt returns the decoded string referenced by the TAPE_POSITION (size,pos)
// pair, reading more bytes into the block buffer as needed.
func (r *Reader) stringAt(size, pos uint16, sep byte) (string, error) {
	if err := r.ensure(int(pos) + int(size)); err != nil {
		return "", err
	}
	return r.decodeStringInto(r.blk, int(pos), int(size), r.strType, sep), nil
}

func (r *Reader) parseTape() error {
	if err := r.ensure(tapeCTimeOff + 6); err != nil {
		return err
	}
	t := &r.tape
	*t = TapeInfo{
		MFMID:                 u32(r.blk, tapeMFMIDOff),
		Attributes:            u32(r.blk, tapeAttrOff),
		Sequence:              u16(r.blk, tapeSeqOff),
		PasswordAlgorithm:     u16(r.blk, tapeEncryptOff),
		SoftFilemarkBlockSize: u16(r.blk, tapeSFMSizeOff),
		CatalogType:           u16(r.blk, tapeCatTypeOff),
		FLBSize:               u16(r.blk, tapeFLBSizeOff),
		SoftwareVendorID:      u16(r.blk, tapeVendorIDOff),
		MTFMajorVersion:       u8(r.blk, tapeVersionOff),
		CreateTime:            decodeDateTime(r.blk, tapeCTimeOff),
	}
	var err error
	if sz, po := tapepos(r.blk, tapeSoftwareOff); sz > 0 {
		if t.Software, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, tapeNameOff); sz > 0 {
		if t.Name, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, tapeLabelOff); sz > 0 {
		if t.Label, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, tapePasswdOff); sz > 0 {
		if t.Password, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	r.tape = *t
	r.hasTape = true
	return nil
}

func (r *Reader) parseSet() error {
	if err := r.ensure(ssetCatVerOff + 1); err != nil {
		return err
	}
	s := &r.set
	*s = SetInfo{
		Number:           u16(r.blk, ssetNumOff),
		PBA:              u64(r.blk, ssetPBAOff),
		SoftwareVendorID: u16(r.blk, ssetVendorOff),
		SoftwareVersion:  u16(r.blk, ssetVerOff),
		Attributes:       u32(r.blk, ssetAttrOff),
		Compression:      u16(r.blk, ssetCompOff),
		Encryption:       u16(r.blk, ssetEncryptOff),
		MajorVersion:     u8(r.blk, ssetMajorOff),
		MinorVersion:     u8(r.blk, ssetMinorOff),
		TimeZone:         int8(u8(r.blk, ssetTZOff)),
		CreateTime:       decodeDateTime(r.blk, ssetCTimeOff),
	}
	var err error
	if sz, po := tapepos(r.blk, ssetNameOff); sz > 0 {
		if s.Name, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, ssetLabelOff); sz > 0 {
		if s.Label, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, ssetPasswdOff); sz > 0 {
		if s.Password, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	if sz, po := tapepos(r.blk, ssetUserOff); sz > 0 {
		if s.Owner, err = r.stringAt(sz, po, '/'); err != nil {
			return err
		}
	}
	r.set = *s
	r.hasSet = true
	return nil
}

func (r *Reader) parseVolb() (*Header, error) {
	if err := r.ensure(volbCTimeOff + 6); err != nil {
		return nil, err
	}
	var device string
	if !r.headerOnly {
		if sz, po := tapepos(r.blk, volbDeviceOff); sz > 0 {
			var err error
			if device, err = r.stringAt(sz, po, '/'); err != nil {
				return nil, err
			}
		}
	}
	r.volume = device
	r.cwd = ""
	r.cwdID = 0

	r.resetHeader()
	h := &r.header
	h.Type = EntryVolume
	h.Volume = r.volume
	if !r.headerOnly {
		h.Name = device
		h.Volume = device
	}
	h.Attributes = u32(r.blk, volbAttrOff)
	h.BlockAttributes = u32(r.blk, dbAttrOff)
	h.OSID = u8(r.blk, dbOSIDOff)
	h.DisplayableSize = u64(r.blk, dbSizeOff)
	h.CreateTime = decodeDateTime(r.blk, volbCTimeOff)
	h.ModTime = decodeDateTime(r.blk, volbCTimeOff)
	var err error
	if !r.headerOnly {
		if sz, po := tapepos(r.blk, volbVolumeOff); sz > 0 {
			if h.VolumeLabel, err = r.stringAt(sz, po, '/'); err != nil {
				return nil, err
			}
		}
		if sz, po := tapepos(r.blk, volbMachineOff); sz > 0 {
			if h.MachineName, err = r.stringAt(sz, po, '/'); err != nil {
				return nil, err
			}
		}
	}
	if r.hasSet {
		h.SetNumber = r.set.Number
	}
	return h, nil
}

func (r *Reader) parseDirb() (*Header, error) {
	if err := r.ensure(dirbNameOff + 4); err != nil {
		return nil, err
	}
	attr := u32(r.blk, dirbAttrOff)
	if attr&0x20000 == 0 && !r.headerOnly {
		if sz, po := tapepos(r.blk, dirbNameOff); sz > 0 {
			name, err := r.stringAt(sz, po, '/')
			if err != nil {
				return nil, err
			}
			r.cwd = name
			r.cwdID = u32(r.blk, dirbIDOff)
		}
	}
	// else: path encoded in a stream; keep the previous cwd.

	r.resetHeader()
	h := &r.header
	h.Type = EntryDirectory
	if !r.headerOnly {
		h.Name = r.joinPathInto(r.volume, r.cwd)
	}
	h.Volume = r.volume
	h.Attributes = attr
	h.BlockAttributes = u32(r.blk, dbAttrOff)
	h.OSID = u8(r.blk, dbOSIDOff)
	if h.OSID == 14 {
		if osSize, osOff := tapepos(r.blk, dbOSDataOff); osSize >= 12 {
			if int(osOff)+12 <= len(r.blk) {
				h.NTFileFlags = u32(r.blk, int(osOff)+8)
			}
		}
	}
	h.DisplayableSize = u64(r.blk, dbSizeOff)
	h.IsHardLink = h.NTFileFlags&NTFileLinkFlag != 0
	h.ModTime = decodeDateTime(r.blk, dirbMTimeOff)
	h.CreateTime = decodeDateTime(r.blk, dirbCTimeOff)
	h.BirthTime = decodeDateTime(r.blk, dirbBTimeOff)
	h.AccessTime = decodeDateTime(r.blk, dirbATimeOff)
	h.DirID = r.cwdID
	if r.hasSet {
		h.SetNumber = r.set.Number
	}
	return h, nil
}

func (r *Reader) parseEset() error {
	if err := r.ensure(esetCTimeOff + 6); err != nil {
		return err
	}
	r.corrupt = u32(r.blk, esetCorruptOff)
	r.eset = ESetInfo{
		Attributes:       u32(r.blk, esetAttrOff),
		CorruptObjects:   r.corrupt,
		FDDMediaSequence: u16(r.blk, esetSeqOff),
		SetNumber:        u16(r.blk, esetSetOff),
		CreateTime:       decodeDateTime(r.blk, esetCTimeOff),
	}
	r.hasEset = true
	return nil
}

// restoreVolb parses a continuation VOLB block to restore the volume/device
// context without emitting an entry. Used when re-synchronizing onto a
// continuation medium.
func (r *Reader) restoreVolb() (*Header, error) {
	if err := r.ensure(volbCTimeOff + 6); err != nil {
		return nil, err
	}
	if sz, po := tapepos(r.blk, volbDeviceOff); sz > 0 {
		device, err := r.stringAt(sz, po, '/')
		if err != nil {
			return nil, err
		}
		r.volume = device
	}
	return nil, nil
}

// restoreDirb parses a continuation DIRB block to restore the directory
// context without emitting an entry. Used when re-synchronizing onto a
// continuation medium.
func (r *Reader) restoreDirb() (*Header, error) {
	if err := r.ensure(dirbNameOff + 4); err != nil {
		return nil, err
	}
	attr := u32(r.blk, dirbAttrOff)
	if attr&0x20000 == 0 {
		if sz, po := tapepos(r.blk, dirbNameOff); sz > 0 {
			name, err := r.stringAt(sz, po, '/')
			if err != nil {
				return nil, err
			}
			r.cwd = name
			r.cwdID = u32(r.blk, dirbIDOff)
		}
	}
	return nil, nil
}

// CorruptObjects returns the corrupt-object count reported by the most recent
// end-of-data-set (ESET) block, or zero if no set has ended yet.
func (r *Reader) CorruptObjects() uint32 { return r.corrupt }

// ESet returns metadata from the most recent end-of-data-set (ESET) block, or
// nil if no data set has ended yet. The returned value is shared; callers must
// not retain it across subsequent calls to [Reader.Next].
func (r *Reader) ESet() *ESetInfo {
	if !r.hasEset {
		return nil
	}
	return &r.eset
}

func (r *Reader) parseFile() (*Header, error) {
	if err := r.ensure(fileNameOff + 4); err != nil {
		return nil, err
	}
	sz, po := tapepos(r.blk, fileNameOff)
	if sz > 0 {
		if err := r.ensure(int(po) + int(sz)); err != nil {
			return nil, err
		}
	}
	dirid := u32(r.blk, fileDirIDOff)

	r.resetHeader()
	h := &r.header
	h.Type = EntryFile
	// Build the full path into the reusable buffer (header-only skips it since
	// classification uses only scalar fields and flags).
	if !r.headerOnly {
		h.Name = r.joinPathDecode(r.volume, r.cwd, sz, po)
	}
	h.Volume = r.volume
	h.Attributes = u32(r.blk, fileAttrOff)
	h.BlockAttributes = u32(r.blk, dbAttrOff)
	h.OSID = u8(r.blk, dbOSIDOff)
	// Parse NT File Flags from OS-specific data for Windows NT (OSID 14).
	if h.OSID == 14 {
		if osSize, osOff := tapepos(r.blk, dbOSDataOff); osSize >= 12 {
			if int(osOff)+12 <= len(r.blk) {
				h.NTFileFlags = u32(r.blk, int(osOff)+8)
			}
		}
	}
	h.IsHardLink = h.NTFileFlags&NTFileLinkFlag != 0
	h.DisplayableSize = u64(r.blk, dbSizeOff)
	h.ModTime = decodeDateTime(r.blk, fileMTimeOff)
	h.CreateTime = decodeDateTime(r.blk, fileCTimeOff)
	h.BirthTime = decodeDateTime(r.blk, fileBTimeOff)
	h.AccessTime = decodeDateTime(r.blk, fileATimeOff)
	h.FileID = u32(r.blk, fileIDOff)
	h.DirID = dirid
	if r.hasSet {
		h.SetNumber = r.set.Number
	}
	return h, nil
}
