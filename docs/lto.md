# Reading LTO tapes

This guide covers using `go-mtf` to read MTF-format data directly from LTO
(and other linear tape) drives under Linux.

## Tape devices

Linux exposes tape drives as character devices under `/dev/`:

| Device | Behavior |
| --- | --- |
| `/dev/nst0` | **Non-rewinding** — use this. After close the tape stays positioned. |
| `/dev/st0` | Auto-rewinds on close — almost never what you want. |

The `0` is the drive number; `/dev/nst1` is the second drive, etc.

**Always use the non-rewinding device.** MTF archives start at the beginning
of the tape, and `NewReader` reads from the current position.

## Opening a tape

```go
f, err := os.Open("/dev/nst0")
if err != nil {
    log.Fatal(err)
}
defer f.Close()

r := mtf.NewReader(f)
```

`/dev/nst0` implements `io.Reader` but not `io.Seeker`, so the library falls
back to read-based skipping (no seek optimisation). This is fine for sequential
tape reads — the data flows past the head once.

## Rewinding and positioning

Before starting, rewind the tape:

```bash
mt -f /dev/nst0 rewind
```

Programmatically:

```go
// #include <sys/mtio.h>
// #define MTREW  Mtop{MTIOCTOP, MTREW, 1}
import "C"
// ... ioctl(f.Fd(), C.MTIOCTOP, ...) — see mtio(4)
```

For most use cases, running `mt rewind` before the Go program is simpler.

### Skipping filemarks

MTF archives are preceded by a tape filemark. If the tape has multiple
backups, skip between them with:

```bash
mt -f /dev/nst0 fsf 1   # forward space 1 filemark
```

## Identifying a tape

Given a single random tape, you can determine which media family it belongs to
and how many tapes you need for a full restore:

```go
f, _ := os.Open("/dev/nst0")
r := mtf.NewReader(f)

// Read just the first block (TAPE) to identify the cartridge.
blk, _ := r.Next()
if blk.Kind == mtf.KindMedia {
    fmt.Printf("tape %q, family 0x%08X\n", blk.Tape.Name, blk.Tape.MFMID)
}

// Continue to the end of the data set to get the catalog.
for {
    blk, err := r.Next()
    if err == io.EOF { break }
    if err != nil { log.Fatal(err) }
    if blk.Kind == mtf.KindSetEnd && blk.Catalog != nil {
        // The Set Map on this tape lists every data set and which
        // tape each starts on.
        if sm := blk.Catalog.SetMap; sm != nil {
            fmt.Printf("media family 0x%08X, %d data sets\n", sm.MediaFamilyID, len(sm.Entries))
            for _, ds := range sm.Entries {
                fmt.Printf("  set %q on tape %d (%d files, %d dirs)\n",
                    ds.Name, ds.MediaSeq, ds.NumFiles, ds.NumDirectories)
            }
        }
    }
}

// Or use the Family() helper for a quick summary.
f2 := r.Family()
fmt.Printf("tape %d of %d\n", f2.TapeSequence, f2.TotalTapes)
```

`Family()` is available after the first `KindMedia` block. On a data-only
cartridge (no catalog), `TotalTapes` will be 0 and `SetMap` will be nil;
the last (catalog) cartridge has the complete Set Map.

## Multi-tape spanning

When a backup spans multiple tapes, the reader calls the continuation
callback when it encounters an `EOTM`. The callback should:

1. **Prompt the operator** to swap the tape.
2. **Open the new tape device** (the same `/dev/nst0` after a physical swap).
3. **Return the reader.**

```go
r.SetContinuation(func(c mtf.Continuation) (io.Reader, error) {
    // Tell the operator what happened.
    fmt.Printf("\n━━━ Tape %d ended", c.Sequence)
    if c.Media != nil {
        fmt.Printf(" (%s)", c.Media.Name)
    }
    fmt.Println()

    // Close the old tape and wait for the operator.
    f.Close()

    fmt.Printf("Insert tape %d and press Enter: ", c.Sequence+1)
    fmt.Scanln()

    // Rewind and open the new tape.
    exec.Command("mt", "-f", "/dev/nst0", "rewind").Run()
    newF, err := os.Open("/dev/nst0")
    if err != nil {
        return nil, fmt.Errorf("open next tape: %w", err)
    }
    return newF, nil
})
```

The callback blocks until the new tape is available. The library re-reads the
continuation blocks (`TAPE`/`SSET`/`VOLB`/`DIRB` with the continuation bit)
and resumes exactly where the previous medium left off — even mid-file.

### Verifying the correct tape

The `Continuation.Media` field carries the `MFMID` (Media Family ID) and
`Sequence` number of the tape that just ended. Use them to verify the operator
loaded the right cartridge:

