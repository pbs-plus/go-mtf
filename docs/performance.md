# Performance & allocations

## What is reused

| State | How |
| --- | --- |
| `Block` / `Header` | Stored on the `Reader`, overwritten each `Next` (not reallocated) |
| `TapeInfo` / `SetInfo` / `ESetInfo` | Embedded values (not heap-allocated pointers) |
| `SecurityDescriptor` / `Streams` / `SparseExtents` | Backing arrays truncated to len 0 each entry (cap kept) |
| String decode buffers | `strBuf` (Name path), `scratchBuf` (other fields), `strU16` (UTF-16) |

## String construction

A file's full path (`<volume>/<dir>/<name>`) is built in one pass into the
reusable `strBuf`, decoding the raw MTF name field directly via
`appendDecodeString`, and converted to a Go string exactly once at the field.
This eliminates the intermediate name string and its buffer.

## HeaderOnly is zero-alloc per entry

In `HeaderOnly` mode, `Name`/`VolumeLabel`/`MachineName` construction is
skipped entirely (only scalar fields and flags are populated), and metadata
bytes are skipped. So cartridge classification is **zero-allocation per entry**.

## Read fills the caller's buffer

`Read` fills the caller's `[]byte` directly; stream data is never copied into an
intermediate library buffer. When the source is seekable, skips use a single
`Seek`.

## The reuse contract

Because `Block` and `Header` are reused across `Next` calls, callers that retain
an entry across iterations **must copy** the fields they keep:

```go
name := strings.Clone(b.Header.Name) // safe across subsequent Next() calls
```

Other string fields (`Volume`, `VolumeLabel`, `MachineName`) are regular Go
strings (allocated per call but infrequent) and are safe to retain.
