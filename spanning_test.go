package mtf

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
	"time"
)

// setAttr writes the common Block Attributes (offset 4) of a block.
func setAttr(b []byte, attr uint32) {
	binary.LittleEndian.PutUint32(b[dbAttrOff:], attr)
}
func buildTapeAttr(attr uint32, flbsize int) []byte {
	b := newBlock()
	writeCommon(b, dbTAPE, 0)
	setAttr(b, attr)
	putU16(b, tapeFLBSizeOff, flbsize)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 0, 0, 0, time.Local))
	copy(b[tapeCTimeOff:], dt[:])
	return b
}

func buildSSETAttr(attr uint32) []byte {
	b := newBlock()
	writeCommon(b, dbSSET, 0)
	setAttr(b, attr)
	b[ssetNumOff], b[ssetNumOff+1] = 1, 0
	b[ssetMajorOff] = 3
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 1, 0, 0, time.Local))
	copy(b[ssetCTimeOff:], dt[:])
	return b
}

func buildVOLBAttr(attr uint32, device string) []byte {
	b := newBlock()
	writeCommon(b, dbVOLB, 0)
	setAttr(b, attr)
	putString(b, volbDeviceOff, volbCTimeOff+6, device)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 2, 0, 0, time.Local))
	copy(b[volbCTimeOff:], dt[:])
	return b
}

func buildDIRBAttr(attr uint32, id uint32, name string) []byte {
	b := newBlock()
	writeCommon(b, dbDIRB, 0)
	setAttr(b, attr)
	putU32(b, dirbIDOff, id)
	putString(b, dirbNameOff, dirbNameOff+4, name)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 5, 0, 0, time.Local))
	copy(b[dirbMTimeOff:], dt[:])
	return b
}

// buildEOTM returns an End-of-Tape-Marker block.
func buildEOTM() []byte {
	b := newBlock()
	writeCommon(b, dbEOTM, 0)
	// FLA and Control Block ID must be zero for a valid EOTM (used to validate
	// the block during mid-stream probing).
	return b
}

// fileStreamOffset returns the byte offset of the first stream header within a
// FILE block for the given name (must match buildFILE's layout).
func fileStreamOffset(name string) int {
	nameOff := fileNameOff + 4
	streamStart := nameOff + len(name) + 1
	if m := streamStart % 4; m != 0 {
		streamStart += 4 - m
	}
	return streamStart
}

// splitFileFirst builds the FIRST-medium portion of a FILE whose STAN data
// stream is split across media. The STAN header carries the FULL length; only
// keepData bytes of data are present, and the block is padded to an FLB
// boundary with no trailing SPAD.
func splitFileFirst(id, dirid uint32, name string, mtime time.Time, fullData []byte, keepData int) []byte {
	nameOff := fileNameOff + 4
	streamStart := fileStreamOffset(name)
	preamble := make([]byte, streamStart+streamHeaderSize)
	writeCommon(preamble, dbFILE, uint16(streamStart))
	putU32(preamble, fileIDOff, id)
	putU32(preamble, fileDirIDOff, dirid)
	putString(preamble, fileNameOff, nameOff, name)
	dt := encodeDateTime(mtime)
	copy(preamble[fileMTimeOff:], dt[:])
	putU32(preamble, streamStart+stTypeOff, StreamSTAN)
	putU64(preamble, streamStart+stLengthOff, uint64(len(fullData))) // FULL length
	var out bytes.Buffer
	out.Write(preamble)
	out.Write(fullData[:keepData])
	return padToFLB(out.Bytes(), testFLBSize)
}

