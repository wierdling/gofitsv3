package ui

import (
	"context"
	"errors"
	"fmt"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComposeLargeProjectStageRollbackDecision(t *testing.T) {
	oldImg := &models.LoadedImage{Path: "old.fits"}
	current := []*models.LoadedImage{{Path: "incoming.fits"}}
	previous := []*models.LoadedImage{oldImg}
	artifacts := map[int]composeArtifactDescriptor{0: {Path: "incoming"}}
	previews := map[int]*image.RGBA{}
	previousArtifacts := map[int]composeArtifactDescriptor{0: {Path: "old"}}
	previousPreviews := map[int]*image.RGBA{}
	previousOverlays := []*overlayLayer{{name: "old-layer"}}
	var overlays []*overlayLayer
	orig := new([][]float32)
	restoreComposeLargeProjectSnapshot(&current, orig, &overlays, &artifacts, &previews, previous, nil, previousOverlays, previousArtifacts, previousPreviews)
	if current[0] != oldImg || artifacts[0].Path != "old" || len(overlays) != 1 || overlays[0].name != "old-layer" {
		t.Fatal("rollback did not restore prior image/artifact state")
	}
}

func TestComposeLargeInstallAllowedRejectsFinalCancellation(t *testing.T) {
	if !composeLargeInstallAllowed(context.Background()) {
		t.Fatal("active context rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if composeLargeInstallAllowed(ctx) {
		t.Fatal("canceled final install accepted")
	}
}

func TestComposeLargeSendCommitAllowedRejectsQueuedClear(t *testing.T) {
	expected := composeArtifactDescriptor{Path: "stage.bin", Generation: 4}
	if !composeLargeSendCommitAllowed(2, 2, 7, 7, true, expected, expected) {
		t.Fatal("matching generation rejected")
	}
	if composeLargeSendCommitAllowed(2, 3, 7, 7, true, expected, expected) {
		t.Fatal("session supersession accepted")
	}
	if composeLargeSendCommitAllowed(2, 2, 7, 8, true, expected, expected) {
		t.Fatal("clear invalidation accepted")
	}
	current := expected
	current.Generation++
	if composeLargeSendCommitAllowed(2, 2, 7, 7, true, current, expected) {
		t.Fatal("descriptor replacement accepted")
	}
}

func TestComposeLargeAlignmentReferenceGuardAndOffsetReset(t *testing.T) {
	expected := composeArtifactDescriptor{Path: "green.bin", Generation: 9}
	if !composeLargeAlignmentReferenceCurrent(expected, expected) {
		t.Fatal("matching reference descriptor rejected")
	}
	for _, current := range []composeArtifactDescriptor{
		{Path: "green-new.bin", Generation: 9},
		{Path: "green.bin", Generation: 10},
		{},
	} {
		if composeLargeAlignmentReferenceCurrent(current, expected) {
			t.Fatalf("stale reference descriptor accepted: %+v", current)
		}
	}
	control := &models.ChannelControl{
		XOffsetEntry:   NewNumberEntry(1, 2),
		YOffsetEntry:   NewNumberEntry(1, 2),
		RotOffsetEntry: NewNumberEntry(1, 2),
	}
	control.XOffsetEntry.SetValue(4)
	control.YOffsetEntry.SetValue(-3)
	control.RotOffsetEntry.SetValue(12)
	resetComposeAlignmentOffsets(control)
	if control.XOffsetEntry.Value() != 0 || control.YOffsetEntry.Value() != 0 || control.RotOffsetEntry.Value() != 0 {
		t.Fatalf("alignment offsets not reset: %v %v %v", control.XOffsetEntry.Value(), control.YOffsetEntry.Value(), control.RotOffsetEntry.Value())
	}
}

func TestComposeLargeStoreLifecycleAndContainment(t *testing.T) {
	work := t.TempDir()
	s, err := newComposeLargeStore(work)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s.root, filepath.Join(work, "working", "tmp")+string(filepath.Separator)) {
		t.Fatalf("session root %q is outside working/tmp", s.root)
	}
	if err := s.Put("channel-0.bin", strings.NewReader("pixels")); err != nil {
		t.Fatal(err)
	}
	f, err := s.Open("channel-0.bin")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	for _, name := range []string{"../escape", filepath.Join(s.root, "escape")} {
		if _, err := s.Create(name); err == nil {
			t.Errorf("Create(%q) accepted path escape", name)
		}
	}
	root := s.root
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("session root still exists after Close: %v", err)
	}
	if _, err := s.Open("channel-0.bin"); err == nil {
		t.Fatal("closed store allowed Open")
	}
}

