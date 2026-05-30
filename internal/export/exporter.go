package export

import (
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"

	"gofitsv3/webpwriter"
	"golang.org/x/image/tiff"
)

// Format enumerates supported export formats.
type Format string

const (
	PNG  Format = "png"
	JPEG Format = "jpeg"
	TIFF Format = "tiff"
	WEBP Format = "webp"
)

// Options controls export behavior.
type Options struct {
	Quality  int // for JPEG
	BitDepth int // 8 (default) or 16; only meaningful for PNG
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

// FromFloat32Channels saves per-channel float32 pixel data (values in [0,1]).
// When format is PNG and opt.BitDepth is 16, writes a 16-bit PNG (NRGBA64).
// Otherwise converts to 8-bit RGBA and delegates to FromRGBABytes.
func FromFloat32Channels(path string, r, g, b []float32, width, height int, format Format, opt Options) error {
	n := width * height
	if len(r) != n || len(g) != n || len(b) != n {
		return errors.New("channel length mismatch")
	}
	if format == PNG && opt.BitDepth == 16 {
		img := image.NewNRGBA64(image.Rect(0, 0, width, height))
		for i := 0; i < n; i++ {
			rv := clampToUint16(r[i])
			gv := clampToUint16(g[i])
			bv := clampToUint16(b[i])
			img.SetNRGBA64(i%width, i/width, color.NRGBA64{R: rv, G: gv, B: bv, A: 0xffff})
		}
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		defer f.Close()
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		return enc.Encode(f, img)
	}
	// Fall back to 8-bit path.
	buf := make([]byte, n*4)
	for i := 0; i < n; i++ {
		buf[i*4+0] = uint8(clampToUint16(r[i]) >> 8)
		buf[i*4+1] = uint8(clampToUint16(g[i]) >> 8)
		buf[i*4+2] = uint8(clampToUint16(b[i]) >> 8)
		buf[i*4+3] = 0xff
	}
	return FromRGBABytes(path, buf, width, height, format, opt)
}

func clampToUint16(v float32) uint16 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 0xffff
	}
	return uint16(v * 0xffff)
}

func saveImage(path string, img image.Image, format Format, opt Options) error {
	if format == WEBP {
		return webpwriter.WriteImageWebPLosslessFile(path, img)
	}

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
