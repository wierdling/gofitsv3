# Plan: Compose Color Calibration

## Goal

Add optional, reproducible color calibration to Compose without changing
existing projects or the current artistic workflow when calibration is off.

Implement the feature in two main phases:

1. **Local calibration:** background neutralization, HST/JWST instrument
   photometric calibration, and opt-in calibrated-linear overlay mixing.
2. **Gaia calibration:** an SPCC-like stellar fit that queries only the Gaia
   data needed for the current field and retains those sources and spectra in
   a growing local SQLite cache.

This is a color-balancing and pretty-picture workflow. It must report its
physical assumptions and provenance, but it must not claim that arbitrary
false-color or narrowband tints are scientifically unique.

## Decisions fixed for this work

- Calibration is opt-in and defaults to **Off**.
- Background neutralization is independent of the photometric mode.
- Photometric modes are **Off**, **Instrument**, and, in Phase 2, **Gaia**.
- White references are flat `Fnu` by default, with flat `Flambda` and an
  average-spiral-galaxy reference available where the required reference data
  exists.
- Automatic background selection uses robust source rejection; the user may
  instead select a background ROI in aligned reference coordinates.
- Missing, contradictory, or unsupported metadata is an explicit unsupported
  result. Never guess a filter, unit conversion, passband, WCS, or catalog
  value.
- Calculation is explicit. Settings or input changes mark a saved result stale
  instead of recalculating silently.
- Original pixels are never overwritten by calibration. Projects persist
  immutable gains, offsets, diagnostics, provenance, and input fingerprints.
- Preview, before/after comparison, Edit handoff, and export use the same
  immutable render request/result path, including 16-bit output.
- Existing overlay behavior remains **Artistic** by default. An overlay may opt
  into **Calibrated Linear** mixing.
- The Gaia feature does not require a preloaded all-sky catalog. It queries
  encountered fields and adds only needed sources and XP spectra to a shared
  local cache.

## Existing behavior to preserve

- `ComposeRGB` aligns each base channel to Channel 2, stretches the three
  grayscale planes independently, and builds the RGB preview.
- Arbitrary overlays live in `imgs[3:]`. `ComposeRGBWithOverlays` currently
  stretches each overlay, applies its tint and opacity, and screen/additively
  blends it into the rendered RGB buffer.
- Compose rendering already uses cancellable background generations and
  applies completed UI changes through the UI thread.
- Compose project JSON has no color-calibration block. Omitted new fields must
  load as calibration Off and all overlays Artistic.
- Alignment, Normalize Scale, and cleaning may modify loaded channel pixels.
  Calibration must not use `origPixels` as an undo store; it must remain a
  separate, non-destructive render transform.

## Canonical calibrated processing order

For calibrated rendering, use one explicit linear pipeline:

1. load the untouched current channel pixels;
2. align/resample every participating plane to the Channel 2 reference grid
   while producing an explicit validity/footprint mask;
3. restrict work to finite pixels whose aligned mask is valid and the optional
   common background ROI;
4. subtract the persisted per-plane background offset;
5. apply the persisted physical/photometric gain;
6. map calibrated-linear overlays through their user-selected tint and strength;
7. form a float RGB composite;
8. apply one shared color-preserving stretch to the calibrated float RGB data;
9. convert to the preview/export representation.

Use `y = (x - backgroundOffset) * gain`. Preserve negative finite linear values
until the stretch/output stage; do not clip during calibration.

Artistic overlays continue to use the current post-stretch tint/blend path.
They must not inherit base RGB gains.

## Relevant current files

| File | Current responsibility |
| --- | --- |
| `internal/models/models.go` | `LoadedImage`, `ChannelState`, `ComposeProject`, and overlay project state |
| `internal/models/models_test.go` | Project JSON round-trip and compatibility tests |
| `internal/processing/processing.go` | Reference-grid alignment, stretch, RGB composition, and overlay blending |
| `internal/processing/star_extracting.go` | Star detection with position, flux, peak, and area |
| `internal/processing/wcs_align.go` | FITS WCS pixel/sky mapping |
| `internal/processing/compose_rgb_test.go` | Compose/reference-grid behavior tests |
| `internal/ui/workspace_compose.go` | Compose state, menus, async preview generation, project save/load, overlays, and export/Edit handoff |
| `internal/ui/compose_state.go` | Small Compose state helpers |
| `docs/user-guide.md` | User-facing Compose workflow |
| `docs/unit-test-standards.md` | Repository test-quality requirements |
| `docs/unit-test-audit.md` | Living coverage and risk inventory |