func TestComposeLargePreviewIsCappedAndReadsArtifactRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pixels.bin")
	a, err := fitsio.CreateFloat32Artifact(path, 4000, 2)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 4000)
	row[0], row[3999] = 0, 10
	if err := a.WriteRow(0, row); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteRow(1, row); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()
	img, _, _, err := composeLargePreview(path)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() > 1600 || img.Bounds().Dy() > 1600 {
		t.Fatalf("preview bounds = %v", img.Bounds())
	}
	if img.At(0, 0) == (color.RGBA{}) {
		t.Fatal("preview pixel unexpectedly empty")
	}
}

func TestComposeLargeCleanupHookClosesSession(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := s.root
	globalComposeLargeCleanup = func() {
		_ = s.Close()
	}
	globalComposeLargeCleanup()
	globalComposeLargeCleanup = nil
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cleanup hook left session root: %v", err)
	}
}

func TestComposeLargeStoreReplaceSlotIsTransactional(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.Replace("channel-0", 2, 1, func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{1, 2}) })
	if err != nil {
		t.Fatal(err)
	}
	if d.Generation != 1 || d.Width != 2 || d.Height != 1 {
		t.Fatalf("descriptor=%+v", d)
	}
	d2, err := s.Replace("channel-0", 2, 1, func(a *fitsio.Float32Artifact) error { return errors.New("injected") })
	if err == nil || d2.Path != "" {
		t.Fatalf("failed replace returned descriptor=%+v err=%v", d2, err)
	}
	got, ok := s.Descriptor("channel-0")
	if !ok || got.Generation != 1 {
		t.Fatalf("old descriptor lost: %+v %v", got, ok)
	}
	a, err := fitsio.OpenFloat32ArtifactReadOnly(got.Path)
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 2)
	_ = a.ReadRow(0, row)
	_ = a.Close()
	if row[1] != 2 {
		t.Fatalf("old artifact changed: %v", row)
	}
	if err := s.RemoveSlot("channel-0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Descriptor("channel-0"); ok {
		t.Fatal("descriptor remained after remove")
	}
}

func TestComposeLargeStoreReplaceManySizedPreservesOnPrepareFailure(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err := s.Replace("channel-0", 2, 1, func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{3, 4}) })
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ReplaceManyIfCurrentSized([]composeArtifactDescriptor{old}, [][2]int{{1, 2}}, []func(*fitsio.Float32Artifact) error{
		func(a *fitsio.Float32Artifact) error {
			if err := a.WriteRow(0, []float32{8}); err != nil {
				return err
			}
			return a.WriteRow(1, []float32{9})
		},
	}, func(staged []composeArtifactDescriptor) error {
		if len(staged) != 1 || staged[0].Width != 1 || staged[0].Height != 2 {
			t.Fatalf("staged dimensions = %+v", staged)
		}
		return errors.New("injected preview failure")
	})
	if err == nil {
		t.Fatal("prepare failure unexpectedly committed")
	}
	current, ok := s.Descriptor("channel-0")
	if !ok || current.Path != old.Path || current.Generation != old.Generation || current.Width != 2 || current.Height != 1 {
		t.Fatalf("old descriptor not preserved: %+v", current)
	}
	r, err := fitsio.OpenFloat32ArtifactReadOnly(current.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	row := make([]float32, 2)
	if err := r.ReadRow(0, row); err != nil {
		t.Fatal(err)
	}
	if row[0] != 3 || row[1] != 4 {
		t.Fatalf("old pixels changed: %v", row)
	}
}

func TestComposeLargeStoreReplaceManySizedRollsBackEarlierCommit(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Replace("channel-0", 1, 1, func(a *fitsio.Float32Artifact) error {
		return a.WriteRow(0, []float32{1})
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Replace("channel-1", 1, 1, func(a *fitsio.Float32Artifact) error {
		return a.WriteRow(0, []float32{2})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force the second fresh-generation destination to fail its rename after
	// the first destination has already committed.
	blocked := filepath.Join(s.root, "channel-1-2.bin")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "keep"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := blocked + ".replace-backup"
	if err := os.Mkdir(backup, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "keep"), []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = s.ReplaceManyIfCurrentSized(
		[]composeArtifactDescriptor{first, second},
		[][2]int{{1, 1}, {1, 1}},
		[]func(*fitsio.Float32Artifact) error{
			func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{8}) },
			func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{9}) },
		}, nil,
	)
	if err == nil {
		t.Fatal("later commit unexpectedly succeeded")
	}
	cur0, ok := s.Descriptor("channel-0")
	if !ok || cur0.Path != first.Path || cur0.Generation != first.Generation {
		t.Fatalf("first slot changed after rollback: %+v", cur0)
	}
	cur1, ok := s.Descriptor("channel-1")
	if !ok || cur1.Path != second.Path || cur1.Generation != second.Generation {
		t.Fatalf("second slot changed after rollback: %+v", cur1)
	}
	if _, statErr := os.Stat(filepath.Join(s.root, "channel-0-2.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("committed first destination remains: %v", statErr)
	}
}

func TestComposeLargeStoreRemoveSlotIfCurrentKeepsNewerGeneration(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first, err := s.Replace("channel", 1, 1, func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{1}) })
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Replace("channel", 1, 1, func(a *fitsio.Float32Artifact) error { return a.WriteRow(0, []float32{2}) })
	if err != nil {
		t.Fatal(err)
	}
	removed, err := s.RemoveSlotIfCurrent(first)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("stale descriptor removed newer slot")
	}
	cur, ok := s.Descriptor("channel")
	if !ok || cur.Generation != second.Generation {
		t.Fatalf("current descriptor=%+v", cur)
	}
	removed, err = s.RemoveSlotIfCurrent(second)
	if err != nil || !removed {
		t.Fatalf("current removal: removed=%v err=%v", removed, err)
	}
}

