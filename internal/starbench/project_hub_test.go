package starbench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gofitsv3/internal/fitsio"
)

func scienceFile(t *testing.T, root, name string, level float32) string {
	t.Helper()
	path := filepath.Join(root, name)
	p := make([]float32, 64*64)
	for i := range p {
		p[i] = level
	}
	if err := fitsio.WriteFloat32Image(path, fitsio.Header{Cards: map[string]string{"FILTER": "'TEST'"}}, fitsio.ImageData{Width: 64, Height: 64, Pixels: p}); err != nil {
		t.Fatal(err)
	}
	return path
}
func hubRequest(h *projectHub, token, method, path string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, bytes.NewReader(b))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Benchmark-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func loadedHub(t *testing.T) (*projectHub, string, string) {
	t.Helper()
	root := t.TempDir()
	a := scienceFile(t, root, "a.fits", 1)
	b := scienceFile(t, root, "b.fits", 2)
	s, err := openProject(context.Background(), a, "", filepath.Join(root, "custom-a"))
	if err != nil {
		t.Fatal(err)
	}
	s.host = "127.0.0.1:8787"
	h := newProjectHub(s)
	t.Cleanup(h.close)
	return h, a, b
}

func TestSwitchFileRestoresLabelsAndOriginalCustomDirectory(t *testing.T) {
	h, a, b := loadedHub(t)
	first := h.current
	initialToken := first.token
	next := cloneProject(first.project)
	next.Labels = []Label{{ID: "saved", RegionID: next.Regions[0].ID, X: 20, Y: 21, Kind: "star", Note: "retained"}}
	if w := hubRequest(h, initialToken, "PUT", "/api/labels", next); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := hubRequest(h, initialToken, "POST", "/api/open", map[string]any{"path": b, "revision": 1}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if h.current.dataset.SciencePath != b || len(h.current.project.Labels) != 0 {
		t.Fatal("new image reused old labels")
	}
	if _, err := os.Stat(filepath.Join(first.dir, "server.lock")); !os.IsNotExist(err) {
		t.Fatal("old workspace lock retained")
	}
	if w := hubRequest(h, initialToken, "PUT", "/api/labels", next); w.Code != 403 {
		t.Fatal("old browser token could write new workspace")
	}
	if w := hubRequest(h, h.current.token, "GET", "/api/image?region=overview&dataset="+first.dataset.SHA256, nil); w.Code != 409 {
		t.Fatal("stale image request displayed new science")
	}
	if w := hubRequest(h, h.current.token, "POST", "/api/open", map[string]any{"path": a, "revision": 0}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if h.current.dir != first.dir || h.current.project.Revision != 1 || len(h.current.project.Labels) != 1 || h.current.project.Labels[0].Note != "retained" {
		t.Fatal("return to original image lost saved workspace")
	}
}
func TestFailedOrBusySwitchLeavesCurrentWorkspaceIntact(t *testing.T) {
	h, _, b := loadedHub(t)
	original := h.current
	for _, scenario := range []string{"missing", "busy", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			path := b
			revision := 0
			want := 409
			switch scenario {
			case "missing":
				path = b + "missing"
				want = 400
			case "busy":
				original.job.Running = true
			case "stale":
				revision = 50
			}
			w := hubRequest(h, original.token, "POST", "/api/open", map[string]any{"path": path, "revision": revision})
			original.job.Running = false
			if w.Code != want || h.current != original {
				t.Fatalf("switch lost original: %d %s", w.Code, w.Body.String())
			}
			if _, err := os.Stat(filepath.Join(original.dir, "server.lock")); err != nil {
				t.Fatal("original lock lost")
			}
		})
	}
}
func TestFailedProjectLoadReleasesOnlyItsOwnLock(t *testing.T) {
	root := t.TempDir()
	path := scienceFile(t, root, "a.fits", 1)
	dir := filepath.Join(root, "bad-project")
	if err := writeJSON(filepath.Join(dir, "dataset.json"), Dataset{Version: 1, SHA256: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := openProject(context.Background(), path, "", dir); err == nil {
		t.Fatal("incompatible dataset accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, "server.lock")); !os.IsNotExist(err) {
		t.Fatal("failed load leaked lock")
	}
	if err := os.WriteFile(filepath.Join(dir, "server.lock"), []byte("another server"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openProject(context.Background(), path, "", dir); err == nil {
		t.Fatal("locked project accepted")
	}
	content, _ := os.ReadFile(filepath.Join(dir, "server.lock"))
	if string(content) != "another server" {
		t.Fatal("removed another server's lock")
	}
}
func TestBrowseRequiresSessionAndFiltersFileTypes(t *testing.T) {
	h, a, _ := loadedHub(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(a), "notes.txt"), []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := hubRequest(h, "", "GET", "/api/files", nil); w.Code != 403 {
		t.Fatal("directory listing lacks session protection")
	}
	w := hubRequest(h, h.current.token, "GET", "/api/files", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "notes.txt") || !strings.Contains(w.Body.String(), "a.fits") {
		t.Fatal("wrong browser file filtering")
	}
}
