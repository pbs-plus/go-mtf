# go-mtf

[![Go Reference](https://pkg.go.dev/badge/github.com/pbs-plus/go-mtf.svg)](https://pkg.go.dev/github.com/pbs-plus/go-mtf)

A pure-Go library and command-line tool for reading **Microsoft Tape Format
(MTF)** streams, the format produced by `NTBACKUP.EXE` and commonly found in
`.bkf` backup files.

This is a port of Ivo van Poorten / geocar's [`mtftar`](https://github.com/geocar/mtftar)
to idiomatic Go. It supports listing and extracting file data from MTF/BKF
streams. It is distributed under the GPLv2 (or later), as a derivative of
mtftar.

## Library

The [`mtf`](https://pkg.go.dev/github.com/pbs-plus/go-mtf) package mirrors the
`archive/tar` API:

```go
r, err := mtf.Open("backup.bkf")
if err != nil {
    log.Fatal(err)
}
defer r.Close()

for {
    h, err := r.Next()
    if err == io.EOF {
        break
    }
    if err != nil {
        log.Fatal(err)
    }

    fmt.Println(h.Name)
    if h.Type == mtf.EntryFile {
        if _, err := io.Copy(os.Stdout, r); err != nil {
            log.Fatal(err)
        }
    }
}
```

### Types

| Entry type        | Meaning                                  |
| ----------------- | ---------------------------------------- |
| `mtf.EntryVolume` | A source volume/device (`VOLB`).         |
| `mtf.EntryDirectory` | A directory (`DIRB`).                 |
| `mtf.EntryFile`   | A regular file (`FILE`); data via `Reader.Read`. |

`Reader.Tape()` and `Reader.Set()` expose metadata from the `TAPE` and `SSET`
descriptor blocks respectively.

### Notes / limitations

- MTF is a **sequential** format: call `Next` to advance and `Read` to consume
  the current file's standard data stream. Skipping an entry (calling `Next`
  again without reading) is supported and discards the data automatically.
- Path resolution matches mtftar: a file's path is `<volume>/<directory>/<name>`
  using the most recently seen directory. Files should appear grouped after
  their directory block, as in real `.bkf` files.
- Streams other than the standard data stream (`STAN`) — ACLs, extended
  attributes, checksums, sparse data — are recognised but not decoded into the
  tar output. Sparse streams are not transparently expanded.
- Dates are returned in the local time zone, matching the original tool.

## Command-line tool

`cmd/mtftar` translates an MTF/BKF stream into a TAR stream, or lists its
contents — a Go reimplementation of the original `mtftar`.

```
# extract
mtftar -f backup.bkf | tar xvf -
mtftar -f backup.bkf -o output.tar

# list
mtftar -l -f backup.bkf
mtftar -l -v -f backup.bkf

# read from standard input
mtftar < backup.bkf | tar xvf -
```

Flags:

| Flag    | Description                                              |
| ------- | -------------------------------------------------------- |
| `-f`    | Input MTF/BKF file (default: standard input).            |
| `-o`    | Output TAR file (default: standard output).              |
| `-l`    | List contents instead of producing a TAR.                |
| `-v`    | Verbose listing.                                         |
| `-s N`  | Only process backup set number `N` (0 = all).            |

## Project layout

```
go-mtf/
  mtf.go        public types (Header, EntryType, TapeInfo, SetInfo), constants
  header.go     little-endian field accessors and block/stream offsets
  datetime.go   MTF date/time decoding
  strings.go    MTF string (UTF-16LE / ASCII) decoding
  reader.go     the streaming Reader (scanner + streamer core)
  reader_test.go  self-contained tests with an in-memory MTF generator
  cmd/mtftar/   the command-line tool
```

## License

GPL-2.0-or-later. See [LICENSE](LICENSE). Derived from mtftar by geocar.
