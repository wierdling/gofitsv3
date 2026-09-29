package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"sort"
	"sync"

	"fyne.io/fyne/v2"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

type magicStretchSnapshot struct {
	Background, Peak, Black, White, MTFMidtone float64
	Mode                                       stretch.Mode
}

// snapshotLargeLoadedImage copies the small, immutable channel state needed by
// a background large-file job. Pixel buffers are deliberately detached because
// the artifact is materialized separately by the worker.
func snapshotLargeLoadedImage(img *models.LoadedImage) models.LoadedImage {
	if img == nil {
		return models.LoadedImage{}
	}
	snapshot := *img
	snapshot.HDU = img.HDU
	snapshot.HDU.Header.Cards = cloneHeaderCards(img.HDU.Header.Cards)
	snapshot.HDU.Data.Pixels = nil
	snapshot.HDU.Data.Int32Pixels = nil
	snapshot.Primary.Cards = cloneHeaderCards(img.Primary.Cards)
	return snapshot
}

func cloneHeaderCards(cards map[string]string) map[string]string {
	if cards == nil {
		return nil
	}
	clone := make(map[string]string, len(cards))
	for key, value := range cards {
		clone[key] = value
	}
	return clone
}

func snapshotMagicStretch(img *models.LoadedImage) magicStretchSnapshot {
	return magicStretchSnapshot{img.Background, img.Peak, img.Black, img.White, img.MTFMidtone, img.Mode}
}

func magicStretchUnchanged(img *models.LoadedImage, s magicStretchSnapshot) bool {
	return snapshotMagicStretch(img) == s
}

func installMagicStretch(dst *models.LoadedImage, src models.LoadedImage) {
	dst.Background, dst.Peak, dst.Black, dst.White, dst.MTFMidtone, dst.Mode = src.Background, src.Peak, src.Black, src.White, src.MTFMidtone, src.Mode
}

type largeChannelRuntime struct {
	store       *composeLargeStore
	artifacts   map[int]composeArtifactDescriptor
	previews    map[int]*image.RGBA
	mu          *sync.RWMutex
	jobMu       sync.Mutex
	syncWidgets map[int]func(*models.LoadedImage)
	refresh     func()
}

func composeLargeStretchedPreview(path string, img *models.LoadedImage) (*image.RGBA, float32, float32, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer a.Close()
	step := 1
	if a.Width > 1600 || a.Height > 1600 {
		if a.Width > a.Height {
			step = (a.Width + 1599) / 1600
		} else {
			step = (a.Height + 1599) / 1600
		}
	}
	pw, ph := (a.Width+step-1)/step, (a.Height+step-1)/step
	out := image.NewRGBA(image.Rect(0, 0, pw, ph))
	var eqVals []float32
	if img.Mode == stretch.HistEq {
		eqVals = make([]float32, pw*ph)
	}
	eqHist := make([]int, 256)
	row := make([]float32, a.Width)
	var min, max float32
	first := true
	for y := 0; y < a.Height; y++ {
		if err := a.ReadRow(y, row); err != nil {
			return nil, 0, 0, err
		}
		for _, v := range row {
			if !isFiniteLarge(v) {
				continue
			}
			if first || v < min {
				min = v
			}
			if first || v > max {
				max = v
			}
			first = false
		}
		if y%step != 0 {
			continue
		}
		py := y / step
		for x := 0; x < a.Width; x += step {
			v := row[x]
			q := float64(processing.DiskStretchPreviewValue(v, *img))
			if eqVals != nil {
				eqVals[py*pw+x/step] = float32(q)
			} else {
				c := uint8(q*255 + 0.5)
				out.SetRGBA(x/step, py, color.RGBA{c, c, c, 255})
			}
		}
	}
	if eqVals != nil {
		for _, v := range eqVals {
			eqHist[int(v*255)]++
		}
		total := 0
		for _, n := range eqHist {
			total += n
		}
		sum := 0
		cdf := make([]float32, 256)
		for i, n := range eqHist {
			sum += n
			if total > 0 {
				cdf[i] = float32(float64(sum) / float64(total))
			}
		}
		for i, v := range eqVals {
			c := uint8(cdf[int(v*255)]*255 + 0.5)
			out.SetRGBA(i%pw, i/pw, color.RGBA{c, c, c, 255})
		}
	}
	if first {
		min, max = 0, 1
	}
	return out, min, max, nil
}

