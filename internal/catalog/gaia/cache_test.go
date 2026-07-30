package gaia

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testSource(id uint64) Source {
	return Source{Release: "DR3", SourceID: id, RA: 10, Dec: 2, ReferenceEpoch: 2016, PositionError: .1, ProperMotionErrorRA: .1, ProperMotionErrorDec: .1, G: 12, BP: 12.2, RP: 11.8, GError: .01, BPError: .01, RPError: .01}
}
func testSpectrum(id uint64) XPSpectrum {
	return XPSpectrum{Release: "DR3", SourceID: id, RepresentationVersion: "xp-v1", CalibrationVersion: "cal-v1", Wavelengths: []float64{400, 500}, Flux: []float64{1, 2}, FluxErrors: []float64{.1, .1}}
}

func TestCacheFreshUpsertAndVersionCoexistence(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cell := QueryCell{Signature: "sig-a", SpatialCell: "hpx-1", Release: "DR3"}
	if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{testSource(2), testSource(1)}, Spectra: []XPSpectrum{testSpectrum(1)}}); err != nil {
		t.Fatal(err)
	}
	ok, err := c.IsCellComplete(context.Background(), cell)
	if err != nil || !ok {
		t.Fatalf("complete=%v err=%v", ok, err)
	}
	sources, err := c.Sources(context.Background(), cell)
	if err != nil || len(sources) != 2 || sources[0].SourceID != 1 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	got, err := c.Spectrum(context.Background(), "DR3", 1, "xp-v1")
	if err != nil || got.CalibrationVersion != "cal-v1" {
		t.Fatalf("spectrum=%+v err=%v", got, err)
	}
	newSpec := testSpectrum(1)
	newSpec.RepresentationVersion = "xp-v2"
	if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Spectra: []XPSpectrum{newSpec}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Spectrum(context.Background(), "DR3", 1, "xp-v2"); err != nil {
		t.Fatal(err)
	}
}

func TestCacheCancellationDoesNotCompleteCell(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cell := QueryCell{Signature: "sig", SpatialCell: "cell", Release: "DR3"}
	if err := c.PutCell(ctx, CellBatch{Cell: cell, Sources: []Source{testSource(1)}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	ok, err := c.IsCellComplete(context.Background(), cell)
	if err != nil || ok {
		t.Fatalf("complete=%v err=%v", ok, err)
	}
}

func TestCacheCorruptSpectrumDetected(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cell := QueryCell{Signature: "sig", SpatialCell: "cell", Release: "DR3"}
	if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{testSource(1)}, Spectra: []XPSpectrum{testSpectrum(1)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB().Exec(`UPDATE xp_spectra SET flux='bad' WHERE source_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Spectrum(context.Background(), "DR3", 1, "xp-v1"); err == nil {
		t.Fatal("corrupt spectrum accepted")
	}
}

func TestCacheConcurrentReaders(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cell := QueryCell{Signature: "sig", SpatialCell: "cell", Release: "DR3"}
	if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{testSource(1)}}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := c.Sources(context.Background(), cell)
			if e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

func TestCacheMigrationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaia.db")
	c, err := OpenCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	c, err = OpenCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var version string
	if err := c.DB().QueryRow(`SELECT value FROM metadata WHERE key='schema_version'`).Scan(&version); err != nil || version != "2" {
		t.Fatalf("version=%q err=%v", version, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.IsCellComplete(ctx, QueryCell{Signature: "missing", SpatialCell: "x", Release: "DR3"}); err != nil {
		t.Fatal(err)
	}
}

func TestCacheOverlappingCellsRetainMembership(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	s := testSource(1)
	cells := []QueryCell{{Signature: "a", SpatialCell: "one", Release: "DR3"}, {Signature: "b", SpatialCell: "two", Release: "DR3"}}
	for _, cell := range cells {
		if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{s}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, cell := range cells {
		got, err := c.Sources(context.Background(), cell)
		if err != nil || len(got) != 1 {
			t.Fatalf("cell %q got=%+v err=%v", cell.SpatialCell, got, err)
		}
	}
}

func TestCacheMigratesV1Membership(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gaia.db")
	c, err := OpenCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	cells := []QueryCell{{Signature: "old-a", SpatialCell: "one", Release: "DR3"}, {Signature: "old-b", SpatialCell: "two", Release: "DR3"}}
	for i, cell := range cells {
		if err := c.PutCell(context.Background(), CellBatch{Cell: cell, Sources: []Source{testSource(uint64(7 + i))}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.DB().Exec(`DROP TABLE query_cell_sources`); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB().Exec(`UPDATE metadata SET value='1' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = OpenCache(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for _, cell := range cells {
		got, err := c.Sources(context.Background(), cell)
		if err != nil || len(got) != 1 {
			t.Fatalf("migrated cell=%+v err=%v", got, err)
		}
	}
}

func TestCachePathResolution(t *testing.T) {
	if got, err := DefaultCachePath("C:/appdata"); err != nil || got != filepath.Join("C:/appdata", "gofitsv3", "gaia-cache.sqlite") {
		t.Fatal(got)
	}
	if got, err := ResolveCachePath("C:/appdata", "D:/portable/cache.db"); err != nil || got != filepath.Join("D:/portable", "cache.db") {
		t.Fatal(got)
	}
	if got, err := DefaultCachePath(""); err != nil || !filepath.IsAbs(got) {
		t.Fatalf("default=%q err=%v", got, err)
	}
}

func TestCacheConnectionPragmasAndForeignKeys(t *testing.T) {
	c, err := OpenCache(context.Background(), filepath.Join(t.TempDir(), "gaia.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	conn1, err := c.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	conn2, err := c.DB().Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	for i, conn := range []*sql.Conn{conn1, conn2} {
		var fk, busy int
		if err := conn.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if fk != 1 || busy < cacheBusyTimeoutMS {
			t.Fatalf("connection %d pragmas fk=%d busy=%d", i, fk, busy)
		}
		if _, err := conn.ExecContext(context.Background(), `INSERT INTO xp_spectra(release,source_id,representation_version,calibration_version,wavelengths,flux,flux_errors,wavelength_min,wavelength_max,checksum,fetched_at) VALUES('DR3',999,'x','x','[]','[]','[]',1,2,'x',0)`); err == nil {
			t.Fatal("foreign-key violation accepted")
		}
	}
}

func TestOpenCacheCreatesParentAndReportsPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "gaia.db")
	c, err := OpenCache(context.Background(), path)
	if err != nil {
		t.Fatalf("OpenCache nested path: %v", err)
	}
	defer c.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cache file was not created: %v", err)
	}
	invalid := filepath.Join(t.TempDir(), "cache-dir")
	if err := os.Mkdir(invalid, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenCache(context.Background(), invalid); err == nil || !strings.Contains(err.Error(), "open Gaia cache") {
		t.Fatalf("invalid cache path error=%v, want contextual open error", err)
	}
}