// continuationFile builds a continuation FILE block (continuation bit set)
// whose STAN stream carries STREAM_CONTINUE and the remaining length. Per the
// MTF spec, the continuation data begins at the next FLB boundary; only n data
// bytes are carried on this medium (the rest continues on a further medium, or
// n == len(remain) if this is the final piece).
func continuationFile(id uint32, name string, mtime time.Time, remain []byte, n int) []byte {
	nameOff := fileNameOff + 4
	streamStart := fileStreamOffset(name)
	preamble := make([]byte, streamStart+streamHeaderSize)
	writeCommon(preamble, dbFILE, uint16(streamStart))
	setAttr(preamble, AttrContinuation)
	putU32(preamble, fileIDOff, id)
	putU32(preamble, fileDirIDOff, 1)
	putString(preamble, fileNameOff, nameOff, name)
	dt := encodeDateTime(mtime)
	copy(preamble[fileMTimeOff:], dt[:])
	putU32(preamble, streamStart+stTypeOff, StreamSTAN)
	putU16(preamble, streamStart+stMediaAttrOff, int(StreamMediaContinue))
	putU64(preamble, streamStart+stLengthOff, uint64(len(remain))) // remaining length
	blk := padToFLB(preamble, testFLBSize)                         // FILE block fills one FLB
	data := padToFLB(remain[:n], testFLBSize)                      // data at next FLB boundary
	return append(blk, data...)
}

func padToFLB(b []byte, flbsize int) []byte {
	if r := len(b) % flbsize; r != 0 {
		b = append(b, make([]byte, flbsize-r)...)
	}
	return b
}

// continuationPrefix builds the leading continuation blocks (TAPE/SSET/VOLB/
// DIRB, all with the continuation bit) used to restore context on every
// continuation medium.
func continuationPrefix() []byte {
	var buf bytes.Buffer
	buf.Write(buildTapeAttr(AttrContinuation, testFLBSize))
	buf.Write(buildSSETAttr(AttrContinuation))
	buf.Write(buildVOLBAttr(AttrContinuation, "C:"))
	buf.Write(buildDIRBAttr(AttrContinuation, 1, "Users"))
	return buf.Bytes()
}

// mediumReader wraps a slice of media (each []byte) and exposes them in order
// via the continuation callback.
type mediumReader struct {
	media  [][]byte
	cursor int
	r      *Reader
}

func newSpannedReader(media [][]byte) *Reader {
	mr := &mediumReader{media: media}
	r := NewReader(bytes.NewReader(mr.media[0]))
	mr.r = r
	r.SetContinuation(mr.next)
	return r
}
func (m *mediumReader) next(c Continuation) (io.Reader, error) {
	m.cursor++
	if m.cursor >= len(m.media) {
		return nil, io.EOF
	}
	return bytes.NewReader(m.media[m.cursor]), nil
}

// TestContinuationEvent verifies that the continuation callback receives an
// accurate Continuation describing the exhausted medium.
func TestContinuationEvent(t *testing.T) {
	const flb = testFLBSize
	full := bytes.Repeat([]byte{0xAB}, int(2.5*float64(flb)))
	name := "split.dat"
	ft := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)

	dataOff := fileStreamOffset(name) + streamHeaderSize
	endOff := ((dataOff/flb)+2)*flb - dataOff
	keep := endOff
	remain := full[keep:]

	var m1 bytes.Buffer
	m1.Write(buildTapeAttr(0, flb)) // medium 1
	m1.Write(buildSSETAttr(0))
	m1.Write(buildVOLBAttr(0, "C:"))
	m1.Write(buildDIRBAttr(0, 1, "Users"))
	m1.Write(splitFileFirst(10, 1, name, ft, full, keep))
	m1.Write(buildEOTM())

	var m2 bytes.Buffer
	m2.Write(continuationPrefix()) // medium 2
	m2.Write(continuationFile(10, name, ft, remain, len(remain)))
	m2.Write(buildESET())

	var got Continuation
	r := NewReader(bytes.NewReader(m1.Bytes()))
	r.SetContinuation(func(c Continuation) (io.Reader, error) {
		got = c
		return bytes.NewReader(m2.Bytes()), nil
	})
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind == KindEntry && blk.Header.Type == EntryFile {
			io.Copy(io.Discard, r) //nolint:errcheck // read split file, triggers EOTM
		}
	}
	if got.Sequence != 1 {
		t.Errorf("Continuation.Sequence = %d, want 1", got.Sequence)
	}
	if got.Media == nil {
		t.Fatal("Continuation.Media is nil")
	}
	if got.Media.FLBSize != flb {
		t.Errorf("Continuation.Media.FLBSize = %d, want %d", got.Media.FLBSize, flb)
	}
}

