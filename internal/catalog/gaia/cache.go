package gaia

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const cacheBusyTimeoutMS = 5000

type Cache struct{ db *sql.DB }

type QueryCell struct {
	Signature   string
	SpatialCell string
	Release     string
}

type CellBatch struct {
	Cell      QueryCell
	Sources   []Source
	Spectra   []XPSpectrum
	FetchedAt time.Time
}

func OpenCache(ctx context.Context, path string) (*Cache, error) {
	if path == "" {
		return nil, errors.New("Gaia cache path is required")
	}
	dsn := path
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("open Gaia cache %q: create parent directory: %w", path, err)
		}
		dsn = "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open Gaia cache %q: database handle: %w", path, err)
	}
	// The cache is a small local SQLite store. A bounded pool avoids opening
	// eight independent SQLite connections (and their page caches) for a
	// single picker/calibration job while still allowing one concurrent reader.
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)
	for _, pragma := range []string{"PRAGMA foreign_keys=ON", "PRAGMA journal_mode=WAL", fmt.Sprintf("PRAGMA busy_timeout=%d", cacheBusyTimeoutMS)} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("open Gaia cache %q: %s: %w", path, pragma, err)
		}
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("open Gaia cache %q: migrate schema: %w", path, err)
	}
	return &Cache{db: db}, nil
}

func (c *Cache) Close() error {
	if c == nil || c.db == nil {
		return nil
	}
	return c.db.Close()
}

func (c *Cache) DB() *sql.DB { return c.db }

func (c *Cache) IsCellComplete(ctx context.Context, cell QueryCell) (bool, error) {
	if err := validateCell(cell); err != nil {
		return false, err
	}
	var complete int
	err := c.db.QueryRowContext(ctx, `SELECT complete FROM query_cells WHERE query_signature=? AND spatial_cell=? AND release=?`, cell.Signature, cell.SpatialCell, cell.Release).Scan(&complete)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return complete != 0, err
}

