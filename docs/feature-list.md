# GoFitsV3 Feature Inventory

Examine supports read-only JWST ASDF 1.0 / Standard 1.6 ImageModel files with embedded, uncompressed 2-D image arrays. The `data`, uncertainty, and DQ planes are available for display with source metadata. Validated JWST MIRI/MIRIMAGE and NIRCam module-B (`NRCB1`-`NRCB4`, `NRCBLONG`) ASDF profiles can also be loaded as Mosaic/drizzle inputs: native GWCS is evaluated per detector pixel for placement and alignment, while the saved mosaic uses a FITS TAN output WCS. Unknown instruments (including future Roman profiles), detectors, or incomplete GWCS models remain Examine-only until explicitly registered; ASDF writing remains unsupported.

This document is a comparison-oriented inventory of features implemented in the GoFitsV3 desktop application as of August 28, 2026. It is organized by what a user is trying to accomplish, rather than by source package. The four primary workspaces are **Mosaic (drizzle)**, **Examine**, **Compose**, and **Edit**.

GoFitsV3 is aimed at producing visually compelling astronomy images from calibrated FITS data, especially Hubble and selected JWST imaging products. It is not presented as a replacement for a mission calibration pipeline or as a photometry/astrometry package. Where an exposed control is disabled, reserved, or subject to an important limitation, that is called out explicitly so this inventory can be used fairly when comparing GoFitsV3 with other programs.

## Drizzle / Mosaic — align and combine calibrated exposures

### Input discovery and frame management

- Loads individual FITS files with `.fits`, `.fit`, or `.fts` extensions.
- Adds every supported FITS file from a selected directory.
- Scans a directory and builds a selectable filter batch using FITS metadata.
- Shows a metadata-only, numbered drizzle-footprint preview in the Add Filter Batch dialog so overlapping source files can be checkbox-selected before loading; invalid or missing WCS inputs remain listed with a warning and no outline.
- Filters batch candidates by filter, proposal ID, exposure time, instrument, observation-date range, and product type.
- Recognizes Hubble `_flt` and `_flc` products and JWST `_cal` products during batch discovery.
- Loads multi-extension FITS images and treats individual `SCI` extensions/chips as drizzle inputs.
- Allows input frames to be reordered, included/excluded, and locked against later offset changes.
- Displays input name, observation date, exposure time, load/processing status, offsets, rotation, and inclusion state.
- Supports per-frame manual X/Y offsets and rotation, with an Apply action and a footprint-flash preview.
- Can use a separate FITS image as a reference-only baseline; its pixels are not combined into the result.
- Can lock the output to the reference baseline's exact size, orientation, origin, and plate scale so separately drizzled filters share a compose-ready grid.
- Allows the reference baseline and all loaded inputs to be cleared independently.

### FITS, detector, WCS, and quality-data handling

- Reads primary and image-extension FITS headers and floating-point image arrays.
- Reads `SCI`, matching `DQ`, `ERR`, and applicable weight data on a per-chip basis.
- Uses WCS for initial placement, including CD/PC matrix geometry and SIP distortion where present.
- Reads HST detector-to-image distortion lookup tables (`D2IMARR`) when available.
- Applies detector-specific DQ masks before processing; HST bad pixels are repaired and unusable JWST pixels are excluded.
- Uses detector metadata and WCS-derived plate scales for output-scale selection.
- Has explicit detector profiles for:
  - HST WFC3/IR and WFC3/UVIS.
  - HST ACS/WFC, ACS/HRC, and ACS/SBC.
  - HST WFPC2 PC/WF multi-chip data.
  - JWST MIRI imaging (`MIRI` / `MIRIMAGE`) and NIRCam module-B imaging (`NIRCAM` / `NRCB1`-`NRCB4`, `NRCBLONG`) when native GWCS is present.
  - JWST NIRCam short-wave detectors NRCA1–4 and NRCB1–4, and long-wave NRCALONG/NRCBLONG.
- Falls back to a conservative generic single-chip profile for unrecognized instruments; this is not the same as explicit instrument support.
- Streams chip data from disk where supported to bound memory use on large datasets.

### Exposure normalization and weighting

- Reviews exposures grouped by filter, including file/extension, `EXPTIME`, `BUNIT`, inclusion, normalization, and scale factor.
- Offers normalization modes:
  - **Off** — keep native units.
  - **Auto** — normalize total-count units when the metadata says normalization is appropriate.
  - **On** — normalize every usable exposure with a valid exposure time.
- Allows normalization and inclusion to be overridden per input.
- Supports drizzle weighting modes:
  - Uniform.
  - Exposure time.
  - `ERR` inverse variance.
