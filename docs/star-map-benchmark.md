# Star-map labeling benchmark

The benchmark is a separate local browser tool in `cmd/starmap`. It uses the same detector and mask rasterizer as GoFitsV3. It keeps human labels independent of the star-map reviewer's manual accept/reject decisions.

## Start with the Trifid F673N mosaic

From the repository root in PowerShell:

```powershell
go build -o tmp/starmap-benchmark.exe ./cmd/starmap
.\tmp\starmap-benchmark.exe benchmark -input "D:\goSource\GoFitsV3\TestImages\Trifid\F673N_drizzle.fits"
```

Open the printed address, normally `http://127.0.0.1:8787`. Keep the server running while labeling; Ctrl+C stops it. Restart with the same command to resume. No external services or additional browser packages are required.

### Open a different image without restarting

Expand **Open another FITS mosaic** at the top of the page. Enter a local FITS path, or use **Browse local files**, choose a file, and click **Load file**. The browser lists FITS files and subdirectories; enter a directory path or use **Parent directory** to navigate. Select a primary-image science mosaic, rather than a star-map mask or an original multi-extension exposure.

Pending annotation edits are saved before switching. Each image opens its own `working/benchmark/<mosaic stem>/` project, restoring its labels, regions and frozen runs when you return. If the initial command used `-dir`, that custom directory is remembered for the initial image during this server session. Other images use their default directories. A failed load leaves the current workspace active. Finish or cancel a detector run before loading another file.

Switching affects the server's active image, including other browser windows connected to it. Clean windows reload automatically; a window with unsaved edits offers **Export labels** before you reload it. Exports contain that window's local annotations, including unsaved edits, so edits cannot silently be sent to another image's workspace. Files that changed on disk cannot reuse labels tied to their earlier fingerprint; use a new mosaic name or a new benchmark directory for reprocessed science data.

The default baseline is `TestImages/Trifid/working/F673N_starmap.fits`. The default benchmark directory is:

```text
TestImages/Trifid/working/benchmark/F673N_drizzle/
  dataset.json                   exact science-file SHA-256 and dimensions
  labels.json                    current human annotations and revision
  history/labels-00000001.json    saved annotation revisions
  runs/<id>/starmap.fits          frozen map with FITS provenance
  runs/<id>/run.json              catalog, settings and run identity
  reports/<id>.json               scores plus the exact labels scored
```

Original science images and existing working star maps are never overwritten by the benchmark. Each frozen F673N map takes about 212 MB. Back up the benchmark directory, not just a downloaded report.

Use `-dir` to choose a separate project, `-listen 127.0.0.1:8789` for another port, or `-maps "mapA.fits,mapB.fits"` to import additional compatible maps. Import checks the map against the science pixels, grid, and recorded native inputs. Existing imports are deduplicated by FITS SHA-256. If the ordinary working map is absent on resume, a frozen map supplies the evidence. The server accepts loopback addresses only and locks its directory against another server. After a crash, confirm the PID in `server.lock` is no longer running before removing that stale lock.

## Label before revealing detections

1. Start with a **development** region. Twelve nonoverlapping, evenly spaced regions are provided without using the detector catalog to choose them: eight development and four validation regions. To add one visually, enable **Draw new region on overview** and drag a rectangle on the overview image. Its proposed bounds populate **Add a region**; choose a name and development/validation split, then click **Add region**. The new region must be 16–2048 pixels per side and cannot overlap existing regions. You can also enter bounds numerically. Escape cancels a drawing gesture. Add or replace regions before revealing detections to cover bright nebulosity, dark sky, crowded areas and saturated stars. The initial grid alone is not guaranteed to cover every difficult case.
2. Adjust **Stretch** and **Zoom**. Use 200–400% zoom when centering a label. Coordinates are zero-based FITS pixels; x increases rightward and y upward. Stretching affects only the display. Purple image pixels indicate nonfinite science values.
3. Click every **certain star**, including faint and blended stars you can distinguish. Add **certain nonstar** marks to compact nebular knots or artifacts, and **ambiguous** or **unusable** marks to objects you cannot classify reliably. These two uncertain classes are point annotations, not exclusion polygons. Use the star tags for bright, saturated, on-nebula and blended objects.
4. Click an existing mark to edit its class, tags or note. **Drag an existing point label** to reposition it; its ID, class, tags and note are retained. Movement starts after a small pointer-motion threshold, commits on release and stays inside the region. Escape or a cancelled pointer gesture discards the unfinished move. **Select / inspect label** also selects a protected rectangle. Delete removes the selected mark; saved revisions retain earlier annotations. Shift-click bypasses proximity selection to add a close companion.
5. Use **Protect nebula** and drag rectangles across areas that must not receive a star mask. Keep real stars and their intended footprints outside these rectangles. Overlapping rectangles count only once.
6. Check **All stars in this region are labeled** only after inspecting the entire region. An unchecked region is excluded from source and protected-area scores. Labels autosave after edits; **Save labels** completes pending saves. Wait for **All labels saved** before exporting or closing.
7. After labeling, **Reveal run A detections** displays automatically accepted footprint circles. The first reveal is recorded permanently for that region. Geometry and split become fixed. Labels remain editable, but edits after reveal and reveals before full review are flagged in reports.

Keyboard shortcuts outside text fields: **1–4** select the object classes, **5** selects protected rectangles, **6** selects existing marks, and **Delete** removes the selected mark.

### Centering a label with a touchpad

