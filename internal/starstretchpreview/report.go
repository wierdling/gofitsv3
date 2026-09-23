// Package starstretchpreview provides source-grid diagnostics for gentler stellar stretching.
// It never changes science images, star maps, or live Compose data.
package starstretchpreview

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/models"
)

type Request struct {
	SciencePath, MapPath string
	Metadata             models.LoadedImage
	AutoLevels           bool
	Strength             float64
	Limit, SourceID      int
}

type StarPreview struct {
	SourceID                             int
	CutoutX, CutoutY, Width, Height      int // zero-based original science coordinates
	Status, Reason                       string
	Normal, Gentle, Mask                 image.Image    `json:"-"`
	Diagnostics                          any            `json:"diagnostics,omitempty"`
	RadialProfile                        []RadialSample `json:"radialProfile,omitempty"`
	NormalProfilePath, GentleProfilePath string         `json:"-"`
}

type RadialSample struct {
	Radius               float64
	Samples              int
	Normal, Gentle, Mask float64
}

type Report struct {
	SciencePath, MapPath, Summary string
	Settings                      string
	Strength                      float64
	// Metadata contains scalar settings only, never science pixels or headers.
	Metadata models.LoadedImage
	Stars    []StarPreview
}

// Save writes a new report directory and never replaces an existing one.
// index.html is written last: its presence marks a successfully completed report.
func Save(ctx context.Context, directory string, report *Report) error {
	if report == nil || len(report.Stars) == 0 {
		return fmt.Errorf("empty star-stretch preview")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0755); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0755); err != nil {
		return fmt.Errorf("create new preview directory: %w", err)
	}
	for i, star := range report.Stars {
		for j, im := range []image.Image{star.Normal, star.Gentle, star.Mask} {
			if err := ctx.Err(); err != nil {
				return err
			}
			if im == nil {
				continue
			}
			name := filepath.Join(directory, fmt.Sprintf("star-%d-%d.png", i, j))
			if err := writeNew(name, func(f *os.File) error { return png.Encode(f, im) }); err != nil {
				return err
			}
		}
	}
	if err := writeNew(filepath.Join(directory, "report.json"), func(f *os.File) error {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	tmp := filepath.Join(directory, "index.html.tmp")
	if err := writeNew(tmp, func(f *os.File) error { return reportTemplate.Execute(f, report) }); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(directory, "index.html"))
}

func writeNew(path string, write func(*os.File) error) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func grayImage(pixels []float32, width, height int) image.Image {
	im := image.NewGray(image.Rect(0, 0, width, height))
	for i, v := range pixels {
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			f = 0
		}
		im.SetGray(i%width, i/width, color.Gray{Y: uint8(math.Round(255 * math.Max(0, math.Min(1, f))))})
	}
	return im
}

func addRadialProfiles(preview *StarPreview, normal, gentle, mask []float32, w, h int, cx, cy, outer float64) {
	n := int(math.Ceil(math.Min(256, math.Max(8, outer+3))))
	bins := make([]RadialSample, n)
	for i := range bins {
		bins[i].Radius = float64(i) + .5
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			bin := int(math.Hypot(float64(x)-cx, float64(y)-cy))
			if bin >= n {
				continue
			}
			i := y*w + x
			bins[bin].Samples++
			bins[bin].Normal += float64(normal[i])
			bins[bin].Gentle += float64(gentle[i])
			bins[bin].Mask += float64(mask[i])
		}
	}
	peak := 1e-12
	for i := range bins {
		if bins[i].Samples > 0 {
			d := float64(bins[i].Samples)
			bins[i].Normal /= d
			bins[i].Gentle /= d
			bins[i].Mask /= d
			peak = math.Max(peak, math.Max(bins[i].Normal, bins[i].Gentle))
		}
	}
	var a, b strings.Builder
	command := "M"
	for i, bin := range bins {
		if bin.Samples == 0 {
			command = "M"
			continue
		}
		x := 5 + 350*float64(i)/float64(n-1)
		fmt.Fprintf(&a, "%s%.2f %.2f ", command, x, 115-110*bin.Normal/peak)
		fmt.Fprintf(&b, "%s%.2f %.2f ", command, x, 115-110*bin.Gentle/peak)
		command = "L"
	}
	preview.RadialProfile = bins
	preview.NormalProfilePath, preview.GentleProfilePath = a.String(), b.String()
}

var reportTemplate = template.Must(template.New("report").Parse(`<!doctype html>
<html lang="en"><meta charset="utf-8"><title>Gentler star stretch — preview</title>
<style>body{font:16px system-ui;background:#181a20;color:#eee;margin:2rem;max-width:1100px}p{line-height:1.5}section{border-top:1px solid #555;padding:1rem 0}.panels{display:flex;gap:1rem;flex-wrap:wrap}figure{margin:0}img{width:256px;height:256px;object-fit:contain;image-rendering:pixelated;background:#000}figcaption{margin:.5rem 0}code{overflow-wrap:anywhere}.note{color:#c5cbd7}</style>
<h1>Gentler star stretch: source-grid preview</h1>
<p>{{.Summary}}</p><p>Source: <code>{{.SciencePath}}</code><br>Map: <code>{{.MapPath}}</code><br>{{.Settings}}<br>Strength: {{.Strength}}</p>
<p class="note">Original-file, single-filter cutouts using one fixed display scale. Normal / gentler / treatment mask. These are not independently auto-scaled. Click an image to inspect its full-resolution PNG. This diagnostic does not include Compose alignment, rotation, cleaning, PSF matching, RGB mixing or levels. No science data or star maps were changed. See report.json for fit and stretch settings.</p>
{{range $i, $s := .Stars}}<section><h2>Star {{$s.SourceID}} — {{$s.Status}}</h2><p>{{$s.Reason}}</p><div class="panels">
{{if $s.Normal}}<figure><a href="star-{{$i}}-0.png"><img src="star-{{$i}}-0.png" alt="Normal stellar stretch"></a><figcaption>Normal</figcaption></figure>{{end}}
{{if $s.Gentle}}<figure><a href="star-{{$i}}-1.png"><img src="star-{{$i}}-1.png" alt="Gentler stellar stretch"></a><figcaption>Gentler</figcaption></figure>{{end}}
{{if $s.Mask}}<figure><a href="star-{{$i}}-2.png"><img src="star-{{$i}}-2.png" alt="Treatment mask"></a><figcaption>Treatment mask</figcaption></figure>{{end}}
</div>{{if $s.NormalProfilePath}}<p>Radial mean intensity: red = normal, white = gentler. Both curves use the same vertical scale; radius increases to the right. Numeric samples are in report.json.</p><svg viewBox="0 0 360 120" width="360" height="120" role="img" aria-label="Normal and gentler radial profiles"><path d="{{$s.NormalProfilePath}}" fill="none" stroke="#ff6688" stroke-width="2"/><path d="{{$s.GentleProfilePath}}" fill="none" stroke="white" stroke-width="2"/></svg>{{end}}</section>{{end}}</html>`))