# Phase 1: Background and instrument calibration

## Step 1. Define calibration models and typed status

**Status: Complete** — implementation, focused tests, gofmt, go vet, and
independent review passed.

### Files

- `internal/models/models.go`
- new `internal/processing/color_calibration.go`
- new `internal/processing/color_calibration_test.go`
- `internal/models/models_test.go`

### Implementation

Add versioned project-level types for:

- photometric mode: Off or Instrument;
- background neutralization enabled/disabled;
- background selection: automatic or aligned-reference ROI;
- white reference: flat `Fnu`, flat `Flambda`, or average spiral galaxy;
- linked calibrated-output stretch settings;
- immutable base-channel transforms;
- per-overlay mode and immutable scalar transform;
- calculation status: disabled, calculating, valid, stale, unsupported,
  cancelled, or failed;
- diagnostics, warnings, algorithm/reference versions, and provenance;
- canonical source/settings fingerprints.

Represent an applied linear transform as an offset and gain. Keep calibration
state out of `HDU.Data.Pixels`; pass immutable composition options into the
processing layer.

Additive JSON fields must preserve legacy behavior:

- no calibration block means Off;
- no overlay mixing mode means Artistic;
- invalid or non-finite persisted transforms are rejected and never applied.

### Tests

- Legacy Compose JSON loads with Off/Artistic defaults.
- Full new state round-trips without losing zero or negative finite values.
- Invalid enums and non-finite transforms are rejected safely.
- Results are copied/treated immutably.

### Acceptance

- Existing project files load and render through the old path.
- A new project can persist settings, valid/stale result state, diagnostics, and
  overlay modes without storing pixels or masks.

## Step 2. Parse supported HST/JWST photometric metadata

**Status: Complete** — implementation, focused tests, gofmt, go vet, and
independent review passed.

### Files

- new `internal/processing/instrument_photometry.go`
- new `internal/processing/instrument_photometry_test.go`
- existing FITS header helpers only if a narrowly scoped parser is missing
- small versioned reference assets in a dedicated processing reference-data
  directory

### Implementation

Create a registry keyed by telescope, instrument, detector, and filter. For
each supported combination define:

- accepted `BUNIT` values;
- whether pixels are counts, count rate, calibrated `Flambda`, calibrated
  `Fnu`, or surface brightness;
- authoritative key precedence between SCI and primary headers;
- valid use of `EXPTIME`, `PHOTFLAM`, `PHOTPLAM`, `PHOTFNU`, `PHOTMJSR`, and
  equivalent supported keywords;
- required pivot/effective wavelength and passband identity;
- conversion to the selected common white-reference system.

Initial HST support should convert a verified count rate with `PHOTFLAM` to
`Flambda`, and convert to `Fnu` when that is the selected reference. Initial
JWST support should accept already calibrated compatible `Fnu`/MJy/sr data.
Do not combine incompatible per-pixel and per-solid-angle measurements or mixed
instruments without an explicitly defined conversion.

Version the average-galaxy template and throughput/reference data. If a filter
lacks the data needed for that reference, disable that reference with an
actionable unsupported reason.

### Tests

- Table-driven success cases for every supported instrument/key/unit form.
- SCI-versus-primary keyword precedence.
- Counts versus count-rate exposure handling.
- Exact `Fnu`/`Flambda` conversion fixtures.
- Missing, contradictory, zero, negative, NaN, and infinite metadata.
- Unknown instrument/filter/unit combinations return typed unsupported status.

### Acceptance

- Every supported conversion is deterministic and documented.
- No fallback silently treats unknown units as comparable data.

## Step 3. Implement robust background neutralization

**Status: Complete** — implementation, focused tests, gofmt, go vet, and
independent review passed.

### Files

- `internal/processing/color_calibration.go`
- `internal/processing/color_calibration_test.go`
- reuse existing background/star helpers where their contracts are suitable

### Implementation

On aligned linear planes:

- ignore NaN/Inf and invalid/out-of-footprint pixels;
- mask detected stars and a conservative surrounding radius;
- robustly reject sources, nebulosity, and other bright outliers;
- use deterministic sigma/MAD clipping with a minimum sample threshold;
- measure background medians in spatial tiles and reject the calculation as
  non-uniform when a gradient or structure exceeds a documented threshold;
