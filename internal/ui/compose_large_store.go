package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
)

func restoreComposeLargeProjectSnapshot(imgs *[]*models.LoadedImage, orig *[][]float32, overlays *[]*overlayLayer, artifacts *map[int]composeArtifactDescriptor, previews *map[int]*image.RGBA, calibration *models.ColorCalibrationState, saveCalibration *bool, previousImgs []*models.LoadedImage, previousOrig [][]float32, previousOverlays []*overlayLayer, previousArtifacts map[int]composeArtifactDescriptor, previousPreviews map[int]*image.RGBA, previousCalibration models.ColorCalibrationState, previousSaveCalibration bool) {
	*imgs = previousImgs
	*orig = previousOrig
	*overlays = previousOverlays
	*artifacts = previousArtifacts
	*previews = previousPreviews
	*calibration = previousCalibration
	*saveCalibration = previousSaveCalibration
}
func composeLargeInstallAllowed(ctx context.Context) bool {
	return ctx == nil || ctx.Err() == nil
}

// composeLargeStore owns the temporary artifacts for one disk-backed Compose
// session.  The session directory is deliberately unique and is removed when
// Close is called.
type composeLargeStore struct {
	root                string
	closed              bool
	mu                  sync.Mutex
	stateMu             sync.RWMutex
	artifacts           map[string]composeArtifactDescriptor
	composite           *composeCompositeDescriptor
	compositeGeneration uint64
}

type composeArtifactDescriptor struct {
	Path          string
	Width, Height int
	Generation    uint64
	Slot          string
}

// composeCompositeDescriptor owns the three immutable planes from one render.
type composeCompositeDescriptor struct {
	Planes        [3]composeArtifactDescriptor
	Width, Height int
	Generation    uint64
}

// SnapshotCompositeForEdit copies a published composite into an Edit-owned
// working/tmp directory while holding the store lifetime read lock.
func (s *composeLargeStore) SnapshotCompositeForEdit(ctx context.Context, d composeCompositeDescriptor) (*editDiskSource, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.composite == nil || s.composite.Generation != d.Generation || s.composite.Width != d.Width || s.composite.Height != d.Height {
		return nil, errors.New("stale Compose composite generation")
	}
	root, err := os.MkdirTemp(filepath.Dir(s.root), "edit-")
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	out := &editDiskSource{width: d.Width, height: d.Height, root: root}
	for i, src := range d.Planes {
		if err := ctx.Err(); err != nil {
			cleanup()
			return nil, err
		}
		in, err := fitsio.OpenFloat32ArtifactReadOnly(src.Path)
		if err != nil {
			cleanup()
			return nil, err
		}
		name := filepath.Join(root, fmt.Sprintf("plane-%d.bin", i))
		a, err := fitsio.CreateFloat32Artifact(name, d.Width, d.Height)
		if err != nil {
			_ = in.Close()
			cleanup()
			return nil, err
		}
		row := make([]float32, d.Width)
		for y := 0; y < d.Height; y++ {
			if err := ctx.Err(); err != nil {
				_ = a.Close()
				_ = in.Close()
				cleanup()
				return nil, err
			}
			if err := in.ReadRow(y, row); err != nil {
				_ = a.Close()
				_ = in.Close()
				cleanup()
				return nil, err
			}
			if err := a.WriteRow(y, row); err != nil {
				_ = a.Close()
				_ = in.Close()
				cleanup()
				return nil, err
			}
		}
		if err := a.Close(); err != nil {
			_ = in.Close()
			cleanup()
			return nil, err
		}
		_ = in.Close()
		out.planes[i] = name
	}
	return out, nil
}

// composeLargeSendCommitAllowed centralizes the queued Send-to-Channel commit
// guard so stale UI completions can be tested without constructing Fyne state.
func composeLargeSendCommitAllowed(session, currentSession, request, currentRequest uint64, currentOK bool, current, expected composeArtifactDescriptor) bool {
	return session == currentSession && request == currentRequest && currentOK && current.Path == expected.Path && current.Generation == expected.Generation
}

