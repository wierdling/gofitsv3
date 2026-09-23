package starbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"time"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
)

// RunCLI serves a benchmark project locally. Labels, runs and reports are
// independent of the desktop application's star-map review overrides.
func RunCLI(args []string) error {
	flags := flag.NewFlagSet("starmap benchmark", flag.ContinueOnError)
	input := flags.String("input", "", "linear mosaic FITS (required)")
	maps := flags.String("maps", "", "comma-separated star-map FITS to freeze; default working star map")
	dir := flags.String("dir", "", "benchmark directory; default working/benchmark/<mosaic stem>")
	listen := flags.String("listen", "127.0.0.1:8787", "loopback address; port 0 chooses a free port")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-input is required")
	}
	source, err := filepath.Abs(*input)
	if err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-listen must be a loopback IP address")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	s, err := openProject(ctx, source, *maps, *dir)
	if err != nil {
		return err
	}
	hub := newProjectHub(s)
	defer hub.close()
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	s.host = listener.Addr().String()
	httpServer := &http.Server{Handler: hub, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			httpServer.Shutdown(c)
		case <-done:
		}
	}()
	fmt.Printf("Star Map Benchmark: http://%s\nLabels and frozen runs: %s\nPress Ctrl+C to stop.\n", s.host, s.dir)
	err = httpServer.Serve(listener)
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	httpServer.Shutdown(shutdownCtx)
	hub.close()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func codeIdentity() (string, string) {
	path, err := os.Executable()
	if err != nil {
		return "", "unknown"
	}
	hash, _ := fileHash(path)
	version := "unversioned"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				version = setting.Value
			}
			if setting.Key == "vcs.modified" && setting.Value == "true" {
				version += " (modified)"
			}
		}
	}
	return hash, version
}
func safeID(id string) bool {
	if len(id) != 24 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *server) importMap(ctx context.Context, path string) error {
	hash, err := fileHash(path)
	if err != nil {
		return err
	}
	product, err := mosaic.LoadStarMapFITS(ctx, path, s.image, s.header)
	if err != nil {
		return err
	}
	// The first imported map defines the evidence for new runs in this session.
	if s.evidence == nil && len(product.Inputs) > 0 {
		width, _ := fitsio.HeaderFloat(product.ReferenceHeader, "NAXIS1")
		height, _ := fitsio.HeaderFloat(product.ReferenceHeader, "NAXIS2")
		if width <= 0 || height <= 0 {
			return fmt.Errorf("baseline lacks reference image dimensions")
		}
		ev := &mosaic.StarMapEvidence{Geometry: product.Geometry, Reference: mosaic.Input{Path: product.ReferencePath, HDU: fitsio.HDU{Header: product.ReferenceHeader, Data: fitsio.ImageData{Width: int(width), Height: int(height)}}}}
		for _, r := range product.Inputs {
			ev.Inputs = append(ev.Inputs, mosaic.Input{Path: r.Path, SCIExt: r.SCIExt, OffsetX: r.OffsetX, OffsetY: r.OffsetY, ManualTransform: r.Transform, HasManualTransform: r.HasTransform})
		}
		s.evidence = ev
		s.inputs = product.Inputs
	}
	for _, r := range s.runs {
		if r.MapSHA256 == hash {
			return nil
		}
	}
	id := newID()
	run := Run{ID: id, Name: filepath.Base(path), CreatedAt: now(), DatasetHash: s.dataset.SHA256, MapSHA256: hash, EvidenceMode: product.EvidenceMode, Origin: "Imported FITS; automatic statuses, footprints as saved. Detector executable identity unavailable.", SourceVersion: "unknown (imported)", MinSNR: product.Map.Options.MinSNR, MaxResidual: product.Map.Options.MaxResidual, FWHM: product.Map.FWHM, Sources: product.Map.Sources}
	// Preserve the exact imported FITS, including provenance. Scoring ignores
	// reviewer overrides; previously edited radii cannot be reconstructed.
	if err = os.MkdirAll(filepath.Join(s.dir, "runs", id), 0755); err != nil {
		return err
	}
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(s.dir, "runs", id, "starmap.fits"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	copiedHash, err := fileHash(filepath.Join(s.dir, "runs", id, "starmap.fits"))
	if err != nil {
		return err
	}
	if copiedHash != hash {
		return fmt.Errorf("star map changed while importing; retry after its writer finishes")
	}
	if err = writeJSON(filepath.Join(s.dir, "runs", id, "run.json"), run); err != nil {
		return err
	}
	s.runs[id] = run
	return nil
}
