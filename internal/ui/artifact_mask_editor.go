package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

type artifactMaskEditorWindow struct {
	ws            *mosaicWorkspace
	win           fyne.Window
	ctrl          *artifactMaskEditorController
	preview       *canvas.Image
	layer         *artifactMaskLayer
	stack         *fyne.Container
	scroll        *container.Scroll
	status        *widget.Label
	previewStatus *widget.Label
	undoBtn       *widget.Button
	redoBtn       *widget.Button
	radius        *NumberEntry
	erase         *widget.Check
	tool          artifactMaskEditorTool
	zoom          float64
	polyInfo      *widget.Label
	targets       []artifactMaskExportTarget
}

const (
	artifactMaskPurposeLabelMIRIArtifact = "MIRI artifact exclusion"
	artifactMaskPurposeLabelRowDestripe  = "NIRCam row-stat exclusion"
)

func (ws *mosaicWorkspace) openArtifactMaskEditor() {
	options, indices := ws.artifactMaskInputOptions()
	if len(options) == 0 && ws.state.result == nil {
		dialog.ShowInformation("Artifact Masks", "Load at least one Mosaic input before creating masks.", ws.win)
		return
	}

	sourceOptions := []string{"Selected Input Frame"}
	if ws.state.result != nil {
		sourceOptions = append([]string{"Current Mosaic"}, sourceOptions...)
	}
	sourceSelect := NewSafeSelect(sourceOptions, nil)
	sourceSelect.SetSelected(sourceOptions[0])
	purposeSelect := NewSafeSelect([]string{artifactMaskPurposeLabelMIRIArtifact, artifactMaskPurposeLabelRowDestripe}, nil)
	purposeSelect.SetSelected(artifactMaskPurposeLabelMIRIArtifact)
	chooser := NewSafeSelect(options, nil)
	if len(options) > 0 {
		chooser.SetSelected(options[0])
	}
	dialog.NewCustomConfirm("Create Artifact Mask", "Open", "Cancel", container.NewVBox(
		widget.NewLabel("Choose the mask purpose and source. MIRI masks exclude pixels from drizzle; NIRCam row masks only exclude pixels from row median estimation."),
		widget.NewForm(widget.NewFormItem("Mask purpose", purposeSelect), widget.NewFormItem("Source", sourceSelect), widget.NewFormItem("Input frame", chooser)),
	), func(ok bool) {
		if !ok {
			return
		}
		if sourceSelect.Selected == "Current Mosaic" {
			if purposeSelect.Selected == artifactMaskPurposeLabelRowDestripe {
				dialog.ShowInformation("Artifact Masks", "NIRCam row-stat masks are edited on individual calibrated input frames so their dimensions match the detector image exactly.", ws.win)
				return
			}
			ws.openArtifactMaskEditorForMosaic()
			return
		}
		if len(indices) == 0 {
			dialog.ShowInformation("Artifact Masks", "No input frames are available to edit.", ws.win)
			return
		}
		sel := chooser.Selected
		idx := indices[0]
		for i, label := range options {
			if label == sel {
				idx = indices[i]
				break
			}
		}
		purpose := artifactMaskPurposeFromLabel(purposeSelect.Selected)
		ws.openArtifactMaskEditorForInput(idx, purpose)
	}, ws.win).Show()
}

func (ws *mosaicWorkspace) artifactMaskInputOptions() ([]string, []int) {
	var options []string
	var indices []int
	for i, input := range ws.state.inputs {
		if input.Excluded || input.ReferenceOnly {
			continue
		}
		options = append(options, mosaic.InputLabel(input))
		indices = append(indices, i)
	}
	return options, indices
}

func (ws *mosaicWorkspace) openArtifactMaskEditorForInput(idx int, purpose models.ArtifactMaskPurpose) {
	if idx < 0 || idx >= len(ws.state.inputs) {
		dialog.ShowError(fmt.Errorf("input index %d out of range", idx), ws.win)
		return
	}
	input := ws.state.inputs[idx]
	if purpose == models.ArtifactMaskPurposeRowDestripe && !isNIRCamPrimary(input) {
		dialog.ShowInformation("Artifact Masks", "NIRCam row-stat masks can only be exported for NIRCam inputs.", ws.win)
		return
	}
	if purpose == models.ArtifactMaskPurposeMIRIArtifact && !isMIRIPrimary(input) {
		dialog.ShowInformation("Artifact Masks", "MIRI artifact masks can only be exported for MIRI inputs.", ws.win)
		return
	}
	go func() {
		pt := newProgressTracker("Artifact Mask", "Loading selected input...", ws.win)
		err := ws.ensureInputPixelsLoadedAt(idx)
		pt.hide()
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, ws.win) })
			return
		}
		input := ws.state.inputs[idx]
		ctrl, err := newArtifactMaskEditorControllerForPurpose(idx, input, purpose, findArtifactMaskDocumentForPurpose(ws.state.artifactMasks, input, purpose))
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, ws.win) })
			return
		}
		black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
		preview := buildArtifactInputPreview(input, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
		fyne.Do(func() {
			editor := newArtifactMaskEditorWindow(ws, ctrl, preview)
			editor.show()
		})
	}()
}