func newComposeLargeStore(workDir string) (*composeLargeStore, error) {
	if strings.TrimSpace(workDir) == "" {
		var err error
		workDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	base := filepath.Join(workDir, "working", "tmp")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, fmt.Errorf("create Compose temporary root: %w", err)
	}
	root, err := os.MkdirTemp(base, "compose-")
	if err != nil {
		return nil, fmt.Errorf("create Compose temporary session: %w", err)
	}
	return &composeLargeStore{root: root, artifacts: make(map[string]composeArtifactDescriptor)}, nil
}

func (s *composeLargeStore) path(name string) (string, error) {
	if s == nil || s.closed {
		return "", errors.New("Compose temporary session is closed")
	}
	name = filepath.Clean(name)
	if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid Compose temporary artifact name")
	}
	p := filepath.Join(s.root, name)
	rel, err := filepath.Rel(s.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("Compose temporary artifact escapes session")
	}
	return p, nil
}

func (s *composeLargeStore) Create(name string) (*os.File, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
}

func (s *composeLargeStore) Put(name string, r io.Reader) error {
	f, err := s.Create(name)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

// PutFITS streams one selected image HDU into a raw float32 artifact.
// The temporary destination is committed atomically by the fitsio writer.
func (s *composeLargeStore) PutFITS(name, srcPath, hduName, extver string) error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return fitsio.CopySelectedHDUToRawFloat32Artifact(srcPath, hduName, extver, p)
}

func (s *composeLargeStore) Descriptor(slot string) (composeArtifactDescriptor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.artifacts[slot]
	return d, ok
}

func (s *composeLargeStore) Composite() (composeCompositeDescriptor, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.composite == nil {
		return composeCompositeDescriptor{}, false
	}
	return *s.composite, true
}

// PublishComposite adopts three completed staging artifacts as one generation.
func (s *composeLargeStore) PublishComposite(paths [3]string, width, height int) (composeCompositeDescriptor, error) {
	return s.publishComposite(paths, width, height, ^uint64(0))
}

// PublishCompositeIfCurrent rejects a stale render when another render has
// already published since the caller captured expectedGeneration. Pass zero
// when no prior composite exists.
func (s *composeLargeStore) PublishCompositeIfCurrent(expectedGeneration uint64, paths [3]string, width, height int) (composeCompositeDescriptor, error) {
	return s.publishComposite(paths, width, height, expectedGeneration)
}

func (s *composeLargeStore) publishComposite(paths [3]string, width, height int, expectedGeneration uint64) (composeCompositeDescriptor, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return composeCompositeDescriptor{}, errors.New("Compose temporary session is closed")
	}
	currentGeneration := uint64(0)
	if s.composite != nil {
		currentGeneration = s.composite.Generation
	}
	if expectedGeneration != ^uint64(0) && expectedGeneration != currentGeneration {
		return composeCompositeDescriptor{}, errors.New("stale Compose composite generation")
	}
	for _, p := range paths {
		clean := filepath.Clean(p)
		if filepath.Dir(clean) != s.root {
			return composeCompositeDescriptor{}, errors.New("composite staging path escapes session")
		}
		if _, err := os.Stat(clean); err != nil {
			return composeCompositeDescriptor{}, err
		}
	}
	gen := s.compositeGeneration + 1
	finals := [3]string{filepath.Join(s.root, fmt.Sprintf("composite-r-%d.bin", gen)), filepath.Join(s.root, fmt.Sprintf("composite-g-%d.bin", gen)), filepath.Join(s.root, fmt.Sprintf("composite-b-%d.bin", gen))}
	moved := 0
	for i := range paths {
		if err := os.Rename(paths[i], finals[i]); err != nil {
			for j := 0; j < moved; j++ {
				_ = os.Rename(finals[j], paths[j])
			}
			return composeCompositeDescriptor{}, err
		}
		moved++
	}
	d := composeCompositeDescriptor{Width: width, Height: height, Generation: gen}
	for i := range d.Planes {
		d.Planes[i] = composeArtifactDescriptor{Path: finals[i], Width: width, Height: height, Generation: gen, Slot: fmt.Sprintf("composite-%d", i)}
	}
	old := s.composite
	s.compositeGeneration = gen
	s.composite = &d
	if old != nil {
		for _, p := range old.Planes {
			_ = os.Remove(p.Path)
		}
	}
	return d, nil
}