- Can normalize mixed-native-scale chips by mapped pixel area to preserve surface brightness, notably for WFPC2 PC/WF data.
- Optionally saves diagnostic mosaic layers alongside the SCI image: accumulated `WHT`, per-pixel `NCONTRIB`, exposure provenance `CTX`, cosmic-ray `CRMASK`, `DQ`, fitted `SKYMODEL`, and `SEAM` planes. The option is off by default so legacy output remains unchanged.

### Automatic and manual registration

- Starts with WCS-projected placement and refines registration using detected star catalogs.
- Uses centroid refinement and robust/RANSAC-style correspondence rejection.
- Offers four alignment modes:
  - TweakReg-style rotation/scale/translation catalog alignment (default).
  - TweakReg-style general six-parameter affine catalog alignment.
  - Legacy rotation/scale/translation alignment.
  - Legacy general affine alignment.
- Configures star-match search radius in arcseconds.
- Configures how many leading images are treated as pre-aligned references; a target can align to whichever reference it overlaps.
- Streams catalogs/pixels in the TweakReg-style modes rather than retaining all full-resolution images.
- For multi-tile external reference baselines, matches each target against reference stars in its projected footprint; if cross-frame consensus over-filters a narrowband catalog, retries with the retained candidate catalog under the same transform-safety checks.
- Shows per-frame alignment results before applying them, including X/Y offset, rotation, and failed matches.
- Provides an alignment diagnostics report for each result: detected/matched/accepted/rejected star counts, X/Y/radial RMS, median and maximum residual, RANSAC inlier percentage, transform decomposition and affine matrix, RScale-versus-affine comparison, residual samples, and warnings for weak, clustered, or overfit solutions.
- Allows successful alignment results to be selectively applied.
- Exports alignment results to CSV.
- Supports manual reference-star selection when automatic matching needs guidance.
- Saves and reloads selected-star sets.
- Saves per-input alignment transforms as sidecars and can merge later alignment updates into them.
- Saves, loads, and clears per-filter offset files; also updates master offset information. Saving/loading offsets currently requires the inputs to share one directory and one filter.
- Provides a two-point Measure mode that calculates X/Y displacement, can snap measurements to detected centroids, and can move selected input frames by the measured offset.
- The report exports both an aggregate alignment CSV and a per-match residual CSV; visual overlays and residual/vector plots consume the same final-transform residual samples.

### Sky and detector-background correction

- Optional AstroDrizzle-style sky subtraction/background matching.
- Sky methods:
  - Local minimum.
  - Global minimum.
  - Relative overlap matching.
  - Global minimum plus overlap matching.
  - Overlap matching plus a relative gradient plane.
- Sky statistics: median, mean, or mode.
- Configurable histogram width, optional lower/upper pixel cutoffs, clipping iterations, and lower/upper sigma limits.
- Can equalize mutually disconnected overlap groups to the darkest group for aesthetic seam reduction; this is explicitly non-photometric.
- Applies detector corrections before sky matching when enabled:
  - NIRCam per-amplifier pedestal removal.
  - NIRCam 1/f row-banding removal with mask sigma and trend-window controls.
  - Optional per-input or directory-based NIRCam row-stat exclusion masks.
  - NIRCam wisp subtraction from local detector/filter template FITS files, with automatic or fixed non-negative scaling.
  - Optional per-input or directory-based MIRI artifact masks.
- Does not download reference templates or perform MIRI shower/snowball correction; those ramp-level corrections are expected to be done upstream.

### Artifact-mask authoring

- Creates **MIRI artifact exclusion** masks and **NIRCam row-stat exclusion** masks inside the application.
- Authors a mask on an individual calibrated input; MIRI masks may alternatively be authored on the current mosaic.
- Provides brush, rectangle, and polygon drawing tools.
- Supports add/erase operations, adjustable brush radius, clear, undo, and redo.
- Can preview and accept/reject threshold-based mask regions and grow or shrink the mask morphology.
- Provides fit/preset/custom zoom and a live masked-pixel count.
- Can project a single-input MIRI mask to other loaded MIRI inputs using WCS.
- Projects a mosaic-authored MIRI mask back into every overlapping detector frame.
- Keeps NIRCam row-stat masks detector-local so their dimensions match the calibrated input.
- Reviews export targets with dimensions and masked-pixel counts, and previews detector-space masks.
- Writes per-input binary FITS masks using `<input-stem>_miri_mask.fits` or `<input-stem>_rowmask.fits` naming.
- Preserves existing mask files unless overwrite is explicitly enabled.
- Stores mask documents and project-relative mask-directory references in Mosaic projects and detects stale mask geometry.