func (ws *mosaicWorkspace) openArtifactMaskEditorForMosaic() {
	result := ws.state.result
	if result == nil {
		dialog.ShowInformation("Artifact Masks", "Build a Mosaic result before editing masks from the current mosaic.", ws.win)
		return
	}
	if result.Width <= 0 || result.Height <= 0 || result.Scale <= 0 || len(result.Pixels) != result.Width*result.Height {
		dialog.ShowInformation("Artifact Masks", "The current Mosaic result does not have usable geometry for mask projection.", ws.win)
		return
	}
	if _, ok := fitsio.HeaderFloat(result.OutputHeader, "CRPIX1"); !ok {
		dialog.ShowInformation("Artifact Masks", "The current Mosaic result does not have WCS metadata for projection.", ws.win)
		return
	}
	existing := findMosaicArtifactMaskDocument(ws.state.artifactMasks, result.Width, result.Height)
	if existing != nil && existing.Stale {
		dialog.ShowInformation("Artifact Masks", "The previous mosaic-authored mask was marked stale after the mosaic changed, so the editor will start from a fresh mask for this result.", ws.win)
	}
	ctrl, err := newMosaicArtifactMaskEditorController(result.Width, result.Height, result.Pixels, existing)
	if err != nil {
		dialog.ShowError(err, ws.win)
		return
	}
	black, white, bg, peak, scaledPeak := ws.parseLevelEntries()
	preview := buildMosaicPreviewImageWithLevels(result, black, white, bg, peak, scaledPeak, ws.stretchMode, ws.mtfMidtone)
	editor := newArtifactMaskEditorWindow(ws, ctrl, preview)
	editor.show()
}

func newArtifactMaskEditorWindow(ws *mosaicWorkspace, ctrl *artifactMaskEditorController, preview *image.RGBA) *artifactMaskEditorWindow {
	win := ws.app.NewWindow("Artifact Mask - " + ctrl.name)
	editor := &artifactMaskEditorWindow{
		ws:            ws,
		win:           win,
		ctrl:          ctrl,
		preview:       canvas.NewImageFromImage(preview),
		status:        widget.NewLabel(""),
		previewStatus: widget.NewLabel(""),
		radius:        NewNumberEntry(1, 0),
		erase:         widget.NewCheck("Erase", nil),
		tool:          artifactMaskToolBrush,
		zoom:          1,
	}
	editor.radius.SetValue(8)
	editor.preview.FillMode = canvas.ImageFillOriginal
	editor.layer = newArtifactMaskLayer(ctrl.width, ctrl.height, ctrl.mask)
	editor.layer.onBrush = func(points []mosaic.MaskPoint) {
		editor.applyEdit(func(erase bool) error {
			return editor.ctrl.applyBrush(points, editor.radius.Value(), erase)
		})
	}
	editor.layer.onRectangle = func(x0, y0, x1, y1 float64) {
		editor.applyEdit(func(erase bool) error {
			return editor.ctrl.applyRectangle(x0, y0, x1, y1, erase)
		})
	}
	editor.layer.onPolygonChanged = editor.updatePolygonStatus
	editor.layer.onPolygonCommit = func(points []mosaic.MaskPoint) {
		editor.applyEdit(func(erase bool) error {
			return editor.ctrl.applyPolygon(points, erase)
		})
	}
	editor.stack = container.NewWithoutLayout(editor.preview, editor.layer)
	editor.scroll = container.NewScroll(editor.stack)
	editor.scroll.SetMinSize(fyne.NewSize(720, 520))

	editor.undoBtn = widget.NewButton("Undo", func() {
		if editor.ctrl.undoLast() {
			editor.refreshMask()
		}
	})
	editor.redoBtn = widget.NewButton("Redo", func() {
		if editor.ctrl.redoLast() {
			editor.refreshMask()
		}
	})
	return editor
}

