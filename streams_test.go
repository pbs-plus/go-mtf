package mtf

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func streamDescriptor(typ uint32, data []byte) []byte {
	total := streamHeaderSize + len(data)
	if m := total % 4; m != 0 {
		total += 4 - m
	}
	b := make([]byte, total)
	putU32(b, stTypeOff, typ)
	putU64(b, stLengthOff, uint64(len(data)))
	copy(b[streamHeaderSize:], data)
	return b
}

// buildStreams emits a sequence of stream descriptors followed by a terminal
// SPAD whose data pads the object (preamble + streams) up to the next FLB
// block boundary. preambleLen is the byte length of the descriptor block that
// precedes the streams.
func buildStreams(preambleLen int, streams ...[]byte) []byte {
	var out bytes.Buffer
	for _, s := range streams {
		out.Write(s)
	}
	spadHeaderEnd := preambleLen + out.Len() + streamHeaderSize
	spadDataLen := testFLBSize - (spadHeaderEnd % testFLBSize)
	if spadDataLen == testFLBSize {
		spadDataLen = 0
	}
	spad := make([]byte, streamHeaderSize)
	putU32(spad, stTypeOff, StreamSPAD)
	putU64(spad, stLengthOff, uint64(spadDataLen))
	out.Write(spad)
	if spadDataLen > 0 {
		out.Write(bytes.Repeat([]byte{0}, spadDataLen))
	}
	return out.Bytes()
}

func buildFileWithStreams(name string, streams ...[]byte) []byte {
	nameOff := fileNameOff + 4
	start := nameOff + len(name) + 1
	if m := start % 4; m != 0 {
		start += 4 - m
	}
	preamble := make([]byte, start)
	writeCommon(preamble, dbFILE, uint16(start))
	putU32(preamble, fileIDOff, 1)
	putU32(preamble, fileDirIDOff, 0)
	putString(preamble, fileNameOff, nameOff, name)
	dt := encodeDateTime(time.Date(2005, 6, 1, 12, 0, 0, 0, time.Local))
	copy(preamble[fileMTimeOff:], dt[:])
	var out bytes.Buffer
	out.Write(preamble)
	out.Write(buildStreams(len(preamble), streams...))
	return out.Bytes()
}

func buildDirbWithStreams(id uint32, name string, streams ...[]byte) []byte {
	nameOff := dirbNameOff + 4
	start := nameOff + len(name) + 1
	if m := start % 4; m != 0 {
		start += 4 - m
	}
	preamble := make([]byte, start)
	writeCommon(preamble, dbDIRB, uint16(start))
	putU32(preamble, dirbIDOff, id)
	putString(preamble, dirbNameOff, nameOff, name)
	var out bytes.Buffer
	out.Write(preamble)
	out.Write(buildStreams(len(preamble), streams...))
	return out.Bytes()
}

func streamArchive(entries ...[]byte) []byte {
	var buf bytes.Buffer
	buf.Write(buildTape())
	buf.Write(buildSSET())
	buf.Write(buildVOLB("C:"))
	for _, e := range entries {
		buf.Write(e)
	}
	buf.Write(buildESET())
	return buf.Bytes()
}

// nextOfType advances r until it returns an entry of the given type.
func nextOfType(r *Reader, want EntryType) *Header {
	for {
		h, err := r.Next()
		if err != nil {
			panic(err)
		}
		if h.Type == want {
			return h
		}
	}
}

