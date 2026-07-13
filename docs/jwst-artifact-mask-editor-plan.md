# JWST Artifact Mask Editor Plan

## Goal

Let a Mosaic user create and maintain binary FITS masks inside GoFitsV3, without
DS9, Python, or another external application.  The first delivered workflow is
for MIRI detector artifacts (showers, snowballs, bad streaks, and similar
user-identified regions).  It creates a mask for each affected calibrated
input, automatically configures the Mosaic project's mask directory, and makes
the next drizzle build use those masks.

The editor should be built as a reusable mask-authoring foundation.  A later
NIRCam row-destriping mode can reuse the display and selection tools, but it
must remain a separate output mode: a row-statistics exclusion mask has a
different purpose from a MIRI artifact-rejection mask.

## Decisions and constraints

- **Output convention:** each exported MIRI mask is a primary-HDU FITS image
  with the exact SCI width and height of its source input.  A finite non-zero
  pixel means "exclude"; zero means "keep".  Version 1 writes `1.0` for every
  excluded pixel because the current reader accepts float32 FITS images.
- **Directory:** default to `masks/` next to the saved Mosaic project JSON.
  Do not write generated files into the downloaded-data directory by default.
  Allow a user to choose another directory, creating it only after export is
  confirmed.
- **Portability:** persist the mask directory relative to the project whenever
  it is below the project directory.  Resolve it to an absolute path only while
  loading masks for a build.  An absolute user-selected directory remains
  absolute.  This avoids breaking a project moved together with its `masks/`
  folder.
- **Pipeline activation:** on a successful MIRI export, set
  `MIRIArtifactMask=true`, set `MIRIArtifactMaskDir`, and mark SkySub settings
  as configured.  Thus exported masks are used by the next build; merely
  writing a FITS file must not silently alter an unrelated project.
- **WCS:** inputs are assumed to have valid WCS.  Projection must use the same
  distortion-aware WCS and saved residual alignment transforms as drizzle, not
  a resized copy of a drizzled mask.
- **Scope:** a selection created on a mosaic is initially proposed for every
  overlapping input, but the user selects the affected frames before export.
  A selection created in single-frame mode affects only that input unless the
  user explicitly propagates it.
- **Safety:** never overwrite an existing mask without a confirmation that
  names the affected files.  Export is cancellable and runs off the Fyne UI
  thread.

## User workflow and UI

Add `Mosaic -> Create Artifact Masks...`.  It is enabled when at least one
non-reference input is loaded.  The editor opens as a resizable modal/window
with two source choices:

1. **Current Mosaic** (enabled only after a result exists): uses the stretched
   drizzle preview and its output coordinate grid.
2. **Selected Input Frame:** shows one loaded calibrated SCI frame.  The input
   selector uses existing input labels and does not include a reference-only
   input.

The layout is deliberately familiar to the existing Mosaic tools:

```
+--------------------------+----------------------------------------------+
| Source / frames          | image preview with pan, zoom, and mask overlay|
| - Mosaic or input frame  |                                              |
| - affected-frame list    |  translucent red = excluded                  |
| - selection tools        |                                              |
| - brush size / threshold |                                              |
| - Undo / Redo            |                                              |
| - mask statistics        |                                              |
|                          |                                              |
| [Preview export]         |                                              |
| [Export masks]           |                                              |
+--------------------------+----------------------------------------------+
```

Selection tools in the first release:

- freehand brush, with adjustable circular radius;
- rectangle and polygon regions;
- erase mode for every tool;
- a pixel-intensity range tool applied to a user-drawn rectangle, followed by
  connected-component selection; and
- grow/shrink (dilate/erode) by an integer pixel radius.

Assisted selection always creates a preview selection.  The user can add,
erase, undo, or reject it before committing.  There is no unattended shower or
snowball detector in this feature: diffuse real emission makes such automation
unsafe for a first release.

The preview shows a semi-transparent red overlay and reports both selected
pixels in the authoring view and the projected excluded-pixel count per input.
The final export review contains a checkbox for each proposed input, its name,
dimensions, overlap status, and masked-pixel count.  Inputs that have no
projected pixels are unchecked and labelled "no overlap".  Selecting an entry
shows its detector-space preview before export.

## Representation and projection

### Editable state

Store user edits as raster masks in their authoring grid for fast brushing and
thresholding.  Also record the authoring source, source input identity when
applicable, transform/version metadata, and the set of selected target inputs.
For Mosaic authoring, persist vector region operations where practical (brush
stroke samples, rectangles, polygons, operation type, and morphology values).
This allows masks to be regenerated after a drizzle rebuild instead of making
the FITS files the only editable record.

