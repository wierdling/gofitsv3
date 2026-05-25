package fitsio

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRealFits verifies we can parse HST/JWST FITS headers and data for sample files.
func TestLoadRealFits(t *testing.T) {
	base := filepath.Join("..", "..", "TestImages")
	paths := []string{
		filepath.Join(base, "ick909030_drc.fits"),
		filepath.Join(base, "ick909030_drz.fits"),
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				t.Skipf("sample FITS file not present: %s", p)
			}
			t.Fatalf("stat %s: %v", p, err)
		}
		f, err := LoadFile(p)
		if err != nil {
			t.Fatalf("load %s: %v", p, err)
		}
		if len(f.HDUs) == 0 {
			t.Fatalf("no HDUs for %s", p)
		}
		sci := f.SelectSCI()
		if len(sci) == 0 {
			t.Fatalf("no SCI extension in %s", p)
		}
		if sci[0].Data.Width == 0 || sci[0].Data.Height == 0 {
			t.Fatalf("empty dimensions for %s", p)
		}
	}
}