func largeArtifactRange(path string) (float32, float32, error) {
	a, err := fitsio.OpenFloat32ArtifactReadOnly(path)
	if err != nil {
		return 0, 0, err
	}
	defer a.Close()
	row := make([]float32, a.Width)
	var min, max float32
	first := true
	for y := 0; y < a.Height; y++ {
		if err := a.ReadRow(y, row); err != nil {
			return 0, 0, err
		}
		for _, v := range row {
			if !isFiniteLarge(v) {
				continue
			}
			if first || v < min {
				min = v
			}
			if first || v > max {
				max = v
			}
			first = false
		}
	}
	if first {
		return 0, 1, nil
	}
	return min, max, nil
}

type largeStretchSummary struct {
	median, high                   float64
	black, white, background, peak float64
	cores                          []float64
	// coreAnchors are retained as a compact catalog so target channels can
	// locate the corresponding star before sampling its core value.  Keeping
	// coordinates (rather than a second pixel plane) preserves the one-plane
	// large-file invariant.
	coreAnchors []processing.Star
}

// largeArtifactStretchSummary deliberately materializes only the channel being
// summarized. It mirrors the normal helper's finite-value percentiles and
// SmartLevels exactly, then releases the plane before the next channel pass.
func largeArtifactStretchSummary(path string, img *models.LoadedImage, starCores bool) (largeStretchSummary, error) {
	return largeArtifactStretchSummaryWithAnchors(path, img, starCores, nil, 0, 0)
}

func largeArtifactStretchSummaryWithAnchors(path string, img *models.LoadedImage, starCores bool, refAnchors []processing.Star, refWidth, refHeight int) (largeStretchSummary, error) {
	lease, err := fitsio.MaterializeFloat32ArtifactLease(path)
	if err != nil {
		return largeStretchSummary{}, err
	}
	defer lease.Release()
	s := largeStretchSummary{}
	var ok bool
	if s.median, ok = composePercentile(lease.Pixels, 50); !ok {
		return s, fmt.Errorf("channel has no finite pixels")
	}
	if s.high, ok = composePercentile(lease.Pixels, 99.8); !ok {
		return s, fmt.Errorf("channel high anchor unavailable")
	}
	s.black, s.white, s.background, s.peak = processing.SmartLevels(lease.Pixels)
	if starCores && img != nil {
		white := img.White
		if white <= img.Black {
			white = img.Peak
		}
		stars := processing.ExtractAndLimitStars(lease.Pixels, lease.Width, lease.Height, 4, 5, 64)
		if refAnchors == nil {
			// Reference pass: retain only compact coordinates and the sampled
			// finite core values. The source plane is released on return.
			for _, star := range stars {
				x, y := int(math.Round(star.X)), int(math.Round(star.Y))
				if x < 0 || y < 0 || x >= lease.Width || y >= lease.Height {
					continue
				}
				v := float64(lease.Pixels[y*lease.Width+x])
				if !isFinite64(v) || (white > img.Black && v >= white*0.98) {
					continue
				}
				s.cores = append(s.cores, v)
				star.Flux = v
				s.coreAnchors = append(s.coreAnchors, star)
			}
		} else {
			// Target pass: map target detections into reference coordinates and
			// use the shared catalog matcher. This avoids assuming the two
			// channels have identical dimensions or perfectly coincident stars.
			sx, sy := 1.0, 1.0
			if refWidth > 0 && refHeight > 0 {
				sx = float64(refWidth) / float64(lease.Width)
				sy = float64(refHeight) / float64(lease.Height)
			}
			targetRef := make([]processing.Star, 0, len(stars))
			for _, star := range stars {
				star.X *= sx
				star.Y *= sy
				targetRef = append(targetRef, star)
			}
			pairs := processing.MatchStars(refAnchors, targetRef, 40, 0.02)
			for _, pair := range pairs {
				if len(s.cores) >= 64 {
					break
				}
				x, y := int(math.Round(pair.TargetX/sx)), int(math.Round(pair.TargetY/sy))
				if x < 0 || y < 0 || x >= lease.Width || y >= lease.Height {
					continue
				}
				v := float64(lease.Pixels[y*lease.Width+x])
				if !isFinite64(v) || (white > img.Black && v >= white*0.98) {
					continue
				}
				s.cores = append(s.cores, v)
			}
		}
	}
	sort.Float64s(s.cores)
	return s, nil
}

