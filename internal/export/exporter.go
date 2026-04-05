package export

import (
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"os"

	"golang.org/x/image/tiff"
)

// Format enumerates supported export formats.
type Format string

const (
	PNG  Format = "png"
	JPEG Format = "jpeg"
	TIFF Format = "tiff"
)

// Options controls export behavior.
type Options struct {
	Quality int // for JPEG
}

// FromRGBABytes saves an RGBA byte buffer (len = w*h*4).
func FromRGBABytes(path string, buf []byte, width, height int, format Format, opt Options) error {
	if len(buf) != width*height*4 {
		return errors.New("buffer length mismatch")
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	copy(img.Pix, buf)
	return saveImage(path, img, format, opt)
}

// FromImage saves any Go image using the requested format.
func FromImage(path string, img image.Image, format Format, opt Options) error {
	return saveImage(path, img, format, opt)
}

func saveImage(path string, img image.Image, format Format, opt Options) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	switch format {
	case PNG:
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		return enc.Encode(f, img)
	case JPEG:
		q := opt.Quality
		if q == 0 {
			q = 90
		}
		return jpeg.Encode(f, img, &jpeg.Options{Quality: q})
	case TIFF:
		return tiff.Encode(f, img, &tiff.Options{Compression: tiff.Deflate, Predictor: true})
	default:
		return errors.New("unsupported format")
	}
}
