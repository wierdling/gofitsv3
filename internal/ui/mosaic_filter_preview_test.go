package ui

import (
	"image/color"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"

	"gofitsv3/internal/mosaic"
)

func TestMosaicFilterPreviewLabelsAreDeterministicallyDeconflicted(t *testing.T) {
	anchor := fyne.NewPos(95, 75)
	labelSize := fyne.NewSize(20, 18)
	var placed []fyne.Position
	for i := 0; i < 10; i++ {
		pos := deconflictedPreviewLabelPositionBounded(anchor, placed, 200, 160, labelSize)
		for _, prior := range placed {
			dx, dy := pos.X-prior.X, pos.Y-prior.Y
			if dx*dx+dy*dy < previewLabelMinDistance*previewLabelMinDistance {
				t.Fatalf("label %d is too close to prior label: %v and %v", i, pos, prior)
			}
			if pos == prior {
				t.Fatalf("label %d overlaps prior label at %v", i, pos)
			}
		}
		placed = append(placed, pos)
	}
	if got, want := deconflictedPreviewLabelPositionBounded(anchor, nil, 200, 160, labelSize), placed[0]; got != want {
		t.Fatalf("label placement is not deterministic: got %v, want %v", got, want)
	}
	bounded := deconflictedPreviewLabelPositionBounded(fyne.NewPos(-10, 500), placed, 100, 80, labelSize)
	if bounded.X < 0 || bounded.Y < 0 || bounded.X+labelSize.Width > 100 || bounded.Y+labelSize.Height > 80 {
		t.Fatalf("bounded label position = %v, outside preview", bounded)
	}
}

func TestMosaicFilterPreviewLabelsRemainAnchorRelative(t *testing.T) {
	anchor := fyne.NewPos(100, 80)
	labelSize := fyne.NewSize(20, 18)
	first := deconflictedPreviewLabelPositionBounded(anchor, nil, 240, 180, labelSize)
	if dx, dy := first.X-anchor.X, first.Y-anchor.Y; dx*dx+dy*dy > previewLabelMinDistance*previewLabelMinDistance {
		t.Fatalf("first label moved too far from anchor: got %v, anchor %v", first, anchor)
	}
	placed := []fyne.Position{first}
	for i := 0; i < 5; i++ {
		placed = append(placed, deconflictedPreviewLabelPositionBounded(anchor, placed, 240, 180, labelSize))
	}
	for i, position := range placed {
		if position.X < 12 || position.Y < 12 || position.X > 216 || position.Y > 160 {
			t.Fatalf("label %d outside preview: %v", i, position)
		}
		for j := 0; j < i; j++ {
			dx, dy := position.X-placed[j].X, position.Y-placed[j].Y
			if dx*dx+dy*dy < previewLabelMinDistance*previewLabelMinDistance {
				t.Fatalf("labels %d and %d are too close: %v and %v", j, i, placed[j], position)
			}
		}
	}
}

func TestMosaicFilterPreviewKeepsMultiDigitLabelsInsideCanvas(t *testing.T) {
	for _, text := range []string{"10", "100"} {
		label := canvas.NewText(text, color.White)
		label.TextSize = previewLabelTextSize
		textSize := label.MinSize()
		occupiedSize := fyne.NewSize(textSize.Width+1, textSize.Height+1)
		for _, anchor := range []fyne.Position{{X: -100, Y: -100}, {X: 1000, Y: -100}, {X: -100, Y: 1000}, {X: 1000, Y: 1000}} {
			position := deconflictedPreviewLabelPositionBounded(anchor, nil, 240, 180, occupiedSize)
			if position.X < 0 || position.Y < 0 || position.X+occupiedSize.Width > 240 || position.Y+occupiedSize.Height > 180 {
				t.Fatalf("label %q at %v with occupied size %v escapes 240x180 canvas", text, position, occupiedSize)
			}
		}
	}
}

