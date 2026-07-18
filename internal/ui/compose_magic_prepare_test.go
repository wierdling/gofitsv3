package ui

import (
	"context"
	"errors"
	"image/color"
	"math"
	"reflect"
	"strings"
	"testing"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func TestPrepareComposeMagicBatchLoadsAllBeforeProcessing(t *testing.T) {
	rows := composeMagicPrepareRows()
	var loaded []*models.LoadedImage
	loader := func(path string) (*models.LoadedImage, error) {
		for _, img := range loaded {
			if img.Mode != stretch.Linear {
				t.Fatalf("previous image was processed before all loads completed")
			}
		}
		img := composeMagicTestImage(path)
		loaded = append(loaded, img)
		return img, nil
	}

	batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Balanced", Rows: rows}, loader)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Channels) != len(rows) {
		t.Fatalf("prepared %d channels, want %d", len(batch.Channels), len(rows))
	}
}

func TestPrepareComposeMagicBatchFailureIsAtomic(t *testing.T) {
	rows := composeMagicPrepareRows()
	first := composeMagicTestImage(rows[0].File.Path)
	loadCount := 0
	batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Balanced", Rows: rows}, func(string) (*models.LoadedImage, error) {
		loadCount++
		if loadCount == 1 {
			return first, nil
		}
		return nil, errors.New("broken FITS")
	})
	if err == nil || batch != nil {
		t.Fatalf("prepare result = %#v, %v; want nil batch and error", batch, err)
	}
	if first.Mode != stretch.Linear || first.Background != 0 || first.Peak != 0 {
		t.Fatalf("first loaded image was modified after later load failure: %+v", first)
	}
}

func TestPrepareComposeMagicBatchProcessesInNumericFilterOrder(t *testing.T) {
	rows := composeMagicPrepareRows()
	rows[0], rows[2] = rows[2], rows[0]
	var loadedPaths []string
	batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Balanced", Rows: rows}, func(path string) (*models.LoadedImage, error) {
		loadedPaths = append(loadedPaths, path)
		return composeMagicTestImage(path), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"blue.fits", "green.fits", "red.fits", "custom.fits"}
	if !reflect.DeepEqual(loadedPaths, want) {
		t.Fatalf("load order = %v, want %v", loadedPaths, want)
	}
	for i, channel := range batch.Channels {
		if channel.Image.Path != want[i] {
			t.Errorf("prepared channel %d path = %q, want %q", i, channel.Image.Path, want[i])
		}
	}
}

func TestPrepareComposeMagicBatchPresetAffectsLevels(t *testing.T) {
	rows := composeMagicPrepareRows()
	prepare := func(preset string) *composeMagicBatch {
		batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: preset, Rows: rows}, func(path string) (*models.LoadedImage, error) {
			return composeMagicTestImage(path), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return batch
	}
	nebula := prepare("Nebula")
	galaxy := prepare("Galaxy")
	if nebula.Channels[0].Image.Peak == galaxy.Channels[0].Image.Peak {
		t.Fatalf("Nebula and Galaxy produced the same peak %v", nebula.Channels[0].Image.Peak)
	}
}

func TestPrepareComposeMagicBatchSetsValidMTFAndRetainsCustomAssignment(t *testing.T) {
	customColor := color.NRGBA{R: 12, G: 34, B: 56, A: 255}
	rows := composeMagicPrepareRows()
	rows[3].Color = customColor
	batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Galaxy", Rows: rows}, func(path string) (*models.LoadedImage, error) {
		return composeMagicTestImage(path), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Preset != "Galaxy" {
		t.Fatalf("prepared preset = %q, want Galaxy", batch.Preset)
	}
	for i, channel := range batch.Channels {
		if channel.Image.Mode != stretch.MTF || channel.Image.MTFMidtone <= 0 || channel.Image.MTFMidtone >= 1 {
			t.Errorf("channel %d has mode %v and midtone %v", i, channel.Image.Mode, channel.Image.MTFMidtone)
		}
	}
	custom := batch.Channels[3]
	if custom.Row.Assignment != composeMagicCustom || custom.Row.Color != customColor {
		t.Fatalf("custom row = %+v, want assignment Custom and color %#v", custom.Row, customColor)
	}
}