func TestNextStreamDirectory(t *testing.T) {
	nacl := []byte{0x01, 0x00, 0x04, 0x80, 0x14, 0x00, 0x00, 0x00}
	ntoi := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	dir := buildDirbWithStreams(1, "Users",
		streamDescriptor(StreamNACL, nacl),
		streamDescriptor(StreamNTOI, ntoi),
	)
	r := NewReader(bytes.NewReader(streamArchive(dir)))
	h := nextOfType(r, EntryDirectory)
	if h.Type != EntryDirectory {
		t.Fatalf("want directory, got %v", h.Type)
	}

	var types []string
	var naclRead bytes.Buffer
	for {
		s, err := r.NextStream()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, StreamTypeName(s.Type))
		if s.Type == StreamNACL {
			if s.Length != int64(len(nacl)) {
				t.Fatalf("NACL length = %d, want %d", s.Length, len(nacl))
			}
			if _, err := io.Copy(&naclRead, r); err != nil && err != io.EOF {
				t.Fatal(err)
			}
			if !bytes.Equal(naclRead.Bytes(), nacl) {
				t.Fatalf("NACL stream bytes = % x, want % x", naclRead.Bytes(), nacl)
			}
		}
	}
	if len(types) != 2 || types[0] != "NACL" || types[1] != "NTOI" {
		t.Fatalf("stream types = %v, want [NACL NTOI]", types)
	}
}

func TestNextStreamFileEnumerateMode(t *testing.T) {
	ntea := []byte("EA-DATA")
	content := bytes.Repeat([]byte("X"), 300)
	file := buildFileWithStreams("doc.txt",
		streamDescriptor(StreamNTEA, ntea),
		streamDescriptor(StreamSTAN, content),
	)
	r := NewReader(bytes.NewReader(streamArchive(file)))
	r.EnumerateStreams(true)
	nextOfType(r, EntryFile)

	var types []string
	var stan bytes.Buffer
	for {
		s, err := r.NextStream()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, StreamTypeName(s.Type))
		if s.Type == StreamSTAN {
			if s.Length != int64(len(content)) {
				t.Fatalf("STAN length = %d, want %d", s.Length, len(content))
			}
			if n, err := io.Copy(&stan, r); err != nil && err != io.EOF {
				t.Fatal(err)
			} else if n != int64(len(content)) {
				t.Fatalf("STAN read %d bytes, want %d", n, len(content))
			}
		}
	}
	if len(types) != 2 || types[0] != "NTEA" || types[1] != "STAN" {
		t.Fatalf("stream types = %v, want [NTEA STAN]", types)
	}
	if !bytes.Equal(stan.Bytes(), content) {
		t.Fatalf("STAN bytes mismatch: got %d bytes, want %d", stan.Len(), len(content))
	}
}

func TestNextStreamDefaultModeSTAN(t *testing.T) {
	content := bytes.Repeat([]byte("Z"), 100)
	file := buildFileWithStreams("f.txt",
		streamDescriptor(StreamSTAN, content))
	r := NewReader(bytes.NewReader(streamArchive(file)))
	h := nextOfType(r, EntryFile)
	if h.Size != int64(len(content)) {
		t.Fatalf("default mode Header.Size = %d, want %d", h.Size, len(content))
	}
	var got bytes.Buffer
	if _, err := io.Copy(&got, r); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), content) {
		t.Fatalf("legacy Read mismatch: got %d bytes, want %d", got.Len(), len(content))
	}
}

func TestNextStreamEnumerateThenNextAdvances(t *testing.T) {
	content := []byte("ABC")
	file1 := buildFileWithStreams("a.txt", streamDescriptor(StreamSTAN, content))
	file2 := buildFileWithStreams("b.txt", streamDescriptor(StreamSTAN, content))
	r := NewReader(bytes.NewReader(streamArchive(file1, file2)))
	r.EnumerateStreams(true)

	seen := 0
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if h.Type != EntryFile {
			continue
		}
		count := 0
		for {
			if _, err := r.NextStream(); err == io.EOF {
				break
			} else if err != nil {
				t.Fatalf("NextStream: %v", err)
			}
			count++
		}
		if count != 1 {
			t.Fatalf("entry %s: saw %d streams, want 1", h.Name, count)
		}
		seen++
	}
	if seen != 2 {
		t.Fatalf("saw %d file entries, want 2", seen)
	}
}
