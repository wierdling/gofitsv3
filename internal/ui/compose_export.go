package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/export"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/utils"
)

func (ws *composeWorkspace) detectExportFormat(path string) export.Format {
	switch {
	case strings.HasSuffix(path, ".webp"):
		return export.WEBP
	case strings.HasSuffix(path, ".png"):
		return export.PNG
	case strings.HasSuffix(path, ".tif"), strings.HasSuffix(path, ".tiff"):
		return export.TIFF
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return export.JPEG
	default:
		return export.PNG
	}
}

func (ws *composeWorkspace) saveChannelGray(idx int) {
	if ws.largeMode {
		if ws.imgs[idx] == nil {
			dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), ws.win)
			return
		}
		d, ok := ws.largeArtifacts[idx]
		if !ok {
			dialog.ShowInformation("Missing", "The channel artifact is unavailable.", ws.win)
			return
		}
		save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
			if err != nil || uc == nil {
				return
			}
			path := uc.URI().Path()
			_ = uc.Close()
			format := ws.detectExportFormat(path)
			showExportOptionsDialog(format, ws.win, func(opts export.Options) {
				if err := export.FromFloat32Artifact(context.Background(), path, d.Path, d.Width, d.Height, format, opts, ws.imgs[idx]); err != nil {
					dialog.ShowError(err, ws.win)
				}
			})
		}, ws.win)
		save.SetFileName(fmt.Sprintf("channel_%d_gray.png", idx+1))
		save.Show()
		return
	}
	if ws.imgs[idx] == nil {
		dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), ws.win)
		return
	}

	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		path := uc.URI().Path()
		_ = uc.Close()

		format := ws.detectExportFormat(path)
		stretched, _ := processing.StretchForDisplay(ws.imgs[idx])
		gray := processing.ToGrayRGBA(stretched, make([]byte, len(stretched.Pixels)))
		showExportOptionsDialog(format, ws.win, func(opts export.Options) {
			if err := export.FromImage(path, gray, format, opts); err != nil {
				dialog.ShowError(err, ws.win)
			}
		})
	}, ws.win)
	save.SetFileName(fmt.Sprintf("channel_%d_gray.png", idx+1))
	save.Show()
}

func (ws *composeWorkspace) closeHeaderWindow(idx int) {
	if ws.headerWins[idx] != nil {
		ws.headerWins[idx].SetCloseIntercept(nil)
		ws.headerWins[idx].Close()
		ws.headerWins[idx] = nil
	}
}

func (ws *composeWorkspace) showHeader(idx int) {
	if ws.imgs[idx] == nil {
		return
	}
	ws.closeHeaderWindow(idx)
	lines := utils.FormatHeadersLines(ws.imgs[idx].Primary, ws.imgs[idx].HDU.Header)
	list := widget.NewList(
		func() int { return len(lines) },
		func() fyne.CanvasObject {
			lbl := widget.NewLabel("")
			lbl.Wrapping = fyne.TextWrapOff
			lbl.TextStyle = fyne.TextStyle{Monospace: true}
			return lbl
		},
		func(id widget.ListItemID, co fyne.CanvasObject) {
			lbl := co.(*widget.Label)
			lbl.SetText(lines[id])
		},
	)
	w := ws.app.NewWindow(fmt.Sprintf("Channel %d Headers", idx+1))
	w.SetContent(list)
	w.Resize(fyne.NewSize(700, 500))
	w.SetCloseIntercept(func() {
		w.SetCloseIntercept(nil)
		w.Close()
		ws.headerWins[idx] = nil
	})
	ws.headerWins[idx] = w
	w.Show()
}

func (ws *composeWorkspace) saveHeader(idx int) {
	if ws.imgs[idx] == nil {
		dialog.ShowInformation("Missing", fmt.Sprintf("Load Channel %d first", idx+1), ws.win)
		return
	}

	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		defer uc.Close()

		lines := utils.FormatHeadersLines(ws.imgs[idx].Primary, ws.imgs[idx].HDU.Header)
		content := strings.Join(lines, "\n") + "\n"
		if _, err := uc.Write([]byte(content)); err != nil {
			dialog.ShowError(err, ws.win)
		}
	}, ws.win)
	save.SetFileName(fmt.Sprintf("channel_%d_headers.txt", idx+1))
	save.SetFilter(storage.NewExtensionFileFilter([]string{".txt"}))
	save.Show()
}

func (ws *composeWorkspace) exportRGB() {
	// Only a missing channel is reported as missing; every other compose
	// failure (disk artifact, LRGB load, PSF, ...) is shown as itself.
	if !composeHasAllBaseChannels(ws.imgs) {
		dialog.ShowInformation("Missing", "Load three FITS first", ws.win)
		return
	}
	in := ws.snapshotRenderInput()
	levels := in.levels
	job := startUIJob(&ws.modalJobs, "Exporting", "Composing full-resolution image…", ws.win)
	go func() {
		buf, w, h, _, err := ws.composeRGB(job.ctx, in, nil)
		if err == nil && buf == nil {
			err = errors.New("compose produced no image")
		}
		var largePaths [3]string
		var finalBuf []byte
		if err == nil && in.large {
			comp, ok := in.largeStore.Composite()
			if !ok {
				err = fmt.Errorf("missing disk composite")
			} else {
				for i := range comp.Planes {
					largePaths[i] = comp.Planes[i].Path
				}
				w, h = comp.Width, comp.Height
			}
		} else if err == nil {
			finalBuf = processing.ApplyRGBLevels(buf, &levels)
		}
		job.finish(func() {
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					dialog.ShowError(err, ws.win)
				}
				return
			}
			ws.saveExportRGB(in.large, finalBuf, largePaths, w, h, levels)
		}, nil)
	}()
}

// saveExportRGB asks for the destination and options, then writes the
// composed image in the background.
func (ws *composeWorkspace) saveExportRGB(large bool, finalBuf []byte, largePaths [3]string, w, h int, levels models.RgbLevels) {
	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		path := uc.URI().Path()
		_ = uc.Close()
		format := ws.detectExportFormat(path)
		showExportOptionsDialog(format, ws.win, func(opts export.Options) {
			if large && format == export.PNG && opts.BitDepth != 16 {
				opts.BitDepth = 8
			}
			job := startUIJob(&ws.modalJobs, "Exporting", "Writing "+filepath.Base(path)+"…", ws.win)
			go func() {
				var err error
				if large {
					err = export.FromFloat32ArtifactsWithLevels(job.ctx, path, largePaths, w, h, format, opts, &levels)
				} else {
					err = export.FromRGBABytes(path, finalBuf, w, h, format, opts)
				}
				if err == nil {
					debuglog.Log(fmt.Sprintf("exportRGB: wrote composite %s", path))
				}
				// Report write errors even after Cancel: the file may be partial.
				fyne.Do(func() {
					if err != nil && !errors.Is(err, context.Canceled) {
						dialog.ShowError(err, ws.win)
					}
				})
				job.finish(nil, nil)
			}()
		})
	}, ws.win)
	save.SetFileName("composite.png")
	save.Show()
}
