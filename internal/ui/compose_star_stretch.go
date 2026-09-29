package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/starstretchpreview"
)

type starStretchPreviewOptions struct {
	Strength float64
	Limit    int
	SourceID int
}

func parseStarStretchPreviewOptions(strengthText, limitText, sourceIDText string) (starStretchPreviewOptions, error) {
	strength, err := strconv.ParseFloat(strings.TrimSpace(strengthText), 64)
	if err != nil || math.IsNaN(strength) || math.IsInf(strength, 0) || strength < 0 || strength > 1 {
		return starStretchPreviewOptions{}, fmt.Errorf("strength must be a number from 0 to 1")
	}
	limit, err := strconv.Atoi(strings.TrimSpace(limitText))
	if err != nil || limit < 1 || limit > 24 {
		return starStretchPreviewOptions{}, fmt.Errorf("maximum stars must be an integer from 1 to 24")
	}
	sourceID := 0
	if strings.TrimSpace(sourceIDText) != "" {
		sourceID, err = strconv.Atoi(strings.TrimSpace(sourceIDText))
		if err != nil || sourceID < 0 {
			return starStretchPreviewOptions{}, fmt.Errorf("source ID must be zero or a positive integer")
		}
	}
	return starStretchPreviewOptions{Strength: strength, Limit: limit, SourceID: sourceID}, nil
}

func snapshotStarStretchSource(img *models.LoadedImage) (string, models.LoadedImage, bool) {
	if img == nil || strings.TrimSpace(img.Path) == "" {
		return "", models.LoadedImage{}, false
	}
	metadata := *img
	// The backend deliberately reopens the original file. Never retain the
	// current in-memory pixels or a live HDU data reference in the job.
	metadata.HDU.Header = fitsio.CloneHeader(metadata.HDU.Header)
	metadata.HDU.Data.Pixels = nil
	metadata.HDU.Data.Int32Pixels = nil
	metadata.Primary = fitsio.CloneHeader(metadata.Primary)
	return img.Path, metadata, true
}

func showComposeStarStretchPreviewDialog(win fyne.Window, imgs []*models.LoadedImage) {
	selected := 2
	for selected >= 0 && (selected >= len(imgs) || imgs[selected] == nil || strings.TrimSpace(imgs[selected].Path) == "") {
		selected--
	}
	if selected < 0 {
		dialog.ShowInformation("Gentler Star Stretch", "Load a Compose source image before opening this preview.", win)
		return
	}

	sources := make([]int, 0, len(imgs))
	labels := make([]string, 0, len(imgs))
	for i, img := range imgs {
		if _, _, ok := snapshotStarStretchSource(img); !ok {
			continue
		}
		sources = append(sources, i)
		labels = append(labels, fmt.Sprintf("Channel %d — %s", i+1, img.Path))
	}
	if len(sources) == 0 {
		dialog.ShowInformation("Gentler Star Stretch", "No loaded Compose source has an original FITS path.", win)
		return
	}
	selectedPos := 0
	for i, source := range sources {
		if source == selected {
			selectedPos = i
			break
		}
	}

	sourceSelect := widget.NewSelect(labels, nil)
	sourceSelect.SetSelectedIndex(selectedPos)
	strength := widget.NewEntry()
	strength.SetText("0.35")
	limit := widget.NewEntry()
	limit.SetText("8")
	sourceID := widget.NewEntry()
	sourceID.SetPlaceHolder("0 = brightest accepted star")
	help := widget.NewLabel("Diagnostic only: reads the original linear FITS file on that source grid. It ignores alignment, rotation, cleanup, PSF edits, and the current composite output.")
	help.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(
		widget.NewLabel("Preview a gentler stretch for stars in one loaded source."),
		container.NewGridWithColumns(2, widget.NewLabel("Source"), sourceSelect),
		container.NewGridWithColumns(2, widget.NewLabel("Gentler strength (0–1)"), strength),
		container.NewGridWithColumns(2, widget.NewLabel("Maximum stars (1–24)"), limit),
		container.NewGridWithColumns(2, widget.NewLabel("Source ID (optional)"), sourceID),
		help,
	)
	d := dialog.NewCustomConfirm("Gentler Star Stretch Preview", "Preview", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		options, err := parseStarStretchPreviewOptions(strength.Text, limit.Text, sourceID.Text)
		if err != nil {
			dialog.ShowError(err, win)
			return
		}
		idx := sources[sourceSelect.SelectedIndex()]
		path, metadata, ok := snapshotStarStretchSource(imgs[idx])
		if !ok {
			dialog.ShowInformation("Gentler Star Stretch", "The selected source no longer has an original FITS path. Reopen the preview.", win)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		pt := newProgressTrackerWithContextOnUI("Gentler Star Stretch", "Reading original source FITS data...", win, ctx, cancel)
		go func() {
			report, runErr := starstretchpreview.Generate(ctx, starstretchpreview.Request{
				SciencePath: path,
				MapPath:     "",
				Metadata:    metadata,
				AutoLevels:  false,
				Strength:    options.Strength,
				Limit:       options.Limit,
				SourceID:    options.SourceID,
			}, pt.progress)
			pt.hide()
			fyne.Do(func() {
				defer cancel()
				if ctx.Err() != nil {
					return
				}
				if runErr != nil {
					if !errors.Is(runErr, context.Canceled) {
						dialog.ShowError(runErr, win)
					}
					return
				}
				showStarStretchPreviewReport(win, report)
			})
		}()
	}, win)
	d.Show()
}

