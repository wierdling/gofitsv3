// genicon generates a simple PNG icon for GoFitsV3.
// Run: go run ./cmd/genicon/
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))

	// Background: deep space dark blue
	bg := color.RGBA{R: 10, G: 20, B: 50, A: 255}
	draw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, draw.Src)

	// Draw a simple circle (telescope aperture / lens)
	cx, cy := size/2, size/2
	outerR := 110
	innerR := 80
	ringColor := color.RGBA{R: 70, G: 150, B: 220, A: 255}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			if d2 <= outerR*outerR && d2 >= innerR*innerR {
				img.Set(x, y, ringColor)
			}
		}
	}

	// Draw a bright star (cross pattern) in center
	starColor := color.RGBA{R: 240, G: 220, B: 120, A: 255}
	// horizontal and vertical arms
	for i := -30; i <= 30; i++ {
		bright := 255 - int(float64(abs(i))/30.0*200)
		c := color.RGBA{R: uint8(bright), G: uint8(bright * 220 / 255), B: uint8(bright * 100 / 255), A: 255}
		// horizontal
		if cx+i >= 0 && cx+i < size {
			img.Set(cx+i, cy, c)
			img.Set(cx+i, cy-1, dimColor(c, 0.5))
			img.Set(cx+i, cy+1, dimColor(c, 0.5))
		}
		// vertical
		if cy+i >= 0 && cy+i < size {
			img.Set(cx, cy+i, c)
			img.Set(cx-1, cy+i, dimColor(c, 0.5))
			img.Set(cx+1, cy+i, dimColor(c, 0.5))
		}
	}

	// Diagonal arms (thinner)
	for i := -20; i <= 20; i++ {
		bright := 200 - int(float64(abs(i))/20.0*200)
		c := color.RGBA{R: uint8(bright), G: uint8(bright * 220 / 255), B: uint8(bright * 100 / 255), A: 255}
		if cx+i >= 0 && cx+i < size && cy+i >= 0 && cy+i < size {
			img.Set(cx+i, cy+i, c)
			img.Set(cx+i, cy-i, c)
		}
	}

	// Small scattered stars
	stars := [][2]int{
		{40, 40}, {200, 60}, {80, 180}, {210, 190},
		{150, 30}, {30, 140}, {220, 130}, {160, 210},
	}
	for _, s := range stars {
		img.Set(s[0], s[1], starColor)
		img.Set(s[0]+1, s[1], dimColor(starColor, 0.4))
		img.Set(s[0]-1, s[1], dimColor(starColor, 0.4))
		img.Set(s[0], s[1]+1, dimColor(starColor, 0.4))
		img.Set(s[0], s[1]-1, dimColor(starColor, 0.4))
	}

	f, err := os.Create("internal/ui/assets/icon.png")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		panic(err)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func dimColor(c color.RGBA, factor float64) color.RGBA {
	return color.RGBA{
		R: uint8(float64(c.R) * factor),
		G: uint8(float64(c.G) * factor),
		B: uint8(float64(c.B) * factor),
		A: c.A,
	}
}