- support automatic whole-frame sampling or a shared user ROI;
- report estimate, dispersion, accepted/rejected counts, and rejection reason
  for each plane;
- check `context.Context` during large scans.

Keep background-only operation valid when instrument calibration is Off.
Combine operations as offset first, gain second.

A Phase 1 transform is scalar per plane and therefore cannot remove a spatial
gradient. This phase detects and refuses unsuitable non-uniform backgrounds; a
persisted 2-D background-surface model is a separate future feature.

### Tests

- Known RGB offsets with stars and outliers.
- Smooth gradients and extended nebulosity, including cases on both sides of
  the documented spatial-uniformity accept/refuse threshold.
- NaN/Inf, empty/partial ROI, short slices, and insufficient samples.
- Cancellation and no input mutation.
- Deterministic output regardless of worker scheduling.

### Acceptance

- Synthetic background casts are removed within a documented tolerance.
- Weak or invalid sampling never produces a valid-looking transform.
- A background requiring spatial correction returns a clear non-uniform status
  instead of publishing a scalar offset.

## Step 4. Calculate transforms, provenance, and staleness

**Status: Complete** — implementation, focused tests, gofmt, go vet, and
independent review passed.

### Files

- `internal/processing/color_calibration.go`
- related model/header helpers
- tests beside those files

### Implementation

Calculate base RGB gains and offsets without applying them. Remove arbitrary
global luminance scaling with a documented normalization, such as geometric
mean gain equal to one.

Build a canonical SHA-256 fingerprint over every behavior-affecting input:

- source identity, dimensions, and a SHA-256 content hash of the current
  float32 pixel bit patterns;
- alignment/reference-grid identity;
- relevant FITS metadata;
- channel/filter mapping;
- background method and ROI;
- white reference and reference-data version;
- linked stretch configuration;
- overlay order, source, tint, strength, mode, and passband;
- algorithm version.

Do not hash Go map iteration order. Provide a pure stale check. UI-only state
such as open windows must not affect the fingerprint.

Maintain a cheap runtime pixel revision that invalidates the cached content
hash after Align, Normalize Scale, cleaning, Reset Data, load, or any future
pixel mutation. Compute the full content hash only when calculating,
persisting, or verifying calibration. On project reload, compare the freshly
loaded pixel hash with the persisted hash. Because current projects do not
persist destructively modified channel pixels, any mismatch must mark the
calibration stale; never retain a valid result merely because the source path
is unchanged.

### Tests

- Hand-calculated combined transforms.
- Offset-before-gain ordering.
- Stable fingerprints across save/load and map ordering.
- Each relevant input change marks stale; irrelevant UI changes do not.
- Save/reload after Align, Normalize Scale, cleaning, Reset Data, and source
  replacement either verifies identical content or marks the result stale.
- Cancel/failure retains the preceding valid immutable result.

### Acceptance

- Equal inputs reproduce the same transform and fingerprint.
- A stale result remains inspectable but is not applied.

## Step 5. Add the canonical calibrated float compositor

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `internal/processing/processing.go`
- `internal/processing/wcs_align.go`
- aligned-plane/WCS tests
- `internal/processing/compose_rgb_test.go`
- a new focused compositor file/test if that keeps `processing.go` narrow

### Implementation

Introduce an immutable `ComposeRenderRequest`/`ComposeRenderResult` (names may
follow repository style) rather than embedding project calibration state in
generic image stretch helpers. The result must carry the final normalized
float RGB representation used by high-bit-depth outputs and the derived 8-bit
preview representation, along with dimensions, statistics, and calibration
status.

Refactor only enough to expose aligned, unstretched channel planes. The
calibrated path must:

- align first and return an `AlignedPlane`-style value containing pixels plus
  an equally sized validity/footprint mask;
- mark WCS/resampling fill outside the true source footprint invalid rather
  than treating its current zero fill as image data;
- apply a matching valid transform to a copy of each finite plane;
- form float RGB contributions;
- apply a shared luminance/color-preserving stretch, scaling RGB together so a
  nonlinear stretch does not recreate a channel cast;
- use the same immutable request/result function for preview, comparison, Edit
  handoff, 8-bit export, and 16-bit export.

The Off path must continue to call the existing composition behavior and
produce byte-for-byte equivalent 8-bit output. The unified renderer must also
fix the current 16-bit path so it includes the same overlays and final display
composition as preview; document this intentional correction.