// TestSpanningMidFile verifies a file whose STAN data stream is split across
// two media is reassembled byte-for-byte.
func TestSpanningMidFile(t *testing.T) {
	const flb = testFLBSize
	full := bytes.Repeat([]byte{0xAB}, int(2.5*float64(flb))) // spans several blocks
	name := "split.dat"
	ft := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)

	// Split the data so the first medium's portion ends exactly at an FLB
	// boundary (EOM occurs at FLB boundaries).
	dataOff := fileStreamOffset(name) + streamHeaderSize
	endOff := ((dataOff/flb)+2)*flb - dataOff // data bytes that end at the 2nd boundary
	keep := endOff
	if keep >= len(full) {
		t.Fatalf("fixture too small")
	}
	remain := full[keep:]

	// Medium 1
	var m1 bytes.Buffer
	m1.Write(buildTapeAttr(0, flb))
	m1.Write(buildSSETAttr(0))
	m1.Write(buildVOLBAttr(0, "C:"))
	m1.Write(buildDIRBAttr(0, 1, "Users"))
	m1.Write(splitFileFirst(10, 1, name, ft, full, keep))
	m1.Write(buildEOTM())

	// Medium 2 (continuation): split.dat remainder + a normal file after it
	var m2 bytes.Buffer
	m2.Write(continuationPrefix())
	m2.Write(continuationFile(10, name, ft, remain, len(remain)))
	m2.Write(buildFILE(11, 1, "after.dat", ft, []byte("medium two")))
	m2.Write(buildESET())

	r := newSpannedReader([][]byte{m1.Bytes(), m2.Bytes()})
	var gotFile bytes.Buffer
	var sawAfter bool
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		if h.Type != EntryFile {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatalf("read %s: %v", h.Name, err)
		}
		switch h.Name {
		case "C:/Users/split.dat":
			gotFile.Write(buf.Bytes())
			if h.Size != int64(len(full)) {
				t.Errorf("split.dat size = %d, want %d", h.Size, len(full))
			}
		case "C:/Users/after.dat":
			sawAfter = true
			if buf.String() != "medium two" {
				t.Errorf("after.dat = %q, want %q", buf.Bytes(), "medium two")
			}
		}
	}
	if !bytes.Equal(gotFile.Bytes(), full) {
		t.Errorf("split.dat reassembled %d bytes, want %d (mismatch)", gotFile.Len(), len(full))
	}
	if !sawAfter {
		t.Errorf("after.dat (between-file spanning) not seen")
	}
}

// TestSpanningMultiMedia reassembles a file split across three media.
func TestSpanningMultiMedia(t *testing.T) {
	const flb = testFLBSize
	full := bytes.Repeat([]byte{0xCD}, 5*flb)
	name := "split.dat"
	ft := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)

	dataOff := fileStreamOffset(name) + streamHeaderSize
	keep1 := ((dataOff/flb)+1)*flb - dataOff // ends at 1st boundary
	keep2 := flb                             // one full block on medium 2
	// remainder on medium 3
	after2 := full[keep1+keep2:]

	var m1 bytes.Buffer
	m1.Write(buildTapeAttr(0, flb))
	m1.Write(buildSSETAttr(0))
	m1.Write(buildVOLBAttr(0, "C:"))
	m1.Write(buildDIRBAttr(0, 1, "Users"))
	m1.Write(splitFileFirst(10, 1, name, ft, full, keep1))
	m1.Write(buildEOTM())

	var m2 bytes.Buffer
	m2.Write(continuationPrefix())
	m2.Write(continuationFile(10, name, ft, full[keep1:], keep2))
	m2.Write(buildEOTM())

	var m3 bytes.Buffer
	m3.Write(continuationPrefix())
	m3.Write(continuationFile(10, name, ft, after2, len(after2)))
	m3.Write(buildFILE(11, 1, "after.dat", ft, []byte("third")))
	m3.Write(buildESET())

	r := newSpannedReader([][]byte{m1.Bytes(), m2.Bytes(), m3.Bytes()})
	var got bytes.Buffer
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		if h.Type == EntryFile && h.Name == "C:/Users/split.dat" {
			if _, err := io.Copy(&got, r); err != nil {
				t.Fatalf("read: %v", err)
			}
		}
	}
	if !bytes.Equal(got.Bytes(), full) {
		t.Errorf("3-media reassembly = %d bytes, want %d", got.Len(), len(full))
	}
}

