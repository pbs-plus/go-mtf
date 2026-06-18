# Media Based Catalog (MBC)

The MTF specification (section 7) defines a **Media Based Catalog** composed of
two data streams attached to the end-of-set (ESET) descriptor block:

- **File/Directory Detail (FDD)**: per-data-set index of every volume, directory
  and file, each annotated with the media sequence number and byte offset of its
  descriptor block. Stream ID `TFDD` (Type 1) or `FDD2` (Type 2).
- **Set Map**: cumulative index of every data set in the media family, one entry
  per backup/host. Stream ID `TSMP` (Type 1) or `MAP2` (Type 2).

Both are accessible via `Block.Catalog` when `Kind == KindSetEnd`.

### The Backup Exec `SMP2` Set Map stream

Real-world Backup Exec (Veritas) cartridges do not use the spec's `MAP2` Set
Map stream ID. They write an undocumented stream whose four-byte ID is
`SMP2` (bytes `53 4D 50 32`, `0x32504D53` when read as a little-endian uint32).
The MTF_100a specification does not mention `SMP2`; it is not in any Backup Exec
public reference located to date, and its layout is documented here purely from
empirical observation of produced media, not from a vendor specification.

What is observed:

- The `SMP2` stream occupies the Set Map position in the catalog region
  (immediately before the closing `ESET`, where `MAP2` would be) and is parsed
  by the same Set Map record layout (an `MTF_SM_HDR` followed by Set Map
  Entries, each with `Number-Of-Volumes` volume entries).
- **Volume entries follow each Set Map Entry as separate records in the stream**
  rather than being nested inside the entry's `LENGTH`, which is the one
  material difference from the Type 1 spec layout.

To keep the main `mtf` package free of vendor-specific logic, `SMP2` is not
parsed by the built-in Set Map parser. Instead the main package exposes a
plugin seam — [mtf.RegisterSetMapParser] — and the `besetmap` subpackage
implements and registers the `SMP2` parser on import. Programs that need
Backup Exec Set Map support add a blank import:

```go
import _ "github.com/pbs-plus/go-mtf/besetmap"
```

Without that import, [mtf.Reader.Catalog] and [mtf.ReadSetMap] fall back to
the spec `TSMP`/`MAP2` parser for `SMP2` payloads (best effort).

In code the stream ID is the constant `StreamSM2P` (alias `StreamSMP2`);
`StreamName` reports it as `"SMP2"`. The historical `SM2P` spelling was a
misreading of the byte order and is retained only as the constant name.

## Catalog struct

```go
type Catalog struct {
    SetMap   *SetMap          // parsed Type 1 Set Map (nil if absent)
    FDD      []CatalogEntry   // parsed Type 1 FDD entries (nil if absent)
    BECatalog *becatalog.Catalog // auto-detected Backup Exec XML catalog
    RawFDD   []byte           // raw FDD stream payload
    RawSetMap []byte           // raw Set Map stream payload
}
```

### Standard (Type 1) MBC

When the FDD payload contains standard binary records (`VOLB`/`DIRB`/`FILE`/
`FEND`), `FDD` and `SetMap` are populated:

```go
for {
    b, _ := r.Next()
    if b.Kind == mtf.KindSetEnd && b.Catalog != nil {
        for _, e := range b.Catalog.FDD {
            fmt.Println(e.Type, e.Name, "on tape", e.MediaSeq)
        }
        for _, e := range b.Catalog.SetMap.Entries {
            fmt.Println("data set", e.Name, "starts on tape", e.MediaSeq)
        }
    }
}
```

### Backup Exec auto-detection

When the FDD payload is a Backup Exec XML catalog (`<CatImageFile>`), the
standard parser finds no binary entries (so `FDD` is empty) and the library
automatically detects the format. `BECatalog` is populated with the parsed
Backup Exec catalog, including image metadata and the full cartridge list:

```go
for {
    b, _ := r.Next()
    if b.Kind == mtf.KindSetEnd && b.Catalog != nil {
        if be := b.Catalog.BECatalog; be != nil {
            fmt.Println("Backup Exec catalog for", be.Image.MachineName)
            // AllCartridges gives every tape in the family, not just this one.
            for _, label := range be.AllCartridges() {
                fmt.Println("  tape:", label)
            }
        }
    }
}
```

The raw payload is always available in `RawFDD` regardless of the format,
so vendor-specific parsers can be written for other non-standard payloads.

## Set Map and media families

The Set Map is the key to understanding a media family from a single cartridge:

```go
f := r.Family()
fmt.Printf("Media family 0x%08X, tape %d of %d\n", f.ID, f.TapeSequence, f.TotalTapes)
if f.SetMap != nil {
    for _, ds := range f.SetMap.Entries {
        fmt.Printf("  data set %q starts on tape %d (%d files, %d dirs)\n",
            ds.Name, ds.MediaSeq, ds.NumFiles, ds.NumDirectories)
    }
}
```

`TotalTapes` is derived from both the Set Map and the Backup Exec catalog
(when present). The MTF Set Map's `MediaSeq` values reflect the number of
MTF-level media, while Backup Exec's `AllCartridges()` may reference more
cartridges (since a single BE media family can span many B2D files). `Family`
uses whichever count is larger.

On a data-only cartridge (no catalog), `TotalTapes` is 0 and `SetMap` is nil;
on the last (catalog) cartridge, both are fully populated.

See [spanning.md](spanning.md) for the `Continuation` callback and
[lto.md](lto.md) for the LTO tape reading guide.