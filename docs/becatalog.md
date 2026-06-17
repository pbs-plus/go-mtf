# Backup Exec catalogs (`becatalog`)

Backup Exec (BE) reuses the standard `TFDD` stream envelope defined by the MTF
spec but writes its own catalog format inside it instead of the spec's binary
records: a small binary prefix followed by an XML document. The companion
package `github.com/pbs-plus/go-mtf/becatalog` decodes that format.

This keeps `go-mtf` spec-faithful: the core package only parses the *standard*
Type 1 binary MBC and exposes raw stream payloads; the vendor-specific XML
lives in a separate package.

## Parse

```go
import "github.com/pbs-plus/go-mtf/becatalog"

for {
	b, err := r.Next()
	if err == io.EOF { break }
	if err != nil { log.Fatal(err) }
	if b.Kind != mtf.KindSetEnd || b.Catalog == nil { continue }

	cat, err := becatalog.Parse(b.Catalog) // takes mtf.CatalogData
	if errors.Is(err, becatalog.ErrNotBackupExec) { continue }
	if err != nil { log.Fatal(err) }

	for _, cart := range cat.Cartridges {
		fmt.Println("cartridge", cart.Label, cart.NumFiles, "files")
	}
	fmt.Println("image dirs", cat.Image.NumDirectories)
}
```

`becatalog.Parse` returns `ErrNotBackupExec` for non-BE payloads, so a caller
can fall back to the standard MTF binary parser in `Catalog.FDD`.

## What the BE catalog contains

The XML embeds a *synthesized disk image* for the whole media family:

- **SynthImage** — a tree of directories and files spanning all cartridges in
  the family. Each node carries a name, size, dates, and attributes. This is a
  re-projected view of the backup, not the on-tape DBLK order.
- **Cartridges** — the list of media in the family, each with a label, number,
  capacity, and allocated/used bytes.

### Known limitation

Each `SynthImage` carries references to the cartridges that contribute to it,
and a single consolidated-catalog media may describe many cartridges. The
parser currently captures the cartridge that the catalog fragment belongs to;
the full cross-referencing of all cartridges per image is not yet complete.

## API

```go
func Parse(cd mtf.CatalogData) (*Catalog, error)
func ParseFDD(rawFDD []byte) (*Catalog, error)

var ErrNotBackupExec error

type Catalog struct {
	Cartridges []Cartridge
	Image      SynthImage
	// ... other top-level fields
}
```

`ParseFDD` takes raw TFDD bytes directly (no `mtf.CatalogData` required),
useful when you already have the stream payload.
