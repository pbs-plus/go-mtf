package mtf

import (
	"errors"
	"io"
	"os"
)

// Reader provides sequential access to the entries of an MTF/BKF stream.
//
// Use [Next] to advance to the next entry and [Read] to read the standard data
// of the current file entry. The API intentionally mirrors archive/tar.
type Reader struct {
	r      io.Reader
	closer io.Closer

	blk     []byte // header / stream-header buffer for the current block
	flbsize uint32 // logical block size (from the TAPE descriptor)
	flbread uint32 // bytes consumed within the current logical block
	abspos  int64  // absolute position in the underlying stream
	strType uint8  // string encoding for the current block

	// current stream descriptor state
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

	// current entry state
	cur       *Header
	inData    bool  // positioned within the file's STAN data stream
	dataRem   int64 // remaining bytes of the file's STAN data
	entryDone bool  // current entry has been fully consumed

	// path context
	volume string
	cwd    string
	cwdID  uint32

	// media spanning (EOTM / continuation) support
	nextMedia func() (io.Reader, error) // supplies the next physical medium
	mediaSeq  int                       // number of continuation media consumed
	peek      []byte                    // read-ahead bytes pending delivery (probe buffer)

	// metadata
	tape *TapeInfo
	set  *SetInfo

	sawESET bool

	// eset is the metadata from the most recent end-of-set (ESET) block.
	eset *ESetInfo

	// corrupt is the corrupt-object count reported by the most recent ESET.
	corrupt uint32

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

// NewReader returns a new Reader reading from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
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
func (r *Reader) Tape() *TapeInfo { return r.tape }

// Set returns metadata from the current (most recent) start-of-data-set block,
// or nil if none has been encountered yet.
func (r *Reader) Set() *SetInfo { return r.set }

// Position reports the byte offset already consumed from the underlying
// stream. It is intended for diagnostics and for building seek indexes for
// random access extraction.
func (r *Reader) Position() int64 { return r.abspos }

// MediaSequence reports the 1-based index of the current physical medium.
// It is 1 for the initial medium and increments each time a continuation
// medium supplied via [Reader.SetContinuation] is switched to. It is always 1
// for non-spanned archives.
func (r *Reader) MediaSequence() int { return r.mediaSeq + 1 }

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
// It never reads past n buffered bytes (mirroring mtfscan_ready, which reads
// exactly the deficit).
func (r *Reader) ensure(n int) error {
	for len(r.blk) < n {
		want := n - len(r.blk)
		buf := r.scratch[:]
		if want > len(buf) {
			buf = make([]byte, want)
		} else {
			buf = buf[:want]
		}
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
	}
	return nil
}

// wrapFlbread keeps flbread within [0, flbsize] as mtftar does after reads that
// can cross logical block boundaries.
func (r *Reader) wrapFlbread() {
	for r.flbsize > 0 && r.flbread > r.flbsize {
		r.flbread -= r.flbsize
	}
}

// skipStreamData discards n bytes of stream data. Stream data flows
// continuously across logical block boundaries, so (unlike block alignment)
// it is not capped to flbsize.
func (r *Reader) skipStreamData(n int64) error {
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
	r.readStreamHeader()
	return nil
}

// streamNext skips the remainder of the current stream's data and loads the
// next stream header (4-byte aligned), unless the current stream was SPAD, in
// which case lastStream is set. This mirrors mtf_stream_copy.
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

	// Load the next stream header. Stream headers are 4-byte aligned; the
	// alignment padding is derived from flbread.
	r.blk = r.blk[:0]
	var pad uint32
	if m := r.flbread % 4; m != 0 {
		pad = 4 - m
	}
	r.streamOff = pad
	if err := r.ensure(int(pad) + streamHeaderSize); err != nil {
		return err
	}
	r.readStreamHeader()
	return nil
}

// Next advances to the next entry in the archive and returns its header.
// It returns io.EOF when the end of the archive is reached.
func (r *Reader) Next() (*Header, error) {
	if r.cur != nil && !r.entryDone {
		if err := r.finishEntry(); err != nil {
			return nil, err
		}
	}
	r.inData = false
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
			// size and continue.
			if err := r.parseTape(); err != nil {
				return nil, err
			}
		case dbSSET:
			if err := r.parseSet(); err != nil {
				return nil, err
			}
		case dbESET:
			r.sawESET = true
			if err := r.parseEset(); err != nil {
				return nil, err
			}
		case dbEOTM:
			// End of medium between entries: switch to the continuation medium.
			if err := r.scanNext(); err != nil {
				return r.endOrError(err)
			}
			if r.switchMedium() {
				continue // resume scanning the continuation medium
			}
			return nil, io.EOF
		case dbSFMB, dbESPB, dbCFIL:
			// metadata / padding blocks: nothing to expose
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
				if err := r.scanNext(); err != nil {
					return r.endOrError(err)
				}
				r.cur = h
				r.entryDone = true
				return h, nil
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
				if err := r.scanNext(); err != nil {
					return r.endOrError(err)
				}
				r.cur = h
				r.entryDone = true
				return h, nil
			}

		case dbFILE:
			h, err := r.parseFile()
			if err != nil {
				return nil, err
			}
			if err := r.streamStart(); err != nil {
				return nil, err
			}
			for r.streamType != StreamSTAN &&
				r.streamType != StreamSPAD &&
				!r.lastStream {
				if err := r.streamNext(); err != nil {
					return nil, err
				}
			}
			r.cur = h
			r.entryDone = false
			if r.streamType == StreamSTAN {
				r.inData = true
				r.dataRem = r.streamLen
				h.Size = r.streamLen
				h.CompressionAlgorithm = r.streamCompAlgo
				h.EncryptionAlgorithm = r.streamEncAlgo
				h.Compressed = r.streamMediaAttr&StreamMediaCompressed != 0
				h.Encrypted = r.streamMediaAttr&StreamMediaEncrypted != 0
				h.Sparse = r.streamSysAttr&StreamFSSparse != 0
				h.StreamChecksum = r.streamChecksum
			} else {
				r.inData = false
				r.dataRem = 0
				h.Size = 0
			}
			return h, nil

		default:
			// unknown or empty (dead) block: skip and continue
		}

		if err := r.scanNext(); err != nil {
			return r.endOrError(err)
		}
	}
}