```go
r.SetContinuation(func(c mtf.Continuation) (io.Reader, error) {
    f.Close()

    for {
        fmt.Printf("Insert tape %d (family 0x%08X) and press Enter: ",
            c.Sequence+1, c.Media.MFMID)
        fmt.Scanln()

        exec.Command("mt", "-f", "/dev/nst0", "rewind").Run()
        newF, err := os.Open("/dev/nst0")
        if err != nil {
            fmt.Println("error:", err)
            continue // retry
        }

        // Peek at the new tape's TAPE block to verify it's the right one.
        newR := mtf.NewReader(newF)
        blk, err := newR.Next()
        if err != nil || blk.Kind != mtf.KindMedia {
            fmt.Println("not a valid MTF tape, try again")
            newF.Close()
            continue
        }
        if blk.Tape.MFMID != c.Media.MFMID || blk.Tape.Sequence != c.Media.Sequence+1 {
            fmt.Printf("wrong tape: got family 0x%08X seq %d, want family 0x%08X seq %d\n",
                blk.Tape.MFMID, blk.Tape.Sequence,
                c.Media.MFMID, c.Media.Sequence+1)
            newF.Close()
            continue
        }

        // Correct tape — hand the reader to the library.
        // The library will re-read the TAPE block, so we don't need to
        // put the tape back to position 0. But we must ensure it's rewound
        // because NewReader reads from the current position.
        newF.Close()
        exec.Command("mt", "-f", "/dev/nst0", "rewind").Run()
        finalF, _ := os.Open("/dev/nst0")
        return finalF, nil
    }
})
```

## Full example: dump all files from tape

```go
package main

import (
    "fmt"
    "io"
    "os"
    "os/exec"

    "github.com/pbs-plus/go-mtf"
)

func main() {
    exec.Command("mt", "-f", "/dev/nst0", "rewind").Run()
    f, err := os.Open("/dev/nst0")
    if err != nil {
        fatal(err)
    }

    r := mtf.NewReader(f)
    r.SetContinuation(promptForTape)

    for {
        blk, err := r.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            fatal(err)
        }
        switch blk.Kind {
        case mtf.KindEntry:
            h := blk.Header
            fmt.Println(h.Name)
            if h.Type == mtf.EntryFile {
                out, _ := os.Create(h.Name) // naive; use filepath.Join in practice
                io.Copy(out, r)
                out.Close()
            }
        case mtf.KindSetEnd:
            if blk.Catalog != nil {
                fmt.Println("(catalog present)")
            }
        }
    }
}

func promptForTape(c mtf.Continuation) (io.Reader, error) {
    fmt.Printf("\n━━━ Tape %d ended", c.Sequence)
    if c.Media != nil {
        fmt.Printf(" (%s)", c.Media.Name)
    }
    fmt.Println()

    fmt.Print("Insert next tape and press Enter: ")
    fmt.Scanln()

    exec.Command("mt", "-f", "/dev/nst0", "rewind").Run()
    return os.Open("/dev/nst0")
}

func fatal(err error) {
    fmt.Fprintln(os.Stderr, err)
    os.Exit(1)
}
```

## Tape device notes

### Block size

LTO drives operate in **variable block mode** by default (the Linux `st`
driver negotiates this). MTF uses the block size declared in the TAPE
descriptor (`TapeInfo.FLBSize`). The library handles framing internally —
`NewReader` works directly on the raw device stream.

If the drive is in fixed-block mode, set variable mode first:

```bash
mt -f /dev/nst0 setblk 0
```

### Buffering and streaming

Tape drives deliver data at tape speed (~140 MB/s for LTO-8, ~300 MB/s for
LTO-9). The kernel `st` driver buffers in fixed-size kernel buffers. For maximum
throughput:

- Use `HeaderOnly()` for classification passes (reads only block headers).
- For extraction, let `Read()` stream data through without seeking — seeking is
  not possible on tape anyway.

### Multiple data sets

A single tape may contain multiple MTF data sets (backups). Each is delimited
by a `KindSet`/`KindSetEnd` pair. The reader walks all of them sequentially.
To process only one data set, check `blk.Set.Number` and break after its
`KindSetEnd`.

### Errors

Tape I/O errors surface as `*os.PathError` or `*os.SyscallError` from the
underlying `os.File`. The most common:

| Error | Cause |
| --- | --- |
| `ENOSPC` | Tape is full (the `EOTM` in the MTF stream handles this gracefully) |
| `EIO` | Read error — bad spot on tape, dirty head, etc. |
| `ENOENT` | Device not found — tape drive offline or no tape loaded |

For production use, wrap `Next`/`Read` in a retry loop with exponential
backoff for transient `EIO` errors.