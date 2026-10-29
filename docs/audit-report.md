# GoFitsV3 Architecture & Code-Quality Audit

- **Scope:** whole repository at `develop` @ `d70f43a` (375 Go files, ~98k lines; `internal/ui` ~31k, `internal/processing` ~16.6k, `internal/mosaic` ~12.2k non-test lines).
- **Mode:** read-only. No source was changed. This file is the only addition.
- **Line numbers** refer to `d70f43a` and will drift as the code changes.

## Method and tooling results

| Check | Result |
|---|---|
| `go vet` on all non-UI packages | Clean. |
| `go vet ./...` (incl. `internal/ui`, `cmd`) | Could not run in the audit container (no GL/X11 headers for cgo). |
| `go test -race ./internal/{fitsio,stretch,histogram,mosaic,processing}/...` | All pass with **0 data races reported**. These tests do not exercise the UI goroutines, which is where the races below live. |
| CI (`.github/workflows/build.yml`) | Only runs `fyne package`. **No `go test`, `go vet` or `-race` step** (see H9). |

Most findings came from manual review: grep for concurrency, error and allocation patterns, then range reads of the hot paths. Every finding below was confirmed against the source. Speculative items were dropped.

### Deliberate designs, not flagged as defects

- **Per-frame EXPTIME normalization** is kept separate from `WeightingMode` in `prepareFramePixels` (`internal/mosaic`). This is intentional. M5 and H5 only concern *how often* this logic is evaluated, not what it does.
- **Compose alignment** matches stars in pixel space rather than via WCS. This is intentional.
- **The mosaic pipeline streams inputs from disk** to bound memory. This is intentional. H3 (release frame planes after use) is a consequence of that design and is correct in itself. Only the seam diagnostic that reads the planes afterwards is wrong.
- **`workspace_mosaic.go` is mid-refactor** into `mosaicWorkspace`. Its duplicated closures are listed as refactor-completion work (L8), not as defects.
- **`diskComposeSem`** (`processing/disk_compose.go:63`) serializes disk compose calls across the process. This looks intentional.

### Summary

| Severity | Count | Themes |
|---|---|---|
| Critical | 2 | FITS scaling keywords ignored; Compose render goroutine races on workspace state |
| High | 9 | Uncancellable/unguarded background jobs, silent data corruption on write and export paths, FITS HDU sizing, per-pixel hot-path waste, no test gate in CI |
| Medium | 16 | Duplication (stretch, bilinear, worker pools, medians, save dialogs), globals, `models` → fyne coupling, swallowed errors, leaked goroutines |
| Low | 12 | Hand-rolled builtins, dead code, small allocations, doc drift |

---

## Critical

### C1. FITS reader ignores `BZERO` / `BSCALE`, so integer images get wrong pixel values

- **Where:**
  - `internal/fitsio/fitsio.go:297-355` (`readImage`)
  - `internal/fitsio/stream.go:352-367` (`decodeRow`)
- **Problem:** `grep -rn "BZERO\|BSCALE" internal/` matches only a keyword-strip list in `internal/mosaic/drizzle.go:2676`. No decoder applies the scaling. The FITS standard stores unsigned 16-bit data as `BITPIX=16, BZERO=32768`, which is the norm for most ground-based CCD/CMOS cameras and many archive products. Such images decode as signed values: bright pixels become negative. Any `BSCALE≠1` product is silently in the wrong units.
- **Why it matters:** every downstream stage (stretch, star extraction, sky subtraction, drizzle weighting) runs on corrupted data, and nothing reports an error. Float (`BITPIX=-32/-64`) JWST/HST products are unaffected, which may be why this has gone unnoticed.

```go
// before (decodeRow)
case 16:
    dst[i] = float32(int16(binary.BigEndian.Uint16(raw[i*2:])))

// after — parse once per HDU, apply per sample (skip the FMA when bscale==1 && bzero==0)
scale := headerFloatOr(hdr, "BSCALE", 1)
zero  := headerFloatOr(hdr, "BZERO", 0)
case 16:
    dst[i] = float32(float64(int16(binary.BigEndian.Uint16(raw[i*2:])))*scale + zero)
```

When `BLANK` is present for integer BITPIX, map those samples to `NaN` in the same pass.

### C2. The Compose render goroutine reads mutable workspace state that worker and UI goroutines write

- **Where:**
  - Render goroutine: `internal/ui/compose_render.go:131,155` → `composeRGB` (`:221-360`).
  - Unlocked reads inside it:
    - `ws.overlayLayers` and `layer.win` (`:318`)
    - `ws.psfSettings` (`:313`)
    - `ws.compositionMode`, `ws.mixWeights` (`:331`)
    - `ws.largeMode` (`:117,222`)
    - widget-backed offsets via `composeChannelOffsetFields` → `NumberEntry.value` (`compose_align_ui.go:30-33`)
  - Writers:
    - The channel-load worker writes `ws.imgs` and `ws.origPixels` off the UI thread in in-memory mode (`compose_channels.go:78-80,91`). Only the disk branch takes `largeMu`.
    - The project-load worker assigns and clears `ws.imgs[i]` and map entries off-thread (`compose_project.go:307-343`).
    - Apply/Auto mutate the shared `*LoadedImage` in place while an older render generation may still hold the same pointer (`compose_channel_controls.go:257-297`; `renderImage` returns `ws.imgs[idx]` itself at `compose_render.go:404-405`).
- **Problem:** these are unsynchronized concurrent reads and writes of slices, maps and struct fields. The render path also discards the UI-thread snapshot taken at `compose_render.go:67` and calls `ws.renderImages()` again (`:312`), which repeats the warp work.
- **Why it matters:**
  - Torn reads can produce a composite from mixed channel and stretch states.
  - A concurrent write to a Go map while it is being read is a fatal runtime error that `recover` cannot catch.
  - In-memory channel loads that overlap in one slot have no generation check, so the older file can win.
  - The race tests don't cover `internal/ui`, so none of this is caught today.

```go
// before (inside render goroutine)
renderedSources := ws.renderImages()
dx, dy, rot, _ := ws.composeChannelOffsetFields(i)
if ws.psfSettings.Enabled { ... }

// after — build an immutable snapshot on the UI thread, pass it in
type composeRenderInput struct {
    imgs     []*models.LoadedImage   // copy-on-write: Apply/Auto replace the pointer, never mutate
    offsets  [][3]float64
    psf      models.PSFSettings
    mode     models.ComposeMode
    weights  []models.ComposeMixWeight
    overlays []overlaySnapshot
    large    bool
}
in := ws.snapshotRenderInput()          // UI thread
go func() { data, err := buildComposePreview(ctx, in); fyne.Do(func() { ws.applyIfCurrent(seq, data, err) }) }()

// workers publish only on the UI thread, behind a per-slot generation
fyne.Do(func() {
    if ws.loadGen[idx] != gen { return }
    replaceComposeChannelImage(ws.imgs, idx, img)
    clearComposeOrigPixels(&ws.origPixels, idx)
})

// Apply/Auto
next := *ws.imgs[idx]; next.Black = ...; ws.imgs[idx] = &next
```