// TestSpanningSkippedFile ensures a caller that does NOT read a spanning file's
// data can still iterate past it across the media boundary.
func TestSpanningSkippedFile(t *testing.T) {
	const flb = testFLBSize
	full := bytes.Repeat([]byte{0xAB}, 3*flb)
	name := "split.dat"
	ft := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)
	dataOff := fileStreamOffset(name) + streamHeaderSize
	keep := ((dataOff/flb)+1)*flb - dataOff
	remain := full[keep:]

	var m1 bytes.Buffer
	m1.Write(buildTapeAttr(0, flb))
	m1.Write(buildSSETAttr(0))
	m1.Write(buildVOLBAttr(0, "C:"))
	m1.Write(buildDIRBAttr(0, 1, "Users"))
	m1.Write(splitFileFirst(10, 1, name, ft, full, keep))
	m1.Write(buildEOTM())

	var m2 bytes.Buffer
	m2.Write(continuationPrefix())
	m2.Write(continuationFile(10, name, ft, remain, len(remain)))
	m2.Write(buildESET())

	r := newSpannedReader([][]byte{m1.Bytes(), m2.Bytes()})
	var names []string
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		names = append(names, h.Name)
		// deliberately do NOT read file data
	}
	want := []string{"C:", "C:/Users", "C:/Users/split.dat"}
	if len(names) != len(want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

// TestSpanningNoContinuation verifies that an EOTM without a registered
// continuation callback ends the archive cleanly (io.EOF) rather than erroring,
// even mid-stream.
func TestSpanningNoContinuation(t *testing.T) {
	const flb = testFLBSize
	full := bytes.Repeat([]byte{0xAB}, 2*flb)
	name := "split.dat"
	ft := time.Date(2005, 7, 2, 9, 15, 30, 0, time.Local)
	dataOff := fileStreamOffset(name) + streamHeaderSize
	keep := ((dataOff/flb)+1)*flb - dataOff

	var m1 bytes.Buffer
	m1.Write(buildTapeAttr(0, flb))
	m1.Write(buildSSETAttr(0))
	m1.Write(buildVOLBAttr(0, "C:"))
	m1.Write(buildDIRBAttr(0, 1, "Users"))
	m1.Write(splitFileFirst(10, 1, name, ft, full, keep))
	m1.Write(buildEOTM())

	r := NewReader(bytes.NewReader(m1.Bytes())) // no SetContinuation
	for {
		blk, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if blk.Kind != KindEntry {
			continue
		}
		h := blk.Header
		if h.Type == EntryFile {
			var buf bytes.Buffer
			n, rerr := io.Copy(&buf, r)
			if rerr != nil && rerr != io.EOF {
				t.Fatalf("read: %v", rerr)
			}
			// Only the first medium's portion is available.
			if n != int64(keep) {
				t.Errorf("partial read = %d bytes, want %d", n, keep)
			}
		}
	}

	// The reader must signal that the archive was truncated by an
	// unhandled EOTM so callers can warn the operator.
	if !r.TruncatedByEOTM() {
		t.Errorf("TruncatedByEOTM = false, want true (EOTM hit without continuation)")
	}
}
