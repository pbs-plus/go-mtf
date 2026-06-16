// Command mtftar translates a Microsoft Tape Format (MTF) / NTBackup (.bkf)
// stream into a TAR stream, or lists its contents.
//
// It is a Go reimplementation of geocar's mtftar, built on the
// github.com/pbs-plus/go-mtf library.
//
// Usage:
//
//	mtftar [-v] [-s setno] [-f backup.bkf] [-o output.tar]
//	mtftar -l [-v] [-s setno] [-f backup.bkf]
//
// With no -o, a TAR stream is written to standard output (extract with
// "mtftar -f backup.bkf | tar xvf -"). With -l, the archive contents are listed
// to standard output instead.
package main

import (
	"archive/tar"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/pbs-plus/go-mtf"
)

func main() {
	var (
		input   = flag.String("f", "", "input MTF/BKF file (default: standard input)")
		output  = flag.String("o", "", "output TAR file (default: standard output)")
		list    = flag.Bool("l", false, "list archive contents instead of extracting")
		verbose = flag.Bool("v", false, "verbose output")
		setno   = flag.Int("s", 0, "only extract the given backup set number (1-65535, 0=all)")
	)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mtftar [-v] [-s setno] [-f backup.bkf] [-o output.tar]")
		fmt.Fprintln(os.Stderr, "       mtftar -l [-v] [-s setno] [-f backup.bkf]")
		fmt.Fprintln(os.Stderr)
		flag.PrintDefaults()
	}
	flag.Parse()

	if *setno < 0 || *setno > 65535 {
		fmt.Fprintln(os.Stderr, "Set number out of range (1-65535)")
		os.Exit(1)
	}

	var in io.ReadCloser
	switch {
	case *input != "":
		f, err := os.Open(*input)
		if err != nil {
			log.Fatal(err)
		}
		in = f
	default:
		in = os.Stdin
	}
	defer func() { _ = in.Close() }()

	r := mtf.NewReader(in)

	var out io.Writer = os.Stdout
	var outf *os.File
	if !*list && *output != "" {
		f, err := os.Create(*output)
		if err != nil {
			log.Fatal(err)
		}
		outf = f
		out = f
		defer func() { _ = outf.Close() }()
	}

	var tw *tar.Writer
	if !*list {
		tw = tar.NewWriter(out)
		defer func() { _ = tw.Close() }()
	}

	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("read error: %v", err)
		}

		if *setno != 0 && int(h.SetNumber) != *setno {
			// Drain the entry's data so the reader can advance.
			if _, err := io.Copy(io.Discard, r); err != nil && err != io.EOF {
				log.Fatal(err)
			}
			continue
		}

		switch h.Type {
		case mtf.EntryVolume, mtf.EntryDirectory:
			if *list {
				printEntry(*verbose, h, 0)
				continue
			}
			if err := writeDir(tw, h); err != nil {
				log.Fatal(err)
			}
		case mtf.EntryFile:
			if *list {
				printEntry(*verbose, h, h.Size)
				if _, err := io.Copy(io.Discard, r); err != nil && err != io.EOF {
					log.Fatal(err)
				}
				continue
			}
			if err := writeFile(tw, h, r); err != nil {
				log.Fatal(err)
			}
		}
	}

	if *verbose && r.Tape() != nil {
		fmt.Fprintln(os.Stderr, "MTF generator:", r.Tape().Software)
	}
}

func printEntry(verbose bool, h *mtf.Header, size int64) {
	if !verbose {
		fmt.Println(h.Name)
		return
	}
	kind := "file"
	switch h.Type {
	case mtf.EntryDirectory:
		kind = "dir "
	case mtf.EntryVolume:
		kind = "vol "
	}
	mtime := "                    "
	if !h.ModTime.IsZero() {
		mtime = h.ModTime.Format(time.RFC3339)
	}
	fmt.Println(kind, fmt.Sprintf("%12d", size), mtime, "", h.Name)
}

func tarHeader(h *mtf.Header, size int64) *tar.Header {
	name := cleanName(h.Name)
	th := &tar.Header{
		Name:    name,
		ModTime: h.ModTime,
		Mode:    0664,
		Size:    size,
		Format:  tar.FormatGNU,
	}
	switch h.Type {
	case mtf.EntryDirectory, mtf.EntryVolume:
		th.Typeflag = tar.TypeDir
		th.Mode = 0775
		th.Name = name + "/"
	default:
		th.Typeflag = tar.TypeReg
	}
	return th
}

func writeDir(tw *tar.Writer, h *mtf.Header) error {
	return tw.WriteHeader(tarHeader(h, 0))
}

func writeFile(tw *tar.Writer, h *mtf.Header, r *mtf.Reader) error {
	if err := tw.WriteHeader(tarHeader(h, h.Size)); err != nil {
		return err
	}
	if _, err := io.Copy(tw, r); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// cleanName turns the MTF "/"-joined path into a tar-friendly relative path,
// stripping a leading drive letter such as "C:".
func cleanName(name string) string {
	name = strings.TrimPrefix(name, "/")
	if len(name) >= 2 && name[1] == ':' {
		name = name[2:]
	}
	name = strings.TrimPrefix(name, "/")
	return name
}