func (e *artifactMaskEditorWindow) show() {
	toolSelect := NewSafeSelect([]string{"Brush", "Rectangle", "Polygon"}, func(s string) {
		switch s {
		case "Rectangle":
			e.tool = artifactMaskToolRectangle
		case "Polygon":
			e.tool = artifactMaskToolPolygon
		default:
			e.tool = artifactMaskToolBrush
		}
		e.layer.SetTool(e.tool)
		e.updatePolygonStatus()
	})
	toolSelect.SetSelected("Brush")

	zoomSelect := NewSafeSelect([]string{"25%", "50%", "100%", "200%", "400%"}, func(s string) {
		e.setZoom(parseMaskZoom(s))
	})
	zoomSelect.SetSelected("100%")

	e.polyInfo = widget.NewLabel("")
	thresholdMin := widget.NewEntry()
	thresholdMin.SetPlaceHolder("min")
	thresholdMax := widget.NewEntry()
	thresholdMax.SetPlaceHolder("max")
	thresholdRect := widget.NewEntry()
	thresholdRect.SetText(fmt.Sprintf("0,0,%d,%d", e.ctrl.width-1, e.ctrl.height-1))
	thresholdSeed := widget.NewEntry()
	thresholdSeed.SetText(fmt.Sprintf("%d,%d", e.ctrl.width/2, e.ctrl.height/2))
	morphRadius := NewNumberEntry(1, 0)
	morphRadius.SetValue(2)

	previewThresholdBtn := widget.NewButton("Preview Threshold", func() {
		minVal, maxVal, err := parseFloat32Range(thresholdMin.Text, thresholdMax.Text)
		if err != nil {
			dialog.ShowInformation("Threshold", err.Error(), e.win)
			return
		}
		x0, y0, x1, y1, err := parseIntRect(thresholdRect.Text)
		if err != nil {
			dialog.ShowInformation("Threshold", err.Error(), e.win)
			return
		}
		sx, sy, err := parseIntPoint(thresholdSeed.Text)
		if err != nil {
			dialog.ShowInformation("Threshold", err.Error(), e.win)
			return
		}
		if err := e.ctrl.previewThreshold(minVal, maxVal, x0, y0, x1, y1, sx, sy); err != nil {
			dialog.ShowError(err, e.win)
			return
		}
		e.refreshMask()
	})
	acceptPreviewBtn := widget.NewButton("Accept Preview", func() {
		if err := e.ctrl.applyPreview(e.erase.Checked); err != nil {
			dialog.ShowInformation("Threshold", err.Error(), e.win)
			return
		}
		e.refreshMask()
	})
	rejectPreviewBtn := widget.NewButton("Reject Preview", func() {
		e.ctrl.clearPreview()
		e.refreshMask()
	})
	growBtn := widget.NewButton("Grow", func() {
		if err := e.ctrl.applyMorphology(true, int(math.Round(morphRadius.Value()))); err != nil {
			dialog.ShowError(err, e.win)
			return
		}
		e.refreshMask()
	})
	shrinkBtn := widget.NewButton("Shrink", func() {
		if err := e.ctrl.applyMorphology(false, int(math.Round(morphRadius.Value()))); err != nil {
			dialog.ShowError(err, e.win)
			return
		}
		e.refreshMask()
	})
	closePolyBtn := widget.NewButton("Close Polygon", func() {
		if err := e.layer.CommitPolygon(); err != nil {
			dialog.ShowInformation("Polygon", err.Error(), e.win)
		}
	})
	clearPolyBtn := widget.NewButton("Clear Polygon", func() {
		e.layer.ClearPolygon()
		e.updatePolygonStatus()
	})
	exportBtn := widget.NewButton("Export Mask", e.openExportReview)
	exportBtn.Importance = widget.HighImportance

	controls := container.NewVBox(
		widget.NewLabel("Source: "+e.ctrl.name),
		widget.NewForm(
			widget.NewFormItem("Tool", toolSelect),
			widget.NewFormItem("Brush radius", e.radius),
			widget.NewFormItem("Mode", e.erase),
			widget.NewFormItem("Zoom", zoomSelect),
		),
		container.NewHBox(e.undoBtn, e.redoBtn),
		container.NewHBox(closePolyBtn, clearPolyBtn),
		widget.NewSeparator(),
		widget.NewLabel("Assisted selection"),
		widget.NewForm(
			widget.NewFormItem("Intensity", container.NewGridWithColumns(2, thresholdMin, thresholdMax)),
			widget.NewFormItem("Rectangle", thresholdRect),
			widget.NewFormItem("Seed", thresholdSeed),
		),
		container.NewHBox(previewThresholdBtn, acceptPreviewBtn, rejectPreviewBtn),
		e.previewStatus,
		widget.NewSeparator(),
		widget.NewLabel("Grow / shrink"),
		widget.NewForm(widget.NewFormItem("Radius", morphRadius)),
		container.NewHBox(growBtn, shrinkBtn),
		e.polyInfo,
		e.status,
		exportBtn,
	)
	content := container.NewBorder(nil, nil, controls, nil, e.scroll)
	e.win.SetContent(content)
	e.win.Resize(fyne.NewSize(1040, 720))
	e.setZoom(1)
	e.refreshMask()
	e.win.Show()
}

func (e *artifactMaskEditorWindow) applyEdit(apply func(erase bool) error) {
	if err := apply(e.erase.Checked); err != nil {
		dialog.ShowError(err, e.win)
		return
	}
	e.refreshMask()
}

func (e *artifactMaskEditorWindow) refreshMask() {
	e.layer.SetMask(e.ctrl.mask)
	e.layer.SetPreviewMask(e.ctrl.preview)
	e.status.SetText(fmt.Sprintf("Masked pixels: %d / %d", e.ctrl.countMasked(), e.ctrl.width*e.ctrl.height))
	if e.ctrl.countPreview() > 0 {
		e.previewStatus.SetText(fmt.Sprintf("Preview pixels: %d", e.ctrl.countPreview()))
	} else {
		e.previewStatus.SetText("Preview pixels: 0")
	}
	if e.ctrl.canUndo() {
		e.undoBtn.Enable()
	} else {
		e.undoBtn.Disable()
	}
	if e.ctrl.canRedo() {
		e.redoBtn.Enable()
	} else {
		e.redoBtn.Disable()
	}
}

