# Media Based Catalog (MBC)

MTF defines a *Media Based Catalog* written as data streams on the End-of-Set
(`ESET`) block that closes a data set (spec §7). It has two parts:

## File/Directory Detail (FDD)

Stream ID `TFDD` (Type 1) or `FDD2` (Type 2), spec §7.3.2.

A per-data-set index of every volume, directory and file, each annotated with
the **media sequence number** and **Format Logical Address** of its descriptor
block. This lets a reader seek directly to any object.

## Set Map

Stream ID `TSMP` (Type 1) or `MAP2` (Type 2), spec §7.3.3.

A **cumulative** index of every data set in the whole Media Family (one entry
per backup/host, each followed by its source volumes and machine name). The Set
Map is rewritten cumulatively as more data sets are appended, so **the Set Map
on the last cartridge is the most complete**. It is the structure to consult for
"which backups live in this family and on which media".

## The Catalog type

The `KindSetEnd` block carries the catalog on `Block.Catalog`:

```go
type Catalog struct {
	SetMap    *SetMap        // cumulative Media Family summary (TSMP)
	FDD       []CatalogEntry // per-set File/Directory Detail (TFDD)
	RawFDD    []byte         // raw TFDD payload (for vendor parsers)
	RawSetMap []byte         // raw TSMP payload
}
```

It is `nil` when no MBC streams were present.

```go
for {
	b, err := r.Next()
	if err == io.EOF { break }
	if err != nil { log.Fatal(err) }
	if b.Kind != mtf.KindSetEnd || b.Catalog == nil { continue }
	for _, ds := range b.Catalog.SetMap.Entries {
		fmt.Println("set", ds.SetNumber, ds.Name, "files", ds.NumFiles)
		for _, vol := range ds.Volumes {
			fmt.Println("  host", vol.MachineName, "vol", vol.Name)
		}
	}
}
```

### SetMapEntry

```go
type SetMapEntry struct {
	MediaSeq, FDDMediaSeq, SetNumber uint16
	NumDirectories, NumFiles, NumCorrupt uint32
	Size       uint64
	Name, Description, Owner string
	WriteTime  time.Time
	TimeZone   int8
	Volumes    []CatalogEntry   // source volumes in this set
	// ... plus PBA/FLA/attributes for seek indexing
}
```

### CatalogEntry

```go
type CatalogEntry struct {
	Type        CatalogEntryType  // Volume / Directory / File
	MediaSeq    uint16   // 1-based medium holding this object's DBLK
	FLA         uint64   // byte offset of the DBLK on that medium
	Size        uint64
	Name        string
	VolumeLabel, MachineName string   // volume entries
	// ... plus attributes, times, link
}
```

`MediaSeq` + `FLA` together locate any object for random-access extraction:
open medium `MediaSeq`, seek to `FLA`, and the DBLK is there.

## CatalogData interface

A vendor may carry a non-standard payload inside the standard stream envelope.
For example, **Backup Exec** writes its own XML catalog inside a `TFDD` stream.
The standard parser leaves the parsed fields empty in that case and exposes the
raw payload.

`CatalogData` decouples vendor parsers from the concrete type:

```go
type CatalogData interface {
	Raw() CatalogRaw   // { FDD, SetMap []byte }
}
```

`*Catalog` satisfies it. A vendor parser accepts a `CatalogData` and works
purely from the bytes, keeping this package spec-faithful. See
[becatalog.md](becatalog.md).
