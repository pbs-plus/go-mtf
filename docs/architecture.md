# Architecture

## Package layout

```
go-mtf/
  mtf.go        public types: Header, Block, EntryType, TapeInfo, SetInfo, ESetInfo,
                Census, Catalog, constants, stream IDs
  reader.go     Reader: the block iterator core (scan/parse/skip/Read)
  header.go     little-endian field accessors (u8/u16/u32/u64), block/stream offsets,
                common-block checksum
  strings.go    MTF string decoding (UTF-16LE / ASCII), zero-alloc path builders
  datetime.go   packed 5-byte MTF date/time decoding
  streams.go    data-stream handling: materializeStreams, sparse reconstruction
  catalog.go    Media Based Catalog: standard Type 1 binary parser + CatalogData
  spanning.go   multi-media spanning: EOTM probing, continuation resync
  compress.go   compression/encryption frame layer (MTF_CMP_HDR / MTF_ENC_HDR)
  lzs.go        Stac LZS decompressor (ANSI X3.241-1994)
  census.go     Census classification helper
  becatalog/    companion package: Backup Exec XML catalog parser
  cmd/
    bkfscan/    CLI: parallel .bkf surveyor using Census
    bkfcensus/  CLI: single-file Census reporter
```

## The reader pipeline

`Next` drives a single byte stream through a sequence of logical blocks. Each
iteration:

1. **scanStart** reads the 48-byte common descriptor block (`MTF_DB_HDR`) into
   `r.blk`. For a `TAPE` block it adopts the Format Logical Block size.
2. **dispatch** on the block type:
   - `TAPE` → `parseTape`, yield `KindMedia`
   - `SSET` → `parseSet`, yield `KindSet`
   - `ESET` → `parseEset`, `captureCatalog`, yield `KindSetEnd`
   - `VOLB`/`DIRB` → `parseVolb`/`parseDirb`, `beginEntry`, yield `KindEntry`
   - `FILE` → `parseFile`, `streamStart`, `materializeStreams`, yield `KindEntry`
   - `EOTM` → `switchMedium` (spanning) or EOF
   - `SFMB`/`ESPB`/`CFIL` → transparent (skipped)
3. **scanNext** skips any remaining bytes of the logical block to align on the
   next.

`beginEntry`/`materializeStreams` walk the object's data streams up to STAN,
capturing metadata (NACL/NTEA/SPAR) into the `Header` and leaving the reader
positioned at STAN content. `Read` then serves bytes from STAN.

## Logical-block accounting

MTF streams bytes inside Format Logical Blocks (FLBs) of `flbsize` bytes. The
reader tracks `flbread` (consumed within the current FLB) and `abspos`
(absolute stream position). Stream data flows continuously across FLB
boundaries (it is not capped to `flbsize`), so stream reads and skips account
directly. Block alignment uses `scanNext`/`wrapFlbread`.

## Read-ahead (peek)

The reader keeps a small `peek` buffer of read-ahead bytes. `probeEOTM` reads
speculatively to test for an EOTM at an FLB boundary; if it is not an EOTM the
bytes are handed back via `peek` for normal delivery. This lets spanning detect
mid-stream media boundaries without consuming data.

## Seek integration

When the source implements `io.Seeker`, `skipStreamData` seeks instead of
reading, and `skipRemainingData` uses a single-seek fast path (when the data
fits before EOF). Seeking clears `peek` (those bytes came from the pre-seek
position) and re-binds on medium switch.

## Buffer separation

Two reusable byte buffers serve different lifetimes:

- `strBuf` — the Name path only. Safe to alias because `Name` is consumed before
  the next `Next` and is not touched by `Read`.
- `scratchBuf` — other decoded strings (volume/machine/tape/set) **and**
  continuation restores during spanning `Read`. Kept separate so a spanning
  `Read` cannot corrupt a retained `Name`.

## Catalog layering

```
go-mtf (standard)          becatalog (vendor)
  captureCatalog             Parse(mtf.CatalogData)
  parseFDD / parseSetMap     ParseFDD([]byte)
  Catalog.Raw() ───────────► raw TFDD bytes ───► XML decode
  Catalog.FDD/SetMap         Catalog (BE)
```

The `CatalogData` interface is the seam: it exposes raw stream payloads so a
vendor parser stays decoupled from the concrete `Catalog` type.
