# Media Based Catalog (MBC)

The MTF specification (section 7) defines a **Media Based Catalog** composed of
two data streams attached to the end-of-set (ESET) descriptor block:

- **File/Directory Detail (FDD)**: per-data-set index of every volume, directory
  and file, each annotated with the media sequence number and byte offset of its
  descriptor block. Stream ID `TFDD` (Type 1) or `FDD2` (Type 2).
- **Set Map**: cumulative index of every data set in the media family, one entry
  per backup/host. Stream ID `TSMP` (Type 1) or `MAP2` (Type 2).

Both are accessible via `Block.Catalog` when `Kind == KindSetEnd`.

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
Backup Exec catalog, including the cartridge list and image metadata:

```go
for {
    b, _ := r.Next()
    if b.Kind == mtf.KindSetEnd && b.Catalog != nil {
        if be := b.Catalog.BECatalog; be != nil {
            fmt.Println("Backup Exec catalog for", be.Image.MachineName)
            for _, c := range be.Cartridges {
                fmt.Println("  cartridge:", c.Label, "family:", c.MediaFamilyName)
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

`TotalTapes` is derived from the Set Map — it is the highest `MediaSeq` across
all data-set entries. On a data-only cartridge (no catalog), `TotalTapes` is 0
and `SetMap` is nil; on the last (catalog) cartridge, both are fully populated.

See [spanning.md](spanning.md) for the `Continuation` callback and
[lto.md](lto.md) for the LTO tape reading guide.