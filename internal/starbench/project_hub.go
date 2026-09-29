package starbench

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// projectHub serializes switches against HTTP operations. Background detector
// jobs belong to their original server and must finish before switching.
type projectHub struct {
	mu      sync.RWMutex
	current *server
	roots   map[string]string
	open    func(context.Context, string, string, string) (*server, error)
}

func projectKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
func newProjectHub(s *server) *projectHub {
	return &projectHub{current: s, roots: map[string]string{projectKey(s.dataset.SciencePath): s.dir}, open: openProject}
}
func (h *projectHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.current
	if s == nil {
		return
	}
	s.jobs.Wait()
	if s.release != nil {
		s.release()
		s.release = nil
	}
}
func (h *projectHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/open" && r.Method == "POST" {
		h.switchProject(w, r)
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if r.URL.Path == "/api/files" && r.Method == "GET" {
		if err := hubAccess(h.current, r, true); err != nil {
			problem(w, 403, err)
			return
		}
		h.listFiles(w, r)
		return
	}
	h.current.handler().ServeHTTP(w, r)
}
func hubAccess(s *server, r *http.Request, token bool) error {
	if r.Host != s.host {
		return fmt.Errorf("unexpected host")
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.host {
		return fmt.Errorf("cross-origin access denied")
	}
	if token && r.Header.Get("X-Benchmark-Token") != s.token {
		return fmt.Errorf("workspace changed; save/export any local edits and reload")
	}
	return nil
}
func (h *projectHub) switchProject(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.current
	w.Header().Set("Cache-Control", "no-store")
	if err := hubAccess(old, r, true); err != nil {
		problem(w, 403, err)
		return
	}
	var req struct {
		Path     string `json:"path"`
		Revision int    `json:"revision"`
	}
	if err := decode(w, r, &req); err != nil {
		problem(w, 400, err)
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(req.Path))
	if err != nil || strings.TrimSpace(req.Path) == "" {
		problem(w, 400, fmt.Errorf("select a local FITS file"))
		return
	}
	old.mu.Lock()
	running := old.job.Running
	revision := old.project.Revision
	old.mu.Unlock()
	if revision != req.Revision {
		problem(w, 409, fmt.Errorf("labels changed in another browser; reload before switching"))
		return
	}
	if running {
		problem(w, 409, fmt.Errorf("wait for the detector run to finish, or cancel it, before loading another file"))
		return
	}
	if projectKey(path) == projectKey(old.dataset.SciencePath) {
		hash, err := fileHash(path)
		if err != nil {
			problem(w, 400, err)
			return
		}
		if hash != old.dataset.SHA256 {
			problem(w, 409, fmt.Errorf("this science file changed on disk; save it under a new name or start a new benchmark directory"))
			return
		}
		respond(w, map[string]bool{"ok": true})
		return
	}
	// The first workspace may use -dir; remember that override on return. Other
	// images use their own normal working/benchmark/<stem> directory.
	next, err := h.open(old.ctx, path, "", h.roots[projectKey(path)])
	if err != nil {
		problem(w, 400, err)
		return
	}
	next.host = old.host
	h.current = next
	h.roots[projectKey(path)] = next.dir
	if old.release != nil {
		old.release()
		old.release = nil
	}
	respond(w, map[string]bool{"ok": true})
}

type fileEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

func (h *projectHub) listFiles(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path = filepath.Dir(h.current.dataset.SciencePath)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		problem(w, 400, err)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		problem(w, 400, err)
		return
	}
	files := []fileEntry{}
	for _, entry := range entries {
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if !entry.IsDir() && ext != ".fits" && ext != ".fit" && ext != ".fts" {
			continue
		}
		files = append(files, fileEntry{Name: entry.Name(), Path: filepath.Join(path, entry.Name()), Directory: entry.IsDir()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Directory != files[j].Directory {
			return files[i].Directory
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	respond(w, struct {
		Path   string      `json:"path"`
		Parent string      `json:"parent"`
		Files  []fileEntry `json:"files"`
	}{path, filepath.Dir(path), files})
}
