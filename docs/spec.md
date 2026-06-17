# MTF specification cross-reference

This document maps the library to the Microsoft Tape Format (MTF) specification.
Spec section numbers refer to the MTF specification document.

## Common descriptor block (`MTF_DB_HDR`, 48 bytes)

| Offset | Size | Field | Library |
| --- | --- | --- | --- |
| 0 | 4 | Block Type | `blockType()`, `dbTAPE`/`dbSSET`/... |
| 4 | 4 | Common Block Attributes | `Header.BlockAttributes`, `Attr*` |
| 8 | 2 | Offset To First Stream | `streamStart` |
| 10 | 1 | OS ID | `Header.OSID` |
| 11 | 1 | OS Version | — |
| 12 | 8 | Logical Block Size | — (per-block) |
| 20 | 8 | Fields/Link Area Offset | `probeEOTM` validation |
| 36 | 4 | Control Block ID | `probeEOTM` validation |
| 40 | 8 | OS-Specific Data (tape_pos) | — |
| 44 | 1 | String Type | `strType` (bit0: 0=UTF-16LE, 1=ASCII) |
| 46 | 2 | Header Checksum | `Checksum()`, `VerifyChecksum()` |

Checksum = 16-bit word-wise XOR over bytes 0..45 (23 words), excluding the
checksum field itself (§"Header Checksum").

## Descriptor blocks (DBLKs)

| DBLK | Library | Spec § |
| --- | --- | --- |
| `TAPE` | `parseTape` → `TapeInfo` | 5.2 |
| `SSET` | `parseSet` → `SetInfo` | 5.3 |
| `VOLB` | `parseVolb` | 5.4 |
| `DIRB` | `parseDirb` | 5.5 |
| `FILE` | `parseFile` | 5.6 |
| `ESET` | `parseEset` → `ESetInfo` | 5.7 |
| `EOTM` | `probeEOTM`, `switchMedium` | 5.2.9 |
| `SFMB`/`ESPB`/`CFIL` | transparent (skipped) | 5.2.x |

Field offsets for each are in `header.go` (`tape*Off`, `sset*Off`, etc.).

### TAPE (`TapeInfo`)

| Field | Offset |
| --- | --- |
| Media Family ID | 48 |
| Attributes | 54 |
| Sequence (media seq) | 58 |
| Password Algorithm | 60 |
| Soft Filemark Block Size | 62 |
| Catalog Type | 64 |
| Name/Label/Password/Software | tape_pos (68/72/76/80) |
| FLB Size | 84 |
| Software Vendor ID | 86 |
| Create Time | 88 |
| MTF Major Version | 93 |

### SSET (`SetInfo`)

| Field | Offset |
| --- | --- |
| Attributes | 52 |
| Encryption / Compression | 56 / 58 |
| Software Vendor ID / Version | 60 / 96 |
| Data Set Number | 62 |
| Name/Label/Password/Owner | tape_pos (64/68/72/76) |
| PBA | 80 |
| Create Time | 88 |
| Major / Minor Version | 93 / 94 |
| Time Zone | 95 |

## Data streams

Each object's streams are a sequence of 22-byte stream headers (`MTF_STREAM_HDR`)
followed by their data, 4-byte aligned.

| Stream hdr offset | Field | Library |
| --- | --- | --- |
| 0 | 4 | Stream Data Type | `streamType` |
| 4 | 2 | Stream File System Attributes | `streamSysAttr`, `StreamFSSparse` |
| 6 | 2 | Stream Media Format Attributes | `streamMediaAttr`, `StreamMedia*` |
| 8 | 8 | Stream Length | `streamLen` |
| 16 | 2 | Data Encryption Algorithm | `streamEncAlgo` |
| 18 | 2 | Data Compression Algorithm | `streamCompAlgo` |
| 20 | 2 | Stream Checksum | `streamChecksum` |

### Stream attributes (§6.1)

| Bit | Name | Constant |
| --- | --- | --- |
| FS, BIT3 | `STREAM_IS_SPARSE` | `StreamFSSparse` |
| Media, BIT0 | `STREAM_CONTINUE` | `StreamMediaContinue` |
| Media, BIT3 | `STREAM_ENCRYPTED` | `StreamMediaEncrypted` |
| Media, BIT4 | `STREAM_COMPRESSED` | `StreamMediaCompressed` |

### Compression/encryption frames (§6.4/6.5)

Compressed/encrypted STAN data is wrapped in 24-byte frame headers
(`MTF_CMP_HDR` / `MTF_ENC_HDR`):

| Offset | Field |
| --- | --- |
| 0 | 2 | Frame ID (`'FH'` / `'EH'`) |
| 12 | 4 | Uncompressed/Unencrypted Size |
| 16 | 4 | Compressed/Encrypted Size |
| 22 | 2 | Checksum (word-XOR of bytes 0..21) |

- Compression: `MTF_LZS221` (`0x0ABE`), Stac LZS, ANSI X3.241-1994 (App. C).
- Encryption: **no cipher defined** by the spec (App. D) — pluggable `Decryptor`.

## Date/time

Packed 5 bytes (in a 6-byte region), bit-packed year/month/day/hour/min/sec.
Decoded by `decodeDateTime` to local time (the archive stores local civil time;
Go applies historical DST correctly).

## Media Based Catalog (§7)

| Stream | Type | Library |
| --- | --- | --- |
| `TFDD` | File/Directory Detail (Type 1) | `Catalog.FDD`, `parseFDD` |
| `FDD2` | File/Directory Detail (Type 2) | raw `Catalog.RawFDD` |
| `TSMP` | Set Map (Type 1) | `Catalog.SetMap`, `parseSetMap` |
| `MAP2` | Set Map (Type 2) | raw `Catalog.RawSetMap` |

FDD entries are 36-byte common headers (`MTF_FDD_HDR`) with appended strings;
Set Map is an 8-byte header + N entries (`MTF_SET_MAP_ENTRY`). See `catalog.go`.

## Spanning (§8)

End-of-media processing: the `EOTM` block marks medium end. Continuation media
repeat the `TAPE`/`SSET`/`VOLB`/`DIRB` context with the `MTF_CONTINUATION`
attribute (`AttrContinuation`, BIT0). A split data stream sets
`STREAM_CONTINUE` and its length is the remaining portion.
