// Command bkfscan classifies every .bkf file in one or more directories using
// mtf.Census, printing a one-line summary and tallying aggregate stats. It
// reads block headers and drains file data (it does not extract content), so
// it is suitable for surveying large archives.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	mtf "github.com/pbs-plus/go-mtf"
)

type result struct {
	path string
	size int64
	c    mtf.Census
	err  error
}

func main() {
	var all []string
	for _, dir := range os.Args[1:] {
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".bkf") {
				all = append(all, p)
			}
			return nil
		})
	}
	fmt.Fprintf(os.Stderr, "scanning %d files\n", len(all))

	workers := 64
	jobs := make(chan string)
	var wg sync.WaitGroup
	var ri int64
	res := make([]result, len(all))
	done := make(chan int64, 1)
	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		var last int64
		for {
			select {
			case <-t.C:
				n := atomic.LoadInt64(&ri)
				fmt.Fprintf(os.Stderr, "  %d/%d (%.0f/s)\n", n, len(all), float64(n-last)/5)
				last = n
			case n := <-done:
				fmt.Fprintf(os.Stderr, "  %d/%d done\n", n, len(all))
				return
			}
		}
	}()
	for range workers {
		wg.Go(func() {
			for p := range jobs {
				idx := atomic.AddInt64(&ri, 1) - 1
				res[idx] = scanOne(p)
			}
		})
	}
	for _, p := range all {
		jobs <- p
	}
	close(jobs)
	wg.Wait()
	done <- atomic.LoadInt64(&ri)

	report(res)
}

func scanOne(p string) result {
	fi, _ := os.Stat(p)
	r := result{path: p, size: fi.Size()}
	f, err := os.Open(p)
	if err != nil {
		r.err = err
		return r
	}
	defer func() { _ = f.Close() }()
	mr := mtf.NewReader(f)
	c, err := mr.Census()
	r.c = c
	if err != nil {
		r.err = err
	}
	return r
}

func roleStr(r mtf.CartridgeRole) string {
	switch r {
	case mtf.RoleData:
		return "data"
	case mtf.RoleCatalog:
		return "catalog"
	default:
		return "unknown"
	}
}

func report(res []result) {
	var ok, fail int
	roleTally := map[string]int{}
	catTypeTally := map[uint16]int{}
	var totalFiles, totalDirs, totalBytes int64
	var comp, enc, sparse int64
	var catOnly, dataOnly, empty int
	var errs []string
	for _, r := range res {
		if r.err != nil {
			fail++
			errs = append(errs, fmt.Sprintf("  FAIL %s: %v", r.path, r.err))
			continue
		}
		ok++
		roleTally[roleStr(r.c.Role)]++
		catTypeTally[r.c.CatalogType]++
		totalFiles += int64(r.c.Files)
		totalDirs += int64(r.c.Directories)
		totalBytes += r.c.FileBytes
		comp += int64(r.c.CompressedFiles)
		enc += int64(r.c.EncryptedFiles)
		sparse += int64(r.c.SparseFiles)
		if r.c.HasData() {
			dataOnly++
		}
		if r.c.Role == mtf.RoleCatalog {
			catOnly++
		}
		if r.c.Files == 0 && r.c.Role != mtf.RoleCatalog {
			empty++
		}
	}
	fmt.Printf("Parsed:     %d ok, %d failed\n", ok, fail)
	fmt.Printf("Roles:      data=%d catalog=%d unknown=%d (empty-noncat=%d)\n",
		roleTally["data"], roleTally["catalog"], roleTally["unknown"], empty)
	fmt.Printf("CatalogType tally:\n")
	var cts []uint16
	for k := range catTypeTally {
		cts = append(cts, k)
	}
	slices.Sort(cts)
	for _, k := range cts {
		fmt.Printf("    %d: %d files\n", k, catTypeTally[k])
	}
	fmt.Printf("Totals:     dirs=%d files=%d fileBytes=%.1f TB\n",
		totalDirs, totalFiles, float64(totalBytes)/1e12)
	fmt.Printf("Compression: %d compressed files\n", comp)
	fmt.Printf("Encryption:  %d encrypted files\n", enc)
	fmt.Printf("Sparse:      %d sparse files\n", sparse)
	if len(errs) > 0 {
		fmt.Printf("\nFailures (%d):\n", len(errs))
		for _, e := range errs[:min(50, len(errs))] {
			fmt.Println(e)
		}
		if len(errs) > 50 {
			fmt.Printf("  ... and %d more\n", len(errs)-50)
		}
	}
}