---

## High

### H1. About 25 progress dialogs can be dismissed without cancelling the job, and their results are applied without a check

- **Where:** `dialog.NewCustom(title, "Please wait...", widget.NewProgressBarInfinite(), win)` appears at:
  - `internal/ui/compose_align_ui.go:56,161`
  - `compose_channels.go:38,128`
  - `compose_stretch_ui.go` (4 sites)
  - `workspace_edit.go` (6 sites)
  - `workspace_examine.go`
  - `workspace_mosaic.go:576`
  - others (`grep -n NewProgressBarInfinite internal/ui`)
- **Problem:** the second argument of `dialog.NewCustom` is the label of a *dismiss button*, so "Please wait..." is a button that hides the dialog while the goroutine keeps running. The in-memory Compose alignment (`compose_align_ui.go:161-200`) has no context and no generation check. If the user dismisses it and clears or reloads a channel, the result is written into whatever now occupies `ws.imgs[idx]`, and `ws.imgs[idx].HDU` panics if that slot is now nil.
- **Related:** two paths in `compose_channels.go` leave the progress dialog on screen:
  - `:68-71`: the load-error path calls `progressDialog.Hide()` and `dialog.ShowError` directly on the worker goroutine, which is off-thread UI under Fyne ≥2.6.
  - `:74-77,84-88`: the "superseded" returns never hide the dialog at all.
- **Fix:** `internal/ui/progress_dialog.go` already provides `newProgressTracker`, which has a real Cancel button and a ctx. Mosaic uses it; Compose and Edit should too.

```go
// before
progressDialog := dialog.NewCustom("Aligning", "Please wait...", widget.NewProgressBarInfinite(), ws.win)
go func() {
    back := align(...)
    fyne.Do(func() { progressDialog.Hide(); setChannelAlignTransform(ws.imgs[idx], back) })
}()

// after
ctx, cancel := context.WithCancel(ws.ctx)
pt  := newProgressTrackerWithContextOnUI("Aligning", "Matching stars…", ws.win, ctx, cancel)
gen := ws.bumpAlignGen()
go func() {
    defer cancel()
    back, err := align(ctx, ...)
    fyne.Do(func() {
        pt.hide()
        if ctx.Err() != nil || gen != ws.alignGen || ws.imgs[idx] == nil { return }
        if err != nil { dialog.ShowError(err, ws.win); return }
        setChannelAlignTransform(ws.imgs[idx], back)
    })
}()
```

### H2. Automatic mosaic star alignment ignores cancellation and drops frame-load errors

- **Where:**
  - `internal/mosaic/drizzle.go:1543`
  - `internal/mosaic/frame_loader.go:35`
  - `internal/mosaic/input_loader.go:281`
- **Problem:** `AlignInputsByStarsWithMode` receives `prog.Ctx` but runs its most expensive phase (load every frame, extract stars) with a fresh context and throws the error away:

  ```go
  candidateCatalogs, _ := extractStarCatalogsForAlignmentCapsCtx(context.Background(), inputs, caps)
  ```

  Cancel therefore has no effect until all frames are processed. A frame that fails to load (I/O error, corrupt file) becomes a nil catalog and later shows up as a vague "no match". The sibling `AlignInputsBySelectedStarsWithModeCtx` (`:2257`) does this correctly. Its primary worker pool (`drizzle.go:1859-1885`) also reports progress only after `wg.Wait()`, so the UI shows 0/N for the whole pass.

```go
// before
candidateCatalogs, _ := extractStarCatalogsForAlignmentCapsCtx(context.Background(), inputs, caps)

// after
candidateCatalogs, err := extractStarCatalogsForAlignmentCapsCtx(prog.Ctx, inputs, caps)
if err != nil {
    if errors.Is(err, context.Canceled) { return nil, ErrCancelled }
    return nil, fmt.Errorf("extract alignment catalogs: %w", err)
}
```

### H3. Written files are published without checking `Close` or calling `Sync`, so truncated outputs and caches look valid

- **Where:**
  - `internal/fitsio/write.go:38-58`: `defer f.Close()` after `bw.Flush()`. This is the main FITS writer, used by `mosaic.SaveResultFITS` (`drizzle.go:2473`), `exposure_combine.go:263-282` and `artifact_mask_service.go:194-205,264-275`.
  - `internal/fitsio/stream.go:923` (`WriteNormalizedFloat32Artifact`)
  - `internal/export/exporter.go:91,131`: PNG and other export writers. A failed encode also leaves a partial file at `path`.
  - `internal/processing/disk_compose.go:395,493,577,866`: `defer a.Close()` on writable artifacts that `publishDiskArtifacts` then renames into place.
  - `internal/starbench/project.go:68-69`: lock-file write and close both ignored.
- **Problem:** on a full disk, on network filesystems, and with delayed-allocation filesystems, write errors often surface only at `Close`. These paths report success and then rename the file into place as a *valid* cache or output. `SaveResultFITS` writes directly to the user's destination, so a failed save also destroys the previous output.
- **Inconsistency:** the repo already has the correct pattern in two places: `mosaic/star_map_fits.go:127-141` (flush, Sync, check Close, atomic rename) and `fitsio.Float32ArtifactTransaction.Commit`.

```go
// before
f, err := os.Create(path); if err != nil { return err }
defer f.Close()
bw := bufio.NewWriter(f)
...
return bw.Flush()

// after — one helper in fitsio, reused everywhere
func WriteFileAtomic(path string, write func(io.Writer) error) (err error) {
    f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
    if err != nil { return err }
    defer func() { if err != nil { f.Close(); os.Remove(f.Name()) } }()
    bw := bufio.NewWriter(f)
    if err = write(bw); err != nil { return err }
    if err = bw.Flush(); err != nil { return err }
    if err = f.Sync(); err != nil { return err }
    if err = f.Close(); err != nil { return err }
    return os.Rename(f.Name(), path)
}
```

### H4. The artifact (disk-backed) export writes black pixels on I/O errors and reports success

- **Where:** `internal/export/artifact_export.go:153-162` and `:369-379`.
- **Problem:** `image.Image.At` cannot return an error, so a `ReadRow` failure is mapped to `color.Gray16{}` / `color.NRGBA{}`. The encoder finishes, and `replaceExportFile` replaces the user's previous export with the corrupted one. The code re-checks cancellation after encoding but never checks I/O errors.

```go
// before
func (g *grayArtifactImage) At(x, y int) color.Color {
    if g.art.ReadRow(y, g.vals) != nil { return color.Gray16{} }
    ...

// after — latch the first error and check it after encode
if g.err == nil {
    if err := g.art.ReadRow(y, g.vals); err != nil { g.err = err }
}
if g.err != nil { return color.Gray16{} }
...
// in FromFloat32Artifact, after enc.Encode(...)
if img.err != nil { return fmt.Errorf("read artifact row: %w", img.err) }
```

### H5. Per-pixel string parsing and struct copies in every drizzle kernel