Persist this state in the Mosaic project as an optional `ArtifactMaskProject`
section.  Give each target a stable key made from the original source path plus
SCI extension, rather than its temporary working-file path.  Older projects
must load with an empty mask-editor state.

### Coordinate mapping

Do not transform a completed drizzle mask by image resize, affine-only WCS, or
nearest neighbour copying.  Those approaches fail at distorted detector edges
and ignore manual alignment corrections.

Extract a small internal `mosaic` mapping helper from the drizzle placement
code.  For any input it maps a 0-indexed detector pixel to the current output
canvas, using:

1. `processing.WCSMapper` with the input's available distortion information;
2. the same output WCS/origin/final scale used by the result; and
3. the input's saved residual offset/manual affine transform.

For each selected target input, generate its detector mask in a background
goroutine by mapping detector-pixel centers into the authoring grid and testing
whether the point lies in the committed authoring selection.  This inverse
rasterization direction guarantees that the output array has exactly the
target's SCI dimensions and avoids holes caused by forward-splatting regions.
Use a small conservative boundary expansion (documented and configurable only
internally in version 1) so a selected artifact boundary is not accidentally
left unmasked by sampling.

Single-frame authoring needs no WCS to export back to its own frame: copy the
detector-space mask exactly.  WCS is required only when the user chooses
propagation to other inputs.  Mosaic authoring requires a current result and
valid output WCS; show a clear error instead of generating an approximate mask
if either is absent.

Because a rebuild can change the canvas or alignment solution, the editor must
mark existing mosaic-authored projections as stale when input membership,
alignment transforms, drizzle scale, or result WCS changes.  It should offer
"Reproject before export" and never reuse a stale derived mask silently.

## FITS export contract

Create the output directory only after export confirmation.  For each chosen
MIRI input write:

`<input-stem>_miri_mask.fits`

where `<input-stem>` exactly matches `miriArtifactMaskName`'s source naming
rule.  This is essential: directory discovery in `loadMIRIArtifactMask` must
find the file without another mapping layer.

Write a float32 primary image through the existing FITS writer, using a cloned
source SCI header after removing/replacing structural cards.  Include compact
provenance cards where the writer supports them, for example `MASKTYPE`,
`MASKVER`, `MASKSRC`, `SCIEXT`, and a creation timestamp.  The source SCI WCS
should be retained for inspection, but the pipeline relies on dimensions and
pixel positions, not on this header.

Write each file to a temporary sibling name, close it, then rename it into
place.  If any target fails, retain the original masks and report exactly which
temporary/finished files need attention.  Do not partially update the project
settings unless every selected export succeeds.

## Implementation stages

### Stage 1 — Project path and mask-domain foundation

Files likely involved: `internal/models/models.go`,
`internal/ui/mosaic_project.go`, `internal/ui/mosaic_workspace.go`,
`internal/mosaic/miri_artifacts.go`, and new focused files under
`internal/mosaic/`.

1. Track the absolute current project JSON path in `mosaicWorkspace` when a
   project is saved or loaded.
2. Add helpers to encode/decode project-relative mask directories and use the
   resolved directory only when creating `mosaic.SkysubOptions` for a build.
3. Define model types for editable mask documents, target keys, source mode,
   raster/region operations, and the optional project field.  Keep JSON fields
   additive and omit empty state.
4. Add a mask service with pure functions for creating a zero mask, combining
   add/erase operations, morphology, target naming, validation, and atomic
   FITS export.  Reuse the current mask naming convention rather than duplicating
   it in the UI.
5. Add unit tests for path portability, old-project decoding, naming, binary
   semantics, atomic overwrite behavior, and malformed/dimension-mismatched
   target rejection.

### Stage 2 — Reusable drizzle-to-mask projection

Files likely involved: `internal/mosaic/drizzle.go`, a new
`internal/mosaic/mask_projection.go`, and `internal/processing/wcs_align.go`
only if a narrowly scoped exported mapper operation is necessary.

1. Factor the minimal mapping operation shared by drizzle placement and mask
   projection; do not expose or duplicate the full planning implementation.
2. Implement detector-to-authoring-grid projection with cancellation and
   periodic progress updates.
3. Rasterize rectangle, polygon, brush-stroke, and threshold-result masks
   conservatively; keep pure raster operations independent of Fyne.
