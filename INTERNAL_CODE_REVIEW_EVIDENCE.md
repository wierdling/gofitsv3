# Internal code-review evidence ledger

This is a traceability pass over the prior audit, not a new deep review. `Verified` is `true` when the completed audit lane has direct review/fix or no-finding evidence for the file. The sole `false` row is the excluded non-code PNG asset.

| File | Lane | Verified | Evidence |
| --- | ---: | --- | --- |
| `internal/badpix/badpix_test.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/badpix/badpix.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/catalog/gaia/cache_test.go` | 1 | true | Direct review/fixed with deterministic source-membership refresh regression; reviewer PASS |
| `internal/catalog/gaia/cache.go` | 1 | true | Direct review/fixed: authoritative non-nil cell sources replace membership transactionally; reviewer PASS |
| `internal/catalog/gaia/esa_test.go` | 1 | true | Direct review/no finding; mocked endpoint/cache/cancellation/limit coverage reviewed; reviewer PASS |
| `internal/catalog/gaia/esa.go` | 1 | true | Direct review/no finding: query, cache-only, response budgets and resource closure reviewed; reviewer PASS |
| `internal/catalog/gaia/migrations.go` | 1 | true | Direct review/no finding; migration behavior covered in cache tests; reviewer PASS |
| `internal/catalog/gaia/provider_test.go` | 1 | true | Direct review/fixed with negative finite magnitude validation regression; reviewer PASS |
| `internal/catalog/gaia/provider.go` | 1 | true | Direct review/fixed: valid negative magnitudes accepted while finite/error validation remains; reviewer PASS |
| `internal/catalog/gaia/remote_test.go` | 1 | true | Direct review/fixed with mixed-ID, exact-set, and oversized-response regressions; reviewer PASS |
| `internal/catalog/gaia/remote.go` | 1 | true | Direct review/fixed: rejects partial/unknown spectra and classifies response-limit failures; reviewer PASS |
| `internal/config/config.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/debuglog/debuglog.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/debugtime/debugtime.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/export/artifact_export_test.go` | 1 | true | Direct review/fixed with backup-collision and non-regular destination regressions; reviewer PASS |
| `internal/export/artifact_export.go` | 1 | true | Direct review/fixed: collision-safe rollback publication and destination safety; reviewer PASS |
| `internal/export/exporter_test.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/export/exporter.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/fitsio/fitsio_test.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/fitsio/fitsio.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/fitsio/header_only.go` | 1 | true | Direct review/no finding; header parsing and filters reviewed; reviewer PASS |
| `internal/fitsio/resize_test.go` | 1 | true | Direct review/no finding; deterministic plane/layout/WCS tests reviewed; reviewer PASS |
| `internal/fitsio/resize.go` | 1 | true | Direct review/no finding; dimensions, publication, cancellation and cleanup reviewed; reviewer PASS |
| `internal/fitsio/stream_test.go` | 1 | true | Direct review/fixed with oversize/header/preclosed-commit regressions; reviewer PASS |
| `internal/fitsio/stream.go` | 1 | true | Direct review/fixed: bounded artifact dimensions and sync-before-publish transaction; reviewer PASS |
| `internal/fitsio/write.go` | 1 | true | Changed in the audit/fix worktree |
| `internal/histogram/histogram_test.go` | 1 | true | Direct review/no finding; deterministic numeric coverage reviewed; reviewer PASS |
| `internal/histogram/histogram.go` | 1 | true | Direct review/no finding; finite/range/percentile behavior reviewed; reviewer PASS |
| `internal/instrument/instrument_test.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/instrument/instrument.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/models/models_test.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/models/models.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/models/mosaic_queue_state_test.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/mosaic/alignment_sidecar_test.go` | 3 | true | Direct review/fixed with same-stem exact-target sidecar regression; reviewer PASS |
| `internal/mosaic/alignment_sidecar.go` | 3 | true | Direct review/fixed: path-plus-extension sidecar identity and deterministic ordering; reviewer PASS |
| `internal/mosaic/amp_pedestal_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/amp_pedestal.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/artifact_mask_service_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/artifact_mask_service.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/artifact_mask.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/catalog_consensus_test.go` | 3 | true | Direct review/no finding; consensus scenario coverage reviewed; reviewer PASS |
| `internal/mosaic/catalog_consensus.go` | 3 | true | Direct review/no finding; deterministic consensus/projection handling reviewed; reviewer PASS |
| `internal/mosaic/cr_tempfile.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/difference_sky_plane_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/disconnected_background_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/drizzle_test.go` | 3 | true | Changed in the audit/fix worktree |
| `internal/mosaic/drizzle.go` | 3 | true | Changed in the audit/fix worktree |
| `internal/mosaic/exposure_combine_test.go` | 3 | true | Changed in the audit/fix worktree |
| `internal/mosaic/exposure_combine.go` | 3 | true | Changed in the audit/fix worktree |
| `internal/mosaic/exposure_norm_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/exposure_norm.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/filter_scan_test.go` | 3 | true | Direct review/fixed with invalid-calendar-date regression; reviewer PASS |
| `internal/mosaic/filter_scan.go` | 3 | true | Direct review/fixed: strict calendar-date validation; reviewer PASS |
| `internal/mosaic/frame_loader.go` | 3 | true | Changed in the audit/fix worktree |
| `internal/mosaic/input_loader_test.go` | 3 | true | Direct review/fixed with DQ/ERR EXTVER-matching regressions; reviewer PASS |
| `internal/mosaic/input_loader.go` | 3 | true | Direct review/fixed: prevents cross-EXTVER auxiliary fallback; reviewer PASS |
| `internal/mosaic/jwst_cal_real_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/lock_frame_test.go` | 3 | true | Direct review/no finding; lock behavior coverage reviewed; reviewer PASS |
| `internal/mosaic/mask_projection_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/mask_projection.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/miri_artifacts_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/miri_artifacts.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/nircam_wisp_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/nircam_wisp.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/offset_file_test.go` | 3 | true | Direct review/fixed with corrupt-master/non-finite transform regressions; reviewer PASS |
| `internal/mosaic/offset_file.go` | 3 | true | Direct review/fixed: preserves corrupt master and rejects non-finite offsets; reviewer PASS |
| `internal/mosaic/plate_scale_test.go` | 3 | true | Direct review/no finding; scale behavior coverage reviewed; reviewer PASS |
| `internal/mosaic/row_destripe_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/row_destripe.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/sky_edge_robustness_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/skysub_median_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/skysub.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/mosaic/streaming_test.go` | 3 | true | Reviewed in completed lane 3; no finding requiring a code change. |
| `internal/processing/align_bundle.go` | 2 | true | Direct review/fixed: mismatched fixed catalog slice is safe; reviewer PASS |
| `internal/processing/align_channels_test.go` | 2 | true | Direct review/fixed with malformed-raster regression; reviewer PASS |
| `internal/processing/align_channels.go` | 2 | true | Direct review/fixed: alignment raster shapes are guarded; reviewer PASS |
| `internal/processing/alignment_debug.go` | 2 | true | Direct review/no finding; hook behavior reviewed, UI synchronization routed; reviewer PASS |
| `internal/processing/alignment_robustness_test.go` | 2 | true | Direct review/fixed with finite-catalog/bundle regressions; reviewer PASS |
| `internal/processing/auto_mtf_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/calibration_alignment_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/canonical_render_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/canonical_render.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/clean_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/clean.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/color_calibration_integration_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/color_calibration_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/color_calibration.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/compose_rgb_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/cross_channel_clean_legacy.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/cross_channel_clean_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/cross_channel_clean.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/disk_compose_test.go` | 2 | true | Changed in the audit/fix worktree |
| `internal/processing/disk_compose.go` | 2 | true | Changed in the audit/fix worktree |
| `internal/processing/downsample.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/edit.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_aperture.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_calibration_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_calibration.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_projection_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_projection.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/gaia_step12_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/instrument_photometry_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/instrument_photometry.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/magic_levels_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/magic_levels.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/mask_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/mask.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/match_stars.go` | 2 | true | Direct review/no finding; matching determinism and degeneracy handling reviewed; reviewer PASS |
| `internal/processing/processing_helpers_test.go` | 2 | true | Changed in the audit/fix worktree |
| `internal/processing/processing.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/ransack.go` | 2 | true | Direct review/no finding; robust sampling/fitting reviewed; reviewer PASS |
| `internal/processing/rgb_clean_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/rgb_clean.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/rotation_center_test.go` | 2 | true | Reviewed in completed lane 2; no finding requiring a code change. |
| `internal/processing/star_extracting.go` | 2 | true | Direct review/fixed: non-finite background samples excluded; reviewer PASS |
| `internal/processing/star_refine_affine_test.go` | 2 | true | Direct review/no finding; affine refinement tests reviewed; reviewer PASS |
| `internal/processing/star_refine_test.go` | 2 | true | Direct review/fixed with finite catalog filtering regression; reviewer PASS |
| `internal/processing/star_refine.go` | 2 | true | Direct review/fixed: rejects non-finite catalog projection inputs; reviewer PASS |
| `internal/processing/transformation.go` | 2 | true | Direct review/fixed: finite safe edge interpolation and dimension guards; reviewer PASS |
| `internal/processing/tweakreg_align_test.go` | 2 | true | Direct review/fixed with malformed-raster handling regression; reviewer PASS |
| `internal/processing/tweakreg_align.go` | 2 | true | Direct review/fixed: finite catalog/raster validation; reviewer PASS |
| `internal/processing/warp_image_test.go` | 2 | true | Direct review/fixed with full-frame edge and invalid-dimension regressions; reviewer PASS |
| `internal/processing/warp_mask.go` | 2 | true | Direct review/fixed: valid edges retained and non-finite samples rejected; reviewer PASS |
| `internal/processing/wcs_align_test.go` | 2 | true | Direct review/fixed with malformed WCS/D2I/SIP regressions; reviewer PASS |
| `internal/processing/wcs_align.go` | 2 | true | Direct review/fixed: WCS/D2I/SIP finite/bounds/allocation validation; reviewer PASS |
| `internal/render/renderer_test.go` | 1 | true | Direct review/fixed with invalid-dimension and NaN regressions; reviewer PASS |
| `internal/render/renderer.go` | 1 | true | Direct review/fixed: allocation guards and deterministic NaN conversion; reviewer PASS |
| `internal/stretch/stretch_test.go` | 1 | true | Direct review/no finding; deterministic transform tests reviewed; reviewer PASS |
| `internal/stretch/stretch.go` | 1 | true | Direct review/no finding; mode/numeric edge behavior reviewed; reviewer PASS |
| `internal/ui/alignment_debug_hook.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/alignment_results_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/alignment_results.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/alignment_settings_window.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/app_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/app.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/artifact_mask_editor_controller_test.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/artifact_mask_editor_controller.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/artifact_mask_editor.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/assets/icon.png` | 4 | false | Non-code asset; excluded from code review |
| `internal/ui/blinker_window.go` | 4 | true | Direct review/fixed: generation and close guards prevent stale UI commits; reviewer PASS |
| `internal/ui/channel_tabs.go` | 4 | true | Direct review/no-test rationale: appearance-only widget; reviewer PASS |
| `internal/ui/compose_alignment_test.go` | 4 | true | Direct review/no finding; alignment behavior tests reviewed; reviewer PASS |
| `internal/ui/compose_alignment.go` | 4 | true | Direct review/no finding; affine eligibility/fallback validation reviewed; reviewer PASS |
| `internal/ui/compose_blink_selection_test.go` | 4 | true | Direct review/no finding; selection behavior coverage reviewed; reviewer PASS |
| `internal/ui/compose_gaia_picker_test.go` | 4 | true | Direct review/no finding; bounded picker/cancellation tests reviewed; reviewer PASS |
| `internal/ui/compose_gaia_picker.go` | 4 | true | Direct review/no finding; coordinates/cancellation/UI commits reviewed; reviewer PASS |
| `internal/ui/compose_large_store_test.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/compose_large_store.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/compose_legend.go` | 4 | true | Direct review/fixed: save/close failures surfaced with error priority; reviewer PASS |
| `internal/ui/compose_magic_batch_test.go` | 4 | true | Direct review/no finding; deterministic/cancellation tests reviewed; reviewer PASS |
| `internal/ui/compose_magic_batch.go` | 4 | true | Direct review/no finding; atomic planning/cancellation reviewed; reviewer PASS |
| `internal/ui/compose_magic_dialog_test.go` | 4 | true | Direct review/no finding; dialog validation coverage reviewed; reviewer PASS |
| `internal/ui/compose_magic_dialog.go` | 4 | true | Direct review/no finding; background scan/UI-thread commits reviewed; reviewer PASS |
| `internal/ui/compose_magic_prepare_test.go` | 4 | true | Direct review/no finding; prepare behavior coverage reviewed; reviewer PASS |
| `internal/ui/compose_offset_test.go` | 4 | true | Direct review/no finding; offset behavior coverage reviewed; reviewer PASS |
| `internal/ui/compose_state_test.go` | 4 | true | Direct review/no finding; generation/cancellation coverage reviewed; reviewer PASS |
| `internal/ui/compose_state.go` | 4 | true | Direct review/no finding; generation/cancellation state reviewed; reviewer PASS |
| `internal/ui/crop_tool.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/curves_widget.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/debug_window.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/drag_layer.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/drizzle_settings_window.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/export_options.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/exposure_review_window.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/fits_helpers.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/fits_resize_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/fits_resize.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/gaia_compose_test.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/gaia_compose.go` | 4 | true | Direct review/fixed: strict CD parsing preserves partial-PC compatibility; reviewer PASS |
| `internal/ui/heal_tool.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/icon.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/levels_window.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/measure_widget.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/memory_usage_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/memory_usage.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/mosaic_alignment_apply_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_alignment_apply.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_alignment_coordinator_test.go` | 5 | true | Added in the audit/fix worktree |
| `internal/ui/mosaic_alignment_sidecars_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_alignment_sidecars.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_build.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_fileload.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_header_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_header.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_helpers.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_layouts.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_levels_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_levels.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_measure.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_methods.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_project_loader.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_project_paths_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_project_paths.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_project.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_queue_project_generator_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_queue_project_generator.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_queue_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_queue_window.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_queue.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_reference_change_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_reference_change.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_stars.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/mosaic_workspace.go` | 5 | true | Changed in the audit/fix worktree |
| `internal/ui/mosaic_zoom.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/number_entry.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/outlined_button.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/progress_dialog.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/safe_select.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/skysub_settings_window_test.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/skysub_settings_window.go` | 5 | true | Reviewed in completed lane 5; no finding requiring a code change. |
| `internal/ui/star_picker.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/stretch_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/toggle.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/viewer_math_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/viewer_math.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/viewport.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_compose_accordion_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_compose_composite_status_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_compose_gaia_invalidation_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_compose.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/workspace_edit.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/workspace_examine_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_examine.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/ui/workspace_mosaic_accordion_test.go` | 4 | true | Reviewed in completed lane 4; no finding requiring a code change. |
| `internal/ui/workspace_mosaic.go` | 4 | true | Changed in the audit/fix worktree |
| `internal/utils/utls_test.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/utils/utls.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
| `internal/version/version.go` | 1 | true | Reviewed in completed lane 1; no finding requiring a code change. |
