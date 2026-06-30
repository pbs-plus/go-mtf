package mtf

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

type mockTape struct {
	data  []byte
	pos   int64
	bsize int
}

func newMockTape(blocks [][]byte, bsize int) *mockTape {
	m := &mockTape{bsize: bsize}
	for _, b := range blocks {
		padded := make([]byte, bsize)
		copy(padded, b)
		m.data = append(m.data, padded...)
	}
	return m
}

func (m *mockTape) ReadBlock(dst []byte) (int, error) {
	off := m.pos * int64(m.bsize)
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	end := min(off+int64(m.bsize), int64(len(m.data)))
	n := copy(dst, m.data[off:end])
	m.pos++
	return n, nil
}

func (m *mockTape) SeekBlock(p int64) error   { m.pos = p; return nil }
func (m *mockTape) TellBlock() (int64, error) { return m.pos, nil }

func (m *mockTape) EOM() error {
	m.pos = int64(len(m.data)) / int64(m.bsize)
	return nil
}

func TestReadSetMapSpecCompliant(t *testing.T) {
	const bsize = 512

	esetStreamOff := uint16(88)
	eset := make([]byte, bsize)
	writeCommon(eset, dbESET, esetStreamOff)
	eset[dbStrTypeOff] = 1
	binary.LittleEndian.PutUint16(eset[esetSeqOff:], 1)
	binary.LittleEndian.PutUint16(eset[esetSetOff:], 1)
	setChecksum(eset)

	tsmpPayload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	tsmpHdr := make([]byte, streamHeaderSize)
	binary.LittleEndian.PutUint32(tsmpHdr[stTypeOff:], StreamTSMP)
	binary.LittleEndian.PutUint64(tsmpHdr[stLengthOff:], uint64(len(tsmpPayload)))
	setStreamChecksum(tsmpHdr)
	copy(eset[esetStreamOff:], tsmpHdr)
	copy(eset[esetStreamOff+uint16(streamHeaderSize):], tsmpPayload)

	spadOff := esetStreamOff + uint16(streamHeaderSize) + uint16(len(tsmpPayload))
	for spadOff%4 != 0 {
		spadOff++
	}
	spadHdr := make([]byte, streamHeaderSize)
	binary.LittleEndian.PutUint32(spadHdr[stTypeOff:], StreamSPAD)
	setStreamChecksum(spadHdr)
	copy(eset[spadOff:], spadHdr)

	eotm := make([]byte, bsize)
	writeCommon(eotm, dbEOTM, 0)
	binary.LittleEndian.PutUint64(eotm[eotmLastESETPBAOff:], 4)
	setChecksum(eotm)

	blocks := [][]byte{
		buildTape(),
		buildSSET(),
		buildVOLB("C:"),
		eset,
		make([]byte, bsize),
		eotm,
		make([]byte, bsize),
	}

	mt := newMockTape(blocks, bsize)
	id, payload, err := ReadSetMapRaw(mt)
	if err != nil {
		t.Fatalf("ReadSetMapRaw: %v", err)
	}
	if payload == nil {
		t.Fatal("ReadSetMapRaw returned nil payload for spec-compliant ESET-attached catalog")
	}
	if id != StreamTSMP {
		t.Errorf("stream ID = 0x%X, want TSMP (0x%X)", id, StreamTSMP)
	}
	if !bytes.Equal(payload, tsmpPayload) {
		t.Errorf("payload = %x, want %x", payload, tsmpPayload)
	}
}

func TestReadSetMapBackupExec(t *testing.T) {
	const bsize = 512

	smp2Payload := []byte{0xCA, 0xFE, 0xBA, 0xBE}
	smp2 := make([]byte, bsize)
	binary.LittleEndian.PutUint32(smp2[stTypeOff:], StreamSM2P)
	binary.LittleEndian.PutUint64(smp2[stLengthOff:], uint64(len(smp2Payload)))
	setStreamChecksum(smp2)
	copy(smp2[streamHeaderSize:], smp2Payload)

	eset := make([]byte, bsize)
	writeCommon(eset, dbESET, 0)
	eset[dbStrTypeOff] = 1
	setChecksum(eset)

	eotm := make([]byte, bsize)
	writeCommon(eotm, dbEOTM, 0)
	binary.LittleEndian.PutUint64(eotm[eotmLastESETPBAOff:], 5)
	setChecksum(eotm)

	blocks := [][]byte{
		buildTape(),
		buildSSET(),
		buildVOLB("C:"),
		smp2,
		eset,
		make([]byte, bsize),
		eotm,
		make([]byte, bsize),
	}

	mt := newMockTape(blocks, bsize)
	id, payload, err := ReadSetMapRaw(mt)
	if err != nil {
		t.Fatalf("ReadSetMapRaw: %v", err)
	}
	if payload == nil {
		t.Fatal("ReadSetMapRaw returned nil payload for Backup Exec standalone catalog")
	}
	if id != StreamSM2P {
		t.Errorf("stream ID = 0x%X, want SMP2 (0x%X)", id, StreamSM2P)
	}
	if !bytes.Equal(payload, smp2Payload) {
		t.Errorf("payload = %x, want %x", payload, smp2Payload)
	}
}
