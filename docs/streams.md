# Data streams

Every MTF object (volume/directory/file) carries a sequence of **data streams**
after its descriptor block. The reader walks these transparently during `Next`
and materializes the metadata a faithful extraction needs onto the `Header`,
then positions file content for `Read`.

## Stream order

A typical file's streams look like:

```
NACL   ← NTFS security descriptor
STAN   ← standard data (the file's content)
CSUM   ← checksum of the content
SPAD   ← terminal padding (marks end of object)
```

Metadata streams *before* STAN are available when the entry is returned. Streams
*after* STAN (e.g. CSUM) are appended to `Header.Streams` as the content is read
or skipped.

## The named streams

| Stream | Header field | Notes |
| --- | --- | --- |
| `NACL` | `SecurityDescriptor` | Self-relative `SECURITY_DESCRIPTOR` (Win32 `BackupRead`) |
| `NTEA` | `ExtendedAttributes` | NT extended attributes |
| `SPAR` | `SparseExtents` | Sparse map; one entry per SPAR stream |
| `STAN` | — (delivered via `Read`) | Standard data — the file's content |

## The generic bucket

Every stream that has no dedicated field is preserved verbatim in
`Header.Streams` so **no stream is ever dropped**:

```go
type StreamData struct {
	Type uint32   // four-byte type code; compare against mtf.Stream* constants
	Data []byte   // raw stream bytes
}

const (
	StreamSTAN uint32 = 0x4E415453 // standard data
	StreamNTOI uint32 = 0x494F544E // NT object id
	StreamCSUM uint32 = 0x4D555343 // checksum
	StreamADAT uint32 = 0x54414441 // NT data (alternate data streams)
	StreamNTQU uint32 = 0x5551544E // NT quota
	// ... see mtf.go for the full list
)
```

Use `mtf.StreamTypeName(s.Type)` for a human-readable label.

## Sparse files

A sparse file is signalled by the `STREAM_IS_SPARSE` bit on a STAN header whose
Stream Length is zero (MTF spec §6.2.1.7). The actual content is carried by one
or more following `SPAR` streams, collected into `Header.SparseExtents`:

```go
type SparseExtent struct {
	Offset int64   // logical byte offset where Data begins
	Data   []byte  // non-hole content at Offset
}
```

`Read` reconstructs the logical file: it places each extent's `Data` at its
`Offset` and zero-fills the gaps. `Header.Size` is the hole-filled logical
length, computed from the extents.

## Compression & encryption

When STAN carries `STREAM_COMPRESSED` / `STREAM_ENCRYPTED`, the raw stream is a
sequence of 24-byte frame headers (`MTF_CMP_HDR` §6.4.1 / `MTF_ENC_HDR` §6.5.1),
each wrapping one independently-decoded block.

**Compression** uses the single spec-defined algorithm `MTF_LZS221`
(`0x0ABE`, Stac LZS, ANSI X3.241-1994). `Read` transparently peels the frames
and decompresses each with the built-in LZS decoder, returning the original
bytes. The anti-expansion case (data stored uncompressed inside a frame when
compression would expand it) is handled.

**Encryption**: the MTF spec defines *no* data-encryption cipher ("Data
Encryption has not been defined", Appendix D) — only the `MTF_ENC_HDR` frame
layout. So encrypted streams are decoded through a caller-supplied decryptor:

```go
type Decryptor func(algo uint16, encrypted []byte) ([]byte, error)

r.SetDecryptor(func(algo uint16, ct []byte) ([]byte, error) {
	return myDecipher(algo, ct) // vendor-specific
})
```

Without a decryptor, `Read` of an encrypted stream returns `mtf.ErrEncrypted`.
When a stream is both compressed and encrypted, data was compressed first then
encrypted, so the reader decrypts each frame then decompresses it.

`Header.Size` is the logical (uncompressed) size for compressed/encrypted
streams (taken from `DisplayableSize`); `Header.DisplayableSize` always carries
the descriptor's recorded size.

> The production dataset contains no compressed or encrypted files. LZS is
> validated via Go round-trip tests; real samples would confirm final parity.

## HeaderOnly mode

`Reader.HeaderOnly()` skips metadata-stream *data* (NACL/NTEA/SPAR bytes are
not materialized) and entry-name string construction. Stream *headers* are still
parsed, so `Compressed`/`Encrypted`/`Sparse`/`Size`/`DisplayableSize` remain
accurate. This makes classification near-zero-allocation. STAN content is never
delivered in this mode.
