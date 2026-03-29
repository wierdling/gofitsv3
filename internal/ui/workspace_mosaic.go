package ui

import (
	"fmt"
	"image"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/badpix"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/stretch"
)

type mosaicState struct {
	sources []fitsio.HDU
	scale   float64
}

func newMosaicWorkspace(win fyne.Window) fyne.CanvasObject {
	state := &mosaicState{scale: 1.0}
	previewImg := image.NewRGBA(image.Rect(0, 0, 10, 10))
	preview := canvas.NewImageFromImage(previewImg)
	preview.FillMode = canvas.ImageFillContain

	loadBtn := widget.NewButton("Add FITS", func() {
		fd := dialog.NewFileOpen(func(r fyne.URIReadCloser, err error) {
			if err != nil || r == nil {
				return
			}
			path := r.URI().Path()
			file, err := fitsio.LoadFile(path)
			if err != nil {
				dialog.ShowError(err, win)
				return
			}
			sci := file.SelectSCI()
			hdu := file.HDUs[0]
			if len(sci) > 0 {
				hdu = sci[0]
			}
			if cleaned, err := cleanHDUWithDQ(hdu, file); err == nil {
				hdu = cleaned
			}
			state.sources = append(state.sources, hdu)
			redrawMosaic(preview, state)
		}, win)
		fd.Show()
	})

	scaleEntry := widget.NewEntry()
	scaleEntry.SetText("1.0")
	scaleEntry.OnChanged = func(s string) {
		var val float64
		fmt.Sscanf(s, "%f", &val)
		if val > 0 {
			state.scale = val
			redrawMosaic(preview, state)
		}
	}

	controls := container.NewVBox(
		loadBtn,
		widget.NewForm(widget.NewFormItem("Scale", scaleEntry)),
		widget.NewButton("Clear", func() {
			state.sources = nil
			redrawMosaic(preview, state)
		}),
	)

	split := container.NewHSplit(controls, preview)
	split.SetOffset(0.25)
	return split
}

func redrawMosaic(preview *canvas.Image, state *mosaicState) {
	if len(state.sources) == 0 {
		return
	}
	base := state.sources[0]
	norm := base.Data.Normalize()
	stretched := stretch.Apply(norm.Pixels, stretch.Asinh)
	buf, w, h := mosaic.Drizzle(stretched, norm.Width, norm.Height, mosaic.DrizzleParams{Scale: state.scale})
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i, v := range buf {
		b := byte(v * 255)
		idx := i * 4
		img.Pix[idx] = b
		img.Pix[idx+1] = b
		img.Pix[idx+2] = b
		img.Pix[idx+3] = 255
	}
	preview.Image = img
	preview.Refresh()
}

// cleanHDUWithDQ runs bad-pixel masking using the file's DQ extension when available.
func cleanHDUWithDQ(hdu fitsio.HDU, file *fitsio.File) (fitsio.HDU, error) {
	dq := file.SelectDQ()
	if dq == nil {
		return hdu, fmt.Errorf("DQ not found")
	}
	mask, err := badpix.MaskFromDQ(hdu, *dq, 0)
	if err != nil {
		return hdu, err
	}
	hdu.Data = badpix.InterpolateBicubic(hdu.Data, mask)
	return hdu, nil
}