### Tests

- Exact tiny-image offset/gain result.
- Calibration is after alignment and before stretch.
- Partial WCS overlap proves zero-filled margins cannot affect background
  estimates, aperture fluxes, gains, or overlay contributions.
- Validity masks follow shared-grid, WCS-warp, resize-fallback, NaN, and
  cancellation paths.
- Negative linear values survive until output mapping.
- Stale/unsupported transforms are not applied.
- Input/aligned buffers are not modified.
- Off output matches the current baseline exactly.
- Reference-grid/WCS fallback behavior remains unchanged.
- Preview, Edit handoff, 8-bit export, and 16-bit export are derived from the
  same render result for base-only and mixed Artistic/Calibrated Linear cases.

### Acceptance

- There is one calibrated application path and one unchanged legacy path.
- Preview, Edit, and both export depths consume the same immutable render
  result for identical settings.

## Step 6. Implement opt-in calibrated-linear overlays

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `internal/models/models.go`
- `internal/processing/processing.go` or the focused compositor file
- overlay/Compose processing tests
- `internal/ui/workspace_compose.go`

### Implementation

For each overlay add:

- Artistic or Calibrated Linear mode;
- independent background-neutralization participation;
- an Instrument scalar transform when metadata is supported;
- user tint;
- a pre-stretch strength multiplier;
- per-layer status, diagnostics, provenance, and fingerprint.

For Calibrated Linear mode:

1. align the grayscale overlay;
2. subtract its background offset;
3. apply its physical scalar normalization;
4. multiply it by the normalized tint vector and user strength;
5. add its RGB contribution to the float base composite;
6. use the shared calibrated output stretch.

Do not apply base RGB gains to an overlay. A calibrated-linear overlay is
additive in linear space; the current post-stretch screen/highlight-protection
control does not have a physical linear equivalent and should be hidden or
disabled in this mode. Artistic mode retains opacity and highlight protection
unchanged.

If calibrated-linear metadata is unsupported, show a layer-specific reason and
do not silently invent a factor. The user can switch the layer back to
Artistic.

### Tests

- Artistic overlay regression output is unchanged.
- Exact subtract/scale/tint/add/stretch ordering.
- Base gains do not rotate the chosen overlay hue.
- Strength is applied exactly once.
- Mixed Artistic and Calibrated Linear overlays remain independent.
- Unsupported metadata is explicit and deterministic.
- Reorder/change invalidates the affected layer and aggregate render
  fingerprint.

### Acceptance

- Existing overlays look identical by default.
- Supported opt-in overlays make a physically normalized linear contribution
  while leaving hue and strength under user control.

## Step 7. Persist and wire the cancellable Compose UI

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `internal/ui/workspace_compose.go`
- `internal/ui/compose_state.go` or a new small calibration state helper
- UI/model tests
- `internal/models/models_test.go`

### Implementation

Add `Compose -> Color Calibration...` with:

- photometric mode;
- independent Neutralize Background checkbox;
- white-reference selection;
- automatic/ROI background selection;
- explicit Calculate and Cancel;
- base and per-overlay status/diagnostic summaries;
- per-overlay Artistic/Calibrated Linear selection;
- calibrated-linear tint and strength controls;
- Before/After comparison using the saved transform, not recalculation;
- reset/disable behavior that is immediate and non-destructive.

Use the existing generation/cancellation pattern:

- snapshot settings and inputs;
- run analysis outside the UI thread;
- publish a result atomically on the UI thread only when its generation and
  fingerprint still match;
- discard late results;
- keep the previous valid result on cancel or failure;
- mark stale immediately after a relevant change;
- invalidate render caches after accepted state changes.

Project load must never calculate automatically. Reset Data must not bake or
erase calibration transforms unexpectedly.

Project load may verify persisted content fingerprints while pixels are already
being loaded. This is validation, not recalculation. A content mismatch keeps
the prior result for inspection but marks it stale and prevents application.

### Tests

- Pure state transitions for Calculate, Cancel, success, failure, stale,
  reset, and Before/After.
- Late-result rejection after settings/project replacement.
- Menu enablement when base channels are missing.
- Save/load of a valid and a stale result.
- Save/load after every current pixel-mutating operation verifies or marks stale
  based on content, never path alone.
- Preview/export/Edit handoff share the same calibrated request.

### Acceptance