- **Where:**
  - `internal/mosaic/drizzle.go:2916-3003`: `bunitIsAlreadyRate`, `framePixelsAreRate`, `drizzlePixelWeight`, `drizzlePixelValue`.
  - These are called for every source pixel from the six kernel loops at `drizzle.go:3005-3180` and from `buildSeamMap` (`:210-216`).
  - The parsing itself is in `exposure_norm.go:32-60`.
- **Problem:** each pixel does all of the following:
  - passes `plannedInput`, which embeds `Input` (several hundred bytes), *by value*, twice;
  - calls `framePixelsAreRate`, which runs `strings.ToUpper`, `TrimSpace`, `ReplaceAll` and several `Contains` checks on `BUnit`;
  - calls `inputExposureTime`.

  `go build -gcflags=-m` shows none of these are inlined. For mixed-case units such as `MJy/sr`, `ToUpper` allocates on every call. That is 10⁷–10⁸ pixels × N frames, with a constant answer per frame.
- **Note:** this finding is about evaluation cost only. The EXPTIME/weighting semantics are deliberate and unchanged.

```go
// before (per pixel)
value := drizzlePixelValue(p, idx, pixels[idx], weightingMode)
w     := drizzlePixelWeight(p, idx, weightingMode)

// after — resolve once per frame
type pixelModel struct {
    isRate  bool
    invExp  float32
    constW  float32
    err, wht []float32
}
pm := newPixelModel(&p, weightingMode)   // calls framePixelsAreRate exactly once
value := pm.value(pixels[idx])
w     := pm.weight(idx)
```

Combine this fix with M5, which merges the six kernel loops so the change is made once.

### H6. Per-pixel stretch on the disk compose path rebuilds GHS coefficients and copies `LoadedImage`, and its logic is duplicated in `export`

- **Where:**
  - `internal/processing/disk_compose.go:780-840` (`stretchDiskValue`), called per pixel at `:591`, `:769` (`diskStretchFunc`) and from `star_treatment_model.go:117`.
  - Duplicate body: `internal/export/artifact_export.go:182-240` (`stretchScalar`), which additionally builds a closure and copies `models.LoadedImage` *per pixel* (`:163-168,223-229`).
- **Problem:**
  - `models.LoadedImage` (which holds the HDU and header) is passed by value per pixel.
  - In GHS mode each call runs `stretch.NewGHS`, which costs 5–8 `math.Exp` calls.
  - The in-memory path (`ApplyStretchParallel`, `processing.go:83-235`) already hoists all of this, so there are three implementations of the same mapping. A fix to one silently makes on-disk compose, export and on-screen preview disagree.
- **Fix:** move the mapping into `internal/stretch`. That also removes `export`'s only reason to import `models`.

```go
// before
func stretchDiskValue(v float32, img models.LoadedImage) float32 {
    bg, peak, scaled := sanitize(img.Background, img.Peak, img.ScaledPeak)
    switch img.Mode { case stretch.GHS: return stretch.NewGHS(d, img.GHSLocal, sp, 0, 1).Eval(...) ... }
}

// after (package stretch)
type Scalar struct { mode Mode; bg, invRange float64; ghs GHSParams /* prebuilt */ }
func NewScalar(p Params) Scalar           // resolve defaults + coefficients once
func (s *Scalar) Apply(v float32) float32 // switch only, no allocation
```

### H7. FITS HDU sizing ignores `NAXIS≥3`, `PCOUNT`/`GCOUNT` and `XTENSION`, so later HDUs are misread

- **Where:** `internal/fitsio/fitsio.go:185-218` (`checkedImageDataBytes`). Its result advances `pos` at `:147,167` and `stream.go:49,86`.
- **Problem:** the data size is always `NAXIS1·NAXIS2·|BITPIX|/8`.
  - For a cube (NAXIS=3) or a BINTABLE with a heap (PCOUNT>0) this underestimates the data unit, so the next `readHeader` parses pixel bytes as header cards.
  - A BINTABLE (NAXIS=2, BITPIX=8) is treated as an 8-bit image.
  - Many archive products put tables or cubes after the science HDU (HST `_flt` ancillary tables, JWST `ASDF` table, `HDRTAB`), so the later SCI/ERR/DQ extensions can be misread.

```go
// after (FITS 4.0 §4.4.1)
n := 1
for k := 1; k <= naxis; k++ { n, err = mulChecked(n, parseIntCard(hdr, fmt.Sprintf("NAXIS%d", k))) }
dataBytes := bpp * gcount * (pcount + n)
isImage := hdr.Cards["XTENSION"] == "" || strings.Contains(hdr.Cards["XTENSION"], "IMAGE")
```

### H8. Disk-mode Edit jobs swallow every error, cannot be cancelled, and can dereference nil

- **Where:** `internal/ui/workspace_edit.go`:
  - `:166-170`: the preview render.
  - The same `if err != nil { return }` shape appears in Heal (`:365-369`), UndoHeal (`:430-433`), Crop (`:494-498`) and Clean (`:900-904`).
- **Problem:**
  - The goroutine reads `es.diskSource.store` *inside* the goroutine, while `es.diskSource` is set to nil on the UI thread (`:818`, `:1603`). This is a race that can end in a nil-pointer panic.
  - Errors are dropped, so the preview silently stays stale.
  - All five jobs use `context.Background()`.

```go
// before
go func() {
    result, err := es.diskSource.store.Render(context.Background(), recipe, 1600)
    if err != nil { return }
    fyne.Do(func() { ... })
}()

// after
store := es.diskSource.store           // capture on the UI thread
ctx := es.beginJob()                    // cancels the previous job's ctx
go func() {
    result, err := store.Render(ctx, recipe, 1600)
    fyne.Do(func() {
        if es.jobGeneration != generation || ctx.Err() != nil { return }
        if err != nil { dialog.ShowError(err, es.win); return }
        ...
    })
}()
```

### H9. CI has no test, vet or race gate

- **Where:** `.github/workflows/build.yml`. The only steps are checkout, setup-go, apt deps, `go install fyne.io/tools/cmd/fyne@latest`, `fyne package`, upload.
- **Problem:** the repo has large test suites (for example `mosaic/drizzle_test.go`, `processing/disk_compose_test.go`, `fitsio/fitsio_test.go`), but nothing runs them on push. The non-UI packages pass `go test -race` today. Without a gate, regressions in C1, C2, H3 and similar areas will not be caught. `fyne@latest` is also unpinned, so a release build is not reproducible.

```yaml
# after — add before "Package"
- name: Vet & test (race)
  if: matrix.target == 'linux'
  run: |
    go vet ./...
    go test -race -count=1 ./...
- name: Install fyne CLI
  run: go install fyne.io/tools/cmd/fyne@v1.7.0   # pin
```

---

## Medium

### M1. FITS parsing makes huge allocations from untrusted header values before checking file size

- **Where:**
  - `internal/fitsio/fitsio.go:323-330`: `make([]byte, dataBytes)`, then `make([]float32, total)`.
  - `stream.go:111-119,888-890`.
