package mtf

import (
	"errors"
	"io"
	"os"
)

// ErrFilemark is returned by [Tape.ReadBlock] when the drive reports a
// filemark with no preceding data: the current recorded section has ended and
// the tape is positioned at the first block of the next section. A single
// filemark is a section delimiter, not end of data — the Reader skips it and
// keeps reading. Two filemarks in a row (no data between) mean end of recorded
// data and are reported as io.EOF.
//
// This mirrors the Linux st driver contract: read() returns zero bytes at a
// filemark. Surfacing it as an explicit sentinel instead of hiding it behind
// io.EOF lets the Reader tell "section boundary" (keep going) apart from "no
// more data" (stop).
var ErrFilemark = errors.New("mtf: filemark")

// Tape is the block-oriented source a [Reader] reads from.
//
// MTF on linear tape (LTO) is a sequence of fixed-size physical blocks
// organised into recorded sections separated by filemarks; logical blocks
// (the FLB-sized units the format itself deals in) are packed many-per-physical
// block. A byte-stream model (io.Reader) cannot represent that: a filemark
// reads as zero bytes, which io.Reader can only express as io.EOF, so the walk
// would end at the first filemark instead of continuing into the data set.
// Tape therefore exposes the medium as physical blocks plus an explicit
// filemark sentinel.
//
// Implementations:
//   - an LTO drive through the Linux st driver (e.g. via
//     github.com/pbs-plus/go-tapedrive), where ReadBlock is one MTread and
//     SeekBlock is MTSEEK;
//   - [SliceTape], an in-memory buffer (single section, no filemarks);
//   - the file wrapper used by [Open], which presents a .bkf file as a tape
//     with a single section.
//
// A Tape is not safe for concurrent use.
type Tape interface {
	// ReadBlock reads the next physical block into dst and returns its length.
	// The returned length is the block's recorded size (for a tape drive, the
	// physical block size). dst must be at least as large as the largest block
	// the medium can produce.
	//
	// A filemark between recorded sections is reported as [ErrFilemark] with a
	// length of zero; the caller skips it and reads again. End of recorded
	// data (two consecutive filemarks, or a genuine clean EOF) is reported as
	// io.EOF. A block followed by end of data may be returned as (n, io.EOF):
	// the n bytes are delivered first and the io.EOF surfaces on the next call.
	ReadBlock(dst []byte) (int, error)

	// SeekBlock positions the source at the start of the given physical block
	// (0-based from the beginning of the medium). After SeekBlock the next
	// ReadBlock returns that block. This is the fast-skip path the Reader uses
	// to jump over a file's data streams without reading them (MTF §3.4.3).
	SeekBlock(block int64) error
}

// maxTapeBlock is the largest physical block the built-in wrappers will handle.
// 1 MiB covers LTO variable-block records with room to spare.
const maxTapeBlock = 1 << 20

// SliceTape is a [Tape] backed by an in-memory byte slice. It models a single
// recorded section (no filemarks): the whole slice is delivered as a sequence
// of fixed-size blocks, then io.EOF. Useful for tests and for archives built
// in memory.
type SliceTape struct {
	data  []byte
	pos   int
	chunk int
}

// NewSliceTape returns a [SliceTape] over b. Reads deliver b in maxTapeBlock-sized
// chunks; SeekBlock addresses chunks.
func NewSliceTape(b []byte) *SliceTape {
	return &SliceTape{data: b, chunk: maxTapeBlock}
}

// ReadBlock implements [Tape].
func (t *SliceTape) ReadBlock(dst []byte) (int, error) {
	if t.pos >= len(t.data) {
		return 0, io.EOF
	}
	end := min(t.pos+t.chunk, len(t.data))
	n := copy(dst, t.data[t.pos:end])
	t.pos = end
	if t.pos >= len(t.data) {
		return n, io.EOF
	}
	return n, nil
}

// SeekBlock implements [Tape].
func (t *SliceTape) SeekBlock(block int64) error {
	pos := min(max(int(block*int64(t.chunk)), 0), len(t.data))
	t.pos = pos
	return nil
}

// NewFileTape returns a [Tape] over an open *os.File, used by [Open]. A .bkf
// file is a single recorded section (no filemarks) of densely packed logical
// blocks, so it is presented as a sequence of maxTapeBlock-sized blocks with
// byte-seek positioning underneath. Ownership of f is transferred: the tape's
// Close (and therefore [Reader.Close]) closes f.
type fileTape struct {
	f     *os.File
	chunk int
}

func NewFileTape(f *os.File) *fileTape {
	return &fileTape{f: f, chunk: maxTapeBlock}
}

// ReadBlock implements [Tape]. It reads up to chunk bytes, looping on short
// reads so the block length (and hence the physical block size the Reader
// infers) is stable.
func (t *fileTape) ReadBlock(dst []byte) (int, error) {
	max := min(t.chunk, len(dst))
	total := 0
	for total < max {
		n, err := t.f.Read(dst[total:max])
		total += n
		if err != nil {
			if total > 0 {
				return total, io.EOF
			}
			return 0, err
		}
		if n == 0 {
			break
		}
	}
	if total == 0 {
		return 0, io.EOF
	}
	return total, nil
}

// SeekBlock implements [Tape].
func (t *fileTape) SeekBlock(block int64) error {
	_, err := t.f.Seek(block*int64(t.chunk), io.SeekStart)
	return err
}

// Close lets fileTape satisfy io.Closer so [Reader.Close] releases the file.
func (t *fileTape) Close() error { return t.f.Close() }
