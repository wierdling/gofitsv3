// Command starmap creates conservative FITS star masks from linear mosaics.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/mosaic"
	"gofitsv3/internal/processing"
	"gofitsv3/internal/starbench"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) > 1 && os.Args[1] == "benchmark" {
		return starbench.RunCLI(os.Args[2:])
	}
	input := flag.String("input", "", "linear mosaic FITS (required)")
	output := flag.String("output", "", "output FITS; default beside input in working/")
	originals := flag.String("originals", "", "comma-separated original WFC3/UVIS FLT/FLC paths")
	reference := flag.String("reference", "", "alignment reference FITS for original working sidecars")
	snr := flag.Float64("snr", 7, "minimum local contrast")
	res := flag.Float64("residual", .22, "maximum normalized profile residual")
	flag.Parse()
	if *input == "" {
		return fmt.Errorf("-input is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	start := time.Now()
	f, err := fitsio.LoadFile(*input)
	if err != nil {
		return err
	}
	h := f.HDUs[0]
	if h.Data.Width == 0 {
		return fmt.Errorf("select a primary-image mosaic")
	}
	var ev *mosaic.StarMapEvidence
	if *originals != "" {
		refPath := *reference
		if refPath == "" {
			refPath = *input
		}
		refPath, err = filepath.Abs(refPath)
		if err != nil {
			return err
		}
		ev, err = mosaic.LoadStarMapEvidence(h.Data, h.Header, strings.Split(*originals, ","), refPath)
		if err != nil {
			return err
		}

	}
	opt := processing.DefaultStarMapOptions()
	opt.MinSNR = *snr
	opt.MaxResidual = *res
	last := ""
	opt.Progress = func(stage string, done, total int) {
		if stage != last {
			fmt.Println(stage)
			last = stage
		}
	}
	p, err := mosaic.CreateStarMap(ctx, h.Data, h.Header, ev, opt)
	if err != nil {
		return err
	}
	out := *output
	if out == "" {
		out = mosaic.StarMapWorkingPath(*input)
	}
	if err = mosaic.SaveStarMapFITS(ctx, out, p, h.Data.Pixels); err != nil {
		return err
	}
	// JSON is a developer review companion; the authoritative catalog is in FITS.
	data, err := json.MarshalIndent(p.Map.Sources, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(strings.TrimSuffix(out, filepath.Ext(out))+"_catalog.json", data, 0644); err != nil {
		return err
	}
	n := 0
	for _, s := range p.Map.Sources {
		if s.Accepted() {
			n++
		}
	}
	fmt.Printf("%s: %d accepted, %d uncertain; FWHM %.2f; %s; elapsed %s\n", out, n, len(p.Map.Sources)-n, p.Map.FWHM, p.EvidenceMode, time.Since(start).Round(time.Millisecond))
	return nil
}