func (s *composeLargeStore) RemoveCompositeIfCurrent(d composeCompositeDescriptor) (bool, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("Compose temporary session is closed")
	}
	if s.composite == nil || s.composite.Generation != d.Generation {
		return false, nil
	}
	for _, p := range s.composite.Planes {
		_ = os.Remove(p.Path)
	}
	s.composite = nil
	return true, nil
}

// Replace stages a new artifact under a sibling temporary name and swaps the
// runtime slot only after the transaction has committed successfully.
func (s *composeLargeStore) Replace(slot string, width, height int, write func(*fitsio.Float32Artifact) error) (composeArtifactDescriptor, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return composeArtifactDescriptor{}, errors.New("Compose temporary session is closed")
	}
	return s.replaceLocked(slot, width, height, write, nil)
}

// ReplaceIfCurrent commits only when slot still refers to expected. It is used
// by background transforms so a reload cannot be overwritten by a stale job.
func (s *composeLargeStore) ReplaceIfCurrent(expected composeArtifactDescriptor, width, height int, write func(*fitsio.Float32Artifact) error) (composeArtifactDescriptor, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return composeArtifactDescriptor{}, errors.New("Compose temporary session is closed")
	}
	cur, ok := s.artifacts[expected.Slot]
	if !ok || cur.Generation != expected.Generation || cur.Path != expected.Path {
		return composeArtifactDescriptor{}, errors.New("stale Compose artifact generation")
	}
	return s.replaceLocked(expected.Slot, width, height, write, &expected)
}

// ReplaceManyIfCurrent stages every replacement before publishing any slot.
// It is used by cross-channel operations whose outputs must commit all-or-none.
func (s *composeLargeStore) ReplaceManyIfCurrent(expected []composeArtifactDescriptor, writes []func(*fitsio.Float32Artifact) error) ([]composeArtifactDescriptor, error) {
	return s.replaceManyIfCurrent(expected, writes, nil)
}

// ReplaceManyIfCurrentPrepared stages replacements and invokes prepare while
// all temporary artifacts still exist. A prepare failure aborts every staged
// output, so callers can validate previews before any slot is published.
func (s *composeLargeStore) ReplaceManyIfCurrentPrepared(expected []composeArtifactDescriptor, writes []func(*fitsio.Float32Artifact) error, prepare func([]composeArtifactDescriptor) error) ([]composeArtifactDescriptor, error) {
	return s.replaceManyIfCurrent(expected, writes, prepare)
}

// ReplaceManyIfCurrentArtifacts stages all outputs and exposes their bounded
// artifact handles to one coordinator, allowing cross-channel jobs to read
// all sources and write all destinations before any slot is published.
func (s *composeLargeStore) ReplaceManyIfCurrentArtifacts(expected []composeArtifactDescriptor, run func([3]*fitsio.Float32Artifact) error) ([]composeArtifactDescriptor, error) {
	return s.replaceManyIfCurrentArtifacts(expected, run, nil)
}

// ReplaceManyIfCurrentArtifactsPrepared is the transactional variant used by
// multi-channel jobs that must validate bounded previews before publishing any
// replacement. The prepare callback runs while staged artifacts are complete
// but before their transactions are committed or slots are swapped.
func (s *composeLargeStore) ReplaceManyIfCurrentArtifactsPrepared(expected []composeArtifactDescriptor, run func([3]*fitsio.Float32Artifact) error, prepare func([]composeArtifactDescriptor) error) ([]composeArtifactDescriptor, error) {
	return s.replaceManyIfCurrentArtifacts(expected, run, prepare)
}

