# GoFitsV3

GoFitsV3 is a Go-based desktop application for turning astronomical FITS data into visually compelling images. The project is primarily focused on processing Hubble-style FITS data (FLT and FLC files), aligning exposures, combining frames, stretching image data, and exporting finished images for presentation.

The goal of this project is practical image processing for astronomy enthusiasts: preserve enough of the scientific structure of the data to make good decisions, while providing tools that make it easier to create finished “pretty picture” images.

> **Project status:** Active development. APIs, processing behavior, and UI workflows may change.

---

## Features

* Load and inspect FITS image data
* Work with Hubble-style calibrated exposures, including FLT and FLC files
* Recognize common HST detectors (WFC3/IR, WFC3/UVIS, ACS/WFC, ACS/HRC, ACS/SBC, WFPC2) with per-detector pixel scales, SIP distortion, and data-quality bad-pixel masks
* Combine multi-chip / multi-extension science data where applicable
* Align images using WCS positioning and star-based refinement, with RANSAC matching to reject bad correspondences
* Support multiple alignment models: translation-only, rotation/scale, and full affine transforms
* Drizzle/mosaic image stacks into a combined output, with memory-bounded streaming for large stacks
* Configure drizzle options such as scale, pixfrac, and kernel (square, point, turbo, Gaussian, tophat, Lanczos-2, Lanczos-3)
* Reject cosmic rays across multiple exposures with an AstroDrizzle-style detector that uses the ERR (error) plane as a per-pixel noise model
* Apply image stretches: linear, logarithmic, square-root, asinh, MTF (midtones transfer function), and GHS (Generalised Hyperbolic Stretch)
* Use auto-scaling / smart-level tools to create usable starting points for display
* Process separate filter images into RGB color composites
* Adjust per-channel balance and apply cross-channel cleaning for aesthetic RGB output
* Detect and reduce hot pixels / localized defects via data-quality flags and badpix handling
* Save and load Compose projects and mosaic alignment offsets
* Export finished images as PNG (8- or 16-bit), JPEG, TIFF, or lossless WebP
* Desktop UI built with Go and Fyne

---

## What GoFitsV3 Is For

GoFitsV3 is intended for users who want to process space-telescope or astronomy FITS files into finished images. It is especially useful when working with multiple exposures or multiple filters that need to be aligned, combined, stretched, and balanced.

Typical use cases include:

* Creating RGB composites from Hubble filter data
* Combining multiple exposures from a visit or observation set
* Drizzling images into a cleaner, higher-quality output frame
* Experimenting with different stretch and color-balance settings
* Creating presentation-ready astronomy images from FITS data

---

## What GoFitsV3 Is Not

GoFitsV3 is not intended to replace professional scientific calibration pipelines such as AstroDrizzle, DrizzlePac, STScI pipeline tools, or mission-specific reduction software.

The project is designed around practical and aesthetic image production. Some processing choices may favor visual alignment, clean output, or artistic presentation over strict scientific measurement accuracy.

Use professional astronomy tools when the output must be scientifically calibrated, publication-grade, or suitable for photometry/astrometry.

---

## Requirements

Exact requirements may vary by platform and build target, but the project is generally expected to require:

* Go 1.26 or newer
* A supported desktop operating system:

  * Windows
  * Linux
  * macOS
* Fyne desktop dependencies for the target platform

For Fyne setup details, see the official Fyne documentation for your operating system.

---

## Building

Clone the repository:

```bash
git clone https://github.com/wierdling/gofitsv3.git
cd gofitsv3
```

Download dependencies:

```bash
go mod download
```

Build the application:

```bash
go build ./...
```

Run the application (the runnable package lives under `cmd`):

```bash
go run ./cmd
```

---

## Testing

Run all tests:

```bash
go test ./...
```

Run tests with verbose output:

```bash
go test -v ./...
```

---

## Workspaces

The application is organized into four tabs, each covering one stage of the workflow:

* **Mosaic** — Add FITS inputs, set a baseline reference frame, star-align the inputs (with RANSAC matching), measure alignment quality, and drizzle/combine the aligned exposures into a mosaic ("Create Mosaic"). Includes a blink viewer for inspecting frames and tools to save/load alignment offsets. The combined result can be sent directly to the Examine tab.

* **Examine** — Inspect a single FITS image or a drizzle result, including histogram and basic statistics. The Mosaic tab can hand its output here for review.

