# Work status

Open TODO lists and the state of multi-session work. Plans themselves live in `PLAN.md` and `docs/*-plan.md`; this file says where each one stands so any session or model can resume. Update it when pausing or finishing an item. Dates are absolute.

## In progress

### Chip-selective loading (paused 2026-06)
Goal: every FITS data-load site reads only the chips/extensions it needs.

Done: shared helpers in `internal/mosaic/input_loader.go` (`chipDecodePredicate`, `loadChipFromDisk`, `loadCleanedSCIForExtraction`); Build reload `loadFrameFromDisk` (`frame_loader.go`) uses them. Guarded by `TestLoadCleanedSCIForExtractionMatchesFullLoad` and `TestLoadFrameFromDiskMatchesFullLoad`.

Remaining, in order:
1. Project load (`internal/ui/mosaic_project.go`, inputs loop and reference): calls `mosaic.LoadInputsFromPath` per saved input, loading the whole file with pixels. Switch to `mosaic.LoadInputsMetadataFromPath` and pick the saved `SCIExt` chip; align/build stream pixels later. Verified safe: post-load only uses dims/status/preview-reset.
2. Set Reference Baseline (`internal/ui/workspace_mosaic.go`): `mosaic.LoadInputFromPath(path)` loads pixels for a reference that only needs metadata. Check nothing uses `referenceInput` pixels before align/build (`enterStarMode` uses input[0]).
3. `ensureInputPixelsLoadedAt` (`internal/ui/mosaic_methods.go`): single-input reload still loads the whole file; could use `loadChipFromDisk`. Low priority.
4. Review and probably leave: `blinker_window.go`, Compose channel load, `badpix/clean.go` use `fitsio.LoadFile` for a single displayed image.

Rule: prefer metadata-only load when pixels are not needed yet; use `loadChipFromDisk` when one chip's pixels are.

### Disk-backed Compose (large-file mode)
Plan and checkboxes: `PLAN.md`. Weighted multi-channel steps 1-3 done; step 4 (bounded disk-backed weighted composition) and step 5 (regression/docs) open. The nine-step completion plan below it is unstarted.

### NIRCam sky background matching
Plan: `docs/sky-background-matching-plan.md`. All three stages implemented as of 2026-07-12 (`computeDifferenceSkyPlanes` / `match+plane`, per-cell median overlap samples with outlier-edge dropping, amp pedestal + row destripe). Not yet validated together on the user's OMC3_SE NIRCam set. Revised diagnosis: the visible flat steps are dither-coverage boundaries, not amplifier boundaries; `match` pins each connected component to its own zero point, `globalmin+match` partially mitigates. Never reintroduce own-frame background plane fitting on nebula data.

### Exposure combine (multi-chip exposures into one working/ FITS before alignment)
Approved 2026-07-03 as six pause-safe stages; the plan file was kept outside the repo and is no longer present. Known design: drizzle an exposure's chips at load time into one combined FITS in `working/` beside the sources, with the WHT weight map as an IMAGE extension consumed verbatim by `drizzlePixelWeight` (WeightERR output weights are rate-space inverse variance). Status: stage 1 was implemented (per-exposure combine into cached working FITS, commit 2026-07-05); later stages need the plan rewritten into `docs/exposure-combine-plan.md` before continuing.

## Finished multi-session work (for orientation)
- Mosaic workspace refactor (2026-05-30): `workspace_mosaic.go` constructor plus `mosaicWorkspace` methods across `mosaic_*.go`. Conversion technique that stayed compiler-safe: promote a local to a `ws` field and delete the local so the compiler flags every use; convert closures to methods in dependency order; repoint call sites by whole-token replace (never replace bare `preview`, a substring of several identifiers).
- Compose workspace refactor (2026-09): see `docs/compose-workspace-refactor-plan.md` and the Large files section of `AGENTS.md`.
- Drizzle and alignment streaming (2026-06): inputs stream from disk, CR models use temp files, alignment runs on star catalogs extracted under a memory budget. Legacy warp modes (`RScale`, `GeneralAffine`) still need resident pixels.

## Audits
The 2026-10-29 cloud code review produced a numbered findings list (H1..H6 and others) that was not saved into the repo. When it is recovered or redone, put it in `docs/audit.md` with one checkbox per finding.
