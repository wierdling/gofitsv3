// Command starstretch previews gentler stellar stretching without modifying FITS data.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"gofitsv3/internal/models"
	"gofitsv3/internal/starstretchpreview"
	"gofitsv3/internal/stretch"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	input := flag.String("input", "", "original linear primary-image mosaic FITS (required)")
	starMap := flag.String("map", "", "reviewed star map; default working/<mosaic>_starmap.fits")
	out := flag.String("output", "", "new report directory (required; must not already exist)")
	mode := flag.String("mode", "asinh", "scalar stretch: linear, log, asinh, sqrt, mtf")
	strength := flag.Float64("strength", .5, "gentler stellar stretch strength, 0..1")
	limit := flag.Int("limit", 8, "number of brightest accepted stars to inspect, 1..24")
	id := flag.Int("star", 0, "one accepted catalog source ID; 0 selects brightest stars")
	black := flag.Float64("background", 0, "stretch background (requires -peak)")
	peak := flag.Float64("peak", 0, "stretch peak; omit to use automatic source levels")
	scale := flag.Float64("scaled-peak", 10, "normal stretch scaled peak")
	beta := flag.Float64("asinh-scale", 1, "asinh softening scale")
	mid := flag.Float64("midtone", .25, "MTF midtone in (0,1)")
	flag.Parse()
	if *input == "" || *out == "" {
		return fmt.Errorf("-input and -output are required")
	}
	modes := map[string]stretch.Mode{"linear": stretch.Linear, "log": stretch.Log, "asinh": stretch.Asinh, "sqrt": stretch.Sqrt, "mtf": stretch.MTF}
	m, ok := modes[*mode]
	if !ok {
		return fmt.Errorf("unsupported stretch mode %q", *mode)
	}
	peakProvided, backgroundProvided := false, false
	flag.Visit(func(f *flag.Flag) {
		peakProvided = peakProvided || f.Name == "peak"
		backgroundProvided = backgroundProvided || f.Name == "background"
	})
	if backgroundProvided && !peakProvided {
		return fmt.Errorf("-background requires -peak")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	report, err := starstretchpreview.Generate(ctx, starstretchpreview.Request{
		SciencePath: *input, MapPath: *starMap, Strength: *strength, Limit: *limit, SourceID: *id,
		AutoLevels: !peakProvided,
		Metadata:   models.LoadedImage{Mode: m, Background: *black, Peak: *peak, ScaledPeak: *scale, AsinhScale: *beta, MTFMidtone: *mid},
	}, func(stage string, done, total int) { fmt.Fprintf(os.Stderr, "%s (%d/%d)\n", stage, done, total) })
	if err != nil {
		return err
	}
	if err := starstretchpreview.Save(ctx, *out, report); err != nil {
		return err
	}
	fmt.Printf("%s\n%s/index.html\n", report.Summary, *out)
	return nil
}
