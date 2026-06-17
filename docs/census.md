# Census

`Reader.Census` walks an entire cartridge (one `.bkf` file / one tape),
classifying it at a glance **without reading file content**. It reads every
block header and stream descriptor, so its counts are authoritative; only the
content bytes are not materialized.

```go
func (r *Reader) Census() (Census, error)
```

It runs in `HeaderOnly` mode (zero per-entry allocation) and returns its result
**by value**. It consumes the reader — use a fresh reader to extract content
afterwards. A non-nil error still returns a partially-populated `Census`, so
you can classify damaged cartridges.

## Census fields

| Field | Meaning |
| --- | --- |
| `Tape` | TAPE metadata (nil if the stream did not begin with TAPE) |
| `Set` | SSET metadata, or nil |
| `Role` | Inferred cartridge role |
| `CatalogType` | Catalog type recorded on the TAPE block |
| `MediaSequence` | 1-based media sequence number within the family |
| `SetsClosed` | ESET blocks seen (data sets ending on this cartridge) |
| `HasCatalog` | Whether any ESET carried catalog streams |
| `CatalogBytes` | Total catalog payload captured |
| `Volumes` / `Directories` / `Files` | Entry counts |
| `EmptyFiles` | Files with no data stream |
| `FileBytes` | Sum of every file's stored data size |
| `SparseFiles` / `CompressedFiles` / `EncryptedFiles` | Per-flag counts |

`Census.HasData()` reports whether the cartridge carries any file content.

## Cartridge roles

`classifyRole` maps the TAPE `CatalogType` to a `CartridgeRole`:

| CatalogType | Role | Meaning |
| --- | --- | --- |
| 64 | `RoleData` | Primarily carries file data |
| 128 | `RoleCatalog` | Consolidated catalog media |
| other / 0 | `RoleUnknown` | Unrecognized |

These values are Backup Exec conventions observed on real production media.

## Example: survey a directory of .bkf files

See `cmd/bkfscan` for a complete parallel surveyor, or:

```go
r, _ := mtf.Open("B2D027089.bkf")
c, err := r.Census()
fmt.Printf("%s: %d files, %d MB, role=%d, catalog=%v\n",
	filepath.Base(path), c.Files, c.FileBytes>>20, c.Role, c.HasCatalog)
```

See [performance.md](performance.md) for why `Census` is cheap even on
network-mounted archives.