func (e *artifactMaskEditorWindow) setZoom(zoom float64) {
	if zoom <= 0 {
		zoom = 1
	}
	e.zoom = zoom
	w := float32(math.Round(float64(e.ctrl.width) * zoom))
	h := float32(math.Round(float64(e.ctrl.height) * zoom))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	size := fyne.NewSize(w, h)
	e.preview.SetMinSize(size)
	e.preview.Resize(size)
	e.layer.SetZoom(float32(zoom), size)
	e.stack.Resize(size)
	e.stack.Refresh()
}

func (e *artifactMaskEditorWindow) updatePolygonStatus() {
	if e.polyInfo == nil {
		return
	}
	if e.tool != artifactMaskToolPolygon {
		e.polyInfo.SetText("")
		return
	}
	e.polyInfo.SetText(fmt.Sprintf("Polygon points: %d", e.layer.PolygonPointCount()))
}

func (e *artifactMaskEditorWindow) openExportReview() {
	if e.ctrl.countMasked() == 0 {
		dialog.ShowInformation("Export Mask", "The mask is empty. Draw at least one excluded region before exporting.", e.win)
		return
	}
	defaultDir := e.defaultMaskDir()
	dirEntry := widget.NewEntry()
	dirEntry.SetText(defaultDir)
	dirEntry.SetPlaceHolder("mask output directory")
	overwrite := widget.NewCheck("Overwrite existing mask", nil)
	targets := e.buildExportTargets(false)
	targetBox := container.NewVBox()
	targetPreviewOptions := artifactMaskTargetLabels(targets)
	targetPreviewSelect := NewSafeSelect(targetPreviewOptions, nil)
	if len(targetPreviewOptions) > 0 {
		targetPreviewSelect.SetSelected(targetPreviewOptions[0])
	}
	for i := range targets {
		idx := i
		label := fmt.Sprintf("%s  %dx%d  masked=%d", targets[i].Label, targets[i].Input.HDU.Data.Width, targets[i].Input.HDU.Data.Height, mosaic.CountMaskPixels(targets[i].Mask))
		check := widget.NewCheck(label, func(v bool) {
			targets[idx].Selected = v
		})
		check.SetChecked(targets[i].Selected)
		targetBox.Add(check)
	}
	propagateLabel := "Offer propagation to other loaded MIRI frames"
	if e.ctrl.sourceMode == models.ArtifactMaskSourceMosaic {
		propagateLabel = "Project to overlapping MIRI frames"
	} else if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		propagateLabel = "Row-stat masks are per-input only"
	}
	propagate := widget.NewCheck(propagateLabel, func(v bool) {
		var err error
		targets, err = e.refreshExportTargets(v, targetBox)
		if err != nil {
			dialog.ShowError(err, e.win)
		}
		targetPreviewSelect.Options = artifactMaskTargetLabels(targets)
		if len(targetPreviewSelect.Options) > 0 {
			targetPreviewSelect.SetSelected(targetPreviewSelect.Options[0])
		}
		targetPreviewSelect.Refresh()
	})
	if e.ctrl.sourceMode == models.ArtifactMaskSourceMosaic {
		propagate.SetChecked(true)
		targets, _ = e.refreshExportTargets(true, targetBox)
		targetPreviewSelect.Options = artifactMaskTargetLabels(targets)
		if len(targetPreviewSelect.Options) > 0 {
			targetPreviewSelect.SetSelected(targetPreviewSelect.Options[0])
		}
	} else if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		propagate.Disable()
	}
	previewTargetBtn := widget.NewButton("Preview Target", func() {
		idx := artifactMaskTargetIndex(targets, targetPreviewSelect.Selected)
		if idx < 0 {
			dialog.ShowInformation("Preview Target", "Choose a target to preview.", e.win)
			return
		}
		e.openTargetPreview(targets[idx])
	})
	content := container.NewVBox(
		widget.NewLabel(e.exportReviewDescription()),
		widget.NewForm(widget.NewFormItem("Directory", dirEntry)),
		overwrite,
		propagate,
		container.NewHBox(targetPreviewSelect, previewTargetBtn),
		targetBox,
	)
	dialog.NewCustomConfirm(e.exportDialogTitle(), "Export", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		dir := strings.TrimSpace(dirEntry.Text)
		if dir == "" {
			dialog.ShowInformation("Export Mask", "Choose an output directory.", e.win)
			return
		}
		e.exportMasks(dir, overwrite.Checked, targets)
	}, e.win).Show()
}

func (e *artifactMaskEditorWindow) defaultMaskDir() string {
	if e.ws.currentProjectPath != "" {
		return "masks"
	}
	lastDir := e.ws.app.Preferences().String("lastDir")
	if strings.TrimSpace(lastDir) == "" {
		lastDir = "."
	}
	return filepath.Join(lastDir, "masks")
}

func (e *artifactMaskEditorWindow) exportDialogTitle() string {
	if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		return "Export NIRCam Row-Stat Mask"
	}
	return "Export MIRI Artifact Mask"
}