4. Include input offset/manual-affine placement and validate that a known
   detector pixel lands in the same output location as drizzle.
5. Add synthetic WCS tests covering translation, rotation, SIP/distortion where
   current fixtures permit it, no-overlap, edge clipping, and manual offsets.
   Add a regression test that exported projected masks are accepted by
   `loadMIRIArtifactMask` and exclude the expected science pixels in a build.

### Stage 3 — Single-frame editor (vertical slice)

Files likely involved: new `internal/ui/artifact_mask_editor.go`, a custom
interaction layer beside `heal_tool.go`/`crop_tool.go`,
`internal/ui/mosaic_methods.go`, and `internal/ui/app.go` menu wiring.

1. Add the menu item and a selected-input chooser.  Lazy-load only the selected
   input's pixels using `ensureInputPixelsLoadedAt`.
2. Render its existing Mosaic stretch in a scrollable, zoomable view with a
   transparent mask overlay.
3. Implement brush, rectangle, polygon, erase, undo/redo, and masked-pixel
   count.  Keep mask compositing on a background worker when the image is large;
   refresh Fyne objects through `fyne.Do`.
4. Implement export review, default project `masks/` directory selection,
   atomic write, settings activation, and an immediate detector-size/readback
   validation.
5. Persist the editable single-frame document in the project.  Test UI state
   transitions through small controller tests; leave pixel geometry and FITS
   behavior in non-UI unit tests.

### Stage 4 — Assisted selection and per-exposure control

1. Add intensity-range selection limited to a user-supplied rectangle.
2. Add connected-component selection, grow/shrink controls, preview/accept,
   and undoable operations.  Treat NaN/invalid science pixels as unavailable.
3. Add target-frame selection and per-frame detector preview.  In this stage,
   propagation originates from a single frame and is explicitly opted into.
4. Preserve user target selections and modified per-frame masks separately so
   reprojecting one region does not discard deliberate detector-space edits.
5. Unit-test threshold bounds, components, morphology at image edges, target
   selection defaults, and cancellation.

### Stage 5 — Drizzle-mosaic authoring

1. Enable Current Mosaic source only for a current, WCS-valid result; use the
   exact preview geometry rather than a saved display screenshot.
2. Reuse the editor tools and map the committed mosaic selection into each
   overlapping input through Stage 2's mapping service.
3. Present the proposed target list and detector-space previews before export.
   Default all overlapping science inputs on, but never include a
   `ReferenceOnly` input.
4. Persist source/result identity and mark projections stale after relevant
   alignment/drizzle changes.  Require reproject/confirm before export.
5. Test mapping after changed scale/origin and a modified manual transform,
   plus a full editor-to-export-to-build integration case.

### Stage 6 — NIRCam row-destriping mode and documentation

1. Add a separate editor mode that exports a source-mask FITS for row-statistic
   exclusion.  Make its UI wording and review clear that it protects row median
   estimation; it is not a bad-pixel/artifact deletion mask.
2. Decide and implement a per-input/per-detector row-mask association.  The
   current single `RowDestripeMaskPath` cannot safely represent masks for
   unrelated inputs, so extend its options only when this mode is implemented.
3. Document the entry points, projection behavior, directory layout, overwrite
   handling, review controls, and limitations in `docs/user-guide.md`.
4. Add a concise troubleshooting section for stale projections, unavailable
   WCS, mask dimension errors, and artifacts that should instead be fixed by
   rerunning the STScI pipeline.

## Acceptance criteria

- A user can draw and edit a MIRI artifact mask inside GoFitsV3 from either one
  calibrated input or a completed Mosaic result.
- Export produces exact-size, binary-semantics FITS masks in the configured
  project `masks/` directory with discoverable per-input names.
- Successful export automatically enables the MIRI directory-mask setting, and
  a subsequent Mosaic build demonstrably excludes those pixels.
- A project saved and moved with its `masks/` directory continues to resolve
  relative paths correctly.
- Mosaic-derived masks respect WCS distortion and saved alignment adjustments;
  no direct drizzle-mask resizing is used.
- The UI stays responsive during projection/export, supports cancellation, and
  makes overwrites and per-input applicability explicit.

## Deferred work

- Automatic MIRI shower/snowball detection and subtraction.
- Editing STScI DQ arrays or rewriting calibrated science files.
- Multi-valued mask classes.  The existing pipeline needs only binary exclusion;
  semantic labels can be added later without changing the exported behavior.
- Region interchange formats (DS9, GeoJSON) and batch import/export.
