# Multi-media spanning

A backup data set may be split across several physical media (tapes or `.bkf`
files). An **End Of Tape Marker** (`EOTM`) marks the end of each medium. The
reader reassembles the data set transparently, whether the split falls between
entries or **in the middle of a file's data stream**.

> The MTF specification describes mid-file spanning; this library implements
> it fully.

## SetContinuation

Register a callback that supplies the next medium when the current one is
exhausted. The callback receives a [Continuation] describing the exhausted
medium — use it to prompt an operator, verify the tape label, or log the
change:

```go
files := []string{"backup-1.bkf", "backup-2.bkf", "backup-3.bkf"}
f, _ := os.Open(files[0])
r := mtf.NewReader(f)

i := 0
r.SetContinuation(func(c mtf.Continuation) (io.Reader, error) {
	i++
	if i >= len(files) {
		return nil, io.EOF // no more media
	}
	fmt.Printf("tape %d ended (%s); loading %s\n",
		c.Sequence, c.Media.Name, files[i])
	return os.Open(files[i])
})
```

[Continuation]: ../mtf.go

### Continuation fields

| Field | Type | Meaning |
| --- | --- | --- |
| `Sequence` | `int` | 1-based index of the medium that just ended. The next medium is `Sequence + 1`. |
| `Media` | `*TapeInfo` | Descriptor of the exhausted medium (name, label, MFMID, FLB size, …). May be nil if no TAPE block was parsed. |

- If the callback is `nil` (the default), an `EOTM` ends the archive (`io.EOF`).
- If the callback returns `io.EOF` or a nil reader, the archive ends.
- Return an error to abort the entire read — the error propagates from `Next`/`Read`.

### Operator prompt pattern

For physical LTO tapes where an operator must swap cartridges, the callback
can block until the next tape is loaded:

```go
r.SetContinuation(func(c mtf.Continuation) (io.Reader, error) {
	fmt.Printf("━━━ Tape %d ended: %q ━━━\n", c.Sequence, c.Media.Name)
	fmt.Printf("    Insert tape %d and press Enter.\n", c.Sequence+1)
	fmt.Scanln() // block until operator confirms
	return os.Open("/dev/nst0")
})
```

See [lto.md](lto.md) for the full LTO reading guide.

## Between-entry spanning

When the `EOTM` falls cleanly between entries, the reader consumes it, calls
the continuation to get the next medium, and the next medium's leading `TAPE`
block is yielded as a new `KindMedia`. The continuation's repeated
`SSET`/`VOLB`/`DIRB` blocks (which carry the `MTF_CONTINUATION` attribute and
exist only to restore context) are parsed silently and emit no entry.

## Mid-stream spanning

When an `EOTM` appears in the middle of a file's data stream, the reader:

1. Detects the `EOTM` at a Format Logical Block boundary (probing without
   consuming stream data) — validated by `FLA == 0` and `CBID == 0` per spec
   §5.2.9 to avoid false positives.
2. Calls the continuation to get the next medium.
3. Re-synchronizes onto the continuation `FILE` block's STAN stream (skipping
   the repeated context blocks).
4. Resumes delivering the remaining data.

The stream's declared length on the continuation is the remaining (unwritten)
portion; if `STREAM_CONTINUE` is set, the data begins at the next Format Logical
Block boundary.

A caller that *discards* (skips) a file spanning media — by calling `Next`
without reading — is also handled: the skip path follows the EOTM to the next
medium and beyond.

## Spanning-aware block sequence

For a 3-media spanned archive, `Next` yields roughly:

```
KindMedia (medium 1)  KindSet  KindEntry... KindEntry...
KindMedia (medium 2)  KindEntry... (same data set, continued)
KindMedia (medium 3)  KindEntry... KindSetEnd (catalog)
```

A continuation medium that only carries the rest of a split file yields its
`KindMedia` then the file's entry continues seamlessly — no duplicate
`KindEntry` is emitted for the same file.