func (s *composeLargeStore) replaceManyIfCurrentArtifacts(expected []composeArtifactDescriptor, run func([3]*fitsio.Float32Artifact) error, prepare func([]composeArtifactDescriptor) error) ([]composeArtifactDescriptor, error) {
	if len(expected) != 3 {
		return nil, errors.New("replacement count mismatch")
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Compose temporary session is closed")
	}
	txs := make([]*fitsio.Float32ArtifactTransaction, 3)
	desc := make([]composeArtifactDescriptor, 3)
	abort := func() {
		for _, tx := range txs {
			if tx != nil {
				_ = tx.Abort()
			}
		}
	}
	for i, old := range expected {
		cur, ok := s.artifacts[old.Slot]
		if !ok || cur.Path != old.Path || cur.Generation != old.Generation {
			abort()
			return nil, errors.New("stale Compose artifact generation")
		}
		p, err := s.path(fmt.Sprintf("%s-%d.bin", old.Slot, old.Generation+1))
		if err != nil {
			abort()
			return nil, err
		}
		tx, err := fitsio.BeginFloat32ArtifactTransaction(p, old.Width, old.Height)
		if err != nil {
			abort()
			return nil, err
		}
		txs[i] = tx
		desc[i] = composeArtifactDescriptor{Path: p, Width: old.Width, Height: old.Height, Generation: old.Generation + 1, Slot: old.Slot}
	}
	var arts [3]*fitsio.Float32Artifact
	for i := range arts {
		arts[i] = txs[i].Artifact()
	}
	if err := run(arts); err != nil {
		abort()
		return nil, err
	}
	if prepare != nil {
		for _, tx := range txs {
			if err := tx.Artifact().Sync(); err != nil {
				abort()
				return nil, err
			}
		}
		staged := append([]composeArtifactDescriptor(nil), desc...)
		for i := range staged {
			staged[i].Path = txs[i].StagedPath()
		}
		if err := prepare(staged); err != nil {
			abort()
			return nil, err
		}
	}
	for _, tx := range txs {
		if err := tx.Commit(); err != nil {
			abort()
			return nil, err
		}
	}
	for _, d := range desc {
		old := s.artifacts[d.Slot]
		s.artifacts[d.Slot] = d
		if old.Path != "" {
			_ = os.Remove(old.Path)
		}
	}
	return desc, nil
}

func (s *composeLargeStore) replaceManyIfCurrent(expected []composeArtifactDescriptor, writes []func(*fitsio.Float32Artifact) error, prepare func([]composeArtifactDescriptor) error) ([]composeArtifactDescriptor, error) {
	if len(expected) != len(writes) {
		return nil, errors.New("replacement count mismatch")
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Compose temporary session is closed")
	}
	type staged struct {
		d  composeArtifactDescriptor
		tx *fitsio.Float32ArtifactTransaction
	}
	stagedTx := make([]staged, 0, len(expected))
	abort := func() {
		for _, x := range stagedTx {
			_ = x.tx.Abort()
		}
	}
	for i, old := range expected {
		cur, ok := s.artifacts[old.Slot]
		if !ok || cur.Path != old.Path || cur.Generation != old.Generation {
			abort()
			return nil, errors.New("stale Compose artifact generation")
		}
		p, err := s.path(fmt.Sprintf("%s-%d.bin", old.Slot, old.Generation+1))
		if err != nil {
			abort()
			return nil, err
		}
		tx, err := fitsio.BeginFloat32ArtifactTransaction(p, old.Width, old.Height)
		if err != nil {
			abort()
			return nil, err
		}
		if err = writes[i](tx.Artifact()); err != nil {
			_ = tx.Abort()
			abort()
			return nil, err
		}
		stagedTx = append(stagedTx, staged{composeArtifactDescriptor{Path: p, Width: old.Width, Height: old.Height, Generation: old.Generation + 1, Slot: old.Slot}, tx})
	}
	if prepare != nil {
		desc := make([]composeArtifactDescriptor, len(stagedTx))
		for i := range stagedTx {
			desc[i] = stagedTx[i].d
			if err := stagedTx[i].tx.Artifact().Sync(); err != nil {
				abort()
				return nil, err
			}
			desc[i].Path = stagedTx[i].tx.StagedPath()
		}
		if err := prepare(desc); err != nil {
			abort()
			return nil, err
		}
	}
	for _, x := range stagedTx {
		if err := x.tx.Commit(); err != nil {
			abort()
			return nil, err
		}
	}
	for _, x := range stagedTx {
		old := s.artifacts[x.d.Slot]
		s.artifacts[x.d.Slot] = x.d
		if old.Path != "" {
			_ = os.Remove(old.Path)
		}
	}
	out := make([]composeArtifactDescriptor, len(stagedTx))
	for i := range stagedTx {
		out[i] = stagedTx[i].d
	}
	return out, nil
}