- **Problem:** the only guard is an overflow check against `maxInt`. A 200-byte file claiming `NAXIS1=NAXIS2=200000, BITPIX=-64` requests about 320 GB and gets the process OOM-killed. `asdfio/reader.go:208` already bounds sizes by `st.Size()`; FITS should do the same.

```go
// after
if fi, err := f.Stat(); err == nil && int64(dataBytes) > fi.Size()-pos {
    return HDU{}, 0, fmt.Errorf("HDU data (%d bytes) exceeds remaining file size", dataBytes)
}
```

### M2. `Float32Artifact.Close` races with `ReadRow` and `WriteRow`

- **Where:** `internal/fitsio/stream.go:517-527` (ReadRow) and `:701-709` (Close).
- **Problem:** `ReadRow` checks `a.f == nil` *before* taking `a.mu`, and `Close` sets `a.f = nil` without the lock. A close between the check and `readRowLocked` hits a nil `*os.File`. These artifacts are shared between UI, render and worker goroutines, which makes that close plausible.

```go
// after
func (a *Float32Artifact) Close() error {
    if a == nil { return nil }
    a.mu.Lock(); defer a.mu.Unlock()
    if a.f == nil { return nil }
    err := a.f.Close(); a.f = nil; recordArtifactOpen(-1)
    return err
}
// and in ReadRow/WriteRow: take a.mu first, then test a.f == nil
```

### M3. A header truncated at a card boundary is treated as a clean end of file

- **Where:** `internal/fitsio/fitsio.go:264-271`, consumed at `:136-141` and `stream.go:38-43`.
- **Problem:** `io.ReadFull` returns `io.EOF` (not `ErrUnexpectedEOF`) when zero bytes of a card were read. Callers `break` on `io.EOF`, so a file cut mid-header loads "successfully" with its trailing HDUs silently dropped.

```go
if _, err := io.ReadFull(r, card); err != nil {
    if cardCount > 0 && errors.Is(err, io.EOF) { err = io.ErrUnexpectedEOF }
    return Header{}, 0, err
}
```

### M4. The SEAM diagnostic ignores ERR/WHT weighting

- **Where:** `internal/mosaic/drizzle.go:1062-1065` (planes released after each final-drizzle frame, which is correct for the streaming design), `:184` and `:210-216` (`buildSeamMap`).
- **Problem:** `buildSeamMap` calls `prepareFramePixels`, *discards* the returned `errPix`/`whtPix` (`pixels, _, _, err := ...`), then calls `drizzlePixelWeight(planned[pi], ...)`. That function reads `p.input.ERRPixels` and `WeightPixels`, which were nil'd at `:1064-1065`. With `WeightERR` every pixel therefore gets weight 1, and the seam RMS no longer reflects the weighting used for SCI.

```go
// before
pixels, _, _, err := prepareFramePixels(planned[pi], options, skyOffset[pi], skyPlanes[pi])
... drizzlePixelWeight(planned[pi], idx, options.WeightingMode)

// after
pixels, errPix, whtPix, err := prepareFramePixels(planned[pi], options, skyOffset[pi], skyPlanes[pi])
p := planned[pi]
p.input.ERRPixels, p.input.WeightPixels = errPix, whtPix   // local copy only
... drizzlePixelWeight(p, idx, options.WeightingMode)
```

### M5. Six copies of the drizzle kernel row loop

- **Where:** `internal/mosaic/drizzle.go:3005-3180` (`drizzlePlannedInputPoint`, `…Square`, `…Turbo`, `…Gaussian`, `…Tophat`, `…Lanczos`).
- **Problem:** each kernel repeats the same scaffolding: row trim, a cancel poll every 200 rows, debug log, CR-mask skip, value/weight lookup and `mapOutputPixel`. Only the deposit call differs. The copies have already drifted: only `Square` skips extreme or NaN coordinates. H5 would have to be fixed six times.

```go
// after
type depositFn func(outX, outY float64, v, w float32)
func drizzleRows(ctx context.Context, p *plannedInput, pm pixelModel, crMask BitMask,
    pixels []float32, trimX, trimY int, deposit depositFn) error { /* the shared loop, incl. finite-coord guard */ }
```

### M6. The CPU-chunked worker pool is written out by hand six times with inconsistent cancellation

- **Where:**
  - `internal/processing/processing.go:147-235` (`ApplyStretchParallel`)
  - `processing.go:624-645` (`TreatedStretchForSource`)
  - `star_extracting.go:680-760` (two passes in `gaussianConvolve`)
  - `wcs_align.go:362-385`
  - `star_stretch_footprint.go:36-60`
  - `internal/stretch/stretch.go:196`
- **Problem:**
  - Only one of these checks `ctx`.
  - Chunking varies between pixels, rows and channels.
  - `gaussianConvolve` starts `2×NumCPU` goroutines per call even for tiny inputs. It is also called concurrently during alignment (per its own comment), which oversubscribes the CPU quadratically.

```go
// after (one helper, e.g. internal/parallel)
func Rows(ctx context.Context, h int, fn func(y0, y1 int)) error {
    n := min(runtime.GOMAXPROCS(0), max(1, h/16)); per := (h + n - 1) / n
    var wg sync.WaitGroup
    for y0 := 0; y0 < h; y0 += per {
        wg.Go(func() { if ctx.Err() == nil { fn(y0, min(h, y0+per)) } }) // Go 1.25+ WaitGroup.Go
    }
    wg.Wait()
    return ctx.Err()
}
```

For bounded-concurrency fan-out with a first-error rule (mosaic, below), `golang.org/x/sync/errgroup` with `SetLimit` would replace three hand-rolled versions: `cr_tempfile.go:25-40` (`firstError`), `frame_loader.go:66-79` (`errMu`/`firstErr`) and `skysub.go:200-244` (`skyErrMu`/`skyErr`). Those launch loops also keep starting expensive jobs after the first failure (`cr_tempfile.go:95-99,160-164`, `skysub.go:206-222`). `errgroup.WithContext` fixes that for free.

### M7. Bilinear sampling exists in five copies with different NaN and edge semantics

- **Where:**
  - `internal/processing/transformation.go:103-156` (inline in `WarpImageToSizeCtx`, the Compose hot path, single-threaded)
  - `warp_mask.go:5-55` (inline again)
  - `wcs_align.go:392-416` (`bilinearSampleImage`, whose comment says it "mirrors" WarpImageToSize)
  - `wcs_align.go:74-96` (`bilinearSampleD2I`: clamps edges, returns NaN)
  - `clean.go:475-506` (renormalizes over finite corners)
- **Problem:** a fix to edge or NaN handling in one copy misses the others, so alignment, warp and clean disagree at image borders.

```go
// after: one sampler with an explicit policy
type EdgePolicy uint8 // EdgeZero, EdgeClamp, EdgeNaN
func SampleBilinear(px []float32, w, h int, x, y float64, edge EdgePolicy, skipNaN bool) float32
```

### M8. Median helpers: same names, different contracts