// endOrError converts a trailing read error into io.EOF once a data-set end has
// been seen (archives may omit trailing block padding), otherwise returns err.
func (r *Reader) endOrError(err error) (*Header, error) {
	if r.sawESET && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return nil, io.EOF
	}
	return nil, err
}

// finishEntry consumes any remaining data and trailing streams of the current
// file entry and advances to the next block boundary.
func (r *Reader) finishEntry() error {
	defer func() { r.entryDone = true }()

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
	for !r.lastStream {
		if err := r.streamNext(); err != nil {
			return err
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

// Read reads the standard (STAN) data of the current file entry into p. It
// returns io.EOF when the entry's data is exhausted.
func (r *Reader) Read(p []byte) (int, error) {
	if r.cur == nil || r.cur.Type != EntryFile || r.entryDone {
		return 0, io.EOF
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
	return decodeString(r.blk, int(pos), int(size), r.strType, sep), nil
}

func (r *Reader) parseTape() error {
	if err := r.ensure(tapeCTimeOff + 6); err != nil {
		return err
	}
	t := &TapeInfo{
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
	r.tape = t
	return nil
}

func (r *Reader) parseSet() error {
	if err := r.ensure(ssetCatVerOff + 1); err != nil {
		return err
	}
	s := &SetInfo{
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
	r.set = s
	return nil
}

func (r *Reader) parseVolb() (*Header, error) {
	if err := r.ensure(volbCTimeOff + 6); err != nil {
		return nil, err
	}
	var device string
	if sz, po := tapepos(r.blk, volbDeviceOff); sz > 0 {
		var err error
		if device, err = r.stringAt(sz, po, '/'); err != nil {
			return nil, err
		}
	}
	r.volume = device
	r.cwd = ""
	r.cwdID = 0

	h := &Header{
		Type:            EntryVolume,
		Name:            device,
		Volume:          device,
		Attributes:      u32(r.blk, volbAttrOff),
		BlockAttributes: u32(r.blk, dbAttrOff),
		OSID:            u8(r.blk, dbOSIDOff),
		DisplayableSize: u64(r.blk, dbSizeOff),
		CreateTime:      decodeDateTime(r.blk, volbCTimeOff),
		ModTime:         decodeDateTime(r.blk, volbCTimeOff),
	}
	var err error
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
	if r.set != nil {
		h.SetNumber = r.set.Number
	}
	return h, nil
}

func (r *Reader) parseDirb() (*Header, error) {
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
	// else: path encoded in a stream; keep the previous cwd.

	h := &Header{
		Type:            EntryDirectory,
		Name:            joinPath(r.volume, r.cwd),
		Volume:          r.volume,
		Attributes:      attr,
		BlockAttributes: u32(r.blk, dbAttrOff),
		OSID:            u8(r.blk, dbOSIDOff),
		DisplayableSize: u64(r.blk, dbSizeOff),
		ModTime:         decodeDateTime(r.blk, dirbMTimeOff),
		CreateTime:      decodeDateTime(r.blk, dirbCTimeOff),
		BirthTime:       decodeDateTime(r.blk, dirbBTimeOff),
		AccessTime:      decodeDateTime(r.blk, dirbATimeOff),
		DirID:           r.cwdID,
	}
	if r.set != nil {
		h.SetNumber = r.set.Number
	}
	return h, nil
}

func (r *Reader) parseEset() error {
	if err := r.ensure(esetCTimeOff + 6); err != nil {
		return err
	}
	r.corrupt = u32(r.blk, esetCorruptOff)
	r.eset = &ESetInfo{
		Attributes:       u32(r.blk, esetAttrOff),
		CorruptObjects:   r.corrupt,
		FDDMediaSequence: u16(r.blk, esetSeqOff),
		SetNumber:        u16(r.blk, esetSetOff),
		CreateTime:       decodeDateTime(r.blk, esetCTimeOff),
	}
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
func (r *Reader) ESet() *ESetInfo { return r.eset }

func (r *Reader) parseFile() (*Header, error) {
	if err := r.ensure(fileNameOff + 4); err != nil {
		return nil, err
	}
	var name string
	if sz, po := tapepos(r.blk, fileNameOff); sz > 0 {
		var err error
		if name, err = r.stringAt(sz, po, '/'); err != nil {
			return nil, err
		}
	}
	dirid := u32(r.blk, fileDirIDOff)

	h := &Header{
		Type:            EntryFile,
		Name:            joinPath(r.volume, r.cwd, name),
		Volume:          r.volume,
		Attributes:      u32(r.blk, fileAttrOff),
		BlockAttributes: u32(r.blk, dbAttrOff),
		OSID:            u8(r.blk, dbOSIDOff),
		DisplayableSize: u64(r.blk, dbSizeOff),
		ModTime:         decodeDateTime(r.blk, fileMTimeOff),
		CreateTime:      decodeDateTime(r.blk, fileCTimeOff),
		BirthTime:       decodeDateTime(r.blk, fileBTimeOff),
		AccessTime:      decodeDateTime(r.blk, fileATimeOff),
		FileID:          u32(r.blk, fileIDOff),
		DirID:           dirid,
	}
	if r.set != nil {
		h.SetNumber = r.set.Number
	}
	return h, nil
}