### Drizzle, resampling, rejection, and output

- Builds a WCS-aware mosaic/output canvas from one or more included inputs.
- Supports output scale as either a direct arcseconds-per-pixel value or an output/input scale multiplier.
- Provides plate-scale presets for recognized HST/JWST detectors and an automatic “match finest input” choice.
- Configures `pixfrac` from greater than zero through 1.0.
- Resampling kernels:
  - Square.
  - Point.
  - Turbo.
  - Gaussian.
  - Tophat.
  - Lanczos-2.
  - Lanczos-3.
- Exposes separate “Sep Kernel” and “Final Kernel” selectors.
- **Current limitation:** only the Sep Kernel governs the current drizzle implementation; Final Kernel is stored but reserved for a future two-pass final-combination stage.
- Offers no cosmic-ray rejection or AstroDrizzle-style multi-frame rejection.
- Multi-frame cosmic-ray rejection builds a clean model, blots it back to inputs, uses local noise/`ERR` information, flags and grows candidates, and excludes them from the final drizzle.
- Configures cosmic-ray seed SNR and derivative/sharpness scale.
- Cosmic-ray masks and frames are streamed/disk-backed to reduce peak memory use on large stacks.
- Can save aligned/debug chip FITS files to an optional debug-output directory.
- Can automatically save the full floating-point result as `<filter>_preview.fits` when Save Preview is enabled; this requires inputs from one directory and one filter.
- Shows a stretched mosaic preview, histogram, mean, standard deviation, and image dimensions.
- Preview stretches: Linear, Log, Asinh, Square Root, Histogram Equalization, and MTF.
- Preview level tools: manual background/peak/scaled peak/black/white, Auto Scaling, Auto MTF, and Magic presets (Balanced, Nebula, Galaxy).
- Preview zoom includes fit, fixed percentages from 6% through 400%, custom percentage, and step zoom.
- Saves the completed mosaic as a floating-point FITS image with output WCS metadata.
- Sends the in-memory result directly to Examine.
- Opens a separate blinker over saved debug/aligned FITS outputs.

### Projects and batch drizzle

- Saves and loads Mosaic projects as JSON.
- Project state includes inputs and chip selections, inclusion/lock/normalization state, offsets and affine transforms, reference baseline, drizzle settings, alignment settings, sky/JWST settings, active filter, exposure-normalization mode, and artifact-mask documents.
- Uses project-relative paths for portable mask directories where possible.
- Runs multiple saved Mosaic projects sequentially in a Drizzle Queue.
- Allows per-job alignment to be enabled or disabled.
- Supports adding/removing/reordering jobs, cancelling the current job, or stopping after the current job.
- Shows per-job stage, progress, status, output path, errors, completion summary, and desktop notification.
- Generates per-filter Mosaic projects directly from a directory, using an existing project as the settings/reference template.
- Generated-project discovery can select all, `.flc`, `.flt`, or `.cal` products and selected filters.

## Examine — inspect FITS/ASDF image planes or a drizzle result

### Loading and navigation

- Loads `.fits`, `.fit`, `.fts`, and supported JWST `.asdf` files.
- Lists every supported FITS image HDU and every supported ASDF image plane, with format-neutral stable selection identities.
- Reloads the current file from disk while preserving each plane's stretch settings and the selected plane when possible; decoding runs in the background.
- Receives a Mosaic result directly without an intermediate save/reload.
- Lists every supported image plane and allows switching among FITS extensions or ASDF arrays, including uncertainty and data-quality planes when present.
- Provides fit and percentage zoom, zoom-in/out, scrolling, and image-size-aware display.
- Can flip the displayed image vertically.

### Data inspection

- Displays the selected FITS headers or ASDF source metadata in a dedicated Headers tab.
- Shows the image histogram, mean, and standard deviation.
- Reports the cursor's pixel coordinates.
- Two-click ruler reports start/end coordinates, signed X/Y delta, and Euclidean distance in pixels.
- Clears the current measurement independently.

### Stretch and handoff

- Preview stretches: Linear, Log, Asinh, Square Root, Histogram Equalization, and MTF.
- Manual controls for background, peak, scaled peak, black point, white point, and MTF midtone.
- Optional clipped-pixel display.
- Auto Scaling modeled after FITS Liberator-style level estimation.
- Auto MTF starting-point calculation.
- Magic auto-level presets for Balanced, Nebula, and Galaxy targets, followed by Auto MTF.
- Sends the selected image, chip, and current stretch settings directly to Compose Channel 1, 2, or 3.