func (s *composeLargeStore) replaceLocked(slot string, width, height int, write func(*fitsio.Float32Artifact) error, expected *composeArtifactDescriptor) (composeArtifactDescriptor, error) {
	old := s.artifacts[slot]
	if expected != nil && (old.Generation != expected.Generation || old.Path != expected.Path) {
		return composeArtifactDescriptor{}, errors.New("stale Compose artifact generation")
	}
	name := fmt.Sprintf("%s-%d.bin", slot, old.Generation+1)
	p, err := s.path(name)
	if err != nil {
		return composeArtifactDescriptor{}, err
	}
	tx, err := fitsio.BeginFloat32ArtifactTransaction(p, width, height)
	if err != nil {
		return composeArtifactDescriptor{}, err
	}
	if err = write(tx.Artifact()); err != nil {
		_ = tx.Abort()
		return composeArtifactDescriptor{}, err
	}
	if err = tx.Commit(); err != nil {
		return composeArtifactDescriptor{}, err
	}
	d := composeArtifactDescriptor{Path: p, Width: width, Height: height, Generation: old.Generation + 1, Slot: slot}
	s.artifacts[slot] = d
	if old.Path != "" && old.Path != p {
		_ = os.Remove(old.Path)
	}
	return d, nil
}

func (s *composeLargeStore) ReplaceFromFITS(slot, srcPath, hduName, extver string) (composeArtifactDescriptor, error) {
	primary, hdr, err := fitsio.InspectSelectedHDU(srcPath, hduName, extver)
	_ = primary
	if err != nil {
		return composeArtifactDescriptor{}, err
	}
	return s.Replace(slot, hdr.Data.Width, hdr.Data.Height, func(a *fitsio.Float32Artifact) error {
		// Copy through a sibling path then into the transaction's bounded rows.
		tmpPath := filepath.Join(s.root, fmt.Sprintf("%s-source.tmp", slot))
		defer os.Remove(tmpPath)
		if err = fitsio.CopySelectedHDUToRawFloat32Artifact(srcPath, hduName, extver, tmpPath); err != nil {
			return err
		}
		r, err := fitsio.OpenFloat32ArtifactReadOnly(tmpPath)
		if err != nil {
			return err
		}
		defer r.Close()
		row := make([]float32, r.Width)
		for y := 0; y < r.Height; y++ {
			if err := r.ReadRow(y, row); err != nil {
				return err
			}
			if err := a.WriteRow(y, row); err != nil {
				return err
			}
		}
		return nil
	})
}

// RotateArtifact90CW rewrites one artifact transactionally and returns the
// replacement descriptor. It uses one source row plus one destination row;
// callers may repeat it for quarter-turn project state restoration.
func (s *composeLargeStore) RotateArtifact90CW(d composeArtifactDescriptor) (composeArtifactDescriptor, error) {
	r, err := fitsio.OpenFloat32ArtifactReadOnly(d.Path)
	if err != nil {
		return composeArtifactDescriptor{}, err
	}
	w, h := r.Width, r.Height
	nd, err := s.ReplaceIfCurrent(d, h, w, func(out *fitsio.Float32Artifact) error {
		srcRow := make([]float32, w)
		dstRow := make([]float32, h)
		for y := 0; y < w; y++ {
			for x := 0; x < h; x++ {
				if err := r.ReadRow(h-1-x, srcRow); err != nil {
					return err
				}
				dstRow[x] = srcRow[y]
			}
			if err := out.WriteRow(y, dstRow); err != nil {
				return err
			}
		}
		return nil
	})
	_ = r.Close()
	return nd, err
}

