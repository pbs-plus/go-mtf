package mtf

import (
	"testing"
)

// TestCensus verifies the Census helper classifies a multi-entry cartridge
// correctly: counts volumes/dirs/files and infers the role from CatalogType.
func TestCensus(t *testing.T) {
	r := NewReader(NewSliceTape(buildArchive()))
	c, err := r.Census()
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	if c.Volumes == 0 {
		t.Error("Volumes = 0, want >=1")
	}
	if c.Directories == 0 {
		t.Error("Directories = 0, want >=1")
	}
	// Census drains file data; confirm the file count matches what the reader
	// delivers when walked entry-by-entry.
	r2 := NewReader(NewSliceTape(buildArchive()))
	var files int
	for {
		b, err := r2.Next()
		if err != nil {
			break
		}
		if b.Kind == KindEntry && b.Header.Type == EntryFile {
			files++
		}
	}
	if c.Files != files {
		t.Errorf("Census Files = %d, want %d (reader walk)", c.Files, files)
	}
}

// TestClassifyRole verifies the catalog-type to role mapping.
func TestClassifyRole(t *testing.T) {
	tests := []struct {
		ct   uint16
		want CartridgeRole
	}{
		{64, RoleData},
		{128, RoleCatalog},
		{0, RoleUnknown},
		{200, RoleUnknown},
	}
	for _, tc := range tests {
		if got := classifyRole(tc.ct); got != tc.want {
			t.Errorf("classifyRole(%d) = %v, want %v", tc.ct, got, tc.want)
		}
	}
}
