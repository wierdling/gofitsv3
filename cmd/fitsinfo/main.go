// Command fitsinfo prints the HDU layout, key header cards, WCS summary and
// optional pixel statistics of a FITS file. It replaces ad hoc header-dump
// programs when diagnosing drizzle, alignment and compose problems.
//
//	go run ./cmd/fitsinfo file.fits            # HDU table + key cards per HDU
//	go run ./cmd/fitsinfo -all file.fits       # every card, sorted, per HDU
//	go run ./cmd/fitsinfo -hdu 1 -stats f.fits # pixel statistics of one HDU
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
)

// keyCards are printed for every HDU when present, in this order.
var keyCards = []string{
	"SIMPLE", "XTENSION", "EXTNAME", "EXTVER", "BITPIX", "NAXIS", "NAXIS1", "NAXIS2",
	"BUNIT", "BSCALE", "BZERO",
	"TELESCOP", "INSTRUME", "DETECTOR", "APERTURE", "FILTER", "FILTER1", "FILTER2", "PUPIL",
	"EXPTIME", "EFFEXPTM", "DATE-OBS", "TIME-OBS", "PROPOSID", "PROGRAM", "TARGNAME", "ROOTNAME", "FILENAME",
	"PHOTFLAM", "PHOTPLAM", "PHOTMJSR", "PIXAR_SR", "PHOTFNU",
	"CCDAMP", "CCDGAIN", "NGOODPIX", "GOODMIN", "GOODMAX", "GOODMEAN", "MDRIZSKY", "SKYSUB",
	"WCSAXES", "CTYPE1", "CTYPE2", "CRVAL1", "CRVAL2", "CRPIX1", "CRPIX2",
	"CD1_1", "CD1_2", "CD2_1", "CD2_2", "PC1_1", "PC1_2", "PC2_1", "PC2_2", "CDELT1", "CDELT2",
	"ORIENTAT", "PA_V3", "A_ORDER", "B_ORDER", "WCSNAME", "RADESYS", "EQUINOX",
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fitsinfo:", err)
		os.Exit(1)
	}
}

