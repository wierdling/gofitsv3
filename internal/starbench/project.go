package starbench

import (
	"context"
	"fmt"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"os"
	"path/filepath"
	"strings"
)

// openProject acquires its own directory lock and publishes no active state.
// Callers keep their previous workspace until this has fully succeeded.
func openProject(ctx context.Context, input, mapPaths, dir string) (result *server, err error) {
	source, err := filepath.Abs(input)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	before, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	f, err := fitsio.LoadFile(source)
	if err != nil {
		return nil, err
	}
	if len(f.HDUs) == 0 || f.HDUs[0].Data.Width < 16 || f.HDUs[0].Data.Height < 16 {
		return nil, fmt.Errorf("select a primary-image mosaic at least 16 by 16 pixels")
	}
	h := f.HDUs[0]
	if fitsio.HeaderString(h.Header, "PRODUCT") == "STARMAP" {
		return nil, fmt.Errorf("select the science mosaic, not a star-map mask")
	}
	if len(h.Data.Pixels) != h.Data.Width*h.Data.Height {
		return nil, fmt.Errorf("science mosaic has no floating-point image")
	}
	hash, err := fileHash(source)
	if err != nil {
		return nil, err
	}
	after, err := os.Stat(source)
	if err != nil {
		return nil, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("science file changed while loading; retry when its writer has finished")
	}
	dataset := Dataset{Version: SchemaVersion, SciencePath: source, SHA256: hash, Width: h.Data.Width, Height: h.Data.Height, Filter: fitsio.HeaderString(h.Header, "FILTER")}
	if dir == "" {
		dir = filepath.Join(filepath.Dir(source), "working", "benchmark", strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)))
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(root, "server.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("benchmark is locked (%s): %w; if a previous server crashed, verify it has stopped before removing server.lock", lockPath, err)
	}
	fmt.Fprintf(lock, "pid=%d\n", os.Getpid())
	lock.Close()
	release := func() { _ = os.Remove(lockPath) }
	defer func() {
		if err != nil {
			release()
		}
	}()
	var existing Dataset
	if err = readJSON(filepath.Join(root, "dataset.json"), &existing); err == nil {
		if existing.Version != SchemaVersion || existing.SHA256 != hash || existing.Width != dataset.Width || existing.Height != dataset.Height {
			return nil, fmt.Errorf("this directory belongs to different science data; use a new -dir")
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err = writeJSON(filepath.Join(root, "dataset.json"), dataset); err != nil {
		return nil, err
	}
	s := &server{ctx: ctx, dir: root, dataset: dataset, image: h.Data, header: h.Header, token: newID(), release: release, runs: map[string]Run{}}
	if err = readJSON(filepath.Join(root, "labels.json"), &s.project); os.IsNotExist(err) {
		s.project = DefaultProject(dataset)
		err = s.persist(s.project)
	}
	if err != nil {
		return nil, err
	}
	if err = ValidateProject(s.project, dataset); err != nil {
		return nil, err
	}
	files, err := filepath.Glob(filepath.Join(root, "runs", "*", "run.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range files {
		var run Run
		if err = readJSON(path, &run); err != nil {
			return nil, err
		}
		if !safeID(run.ID) || run.DatasetHash != hash || filepath.Base(filepath.Dir(path)) != run.ID {
			return nil, fmt.Errorf("invalid saved run %s", path)
		}
		frozenHash, err := fileHash(filepath.Join(filepath.Dir(path), "starmap.fits"))
		if err != nil {
			return nil, err
		}
		if frozenHash != run.MapSHA256 {
			return nil, fmt.Errorf("frozen FITS changed for run %s", run.Name)
		}
		s.runs[run.ID] = run
	}
	if mapPaths == "" {
		path := mosaic.StarMapWorkingPath(source)
		if _, e := os.Stat(path); e == nil {
			mapPaths = path
		}
		if mapPaths == "" && len(files) > 0 {
			mapPaths = filepath.Join(filepath.Dir(files[0]), "starmap.fits")
		}
	}
	for _, path := range strings.Split(mapPaths, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if err = s.importMap(ctx, path); err != nil {
			return nil, fmt.Errorf("import %s: %w", path, err)
		}
	}
	if len(s.runs) == 0 {
		fmt.Println("No baseline map found. Create a run in the browser (mosaic-only evidence).")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return s, nil
}