- **Where:**
  - `processing/star_map.go:43-53`: `starMedian` sorts its argument *in place*. It has about 30 callers; some pass live buffers, for example `star_neutralize.go:429` sorts each RGB `sector.values[c]` independently and so breaks the cross-channel slot correspondence.
  - `processing/star_refine.go:941`: `medianFloat64` copies.
  - `mosaic/skysub.go:707`: `medianFloat64` *expects pre-sorted* input.
  - `mosaic/gmos_calibration.go:672`: `finiteMedian` sorts.
  - `medianCopy` / `quickSelectMedian` elsewhere.
- **Problem:** whether a call mutates its input depends on which file it lives in. That is a latent correctness hazard, and sorting with `sort.Float64s` is O(n log n) where quickselect is O(n).

```go
// after (one package, two explicit names)
func MedianInPlace(a []float64) float64 // quickselect; documents mutation
func Median(a []float64) float64 { return MedianInPlace(slices.Clone(a)) }
```

### M9. `processing.AlignmentDebugHook` is a mutable global that changes algorithm results

- **Where:**
  - Defined at `internal/processing/alignment_debug.go:15`.
  - Read without synchronization at `tweakreg_align.go:169,217` and `internal/mosaic/drizzle.go:1673,1733,1741,2296`.
- **Problem:** in mosaic, a non-nil hook changes which catalog is retried (`retrySource = candidateCatalogs[i]`, `drizzle.go:1733-1735`) and triggers pixel reloads. Enabling "debug" therefore changes alignment output, and writing the hook while alignment runs is a data race. The UI installer (`internal/ui/alignment_debug_hook.go`) is a no-op, so only tests ever set it.

```go
// before
var AlignmentDebugHook func(AlignmentDiag)
if processing.AlignmentDebugHook != nil { retrySource = candidateCatalogs[i] }

// after
type AlignOptions struct { Debug func(AlignmentDiag) }  // passed explicitly
retrySource := catalogs[i]                               // independent of Debug
if opts.Debug != nil { opts.Debug(diag) }
```

### M10. `models` imports fyne, so the domain layer depends on the UI toolkit

- **Where:** `internal/models/models.go:7-24` and `:502-518`. `ChannelControl` holds `*widget.Select` and `fyne.CanvasObject`; `NumberField` and `CheckField` are also widget types.
- **Problem:** `processing` imports `models`, so every headless binary links fyne. `go list -deps ./cmd/starstretch ./cmd/starmap` shows 24 fyne packages. `models` also mixes:
  - persisted JSON DTOs (`ChannelState`, `ComposeProject`, `MosaicProject`);
  - runtime state (`LoadedImage` embeds `fitsio.HDU` with full pixel slices);
  - a renderer interface (`StretchTreatment`);
  - UI widget bundles.

  Only `internal/ui` uses the widget types (12 files).

```go
// after: move widget bundles to internal/ui
// internal/ui/channel_control.go
type ChannelControl struct { Content fyne.CanvasObject; ModeSelect *widget.Select; ... }
// models keeps plain data; `go list -deps ./cmd/starstretch | grep fyne` → empty
```

### M11. The UI package wires workspaces through package-level mutable globals

- **Where:**
  - `internal/ui/workspace_compose.go:22-30`: `globalSendToChannel`, `globalSelectComposeTab`, `globalComposeLargeCleanup`, `composeLargeModeActive`.
  - `workspace_edit.go:53`: `globalExportToEdit`.
  - `compose_starmap_refresh.go:8`: `activeComposeWorkspace`.
  - `blinker_window.go:23`, `debug_window.go:15-21`.
- **Problem:** these hide dependencies and block testing. Nil-checking is inconsistent: `compose_channel_controls.go:30` calls `composeLargeModeActive()` unguarded, while `:142` guards it. They would also break if a second workspace instance were ever created.
- **Other packages:**
  - `astroio.DefaultRegistry` (`decoder.go:58`, exported; `Register` appends without a lock).
  - `instrument.Default` (`instrument.go:92,111-129`).
  - The single-entry GMOS master cache (`mosaic/gmos_calibration.go:74-115`) hands out its cached slices to callers, who can mutate them, and lets two concurrent misses build the masters twice.
  - Test seams in production code: `processing.diskComposeRename`, `diskCompositePreviewForCompose`, `diskColorSpeckCleanCommitHook`, and `fitsio/stream.go:402`. These stop affected tests from using `t.Parallel()`.

```go
// before
var composeLargeModeActive func() bool
var globalExportToEdit func(editImageHandoff) error

// after: one explicit hub constructed in app setup and passed to each workspace
type appHub struct {
    sendToChannel  func(int, *models.LoadedImage)
    exportToEdit   func(editImageHandoff) error
    selectCompose  func()
    largeModeActive func() bool
}
func newComposeWorkspace(win fyne.Window, hub *appHub) *composeWorkspace
```

### M12. The save-file flow is copy-pasted 15 times and leaves stray empty files

- **Where:**
  - `internal/ui/compose_export.go:45,67,132,172`
  - `compose_overlay_ui.go:113,134`
  - `compose_legend.go:244`
  - `workspace_edit.go:1328`
  - `workspace_mosaic.go:214,445,1101,1194,1228`
  - `alignment_results.go:148`
  - `debug_window.go:99`
- **Problem:** Fyne has already created the file when the callback fires. The code closes it and reopens the path, and then:
  - leaves an empty file behind if the user cancels the follow-up options dialog or the code appends an extension (`path += ".csv"` at `workspace_mosaic.go:1106`, `+= ".fits"` at `:1201`);
  - drops the dialog `err` silently;
  - duplicates format detection (`workspace_edit.go:1336-1345` vs `detectExportFormat`).

```go
// before (×15)
save := dialog.NewFileSave(func(uc fyne.URIWriteCloser, err error) {
    if err != nil || uc == nil { return }
    path := uc.URI().Path(); _ = uc.Close()
    if filepath.Ext(path) == "" { path += ".fits" }
    ...
}, win)

// after
func pickSavePath(win fyne.Window, name string, exts []string, onPath func(path string)) {
    // shows the dialog, reports err, applies default ext,
    // removes Fyne's placeholder if the final path differs or the caller aborts
}
```

### M13. `exportRGB` reports compose errors as "Load three FITS first" and blocks the UI thread

- **Where:** `internal/ui/compose_export.go:149-157`. `compositeImageForEdit` (`compose_channels.go:186`) has the same shape.
- **Problem:** `buf == nil` is tested *before* `err`, so a disk-artifact read failure, an LRGB load error or a PSF failure is reported as a missing channel. The full-resolution compose (or a large-mode disk render) also runs synchronously in the menu handler, with no progress dialog and no way to cancel.