func (e *artifactMaskEditorWindow) exportReviewDescription() string {
	if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		return "Choose the per-input source mask to write. Nonzero pixels are excluded only from NIRCam row median estimation; science pixels are not directly deleted by this mask."
	}
	return "Choose target masks to write. Propagated MIRI targets use WCS projection from the edited frame and exclude selected pixels from sky matching, CR modeling, and drizzle."
}

func (e *artifactMaskEditorWindow) refreshExportTargets(propagate bool, targetBox *fyne.Container) ([]artifactMaskExportTarget, error) {
	targets := e.buildExportTargets(propagate)
	targetBox.RemoveAll()
	for i := range targets {
		idx := i
		label := fmt.Sprintf("%s  %dx%d  masked=%d", targets[i].Label, targets[i].Input.HDU.Data.Width, targets[i].Input.HDU.Data.Height, mosaic.CountMaskPixels(targets[i].Mask))
		check := widget.NewCheck(label, func(v bool) {
			targets[idx].Selected = v
		})
		check.SetChecked(targets[i].Selected)
		targetBox.Add(check)
	}
	targetBox.Refresh()
	return targets, nil
}

func (e *artifactMaskEditorWindow) buildExportTargets(propagate bool) []artifactMaskExportTarget {
	if e.ctrl.sourceMode == models.ArtifactMaskSourceMosaic {
		return e.buildMosaicExportTargets()
	}
	targets := []artifactMaskExportTarget{{
		Input:    e.ctrl.input,
		Index:    e.ctrl.inputIndex,
		Label:    mosaic.InputLabel(e.ctrl.input),
		Mask:     cloneBoolMask(e.ctrl.mask),
		Selected: true,
	}}
	if !propagate || e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		return targets
	}
	geom := mosaic.MaskOutputGeometry{Width: e.ctrl.width, Height: e.ctrl.height, OriginX: 0, OriginY: 0, Scale: 1}
	for i, input := range e.ws.state.inputs {
		if i == e.ctrl.inputIndex || input.Excluded || input.ReferenceOnly || !isMIRIPrimary(input) {
			continue
		}
		mask, err := mosaic.ProjectAuthoringMaskToDetector(input, e.ctrl.input, geom, e.ctrl.mask, mosaic.MaskProjectionOptions{ConservativeRadius: 0.5})
		if err != nil || mosaic.CountMaskPixels(mask) == 0 {
			targets = append(targets, artifactMaskExportTarget{
				Input:    input,
				Index:    i,
				Label:    mosaic.InputLabel(input) + " (no overlap)",
				Mask:     make([]bool, input.HDU.Data.Width*input.HDU.Data.Height),
				Selected: false,
				Error:    err,
			})
			continue
		}
		targets = append(targets, artifactMaskExportTarget{
			Input:    input,
			Index:    i,
			Label:    mosaic.InputLabel(input),
			Mask:     mask,
			Selected: false,
		})
	}
	return targets
}

func (e *artifactMaskEditorWindow) buildMosaicExportTargets() []artifactMaskExportTarget {
	result := e.ws.state.result
	if result == nil {
		return nil
	}
	targets := make([]artifactMaskExportTarget, 0, len(e.ws.state.inputs))
	ref, ok := e.mosaicProjectionReference()
	if !ok {
		return targets
	}
	geom := mosaic.MaskOutputGeometry{Width: result.Width, Height: result.Height, OriginX: result.OriginX, OriginY: result.OriginY, Scale: result.Scale}
	for i, input := range e.ws.state.inputs {
		if input.Excluded || input.ReferenceOnly || !isMIRIPrimary(input) {
			continue
		}
		mask, err := mosaic.ProjectAuthoringMaskToDetector(input, ref, geom, e.ctrl.mask, mosaic.MaskProjectionOptions{ConservativeRadius: 0.5})
		count := mosaic.CountMaskPixels(mask)
		label := mosaic.InputLabel(input)
		selected := true
		if err != nil || count == 0 {
			label += " (no overlap)"
			selected = false
			if mask == nil {
				mask = make([]bool, input.HDU.Data.Width*input.HDU.Data.Height)
			}
		}
		targets = append(targets, artifactMaskExportTarget{
			Input:    input,
			Index:    i,
			Label:    label,
			Mask:     mask,
			Selected: selected,
			Error:    err,
		})
	}
	return targets
}

func (e *artifactMaskEditorWindow) mosaicProjectionReference() (mosaic.Input, bool) {
	if e.ws.state.referenceInput != nil {
		ref := *e.ws.state.referenceInput
		ref.ReferenceOnly = true
		return ref, true
	}
	for _, input := range e.ws.state.inputs {
		if !input.Excluded && !input.ReferenceOnly {
			return input, true
		}
	}
	return mosaic.Input{}, false
}

