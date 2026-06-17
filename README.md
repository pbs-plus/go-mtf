# go-mtf

[![Go Reference](https://pkg.go.dev/badge/github.com/pbs-plus/go-mtf.svg)](https://pkg.go.dev/github.com/pbs-plus/go-mtf)

A pure-Go library for reading **Microsoft Tape Format (MTF)** streams — the
format produced by `NTBACKUP.EXE` and commonly found in `.bkf` backup files.

Features media spanning, transparent decompression, sparse reconstruction,
Media Based Catalog parsing, and near-zero-allocation classification.

## Features

- **Typed block iterator** - `Next` yields media/set/entry/set-end blocks; a
  medium's role is self-evident from the sequence.
- **Faithful extraction** - NTFS security descriptors, extended attributes,
  sparse maps, and *every* remaining stream are auto-materialized onto the
  `Header`; sparse files are reconstructed (holes zero-filled).
- **Transparent decompression** - Stac LZS (`MTF_LZS221`) and the
  compression/encryption frame layer; encryption via a pluggable decryptor
  (the spec defines no cipher).
- **Multi-media spanning** - reassembles a data set split across media,
  including mid-file splits.
- **Media Based Catalog** - standard Type 1 Set Map / File/Directory Detail
  parsing, plus a `becatalog` companion for Backup Exec XML catalogs.

## Quick start

```go
r, err := mtf.Open("backup.bkf")
if err != nil { log.Fatal(err) }
defer r.Close()

for {
	b, err := r.Next()
	if err == io.EOF { break }
	if err != nil { log.Fatal(err) }

	switch b.Kind {
	case mtf.KindEntry:
		fmt.Println(b.Header.Name)
		if b.Header.Type == mtf.EntryFile {
			io.Copy(os.Stdout, r) // stream file content
		}
	case mtf.KindSetEnd:
		fmt.Println("data set ended; catalog:", b.Catalog != nil)
	}
}
```

## Documentation

Full reference lives in [`docs/`](./docs):

- [Quick start](./docs/quickstart.md) — open, list, extract
- [Reader API](./docs/reader.md) — `Next`/`Read`, block kinds, `Header` fields
- [Data streams](./docs/streams.md) — metadata, sparse, compression/encryption
- [Media Based Catalog](./docs/catalog.md) — Set Map, FDD, `CatalogData`
- [Spanning](./docs/spanning.md) — multi-media continuation
- [Backup Exec catalogs](./docs/becatalog.md) — the `becatalog` package
- [Census](./docs/census.md) — cartridge classification
- [Performance](./docs/performance.md) — allocation strategy & benchmarks
- [Architecture](./docs/architecture.md) — package layout & reader pipeline
- [Spec reference](./docs/spec.md) — MTF field offsets & checksums

## Command-line tools

These are small utilities built on the library, primarily for surveying
archives:

| Tool | Purpose |
| --- | --- |
| `cmd/bkfscan` | Parallel `.bkf` surveyor using `Census`. |
| `cmd/bkfcensus` | Single-file `Census` reporter. |

```
bkfscan /mnt/archive/BEData          # survey
```

## Project layout

```
go-mtf/
  mtf.go          public types & constants
  reader.go       the block iterator
  header.go       field accessors & offsets
  strings.go      MTF string decoding
  datetime.go     date/time decoding
  streams.go      data-stream materialization & sparse
  catalog.go      Media Based Catalog (standard)
  spanning.go     multi-media continuation
  compress.go     compression/encryption frames
  lzs.go          Stac LZS decompressor
  census.go       cartridge classification
  becatalog/      Backup Exec XML catalog parser
  cmd/            bkfscan, bkfcensus (survey utilities)
  docs/           detailed documentation
```

## License

MIT. See [LICENSE](LICENSE).
