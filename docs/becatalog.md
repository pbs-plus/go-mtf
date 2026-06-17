# Backup Exec catalogs (`becatalog`)

Backup Exec (BE) reuses the standard `TFDD` stream envelope defined by the MTF
spec but writes its own catalog format inside it instead of the spec's binary
records: a small binary prefix followed by an XML document. The companion
package `github.com/pbs-plus/go-mtf/becatalog` decodes that format.

## Auto-detection

The core `go-mtf` library **automatically detects** Backup Exec FDD payloads.
When `Catalog.BECatalog` is non-nil, the FDD was a Backup Exec XML catalog and
the parsed result is directly available:

```go
for {
    b, err := r.Next()
    if err == io.EOF { break }
    if err != nil { log.Fatal(err) }
    if b.Kind == mtf.KindSetEnd && b.Catalog != nil {
        if be := b.Catalog.BECatalog; be != nil {
            fmt.Println("Backup Exec catalog for", be.Image.MachineName)
            for _, cart := range be.Cartridges {
                fmt.Println("  cartridge:", cart.Label, "family:", cart.MediaFamilyName)
            }
        }
    }
}
```

For standard MTF binary FDDs, `BECatalog` is nil and `FDD` is populated instead.
The raw payload is always available in `Catalog.RawFDD` regardless of format.

## Direct parsing

The `becatalog` package can also be used standalone to parse a raw Backup Exec
FDD payload (for example, one extracted from a different source):

```go
import "github.com/pbs-plus/go-mtf/becatalog"

cat, err := becatalog.ParseFDD(rawFDDBytes)
if errors.Is(err, becatalog.ErrNotBackupExec) {
    // not a Backup Exec payload
}
```

## What the BE catalog contains

The XML embeds a *synthesized disk image* for the whole media family:

- **SynthImage** — a tree of directories and files spanning all cartridges in
  the family. Each node carries a name, size, dates, and attributes. This is a
  re-projected view of the backup, not the on-tape DBLK order.
- **Cartridges** — the list of media in the family, each with a label, location,
  and media family name.

### Full cartridge list

The top-level `Cartridges` slice only contains the cartridge the catalog file
belongs to (typically just one entry). The full list of all cartridges in the
media family is available via `AllCartridges()`, which deduplicates the
`CartridgeLabel` values from the `SynthImageExtraInfo` entries — one per
image×cartridge combination:

```go
tapes := be.AllCartridges()
fmt.Printf("need %d tapes for full restore\n", len(tapes))
```

The `Family()` method on the reader automatically incorporates this count,
using whichever is larger: the MTF Set Map's `TotalTapes` or the BE catalog's
`AllCartridges()` count.

## API

```go
func ParseFDD(rawFDD []byte) (*Catalog, error)
func (c *Catalog) AllCartridges() []string

var ErrNotBackupExec error

type Catalog struct {
    Header      FileHeader
    Image       Image
    Cartridges  []Cartridge
    Images      []SynthImage
    ImageExtras []SynthImageExtraInfo // per-image cartridge refs
    Tree        []Node
}

type SynthImageExtraInfo struct {
    Size           int64
    BackupTimeUTC  int64
    ImageNumber    int
    MediaNumber    int
    BackupType     int
    CartridgeLabel string // the tape this image lives on
    // ...
}

type Cartridge struct {
    Label           string // e.g. "B2D027089"
    Location        string
    MediaFamilyName string
    // ...
}
```