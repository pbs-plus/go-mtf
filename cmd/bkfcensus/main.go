// Command bkfcensus runs mtf.Census on a single .bkf file and prints a compact
// one-line classification. Intended for validating the library against samples.
package main

import (
	"fmt"
	"os"

	mtf "github.com/pbs-plus/go-mtf"
)

func main() {
	fi, _ := os.Stat(os.Args[1])
	f, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Printf("OPEN-ERR %s: %v\n", os.Args[1], err)
		return
	}
	defer func() { _ = f.Close() }()
	r := mtf.NewReader(f)
	c, err := r.Census()
	role := "?"
	switch c.Role {
	case mtf.RoleData:
		role = "data"
	case mtf.RoleCatalog:
		role = "catalog"
	case mtf.RoleUnknown:
		role = "unknown"
	}
	flags := ""
	if c.CompressedFiles > 0 {
		flags += " COMP"
	}
	if c.EncryptedFiles > 0 {
		flags += " ENC"
	}
	if c.SparseFiles > 0 {
		flags += " " + fmt.Sprintf("SPARSE:%d", c.SparseFiles)
	}
	if c.HasCatalog {
		flags += " CAT"
	}
	errstr := ""
	if err != nil {
		errstr = "  ERR=" + err.Error()
	}
	fmt.Printf("%-22s size=%-12d role=%-7s ct=%-3d seq=%d vol=%d dir=%-6d file=%-6d(empty=%d) bytes=%-14d%s%s\n",
		shortName(os.Args[1]), fi.Size(), role, c.CatalogType, c.MediaSequence,
		c.Volumes, c.Directories, c.Files, c.EmptyFiles, c.FileBytes, flags, errstr)
}

func shortName(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}