* **Compose** — Assign filter images to R/G/B channels, stretch each channel independently, and merge them into an RGB composite. Includes "Align to Channel 2" (pixel-space star alignment with a full affine fit), cross-channel cleaning, RGB levels, scale normalization, and Compose project save/load. The composite can be sent to the Edit tab.

* **Edit** — Final touch-up of the composited image before export.

---

## FITS and Hubble Data Notes

Many Hubble FITS files (FLT/FLC) contain multiple science extensions. In these cases, the correct extensions usually correspond to separate detector chips or image sections that must be interpreted together. GoFitsV3 recognizes the common HST instrument/detector combinations and applies the appropriate pixel scale, chip count, SIP distortion handling, and data-quality bad-pixel bitmask:

* WFC3/IR, WFC3/UVIS
* ACS/WFC, ACS/HRC, ACS/SBC
* WFPC2 (PC and WF chips)

Users should still inspect the file metadata and verify that the correct image extensions are being used.

Important FITS metadata may include:

* `SCI` extensions
* WCS headers (including SIP distortion coefficients)
* filter names
* exposure time
* detector/chip identifiers
* data quality (`DQ`) arrays
* error (`ERR`) arrays, where available

---

## Alignment Notes

Image alignment is one of the most important parts of producing clean astronomy composites. GoFitsV3 includes alignment workflows intended to handle common astronomy image-processing cases.

Alignment concepts in use include:

* WCS-based initial positioning (mosaic drizzle)
* Star centroid detection and refinement
* Translation-only alignment
* Rotation/scale alignment
* Full affine transforms, including scale and skew
* RANSAC-style matching for rejecting bad star correspondences
* Spatially distributed star catalogs and a global affine refinement pass, so the fit is well constrained across the whole frame rather than anchored by a bright cluster

The Mosaic tab uses WCS for initial placement and refines with star matching. The Compose tab's "Align to Channel 2" works directly in pixel space (independent of channel WCS headers, which can disagree with the true pixel registration), fitting and applying a full affine transform with a manual nudge available on top.

For single-visit Hubble data, rotation/scale alignment may often be sufficient. For wider mosaics, edge overlaps, or images with stronger distortion differences, a full affine model may be required.

Always inspect stars at high zoom after alignment. Color fringing, offset red/green/blue pixels, or repeated star cores usually indicate imperfect alignment, filter-specific PSF differences, or both.

---

## Drizzle Notes

The drizzle workflow is used to combine aligned images into a final output grid. Useful settings include:

* **Scale**: controls the output pixel scale relative to the input
* **Pixfrac**: controls the size of the drizzle “drop”
* **Kernel**: controls how each input pixel's flux is distributed into the output image — available kernels are square, point, turbo, Gaussian, tophat, Lanczos-2, and Lanczos-3
* **Cosmic-ray settings**: reject transient artifacts when multiple comparable exposures are available

Smaller pixfrac values can produce sharper output but may create holes or uneven coverage if there are too few exposures. Larger pixfrac values are more forgiving but can soften detail.

Cosmic-ray rejection uses an AstroDrizzle-style multi-frame approach: a clean model image is built across the aligned exposures, blotted back to each input frame, and pixels that exceed a per-pixel noise floor are flagged and grown. When an ERR (error) plane is available it is used as the per-pixel noise model, so detection thresholds scale with local signal. For large stacks, frames and cosmic-ray masks are streamed via temporary files to keep memory use bounded.

---

## Stretching and Display

Astronomical image data usually has a much wider dynamic range than a normal display image. GoFitsV3 includes stretch functions to map FITS data into a visible range.

Available stretch modes:

