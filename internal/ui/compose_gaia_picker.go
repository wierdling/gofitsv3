package ui

import (
	"context"
	"fmt"
	"image"
	"math"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/catalog/gaia"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

const composeGaiaPickerMinStars = 6

const composeGaiaPickerMinSeparationPx = 2.0

// validateComposeGaiaPickerGeometry defines the picker coordinate contract:
// selections are native Channel 2 pixels, before any render-time transform.
// A rotated or fitted channel cannot be interpreted safely here, so it is
// rejected instead of silently guessing a transform.
func validateComposeGaiaPickerGeometry(img *models.LoadedImage) error {
	if img == nil || img.HDU.Data.Width <= 0 || img.HDU.Data.Height <= 0 {
		return fmt.Errorf("Channel 2 must have a non-empty image")
	}
	if img.Rotation90%4 != 0 {
		return fmt.Errorf("Gaia star picking does not support a rotated Channel 2")
	}
	if img.HasAlignTransform {
		return fmt.Errorf("Gaia star picking does not support an aligned Channel 2; reset alignment first")
	}
	return nil
}

func validateComposeGaiaPickerStars(stars []processing.Star, width, height int) error {
	if len(stars) < composeGaiaPickerMinStars {
		return fmt.Errorf("select at least %d Channel 2 stars", composeGaiaPickerMinStars)
	}
	if width <= 0 || height <= 0 {
		return fmt.Errorf("invalid Channel 2 dimensions")
	}
	for i, star := range stars {
		if math.IsNaN(star.X) || math.IsNaN(star.Y) || math.IsInf(star.X, 0) || math.IsInf(star.Y, 0) || star.X < 0 || star.Y < 0 || star.X >= float64(width) || star.Y >= float64(height) {
			return fmt.Errorf("selected star %d is outside the Channel 2 image", i+1)
		}
		for j := 0; j < i; j++ {
			dx := star.X - stars[j].X
			dy := star.Y - stars[j].Y
			if dx*dx+dy*dy < composeGaiaPickerMinSeparationPx*composeGaiaPickerMinSeparationPx {
				return fmt.Errorf("selected stars %d and %d are too close; choose distinct stars", j+1, i+1)
			}
		}
	}
	return nil
}

type composeGaiaResidualResult struct {
	Transform processing.AffineTransform
	Stats     processing.AlignStats
	Sources   []gaia.Source
}

type composeGaiaResidualRunner func(context.Context, []processing.Star) (composeGaiaResidualResult, error)

func composeGaiaPickerCompletionAllowed(ctx context.Context) bool {
	return ctx != nil && ctx.Err() == nil
}

func composeGaiaPickerCancel(jobRunning *bool, jobCancel *context.CancelFunc) bool {
	if jobCancel == nil || *jobCancel == nil {
		return false
	}
	(*jobCancel)()
	*jobCancel = nil
	if jobRunning != nil {
		*jobRunning = false
	}
	return true
}

// composeGaiaPickerImage returns the native Channel 2 raster when it is
// resident (normal Compose mode). Disk-backed mode intentionally falls back
// to its bounded preview; materialising a full artifact just to open the
// picker would defeat large-file memory limits.
func composeGaiaPickerImage(img *models.LoadedImage, preview image.Image) image.Image {
	if img != nil {
		w, h := img.HDU.Data.Width, img.HDU.Data.Height
		if w > 0 && h > 0 && len(img.HDU.Data.Pixels) >= w*h {
			stretched, _ := processing.ApplyStretchParallel(img)
			return processing.ToGrayRGBA(stretched, nil)
		}
	}
	return preview
}

// showComposeGaiaPicker opens the bounded Channel 2 picker. The runner owns
// source discovery and fitting; cancellation is propagated through its context.
// A successful result is published only after the user presses Apply.
func showComposeGaiaPicker(app fyne.App, win fyne.Window, img *models.LoadedImage, preview image.Image, run composeGaiaResidualRunner, apply func(composeGaiaResidualResult) bool) {
	if err := validateComposeGaiaPickerGeometry(img); err != nil {
		dialog.ShowError(err, win)
		return
	}
	if preview == nil {
		dialog.ShowError(fmt.Errorf("Channel 2 preview is unavailable"), win)
		return
	}
	picker := newStarPickerWidget(composeGaiaPickerImage(img, preview), img.HDU.Data.Width, img.HDU.Data.Height)
	picker.MaxStars = 50
	status := widget.NewLabel(fmt.Sprintf("Select at least %d bright stars (native Channel 2 pixels).", composeGaiaPickerMinStars))
	diagnostics := widget.NewLabel("")
	clear := widget.NewButton("Clear", picker.ClearStars)
	cancel := widget.NewButton("Cancel", nil)
	calculate := widget.NewButton("Discover Gaia + fit residual", nil)
	applyButton := widget.NewButton("Apply", nil)
	applyButton.Disable()
	var latest composeGaiaResidualResult
	var jobCancel context.CancelFunc
	var jobRunning bool
	calculate.OnTapped = func() {
		if jobRunning {
			return
		}
		stars := append([]processing.Star(nil), picker.Stars...)
		if err := validateComposeGaiaPickerStars(stars, img.HDU.Data.Width, img.HDU.Data.Height); err != nil {
			status.SetText(err.Error())
			return
		}
		ctx, cancelJob := context.WithCancel(context.Background())
		jobCancel, jobRunning = cancelJob, true
		calculate.Disable()
		status.SetText("Discovering Gaia sources and fitting residual…")
		go func() {
			result, err := run(ctx, stars)
			fyne.Do(func() {
				// A closed dialog or explicit cancellation owns the result lifetime;
				// never publish even a successful late result into its widgets.
				if !composeGaiaPickerCompletionAllowed(ctx) {
					return
				}
				jobRunning = false
				jobCancel = nil
				calculate.Enable()
				if err != nil {
					if ctx.Err() != nil {
						status.SetText("Gaia residual fit cancelled")
					} else {
						status.SetText("Gaia residual fit failed")
						diagnostics.SetText(err.Error())
					}
					return
				}
				status.SetText("Gaia residual fit complete (not applied)")
				diagnostics.SetText(fmt.Sprintf("matched=%d  inliers=%d  RMS=%.3f px  max=%.3f px  shift=(%.3f, %.3f)", result.Stats.MatchedStars, result.Stats.GlobalInliers, result.Stats.RMS, result.Stats.MaxError, result.Transform.C, result.Transform.F))
				latest = result
				applyButton.Enable()
			})
		}()
	}
	applyButton.OnTapped = func() {
		if apply == nil || len(latest.Sources) == 0 {
			return
		}
		if !apply(latest) {
			status.SetText("Gaia residual is stale; run discovery again")
			applyButton.Disable()
			return
		}
		status.SetText("Gaia residual applied for this Compose session")
		applyButton.Disable()
	}
	cancel.OnTapped = func() {
		if composeGaiaPickerCancel(&jobRunning, &jobCancel) {
			// Release the local busy state immediately. The runner may still be
			// unwinding, but its completion is suppressed by the context guard.
			calculate.Enable()
			status.SetText("Gaia residual fit cancelled")
		}
	}
	picker.OnChanged = func() {
		latest = composeGaiaResidualResult{}
		applyButton.Disable()
		status.SetText(fmt.Sprintf("Selected %d / 50 stars (native Channel 2 pixels)", len(picker.Stars)))
	}
	content := container.NewBorder(nil, container.NewVBox(status, diagnostics, container.NewHBox(clear, calculate, applyButton, cancel)), nil, nil, container.NewVScroll(picker))
	w := app.NewWindow("Channel 2 Gaia Star Refinement")
	w.SetContent(content)
	w.Resize(fyne.NewSize(900, 700))
	w.SetOnClosed(func() {
		if jobCancel != nil {
			jobCancel()
		}
	})
	w.Show()
}

// composeGaiaResidualRunnerForImage adapts the existing Gaia provider and
// projection setup to the picker without changing calibration state.
func composeGaiaResidualRunnerForImage(img *models.LoadedImage, settings models.GaiaCalibrationSettings) composeGaiaResidualRunner {
	return func(ctx context.Context, observed []processing.Star) (composeGaiaResidualResult, error) {
		settings = resolveGaiaSettings(settings)
		q, _, skyToPixel, err := deriveGaiaFieldProjection(img, settings)
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		cachePath, err := gaiaCachePath(settings, "")
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		cache, err := composeGaiaCacheOpener(ctx, cachePath)
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		defer cache.Close()
		mode := gaia.AccessOnline
		if settings.AccessMode == "cacheOnly" {
			mode = gaia.AccessCacheOnly
		}
		provider, err := newGaiaProvider(settings.Endpoint, mode, settings.Release, settings.XPRepresentation, cache)
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		sources, err := provider.DiscoverSources(ctx, q)
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		sources, err = gaia.NormalizeSources(sources, settings.Release)
		if err != nil {
			return composeGaiaResidualResult{}, err
		}
		transform, stats, err := processing.FitGaiaSourceResidual(sources, observed, q.ObservationEpoch, skyToPixel, img.HDU.Data.Width, img.HDU.Data.Height, 30, "general")
		return composeGaiaResidualResult{Transform: transform, Stats: stats, Sources: append([]gaia.Source(nil), sources...)}, err
	}
}
