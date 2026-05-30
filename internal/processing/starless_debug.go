package processing

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/export"
	"gofitsv3/internal/fitsio"
)

type StarDebugExportSettings struct {
	Dir     string
	Prefix  string
	Format  export.Format
	Options export.Options
}

func (s StarDebugExportSettings) Validate() error {
	if strings.TrimSpace(s.Dir) == "" {
		return fmt.Errorf("debug export directory must not be empty")
	}
	format := s.Format
	if format == "" {
		format = export.PNG
	}
	switch format {
	case export.PNG, export.JPEG, export.TIFF, export.WEBP:
		return nil
	default:
		return fmt.Errorf("unsupported debug export format %q", s.Format)
	}
}

// DebugImage pairs a display name with a rendered diagnostic image.
type DebugImage struct {
	Name  string
	Image image.Image
}

// BuildStarlessDebugImages returns named diagnostic images from a StarlessResult.
func BuildStarlessDebugImages(result *StarlessResult) ([]DebugImage, error) {
	if result == nil {
		return nil, fmt.Errorf("result must not be nil")
	}
	if result.Width <= 0 || result.Height <= 0 {
		return nil, fmt.Errorf("result dimensions must be > 0")
	}
	total := result.Width * result.Height
	if len(result.DetectionImage) != total || len(result.HardMask) != total ||
		len(result.RawCandidateMask) != total || len(result.AcceptedSeedMask) != total ||
		len(result.RejectedSeedMask) != total || len(result.AlphaMask) != total {
		return nil, fmt.Errorf("result arrays have inconsistent lengths")
	}
	images := []DebugImage{
		{"Detection", float32PlaneDebugImage(result.DetectionImage, result.Width, result.Height, true)},
		{"Candidate Mask", boolMaskDebugImage(result.RawCandidateMask, result.Width, result.Height)},
		{"Accepted Seeds", boolMaskDebugImage(result.AcceptedSeedMask, result.Width, result.Height)},
		{"Rejected Seeds", boolMaskDebugImage(result.RejectedSeedMask, result.Width, result.Height)},
		{"Merged Seeds", boolMaskDebugImage(result.MergedSeedMask, result.Width, result.Height)},
		{"Hard Mask", boolMaskDebugImage(result.HardMask, result.Width, result.Height)},
		{"Alpha Mask", float32PlaneDebugImage(result.AlphaMask, result.Width, result.Height, false)},
	}
	if strings.ToLower(strings.TrimSpace(result.Settings.DetectionPreprocessMode)) == "dog" {
		images = append(images, DebugImage{"High-Pass Detection", float32PlaneDebugImage(preprocessStarSeedDetectionImage(result.DetectionImage, result.SharedValid, result.Width, result.Height, result.Settings.DetectionPreprocessMode), result.Width, result.Height, true)})
	}
	if len(result.RejectedLowProminenceSeedMask) == total {
		images = append(images, DebugImage{"Rejected Low Prominence Seeds", boolMaskDebugImage(result.RejectedLowProminenceSeedMask, result.Width, result.Height)})
	}
	if len(result.RejectedDiffuseSeedMask) == total {
		images = append(images, DebugImage{"Rejected Diffuse Seeds", boolMaskDebugImage(result.RejectedDiffuseSeedMask, result.Width, result.Height)})
	}
	if len(result.MergedSeedMaskBeforeChannelFilter) == total {
		images = append(images, DebugImage{"Merged Seeds Before Channel Filter", boolMaskDebugImage(result.MergedSeedMaskBeforeChannelFilter, result.Width, result.Height)})
	}
	if len(result.MergedSeedMaskAfterChannelFilter) == total {
		images = append(images, DebugImage{"Merged Seeds After Channel Filter", boolMaskDebugImage(result.MergedSeedMaskAfterChannelFilter, result.Width, result.Height)})
	}
	if len(result.RejectedSingleChannelSeedMask) == total {
		images = append(images, DebugImage{"Rejected Single Channel Seeds", boolMaskDebugImage(result.RejectedSingleChannelSeedMask, result.Width, result.Height)})
	}
	if len(result.RejectedSmallFootprintSeedMask) == total {
		images = append(images, DebugImage{"Rejected Small Footprint Seeds", boolMaskDebugImage(result.RejectedSmallFootprintSeedMask, result.Width, result.Height)})
	}
	for i, det := range result.ChannelDetections {
		if len(det.AcceptedSeedMask) == total {
			images = append(images, DebugImage{
				fmt.Sprintf("Accepted Seeds Ch%d", i+1),
				boolMaskDebugImage(det.AcceptedSeedMask, result.Width, result.Height),
			})
		}
		if len(det.HardMask) == total {
			images = append(images, DebugImage{
				fmt.Sprintf("Hard Mask Ch%d", i+1),
				boolMaskDebugImage(det.HardMask, result.Width, result.Height),
			})
		}
		if len(det.RejectedSmallSeedMask) == total {
			images = append(images, DebugImage{
				fmt.Sprintf("Rejected Small Seeds Ch%d", i+1),
				boolMaskDebugImage(det.RejectedSmallSeedMask, result.Width, result.Height),
			})
		}
	}
	for i, ch := range result.Starless {
		if len(ch) == total {
			images = append(images, DebugImage{
				fmt.Sprintf("Starless Ch%d", i+1),
				float32PlaneDebugImage(ch, result.Width, result.Height, true),
			})
		}
	}
	for i, ch := range result.Stars {
		if len(ch) == total {
			images = append(images, DebugImage{
				fmt.Sprintf("Stars Ch%d", i+1),
				float32PlaneDebugImage(alphaGateStarLayer(ch, result.AlphaMask), result.Width, result.Height, true),
			})
		}
	}
	if len(result.Stars) == 3 {
		images = append(images, DebugImage{"Star Luminance", float32PlaneDebugImage(starLuminanceLayer(result.Stars, result.AlphaMask), result.Width, result.Height, true)})
		images = append(images, DebugImage{"Neutral Star RGB", starRGBDebugImage(result.Stars, result.AlphaMask, result.Width, result.Height, 0)})
		images = append(images, DebugImage{"Recombined Star RGB", starRGBDebugImage(result.Stars, result.AlphaMask, result.Width, result.Height, 1)})
	}
	return images, nil
}