```go
// before
buf, w, h, _, err := ws.composeRGB(context.Background())
if buf == nil { dialog.ShowInformation("Missing", "Load three FITS first", ws.win); return }
if err != nil { dialog.ShowError(err, ws.win); return }

// after
if !composeHasAllBaseChannels(ws.imgs) { dialog.ShowInformation("Missing", "Load three FITS first", ws.win); return }
pt := newProgressTrackerWithContextOnUI("Exporting", "Composing…", ws.win, ctx, cancel)
go func() {
    buf, w, h, _, err := ws.composeRGB(ctx)
    fyne.Do(func() { pt.hide(); if err != nil { dialog.ShowError(err, ws.win); return }; ws.saveRGB(buf, w, h) })
}()
```

### M14. A cancelled context is treated as a failed star treatment, and the full untreated stretch runs anyway

- **Where:**
  - `internal/processing/processing.go:558-569` (`stretchForReferenceGrid`)
  - `:514-527` (`ImageDataForReferenceGridCtx`)
- **Problem:** `TreatedStretchForSource` returns `ctx.Err()` when cancelled. The caller logs "star treatment ignored" and falls through to a context-free `ApplyStretchParallel` plus a WCS warp. The same happens with a cancelled WCS align. Every superseded Compose refresh therefore still pays a full-frame stretch per channel.

```go
treated, err := TreatedStretchForSource(ctx, img)
if ctx.Err() != nil { return fitsio.ImageData{} }   // caller already discards stale generations
if err == nil { ... }
```

### M15. Domain and pipeline logic lives in `internal/ui`

- **Where:**
  - `internal/ui/compose_cross_channel_large.go:67-240`: about 14 temp files per channel, ping-pong scratch artifacts and row reader/writer closures, all plumbing for `processing.CrossChannelCleanDisk`. Its error path at `:83-88` also closes `maskFiles` twice: once inline and once in the already-registered defer.
  - `compose_alignment.go` (435 lines), which decides channel-alignment eligibility.
  - `compose_render.go:290-360`, which orders the PSF, weighted/LRGB and neutralizer pipeline.
  - `mosaic_levels.go` (`autoLevelsForPixels`).
- **Problem:** this logic can't be tested without the GUI. It is the main reason `internal/ui` is 31k lines.

```go
// before (ui)
f, e := os.CreateTemp(ws.largeStore.root, "clean-mask-%d-*.bin") // ×14, manual cleanup
// after
next, err := processing.CrossChannelCleanArtifacts(ctx, inputs, scratchDir, opts) // owns scratch lifecycle
```

### M16. The debug window leaks a goroutine per open and keeps logging after close

- **Where:** `internal/ui/debug_window.go:58,66-80,105,111-117`.
- **Problem:**
  - `ticker.Stop()` does not close `ticker.C`, so `for range ticker.C` blocks forever. Each open/close of the window leaks one goroutine.
  - `debuglog.SetHandler` is never reset, so `debugLines` grows for the rest of the process.
  - The `uc.Write` error is ignored.

```go
// before
go func() { for range ticker.C { ... } }()
win.SetOnClosed(func() { ticker.Stop(); ... })

// after
done := make(chan struct{})
go func() {
    defer ticker.Stop()
    for { select { case <-ticker.C: flush(); case <-done: return } }
}()
win.SetOnClosed(func() { close(done); debuglog.SetHandler(nil) })
```

---

## Low

### L1. Hand-rolled helpers duplicate Go 1.21+ builtins and the stdlib (module is `go 1.27.1`)

- **Where:**
  - `func min(a, b int)` at `internal/processing/star_extracting.go:126` shadows the builtin for the whole package.
  - `minInt`/`maxInt`/`clampInt`/`absInt` in `processing/processing.go:893`, `clean.go:462`, `wcs_align.go:98`, `mosaic/skysub.go:1675`, `webpwriter/webpwriter.go:1156-1176`.
  - `badpix.absFloat` (`badpix.go:367`) duplicates `math.Abs`.
  - Five or more `clamp01` variants (`fitsio/fitsio.go:422`, `stretch/stretch.go:186`, `export/artifact_export.go:430`, `processing/disk_compose.go:1158`, `utils/utls.go:12`) *disagree on NaN*: some map NaN to 0, others pass it through, and `byte(NaN*255)` is implementation-defined.
  - Nine or more "is finite" helpers across processing and mosaic.
  - Two Gaussian kernel builders (`processing/edit.go:93`, inline in `star_extracting.go:668-677`).

```go
// before
func min(a, b int) int { if a < b { return a }; return b }
func clampLRGB(v float64) float64 { if v < 0 { return 0 }; if v > 1 { return 1 }; return v }
// after
min(a, b)
func Clamp01(v float64) float64 { if !(v > 0) { return 0 }; return min(v, 1) } // NaN → 0, one definition
```

### L2. `stretch.Mode` has no `String`/`Parse`, so the persisted mode names are spelled in four places

- **Where:** `cmd/starstretch/main.go:41`; `internal/ui/workspace_compose.go:341-358` (`labelToMode`); `compose_channel_state.go:163` (`modeToLabel`); `mosaic_helpers.go:188` (`modeNameForMode`).
- **Problem:** `ChannelState.Mode` is stored in project files, so drift between the copies breaks project loading.

```go
var modeNames = [...]string{"linear", "log", "asinh", "sqrt", "histeq", "mtf", "ghs"}
func (m Mode) String() string { return modeNames[m] }
func ParseMode(s string) (Mode, bool) { i := slices.Index(modeNames[:], strings.ToLower(s)); return Mode(i), i >= 0 }
```

### L3. The FITS header float parser is duplicated and diverges

- **Where:** `internal/fitsio/write.go:114-133` (`HeaderFloat`) and `internal/processing/wcs_align.go:699-720` (`tryHeaderFloat`).
- **Problem:** the bodies are the same except that processing rejects NaN/Inf. Neither handles the Fortran `D` exponent (`1.0D+03`), and both cut at the first `/` even inside quoted strings, although `fitsio.parseCardValue` (`header_only.go:32`) handles quotes correctly. `fitsio.parseInt` (`fitsio.go:381-385`) uses `fmt.Sscanf`, ignores its error (so garbage parses as 0), and is called *per row* inside `stream.go:64,74,334`.

```go
func HeaderFloat(h Header, key string) (float64, bool) {
    v := strings.ReplaceAll(strings.ToUpper(parseCardValue(h.Cards[key])), "D", "E")
    f, err := strconv.ParseFloat(v, 64)
    return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}
// and hoist bitpix := parseInt(...) out of row loops; implement with strconv.Atoi
```

### L4. About 500 lines of dead code, plus unused packages

- **Where:**
  - In `internal/processing`, none of these has a production caller:
    - `ExtractStarsTiledReader` (`star_extracting.go:23-124`)
    - `combineDiskChannels` (`disk_compose.go:986-1098`, which duplicates `publishDiskArtifacts` rollback)
    - `WarpImageThroughWCS` (`wcs_align.go:348-390`)
    - `ComposeRGBFloat32` / `blendOverlay` (`processing.go:400-461`)
    - `CrossChannelCleanTiled` (`cross_channel_clean.go:1507-1590`), with a 70-line commented-out copy after its `return` at `:1593-1667`
    - `cross_channel_clean_legacy.go`
    - `AlignChannel`
  - In `internal/mosaic`: `compressCRMasks` (`drizzle.go:314-332`, re-inlined at `cr_tempfile.go:208-216`) and `extractStarCatalogsForAlignment` (`frame_loader.go:34-37`, which also swallows its error).
  - Unused packages: `internal/config`, `internal/debugtime`, and the top-level `badpix/` (only its own test uses it).