// ResizeArtifact rewrites an artifact to the requested dimensions using the
// existing in-memory resampler, while keeping only the source plane for this
// single channel alive during the transaction. The slot is swapped atomically
// and stale generations are rejected.
func (s *composeLargeStore) ResizeArtifact(d composeArtifactDescriptor, width, height int) (composeArtifactDescriptor, error) {
	src, err := fitsio.OpenFloat32ArtifactReadOnly(d.Path)
	if err != nil {
		return composeArtifactDescriptor{}, err
	}
	defer src.Close()
	return s.ReplaceIfCurrent(d, width, height, func(out *fitsio.Float32Artifact) error {
		r0, r1, row := make([]float32, src.Width), make([]float32, src.Width), make([]float32, width)
		for y := 0; y < height; y++ {
			sy := float64(y) * float64(src.Height-1) / float64(maxInt(height-1, 1))
			y0 := int(sy)
			y1 := y0
			if y0+1 < src.Height {
				y1 = y0 + 1
			}
			fy := float32(sy - float64(y0))
			if err := src.ReadRow(y0, r0); err != nil {
				return err
			}
			if y1 != y0 {
				if err := src.ReadRow(y1, r1); err != nil {
					return err
				}
			} else {
				copy(r1, r0)
			}
			for x := 0; x < width; x++ {
				sx := float64(x) * float64(src.Width-1) / float64(maxInt(width-1, 1))
				x0 := int(sx)
				x1 := x0
				if x0+1 < src.Width {
					x1 = x0 + 1
				}
				fx := float32(sx - float64(x0))
				a := r0[x0]*(1-fx) + r0[x1]*fx
				b := r1[x0]*(1-fx) + r1[x1]*fx
				row[x] = a*(1-fy) + b*fy
			}
			if err := out.WriteRow(y, row); err != nil {
				return err
			}
		}
		return nil
	})
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *composeLargeStore) RemoveSlot(slot string) error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.artifacts[slot]
	if !ok {
		return nil
	}
	delete(s.artifacts, slot)
	if d.Path != "" {
		if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// RemoveSlotIfCurrent removes a slot only when it still refers to d. This
// prevents stale cleanup from deleting a newer generation published by a
// concurrent job.
func (s *composeLargeStore) RemoveSlotIfCurrent(d composeArtifactDescriptor) (bool, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, errors.New("Compose temporary session is closed")
	}
	cur, ok := s.artifacts[d.Slot]
	if !ok || cur.Generation != d.Generation || cur.Path != d.Path {
		return false, nil
	}
	delete(s.artifacts, d.Slot)
	if d.Path == "" {
		return true, nil
	}
	if err := os.Remove(d.Path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	return true, nil
}

// composeLargePreview reads an artifact row-by-row and creates a bounded
// grayscale preview. At most one source row and 1600x1600 RGBA pixels are
// resident; the full raster is never materialized.
func composeLargePreview(path string) (*image.RGBA, float32, float32, error) {
	a, err := fitsio.OpenFloat32Artifact(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer a.Close()
	maxEdge := 1600
	s := 1
	if a.Width > maxEdge || a.Height > maxEdge {
		if a.Width > a.Height {
			s = (a.Width + maxEdge - 1) / maxEdge
		} else {
			s = (a.Height + maxEdge - 1) / maxEdge
		}
	}
	w, h := (a.Width+s-1)/s, (a.Height+s-1)/s
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	row := make([]float32, a.Width)
	min, max := float32(0), float32(0)
	first := true
	for y := 0; y < a.Height; y += s {
		if err := a.ReadRow(y, row); err != nil {
			return nil, 0, 0, err
		}
		for x := 0; x < a.Width; x += s {
			v := row[x]
			if !isFiniteLarge(v) {
				continue
			}
			if first || v < min {
				min = v
			}
			if first || v > max {
				max = v
			}
			first = false
		}
	}
	if first {
		min, max = 0, 1
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	for y := 0; y < a.Height; y += s {
		if err := a.ReadRow(y, row); err != nil {
			return nil, 0, 0, err
		}
		for x := 0; x < a.Width; x += s {
			v := row[x]
			if !isFiniteLarge(v) {
				v = min
			}
			q := uint8(clampLarge(float64((v-min)/span)*255, 0, 255))
			out.SetRGBA(x/s, y/s, color.RGBA{q, q, q, 255})
		}
	}
	return out, min, max, nil
}

func isFiniteLarge(v float32) bool { return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0) }
func clampLarge(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s *composeLargeStore) Open(name string) (*os.File, error) {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	p, err := s.path(name)
	if err != nil {
		return nil, err
	}
	return os.Open(p)
}

func (s *composeLargeStore) Remove(name string) error {
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	p, err := s.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (s *composeLargeStore) Close() error {
	if s == nil {
		return nil
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.artifacts = nil
	return os.RemoveAll(s.root)
}
