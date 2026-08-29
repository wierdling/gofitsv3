package ui

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sync"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// editDiskStore owns the Edit-side copy of a Compose result. Compose may
// remove its session immediately after handoff; Edit never retains those
// paths. Recipe state is small and replayable against the immutable baseline.
type editDiskStore struct {
	mu                    sync.Mutex
	root                  string
	source, current       [3]string
	width, height         int
	baseline              models.RgbLevels
	recipe                processing.DiskEditRecipe
	generation            uint64
	undo                  [3]string
	undoWidth, undoHeight int
}

// editExportSnapshot is a temporary, Edit-owned render of a recipe. It is
// deliberately separate from current: Save must include controls that have
// not been applied yet without changing the displayed Edit state.
type editExportSnapshot struct {
	planes        [3]string
	width, height int
	cleanupOnce   sync.Once
}

func (s *editExportSnapshot) cleanup() {
	if s == nil {
		return
	}
	s.cleanupOnce.Do(func() {
		for _, p := range s.planes {
			if p != "" {
				_ = os.Remove(p)
			}
		}
		if len(s.planes) > 0 {
			_ = os.Remove(filepath.Dir(s.planes[0]))
		}
	})
}

func newEditDiskStore(root string, source [3]string, width, height int, baseline models.RgbLevels) (*editDiskStore, error) {
	if root == "" || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid disk Edit store")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	s := &editDiskStore{root: root, width: width, height: height, baseline: baseline}
	for i, p := range source {
		if p == "" {
			s.Close()
			return nil, fmt.Errorf("missing source plane %d", i)
		}
		a, err := fitsio.OpenFloat32ArtifactReadOnly(p)
		if err != nil {
			s.Close()
			return nil, err
		}
		if a.Width != width || a.Height != height {
			_ = a.Close()
			s.Close()
			return nil, fmt.Errorf("source plane %d dimensions mismatch", i)
		}
		_ = a.Close()
		dst := filepath.Join(root, fmt.Sprintf("source-%d.bin", i))
		if err := fitsio.CopyFloat32Artifact(p, dst); err != nil {
			s.Close()
			return nil, err
		}
		s.source[i], s.current[i] = dst, dst
	}
	s.recipe = identityDiskEditRecipe()
	return s, nil
}

func identityDiskEditRecipe() processing.DiskEditRecipe {
	r := processing.DiskEditRecipe{Min: [3]float64{0, 0, 0}, Max: [3]float64{255, 255, 255}}
	for c := range r.Curves {
		for i := range r.Curves[c] {
			r.Curves[c][i] = byte(i)
		}
	}
	return r
}

func (s *editDiskStore) Render(ctx context.Context, recipe processing.DiskEditRecipe, previewMax int) (processing.DiskEditRenderResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.root == "" {
		return processing.DiskEditRenderResult{}, fmt.Errorf("disk Edit store is closed")
	}
	s.generation++
	gen := s.generation
	var out [3]string
	for i := range out {
		out[i] = filepath.Join(s.root, fmt.Sprintf("render-%d-%d.bin", gen, i))
	}
	// Every recipe is replayed against the immutable Compose baseline. This
	// avoids cumulative rounding/curve application across repeated Apply.
	result, err := processing.RenderDiskEdit(ctx, processing.DiskEditRenderRequest{Source: s.source, Output: out, Width: s.width, Height: s.height, Baseline: s.baseline, Recipe: recipe, PreviewMax: previewMax})
	if err != nil {
		return processing.DiskEditRenderResult{}, err
	}
	for _, p := range s.current {
		if p != "" && p != s.source[0] && p != s.source[1] && p != s.source[2] {
			_ = os.Remove(p)
		}
	}
	s.current, s.recipe = out, recipe
	return result, nil
}