func TestFootprintPreviewCenterAveragesAllCorners(t *testing.T) {
	fp := [4][2]float64{{10, 20}, {50, 20}, {50, 80}, {10, 80}}
	if got, want := footprintPreviewCenter(fp), [2]float64{30, 50}; got != want {
		t.Fatalf("footprintPreviewCenter() = %v, want %v", got, want)
	}
	anchor := footprintPreviewLabelAnchor(fp, 2, 12, fyne.NewSize(20, 18))
	if want := fyne.NewPos(62, 103); anchor != want {
		t.Fatalf("footprintPreviewLabelAnchor() = %v, want %v", anchor, want)
	}
}

func TestMosaicFilterPreviewUsesLargerLabelsAndSpacing(t *testing.T) {
	if previewLabelTextSize <= 14 {
		t.Fatalf("preview label text size = %d, want larger than 14", previewLabelTextSize)
	}
	if previewLabelMinDistance <= 16 {
		t.Fatalf("preview label spacing = %v, want larger than 16", previewLabelMinDistance)
	}
}

func TestMosaicFilterPreviewReservesCompactRowHeight(t *testing.T) {
	preview := newMosaicFilterPreview()
	if got := preview.root.MinSize(); got.Width < 300 || got.Height < 200 {
		t.Fatalf("preview minimum size = %v, want at least 300x200", got)
	}
}

func TestSnapshotCheckedPathsPreservesOrderAndExcludesUnchecked(t *testing.T) {
	checks := []mosaicFilterBatchFileCheck{
		{path: "one.fits", checked: false},
		{path: "two.fits", checked: true},
		{path: "three.fits", checked: true},
	}
	got := snapshotCheckedPaths(checks)
	want := []mosaicFilterBatchPreviewRequest{{path: "two.fits", sourceNumber: 2}, {path: "three.fits", sourceNumber: 3}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("snapshotCheckedPaths() = %v, want %v", got, want)
	}
}

func TestFilterPreviewRefreshRequestsCoalescesBulkChanges(t *testing.T) {
	if got := filterPreviewRefreshRequests(true, true, 8); got != 1 {
		t.Fatalf("bulk refresh requests = %d, want 1", got)
	}
	if got := filterPreviewRefreshRequests(true, false, 1); got != 1 {
		t.Fatalf("individual refresh requests = %d, want 1", got)
	}
	if got := filterPreviewRefreshRequests(false, false, 8); got != 0 {
		t.Fatalf("closed-preview refresh requests = %d, want 0", got)
	}
}

func TestMosaicFilterPreviewRedrawsCachedPlanAfterResize(t *testing.T) {
	preview := newMosaicFilterPreview()
	groups := []mosaic.FootprintPreview{{SourcePath: "one.fits", SourceNumber: 1, Footprints: [][4][2]float64{{
		{0, 0}, {10, 0}, {10, 10}, {0, 10},
	}}}}
	preview.show(groups, 10, 10)
	preview.root.Resize(fyne.NewSize(800, 600))
	preview.renderCached()
	if preview.renderedSize != fyne.NewSize(800, 600) {
		t.Fatalf("rendered size = %v, want 800x600", preview.renderedSize)
	}
	if !preview.hasCachedPlan || len(preview.cachedGroups) != 1 {
		t.Fatal("resize redraw lost cached footprint plan")
	}
}

func TestMosaicFilterPreviewShowsWCSWarning(t *testing.T) {
	groups := []mosaic.FootprintPreview{{SourcePath: "bad.fits", SourceNumber: 1, Error: "missing WCS"}}
	if got := mosaicFilterPreviewWarning(groups); got == "" {
		t.Fatal("mosaicFilterPreviewWarning() returned empty text for invalid source")
	}
	preview := newMosaicFilterPreview()
	preview.show(groups, 0, 0)
	if len(preview.root.Objects) != 1 {
		t.Fatalf("warning view objects = %d, want one", len(preview.root.Objects))
	}
}
