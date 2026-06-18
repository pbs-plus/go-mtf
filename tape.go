package mtf

import (
	"errors"
	"io"
	"os"

	"github.com/pbs-plus/go-tapedrive"
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

	// TellBlock returns the physical block address (PBA) of the block about to
	// be returned by the next ReadBlock — i.e. the current position expressed
	// as a 0-based block number from the start of the medium. This is the value
	// MTF stores in the MTF_SSET DBLK and uses as the anchor for the §3.4.3
	// FLA→PBA seek calculation. On a real drive it is MTIOCPOS; on the in-memory
	// and file wrappers it is the synthetic block number (byte offset / chunk).
	//
	// Implementations that cannot report position should return an error; the
	// Reader falls back to sequential reading when TellBlock is unavailable.
	TellBlock() (int64, error)
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

// TellBlock implements [Tape], reporting the synthetic block number of the
// next chunk (byte position / chunk size).
func (t *SliceTape) TellBlock() (int64, error) { return int64(t.pos / t.chunk), nil }

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

// TellBlock implements [Tape], reporting the synthetic block number of the
// next chunk (current byte offset / chunk size).
func (t *fileTape) TellBlock() (int64, error) {
	off, err := t.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	return off / int64(t.chunk), nil
}

// Close lets fileTape satisfy io.Closer so [Reader.Close] releases the file.
func (t *fileTape) Close() error { return t.f.Close() }

// DriveTape adapts a [tapedrive.Drive] (the Linux st driver binding) to the
// [Tape] interface. It is the bridge from a real LTO/SCSI tape drive to a
// [Reader].
//
// The two libraries use opposite sentinel conventions for the two tape
// boundaries, so DriveTape translates them:
//
//	go-tapedrive io.EOF        (a single filemark) -> mtf ErrFilemark
//	go-tapedrive ErrEndOfData  (two filemarks/EOD) -> mtf io.EOF
//
// This keeps the mtf.Reader's contract intact — it skips ErrFilemark between
// recorded sections and stops cleanly at io.EOF — while go-tapedrive stays a
// pure driver binding with no mtf knowledge.
type DriveTape struct {
	d *tapedrive.Drive
}

// NewDriveTape wraps an open tapedrive.Drive. The caller transfers ownership:
// the tape's Close (and therefore [Reader.Close]) closes the underlying drive.
func NewDriveTape(d *tapedrive.Drive) *DriveTape { return &DriveTape{d: d} }

// ReadBlock implements [Tape]. It delegates to [tapedrive.Drive.ReadBlockInto]
// and translates the boundary errors to mtf's convention. A data block is
// returned as (n, nil); a filemark as (0, ErrFilemark); end of recorded data
// as (0, io.EOF). The caller's dst must be sized for the drive's largest
// record (use maxTapeBlock, or ReadBlock from a sized scratch buffer).
func (t *DriveTape) ReadBlock(dst []byte) (int, error) {
	n, err := t.d.ReadBlockInto(dst)
	if err == nil {
		return n, nil
	}
	switch {
	case errors.Is(err, io.EOF):
		// go-tapedrive: a filemark was crossed.
		return 0, ErrFilemark
	case errors.Is(err, tapedrive.ErrEndOfData):
		// go-tapedrive: end of recorded data (two filemarks).
		return 0, io.EOF
	default:
		return n, err
	}
}

// SeekBlock implements [Tape], positioning the drive at a physical block
// address via MTSEEK. The block number is the same device-level PBA that
// [tapedrive.Drive.TellBlock] reports and that MTF stores in MTF_SSET.
func (t *DriveTape) SeekBlock(block int64) error { return t.d.SeekBlock(block) }

// TellBlock implements [Tape], returning the drive's current physical block
// address via MTIOCPOS. This is the live PBA the Reader captures when it reads
// the MTF_SSET DBLK, anchoring the §3.4.3 FLA→PBA seek calculation.
func (t *DriveTape) TellBlock() (int64, error) { return t.d.TellBlock() }

// NativePBA marks DriveTape as a source whose PBAs are device-native and
// independent of byte offset. The Reader uses this to select the §3.4.3
// SSET-anchored seek calculation instead of the byte-derived fallback.
func (t *DriveTape) NativePBA() bool { return true }

// Close closes the underlying drive, satisfying io.Closer so [Reader.Close]
// releases the device.
func (t *DriveTape) Close() error { return t.d.Close() }