- The UI remains responsive.
- Cancellation and stale generations cannot overwrite current state.
- Turning calibration Off restores the existing rendering immediately.

## Step 8. Phase 1 documentation and validation gate

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `docs/user-guide.md`
- `docs/unit-test-audit.md`
- relevant package tests

### Documentation

Document:

- the exact supported HST/JWST metadata matrix;
- unit and white-reference formulas;
- background ROI and rejection behavior;
- linked calibrated stretch;
- Artistic versus Calibrated Linear overlays;
- unsupported/stale status and provenance;
- why a physically normalized overlay tint is still a user-chosen palette.

### Validation

Run the narrowest package tests during each implementation step, followed by:

- `go test ./internal/processing`
- `go test ./internal/models`
- `go test ./internal/ui`
- `go test ./...`

Manual acceptance must cover Off regression, background-only, each supported
instrument/reference, unsupported metadata, ROI, cancellation, save/reload,
Before/After, preview/export equality, and mixed Artistic/Calibrated Linear
overlays.

### Phase 1 exit criteria

- Off/Artistic results are unchanged.
- Valid calibrations are non-destructive and reproducible after project reload.
- Preview, Edit handoff, and export agree.
- Tests and documentation name unsupported boundaries instead of guessing.

# Phase 2: Gaia SPCC-like calibration with a growing local cache

## Step 9. Define Gaia provider and calibration contracts

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- new `internal/catalog/gaia/provider.go`
- new `internal/processing/gaia_calibration.go`
- new focused tests
- Phase 1 model/settings files

### Implementation

Define a provider interface, independent of HTTP and SQLite, for:

- field/cone/polygon source discovery;
- batched XP spectrum retrieval;
- cache-only/offline lookup;
- release and provider provenance.

Normalize records to:

- Gaia release and source ID;
- RA/Dec, reference epoch, proper motion, and required uncertainties;
- G/BP/RP photometry and errors;
- documented quality/variability/contamination flags;
- XP representation and calibration version.

Define immutable fit diagnostics: detected, matched, accepted, and rejected
stars; per-star rejection reason; residuals; robust scatter; fitted gains;
catalog/passband/algorithm versions; and source IDs.

### Tests

- Processing operates entirely against an in-memory fake provider.
- Malformed/incomplete records and unsupported passbands fail explicitly.
- Provider ordering does not change a result.
- Gaia release/passband changes participate in staleness.

### Acceptance

- Processing has no direct dependency on HTTP or SQLite.
- Online, cached, and test providers share one contract.

## Step 10. Add the local SQLite cache

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Dependency decision

Use `database/sql` with a pinned CGO-free SQLite driver such as
`modernc.org/sqlite`, after verifying its license and supported Go/toolchain
versions during implementation. This dependency is justified by transactional
upserts, deduplication, migrations, indexes, and crash recovery. Do not replace
it with growing JSON files.

### Files

- `go.mod` and `go.sum`
- new `internal/catalog/gaia/cache.go`
- new `internal/catalog/gaia/migrations.go`
- new cache tests

### Storage and schema

Use a configurable application cache/data path, not the project directory or
Fyne Preferences. Suggested tables:

- `metadata`: schema version, Gaia release, XP representation version;
- `sources`: primary key `(release, source_id)`, astrometry, epoch/proper
  motion, photometry/errors, flags, fetch timestamp, and spatial cell;
- `xp_spectra`: primary key `(release, source_id, representation_version)`,
  version metadata, compressed coefficients/samples, wavelength metadata, and
  checksum;
- `query_cells`: primary key `(query_signature, spatial_cell)`, where the
  canonical signature includes Gaia release, provider/endpoint semantics,
  source-column schema, query algorithm version, HEALPix level, magnitude
  bounds, and every server-side quality constraint; store completion timestamp
  and proof that the full cell was fetched;
- bounded retry/backoff metadata for transient failures.

Use a tested HEALPix or equivalent spherical tiling implementation. Index
spatial cell and source ID. Enable foreign keys, WAL, a bounded busy timeout,
explicit migrations, and transactional batched upserts.

Prefer fetching complete HEALPix cells and applying user-selectable quality
filters locally so raw cached records are reusable. A query cell becomes
complete only in the same successful transaction that stores all returned
source summaries for that exact signature and full cell. If an endpoint can
return only a partial cone/polygon, persist its normalized coverage geometry
and reuse it only when it contains the later request; never mark the whole cell
complete. A deeper magnitude request or changed server-side constraint requires
a distinct signature or an atomic coverage upgrade. Do not permanently cache a
transient network failure as an empty field.