func (e *artifactMaskEditorWindow) openTargetPreview(target artifactMaskExportTarget) {
	go func() {
		if target.Index >= 0 && target.Index < len(e.ws.state.inputs) && e.ws.state.inputs[target.Index].HDU.Data.Pixels == nil {
			pt := newProgressTracker("Preview Target", "Loading target input...", e.win)
			err := e.ws.ensureInputPixelsLoadedAt(target.Index)
			pt.hide()
			if err != nil {
				fyne.Do(func() { dialog.ShowError(err, e.win) })
				return
			}
			target.Input = e.ws.state.inputs[target.Index]
		}
		img := buildDetectorMaskPreview(target.Input, target.Mask)
		fyne.Do(func() {
			win := e.ws.app.NewWindow("Mask Preview - " + target.Label)
			preview := canvas.NewImageFromImage(img)
			preview.FillMode = canvas.ImageFillOriginal
			scroll := container.NewScroll(preview)
			scroll.SetMinSize(fyne.NewSize(640, 480))
			win.SetContent(scroll)
			win.Resize(fyne.NewSize(700, 540))
			win.Show()
		})
	}()
}

func (e *artifactMaskEditorWindow) exportMasks(dir string, overwrite bool, targets []artifactMaskExportTarget) {
	projectDirValue := encodeProjectRelativePath(e.ws.currentProjectPath, dir)
	resolvedDir := resolveProjectRelativePath(e.ws.currentProjectPath, projectDirValue)
	go func() {
		pt := newProgressTracker("Export Mask", "Writing mask FITS...", e.win)
		var written []string
		var err error
		selected := 0
		for _, target := range targets {
			if !target.Selected {
				continue
			}
			selected++
			if target.Error != nil {
				err = target.Error
				break
			}
			path, exportErr := e.exportTargetMask(target, resolvedDir, overwrite)
			if exportErr == nil {
				exportErr = validateExportedArtifactMask(path, target.Input.HDU.Data.Width, target.Input.HDU.Data.Height)
			}
			if exportErr != nil {
				err = exportErr
				break
			}
			written = append(written, path)
			pt.progress("Writing mask FITS...", selected, len(targets))
		}
		if selected == 0 && err == nil {
			err = fmt.Errorf("no target masks were selected")
		}
		pt.hide()
		if err != nil {
			fyne.Do(func() { dialog.ShowError(err, e.win) })
			return
		}
		fyne.Do(func() {
			doc := e.ctrl.document()
			doc.Targets = artifactMaskTargetsFromExportTargets(targets)
			doc.Stale = false
			e.ws.state.artifactMasks = upsertArtifactMaskDocument(e.ws.state.artifactMasks, doc)
			if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
				e.ws.state.skysubSettings.RowDestripe = true
				e.ws.state.skysubSettings.RowDestripeMaskDir = projectDirValue
			} else {
				e.ws.state.skysubSettings.MIRIArtifactMask = true
				e.ws.state.skysubSettings.MIRIArtifactMaskDir = projectDirValue
			}
			e.ws.state.skysubSettingsSet = true
			dialog.ShowInformation("Mask Exported", fmt.Sprintf("Saved %d mask file(s) and enabled the matching mask directory for the next build.", len(written)), e.win)
		})
	}()
}

func (e *artifactMaskEditorWindow) exportTargetMask(target artifactMaskExportTarget, dir string, overwrite bool) (string, error) {
	if e.ctrl.purpose == models.ArtifactMaskPurposeRowDestripe {
		return mosaic.ExportRowDestripeMaskAtomic(target.Input, dir, target.Mask, overwrite)
	}
	return mosaic.ExportMIRIArtifactMaskAtomic(target.Input, dir, target.Mask, overwrite)
}

func buildArtifactInputPreview(input mosaic.Input, black, white, background, peak, scaledPeak float64, mode stretch.Mode, mtfMidtone float64) *image.RGBA {
	img := &models.LoadedImage{
		HDU: fitsio.HDU{Data: fitsio.ImageData{
			Pixels: input.HDU.Data.Pixels,
			Width:  input.HDU.Data.Width,
			Height: input.HDU.Data.Height,
		}},
		Mode:       mode,
		Black:      black,
		White:      white,
		Background: background,
		Peak:       peak,
		ScaledPeak: scaledPeak,
		MTFMidtone: mtfMidtone,
	}
	stretched, mask := processing.ApplyStretchParallel(img)
	if mask == nil {
		mask = make([]byte, len(stretched.Pixels))
	}
	return processing.ToGrayRGBA(stretched, mask)
}

func validateExportedArtifactMask(path string, width, height int) error {
	file, err := fitsio.LoadFile(path)
	if err != nil {
		return err
	}
	if len(file.HDUs) == 0 {
		return fmt.Errorf("exported mask has no HDUs")
	}
	data := file.HDUs[0].Data
	if data.Width != width || data.Height != height {
		return fmt.Errorf("exported mask dimensions %dx%d do not match input %dx%d", data.Width, data.Height, width, height)
	}
	return nil
}

func artifactMaskTargetsFromExportTargets(targets []artifactMaskExportTarget) []models.ArtifactMaskTarget {
	out := make([]models.ArtifactMaskTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, models.ArtifactMaskTarget{
			Key:      artifactMaskTargetKey(target.Input),
			Path:     artifactMaskSourcePath(target.Input),
			SCIExt:   target.Input.SCIExt,
			Selected: target.Selected,
		})
	}
	return out
}

