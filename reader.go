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
	streamOff  uint32 // offset of the current stream header within blk
	streamLen  int64  // total length of the current stream's data
	streamDid  int64  // bytes of current stream data consumed so far
	streamType uint32 // current stream data type
	lastStream bool   // a SPAD stream has been seen (end of object streams)

	// current entry state
	cur       *Header
	inData    bool  // positioned within the file's STAN data stream
	dataRem   int64 // remaining bytes of the file's STAN data
	entryDone bool  // current entry has been fully consumed

	// path context
	volume string
	cwd    string
	cwdID  uint32

	// metadata
	tape *TapeInfo
	set  *SetInfo

	sawESET bool

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

// absPosition reports the byte offset already consumed from the underlying
// stream. It is intended for diagnostics and index-building.
// Position reports the byte offset already consumed from the underlying stream.
// It is intended for diagnostics and for building seek indexes for random
// access extraction.
func (r *Reader) Position() int64 { return r.abspos }

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
		nr, err := r.r.Read(buf)
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
		nr, err := io.ReadFull(r.r, r.scratch[:chunk])
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
	nr, err := io.ReadFull(r.r, p)
	if nr > 0 {
		r.flbread += uint32(nr)
		r.abspos += int64(nr)
		r.streamDid += int64(nr)
		r.wrapFlbread()
	}
	return nr, err
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
	r.streamLen = int64(u64(r.blk, off+stLengthOff))
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
		case dbSFMB, dbESPB, dbEOTM, dbCFIL:
			// metadata / padding blocks: nothing to expose

		case dbVOLB:
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

		case dbDIRB:
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
		if err := r.skipStreamData(r.dataRem); err != nil {
			return err
		}
		r.dataRem = 0
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
	r.dataRem -= int64(nr)
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
		MFMID:      u32(r.blk, tapeMFMIDOff),
		Attributes: u32(r.blk, tapeAttrOff),
		Sequence:   u16(r.blk, tapeSeqOff),
		FLBSize:    u16(r.blk, tapeFLBSizeOff),
		CreateTime: decodeDateTime(r.blk, tapeCTimeOff),
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
	r.tape = t
	return nil
}

func (r *Reader) parseSet() error {
	if err := r.ensure(ssetCatVerOff + 1); err != nil {
		return err
	}
	s := &SetInfo{
		Number:       u16(r.blk, ssetNumOff),
		Attributes:   u32(r.blk, ssetAttrOff),
		Compression:  u16(r.blk, ssetCompOff),
		Encryption:   u16(r.blk, ssetEncryptOff),
		MajorVersion: u8(r.blk, ssetMajorOff),
		MinorVersion: u8(r.blk, ssetMinorOff),
		TimeZone:     int8(u8(r.blk, ssetTZOff)),
		CreateTime:   decodeDateTime(r.blk, ssetCTimeOff),
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
		Type:       EntryVolume,
		Name:       device,
		Volume:     device,
		Attributes: u32(r.blk, volbAttrOff),
		OSID:       u8(r.blk, dbOSIDOff),
		CreateTime: decodeDateTime(r.blk, volbCTimeOff),
		ModTime:    decodeDateTime(r.blk, volbCTimeOff),
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
		Type:       EntryDirectory,
		Name:       joinPath(r.volume, r.cwd),
		Volume:     r.volume,
		Attributes: attr,
		OSID:       u8(r.blk, dbOSIDOff),
		ModTime:    decodeDateTime(r.blk, dirbMTimeOff),
		CreateTime: decodeDateTime(r.blk, dirbCTimeOff),
		AccessTime: decodeDateTime(r.blk, dirbATimeOff),
		DirID:      r.cwdID,
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
	return nil
}

// CorruptObjects returns the corrupt-object count reported by the most recent
// end-of-data-set (ESET) block, or zero if no set has ended yet.
func (r *Reader) CorruptObjects() uint32 { return r.corrupt }

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
		Type:       EntryFile,
		Name:       joinPath(r.volume, r.cwd, name),
		Volume:     r.volume,
		Attributes: u32(r.blk, fileAttrOff),
		OSID:       u8(r.blk, dbOSIDOff),
		ModTime:    decodeDateTime(r.blk, fileMTimeOff),
		CreateTime: decodeDateTime(r.blk, fileCTimeOff),
		AccessTime: decodeDateTime(r.blk, fileATimeOff),
		FileID:     u32(r.blk, fileIDOff),
		DirID:      dirid,
	}
	if r.set != nil {
		h.SetNumber = r.set.Number
	}
	return h, nil
}