Persist fitted transforms and source provenance in the project so an already
calibrated project renders even if the shared cache is moved or deleted.

### Tests

- Fresh creation and every migration.
- Duplicate source/spectrum upsert.
- Different Gaia/XP versions coexist safely.
- Cancellation/error rolls back data and cell completion.
- Partial-cell coverage cannot satisfy a request for another part of the cell.
- Changed magnitude, column schema, release, endpoint semantics, or server-side
  constraints cannot reuse an incompatible completion marker.
- Concurrent readers and bounded writer behavior.
- Corrupt row/checksum handling.
- Deleting the cache does not prevent a saved valid project from rendering.

### Acceptance

- Repeated and overlapping fields deduplicate by Gaia release/source ID.
- Interrupted work cannot create a falsely complete cache entry.

## Step 11. Implement remote cache-through Gaia retrieval

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- new `internal/catalog/gaia/remote.go`
- `internal/catalog/gaia/provider.go`
- HTTP fixture tests

### Implementation

For a new image field:

1. derive its footprint and observation epoch from verified WCS/metadata;
2. split the footprint into spatial cache cells and canonical query signatures;
3. read completed compatible cells from SQLite;
4. query the configured official Gaia service only for missing cells,
   retrieving the complete cell where supported;
5. store/deduplicate source summaries transactionally;
6. crossmatch and reject unsuitable sources locally;
7. request XP data only for viable encountered source IDs not already cached;
8. store/deduplicate spectra transactionally;
9. return one normalized provider result combining cache and remote data.

Support configurable endpoint, release, request timeout, bounded concurrency,
batch size, retry/backoff, user agent, online/cache-only policy, and
`context.Context` cancellation throughout HTTP, decoding, and database writes.

### Tests

Use `httptest`; unit tests must not depend on the live Gaia service.

- Pagination and batch boundaries.
- Rate limiting, retry, malformed response, timeout, and cancellation.
- Full cache hit, partial hit, and cache-only miss.
- Partial-cell first query followed by a disjoint request in the same cell.
- Shallower/deeper magnitude limits and changed server-side constraints.
- Duplicate IDs across overlapping cells.
- No XP request for locally rejected or unmatched stars.
- A second identical calculation performs no unnecessary network request.

### Acceptance

- Only encountered field data is downloaded.
- Cache-only behavior is deterministic and network failures never corrupt
  completed cache coverage.

## Step 12. Implement Gaia matching, synthetic photometry, and robust fitting

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `internal/processing/gaia_calibration.go`
- `internal/processing/star_extracting.go` consumers or a new aperture
  photometry helper
- `internal/processing/wcs_align.go` helpers only where needed
- versioned passband/throughput assets
- focused processing tests

### Implementation

- Detect suitable unsaturated stars using existing extraction primitives.
- Measure repeatable aperture/annulus fluxes at common matched positions in
  every aligned linear channel; do not rely on independently detected blob
  flux as final photometry.
- Propagate Gaia positions from their reference epoch to FITS observation time
  using proper motion.
- Crossmatch deterministically with documented radius and tie-breaking.
- Reject saturation, blends, low SNR, variables/contaminated sources,
  ambiguous matches, weak XP data, and poor color coverage.
- Integrate each Gaia XP spectrum through the exact versioned channel
  throughput/passband to predict observed band flux.
- Fit relative gains robustly in log-ratio space, iteratively reject outliers,
  require a minimum accepted-star count and color span, and remove arbitrary
  global luminance scale.
- Compose Gaia gains after optional background subtraction.
- For a Calibrated Linear overlay, fit only an independent scalar and only when
  its exact passband lies inside supported Gaia XP coverage. Otherwise return
  a layer-specific unsupported result.

### Tests

- Aperture/annulus photometry with known background, saturation, and blends.
- Synthetic WCS star fields with translation/rotation and known gains.
- Proper-motion epoch propagation.
- Ambiguous match and deterministic tie handling.
- Outliers, insufficient stars, insufficient color span, and cancellation.
- Golden XP/passband integrations.
- Supported overlay scalar and unsupported passband.
- Provider record order does not affect the fit.

### Acceptance

- Injected gains are recovered within a documented tolerance.
- Underconstrained or low-quality fields never publish a valid calibration.