func run() error {
	all := flag.Bool("all", false, "print every header card (sorted) for each HDU")
	hdu := flag.Int("hdu", -1, "restrict output to this HDU index (0-based)")
	stats := flag.Bool("stats", false, "decode pixels and print statistics (use with -hdu to limit decoding)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: fitsinfo [-all] [-hdu N] [-stats] file.fits [more.fits ...]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		return fmt.Errorf("no input file")
	}
	for i, path := range flag.Args() {
		if i > 0 {
			fmt.Println()
		}
		if err := describe(path, *all, *hdu, *stats); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func describe(path string, all bool, only int, stats bool) error {
	var (
		f   *fitsio.File
		err error
	)
	if stats {
		f, err = fitsio.LoadFileSelectiveIndexed(path, func(index int, _ fitsio.Header) bool {
			return only < 0 || index == only
		})
	} else {
		f, err = fitsio.LoadFileMetadata(path)
	}
	if err != nil {
		return err
	}
	fmt.Printf("%s\n", path)
	fmt.Printf("%-4s %-10s %-4s %-7s %-11s %s\n", "HDU", "EXTNAME", "VER", "BITPIX", "SIZE", "FILTER/BUNIT")
	for i, h := range f.HDUs {
		size := "-"
		if h.Data.Width > 0 || h.Data.Height > 0 {
			size = fmt.Sprintf("%dx%d", h.Data.Width, h.Data.Height)
		}
		extra := strings.TrimSpace(fitsio.FilterString(h.Header) + " " + card(h.Header, "BUNIT"))
		fmt.Printf("%-4d %-10s %-4s %-7s %-11s %s\n", i, nonEmpty(h.ExtName, card(h.Header, "EXTNAME")),
			card(h.Header, "EXTVER"), card(h.Header, "BITPIX"), size, extra)
	}
	for i, h := range f.HDUs {
		if only >= 0 && i != only {
			continue
		}
		fmt.Printf("\n[HDU %d]\n", i)
		if all {
			printAllCards(h.Header)
		} else {
			printKeyCards(h.Header)
		}
		printWCS(h.Header)
		if stats {
			printStats(h.Data)
		}
	}
	return nil
}

func card(h fitsio.Header, key string) string {
	return fitsio.HeaderString(h, key)
}

func nonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func printKeyCards(h fitsio.Header) {
	for _, k := range keyCards {
		if v := card(h, k); v != "" {
			fmt.Printf("  %-8s = %s\n", k, v)
		}
	}
}

func printAllCards(h fitsio.Header) {
	keys := make([]string, 0, len(h.Cards))
	for k := range h.Cards {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Printf("  %-8s = %s\n", k, strings.TrimSpace(h.Cards[k]))
	}
}

// printWCS derives plate scale and rotation from the CD or PC/CDELT matrix.
func printWCS(h fitsio.Header) {
	if card(h, "CTYPE1") == "" {
		return // distortion lookup tables and non-image HDUs carry no celestial WCS
	}
	cd11, ok1 := num(h, "CD1_1")
	cd12, _ := num(h, "CD1_2")
	cd21, _ := num(h, "CD2_1")
	cd22, ok4 := num(h, "CD2_2")
	if !ok1 || !ok4 {
		pc11, p1 := num(h, "PC1_1")
		pc22, p4 := num(h, "PC2_2")
		d1, c1 := num(h, "CDELT1")
		d2, c2 := num(h, "CDELT2")
		if !(p1 && p4 && c1 && c2) {
			if c1 && c2 {
				fmt.Printf("  WCS: CDELT %.4g\" x %.4g\" /px (no rotation matrix)\n", d1*3600, d2*3600)
			}
			return
		}
		pc12, _ := num(h, "PC1_2")
		pc21, _ := num(h, "PC2_1")
		cd11, cd12, cd21, cd22 = pc11*d1, pc12*d1, pc21*d2, pc22*d2
	}
	sx := math.Hypot(cd11, cd21) * 3600
	sy := math.Hypot(cd12, cd22) * 3600
	rot := math.Atan2(cd21, cd11) * 180 / math.Pi
	det := cd11*cd22 - cd12*cd21
	parity := "normal"
	if det > 0 {
		parity = "flipped"
	}
	sip := ""
	if card(h, "A_ORDER") != "" {
		sip = ", SIP distortion present"
	}
	fmt.Printf("  WCS: scale %.4f\" x %.4f\" /px, rotation %.2f deg, parity %s%s\n", sx, sy, rot, parity, sip)
}

func num(h fitsio.Header, key string) (float64, bool) {
	v := card(h, key)
	if v == "" {
		return 0, false
	}
	x, err := strconv.ParseFloat(strings.ReplaceAll(v, "D", "E"), 64)
	return x, err == nil
}

// printStats reports finite-pixel statistics on at most ~2M strided samples.
func printStats(d fitsio.ImageData) {
	if len(d.Pixels) == 0 {
		fmt.Println("  stats: no pixel data decoded")
		return
	}
	const maxSamples = 2_000_000
	stride := len(d.Pixels)/maxSamples + 1
	vals := make([]float64, 0, len(d.Pixels)/stride+1)
	nonFinite, zeros := 0, 0
	var sum float64
	for i := 0; i < len(d.Pixels); i += stride {
		v := float64(d.Pixels[i])
		if math.IsNaN(v) || math.IsInf(v, 0) {
			nonFinite++
			continue
		}
		if v == 0 {
			zeros++
		}
		vals = append(vals, v)
		sum += v
	}
	if len(vals) == 0 {
		fmt.Printf("  stats: all %d sampled pixels non-finite\n", nonFinite)
		return
	}
	sort.Float64s(vals)
	median := vals[len(vals)/2]
	dev := make([]float64, len(vals))
	for i, v := range vals {
		dev[i] = math.Abs(v - median)
	}
	sort.Float64s(dev)
	mad := dev[len(dev)/2] * 1.4826
	pct := func(p float64) float64 { return vals[int(float64(len(vals)-1)*p)] }
	fmt.Printf("  stats (stride %d, %d samples): min %.6g  p1 %.6g  median %.6g  p99 %.6g  max %.6g\n",
		stride, len(vals), vals[0], pct(0.01), median, pct(0.99), vals[len(vals)-1])
	fmt.Printf("         mean %.6g  robust sigma (MAD) %.6g  zeros %d  non-finite %d\n",
		sum/float64(len(vals)), mad, zeros, nonFinite)
}
