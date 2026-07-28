package gaia

import (
	"context"
	"database/sql"
	"fmt"
)

const schemaVersion = 2

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(CAST(value AS INTEGER), 0) FROM metadata WHERE key='schema_version'`).Scan(&version)
	if err == sql.ErrNoRows {
		version = 0
	} else if err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("unsupported Gaia cache schema version %d", version)
	}
	for version < schemaVersion {
		switch version + 1 {
		case 1:
			if err := migrateV1(ctx, db); err != nil {
				return err
			}
		case 2:
			if err := migrateV2(ctx, db); err != nil {
				return err
			}
		}
		version++
		if _, err := db.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES('schema_version',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, version); err != nil {
			return err
		}
	}
	return nil
}

func migrateV2(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	fail := func(e error) error { _ = tx.Rollback(); return e }
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS query_cell_sources (
		query_signature TEXT NOT NULL, spatial_cell TEXT NOT NULL, release TEXT NOT NULL,
		source_id INTEGER NOT NULL,
		PRIMARY KEY(query_signature, spatial_cell, release, source_id),
		FOREIGN KEY(release, source_id) REFERENCES sources(release, source_id) ON DELETE CASCADE
	)`)
	if err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO query_cell_sources(query_signature,spatial_cell,release,source_id) SELECT q.query_signature,q.spatial_cell,q.release,s.source_id FROM query_cells q JOIN sources s ON s.release=q.release AND s.spatial_cell=q.spatial_cell WHERE q.complete=1`); err != nil {
		return fail(err)
	}
	if _, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS query_cell_sources_source_idx ON query_cell_sources(release, source_id)`); err != nil {
		return fail(err)
	}
	return tx.Commit()
}

func migrateV1(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS sources (
			release TEXT NOT NULL, source_id INTEGER NOT NULL,
			ra REAL NOT NULL, dec REAL NOT NULL, reference_epoch REAL NOT NULL,
			proper_motion_ra REAL NOT NULL, proper_motion_dec REAL NOT NULL,
			position_error REAL NOT NULL, proper_motion_error_ra REAL NOT NULL, proper_motion_error_dec REAL NOT NULL,
			g REAL NOT NULL, bp REAL NOT NULL, rp REAL NOT NULL,
			g_error REAL NOT NULL, bp_error REAL NOT NULL, rp_error REAL NOT NULL,
			quality_flags TEXT NOT NULL, variability_flags TEXT NOT NULL, contamination_flags TEXT NOT NULL,
			spatial_cell TEXT NOT NULL, fetched_at INTEGER NOT NULL,
			PRIMARY KEY(release, source_id)
		)`,
		`CREATE INDEX IF NOT EXISTS sources_spatial_idx ON sources(release, spatial_cell)`,
		`CREATE TABLE IF NOT EXISTS query_cell_sources (
			query_signature TEXT NOT NULL, spatial_cell TEXT NOT NULL, release TEXT NOT NULL,
			source_id INTEGER NOT NULL,
			PRIMARY KEY(query_signature, spatial_cell, release, source_id),
			FOREIGN KEY(release, source_id) REFERENCES sources(release, source_id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS query_cell_sources_source_idx ON query_cell_sources(release, source_id)`,
		`CREATE TABLE IF NOT EXISTS xp_spectra (
			release TEXT NOT NULL, source_id INTEGER NOT NULL, representation_version TEXT NOT NULL,
			calibration_version TEXT NOT NULL, wavelengths BLOB NOT NULL, flux BLOB NOT NULL,
			flux_errors BLOB NOT NULL, wavelength_min REAL NOT NULL, wavelength_max REAL NOT NULL,
			checksum TEXT NOT NULL, fetched_at INTEGER NOT NULL,
			PRIMARY KEY(release, source_id, representation_version),
			FOREIGN KEY(release, source_id) REFERENCES sources(release, source_id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS query_cells (
			query_signature TEXT NOT NULL, spatial_cell TEXT NOT NULL, release TEXT NOT NULL,
			complete INTEGER NOT NULL DEFAULT 0, fetched_at INTEGER NOT NULL DEFAULT 0,
			failure_count INTEGER NOT NULL DEFAULT 0, next_retry_at INTEGER NOT NULL DEFAULT 0,
			last_error TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(query_signature, spatial_cell)
		)`,
		`CREATE INDEX IF NOT EXISTS query_cells_release_idx ON query_cells(release, spatial_cell, complete)`,
	}
	for _, stmt := range statements {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