## Compose — stretch filters, register channels, and build color

### Channel and filter loading

- Loads one FITS image into each of three base channels labeled Blue, Green, and Red, and uses the same custom FITS file picker for added colored channels.
- Accepts a handoff from Examine into any base channel.
- Loads a filter set from a directory by discovering named `_drz.fits`, `_driz.fits`, and `_drizzle.fits` products.
- In filter-set loading, assigns each discovered filter to Blue, Green, Red, or a custom-colored overlay, and applies a selected Magic preset plus Auto MTF.
- Supports up to 16 additional custom-colored FITS overlay layers beyond the three base channels.
- Saves any base channel or overlay as a stretched grayscale image.
- Views each base channel's FITS headers and saves those headers as text.
- Clears channels, resets the complete Compose state, or resets pixel data to undo alignment and cleaning.

### Per-channel stretch and positioning

- Independent stretch modes per base channel and overlay:
  - Linear.
  - Logarithmic.
  - Asinh, with softening control.
  - Square Root.
  - Histogram Equalization.
  - MTF, with midtone control.
  - GHS (Generalised Hyperbolic Stretch), with strength, local parameter, and symmetry/stretch-point controls.
- Independent background, peak, scaled peak, black point, and white point.
- Numeric level fields support precise decimal entry and reject malformed values while typing.
- Optional lock between black/background and between white/peak controls.
- Optional clipped-pixel display.
- Per-channel Auto Scaling, Auto MTF, and Magic (Balanced, Nebula, Galaxy).
- Applies Magic plus Auto MTF to all currently loaded base and overlay channels in one operation.
- Copies Channel 1 stretch/position settings to Channels 2 and 3.
- Matches one channel's stretch to a selected reference channel, optionally using star-core anchors while ignoring saturated cores.
- Supports manual X/Y translation and rotation for each base channel.
- Rotates a base channel 90 degrees clockwise.
- Provides pixel-value readout and image-region median pickers for black and white points.
- Measures each base channel's stellar PSF/FWHM, suggests a common target, and non-destructively convolves sharper channels to match it. Normal in-memory Compose includes optional saturated-core protection and a visual before/after representative-star preview.

### Registration, cleaning, and color combination

- Aligns Channels 1 and 3 to Channel 2 using pixel-space star detection/matching and a full affine fit; it does not trust cross-filter WCS agreement for this operation.
- Applies manual X/Y/rotation adjustments on top of automatic alignment.
- Normalizes channel scales relative to Channel 2 for a balanced starting point.
- Cross-channel cleaning identifies defects present in only one color channel, builds a protective star mask, and replaces isolated cosmic-ray/hot-pixel remnants while preserving real stars.
- Builds a live RGB color composite from the three base channels, with optional simultaneous multi-channel mixing when additional filters are loaded.
- Offers Auto, Weighted multi-channel, and Artistic overlays composition modes; Auto uses weighted mixing when four or more sources are loaded while retaining artistic behavior for three-filter sets.
- Provides editable non-negative RGB contribution weights for every loaded base channel and overlay. Weights persist by stable overlay identity and filter-set custom colors seed their initial values.
- Provides an editable wideband cross-mix preset (default 8%, bounded to 0–50%) that blends each blue/green/red base filter into neighboring color outputs while preserving overlay weights and selecting explicit Weighted mode.
- Applies per-channel RGB output levels in a separate levels window.
- Supports optional LRGB-style combination with a dedicated luminance FITS input or synthetic luminance from selected RGB filters, adjustable luminance contribution, and chrominance-only smoothing that preserves luminance detail. Dedicated-L and LRGB settings persist in Compose projects. Disk-backed Compose rejects enabled LRGB with an actionable error until bounded LRGB processing is available, preventing a silently different render.
- Adds custom-colored layers with independently adjustable RGB tint, opacity, and highlight protection; overlays are blended into the composite.
- Displays a color legend describing base filters and active overlay colors.
- Measures the composite with a two-point pixel ruler.

### Comparison and large-data workflow

- Shows individual channel previews, per-channel histograms/statistics, and the combined composite in a four-pane layout.
- Supports a shared histogram scale across filters or per-filter automatic histogram scaling.
- Can maximize one preview and restore the four-pane layout.
- Blinks any chosen set of two or more base channels and colored overlays and allows the blink set to be changed.
- Offers an opt-in disk-backed large-file mode that streams processing artifacts instead of retaining full channel arrays in memory, including weighted multi-channel mixing with bounded row processing.
- **Current limitation:** Blink is disabled in disk-backed large-file mode.
- Can disable live composite construction to reduce processing load while adjusting channels.