func showStarStretchPreviewReport(win fyne.Window, report *starstretchpreview.Report) {
	if report == nil {
		dialog.ShowInformation("Gentler Star Stretch", "The preview produced no report.", win)
		return
	}
	summary := widget.NewLabel(report.Summary)
	summary.Wrapping = fyne.TextWrapWord
	settings := widget.NewLabel(fmt.Sprintf("%s · Strength %g", report.Settings, report.Strength))
	settings.Wrapping = fyne.TextWrapWord
	content := container.NewVBox(
		widget.NewLabel("Original source-grid diagnostic; this preview does not change Compose."),
		widget.NewLabel(fmt.Sprintf("Science: %s\nStar map: %s", report.SciencePath, report.MapPath)),
		summary,
		settings,
	)
	for _, star := range report.Stars {
		status := widget.NewLabel(fmt.Sprintf("Source %d — %s: %s", star.SourceID, star.Status, star.Reason))
		status.Wrapping = fyne.TextWrapWord
		content.Add(status)
		if star.Status != "preview" {
			content.Add(widget.NewLabel("No corrected image or treatment mask was generated for this source."))
			if star.Normal != nil {
				view := canvas.NewImageFromImage(star.Normal)
				view.SetMinSize(fyne.NewSize(180, 180))
				view.FillMode = canvas.ImageFillContain
				content.Add(container.NewGridWithColumns(3, container.NewBorder(nil, widget.NewLabel("Original appearance"), nil, nil, view), widget.NewLabel(""), widget.NewLabel("")))
			}
			continue
		}
		stamps := make([]fyne.CanvasObject, 0, 3)
		for _, item := range []struct {
			name string
			img  image.Image
		}{{"Normal", star.Normal}, {"Gentle", star.Gentle}, {"Mask", star.Mask}} {
			if item.img == nil {
				stamps = append(stamps, widget.NewLabel(item.name+" unavailable"))
				continue
			}
			view := canvas.NewImageFromImage(item.img)
			view.SetMinSize(fyne.NewSize(180, 180))
			view.FillMode = canvas.ImageFillContain
			stamps = append(stamps, container.NewBorder(nil, widget.NewLabel(item.name), nil, nil, view))
		}
		content.Add(container.NewGridWithColumns(3, stamps...))
	}
	scroll := container.NewScroll(content)
	scroll.SetMinSize(fyne.NewSize(760, 620))
	dialog.ShowCustom("Gentler Star Stretch Preview", "Close", scroll, win)
}
