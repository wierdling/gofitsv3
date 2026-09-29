package ui

import (
	"context"
	"fmt"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/export"
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
	buf, w, h, _, err := ws.composeRGB(context.Background())
	if buf == nil {
		dialog.ShowInformation("Missing", "Load three FITS first", ws.win)
		return
	}
	if err != nil {
		dialog.ShowError(err, ws.win)
		return
	}
	var largePaths [3]string
	if ws.largeMode && ws.largeStore != nil {
		comp, ok := ws.largeStore.Composite()
		if !ok {
			dialog.ShowError(fmt.Errorf("missing disk composite"), ws.win)
			return
		}
		for i := range comp.Planes {
			largePaths[i] = comp.Planes[i].Path
		}
		w, h = comp.Width, comp.Height
	}
	finalBuf := processing.ApplyRGBLevels(buf, ws.levels)
	save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
		if err != nil || uc == nil {
			return
		}
		path := uc.URI().Path()
		_ = uc.Close()
		format := ws.detectExportFormat(path)
		showExportOptionsDialog(format, ws.win, func(opts export.Options) {
			if ws.largeMode {
				if format == export.PNG && opts.BitDepth != 16 {
					opts.BitDepth = 8
				}
				if err := export.FromFloat32ArtifactsWithLevels(context.Background(), path, largePaths, w, h, format, opts, ws.levels); err != nil {
					dialog.ShowError(err, ws.win)
					return
				}
				debuglog.Log(fmt.Sprintf("exportRGB: wrote disk composite %s", path))
				return
			}
			if err := export.FromRGBABytes(path, finalBuf, w, h, format, opts); err != nil {
				dialog.ShowError(err, ws.win)
				return
			}
			debuglog.Log(fmt.Sprintf("exportRGB: wrote composite %s", path))
		})
	}, ws.win)
	save.SetFileName("composite.png")
	save.Show()
}