Select a **star** label, then click **Preview star center** above the image. The default search radius is 10 science pixels; choose 3, 6 or 12 pixels when appropriate. A yellow target and connecting line show the proposed center and its displacement. **Use proposed center** applies the move and autosaves it. **Cancel preview** or Escape leaves the label unchanged. Zooming keeps the selected label or proposal in view.

This optional pointing aid searches for the strongest nearby smoothed peak and estimates a background-subtracted centroid from linear science pixels. It does not consult the detector catalog, reveal detections, classify an object, or change the matching radius. Inspect each preview: a brighter neighbor, compact nebular knot, blend or saturated core can bias a centroid. Reduce the search radius or drag manually when it chooses the wrong feature. Flat, low-contrast, invalid or unsupported edge areas may yield no suggestion. Accepted moves retain label metadata and go through the normal revision history and edits-after-reveal audit. Existing frozen reports do not change; evaluate again after correcting labels.

Keep validation detections and scores hidden while tuning. The tool records exposure; it does not prevent you from looking at held-out results. For a defensible hold-out evaluation, finish its labels before reveal, then avoid tuning to those results. Do not move difficult cases out of the benchmark to improve a score. Store any changed sampling protocol with a new benchmark version.

## Compare detector runs

**Create a detector run** lets you vary minimum SNR, maximum residual and FWHM. FWHM 0 requests automatic estimation. Processing runs in a cancellable background job. The **Verify against original exposures** checkbox is off by default. Unchecked runs use only the mosaic, bypassing both native profile confirmation and native saturation verification. When available, checking it uses original evidence recovered from the imported baseline and requires two independent confirmations. Native file size/timestamps are checked before and after verified runs only. The checkbox is disabled when evidence is unavailable. Saved runs retain their evidence mode and requested verification choice; existing runs are unchanged. A changed science file requires a new benchmark directory on restart.

Select runs A and B, a split, and a matching radius; **Evaluate / compare** scores both against one saved annotation snapshot. Development is the default split. Validation scoring explicitly reveals the held-out results. Reports remain on disk and can also be downloaded. Error buttons navigate to their region and position for inspection.

Generated runs record their settings, requested and measured FWHM, executable SHA-256, available Git revision/dirty status, FITS SHA-256 and evidence provenance. An imported FITS cannot establish which detector executable originally created it; its metadata states that limitation. Scoring uses automatic `Status`, ignoring manual accept/reject overrides. Imported footprint radii are scored as saved because earlier radius edits cannot be reconstructed. Use freshly generated automatic maps for clean comparisons.

## What the scores mean

- **True positives:** one-to-one matches between automatically accepted detections and certain-star labels within the chosen pixel radius. Matching maximizes the number of matches; duplicate detections cannot share a truth star.
- **False positives:** unmatched accepted detections in fully reviewed regions, including detections on labeled nonstars. A duplicate near an already matched star is also a false positive.
- **Missed stars:** certain-star labels without an accepted match.
- **Unresolved:** unmatched selections close only to ambiguous/unusable labels. These are not silently removed from the headline precision denominator.
- **Precision lower bound:** TP / all accepted selections in scored regions. The JSON also includes conditional precision TP / (TP + FP), which excludes unresolved selections. The lower bound describes uncertainty in the labels; it is not a statistical confidence interval.
- **Recall:** TP / certain-star labels. Category recall is also reported for bright, saturated, on-nebula and blended labels. Categories may overlap.
- **Protected-area leakage:** number of finite protected pixels with mask weight greater than 0.05, plus summed/mean mask weight. This uses the production rasterizer and includes footprints centered outside the labeled region. No protected pixels means no leakage measurement.

Source centers and truth labels within one matching radius of a region boundary are excluded from source matching; the report counts excluded labels. Only centers in the remaining inset are matched. Inspect close pairs straddling this scoring boundary when interpreting individual misses. Zero-denominator metrics appear as a dash/null, not a perfect or zero score.

An exhaustive checkbox is a human assertion, not something the tool can verify. An unlabeled real star in a checked region will be scored as a false positive. Ambiguous labels, boundary choices, blends and incomplete sampling limit what the numbers establish. A small labeled set does not certify the proposed 99% precision target; expand and independently review the held-out sample before making that claim.

## Implementation verification

Focused Go tests cover one-to-one matching, duplicates, missed and uncertain objects, ignored review overrides, category recall, incomplete/boundary regions, protected-area union, external footprints, NaNs, invalid data, save conflicts, reveal history, split isolation and display orientation. The package passes `go test -race ./internal/starbench`.

The F673N native-evidence replay was checked against the imported baseline: 117 accepted and 1,145 uncertain candidates. Browser smoke tests used a separate `tmp/benchmark-smoke` directory for object labeling, metadata, autosave/reload, protected rectangles, reveal history and two-run comparisons. Those scratch labels and scores are UI test fixtures, not scientific ground truth. The default user benchmark starts unreviewed and unlabeled.

Mosaic detection now includes a conservative saturated-star rescue at two spatial scales. It requires a bright damaged core, a robust stellar-wing fit, and two approximately perpendicular diffraction-spike pairs persisting through outer annuli. Such sources carry the reason `mosaic-inferred saturation with stellar wings`; the saturation flag is morphological evidence, not a native detector measurement. The enlarged circular footprint covers the core/halo, not the full diffraction spikes. Border stars and saturated stars without adequate spike evidence can remain uncertain. Original-exposure verification, if selected, still applies to these candidates.

The mosaic rescue accepts a factor-of-two core/wing mismatch in either direction: a suppressed core or a bright reconstructed core. Both still require stellar wings and diffraction evidence. Accepted saturated-wing fits take precedence over competing ordinary core fits during duplicate suppression.
