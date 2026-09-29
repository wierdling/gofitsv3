package starbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
)

func testServer(t *testing.T) *server {
	t.Helper()
	p, run, pixels := fixture()
	run.Sources = []processing.StarMapSource{star(1, 10, 10)}
	return &server{ctx: context.Background(), dir: t.TempDir(), host: "127.0.0.1:8787", token: "test-token", dataset: Dataset{SHA256: p.DatasetHash, Width: 64, Height: 64}, project: p, image: fitsio.ImageData{Width: 64, Height: 64, Pixels: pixels}, runs: map[string]Run{run.ID: run}}
}
func request(s *server, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://"+s.host+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Benchmark-Token", s.token)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	return w
}
func TestSavePersistsLabelsAndRefusesStaleOverwrite(t *testing.T) {
	s := testServer(t)
	next := cloneProject(s.project)
	next.Labels = []Label{label("a", "star", 10, 10)}
	w := request(s, "PUT", "/api/labels", next)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var saved Project
	if err := readJSON(filepath.Join(s.dir, "labels.json"), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 1 || len(saved.Labels) != 1 {
		t.Fatalf("saved: %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "history", "labels-00000001.json")); err != nil {
		t.Fatal(err)
	}
	if w = request(s, "PUT", "/api/labels", next); w.Code != 409 {
		t.Fatalf("stale overwrite: %d", w.Code)
	}
	if len(s.project.Labels) != 1 || s.project.Revision != 1 {
		t.Fatal("conflict changed labels")
	}
}
func TestServerRejectsForeignHostOriginAndMissingToken(t *testing.T) {
	s := testServer(t)
	for _, name := range []string{"host", "origin", "token"} {
		t.Run(name, func(t *testing.T) {
			b, _ := json.Marshal(s.project)
			r := httptest.NewRequest("PUT", "http://"+s.host+"/api/labels", bytes.NewReader(b))
			r.Header.Set("X-Benchmark-Token", s.token)
			r.Header.Set("Content-Type", "application/json")
			switch name {
			case "host":
				r.Host = "attacker.example"
			case "origin":
				r.Header.Set("Origin", "https://attacker.example")
			case "token":
				r.Header.Del("X-Benchmark-Token")
			}
			w := httptest.NewRecorder()
			s.handler().ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("access accepted: %d", w.Code)
			}
		})
	}
}
func TestStateHidesCatalogAndRevealPreservesAudit(t *testing.T) {
	s := testServer(t)
	s.project.Regions[0].Reviewed = false
	w := request(s, "GET", "/api/state", nil)
	if strings.Contains(w.Body.String(), `"Radius"`) {
		t.Fatal("state exposed detections")
	}
	w = request(s, "POST", "/api/reveal", map[string]any{"regionId": "r", "runId": "baseline", "revision": 0})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"Radius":3`) || s.project.Regions[0].RevealedAt == "" || !s.project.Regions[0].RevealedBeforeReview {
		t.Fatal("reveal missing sources or audit")
	}
}
func TestReportUsesFrozenLabelsAndDoesNotRevealOtherSplit(t *testing.T) {
	s := testServer(t)
	s.dataset.Width = 128
	s.image.Width = 128
	s.image.Pixels = make([]float32, 128*64)
	s.project.Labels = []Label{label("a", "star", 10, 10)}
	s.project.Regions = append(s.project.Regions, Region{ID: "heldout", Name: "Held out", X: 64, Width: 64, Height: 64, Split: "validation", Reviewed: true})
	w := request(s, "POST", "/api/report", map[string]any{"runIds": []string{"baseline"}, "tolerance": 2, "split": "development", "revision": 0})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if s.project.Revision != 1 || s.project.Regions[0].RevealedAt == "" || s.project.Regions[1].RevealedAt != "" {
		t.Fatal("report reveal affected wrong partition")
	}
	files, _ := filepath.Glob(filepath.Join(s.dir, "reports", "*.json"))
	if len(files) != 1 {
		t.Fatal("report not saved")
	}
	var value struct {
		ID      string   `json:"id"`
		Labels  Project  `json:"labels"`
		Reports []Report `json:"reports"`
	}
	if err := readJSON(files[0], &value); err != nil {
		t.Fatal(err)
	}
	if value.Labels.Revision != 0 || value.Reports[0].LabelsRevision != 0 || value.Reports[0].Splits["development"].TruePositive != 1 || value.Reports[0].Splits["validation"].Recall != nil {
		t.Fatal("report snapshot is inconsistent")
	}
}
func TestRenderPreservesFITSOrientationAndMarksInvalidPixels(t *testing.T) {
	s := testServer(t)
	s.image.Pixels[0] = 100
	s.image.Pixels[63*64] = float32(math.NaN())
	w := request(s, "GET", "/api/image?region=r&gain=20", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	img, err := png.Decode(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(0, 63).RGBA()
	if r != 65535 || g != 65535 || b != 65535 {
		t.Fatal("bright bottom FITS pixel not displayed at bottom")
	}
	r, g, b, _ = img.At(0, 0).RGBA()
	if r <= g || b <= g {
		t.Fatal("invalid top pixel not tinted")
	}
	w = request(s, "GET", "/api/image?region=../../secret", nil)
	if w.Code != 404 {
		t.Fatal("invalid image selector accepted")
	}
}

func TestSaveFailureDoesNotAdvanceInMemoryRevision(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(s.dir, "not-a-directory")
	if err := os.WriteFile(path, []byte("occupied"), 0644); err != nil {
		t.Fatal(err)
	}
	s.dir = path
	next := cloneProject(s.project)
	next.Labels = []Label{label("a", "star", 10, 10)}
	w := request(s, "PUT", "/api/labels", next)
	if w.Code != 500 {
		t.Fatal(w.Body.String())
	}
	if s.project.Revision != 0 || len(s.project.Labels) != 0 {
		t.Fatal("failed save published in memory")
	}
}

func TestCancelledServerRefusesNewDetectorRun(t *testing.T) {
	s := testServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.ctx = ctx
	w := request(s, "POST", "/api/runs", map[string]any{"name": "cancelled", "minSNR": 7, "maxResidual": .22, "fwhm": 0})
	if w.Code != 503 || s.job.Running {
		t.Fatalf("shutdown accepted a job: %d %+v", w.Code, s.job)
	}
}

func TestRunRejectsOptionsOutsideSharedDetectorRange(t *testing.T) {
	for _, options := range []struct{ snr, fwhm float64 }{{2, 0}, {7, 9}} {
		s := testServer(t)
		w := request(s, "POST", "/api/runs", map[string]any{"name": "invalid", "minSNR": options.snr, "maxResidual": .22, "fwhm": options.fwhm})
		if w.Code != 400 || s.job.Running {
			t.Fatal("invalid options started a job")
		}
	}
}

// Missing native files must not block the default mosaic-only run, but must
// still fail verification rather than silently weakening a requested check.
func TestRunVerificationChoiceControlsNativeInputs(t *testing.T) {
	for _, verify := range []bool{false, true} {
		t.Run(fmt.Sprint(verify), func(t *testing.T) {
			root := t.TempDir()
			science := scienceFile(t, root, "science.fits", 1)
			s, err := openProject(context.Background(), science, "", filepath.Join(root, "benchmark"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.release()
			for y := 0; y < 64; y++ {
				for x := 0; x < 64; x++ {
					dx, dy := float64(x-32), float64(y-32)
					s.image.Pixels[y*64+x] = float32(10 + 500*math.Exp(-(dx*dx+dy*dy)/2.5))
				}
			}
			s.host, s.token = "127.0.0.1:8787", "test-token"
			missing := filepath.Join(s.dir, "missing-flt.fits")
			s.inputs = []mosaic.StarMapInputRecord{{Path: missing}}
			s.evidence = &mosaic.StarMapEvidence{Inputs: []mosaic.Input{{Path: missing}}}
			body := map[string]any{"name": "verification test", "minSNR": 5, "maxResidual": 0.4, "fwhm": 0}
			if verify {
				body["verifyOriginals"] = true
			}
			w := request(s, "POST", "/api/runs", body)
			if w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			s.jobs.Wait()
			if verify {
				if s.job.RunID != "" || !strings.Contains(s.job.Message, "missing-flt.fits") {
					t.Fatalf("verification skipped native input: %+v", s.job)
				}
				return
			}
			run, ok := s.runs[s.job.RunID]
			if !ok || run.EvidenceMode != "mosaic-only" || run.VerifyOriginals == nil || *run.VerifyOriginals {
				t.Fatalf("mosaic run failed: %+v, %+v", s.job, run)
			}
			var saved Run
			if err := readJSON(filepath.Join(s.dir, "runs", run.ID, "run.json"), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.VerifyOriginals == nil || *saved.VerifyOriginals {
				t.Fatal("verification choice not persisted")
			}
			product, err := mosaic.LoadStarMapFITS(context.Background(), filepath.Join(s.dir, "runs", run.ID, "starmap.fits"), s.image, s.header)
			if err != nil {
				t.Fatal(err)
			}
			if product.EvidenceMode != "mosaic-only" || len(product.Inputs) != 0 {
				t.Fatal("mosaic-only FITS retained native evidence")
			}
		})
	}
}
func TestRunRefusesVerificationWithoutEvidence(t *testing.T) {
	s := testServer(t)
	_, err := s.createRun(context.Background(), "verified", true, processing.DefaultStarMapOptions())
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("missing evidence accepted: %v", err)
	}
}