func parseMaskZoom(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	switch s {
	case "25":
		return 0.25
	case "50":
		return 0.5
	case "200":
		return 2
	case "400":
		return 4
	default:
		return 1
	}
}

type artifactMaskLayer struct {
	widget.BaseWidget
	width            int
	height           int
	mask             []bool
	previewMask      []bool
	zoom             float32
	displaySize      fyne.Size
	tool             artifactMaskEditorTool
	dragStart        *fyne.Position
	lastDrag         fyne.Position
	brushStroke      []mosaic.MaskPoint
	polygon          []mosaic.MaskPoint
	raster           *canvas.Raster
	onBrush          func([]mosaic.MaskPoint)
	onRectangle      func(x0, y0, x1, y1 float64)
	onPolygonChanged func()
	onPolygonCommit  func([]mosaic.MaskPoint)
}

func newArtifactMaskLayer(width, height int, mask []bool) *artifactMaskLayer {
	l := &artifactMaskLayer{width: width, height: height, mask: mask, zoom: 1}
	l.raster = canvas.NewRaster(l.drawOverlay)
	l.ExtendBaseWidget(l)
	return l
}

func (l *artifactMaskLayer) SetTool(tool artifactMaskEditorTool) {
	l.tool = tool
	l.dragStart = nil
	l.brushStroke = nil
	canvas.Refresh(l)
}

func (l *artifactMaskLayer) SetMask(mask []bool) {
	l.mask = mask
	l.raster.Refresh()
	canvas.Refresh(l)
}

func (l *artifactMaskLayer) SetPreviewMask(mask []bool) {
	l.previewMask = mask
	l.raster.Refresh()
	canvas.Refresh(l)
}

func (l *artifactMaskLayer) SetZoom(zoom float32, size fyne.Size) {
	if zoom <= 0 {
		zoom = 1
	}
	l.zoom = zoom
	l.displaySize = size
	l.Resize(size)
	l.raster.Resize(size)
	l.Refresh()
}

func (l *artifactMaskLayer) PolygonPointCount() int { return len(l.polygon) }

func (l *artifactMaskLayer) ClearPolygon() {
	l.polygon = nil
	if l.onPolygonChanged != nil {
		l.onPolygonChanged()
	}
}

func (l *artifactMaskLayer) CommitPolygon() error {
	if len(l.polygon) < 3 {
		return fmt.Errorf("a polygon needs at least three points")
	}
	points := append([]mosaic.MaskPoint(nil), l.polygon...)
	l.polygon = nil
	if l.onPolygonChanged != nil {
		l.onPolygonChanged()
	}
	if l.onPolygonCommit != nil {
		l.onPolygonCommit(points)
	}
	return nil
}

func (l *artifactMaskLayer) Tapped(e *fyne.PointEvent) {
	pt := l.imagePoint(e.Position)
	switch l.tool {
	case artifactMaskToolPolygon:
		l.polygon = append(l.polygon, pt)
		if l.onPolygonChanged != nil {
			l.onPolygonChanged()
		}
	case artifactMaskToolRectangle:
		if l.onRectangle != nil {
			l.onRectangle(pt.X, pt.Y, pt.X, pt.Y)
		}
	default:
		if l.onBrush != nil {
			l.onBrush([]mosaic.MaskPoint{pt})
		}
	}
}

func (l *artifactMaskLayer) TappedSecondary(*fyne.PointEvent) {
	l.ClearPolygon()
}

func (l *artifactMaskLayer) Dragged(e *fyne.DragEvent) {
	pt := l.imagePoint(e.Position)
	if l.dragStart == nil {
		pos := e.Position
		l.dragStart = &pos
		l.brushStroke = nil
	}
	l.lastDrag = e.Position
	if l.tool == artifactMaskToolBrush {
		l.brushStroke = append(l.brushStroke, pt)
	}
}

func (l *artifactMaskLayer) DragEnd() {
	if l.dragStart == nil {
		return
	}
	start := l.imagePoint(*l.dragStart)
	end := l.imagePoint(l.lastDrag)
	switch l.tool {
	case artifactMaskToolRectangle:
		if l.onRectangle != nil {
			l.onRectangle(start.X, start.Y, end.X, end.Y)
		}
	case artifactMaskToolBrush:
		if len(l.brushStroke) == 0 {
			l.brushStroke = []mosaic.MaskPoint{start, end}
		}
		if l.onBrush != nil {
			l.onBrush(append([]mosaic.MaskPoint(nil), l.brushStroke...))
		}
	}
	l.dragStart = nil
	l.brushStroke = nil
}

func (l *artifactMaskLayer) imagePoint(pos fyne.Position) mosaic.MaskPoint {
	z := float64(l.zoom)
	if z <= 0 {
		z = 1
	}
	x := clampFloat(float64(pos.X)/z, 0, float64(l.width-1))
	y := clampFloat(float64(pos.Y)/z, 0, float64(l.height-1))
	return mosaic.MaskPoint{X: x, Y: y}
}

