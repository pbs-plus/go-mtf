package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	mtf "github.com/pbs-plus/go-mtf"
	"github.com/pbs-plus/go-tape"
)

// hideSkip wraps gotape.Reader exposing only Read (NOT SkipForward), forcing
// go-mtf to use the byte-read-loop skip path instead of block LOCATE. Used to
// isolate whether the non-deterministic desync is in SkipForward or elsewhere.
type hideSkip struct{ r *gotape.Reader }

func (h hideSkip) Read(p []byte) (int, error) { return h.r.Read(p) }

func main() {
	dev := flag.String("dev", "/dev/sg3", "sg device")
	noskip := flag.Bool("no-skip", false, "hide SkipForward (force read-loop path)")
	flag.Parse()
	d, err := gotape.Open(*dev)
	die(err)
	defer func() { _ = d.Close() }()
	die(d.WaitUntilReady(ptrDur(180)))
	die(d.Rewind())
	die(d.SetVariableBlock())
	r := gotape.NewReader(d)
	var src interface{ Read([]byte) (int, error) } = r
	if *noskip {
		src = hideSkip{r}
		fmt.Println("MODE: no-skip (read-loop path)")
	} else {
		fmt.Println("MODE: block-skip (LOCATE path)")
	}
	mr := mtf.NewReader(src)
	n := 0
	for {
		b, err := mr.Next()
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
			fmt.Printf("%3d SET    num=%d\n", n, b.Set.Number)
		case mtf.KindEntry:
			h := b.Header
			if h != nil {
				fmt.Printf("%3d ENTRY  type=%d name=%q sz=%d\n", n, h.Type, h.Name, h.Size)
			}
		case mtf.KindSetEnd:
			fmt.Printf("%3d SETEND\n", n)
		}
	}
}

func die(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERR:", err)
		os.Exit(1)
	}
}
func ptrDur(sec int) *time.Duration {
	d := time.Duration(sec) * time.Second
	return &d
}