func (c *Cache) PutCell(ctx context.Context, batch CellBatch) error {
	if err := validateCell(batch.Cell); err != nil {
		return err
	}
	if batch.FetchedAt.IsZero() {
		batch.FetchedAt = time.Now().UTC()
	}
	for _, source := range batch.Sources {
		if err := source.Validate(batch.Cell.Release); err != nil {
			return err
		}
	}
	for _, spectrum := range batch.Spectra {
		if err := spectrum.Validate(batch.Cell.Release, spectrum.RepresentationVersion); err != nil {
			return err
		}
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	rollback := func(e error) error { _ = tx.Rollback(); return e }
	stamp := batch.FetchedAt.UnixNano()
	for _, source := range batch.Sources {
		if err := contextErr(ctx); err != nil {
			return rollback(err)
		}
		q, _ := json.Marshal(source.QualityFlags)
		v, _ := json.Marshal(source.VariabilityFlags)
		co, _ := json.Marshal(source.ContaminationFlags)
		_, err = tx.ExecContext(ctx, `INSERT INTO sources(release,source_id,ra,dec,reference_epoch,proper_motion_ra,proper_motion_dec,position_error,proper_motion_error_ra,proper_motion_error_dec,g,bp,rp,g_error,bp_error,rp_error,quality_flags,variability_flags,contamination_flags,spatial_cell,fetched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(release,source_id) DO UPDATE SET ra=excluded.ra,dec=excluded.dec,reference_epoch=excluded.reference_epoch,proper_motion_ra=excluded.proper_motion_ra,proper_motion_dec=excluded.proper_motion_dec,position_error=excluded.position_error,proper_motion_error_ra=excluded.proper_motion_error_ra,proper_motion_error_dec=excluded.proper_motion_error_dec,g=excluded.g,bp=excluded.bp,rp=excluded.rp,g_error=excluded.g_error,bp_error=excluded.bp_error,rp_error=excluded.rp_error,quality_flags=excluded.quality_flags,variability_flags=excluded.variability_flags,contamination_flags=excluded.contamination_flags,spatial_cell=excluded.spatial_cell,fetched_at=excluded.fetched_at`, batch.Cell.Release, source.SourceID, source.RA, source.Dec, source.ReferenceEpoch, source.ProperMotionRA, source.ProperMotionDec, source.PositionError, source.ProperMotionErrorRA, source.ProperMotionErrorDec, source.G, source.BP, source.RP, source.GError, source.BPError, source.RPError, string(q), string(v), string(co), batch.Cell.SpatialCell, stamp)
		if err != nil {
			return rollback(err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO query_cell_sources(query_signature,spatial_cell,release,source_id) VALUES(?,?,?,?)`, batch.Cell.Signature, batch.Cell.SpatialCell, batch.Cell.Release, source.SourceID); err != nil {
			return rollback(err)
		}
	}
	for _, spectrum := range batch.Spectra {
		if err := contextErr(ctx); err != nil {
			return rollback(err)
		}
		w, _ := json.Marshal(spectrum.Wavelengths)
		f, _ := json.Marshal(spectrum.Flux)
		fe, _ := json.Marshal(spectrum.FluxErrors)
		_, err = tx.ExecContext(ctx, `INSERT INTO xp_spectra(release,source_id,representation_version,calibration_version,wavelengths,flux,flux_errors,wavelength_min,wavelength_max,checksum,fetched_at) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(release,source_id,representation_version) DO UPDATE SET calibration_version=excluded.calibration_version,wavelengths=excluded.wavelengths,flux=excluded.flux,flux_errors=excluded.flux_errors,wavelength_min=excluded.wavelength_min,wavelength_max=excluded.wavelength_max,checksum=excluded.checksum,fetched_at=excluded.fetched_at`, spectrum.Release, spectrum.SourceID, spectrum.RepresentationVersion, spectrum.CalibrationVersion, w, f, fe, spectrum.Wavelengths[0], spectrum.Wavelengths[len(spectrum.Wavelengths)-1], spectrumChecksum(spectrum), stamp)
		if err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('gaia_release',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, batch.Cell.Release); err != nil {
		return rollback(err)
	}
	for _, spectrum := range batch.Spectra {
		if _, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "xp_representation:"+spectrum.Release, spectrum.RepresentationVersion); err != nil {
			return rollback(err)
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO query_cells(query_signature,spatial_cell,release,complete,fetched_at,failure_count,next_retry_at,last_error) VALUES(?,?,?,1,?,0,0,'') ON CONFLICT(query_signature,spatial_cell) DO UPDATE SET release=excluded.release,complete=1,fetched_at=excluded.fetched_at,failure_count=0,next_retry_at=0,last_error=''`, batch.Cell.Signature, batch.Cell.SpatialCell, batch.Cell.Release, stamp); err != nil {
		return rollback(err)
	}
	if err := contextErr(ctx); err != nil {
		return rollback(err)
	}
	return tx.Commit()
}

func (c *Cache) Sources(ctx context.Context, cell QueryCell) ([]Source, error) {
	if err := validateCell(cell); err != nil {
		return nil, err
	}
	complete, err := c.IsCellComplete(ctx, cell)
	if err != nil || !complete {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT s.source_id,s.ra,s.dec,s.reference_epoch,s.proper_motion_ra,s.proper_motion_dec,s.position_error,s.proper_motion_error_ra,s.proper_motion_error_dec,s.g,s.bp,s.rp,s.g_error,s.bp_error,s.rp_error,s.quality_flags,s.variability_flags,s.contamination_flags FROM sources s JOIN query_cell_sources m ON m.release=s.release AND m.source_id=s.source_id WHERE m.query_signature=? AND m.spatial_cell=? AND m.release=? ORDER BY s.source_id`, cell.Signature, cell.SpatialCell, cell.Release)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var s Source
		var q, v, co string
		if err := rows.Scan(&s.SourceID, &s.RA, &s.Dec, &s.ReferenceEpoch, &s.ProperMotionRA, &s.ProperMotionDec, &s.PositionError, &s.ProperMotionErrorRA, &s.ProperMotionErrorDec, &s.G, &s.BP, &s.RP, &s.GError, &s.BPError, &s.RPError, &q, &v, &co); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(q), &s.QualityFlags) != nil || json.Unmarshal([]byte(v), &s.VariabilityFlags) != nil || json.Unmarshal([]byte(co), &s.ContaminationFlags) != nil {
			return nil, errors.New("corrupt Gaia source flags")
		}
		s.Release = cell.Release
		if err := s.Validate(cell.Release); err != nil {
			return nil, fmt.Errorf("corrupt Gaia source %d: %w", s.SourceID, err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (c *Cache) Spectrum(ctx context.Context, release string, sourceID uint64, representation string) (XPSpectrum, error) {
	var s XPSpectrum
	var w, f, fe []byte
	var storedChecksum string
	err := c.db.QueryRowContext(ctx, `SELECT calibration_version,wavelengths,flux,flux_errors,checksum FROM xp_spectra WHERE release=? AND source_id=? AND representation_version=?`, release, sourceID, representation).Scan(&s.CalibrationVersion, &w, &f, &fe, &storedChecksum)
	if err != nil {
		return s, err
	}
	s.Release = release
	s.SourceID = sourceID
	s.RepresentationVersion = representation
	if json.Unmarshal(w, &s.Wavelengths) != nil || json.Unmarshal(f, &s.Flux) != nil || json.Unmarshal(fe, &s.FluxErrors) != nil {
		return s, errors.New("corrupt Gaia XP row")
	}
	if err := s.Validate(release, representation); err != nil {
		return s, err
	}
	if storedChecksum != checksumForBytes(w, f, fe) {
		return s, errors.New("Gaia XP checksum mismatch")
	}
	return s, nil
}

func validateCell(c QueryCell) error {
	if c.Signature == "" || c.SpatialCell == "" || c.Release == "" {
		return errors.New("query cell requires signature, spatial cell, and release")
	}
	return nil
}

// DefaultCachePath returns the application-data cache location. An explicit
// override is accepted as-is so callers can support portable installations.
func DefaultCachePath(appData string) (string, error) {
	if appData == "" {
		var err error
		appData, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("resolve application cache directory: %w", err)
		}
	}
	path, err := filepath.Abs(filepath.Join(appData, "gofitsv3", "gaia-cache.sqlite"))
	if err != nil {
		return "", err
	}
	return path, nil
}
func ResolveCachePath(appData, override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	return DefaultCachePath(appData)
}
func contextErr(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
func spectrumChecksum(s XPSpectrum) string {
	w, _ := json.Marshal(s.Wavelengths)
	f, _ := json.Marshal(s.Flux)
	e, _ := json.Marshal(s.FluxErrors)
	return checksumForBytes(w, f, e)
}
func checksumForBytes(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