func (l *artifactMaskLayer) drawOverlay(w, h int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if l.width <= 0 || l.height <= 0 || len(l.mask) != l.width*l.height {
		return img
	}
	red := color.NRGBA{R: 255, G: 0, B: 0, A: 110}
	yellow := color.NRGBA{R: 255, G: 220, B: 0, A: 140}
	for y := 0; y < h; y++ {
		srcY := int(float64(y) / float64(l.zoom))
		if srcY < 0 || srcY >= l.height {
			continue
		}
		for x := 0; x < w; x++ {
			srcX := int(float64(x) / float64(l.zoom))
			if srcX < 0 || srcX >= l.width {
				continue
			}
			idx := srcY*l.width + srcX
			if len(l.previewMask) == len(l.mask) && l.previewMask[idx] {
				img.SetNRGBA(x, y, yellow)
			} else if l.mask[idx] {
				img.SetNRGBA(x, y, red)
			}
		}
	}
	return img
}

type artifactMaskExportTarget struct {
	Input    mosaic.Input
	Index    int
	Label    string
	Mask     []bool
	Selected bool
	Error    error
}

func artifactMaskTargetLabels(targets []artifactMaskExportTarget) []string {
	labels := make([]string, len(targets))
	for i, target := range targets {
		labels[i] = target.Label
	}
	return labels
}

func artifactMaskTargetIndex(targets []artifactMaskExportTarget, label string) int {
	for i, target := range targets {
		if target.Label == label {
			return i
		}
	}
	return -1
}

func buildDetectorMaskPreview(input mosaic.Input, mask []bool) *image.RGBA {
	w := input.HDU.Data.Width
	h := input.HDU.Data.Height
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	pixels := input.HDU.Data.Pixels
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := y*w + x
			base := uint8(30)
			if idx < len(pixels) && mosaicFinite32(pixels[idx]) {
				v := pixels[idx]
				if v < 0 {
					v = 0
				}
				if v > 1 {
					v = 1
				}
				base = uint8(v * 180)
			}
			c := color.RGBA{R: base, G: base, B: base, A: 255}
			if idx < len(mask) && mask[idx] {
				c = color.RGBA{R: 255, G: base / 3, B: base / 3, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func mosaicFinite32(v float32) bool {
	return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
}

func parseFloat32Range(minText, maxText string) (float32, float32, error) {
	minVal, err := strconv.ParseFloat(strings.TrimSpace(minText), 32)
	if err != nil {
		return 0, 0, fmt.Errorf("minimum intensity must be a number")
	}
	maxVal, err := strconv.ParseFloat(strings.TrimSpace(maxText), 32)
	if err != nil {
		return 0, 0, fmt.Errorf("maximum intensity must be a number")
	}
	return float32(minVal), float32(maxVal), nil
}

func parseIntRect(text string) (int, int, int, int, error) {
	parts := strings.Split(text, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("rectangle must be x0,y0,x1,y1")
	}
	values := make([]int, 4)
	for i, part := range parts {
		v, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return 0, 0, 0, 0, fmt.Errorf("rectangle value %d must be an integer", i+1)
		}
		values[i] = v
	}
	return values[0], values[1], values[2], values[3], nil
}

func parseIntPoint(text string) (int, int, error) {
	parts := strings.Split(text, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("seed must be x,y")
	}
	x, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("seed x must be an integer")
	}
	y, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("seed y must be an integer")
	}
	return x, y, nil
}

func isMIRIPrimary(input mosaic.Input) bool {
	return strings.EqualFold(strings.Trim(fitsio.HeaderString(input.PrimaryHeader, "INSTRUME"), "' "), "MIRI")
}

func isNIRCamPrimary(input mosaic.Input) bool {
	return strings.EqualFold(strings.Trim(fitsio.HeaderString(input.PrimaryHeader, "INSTRUME"), "' "), "NIRCAM")
}

func artifactMaskPurposeFromLabel(label string) models.ArtifactMaskPurpose {
	if label == artifactMaskPurposeLabelRowDestripe {
		return models.ArtifactMaskPurposeRowDestripe
	}
	return models.ArtifactMaskPurposeMIRIArtifact
}

func (l *artifactMaskLayer) CreateRenderer() fyne.WidgetRenderer {
	l.raster.Resize(l.Size())
	return &artifactMaskLayerRenderer{layer: l, objects: []fyne.CanvasObject{l.raster}}
}

func (l *artifactMaskLayer) MinSize() fyne.Size {
	if l.displaySize.Width > 0 && l.displaySize.Height > 0 {
		return l.displaySize
	}
	return fyne.NewSize(float32(l.width), float32(l.height))
}

type artifactMaskLayerRenderer struct {
	layer   *artifactMaskLayer
	objects []fyne.CanvasObject
}

func (r *artifactMaskLayerRenderer) Layout(size fyne.Size) { r.layer.raster.Resize(size) }
func (r *artifactMaskLayerRenderer) MinSize() fyne.Size    { return r.layer.MinSize() }
func (r *artifactMaskLayerRenderer) Refresh()              { r.layer.raster.Refresh() }
func (r *artifactMaskLayerRenderer) Destroy()              {}
func (r *artifactMaskLayerRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
