# go-mtf documentation

Detailed reference for the `go-mtf` library, a pure-Go reader for Microsoft
Tape Format (MTF) — the format used by `NTBACKUP.EXE` and Backup Exec `.bkf`
files.

| Document | Covers |
| --- | --- |
| [quickstart.md](quickstart.md) | Install, open a file, list entries, extract content |
| [reader.md](reader.md) | The `Reader`/`Block`/`Header` API: `Next`, `Read`, block kinds, entry fields |
| [streams.md](streams.md) | Data-stream materialization: metadata streams, STAN, sparse, compression/encryption |
| [catalog.md](catalog.md) | Media Based Catalog (MBC): Set Map, File/Directory Detail, `CatalogData` |
| [spanning.md](spanning.md) | Multi-media spanning: `SetContinuation`, EOTM, mid-file reassembly |
| [becatalog.md](becatalog.md) | Backup Exec XML catalog format (the `becatalog` companion package) |
| [census.md](census.md) | Cartridge classification with `Census` |
| [performance.md](performance.md) | Allocation strategy and benchmarks |
| [architecture.md](architecture.md) | Package layout, the reader pipeline, internal data flow |
| [spec.md](spec.md) | MTF spec cross-reference: block layouts, field offsets, checksum |

For the original README (project overview, license), see the top-level
[../README.md](../README.md).
