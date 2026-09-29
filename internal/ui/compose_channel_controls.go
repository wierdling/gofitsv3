package ui

import (
	"errors"
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func channelControls(label string, col color.Color, idx int, imgs []*models.LoadedImage, origPixels *[][]float32, views []*viewport, refresh func(), magicPreset *widget.Select, allowRotate bool, large ...*largeChannelRuntime) *models.ChannelControl {
	invalidateChannelRefinement := func() {
	}
	var disk *largeChannelRuntime
	if len(large) > 0 {
		disk = large[0]
	}
	largeJob := func(op string, mutate func(*models.LoadedImage) error) {
		if disk == nil || disk.store == nil || !composeLargeModeActive() || idx < 0 || idx >= len(imgs) {
			return
		}
		disk.mu.RLock()
		d, ok := disk.artifacts[idx]
		identity := (*models.LoadedImage)(nil)
		var snapshot models.LoadedImage
		if ok && idx < len(imgs) && imgs[idx] != nil {
			identity = imgs[idx]
			snapshot = snapshotLargeLoadedImage(identity)
		} else {
			ok = false
		}
		disk.mu.RUnlock()
		if !ok {
			return
		}
		go func() {
			disk.jobMu.Lock()
			defer disk.jobMu.Unlock()
			lease, err := fitsio.MaterializeFloat32ArtifactLease(d.Path)
			if err == nil {
				img := snapshot
				img.HDU.Data.Pixels = lease.Pixels
				err = mutate(&img)
				lease.Release()
				if err == nil {
					preview, _, _, pErr := composeLargeStretchedPreview(d.Path, &img)
					err = pErr
					if err == nil {
						disk.mu.Lock()
						cur, current := disk.artifacts[idx]
						storeCur, storeCurrent := disk.store.Descriptor(d.Slot)
						if current && storeCurrent && imgs[idx] == identity && cur.Generation == d.Generation && cur.Path == d.Path && storeCur.Generation == d.Generation && storeCur.Path == d.Path {
							*imgs[idx] = img
							imgs[idx].HDU.Data.Pixels = nil
							disk.previews[idx] = preview
						} else {
							err = errors.New("stale Compose artifact generation")
						}
						disk.mu.Unlock()
					}
				}
			}
			fyne.Do(func() {
				if err != nil {
					debuglog.Log(fmt.Sprintf("large channel %s: %v", op, err))
				} else if idx < len(views) && views[idx] != nil {
					views[idx].blackBox.SetValue(imgs[idx].Black)
					views[idx].whiteBox.SetValue(imgs[idx].White)
				}
				if err == nil && disk.syncWidgets != nil {
					if syncFn := disk.syncWidgets[idx]; syncFn != nil {
						syncFn(imgs[idx])
					}
				}
				refresh()
			})
		}()
	}
	// Stretch-specific parameter rows. Only the row(s) relevant to the selected
	// mode are shown; the rest stay hidden to avoid clutter.
	asinhScaleEntry := NewNumberEntry(0.1, 3)
	mtfMidtoneEntry := NewNumberEntry(0.01, 3)
	ghsStretchEntry := NewNumberEntry(0.1, 2)
	ghsLocalEntry := NewNumberEntry(0.1, 2)
	ghsSymmetryEntry := NewNumberEntry(0.05, 3)

	asinhScaleEntry.SetValue(stretch.DefaultAsinhScale)
	mtfMidtoneEntry.SetValue(stretch.DefaultMTFMidtone)
	ghsStretchEntry.SetValue(stretch.DefaultGHSStretch)
	ghsLocalEntry.SetValue(stretch.DefaultGHSLocal)
	ghsSymmetryEntry.SetValue(stretch.DefaultGHSSymmetry)

	paramRow := func(label string, entry models.NumberField) *fyne.Container {
		return container.NewBorder(nil, nil, widget.NewLabel(label), nil, entry)
	}
	asinhRow := paramRow("Asinh softening", asinhScaleEntry)
	mtfRow := paramRow("MTF midtone", mtfMidtoneEntry)
	ghsDRow := paramRow("GHS strength D", ghsStretchEntry)
	ghsBRow := paramRow("GHS local b", ghsLocalEntry)
	ghsSPRow := paramRow("GHS symmetry SP", ghsSymmetryEntry)

	updateStretchParams := func(mode stretch.Mode) {
		asinhRow.Hide()
		mtfRow.Hide()
		ghsDRow.Hide()
		ghsBRow.Hide()
		ghsSPRow.Hide()
		switch mode {
		case stretch.Asinh:
			asinhRow.Show()
		case stretch.MTF:
			mtfRow.Show()
		case stretch.GHS:
			ghsDRow.Show()
			ghsBRow.Show()
			ghsSPRow.Show()
		}
	}

	var syncingWidgets bool
	selectBox := widget.NewSelect([]string{"Linear", "Log", "Asinh", "Sqrt", "HistEq", "MTF", "GHS"}, func(value string) {
		if syncingWidgets {
			updateStretchParams(labelToMode(value))
			return
		}
		if imgs[idx] == nil {
			updateStretchParams(labelToMode(value))
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			mode := labelToMode(value)
			updateStretchParams(mode)
			largeJob("mode", func(img *models.LoadedImage) error { img.Mode = mode; return nil })
			return
		}
		imgs[idx].Mode = labelToMode(value)
		updateStretchParams(imgs[idx].Mode)
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("mode", func(_ *models.LoadedImage) error { return nil })
			return
		}
		refresh()
	})
	initialMode := stretch.Linear
	if idx >= 0 && idx < len(imgs) && imgs[idx] != nil {
		initialMode = imgs[idx].Mode
	}
	selectBox.SetSelected(modeToLabel(initialMode))

	backgroundEntry := NewNumberEntry(0.001, 4)
	peakEntry := NewNumberEntry(0.001, 4)
	scaledPeakEntry := NewNumberEntry(0.001, 4)

	backgroundEntry.SetValue(0)
	peakEntry.SetValue(1)
	scaledPeakEntry.SetValue(1)

	// Lock buttons: when engaged, the "Black" level and "Background level" fields
	// mirror each other (and likewise "White"/"Peak level"), so editing one input
	// updates the other. Locking is per-channel and only affects future edits; the
	// syncing guards prevent the paired SetValue from recursing back.
	var blackBgLocked, whitePeakLocked bool
	var syncingBlackBg, syncingWhitePeak bool

	blackBgLockBtn := widget.NewButton("Lock", nil)
	whitePeakLockBtn := widget.NewButton("Lock", nil)
	blackBgLockBtn.Importance = widget.LowImportance
	whitePeakLockBtn.Importance = widget.LowImportance

	setLockAppearance := func(btn *widget.Button, locked bool) {
		if locked {
			btn.SetText("Locked")
			btn.Importance = widget.HighImportance
		} else {
			btn.SetText("Lock")
			btn.Importance = widget.LowImportance
		}
		btn.Refresh()
	}
	blackBgLockBtn.OnTapped = func() {
		blackBgLocked = !blackBgLocked
		setLockAppearance(blackBgLockBtn, blackBgLocked)
	}
	whitePeakLockBtn.OnTapped = func() {
		whitePeakLocked = !whitePeakLocked
		setLockAppearance(whitePeakLockBtn, whitePeakLocked)
	}

	backgroundEntry.OnChanged = func(v float64) {
		if !blackBgLocked || syncingBlackBg {
			return
		}
		syncingBlackBg = true
		views[idx].blackBox.SetValue(v)
		syncingBlackBg = false
	}
	views[idx].blackBox.OnChanged = func(v float64) {
		if !blackBgLocked || syncingBlackBg {
			return
		}
		syncingBlackBg = true
		backgroundEntry.SetValue(v)
		syncingBlackBg = false
	}
	peakEntry.OnChanged = func(v float64) {
		if !whitePeakLocked || syncingWhitePeak {
			return
		}
		syncingWhitePeak = true
		views[idx].whiteBox.SetValue(v)
		syncingWhitePeak = false
	}
	views[idx].whiteBox.OnChanged = func(v float64) {
		if !whitePeakLocked || syncingWhitePeak {
			return
		}
		syncingWhitePeak = true
		peakEntry.SetValue(v)
		syncingWhitePeak = false
	}

	showClip := NewToggle(func(v bool) {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("show-clip", func(img *models.LoadedImage) error { img.ShowClip = v; return nil })
			return
		}
		imgs[idx].ShowClip = v
		refresh()
	})
	showClip.SetChecked(true)

	var apply *widget.Button
	apply = widget.NewButton("Apply", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			background, peak, scaled, black, white := backgroundEntry.Value(), peakEntry.Value(), scaledPeakEntry.Value(), views[idx].blackBox.Value(), views[idx].whiteBox.Value()
			asinh, mtfm, ghsd, ghsb, ghssp := asinhScaleEntry.Value(), mtfMidtoneEntry.Value(), ghsStretchEntry.Value(), ghsLocalEntry.Value(), ghsSymmetryEntry.Value()
			largeJob("apply", func(img *models.LoadedImage) error {
				img.Background, img.Peak, img.ScaledPeak, img.Black, img.White = background, peak, scaled, black, white
				img.AsinhScale, img.MTFMidtone, img.GHSStretch, img.GHSLocal, img.GHSSymmetry = asinh, mtfm, ghsd, ghsb, ghssp
				return nil
			})
			return
		}
		imgs[idx].Background = backgroundEntry.Value()
		imgs[idx].Peak = peakEntry.Value()
		imgs[idx].ScaledPeak = scaledPeakEntry.Value()
		imgs[idx].AsinhScale = asinhScaleEntry.Value()
		imgs[idx].MTFMidtone = mtfMidtoneEntry.Value()
		imgs[idx].GHSStretch = ghsStretchEntry.Value()
		imgs[idx].GHSLocal = ghsLocalEntry.Value()
		imgs[idx].GHSSymmetry = ghsSymmetryEntry.Value()
		imgs[idx].Black = views[idx].blackBox.Value()
		imgs[idx].White = views[idx].whiteBox.Value()
		apply.SetText("Working…")
		apply.Disable()
		go func() {
			time.Sleep(50 * time.Millisecond) // let Fyne paint "Working…" before blocking main thread
			fyne.Do(func() {
				refresh()
				apply.SetText("Apply")
				apply.Enable()
			})
		}()
	})
	apply.Importance = widget.HighImportance

	auto := widget.NewButton("Auto scaling", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("auto scaling", func(img *models.LoadedImage) error { processing.AutoScaleLikeFitsLiberator(img); return nil })
			return
		}
		processing.AutoScaleLikeFitsLiberator(imgs[idx])
		views[idx].blackBox.SetValue(imgs[idx].Black)
		views[idx].whiteBox.SetValue(imgs[idx].White)
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		scaledPeakEntry.SetValue(imgs[idx].ScaledPeak)
		refresh()
	})

	autoMTF := widget.NewButton("Auto MTF", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("auto MTF", func(img *models.LoadedImage) error { processing.AutoMTFMidtone(img); return nil })
			return
		}
		processing.AutoMTFMidtone(imgs[idx])
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		scaledPeakEntry.SetValue(imgs[idx].ScaledPeak)
		mtfMidtoneEntry.SetValue(imgs[idx].MTFMidtone)
		selectBox.SetSelected("MTF") // also reveals the MTF row and triggers refresh
	})

	magic := widget.NewButton("Magic", func() {
		if imgs[idx] == nil {
			return
		}
		invalidateChannelRefinement()
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			preset := processing.ParseMagicPreset(magicPreset.Selected)
			largeJob("magic", func(img *models.LoadedImage) error {
				processing.ApplyMagicLevelsAndMTF(img, preset)
				return nil
			})
			return
		}
		res := processing.ApplyMagicLevelsAndMTF(imgs[idx], processing.ParseMagicPreset(magicPreset.Selected))
		backgroundEntry.SetValue(imgs[idx].Background)
		peakEntry.SetValue(imgs[idx].Peak)
		views[idx].blackBox.SetValue(imgs[idx].Black)
		views[idx].whiteBox.SetValue(imgs[idx].White)
		mtfMidtoneEntry.SetValue(imgs[idx].MTFMidtone)
		selectBox.SetSelected("MTF") // also reveals the MTF row and triggers refresh
		debuglog.Log(fmt.Sprintf(
			"Magic[%s] ch%d: black=%.4g white=%.4g sky=%.4g sigma=%.4g clipLow=%.3f%% clipHigh=%.3f%% stars=%v(%.2f%%) whiteSrc=%s whiteN=%d(%.2f%%)",
			res.Preset, idx, res.Black, res.White, res.Background, res.Sigma,
			res.ClipLowPercent, res.ClipHighPercent, res.StarsExcluded, res.StarPixelPercent,
			res.WhiteSampleSource, res.WhiteSampleCount, res.WhiteSamplePercent))
		refresh()
	})

	xOffsetEntry := NewNumberEntry(1, 2)
	yOffsetEntry := NewNumberEntry(1, 2)
	rotOffsetEntry := NewNumberEntry(0.1, 1)

	// Manual Offsets are applied at render time (never baked into the pixels), so
	// "Apply Offset" simply re-renders the previews and composite with the current
	// X/Y/Rot field values.
	applyOffset := widget.NewButton("Apply Offset", func() {
		if imgs[idx] == nil {
			return
		}
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeJob("offset", func(_ *models.LoadedImage) error { return nil })
			return
		}
		refresh()
	})
	rotate := widget.NewButton("Rotate 90°", func() {
		if imgs[idx] == nil {
			return
		}
		if disk != nil && composeLargeModeActive != nil && composeLargeModeActive() {
			largeRotateChannel(disk, idx, imgs, refresh, func() {
				xOffsetEntry.SetValue(0)
				yOffsetEntry.SetValue(0)
				rotOffsetEntry.SetValue(0)
				clearComposeOrigPixels(origPixels, idx)
			})
			return
		}
		rotateComposeChannel90CW(imgs[idx])
		clearComposeChannelAlignment(imgs[idx])
		xOffsetEntry.SetValue(0)
		yOffsetEntry.SetValue(0)
		rotOffsetEntry.SetValue(0)
		clearComposeOrigPixels(origPixels, idx)
		refresh()
	})
	if disk != nil && disk.syncWidgets != nil {
		disk.syncWidgets[idx] = func(img *models.LoadedImage) {
			if syncingWidgets || img == nil {
				return
			}
			syncingWidgets = true
			backgroundEntry.SetValue(img.Background)
			peakEntry.SetValue(img.Peak)
			scaledPeakEntry.SetValue(img.ScaledPeak)
			views[idx].blackBox.SetValue(img.Black)
			views[idx].whiteBox.SetValue(img.White)
			mtfMidtoneEntry.SetValue(img.MTFMidtone)
			selectBox.SetSelected(modeToLabel(img.Mode))
			syncingWidgets = false
		}
	}

	return &models.ChannelControl{
		Content: container.NewVBox(
			func() fyne.CanvasObject {
				t := canvas.NewText(label, col)
				t.TextStyle = fyne.TextStyle{Bold: true}
				return t
			}(),
			selectBox,
			widget.NewForm(
				widget.NewFormItem("Background level", container.NewBorder(nil, nil, nil, blackBgLockBtn, backgroundEntry)),
				widget.NewFormItem("Peak level", container.NewBorder(nil, nil, nil, whitePeakLockBtn, peakEntry)),
				widget.NewFormItem("Scaled peak level", scaledPeakEntry),
			),
			asinhRow,
			mtfRow,
			ghsDRow,
			ghsBRow,
			ghsSPRow,
			container.NewHBox(showClip, widget.NewLabel("Show clipped pixels")),
			container.NewHBox(outlinedButton(col, auto), outlinedButton(col, autoMTF), outlinedButton(col, apply)),
			outlinedButton(col, magic),
			func() fyne.CanvasObject {
				if allowRotate {
					return outlinedButton(col, rotate)
				}
				return layout.NewSpacer()
			}(),
			widget.NewLabel("Manual Offset"),
			offsetRow("X", xOffsetEntry),
			offsetRow("Y", yOffsetEntry),
			offsetRow("Rot°", rotOffsetEntry),
			applyOffset,
			widget.NewSeparator(),
		),
		ModeSelect:        selectBox,
		BackgroundEntry:   backgroundEntry,
		PeakEntry:         peakEntry,
		ScaledPeakEntry:   scaledPeakEntry,
		AsinhScaleEntry:   asinhScaleEntry,
		MTFMidtoneEntry:   mtfMidtoneEntry,
		GHSStretchEntry:   ghsStretchEntry,
		GHSLocalEntry:     ghsLocalEntry,
		GHSSymmetryEntry:  ghsSymmetryEntry,
		MagicPresetSelect: magicPreset,
		XOffsetEntry:      xOffsetEntry,
		YOffsetEntry:      yOffsetEntry,
		RotOffsetEntry:    rotOffsetEntry,
		ShowClip:          showClip,
	}
}