// ExportSnapshot replays recipe against the immutable baseline and returns
// temporary artifacts for streaming export. The store's current descriptor,
// recipe, and preview are never changed.
func (s *editDiskStore) ExportSnapshot(ctx context.Context, recipe processing.DiskEditRecipe) (*editExportSnapshot, error) {
	if s == nil {
		return nil, fmt.Errorf("disk Edit store is closed")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" {
		return nil, fmt.Errorf("disk Edit store is closed")
	}
	root := s.root
	source, width, height, baseline := s.source, s.width, s.height, s.baseline
	dir, err := os.MkdirTemp(root, "export-")
	if err != nil {
		return nil, err
	}
	snapshot := &editExportSnapshot{width: width, height: height}
	for i := range snapshot.planes {
		snapshot.planes[i] = filepath.Join(dir, fmt.Sprintf("plane-%d.bin", i))
	}
	if _, err := processing.RenderDiskEdit(ctx, processing.DiskEditRenderRequest{
		Source: source, Output: snapshot.planes, Width: width, Height: height,
		Baseline: baseline, Recipe: recipe, PreviewMax: 1,
	}); err != nil {
		snapshot.cleanup()
		return nil, err
	}
	return snapshot, nil
}

func (s *editDiskStore) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current, s.recipe = s.source, identityDiskEditRecipe()
}

// Heal applies one ordered stroke and retains the prior descriptor for the
// single-level Heal undo. The source is never read from a partially written
// output, and failed jobs leave current and undo state untouched.
func (s *editDiskStore) Heal(ctx context.Context, stroke processing.DiskHealStroke, previewMax int) (processing.DiskEditRenderResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.root == "" {
		return processing.DiskEditRenderResult{}, fmt.Errorf("disk Edit store is closed")
	}
	s.generation++
	gen := s.generation
	var out [3]string
	for i := range out {
		out[i] = filepath.Join(s.root, fmt.Sprintf("heal-%d-%d.bin", gen, i))
	}
	if err := processing.HealDisk(ctx, s.current, out, s.width, s.height, stroke); err != nil {
		removeEditArtifacts(out)
		return processing.DiskEditRenderResult{}, err
	}
	// Keep the old descriptor and all metadata authoritative until the staged
	// planes have also produced a valid bounded preview.
	result, err := renderDiskStorePreview(ctx, out, s.width, s.height, previewMax)
	if err != nil {
		removeEditArtifacts(out)
		return processing.DiskEditRenderResult{}, err
	}
	old := s.current
	oldUndo := s.undo
	s.undo, s.undoWidth, s.undoHeight = old, s.width, s.height
	s.source, s.current, s.recipe = out, out, identityDiskEditRecipe()
	s.baseline = models.RgbLevels{Max: [3]float64{255, 255, 255}}
	for _, p := range oldUndo {
		if p != "" && p != old[0] && p != old[1] && p != old[2] {
			_ = os.Remove(p)
		}
	}
	return result, nil
}

func (s *editDiskStore) UndoHeal(ctx context.Context, previewMax int) (processing.DiskEditRenderResult, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.root == "" {
		return processing.DiskEditRenderResult{}, false, fmt.Errorf("disk Edit store is closed")
	}
	if s.undo[0] == "" {
		return processing.DiskEditRenderResult{}, false, nil
	}
	// Validate and render the undo descriptor while the current state remains
	// authoritative. Cancellation or preview failure must not consume undo.
	undo := s.undo
	result, err := renderDiskStorePreview(ctx, undo, s.undoWidth, s.undoHeight, previewMax)
	if err != nil {
		return processing.DiskEditRenderResult{}, false, err
	}
	old := s.current
	s.current, s.source = undo, undo
	s.width, s.height = s.undoWidth, s.undoHeight
	s.undo = [3]string{}
	s.recipe = identityDiskEditRecipe()
	s.baseline = models.RgbLevels{Max: [3]float64{255, 255, 255}}
	for _, p := range old {
		_ = os.Remove(p)
	}
	return result, true, nil
}