## Step 13. Integrate Gaia mode into Compose

**Status: Complete** — implementation, full test suite, gofmt, go vet, and
independent review passed.

### Files

- `internal/ui/workspace_compose.go`
- Phase 1 model/state helpers
- related tests

### Implementation

Extend the Phase 1 dialog with:

- Gaia mode;
- Online or Cache Only policy;
- cache path/size/status and clear-cache action;
- Gaia endpoint/release display;
- staged progress: detect, query, fetch spectra, photometry, fit, save, render;
- accepted/rejected match diagnostics and fitted gains;
- clear offline-miss, insufficient-star, and unsupported-passband results.

Use the Phase 1 job-generation guard. Persist the fitted transform, source IDs,
Gaia release, query/quality configuration, passband versions, diagnostics, and
fingerprint, but not full spectra.

Changing WCS, input pixels, filters, mapping, observation epoch, Gaia release,
quality settings, passband assets, ROI, overlay mapping, or algorithm version
marks the result stale.

### Tests

- Gaia project round-trip and render without network/cache.
- Stale-on-relevant-change table.
- Cancel during detection, HTTP, DB write, photometry, and fit.
- Late HTTP completion cannot publish over a newer state.
- Cache path/settings UI state does not enter rendered results except where it
  changes provider/release provenance.

### Acceptance

- A valid saved Gaia calibration remains renderable offline.
- First-use and cached-use states are clear and cancellable end to end.

## Step 14. Phase 2 documentation and validation gate

**Status: Complete** — implementation, full test suite, gofmt, go vet, race
checks for Gaia packages, and independent review passed.

### Files

- `docs/user-guide.md`
- developer documentation for provider/cache schema and migrations
- `docs/unit-test-audit.md`
- all affected tests

### Documentation

Document:

- that the implementation is SPCC-like, not PixInsight code;
- Gaia network/privacy behavior;
- cache location, incremental growth, deduplication, migrations, deletion, and
  rebuild behavior;
- Online versus Cache Only;
- supported XP wavelength/passband limits;
- proper-motion handling and quality thresholds;
- diagnostic/provenance fields;
- why Gaia can fit an overlay scalar but cannot choose its artistic tint.

### Validation

Run narrow catalog and processing tests first, then:

- `go test ./internal/catalog/gaia`
- `go test ./internal/processing`
- `go test ./internal/models`
- `go test ./internal/ui`
- `go test ./...`
- race testing for the catalog/cache and calculation packages where supported

Manual acceptance must cover first online fetch, repeat cache hit, overlapping
field deduplication, cache-only hit/miss, cancellation at each stage,
insufficient stars, supported/unsupported overlay passbands, project
save/reload without network, and preview/export equality.

### Phase 2 exit criteria

- Only needed Gaia sources/spectra are fetched and cached.
- Repeated/overlapping queries demonstrably reuse cached records.
- No partial or cancelled operation publishes a transform or falsely completes
  a cache cell.
- A saved valid result is reproducible without the cache or network.
- Operational limits and unsupported passbands are explicit.

## Post-plan correction: official ESA endpoint guard

**Status: Complete** — independent review, `gofmt`, `go test ./...`, `go vet
./...`, and `git diff --check` passed.

The first Gaia UI implementation configured the official ESA TAP endpoint for
an HTTP client that implements a separate JSON-adapter contract. This caused
invalid `/tap/sources` requests and HTTP 500 responses. The provider now
rejects that incompatible online configuration before any request, preserving
compatible JSON-adapter and cache-only modes and reporting the required action.

This guard remains as a defense for the JSON-adapter provider. Native official
ESA support now routes the public endpoint through a TAP source-discovery
client and the separate DataLink XP sampled-spectrum path; custom JSON adapters
continue to use the `/sources` and `/spectra` contract.

## Implementation-agent sequencing

Use one `implementer` assignment per numbered step. Steps may be grouped only
when they touch the same narrow files and the preceding acceptance gate has
passed.

After every implementation step:

1. run the narrowest relevant tests;
2. have a `code_reviewer` inspect the actual diff and tests;
3. resolve review findings before starting the dependent step;
4. update this plan if implementation evidence changes a later assumption.

Do not begin Phase 2 until the Phase 1 exit criteria pass. Within Phase 2,
complete provider contracts before the SQLite and remote implementations, and
complete both provider paths before integrating the Gaia fitter into the UI.