### Projects and output

- Saves and loads Compose projects, including base-channel files, stretch settings, transforms, RGB levels, overlay layers, colors/opacity/highlight protection, composition mode, and stable per-source weighted-mix settings.
- Exports the composed RGB image directly.
- Sends the composite directly to Edit.
- Direct Compose and Edit exports support:
  - PNG at 8-bit or 16-bit depth.
  - JPEG with quality from 1–100.
  - Deflate-compressed TIFF.
  - Lossless WebP.
- Disk-backed Compose can stream full-resolution RGB output to export without first materializing the complete image in RAM.

## Edit — final raster-image adjustments and export

### Input and viewing

- Receives a color composite directly from Compose.
- Loads PNG, JPEG, and TIFF images from disk.
- Fit, fixed 10%–400%, custom, and step zoom with scrollable image viewing.
- Displays separate red, green, and blue histograms with level markers.
- Reset restores the last loaded/received/committed image and resets adjustment controls.

### Adjustments and repair tools

- Per-channel RGB input levels with independent minimum and maximum sliders.
- Interactive tone curves for All/luminance, Red, Green, and Blue.
- Unsharp-mask sharpening with adjustable strength and Gaussian radius.
- Color/dark-speck cleaner for small single-color remnants and near-black dropout dots, with maximum blob size and intensity controls.
- Clone/heal tool with selectable source point, click/drag destination painting, adjustable brush size, and one-level undo (`Ctrl+Z` or button).
- Interactive rectangular crop tool; an applied crop becomes the new edit base.
- Adjustment Apply combines levels, curves, and sharpening without cumulatively reapplying them on each preview.

### Export and large-data limitation

- Saves PNG (8- or 16-bit), JPEG (quality control), TIFF (Deflate), or lossless WebP.
- Disk-backed Compose results retain a bounded Edit preview (maximum 1600×1600), while Levels, Curves, Sharpen, Clean, Crop, and Clone/heal adjustments are recorded and replayed tile-by-tile against the full-resolution source during Apply or export.
- Disk-backed Edit exports stream the full-resolution adjusted result without materializing the complete image in memory.
- **Current limitation:** Edit can export WebP but its file-open dialog does not load WebP.

## Shared utilities and operational features

- Batch-resizes FITS files by power-of-two block averaging, with file selection, output-size preview, explicit edge-pixel discard notice, progress, and cancellation.
- Stores the last-used directory and relevant display/workflow preferences.
- Runs expensive loading, alignment, drizzle, Compose, cleaning, resize, and export preparation work with progress dialogs and cancellation where implemented.
- Shows current application memory usage.
- Provides an in-app debug log viewer with clear and text-export actions.
- Uses a desktop GUI on Windows, Linux, and macOS through Fyne; the application also contains Windows-specific maximize behavior.

## Important comparison boundaries

- The primary processing inputs are calibrated FITS products; GoFitsV3 does not claim to perform raw detector calibration such as bias, dark, flat-field, ramp fitting, or a complete HST/JWST calibration pipeline.
- The program is optimized for aesthetic image production. Some options, especially disconnected-background equalization and manual/artifact cleanup, are intentionally non-photometric.
- Cosmic-ray rejection is a multi-exposure drizzle feature and works best with comparable overlapping frames; it is not a general single-image cosmic-ray removal pipeline.
- Explicit detector support is concentrated on the HST and JWST imagers listed above. Generic FITS files may load, but instrument-specific scale, chip, distortion, and DQ behavior is not guaranteed.
- The application provides image statistics, headers, pixel coordinates, and pixel-distance measurements, but does not claim catalog querying, source photometry, astrometric solving, spectral analysis, deconvolution, or scientific uncertainty propagation through final Edit output.
- Project files are local JSON files; no cloud catalog, collaboration, or remote archive integration is implemented in the desktop workflow.

## Inventory evidence

This inventory was cross-checked against the current UI entry points, settings dialogs, processing models, and export paths, principally:

- `internal/ui/workspace_mosaic.go` and the `mosaic_*` UI files.
- `internal/ui/workspace_examine.go`.
- `internal/ui/workspace_compose.go` and Compose support dialogs.
- `internal/ui/workspace_edit.go`.
- `internal/ui/drizzle_settings_window.go`, `alignment_settings_window.go`, `skysub_settings_window.go`, `exposure_review_window.go`, and `artifact_mask_editor.go`.
- `internal/mosaic`, `internal/processing`, `internal/fitsio`, `internal/instrument`, and `internal/export`.

Planning or roadmap documents were not treated as proof of an implemented feature.