- **Fix:** delete them. Git keeps the history.

### L5. `utils` is a grab-bag with a misspelled file name

- **Where:** `internal/utils/utls.go` holds numeric clamps, a `strconv` wrapper, and FITS header formatting, which pulls `fitsio` into `utils`.
- **Fix:** move `FormatHeadersLines` to `fitsio` as `(Header).Lines()`, replace the clamps with builtins (L1), and delete the package.

### L6. Debug logging formats strings and stops the world even when disabled

- **Where:**
  - 165 `debuglog.Log(fmt.Sprintf(...))` call sites, including every drizzle kernel loop (`drizzle.go:3005-3180`).
  - `logMemStats` (`mosaic/frame_loader.go:351-357`) calls `runtime.ReadMemStats` unconditionally at each phase and every 8 frames.

```go
// internal/debuglog
var handler atomic.Pointer[func(string)]
func Enabled() bool { return handler.Load() != nil }
func Logf(format string, args ...any) { if h := handler.Load(); h != nil { (*h)(fmt.Sprintf(format, args...)) } }
// callers: debuglog.Logf("...%s", InputKey(p.input)); guard logMemStats with Enabled()
```

### L7. Small allocations in hot paths

- **RANSAC** (`processing/ransack.go:329-343`, 2000 iterations per fit): `rng.Perm(n)[:3]` allocates O(n) to pick 3 indices, and the inlier slice is reallocated every iteration. `tweakreg_align.go:427` also re-runs the same deterministically seeded RANSAC already computed at `:376`.
- **Blob detection in `ExtractStars`** (`star_extracting.go:217-270`): a new queue and blob per blob, and `q = q[1:]` never reuses the backing array. `clean.go:41-108` already shows the reusable head-index pattern.
- **`blurBufPool.Put(b[:0])`** (`star_extracting.go:649-664`) stores a slice value, so every `Put` allocates (staticcheck SA6002).
- **Streamed CR median** (`mosaic/cr_tempfile.go:453-474`) allocates a new 1 MiB `bufio.Reader` and a 32 KiB scratch buffer per frame per band.
- **`CrossChannelCleanTile`** (`cross_channel_clean.go:1486-1492`) runs `EstimateBackground` per tile even when the caller supplied sigma.
- **`badpix.weightedMedianFill`** (`badpix.go:250,283-285`) allocates and `sort.Slice`s up to 289 entries per masked pixel.
- **`processing.gaussianBlurRGBA`** (`edit.go:38-90`) recomputes `Bounds()` and `PixOffset` per tap.
- **`astroio` FITS `StreamPlane`** (`fits_decoder.go:96-127`) re-reads all headers on the first row and allocates `[]uint32` per row.

```go
// RANSAC
var s [3]MatchedPair
i := rng.Intn(n); j := rng.Intn(n - 1); if j >= i { j++ }
k := rng.Intn(n - 2); if k >= min(i, j) { k++ }; if k >= max(i, j) { k++ }
// pools
blurBufPool.Put(&b)            // store *[]float32
// queues
q = append(q[:0], seed); for head := 0; head < len(q); head++ { ... }
```

### L8. Mosaic alignment closures are duplicated and have drifted (refactor-completion item)

- **Where:** `internal/ui/workspace_mosaic.go:343-496` (selected stars) and `:993-1152` (auto stars), about 120 near-identical lines.
- **How they differ:**
  - `pt.hide()` is called inside `fyne.Do` in one and outside it in the other.
  - Only one handles `ErrCancelled`.
  - Only one reports the CSV close error (`:1101` vs `:445`).
  - A mojibake column label at `:424` (`"RotÃƒÆ’…"`) vs `"Rot (deg)"` at `:1080`.
- **Fix:** as part of the `mosaicWorkspace` refactor, collapse both into one method:

```go
func (ws *mosaicWorkspace) runAlignment(title string, align func(context.Context, []mosaic.Input) ([]mosaic.StarAlignmentResult, error))
```

The same applies to the duplicated result setup in `mosaic/drizzle.go:1473-1508` vs `:2171-2205`. Extract `initAlignmentResults(inputs, numRefs)`.

### L9. Inconsistent cancellation sentinels

- **Where:** `internal/mosaic/drizzle.go:525-530` (`Options.cancelled` maps *every* ctx error, including `DeadlineExceeded`, to `ErrCancelled`), `:2146` (returns raw `ctx.Err()`), and `exposure_combine.go:185` (`cerr == ErrCancelled`).
- **Problem:** a wrapped cancellation or `context.Canceled` is treated as a "fallback" error, and the pipeline carries on.

```go
// before
if cerr == ErrCancelled {
// after
if errors.Is(cerr, ErrCancelled) || errors.Is(cerr, context.Canceled) {
// and make ErrCancelled wrap: fmt.Errorf("%w: %w", ErrCancelled, ctx.Err())
```

### L10. The second atomic-replace helper reuses the backup scheme its sibling warns against

- **Where:** `internal/mosaic/artifact_mask_service.go:296-327` (`replaceFileAtomically`) vs `exposure_combine.go:297-332` (`atomicReplaceWorkingFile`). See also `artifact_mask_service.go:186-194,256-264`.
- **Problem:**
  - `replaceFileAtomically` removes and reuses `path+".bak"`, which is exactly what the other helper's comment forbids ("may contain the only recoverable cache").
  - The mask exporters call `CreateTemp` → `Close` → `Remove`, then rewrite the same name, which loses `O_EXCL` uniqueness.
  - The `!overwrite` branch relies on `Stat` followed by `os.Rename`, which overwrites anyway on POSIX.
- **Fix:** keep one helper (see H3) and use `os.Link` for no-overwrite publishing.

### L11. Swallowed errors that are small but user-visible

- `internal/ui/workspace_mosaic.go:1238`: `_ = mosaic.UpdateMasterOffsets(...)`, followed by a "Saved" message.
- `internal/ui/alignment_results.go:41`: `_ = json.Unmarshal(...)` hides corrupt residuals.
- `internal/ui/compose_load.go:22-26`: `recover()` turns a panic into an error but drops the stack. Log `debug.Stack()`.
- `internal/asdfio/reader.go:172-179`: the block scan `break`s on *any* `ReadAt` error, so a real I/O error surfaces later as "source block unavailable". It also issues one `ReadAt` syscall per non-magic byte.
- `internal/badpix/badpix.go:45-48`: `RepairMaskedPixels` silently returns the unrepaired image when the mask length is wrong.
- `internal/ui/compose_project.go`: a local `errors := make([]string…)` shadows the `errors` package.

