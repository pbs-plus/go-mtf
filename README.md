# go-mtf

[![Go Reference](https://pkg.go.dev/badge/github.com/pbs-plus/go-mtf.svg)](https://pkg.go.dev/github.com/pbs-plus/go-mtf)

A pure-Go library and command-line tool for reading **Microsoft Tape Format
(MTF)** streams, the format produced by `NTBACKUP.EXE` and commonly found in
`.bkf` backup files.

This is a port of Ivo van Poorten / geocar's [`mtftar`](https://github.com/geocar/mtftar)
to idiomatic Go. It supports listing and extracting file data from MTF/BKF
streams. It is distributed under the GPLv2 (or later), as a derivative of
mtftar.

## Library

The [`mtf`](https://pkg.go.dev/github.com/pbs-plus/go-mtf) package exposes MTF
as a typed block iterator. `Reader.Next` returns a `*Block` whose `Kind`
tells you what was encountered — a medium starting, a data set starting, an
extractable entry, or a data set ending (with its catalog). This makes a
medium's role explicit: one with entries but no trailing data-set-end is
data-only (its set continues on the next medium); one whose data-set-end carries
a catalog with no file-data entries is catalog-heavy; one with both is normal.

```go
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
    case mtf.KindMedia:   // b.Tape: sequence, family ID, catalog type
    case mtf.KindSet:     // b.Set: data-set metadata
    case mtf.KindEntry:   // b.Header is fully materialized
        fmt.Println(b.Header.Name)
        if b.Header.Type == mtf.EntryFile {
            if _, err := io.Copy(os.Stdout, r); err != nil {
                log.Fatal(err)
            }
        }
    case mtf.KindSetEnd:  // b.ESet + b.Catalog (the MBC, nil if none)
    }
}
```

| Block kind     | Meaning                                                         |
| -------------- | --------------------------------------------------------------- |
| `mtf.KindMedia`  | A medium (`TAPE`) started; `Block.Tape` holds its metadata.   |
| `mtf.KindSet`    | A data set (`SSET`) started; `Block.Set` holds its metadata.  |
| `mtf.KindEntry`  | An extractable object (`VOLB`/`DIRB`/`FILE`); read data via `Reader.Read`. |
| `mtf.KindSetEnd` | A data set (`ESET`) ended; `Block.Catalog` carries any catalog. |

`Reader.Tape()` and `Reader.Set()` expose metadata from the `TAPE` and `SSET`
descriptor blocks respectively. `Reader.ESet()` exposes the most recent
end-of-set (`ESET`) metadata (corrupt-object count, data-set number, write
date) after a data set has ended.

### Entry & block metadata

`Header` carries the full descriptor detail per entry: modification/access/
creation/**birth** times, type-specific `Attributes`, the source `Volume`, the
volume **label** and **machine name** (volume entries), the MTF object IDs, and
the **Displayable Size** from the common descriptor block. For file entries it
also reports the standard data stream's properties:

- `CompressionAlgorithm` / `EncryptionAlgorithm` — registered algorithm IDs,
- `Compressed` / `Encrypted` / `Sparse` — decoded stream flags,
- `StreamChecksum` — the stream header checksum field.

> **Note:** compression and decryption are *not* performed by the library —
> these fields let a caller detect streams that need external decoding. Raw
> stream bytes are delivered as stored. Sparse files **are** transparently
> reconstructed (holes zero-filled) by `Read`.

### Metadata streams (auto-materialized)

`Reader.Next()` transparently walks each object's data streams and
materializes the metadata a faithful extraction needs directly onto the
returned `Header`. The caller never deals with stream types, lengths or
alignment:

- `Header.SecurityDescriptor` — the raw NTFS security descriptor (NACL
  stream), a self-relative `SECURITY_DESCRIPTOR` as produced by the Win32
  `BackupRead` API. Present on both file and directory entries.
- `Header.ExtendedAttributes` — the raw NT extended-attribute data (NTEA
  stream).
- `Header.SparseExtents` — the parsed sparse map (`[]SparseExtent`, one per
  SPAR stream) for sparse files; `Read` zero-fills the holes.

```go
for {
	h, err := r.Next()
	if err == io.EOF { break }
	if h.Type == mtf.EntryFile {
		io.Copy(out, r)            // file content (STAN)
	}
	if h.SecurityDescriptor != nil {
		_ = h.SecurityDescriptor    // NTFS ACL, ready to convert
	}
}
```

Other streams (object IDs, quotas, alternate data, per-stream checksums) have
no pxar equivalent and are skipped. File content is streamed lazily through
`Read` (spanning-aware); only the small metadata streams are buffered into
the `Header`.

### Checksum verification

`Reader.VerifyChecksum()` validates the MTF common-block header (`MTF_DB_HDR`)
checksum (16-bit word-wise XOR over the header, per the spec) of the current
block, for advisory corruption detection. `Reader.Checksum()` returns both the
stored and recomputed values. Some writers emit a zero checksum, so treat the
result as advisory.

### Media Based Catalog

MTF defines a *Media Based Catalog* (MBC) written as data streams on the
End-of-Set (`ESET`) block (spec section 7). It has two parts:

* **File/Directory Detail** (`TFDD` stream) — a per-data-set index of every
  volume, directory and file, each annotated with the media sequence number and
  Format Logical Address of its descriptor block.
* **Set Map** (`TSMP` stream) — a *cumulative* index of every data set in the
  whole Media Family (one entry per backup/host, each followed by its source
  volumes and machine name). The Set Map on the last cartridge is the most
  complete; it is the structure to consult for "which backups live in this
  family and on which media".

After a data set ends, `Reader.Catalog()` returns the parsed catalog as a
`*mtf.Catalog` (`SetMap *SetMap`, `FDD []CatalogEntry`). It is `nil` when no
MBC streams were present.

The standard Type 1 binary layouts are parsed. A vendor may carry a
non-standard payload inside the standard stream envelope (for example a Backup
Exec XML catalog in a `TFDD` stream); in that case the parsed fields are left
empty and the raw stream payload is exposed as `Catalog.RawFDD`/
`Catalog.RawSetMap` for a vendor-specific parser:

```go
for {
	h, err := r.Next()
	if err == io.EOF { break }
	if err != nil { log.Fatal(err) }
	_ = h
}
c := r.Catalog()
if c != nil && c.SetMap != nil {
	for _, ds := range c.SetMap.Entries {
		for _, vol := range ds.Volumes {
			fmt.Println("host:", vol.MachineName, "volume:", vol.Name)
		}
	}
}
```

### Spanning multiple media

A backup data set may be split across several physical media (tapes or `.bkf`
files) — an *End Of Tape Marker* (`EOTM`) marks the end of each medium. Register
a continuation callback with `Reader.SetContinuation` to feed the next medium
when the current one is exhausted. Spanning is handled transparently whether
the split falls between entries or in the middle of a file's data stream (the
file is reassembled across the media boundary):

```go
files := []string{"backup-1.bkf", "backup-2.bkf", "backup-3.bkf"}
f, err := os.Open(files[0])
// ...
r := mtf.NewReader(f)
i := 0
r.SetContinuation(func() (io.Reader, error) {
    i++
    if i >= len(files) {
        return nil, io.EOF // no more media
    }
    return os.Open(files[i])
})
```

`Reader.MediaSequence()` reports the 1-based index of the current medium. If no
continuation is registered, an `EOTM` simply ends the archive (`io.EOF`).

### Notes / limitations

- MTF is a **sequential** format: call `Next` to advance and `Read` to consume
  the current file's standard data stream. Skipping an entry (calling `Next`
  again without reading) is supported and discards the data automatically.
- Path resolution matches mtftar: a file's path is `<volume>/<directory>/<name>`
  using the most recently seen directory. Files should appear grouped after
  their directory block, as in real `.bkf` files.
- Streams other than the standard data stream (`STAN`) — ACLs, extended
  attributes, checksums, sparse data — are recognised but not decoded into the
  tar output. Sparse streams are not transparently expanded.
- Dates are returned in the local time zone, matching the original tool.

## Command-line tool

`cmd/mtftar` translates an MTF/BKF stream into a TAR stream, or lists its
contents — a Go reimplementation of the original `mtftar`.

```
# extract
mtftar -f backup.bkf | tar xvf -
mtftar -f backup.bkf -o output.tar

# list
mtftar -l -f backup.bkf
mtftar -l -v -f backup.bkf

# read from standard input
mtftar < backup.bkf | tar xvf -
```

Flags:

| Flag    | Description                                              |
| ------- | -------------------------------------------------------- |
| `-f`    | Input MTF/BKF file (default: standard input).            |
| `-o`    | Output TAR file (default: standard output).              |
| `-l`    | List contents instead of producing a TAR.                |
| `-v`    | Verbose listing.                                         |
| `-s N`  | Only process backup set number `N` (0 = all).            |

## Project layout

```
go-mtf/
  mtf.go        public types (Header, EntryType, TapeInfo, SetInfo), constants
  header.go     little-endian field accessors and block/stream offsets
  datetime.go   MTF date/time decoding
  strings.go    MTF string (UTF-16LE / ASCII) decoding
  reader.go     the streaming Reader (scanner + streamer core)
  reader_test.go  self-contained tests with an in-memory MTF generator
  cmd/mtftar/   the command-line tool
```

## License

GPL-2.0-or-later. See [LICENSE](LICENSE). Derived from mtftar by geocar.