* **Linear**: simple and predictable, but often poor for faint nebulae or galaxies
* **Log**: compresses bright areas and reveals faint structure
* **Square root**: moderate compression with a natural-looking transition
* **Asinh**: useful for astronomy images because it can preserve color while compressing highlights
* **MTF** (midtones transfer function): a PixInsight STF-style stretch driven by a midtone parameter
* **GHS** (Generalised Hyperbolic Stretch): a flexible stretch (Payne "normal" form, ported from Siril's `STRETCH_PAYNE_NORMAL`) driven by strength, local intensity, and symmetry parameters

For color composites, asinh, MTF, or GHS stretching is often a good starting point because each can reduce blown-out stars while preserving faint detail.

---

## Color Composite Notes

When combining filters into RGB images, the brightest filter is not always the best visual match for its assigned channel. Narrowband filters can easily dominate a color channel, especially around stars.

Common strategies include:

* Reduce the gain of the dominant channel
* Stretch luminance and color separately
* Use star-specific color balancing
* Apply channel weights before the final RGB merge
* Use a synthetic luminance layer for detail
* Avoid clipping any single channel too early

For example, when using filters such as `F502N`, `F656N`, and `F658N`, the red channel may need to be reduced or stretched differently to prevent red stars from overpowering the image.

---

## Repository Structure

```text
GoFitsV3/
├── cmd/                    # Application entry point (main.go)
├── internal/
│   ├── badpix/             # Bad-pixel / hot-pixel handling
│   ├── config/             # Configuration
│   ├── debuglog/           # Debug logging
│   ├── debugtime/          # Timing instrumentation
│   ├── export/             # Image export (PNG/JPEG/TIFF/WebP)
│   ├── fitsio/             # FITS reading/writing
│   ├── histogram/          # Histogram and image statistics
│   ├── instrument/         # HST detector definitions (scale, chips, DQ, SIP)
│   ├── models/             # Shared data structures and settings
│   ├── mosaic/             # Drizzle, mosaic, and cosmic-ray logic
│   ├── processing/         # Alignment, stretch, filtering, image processing
│   ├── render/             # Rendering helpers
│   ├── stretch/            # Stretch functions (linear/log/sqrt/asinh/MTF/GHS)
│   ├── ui/                 # Fyne UI workspaces (Mosaic/Examine/Compose/Edit)
│   ├── utils/              # Shared utilities
│   └── version/            # Version information
├── webpwriter/             # WebP encoding helper
├── go.mod
├── go.sum
└── README.md
```

---

## Development Notes

This project emphasizes:

* Clear, maintainable Go code
* Deterministic image-processing behavior where practical
* Parallel processing for large images where useful
* Conservative, bounded memory use for large FITS datasets
* UI workflows that make visual inspection practical
* Processing steps that are understandable and adjustable

When changing alignment or drizzle behavior, test against multiple datasets. A change that improves one mosaic can easily make another dataset worse.

---

## Performance Notes

Astronomy images can be very large, especially when processing multi-extension FITS files or drizzle outputs. Some operations may be CPU- and memory-intensive.

Performance-sensitive areas include:

* FITS loading
* WCS and SIP transforms
* star detection and matching
* drizzle accumulation
* interpolation/resampling
* stretch preview generation
* image export (including WebP)

Where practical, GoFitsV3 uses parallel processing and disk-backed streaming to improve throughput and bound memory on multi-core systems.

---

## Known Limitations

Current or expected limitations may include:

* Some workflows assume Hubble-like FITS structure
* Scientific calibration is not the primary goal
* Alignment quality depends heavily on WCS quality and star matching
* Drizzle quality depends on overlap, exposure count, and input sampling
* Cosmic-ray rejection works best with multiple comparable exposures
* Some visual defects may require manual adjustment or masking
* Large datasets may still require significant memory

---

## Roadmap Ideas

Possible future improvements:

* More robust FITS extension selection
* Better automatic WCS/chip handling
* Improved star matching for small-overlap images
* Improved smart-level / auto-stretch behavior
* Separate luminance and chrominance workflows
* Batch processing
* Additional export formats
* Better preview caching for large images

---

## Contributing

Contributions, experiments, and issue reports are welcome if this repository is made public.

Useful contributions include:

* Bug reports with sample FITS headers or reproduction steps
* Alignment test cases
* Performance improvements
* UI workflow improvements
* Documentation corrections
* Additional export options
* Safer handling of unusual FITS files

When reporting image-processing issues, include:

* Input file type
* Telescope/instrument, if known
* Filters involved
* Approximate workflow used
* Screenshots of the artifact or alignment problem
* Any relevant console output or error messages

---

## License

```text
MIT License
```

## Acknowledgments

GoFitsV3 is inspired by astronomy image-processing workflows used with FITS data, Hubble observations, drizzle-based image combination, and the broader astrophotography community. The GHS stretch is ported from Siril's Generalised Hyperbolic Stretch implementation.

This project is not affiliated with NASA, ESA, STScI, or the Hubble Space Telescope program unless explicitly stated elsewhere.

---

## Disclaimer

GoFitsV3 is provided as-is. Verify output carefully before relying on it for scientific, technical, or archival purposes.
