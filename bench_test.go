package mtf

import (
	"bytes"
	"io"
	"testing"
)

// makeArchive builds a representative archive with N files (some with NTFS
// metadata streams) and returns its bytes. Used by allocation benchmarks.
func makeArchive(n int) []byte {
	files := make([][]byte, n)
	for i := range n {
		content := []byte("file contents number ")
		content = append(content, byte('0'+i%10))
		// Attach an NACL (security descriptor) + STAN + CSUM stream to exercise
		// the metadata-capture and stream-bucket paths.
		nacl := streamDescriptor(StreamNACL, []byte{0x01, 0x00, 0x14, 0x00, 0xAA})
		stan := streamDescriptor(StreamSTAN, content)
		csum := streamDescriptor(StreamCSUM, []byte{0x00, 0x01, 0x02, 0x03})
		files[i] = buildFileWithStreams(nameFor(i), nacl, stan, csum)
	}
	return streamArchive(files...)
}

func nameFor(i int) string {
	switch i % 3 {
	case 0:
		return "alpha.txt"
	case 1:
		return "beta.log"
	default:
		return "gamma.dat"
	}
}

// BenchmarkNext measures allocations for iterating all block headers of a
// multi-entry archive (the listing/classification hot path).
func BenchmarkNext(b *testing.B) {
	arc := makeArchive(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(arc))
		for {
			_, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkNextHeaderOnly measures the header-only path (Census-style).
func BenchmarkNextHeaderOnly(b *testing.B) {
	arc := makeArchive(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(arc))
		r.HeaderOnly()
		for {
			_, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkRead measures allocations for streaming file content.
func BenchmarkRead(b *testing.B) {
	arc := makeArchive(50)
	var buf [4096]byte
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(arc))
		for {
			blk, err := r.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
			if blk.Kind == KindEntry && blk.Header.Type == EntryFile {
				for {
					_, rerr := r.Read(buf[:])
					if rerr == io.EOF {
						break
					}
					if rerr != nil {
						b.Fatal(rerr)
					}
				}
			}
		}
	}
}

// BenchmarkCensus measures allocations for the Census helper.
func BenchmarkCensus(b *testing.B) {
	arc := makeArchive(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := NewReader(bytes.NewReader(arc))
		if _, err := r.Census(); err != nil {
			b.Fatal(err)
		}
	}
}
