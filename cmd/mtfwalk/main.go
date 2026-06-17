// Command mtfwalk opens an MTF archive (seekable) and prints each block the
// reader yields, plus the file size for entries. It is a diagnostic for
// verifying the reader walks a real archive end-to-end without desyncing.
package main

import (
	"fmt"
	"os"

	mtf "github.com/pbs-plus/go-mtf"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mtfwalk <file>")
		os.Exit(2)
	}
	r, err := mtf.Open(os.Args[1])
	if err != nil {
		die(err)
	}
	n := 0
	for {
		b, err := r.Next()
		if err != nil {
			fmt.Printf("(end: %v after %d blocks)\n", err, n)
			if err.Error() == "EOF" {
				return
			}
			os.Exit(1)
		}
		n++
		switch b.Kind {
		case mtf.KindMedia:
			fmt.Printf("%3d MEDIA  flb=%d\n", n, b.Tape.FLBSize)
		case mtf.KindSet:
			fmt.Printf("%3d SET    num=%d major=%d.%d\n", n, b.Set.Number, b.Set.MajorVersion, b.Set.MinorVersion)
		case mtf.KindEntry:
			h := b.Header
			extra := ""
			if h != nil {
				extra = fmt.Sprintf(" name=%q size=%d disp=%d", h.Name, h.Size, h.DisplayableSize)
			}
			fmt.Printf("%3d ENTRY  type=%d%s\n", n, b.Header.Type, extra)
		case mtf.KindSetEnd:
			fmt.Printf("%3d SETEND num=%d catalog=%v\n", n, b.ESet.SetNumber, b.Catalog != nil)
		}
	}
}

func die(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