func TestPrepareComposeMagicBatchCancellationStopsAtEachBoundary(t *testing.T) {
	tests := []struct {
		name      string
		cancelAt  string
		wantCalls []string
	}{
		{name: "after load", cancelAt: "load"},
		{name: "after Magic", cancelAt: "magic", wantCalls: []string{"magic"}},
		{name: "after MTF selection", cancelAt: "mtf", wantCalls: []string{"magic", "mtf"}},
		{name: "after Auto MTF", cancelAt: "auto", wantCalls: []string{"magic", "mtf", "auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			var calls []string
			loader := func(path string) (*models.LoadedImage, error) {
				img := composeMagicTestImage(path)
				if tt.cancelAt == "load" {
					cancel()
				}
				return img, nil
			}
			stages := composeMagicStages{
				magic: func(*models.LoadedImage, processing.MagicPreset) processing.MagicLevelsResult {
					calls = append(calls, "magic")
					if tt.cancelAt == "magic" {
						cancel()
					}
					return processing.MagicLevelsResult{ValidPixels: 1}
				},
				setMTF: func(*models.LoadedImage) {
					calls = append(calls, "mtf")
					if tt.cancelAt == "mtf" {
						cancel()
					}
				},
				autoMTF: func(*models.LoadedImage) {
					calls = append(calls, "auto")
					if tt.cancelAt == "auto" {
						cancel()
					}
				},
			}
			batch, err := prepareComposeMagicBatchWithStages(ctx, composeMagicSpec{Preset: "Balanced", Rows: composeMagicPrepareRows()}, loader, stages)
			if batch != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("result = %#v, %v; want nil and context.Canceled", batch, err)
			}
			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("stage calls = %v, want %v", calls, tt.wantCalls)
			}
		})
	}
}

func TestPrepareComposeMagicBatchCanceledBeforeLoadDoesNotCallLoader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaderCalled := false
	batch, err := prepareComposeMagicBatch(ctx, composeMagicSpec{Preset: "Balanced", Rows: composeMagicPrepareRows()}, func(string) (*models.LoadedImage, error) {
		loaderCalled = true
		return composeMagicTestImage("unexpected.fits"), nil
	})
	if batch != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("result = %#v, %v; want nil and context.Canceled", batch, err)
	}
	if loaderCalled {
		t.Fatal("loader was called after cancellation")
	}
}

func TestPrepareComposeMagicBatchRejectsMalformedLoadedImages(t *testing.T) {
	tests := []struct {
		name string
		edit func(*models.LoadedImage)
		want string
	}{
		{name: "invalid dimensions", edit: func(img *models.LoadedImage) { img.HDU.Data.Width = 0 }, want: "invalid image dimensions"},
		{name: "empty pixels", edit: func(img *models.LoadedImage) { img.HDU.Data.Pixels = nil }, want: "image has no pixels"},
		{name: "short pixels", edit: func(img *models.LoadedImage) { img.HDU.Data.Pixels = img.HDU.Data.Pixels[:99] }, want: "has 99 pixels, need 10000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Balanced", Rows: composeMagicPrepareRows()}, func(path string) (*models.LoadedImage, error) {
				img := composeMagicTestImage(path)
				tt.edit(img)
				return img, nil
			})
			if batch != nil || err == nil || !strings.Contains(err.Error(), "F200W_blue_drz.fits") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("result = %#v, %v; want filename-qualified %q error", batch, err, tt.want)
			}
		})
	}
}

