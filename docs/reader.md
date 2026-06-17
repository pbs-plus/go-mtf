# Reader API

The library exposes MTF as a **typed block iterator**. `Reader.Next` returns a
`*Block` describing one structural element; the block's `Kind` tells you what
was encountered and which fields are populated.

```go
type Reader struct { /* fields unexported */ }

func Open(name string) (*Reader, error)
func NewReader(r io.Reader) *Reader
func (r *Reader) Next() (*Block, error)
func (r *Reader) Read(p []byte) (int, error)
func (r *Reader) Close() error
```

## Block

```go
type Block struct {
	Kind    BlockKind
	Tape    *TapeInfo  // Kind == KindMedia
	Set     *SetInfo   // Kind == KindSet
	Header  *Header    // Kind == KindEntry
	ESet    *ESetInfo  // Kind == KindSetEnd
	Catalog *Catalog   // Kind == KindSetEnd (nil if the set had no catalog)
}
```

A single `Block` (and `Header`) are **reused** across `Next` calls — every
field is overwritten on the next iteration. If you need to retain an entry
across iterations, copy the fields you keep (e.g. `strings.Clone(h.Name)`).

| Kind | Meaning | Populated fields |
| --- | --- | --- |
| `KindMedia` | An `MTF_TAPE` descriptor: a physical medium started | `Tape` |
| `KindSet` | An `MTF_SSET` descriptor: a data set started | `Set` |
| `KindEntry` | An extractable object (`VOLB`/`DIRB`/`FILE`) | `Header` |
| `KindSetEnd` | An `MTF_ESET` descriptor: a data set ended | `ESet`, `Catalog` |

The medium's role is self-evident from the block sequence:

- A medium with `KindEntry` blocks but **no** trailing `KindSetEnd` is
  **data-only** — its data set continues on the next medium.
- A medium whose `KindSetEnd` carries a `Catalog` with no file-data entries is
  **catalog-heavy**.
- A medium with both entries and a set-end is the **normal** case.

## Header

`Header` is populated for every `KindEntry` block. Fields common to all entry
types:

| Field | Meaning |
| --- | --- |
| `Type` | `EntryFile`, `EntryDirectory`, or `EntryVolume` |
| `Name` | Fully-resolved path (`<volume>/<dir>/<name>`, `/`-separated) |
| `ModTime`, `AccessTime`, `CreateTime`, `BirthTime` | Times, as recorded |
| `Attributes` | Type-specific DBLK attributes |
| `BlockAttributes` | Common `MTF_DB_HDR` attributes (test with `Attr*`) |
| `OSID` | Operating-system identifier |
| `SetNumber` | Data-set number |
| `Volume` | Source volume/device |
| `VolumeLabel`, `MachineName` | Volume entries |
| `FileID`, `DirID` | MTF object identifiers |
| `DisplayableSize` | Object size from the common descriptor |

### File entries — standard data stream

These are meaningful for `EntryFile`:

| Field | Meaning |
| --- | --- |
| `Size` | Logical bytes delivered by `Read` (hole-filled for sparse; uncompressed logical size for compressed) |
| `Compressed` / `Encrypted` / `Sparse` | Decoded stream flags |
| `CompressionAlgorithm` / `EncryptionAlgorithm` | Registered algorithm IDs |
| `StreamChecksum` | STAN header checksum field |

### Metadata (auto-materialized)

These are populated by `Next` for every entry (see
[streams.md](streams.md)):

| Field | Meaning |
| --- | --- |
| `SecurityDescriptor` | Raw NTFS security descriptor (NACL) |
| `ExtendedAttributes` | Raw NT extended attributes (NTEA) |
| `SparseExtents` | Parsed sparse map (`[]SparseExtent`) |
| `Streams` | Every other stream as `[]StreamData{Type, Data}` |

## Metadata accessors

```go
func (r *Reader) Tape() *TapeInfo           // most recent TAPE, or nil
func (r *Reader) Set() *SetInfo             // most recent SSET, or nil
func (r *Reader) ESet() *ESetInfo           // most recent ESET, or nil
func (r *Reader) Catalog() *Catalog         // parsed MBC of last completed set
func (r *Reader) CorruptObjects() uint32    // corrupt-object count from ESET
func (r *Reader) MediaSequence() int        // 1-based current medium index
func (r *Reader) Position() int64           // bytes consumed from the stream
```

`TapeInfo`, `SetInfo`, `ESetInfo` are the parsed descriptor blocks. See
[spec.md](spec.md) for the exact field each carries.

## Checksums

```go
func (r *Reader) VerifyChecksum() bool
func (r *Reader) Checksum() (stored, computed uint16)
```

Validates the 16-bit word-wise XOR checksum over the common descriptor header
(`MTF_DB_HDR`). Call immediately after `Next` returns. Some writers emit a zero
checksum, so treat the result as advisory.

## Mode setters

```go
func (r *Reader) HeaderOnly()               // skip metadata data + names
func (r *Reader) SetContinuation(func() (io.Reader, error))  // see spanning.md
func (r *Reader) SetDecryptor(Decryptor)    // see streams.md
```