func applyLargeStretchMatch(dst *models.ChannelState, ref *models.LoadedImage, rs, ts largeStretchSummary) error {
	if dst == nil || ref == nil {
		return fmt.Errorf("missing channel state")
	}
	rh, th := rs.high, ts.high
	if len(rs.cores) >= 5 && len(ts.cores) >= 5 {
		rh, th = composePercentileSorted(rs.cores, 75), composePercentileSorted(ts.cores, 75)
	}
	sp := ref.ScaledPeak
	if !isFinite64(sp) || sp <= 0 {
		sp = 10
	}
	a := composeScaledInput(rs.median, ref.Background, ref.Peak, sp) / sp
	b := composeScaledInput(rh, ref.Background, ref.Peak, sp) / sp
	if !isFinite64(a) || !isFinite64(b) || math.Abs(b-a) < 1e-9 || th <= ts.median {
		dst.Black, dst.White, dst.Background, dst.Peak = ts.black, ts.white, ts.background, ts.peak
	} else {
		denom := (th - ts.median) / (b - a)
		bg, peak := ts.median-a*denom, ts.median-a*denom+denom
		if !isFinite64(bg) || !isFinite64(peak) || peak <= bg {
			dst.Black, dst.White, dst.Background, dst.Peak = ts.black, ts.white, ts.background, ts.peak
		} else {
			dst.Black, dst.White, dst.Background, dst.Peak = bg, peak, bg, peak
		}
	}
	dst.Mode, dst.ScaledPeak, dst.ShowClip = modeToLabel(ref.Mode), sp, ref.ShowClip
	return nil
}

// largeArtifactStars extracts a bounded catalog while materializing only this
// one channel. The catalog is retained; source pixels are released before the
// next channel is opened.
func largeArtifactStars(path string) ([]processing.Star, error) {
	lease, err := fitsio.MaterializeFloat32ArtifactLease(path)
	if err != nil {
		return nil, err
	}
	stars := processing.ExtractAndLimitStars(lease.Pixels, lease.Width, lease.Height, 4.0, 3, 30)
	lease.Release()
	return stars, nil
}

func largeRotateChannel(rt *largeChannelRuntime, idx int, imgs []*models.LoadedImage, refresh func(), onCommit func()) {
	rt.mu.RLock()
	d, ok := rt.artifacts[idx]
	var previewImg models.LoadedImage
	if idx >= 0 && idx < len(imgs) && imgs[idx] != nil {
		previewImg = *imgs[idx]
		previewImg.HDU = imgs[idx].HDU
		previewImg.HDU.Header.Cards = make(map[string]string, len(imgs[idx].HDU.Header.Cards))
		for key, value := range imgs[idx].HDU.Header.Cards {
			previewImg.HDU.Header.Cards[key] = value
		}
		previewImg.HDU.Data.Pixels = nil
		previewImg.HDU.Data.Int32Pixels = nil
	}
	rt.mu.RUnlock()
	if !ok || rt.store == nil {
		return
	}
	go func() {
		rt.jobMu.Lock()
		defer rt.jobMu.Unlock()
		rt.mu.RLock()
		cur, current := rt.artifacts[idx]
		rt.mu.RUnlock()
		if !current || cur.Generation != d.Generation || cur.Path != d.Path {
			fyne.Do(refresh)
			return
		}
		src, err := fitsio.OpenFloat32ArtifactReadOnly(d.Path)
		if err != nil {
			fyne.Do(refresh)
			return
		}
		w, h := src.Width, src.Height
		nd, err := rt.store.ReplaceIfCurrent(d, h, w, func(out *fitsio.Float32Artifact) error {
			row := make([]float32, w)
			dst := make([]float32, h)
			for y := 0; y < w; y++ {
				for x := 0; x < h; x++ {
					if err := src.ReadRow(h-1-x, row); err != nil {
						return err
					}
					dst[x] = row[y]
				}
				if err := out.WriteRow(y, dst); err != nil {
					return err
				}
			}
			return nil
		})
		src.Close()
		if err == nil {
			previewImg.HDU.Data.Width, previewImg.HDU.Data.Height = h, w
			preview, _, _, pErr := composeLargeStretchedPreview(nd.Path, &previewImg)
			err = pErr
			if err == nil {
				rt.mu.Lock()
				cur, current := rt.artifacts[idx]
				if current && cur.Generation == d.Generation {
					rt.artifacts[idx] = nd
					rt.previews[idx] = preview
					imgs[idx].HDU.Data.Width, imgs[idx].HDU.Data.Height = h, w
					imgs[idx].HDU.Data.Pixels = nil
					imgs[idx].Rotation90 = (imgs[idx].Rotation90 + 1) % 4
					clearComposeChannelAlignment(imgs[idx])
				}
				rt.mu.Unlock()
			}
		}
		fyne.Do(func() {
			if err == nil && onCommit != nil {
				onCommit()
			}
			refresh()
		})
	}()
}