func (s *editDiskStore) Crop(ctx context.Context, rect image.Rectangle, previewMax int) (processing.DiskEditRenderResult, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.root == "" {
		return processing.DiskEditRenderResult{}, 0, 0, fmt.Errorf("disk Edit store is closed")
	}
	gen := s.generation + 1
	rect = rect.Intersect(image.Rect(0, 0, s.width, s.height))
	if rect.Empty() {
		return processing.DiskEditRenderResult{}, 0, 0, fmt.Errorf("empty disk crop")
	}
	var out [3]string
	for i := range out {
		out[i] = filepath.Join(s.root, fmt.Sprintf("crop-%d-%d.bin", gen, i))
	}
	w, h, err := processing.CropDisk(ctx, s.current, out, s.width, s.height, rect)
	if err != nil {
		removeEditArtifacts(out)
		return processing.DiskEditRenderResult{}, 0, 0, err
	}
	result, err := renderDiskStorePreview(ctx, out, w, h, previewMax)
	if err != nil {
		removeEditArtifacts(out)
		return processing.DiskEditRenderResult{}, 0, 0, err
	}
	old := s.current
	s.generation = gen
	s.source, s.current = out, out
	s.width, s.height = w, h
	s.recipe = identityDiskEditRecipe()
	s.baseline = models.RgbLevels{Max: [3]float64{255, 255, 255}}
	s.undo = [3]string{}
	for _, p := range old {
		_ = os.Remove(p)
	}
	return result, w, h, nil
}

func removeEditArtifacts(paths [3]string) {
	for _, p := range paths {
		if p != "" {
			_ = os.Remove(p)
		}
	}
}

func renderDiskStorePreview(ctx context.Context, paths [3]string, width, height, previewMax int) (processing.DiskEditRenderResult, error) {
	out := [3]string{filepath.Join(filepath.Dir(paths[0]), "preview-unused-0"), filepath.Join(filepath.Dir(paths[0]), "preview-unused-1"), filepath.Join(filepath.Dir(paths[0]), "preview-unused-2")}
	defer func() {
		for _, p := range out {
			_ = os.Remove(p)
		}
	}()
	return processing.RenderDiskEdit(ctx, processing.DiskEditRenderRequest{Source: paths, Output: out, Width: width, Height: height, Baseline: models.RgbLevels{Max: [3]float64{255, 255, 255}}, Recipe: identityDiskEditRecipe(), PreviewMax: previewMax})
}

func (s *editDiskStore) Clean(ctx context.Context, cfg processing.ColorSpeckCleanConfig, previewMax int) (processing.DiskColorSpeckCleanResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s == nil || s.root == "" {
		return processing.DiskColorSpeckCleanResult{}, fmt.Errorf("disk Edit store is closed")
	}
	s.generation++
	gen := s.generation
	var out [3]string
	var effective [3]string
	for i := range out {
		out[i] = filepath.Join(s.root, fmt.Sprintf("clean-%d-%d.bin", gen, i))
		effective[i] = filepath.Join(s.root, fmt.Sprintf("clean-effective-%d-%d.bin", gen, i))
	}
	// The current artifact can lag the controls while an Apply is being
	// scheduled. Replay the captured recipe against the immutable baseline first
	// so Clean always operates on the effective full-resolution image.
	if _, err := processing.RenderDiskEdit(ctx, processing.DiskEditRenderRequest{Source: s.source, Output: effective, Width: s.width, Height: s.height, Baseline: s.baseline, Recipe: s.recipe, PreviewMax: 1}); err != nil {
		return processing.DiskColorSpeckCleanResult{}, err
	}
	defer func() {
		for _, p := range effective {
			_ = os.Remove(p)
		}
	}()
	result, err := processing.CleanColorSpecksDisk(ctx, processing.DiskColorSpeckCleanRequest{Source: effective, Output: out, Width: s.width, Height: s.height, Config: cfg, PreviewMax: previewMax})
	if err != nil {
		return processing.DiskColorSpeckCleanResult{}, err
	}
	old := s.current
	s.source, s.current = out, out
	s.baseline = models.RgbLevels{Max: [3]float64{255, 255, 255}}
	s.recipe = identityDiskEditRecipe()
	for i := range old {
		if old[i] != out[i] && old[i] != s.source[i] {
			_ = os.Remove(old[i])
		}
	}
	return result, nil
}
func (s *editDiskStore) Source() ([3]string, int, int, processing.DiskEditRecipe) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current, s.width, s.height, s.recipe
}
func (s *editDiskStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" {
		return nil
	}
	root := s.root
	s.root = ""
	return os.RemoveAll(root)
}
