package starbench

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

//go:embed web/*
var assets embed.FS

type job struct {
	Running bool   `json:"running"`
	Message string `json:"message"`
	RunID   string `json:"runId,omitempty"`
}
type server struct {
	ctx              context.Context
	mu               sync.Mutex
	jobs             sync.WaitGroup
	dir, host, token string
	dataset          Dataset
	project          Project
	image            fitsio.ImageData
	header           fitsio.Header
	runs             map[string]Run
	evidence         *mosaic.StarMapEvidence
	inputs           []mosaic.StarMapInputRecord
	job              job
	cancel           context.CancelFunc
	release          func()
}

func (s *server) persist(p Project) error {
	// History is written first; a failed publication leaves recoverable labels.
	if err := writeJSON(filepath.Join(s.dir, "history", fmt.Sprintf("labels-%08d.json", p.Revision)), p); err != nil {
		return err
	}
	return writeJSON(filepath.Join(s.dir, "labels.json"), p)
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func problem(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return fmt.Errorf("JSON content type required")
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	return nil
}
func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/image", s.render)
	mux.HandleFunc("GET /api/labels", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Disposition", "attachment; filename=labels.json")
		respond(w, s.project)
	})
	mux.HandleFunc("PUT /api/labels", s.save)
	mux.HandleFunc("POST /api/reveal", s.reveal)
	mux.HandleFunc("POST /api/center-preview", s.previewCenter)
	mux.HandleFunc("POST /api/report", s.report)
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("POST /api/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
		respond(w, map[string]bool{"ok": true})
	})
	web, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(web)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' blob:; connect-src 'self'; frame-ancestors 'none'")
		if r.Host != s.host {
			problem(w, 403, fmt.Errorf("unexpected host"))
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.host {
			problem(w, 403, fmt.Errorf("cross-origin access denied"))
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && r.Header.Get("X-Benchmark-Token") != s.token {
			problem(w, 403, fmt.Errorf("missing session token"))
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *server) state(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := []Run{}
	for _, v := range s.runs {
		v.Sources = nil
		runs = append(runs, v)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt < runs[j].CreatedAt })
	evidence := "mosaic-only"
	if s.evidence != nil && len(s.evidence.Inputs) > 0 {
		evidence = "native evidence from imported baseline"
	}
	respond(w, struct {
		Dataset  Dataset `json:"dataset"`
		Project  Project `json:"project"`
		Runs     []Run   `json:"runs"`
		Job      job     `json:"job"`
		Token    string  `json:"token"`
		Evidence string  `json:"evidence"`
	}{s.dataset, s.project, runs, s.job, s.token, evidence})
}
func (s *server) save(w http.ResponseWriter, r *http.Request) {
	var next Project
	if err := decode(w, r, &next); err != nil {
		problem(w, 400, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if next.Revision != s.project.Revision {
		problem(w, 409, fmt.Errorf("another action changed labels; reload before editing"))
		return
	}
	merged, err := MergeProject(s.project, next, s.dataset)
	if err != nil {
		problem(w, 400, err)
		return
	}
	if err = s.persist(merged); err != nil {
		problem(w, 500, err)
		return
	}
	s.project = merged
	respond(w, merged)
}
func cloneProject(p Project) Project {
	b, _ := json.Marshal(p)
	var next Project
	json.Unmarshal(b, &next)
	return next
}
func (s *server) reveal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegionID string `json:"regionId"`
		RunID    string `json:"runId"`
		Revision int    `json:"revision"`
	}
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[req.RunID]
	if !ok {
		problem(w, 404, fmt.Errorf("unknown run"))
		return
	}
	if req.Revision != s.project.Revision {
		problem(w, 409, fmt.Errorf("labels changed; reload"))
		return
	}
	next := cloneProject(s.project)
	for i := range next.Regions {
		region := &next.Regions[i]
		if region.ID != req.RegionID {
			continue
		}
		if region.RevealedAt == "" {
			region.RevealedAt = now()
			region.RevealedBeforeReview = !region.Reviewed
			next.Revision++
			if err := s.persist(next); err != nil {
				problem(w, 500, err)
				return
			}
			s.project = next
		}
		sources := []processing.StarMapSource{}
		for _, source := range run.Sources {
			if inside(*region, source.X, source.Y, -source.Radius) {
				sources = append(sources, source)
			}
		}
		respond(w, struct {
			Project Project                    `json:"project"`
			Sources []processing.StarMapSource `json:"sources"`
		}{s.project, sources})
		return
	}
	problem(w, 404, fmt.Errorf("unknown region"))
}
func (s *server) report(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunIDs    []string `json:"runIds"`
		Tolerance float64  `json:"tolerance"`
		Split     string   `json:"split"`
		Revision  int      `json:"revision"`
	}
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	if len(req.RunIDs) < 1 || len(req.RunIDs) > 2 || (req.Split != "development" && req.Split != "validation") {
		problem(w, 400, fmt.Errorf("choose one or two runs and a split"))
		return
	}
	s.mu.Lock()
	if req.Revision != s.project.Revision {
		s.mu.Unlock()
		problem(w, 409, fmt.Errorf("labels changed; reload"))
		return
	}
	runs := []Run{}
	for _, id := range req.RunIDs {
		run, ok := s.runs[id]
		if !ok {
			s.mu.Unlock()
			problem(w, 404, fmt.Errorf("unknown run"))
			return
		}
		runs = append(runs, run)
	}
	snapshot := cloneProject(s.project)
	next := cloneProject(s.project)
	count := 0
	for i := range snapshot.Regions {
		region := &snapshot.Regions[i]
		if region.Split != req.Split {
			region.Reviewed = false
			continue
		}
		if region.Reviewed {
			count++
			if next.Regions[i].RevealedAt == "" {
				next.Regions[i].RevealedAt = now()
			}
		}
	}
	if count == 0 {
		s.mu.Unlock()
		problem(w, 400, fmt.Errorf("no fully reviewed regions in this split"))
		return
	}
	// Validate before recording reveal. Snapshot and both reports use identical
	// annotations, even if another browser starts editing during evaluation.
	if !finite(req.Tolerance) || req.Tolerance <= 0 || req.Tolerance > 10 {
		s.mu.Unlock()
		problem(w, 400, fmt.Errorf("matching radius must be in (0,10]"))
		return
	}
	next.Revision++
	if err := s.persist(next); err != nil {
		s.mu.Unlock()
		problem(w, 500, err)
		return
	}
	s.project = next
	s.mu.Unlock()
	reports := []Report{}
	for _, run := range runs {
		report, err := Evaluate(snapshot, run, s.image.Pixels, s.image.Width, s.image.Height, req.Tolerance)
		if err != nil {
			problem(w, 400, err)
			return
		}
		reports = append(reports, report)
	}
	id := newID()
	result := struct {
		ID      string   `json:"id"`
		Labels  Project  `json:"labels"`
		Reports []Report `json:"reports"`
	}{id, snapshot, reports}
	if err := writeJSON(filepath.Join(s.dir, "reports", id+".json"), result); err != nil {
		problem(w, 500, err)
		return
	}
	respond(w, struct {
		Project Project `json:"project"`
		Result  any     `json:"result"`
	}{next, result})
}
func (s *server) startRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name            string  `json:"name"`
		MinSNR          float64 `json:"minSNR"`
		MaxResidual     float64 `json:"maxResidual"`
		FWHM            float64 `json:"fwhm"`
		VerifyOriginals bool    `json:"verifyOriginals"`
	}
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 100 || !finite(req.MinSNR) || req.MinSNR < 3 || req.MinSNR > 100 || !finite(req.MaxResidual) || req.MaxResidual <= 0 || req.MaxResidual > 1 || !finite(req.FWHM) || req.FWHM < 0 || req.FWHM > 8 {
		problem(w, 400, fmt.Errorf("name required; SNR 3..100, residual (0,1], FWHM 0..8 (0 estimates automatically)"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		problem(w, 503, err)
		return
	}
	if s.job.Running {
		problem(w, 409, fmt.Errorf("a detector run is already active"))
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.cancel = cancel
	s.job = job{Running: true, Message: "Starting detector"}
	s.jobs.Add(1)
	go func() {
		defer s.jobs.Done()
		defer cancel()
		run, err := s.createRun(ctx, req.Name, req.VerifyOriginals, processing.StarMapOptions{MinSNR: req.MinSNR, MaxResidual: req.MaxResidual, FWHM: req.FWHM, Progress: func(stage string, done, total int) {
			s.mu.Lock()
			s.job.Message = fmt.Sprintf("%s %d/%d", stage, done, total)
			s.mu.Unlock()
		}})
		s.mu.Lock()
		defer s.mu.Unlock()
		s.cancel = nil
		s.job.Running = false
		if err != nil {
			s.job.Message = err.Error()
			return
		}
		s.runs[run.ID] = run
		s.job.Message = "Run saved"
		s.job.RunID = run.ID
	}()
	respond(w, s.job)
}
func (s *server) createRun(ctx context.Context, name string, verifyOriginals bool, opt processing.StarMapOptions) (Run, error) {
	var evidence *mosaic.StarMapEvidence
	var inputs []mosaic.StarMapInputRecord
	if verifyOriginals {
		if s.evidence == nil || len(s.evidence.Inputs) == 0 {
			return Run{}, fmt.Errorf("original-exposure verification is unavailable for this image")
		}
		evidence, inputs = s.evidence, s.inputs
	}
	for _, input := range inputs {
		st, err := os.Stat(input.Path)
		if err != nil {
			return Run{}, err
		}
		if st.Size() != input.Size || st.ModTime().UnixNano() != input.Modified {
			return Run{}, fmt.Errorf("native input changed: %s; start a new benchmark", input.Path)
		}
	}
	product, err := mosaic.CreateStarMap(ctx, s.image, s.header, evidence, opt)
	if err != nil {
		return Run{}, err
	}
	for _, input := range inputs {
		st, err := os.Stat(input.Path)
		if err != nil {
			return Run{}, err
		}
		if st.Size() != input.Size || st.ModTime().UnixNano() != input.Modified {
			return Run{}, fmt.Errorf("native input changed during run: %s", input.Path)
		}
	}
	id := newID()
	path := filepath.Join(s.dir, "runs", id, "starmap.fits")
	if err = mosaic.SaveStarMapFITS(ctx, path, product, s.image.Pixels); err != nil {
		return Run{}, err
	}
	hash, err := fileHash(path)
	if err != nil {
		return Run{}, err
	}
	exe, version := codeIdentity()
	run := Run{ID: id, Name: name, CreatedAt: now(), DatasetHash: s.dataset.SHA256, MapSHA256: hash, ExecutableHash: exe, SourceVersion: version, EvidenceMode: product.EvidenceMode, Origin: "Generated by the shared GoFitsV3 detector", MinSNR: product.Map.Options.MinSNR, MaxResidual: product.Map.Options.MaxResidual, FWHM: product.Map.FWHM, Sources: product.Map.Sources}
	run.RequestedFWHM = &opt.FWHM
	run.VerifyOriginals = &verifyOriginals
	if err = writeJSON(filepath.Join(s.dir, "runs", id, "run.json"), run); err != nil {
		return Run{}, err
	}
	return run, nil
}
