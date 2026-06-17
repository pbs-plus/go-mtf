# Quick start

## Install

```
go get github.com/pbs-plus/go-mtf
```

The package import path is `github.com/pbs-plus/go-mtf` (package name `mtf`).

## Open and list

```go
package main

import (
	"fmt"
	"io"
	"log"

	mtf "github.com/pbs-plus/go-mtf"
)

func main() {
	r, err := mtf.Open("backup.bkf")
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()

	for {
		b, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatal(err)
		}
		switch b.Kind {
		case mtf.KindMedia:
			fmt.Println("medium:", b.Tape.Name, "seq", b.Tape.Sequence)
		case mtf.KindSet:
			fmt.Println("data set:", b.Set.Name)
		case mtf.KindEntry:
			fmt.Println(" ", b.Header.Name)
		case mtf.KindSetEnd:
			fmt.Println("data set ended")
		}
	}
}
```

## Extract a file

`Reader.Read` implements `io.Reader` over the current file entry's standard
data stream. It is repositioned automatically each time `Next` returns a
`KindEntry` block with `Header.Type == mtf.EntryFile`:

```go
for {
	b, err := r.Next()
	if err == io.EOF { break }
	if err != nil { log.Fatal(err) }
	if b.Kind != mtf.KindEntry || b.Header.Type != mtf.EntryFile {
		continue
	}
	out, err := os.Create(filepath.Base(b.Header.Name))
	if err != nil { log.Fatal(err) }
	if _, err := io.Copy(out, r); err != nil { log.Fatal(err) }
	out.Close()
}
```

Calling `Next` again without reading the current entry automatically discards
its remaining content.

## Seekable vs. streaming sources

`mtf.Open` returns a reader backed by an `*os.File`, which is seekable. When
the source implements `io.Seeker`, the reader skips unwanted data (e.g. when
listing or classifying) with a single `Seek` per skip instead of reading the
bytes. This is dramatically faster on large archives when you only need
headers.

For a non-seekable source (a pipe, network stream, gzip reader), pass it to
`mtf.NewReader`; skips are done by reading and discarding.

## Skip content with HeaderOnly

`Reader.HeaderOnly()` puts the reader into a mode that skips metadata-stream
*data* and entry-name string construction, keeping only scalar fields and
flags. It is the mode `Census` uses to classify a cartridge cheaply. See
[census.md](census.md) and [performance.md](performance.md).