func TestPrepareComposeMagicBatchRejectsImagesWithoutValidSamples(t *testing.T) {
	tests := []struct {
		name string
		fill func([]float32)
	}{
		{name: "all zero", fill: func(pixels []float32) {
			for i := range pixels {
				pixels[i] = 0
			}
		}},
		{name: "all nonfinite", fill: func(pixels []float32) {
			for i := range pixels {
				if i%2 == 0 {
					pixels[i] = float32(math.NaN())
				} else {
					pixels[i] = float32(math.Inf(1))
				}
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var later *models.LoadedImage
			loadIndex := 0
			batch, err := prepareComposeMagicBatch(context.Background(), composeMagicSpec{Preset: "Balanced", Rows: composeMagicPrepareRows()}, func(path string) (*models.LoadedImage, error) {
				img := composeMagicTestImage(path)
				if loadIndex == 0 {
					tt.fill(img.HDU.Data.Pixels)
				} else if loadIndex == 1 {
					later = img
				}
				loadIndex++
				return img, nil
			})
			if batch != nil || err == nil || !strings.Contains(err.Error(), "F200W_blue_drz.fits") || !strings.Contains(err.Error(), "no valid image samples") {
				t.Fatalf("result = %#v, %v; want filename-qualified no-valid-samples error", batch, err)
			}
			if later == nil || later.Mode != stretch.Linear {
				t.Fatalf("later channel mode = %v, want unprocessed Linear", later.Mode)
			}
		})
	}
}

func TestComposeMagicCustomControlInstallPreservesMTF(t *testing.T) {
	const idx = 3
	img := composeMagicTestImage("custom.fits")
	img.Mode = stretch.MTF
	img.MTFMidtone = 0.23
	imgs := make([]*models.LoadedImage, idx+1)
	imgs[idx] = img
	origPixels := make([][]float32, idx+1)
	views := make([]*viewport, idx+1)
	views[idx] = newViewport()
	control := channelControls("Custom Image", color.NRGBA{R: 12, G: 34, B: 56, A: 255}, idx, imgs, &origPixels, views, func() {}, nil)
	controls := make([]*models.ChannelControl, idx+1)
	controls[idx] = control
	applyChannelState(idx, channelStateFromImage(img), imgs, views, controls)

	if img.Mode != stretch.MTF {
		t.Fatalf("installed custom image mode = %v, want MTF", img.Mode)
	}
	if control.ModeSelect.Selected != "MTF" {
		t.Fatalf("installed custom selector = %q, want MTF", control.ModeSelect.Selected)
	}
	if img.MTFMidtone != 0.23 {
		t.Fatalf("installed custom MTF midtone = %v, want 0.23", img.MTFMidtone)
	}
}

func TestPlanComposeMagicInstallFailureDoesNotMutateExistingChannels(t *testing.T) {
	rows := composeMagicPrepareRows()
	channels := make([]composeMagicPreparedChannel, len(rows))
	for i, row := range rows {
		channels[i] = composeMagicPreparedChannel{Row: row, Image: composeMagicTestImage(row.File.Path)}
	}
	batch := &composeMagicBatch{Preset: "Balanced", Channels: channels}
	imgs := make([]*models.LoadedImage, 3+maxOverlayLayers)
	for i := range imgs {
		imgs[i] = composeMagicTestImage(string(rune('a' + i)))
	}
	before := append([]*models.LoadedImage(nil), imgs...)
	existingSlots := make([]int, maxOverlayLayers)
	for i := range existingSlots {
		existingSlots[i] = 3 + i
	}
	plan, err := planComposeMagicInstall(batch, imgs, existingSlots)
	if plan != nil || err == nil {
		t.Fatalf("plan = %#v, %v; want atomic capacity failure", plan, err)
	}
	if !reflect.DeepEqual(imgs, before) {
		t.Fatal("planning failure mutated existing image destinations")
	}
}

func composeMagicPrepareRows() []composeMagicRow {
	return []composeMagicRow{
		{File: composeMagicFile{Path: "blue.fits", Name: "F200W_blue_drz.fits", FilterNumber: 200}, Assignment: composeMagicBlue, Color: color.NRGBA{A: 255}},
		{File: composeMagicFile{Path: "green.fits", Name: "F400W_green_drz.fits", FilterNumber: 400}, Assignment: composeMagicGreen, Color: color.NRGBA{A: 255}},
		{File: composeMagicFile{Path: "red.fits", Name: "F600W_red_drz.fits", FilterNumber: 600}, Assignment: composeMagicRed, Color: color.NRGBA{A: 255}},
		{File: composeMagicFile{Path: "custom.fits", Name: "F800W_custom_drz.fits", FilterNumber: 800}, Assignment: composeMagicCustom, Color: color.NRGBA{R: 1, G: 2, B: 3, A: 255}},
	}
}

func composeMagicTestImage(path string) *models.LoadedImage {
	pixels := make([]float32, 10000)
	for i := range pixels {
		pixels[i] = 10 + float32(i%200)/20
	}
	for i := 0; i < 100; i++ {
		pixels[i] = 40 + float32(i)
	}
	return &models.LoadedImage{
		Path: path,
		HDU: fitsio.HDU{Data: fitsio.ImageData{
			Width: 100, Height: 100, Pixels: pixels,
		}},
		Mode: stretch.Linear,
	}
}
