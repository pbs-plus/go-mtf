// Integration harness: walk a real MTF tape via the go-tapedrive adapter.
//
// Usage: tdmtf <device>
//
// Opens the st device with go-tapedrive, wraps it with mtf.NewDriveTape, and
// walks the entire MTF structure with mtf.Reader.Next(), printing each block.
// Exercises the full bridge: ReadBlockInto + error translation (filemark/EOD)
// and SeekBlock for data-stream skipping. READ-ONLY; rewinds to BOT on exit.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/pbs-plus/go-mtf"
	"github.com/pbs-plus/go-tapedrive"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tdmtf <device>")
		os.Exit(2)
	}
	dev := os.Args[1]

	d, err := tapedrive.Open(dev)
	if err != nil {
		fail("open drive: %v", err)
	}
	// Ensure BOT start and rewind on exit.
	if err := d.Rewind(); err != nil {
		fail("rewind: %v", err)
	}
	defer func() {
		_ = d.Rewind()
		_ = d.Close()
	}()

	// The bridge: real drive -> mtf.Tape.
	r := mtf.NewReader(mtf.NewDriveTape(d))
	defer r.Close()

	st, _ := d.Status()
	fmt.Printf("drive: online=%v variable-block=%v blocksize=%d\n",
		st.Online, st.BlockSize == 0, st.BlockSize)

	var media, sets, entries, setEnds int
	var firstTape, firstSet string
	for {
		b, err := r.Next()
		if err == io.EOF {
			fmt.Println("walk: end of recorded data (io.EOF)")
			break
		}
		if err != nil {
			fail("walk: %v", err)
		}
		switch b.Kind {
		case mtf.KindMedia:
			media++
			if b.Tape != nil {
				firstTape = fmt.Sprintf("name=%q", b.Tape.Name)
			}
			fmt.Printf("[media] MTF_TAPE %s\n", firstTape)
		case mtf.KindSet:
			sets++
			if b.Set != nil {
				firstSet = fmt.Sprintf("name=%q", b.Set.Name)
			}
			fmt.Printf("[set]   MTF_SSET %s\n", firstSet)
		case mtf.KindEntry:
			entries++
			if b.Header != nil {
				fmt.Printf("[entry] %s %s\n", entryTypeStr(b.Header.Type), quote(b.Header.Name))
			} else {
				fmt.Println("[entry] (no header)")
			}
		case mtf.KindSetEnd:
			setEnds++
			hasCat := b.Catalog != nil
			fmt.Printf("[setend] MTF_ESET catalog=%v\n", hasCat)
		default:
			fmt.Printf("[unknown kind %d]\n", b.Kind)
		}
		if entries > 200 {
			fmt.Println("walk: capped at 200 entries, stopping")
			break
		}
	}

	fmt.Printf("\nsummary: media=%d sets=%d entries=%d setEnds=%d\n",
		media, sets, entries, setEnds)
	fmt.Println("OK")
}

func quote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return fmt.Sprintf("%q", s)
}

func entryTypeStr(t mtf.EntryType) string {
	switch t {
	case mtf.EntryFile:
		return "file"
	case mtf.EntryDirectory:
		return "dir "
	case mtf.EntryVolume:
		return "vol "
	default:
		return fmt.Sprintf("type(%d)", int(t))
	}
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "tdmtf: "+format+"\n", a...)
	os.Exit(1)
}