```go
if err := mosaic.UpdateMasterOffsets(dir, state.inputs); err != nil { dialog.ShowError(err, win); return }
```

### L12. Documentation drift and small inefficiencies

- `mosaic.Options.FinalKernel` (`drizzle.go:444-446`) is documented as "Currently unused; SepKernel governs all drizzling", but Build uses it for the final pass (`:954,1032`). A caller who follows the doc silently gets the Square kernel.
- `NContrib` is incremented per deposit in `mark()` (`drizzle.go:675`) and then zeroed and recomputed from the context planes (`:1147-1152`), so the per-deposit increments are wasted work.
- `buildSeamMap` allocates two full-canvas `float64` planes (8 GB at the 500 Mpx cap). `float32` is sufficient.
- `webpwriter.WriteImageWebPLosslessFile` (`webpwriter.go:80-106`) allocates the full RGBA buffer before checking `maxVP8LDimension` (`:47-55`). Check the bounds first.
- `compose_channels.go:150-155` calls `ws.refresh()` twice in a row.
- The dedicated-L open dialog (`compose_channels.go:108-167`) re-implements `showSingleFITSOpenDialog` (`fits_resize.go:59`).
- `compose_channel_controls.go` keeps a `time.Sleep(50ms)` "let Fyne paint" step that is obsolete now that `refresh()` is asynchronous.

---

## Dependency & module architecture

### Internal import graph

```
leaf:   fitsio  asdfio  stretch  histogram  debuglog  version  webpwriter
        render → stretch
        badpix, instrument, utils → fitsio
        astroio → fitsio, asdfio
        models  → fitsio, stretch, **fyne/v2, fyne/v2/widget**        ← layering violation (M10)
        processing → models, render, histogram, utils, stretch, fitsio, debuglog
        mosaic  → processing, astroio, badpix, instrument, fitsio, debuglog
        export  → models (only for stretch params, H6), stretch, fitsio, webpwriter
        starbench, starstretchpreview → mosaic, processing, (models), stretch, fitsio
        ui      → nearly everything
unused: config, debugtime, /badpix
```

There are no import cycles.

### Coupling issues

1. `models` → fyne (M10): the headless CLIs link the GUI toolkit.
2. `mosaic` → `processing`: mosaic uses processing for star extraction and alignment, and also reads a processing global (M9). A small `internal/align` (or `internal/stars`) package would let both depend on alignment without mosaic pulling in the whole Compose/stretch stack.
3. `export` → `models`: exists only to read stretch parameters. Solved by moving the scalar stretch into `stretch` (H6).
4. `utils` → `fitsio`: header formatting is FITS-domain code (L5).
5. `ui` owns pipeline logic (M15), which is the main cause of the 31k-line package.

### Interfaces

No meaningful interface pollution was found: the interfaces that exist (`astroio` decoders, `models.StretchTreatment`) have several implementations or are consumed across packages. The opposite problem is more common here: function-typed globals (M9, M11) are used where a small explicitly passed struct would make the dependencies visible.

### panic / recover

No misuse found. `panic` is used only for invariant assertions (`webpwriter`) and `crypto/rand` failure (`starbench/model.go:71`). The single `recover` (`ui/compose_load.go`) is a reasonable crash barrier but should log the stack (L11).

---

## Prioritized refactoring plan

The plan is ordered by **impact ÷ risk**. Each step is independently shippable and testable.

| # | Step | Findings | Impact | Risk | Notes |
|---|---|---|---|---|---|
| 1 | Add a `go vet` + `go test -race ./...` job (Linux) to CI; pin `fyne` CLI version | H9 | High | Very low | Do this first so every later step is gated. |
| 2 | Apply `BZERO`/`BSCALE`/`BLANK` in `fitsio` decode paths; add an unsigned-16-bit fixture test | C1 | Critical | Low | One decode function; the test proves it. |
| 3 | Introduce one `fitsio.WriteFileAtomic` helper; migrate `fitsio.Write`, `SaveResultFITS`, export writers, disk-compose artifact closes, mask exporters; delete `replaceFileAtomically` | H3, L10 | High | Low | Mechanical. The pattern already exists in `star_map_fits.go`. |
| 4 | Latch `ReadRow` errors in artifact export images | H4 | High | Very low | About 10 lines. |
| 5 | Pass `prog.Ctx` and check errors in mosaic auto-alignment; switch the three first-error pools to `errgroup` | H2, M6 (mosaic part), L9 | High | Low | |
| 6 | FITS robustness: full NAXISn/PCOUNT/GCOUNT sizing, file-size bound, truncated-header EOF, lock-protected `Float32Artifact.Close` | H7, M1, M2, M3 | High | Low–Med | Add fuzz tests (`testing.F`) for `readHeader` and `checkedImageDataBytes`. |
| 7 | Per-frame `pixelModel` + single `drizzleRows` loop in mosaic | H5, M5, M4 | High (throughput) | Medium | Guard with the existing `drizzle_test.go` and a golden-output comparison. Semantics unchanged. |
| 8 | Move the scalar stretch into `stretch.Scalar`; use it from disk compose, export and in-memory paths; add `Mode.String/ParseMode` | H6, L2 | High (perf + consistency) | Medium | Golden tests per stretch mode before switching. |
| 9 | Compose concurrency: immutable `composeRenderInput` snapshot; publish worker results only inside `fyne.Do` behind per-slot generations; copy-on-write in Apply/Auto | C2, M14 | Critical | Medium–High | Largest UI change. Do it after step 1 so the snapshot builder can be unit-tested with `-race`. |
| 10 | Replace "Please wait" dialogs with `progressTracker` + ctx + generation (Compose, Edit, Examine); fix disk-Edit jobs; async `exportRGB` | H1, H8, M13 | High | Medium | Shares the generation helpers with step 9. |
| 11 | Shared helpers: `parallel.Rows`, one bilinear sampler, `Median`/`MedianInPlace`, NaN-safe `Clamp01`, builtin `min`/`max` | M6–M8, L1, L3 | Medium | Low–Med | Do it package by package; each swap is covered by existing tests. |
| 12 | Decouple: move widget bundles out of `models`; replace UI globals with an `appHub`; pass `AlignOptions.Debug` explicitly | M9, M10, M11 | Medium | Medium | Verify with `go list -deps ./cmd/starstretch \| grep fyne` → empty. |
| 13 | UI hygiene: `pickSavePath` helper, debug-window goroutine fix, swallowed-error fixes | M12, M16, L11 | Medium | Low | |
| 14 | Move pipeline logic out of `ui` (cross-channel-clean scratch handling, alignment eligibility, compose pipeline order) | M15 | Medium | Medium | Natural continuation of the compose-workspace refactor (`docs/compose-workspace-refactor-plan.md`). |
| 15 | Finish the `mosaicWorkspace` refactor by merging the alignment closures | L8 | Low–Med | Low | |
| 16 | Delete dead code and unused packages; fold `utils` into its owners; `debuglog.Logf`/`Enabled`; small allocation fixes | L4–L7, L12 | Low | Very low | Good filler between larger steps. |