func TestComposeLargeStorePublishesCompositeAsOneGeneration(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var paths [3]string
	for i := range paths {
		paths[i] = filepath.Join(s.root, fmt.Sprintf("staging-%d.bin", i))
		a, e := fitsio.CreateFloat32Artifact(paths[i], 2, 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.WriteRow(0, []float32{float32(i), 1}); e != nil {
			t.Fatal(e)
		}
		if e = a.Close(); e != nil {
			t.Fatal(e)
		}
	}
	d, err := s.PublishComposite(paths, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.Generation != 1 || d.Planes[0].Path == paths[0] {
		t.Fatalf("unexpected descriptor: %+v", d)
	}
	if _, ok := s.Composite(); !ok {
		t.Fatal("composite not current")
	}
	for _, p := range paths {
		if _, e := os.Stat(p); !os.IsNotExist(e) {
			t.Fatalf("staging path remains: %s (%v)", p, e)
		}
	}
	// A failed publication leaves the existing generation untouched.
	bad := [3]string{filepath.Join(s.root, "missing"), paths[1], paths[2]}
	if _, err = s.PublishComposite(bad, 2, 1); err == nil {
		t.Fatal("missing staging artifact accepted")
	}
	cur, ok := s.Composite()
	if !ok || cur.Generation != d.Generation {
		t.Fatalf("current composite changed after failed publish: %+v %v", cur, ok)
	}
}

func TestSnapshotCompositeForEditCopiesOwnedPlanes(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var paths [3]string
	for i := range paths {
		paths[i] = filepath.Join(s.root, fmt.Sprintf("src-%d.bin", i))
		a, e := fitsio.CreateFloat32Artifact(paths[i], 2, 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.WriteRow(0, []float32{float32(i + 1), 2}); e != nil {
			t.Fatal(e)
		}
		_ = a.Close()
	}
	d, err := s.PublishComposite(paths, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	ed, err := s.SnapshotCompositeForEdit(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	defer ed.cleanup()
	if ed.root == s.root || ed.width != 2 || ed.height != 1 {
		t.Fatalf("bad edit source: %+v", ed)
	}
	a, err := fitsio.OpenFloat32ArtifactReadOnly(ed.planes[0])
	if err != nil {
		t.Fatal(err)
	}
	row := make([]float32, 2)
	_ = a.ReadRow(0, row)
	_ = a.Close()
	if row[0] != 1 {
		t.Fatalf("copied row=%v", row)
	}
	if _, err := os.Stat(ed.root); err != nil {
		t.Fatal(err)
	}
	ed.cleanup()
	if _, err := os.Stat(ed.root); !os.IsNotExist(err) {
		t.Fatalf("cleanup left root: %v", err)
	}
}

func TestSnapshotCompositeForEditRejectsMalformedPlaneDimensions(t *testing.T) {
	s, err := newComposeLargeStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var paths [3]string
	for i := range paths {
		paths[i] = filepath.Join(s.root, fmt.Sprintf("src-%d.bin", i))
		a, e := fitsio.CreateFloat32Artifact(paths[i], 2, 1)
		if e != nil {
			t.Fatal(e)
		}
		if e = a.WriteRow(0, []float32{1, 2}); e != nil {
			t.Fatal(e)
		}
		if e = a.Close(); e != nil {
			t.Fatal(e)
		}
	}
	d, err := s.PublishComposite(paths, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a malformed descriptor pointing at a valid artifact with the
	// wrong dimensions; SnapshotCompositeForEdit must reject it transactionally.
	d.Planes[1].Width = 3
	if _, err := s.SnapshotCompositeForEdit(context.Background(), d); err == nil {
		t.Fatal("malformed plane dimensions accepted")
	}
	entries, err := os.ReadDir(filepath.Dir(s.root))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "edit-") {
			t.Fatalf("staging root leaked after malformed plane: %s", entry.Name())
		}
	}
}
