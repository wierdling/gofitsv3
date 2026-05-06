# GoFitsV3

GoFitsV3 is a Go-based desktop application for turning astronomical FITS data into visually compelling images. The project is primarily focused on processing Hubble-style FITS/FLC data, aligning exposures, combining frames, stretching image data, and exporting finished images for presentation.

The goal of this project is practical image processing for astronomy enthusiasts: preserve enough of the scientific structure of the data to make good decisions, while providing tools that make it easier to create finished “pretty picture” images.

> **Project status:** Active development. APIs, processing behavior, and UI workflows may change.

---

## Features

* Load and inspect FITS image data
* Work with Hubble-style calibrated exposures, including FLC files
* Combine image chips/extensions where applicable
* Align images using WCS and star-based refinement workflows
* Support multiple alignment models, including shift, rotation/scale, and affine-style transforms
* Drizzle/mosaic image stacks into a combined output
* Configure drizzle options such as scale, pixfrac, kernels, and cosmic-ray handling
* Apply image stretches including linear, logarithmic, square-root, histogram equalization, and asinh-style stretches
* Use auto-scaling / smart-level style tools to create usable starting points for display
* Process separate filter images into color composites
* Adjust filter balance for aesthetic RGB output
* Detect and reduce small hot pixels / localized defects where possible
* Export finished images, including WebP-oriented workflows
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

* Go 1.22 or newer
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
git clone https://github.com/<your-user-or-org>/GoFitsV3.git
cd GoFitsV3
```

Download dependencies:

```bash
go mod download
```

Build the application:

```bash
go build ./...
```

Run the application:

```bash
go run .
```

Depending on the final repository layout, the runnable package may be under a command directory such as `cmd/gofitsv3`:

```bash
go run ./cmd/gofitsv3
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

## Basic Workflow

A typical workflow looks like this:

1. **Load FITS/FLC files**
   Open calibrated astronomy files and inspect the available image extensions.

2. **Prepare channels or exposures**
   Select the image data to use, combine chips/extensions if needed, and group files by filter or exposure set.

3. **Align images**
   Use WCS information where available, then refine alignment with star matching or manual inspection.

4. **Drizzle / combine**
   Combine aligned exposures into a mosaic or stacked image using drizzle settings appropriate for the dataset.

5. **Stretch the data**
   Apply linear, logarithmic, asinh, square-root, or other stretch modes to bring out faint structure.

6. **Balance filters**
   Assign filters to RGB channels and adjust channel strength to avoid color dominance, clipped stars, or unnatural balance.

7. **Clean defects**
   Use available masking, hot-pixel handling, or local smoothing tools to reduce small artifacts.

8. **Export**
   Save the finished image in a presentation-friendly format.

---

## FITS and Hubble Data Notes

Many Hubble FITS/FLC files contain multiple science extensions. In these cases, the correct extensions usually correspond to separate detector chips or image sections that must be interpreted together.

GoFitsV3 is being developed with these kinds of files in mind, but users should still inspect the file metadata and verify that the correct image extensions are being used.

Important FITS metadata may include:

* `SCI` extensions
* WCS headers
* filter names
* exposure time
* detector/chip identifiers
* data quality information
* error arrays, where available

---

## Alignment Notes

Image alignment is one of the most important parts of producing clean astronomy composites. GoFitsV3 includes alignment workflows intended to handle common astronomy image-processing cases.

Supported or planned alignment concepts include:

* WCS-based initial positioning
* Star centroid refinement
* Translation-only alignment
* Rotation/scale alignment
* Full affine-style transforms where needed
* RANSAC-style matching for rejecting bad star correspondences

For single-visit Hubble data, rotation/scale alignment may often be sufficient. For wider mosaics, edge overlaps, or images with stronger distortion differences, a more flexible model may be required.

Always inspect stars at high zoom after alignment. Color fringing, offset red/green/blue pixels, or repeated star cores usually indicate imperfect alignment, filter-specific PSF differences, or both.

---

## Drizzle Notes

The drizzle workflow is used to combine aligned images into a final output grid. Useful settings may include:

* **Final scale**: controls the output pixel scale
* **Scale**: controls the input-to-output scale relationship
* **Pixfrac**: controls the size of the drizzle “drop”
* **Kernel**: controls how pixels are distributed into the output image
* **Cosmic-ray settings**: help reject transient artifacts when multiple exposures are available

Smaller pixfrac values can produce sharper output but may create holes or uneven coverage if there are too few exposures. Larger pixfrac values are more forgiving but can soften detail.

---

## Stretching and Display

Astronomical image data usually has a much wider dynamic range than a normal display image. GoFitsV3 includes stretch functions to map FITS data into a visible range.

Common stretch types include:

* **Linear**: simple and predictable, but often poor for faint nebulae or galaxies
* **Log**: compresses bright areas and reveals faint structure
* **Asinh**: useful for astronomy images because it can preserve color while compressing highlights
* **Square root**: moderate compression with a natural-looking transition
* **Histogram equalization**: can reveal structure, but may produce unnatural contrast

For color composites, asinh-style stretching is often a good starting point because it can reduce blown-out stars while preserving faint details.

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

The exact repository layout may change, but the project may include packages similar to:

```text
GoFitsV3/
├── cmd/                    # Application entry points, if used
├── internal/
│   ├── models/             # Shared data structures and settings
│   ├── mosaic/             # Drizzle and mosaic logic
│   ├── processing/         # Alignment, stretch, filtering, and image processing
│   └── ui/                 # Fyne UI components
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
* Conservative memory use for large FITS datasets
* UI workflows that make visual inspection practical
* Processing steps that are understandable and adjustable

When changing alignment or drizzle behavior, test against multiple datasets. A change that improves one mosaic can easily make another dataset worse.

---

## Performance Notes

Astronomy images can be very large, especially when processing multi-extension FITS files or drizzle outputs. Some operations may be CPU- and memory-intensive.

Performance-sensitive areas include:

* FITS loading
* WCS transforms
* star detection and matching
* drizzle accumulation
* interpolation/resampling
* stretch preview generation
* WebP or other compressed image export

Where practical, GoFitsV3 uses parallel processing to improve throughput on multi-core systems.

---

## Known Limitations

Current or expected limitations may include:

* Some workflows may assume Hubble-like FITS structure
* Scientific calibration is not the primary goal
* Alignment quality depends heavily on WCS quality and star matching
* Drizzle quality depends on overlap, exposure count, and input sampling
* Cosmic-ray rejection works best with multiple comparable exposures
* Some visual defects may require manual adjustment or masking
* Large datasets may require significant memory

---

## Roadmap Ideas

Possible future improvements:

* More robust FITS extension selection
* Better automatic WCS/chip handling
* Improved star matching for small-overlap images
* Additional drizzle kernels
* Better cosmic-ray and hot-pixel detection
* Improved smart-level / auto-stretch behavior
* Separate luminance and chrominance workflows
* Batch processing
* More export formats
* Project/session files
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

Add the project license here.

Example:

```text
MIT License
```

or

```text
All rights reserved.
```

---

## Acknowledgments

GoFitsV3 is inspired by astronomy image-processing workflows used with FITS data, Hubble observations, drizzle-based image combination, and the broader astrophotography community.

This project is not affiliated with NASA, ESA, STScI, or the Hubble Space Telescope program unless explicitly stated elsewhere.

---

## Disclaimer

GoFitsV3 is provided as-is. Verify output carefully before relying on it for scientific, technical, or archival purposes.