func ExportStarlessDebug(result *StarlessResult, settings StarDebugExportSettings) error {
	debuglog.Log("ExportStarlessDebug: starting")
	defer debuglog.Log("ExportStarlessDebug: finished")
	if result == nil {
		return fmt.Errorf("starless result must not be nil")
	}
	if err := settings.Validate(); err != nil {
		return err
	}
	if result.Width <= 0 || result.Height <= 0 {
		return fmt.Errorf("starless result dimensions must be > 0")
	}
	total := result.Width * result.Height
	if len(result.DetectionImage) != total {
		return fmt.Errorf("detection image length = %d, want %d", len(result.DetectionImage), total)
	}
	if len(result.HardMask) != total {
		return fmt.Errorf("hard mask length = %d, want %d", len(result.HardMask), total)
	}
	if len(result.RawCandidateMask) != total {
		return fmt.Errorf("raw candidate mask length = %d, want %d", len(result.RawCandidateMask), total)
	}
	if len(result.AcceptedSeedMask) != total {
		return fmt.Errorf("accepted seed mask length = %d, want %d", len(result.AcceptedSeedMask), total)
	}
	if len(result.RejectedSeedMask) != total {
		return fmt.Errorf("rejected seed mask length = %d, want %d", len(result.RejectedSeedMask), total)
	}
	if len(result.AlphaMask) != total {
		return fmt.Errorf("alpha mask length = %d, want %d", len(result.AlphaMask), total)
	}
	if err := os.MkdirAll(settings.Dir, 0o755); err != nil {
		return err
	}
	debuglog.Log(fmt.Sprintf("ExportStarlessDebug: dir=%s", settings.Dir))
	for i, ch := range result.Starless {
		if len(ch) != total {
			return fmt.Errorf("starless channel %d length = %d, want %d", i, len(ch), total)
		}
	}
	for i, ch := range result.Stars {
		if len(ch) != total {
			return fmt.Errorf("star layer %d length = %d, want %d", i, len(ch), total)
		}
	}

	format := settings.Format
	if format == "" {
		format = export.PNG
	}
	prefix := strings.TrimSpace(settings.Prefix)
	if prefix == "" {
		prefix = "starless"
	}

	write := func(name string, img image.Image) error {
		filename := fmt.Sprintf("%s_%s.%s", prefix, name, string(format))
		path := filepath.Join(settings.Dir, filename)
		return export.FromImage(path, img, format, settings.Options)
	}

	if err := write("detection", float32PlaneDebugImage(result.DetectionImage, result.Width, result.Height, true)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote detection image")
	if err := write("candidate_mask", boolMaskDebugImage(result.RawCandidateMask, result.Width, result.Height)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote raw candidate mask")
	if err := write("accepted_seeds", boolMaskDebugImage(result.AcceptedSeedMask, result.Width, result.Height)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote accepted seed points")
	if err := write("rejected_seeds", boolMaskDebugImage(result.RejectedSeedMask, result.Width, result.Height)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote rejected seed points")
	if strings.ToLower(strings.TrimSpace(result.Settings.DetectionPreprocessMode)) == "dog" {
		if err := write("high_pass_detection", float32PlaneDebugImage(preprocessStarSeedDetectionImage(result.DetectionImage, result.SharedValid, result.Width, result.Height, result.Settings.DetectionPreprocessMode), result.Width, result.Height, true)); err != nil {
			return err
		}
	}
	if len(result.RejectedLowProminenceSeedMask) == total {
		if err := write("rejected_low_prominence_seeds", boolMaskDebugImage(result.RejectedLowProminenceSeedMask, result.Width, result.Height)); err != nil {
			return err
		}
	}
	if len(result.RejectedDiffuseSeedMask) == total {
		if err := write("rejected_diffuse_seeds", boolMaskDebugImage(result.RejectedDiffuseSeedMask, result.Width, result.Height)); err != nil {
			return err
		}
	}
	if len(result.MergedSeedMask) == total {
		if err := write("merged_seeds", boolMaskDebugImage(result.MergedSeedMask, result.Width, result.Height)); err != nil {
			return err
		}
		debuglog.Log("ExportStarlessDebug: wrote merged seed points")
	}
	if len(result.MergedSeedMaskBeforeChannelFilter) == total {
		if err := write("merged_seeds_before_channel_filter", boolMaskDebugImage(result.MergedSeedMaskBeforeChannelFilter, result.Width, result.Height)); err != nil {
			return err
		}
	}
	if len(result.MergedSeedMaskAfterChannelFilter) == total {
		if err := write("merged_seeds_after_channel_filter", boolMaskDebugImage(result.MergedSeedMaskAfterChannelFilter, result.Width, result.Height)); err != nil {
			return err
		}
	}
	if len(result.RejectedSingleChannelSeedMask) == total {
		if err := write("rejected_single_channel_seeds", boolMaskDebugImage(result.RejectedSingleChannelSeedMask, result.Width, result.Height)); err != nil {
			return err
		}
	}
	if len(result.RejectedSmallFootprintSeedMask) == total {
		if err := write("rejected_small_footprint_seeds", boolMaskDebugImage(result.RejectedSmallFootprintSeedMask, result.Width, result.Height)); err != nil {
			return err
		}
	}
	for i, det := range result.ChannelDetections {
		if len(det.AcceptedSeedMask) == total {
			if err := write(fmt.Sprintf("accepted_seeds_ch%d", i+1), boolMaskDebugImage(det.AcceptedSeedMask, result.Width, result.Height)); err != nil {
				return err
			}
		}
		if len(det.HardMask) == total {
			if err := write(fmt.Sprintf("hard_mask_ch%d", i+1), boolMaskDebugImage(det.HardMask, result.Width, result.Height)); err != nil {
				return err
			}
		}
		if len(det.RejectedSmallSeedMask) == total {
			if err := write(fmt.Sprintf("rejected_small_seeds_ch%d", i+1), boolMaskDebugImage(det.RejectedSmallSeedMask, result.Width, result.Height)); err != nil {
				return err
			}
		}
	}
	if err := write("hard_mask", boolMaskDebugImage(result.HardMask, result.Width, result.Height)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote hard mask")
	if err := write("alpha_mask", float32PlaneDebugImage(result.AlphaMask, result.Width, result.Height, false)); err != nil {
		return err
	}
	debuglog.Log("ExportStarlessDebug: wrote alpha mask")
	for i, ch := range result.Starless {
		if err := write(fmt.Sprintf("starless_ch%d", i+1), float32PlaneDebugImage(ch, result.Width, result.Height, true)); err != nil {
			return err
		}
		debuglog.Log(fmt.Sprintf("ExportStarlessDebug: wrote starless channel %d", i+1))
	}
	for i, ch := range result.Stars {
		if err := write(fmt.Sprintf("stars_ch%d", i+1), float32PlaneDebugImage(alphaGateStarLayer(ch, result.AlphaMask), result.Width, result.Height, true)); err != nil {
			return err
		}
		debuglog.Log(fmt.Sprintf("ExportStarlessDebug: wrote star layer %d", i+1))
	}
	if len(result.Stars) == 3 {
		if err := write("star_luminance", float32PlaneDebugImage(starLuminanceLayer(result.Stars, result.AlphaMask), result.Width, result.Height, true)); err != nil {
			return err
		}
		if err := write("recombined_neutral_stars", starRGBDebugImage(result.Stars, result.AlphaMask, result.Width, result.Height, 0)); err != nil {
			return err
		}
		if err := write("recombined_stars", starRGBDebugImage(result.Stars, result.AlphaMask, result.Width, result.Height, 1)); err != nil {
			return err
		}
	}
	return nil
}

func float32PlaneDebugImage(pixels []float32, width, height int, normalize bool) image.Image {
	plane := pixels
	if normalize {
		plane = NormalizeRobust(pixels)
	} else {
		plane = clampFloat32Plane01(pixels)
	}
	return ToGrayRGBA(fitsio.ImageData{Width: width, Height: height, Pixels: plane}, nil)
}

func boolMaskDebugImage(mask []bool, width, height int) image.Image {
	pixels := make([]float32, len(mask))
	for i, v := range mask {
		if v {
			pixels[i] = 1
		}
	}
	return ToGrayRGBA(fitsio.ImageData{Width: width, Height: height, Pixels: pixels}, nil)
}

func alphaGateStarLayer(stars []float32, alpha []float32) []float32 {
	out := make([]float32, len(stars))
	for i, v := range stars {
		if i >= len(alpha) || alpha[i] <= 0 || !isFiniteStar32(v) {
			continue
		}
		out[i] = v * alpha[i]
	}
	return out
}

func starLuminanceLayer(stars [][]float32, alpha []float32) []float32 {
	if len(stars) != 3 {
		return nil
	}
	out := make([]float32, len(stars[0]))
	for i := range out {
		if i >= len(stars[1]) || i >= len(stars[2]) || i >= len(alpha) || alpha[i] <= 0 {
			continue
		}
		lum := maxStarFloat32(stars[0][i], maxStarFloat32(stars[1][i], stars[2][i]))
		out[i] = lum * alpha[i]
	}
	return out
}

func starRGBDebugImage(stars [][]float32, alpha []float32, width, height int, saturation float32) image.Image {
	if len(stars) != 3 || len(stars[0]) == 0 {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	total := len(stars[0])
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := 0; i < total; i++ {
		if i >= len(stars[1]) || i >= len(stars[2]) || i >= len(alpha) || alpha[i] <= 0 {
			continue
		}
		lum := maxStarFloat32(stars[0][i], maxStarFloat32(stars[1][i], stars[2][i]))
		var rgb [3]float32
		for ch := 0; ch < 3; ch++ {
			rgb[ch] = alpha[i] * (lum + saturation*(stars[ch][i]-lum))
		}
		img.SetRGBA(i%width, i/width, color.RGBA{R: debugByte(rgb[0]), G: debugByte(rgb[1]), B: debugByte(rgb[2]), A: 255})
	}
	return img
}

func debugByte(v float32) uint8 {
	if !isFiniteStar32(v) || v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v * 255)
}

func clampFloat32Plane01(pixels []float32) []float32 {
	out := make([]float32, len(pixels))
	for i, v := range pixels {
		if !isFiniteStar32(v) {
			continue
		}
		if v < 0 {
			v = 0
		} else if v > 1 {
			v = 1
		}
		out[i] = v
	}
	return out
}
