package ui

import (
	"context"
	"fmt"
	"sync"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeCalibrationJob is the small, thread-safe state machine used by the
// Compose calibration dialog. It deliberately keeps calculation results out
// of the live project until Accept succeeds on the UI thread.
type composeCalibrationJob struct {
	mu         sync.Mutex
	generation uint64
	cancel     context.CancelFunc
	active     bool
}

func (j *composeCalibrationJob) begin(generation uint64) (context.Context, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.active {
		return nil, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	j.generation, j.cancel, j.active = generation, cancel, true
	return ctx, true
}

func (j *composeCalibrationJob) cancelJob() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.active {
		return false
	}
	j.cancel()
	j.active = false
	return true
}

func (j *composeCalibrationJob) finish(generation uint64) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.active || j.generation != generation {
		return false
	}
	j.active = false
	j.cancel = nil
	return true
}

// composeCalibrationResultForUI applies a completed result only when the job
// generation and current settings still match. A stale completion is ignored.
func composeCalibrationResultForUI(job *composeCalibrationJob, generation, currentGeneration uint64, current, prior *models.ColorCalibrationState, result processing.CalibrationResult, err error) bool {
	if job == nil || current == nil || generation != currentGeneration || !job.finish(generation) {
		return false
	}
	if err != nil {
		if processing.IsUnsupportedPhotometry(err) || processing.IsUnsupportedCalibration(err) {
			if prior != nil {
				*current = *prior
				current.Overlays = append([]models.OverlayCalibrationState(nil), prior.Overlays...)
			}
			current.Status = models.CalibrationUnsupported
			current.Diagnostics.Message = err.Error()
			return true
		}
		if prior != nil {
			*current = *prior
			current.Overlays = append([]models.OverlayCalibrationState(nil), prior.Overlays...)
			current.Diagnostics.Message = "Calculation failed; previous result retained: " + err.Error()
			return true
		}
		current.Status = models.CalibrationFailed
		current.Diagnostics.Message = err.Error()
		return true
	}
	if result.Status != models.CalibrationValid {
		current.Status = result.Status
		current.Diagnostics = result.Diagnostics
		return true
	}
	current.BaseTransforms = result.Base
	current.Overlays = append([]models.OverlayCalibrationState(nil), result.Overlays...)
	current.Status = models.CalibrationValid
	current.Diagnostics = result.Diagnostics
	current.Provenance = result.Provenance
	current.SourceFingerprint = result.SourceFingerprint
	current.SettingsFingerprint = result.SettingsFingerprint
	return true
}

// composeCalibrationSnapshot clones calibration state for rendering. The
// disabled flag only affects the snapshot, preserving valid transforms and
// provenance in the live project so they can be re-enabled without a redo.
func composeCalibrationSnapshot(state models.ColorCalibrationState, disable bool) *models.ColorCalibrationState {
	snapshot := state
	snapshot.Overlays = append([]models.OverlayCalibrationState(nil), state.Overlays...)
	for i := range snapshot.Overlays {
		snapshot.Overlays[i].Diagnostics.Warnings = append([]string(nil), state.Overlays[i].Diagnostics.Warnings...)
	}
	if disable {
		snapshot.Status = models.CalibrationDisabled
	}
	return &snapshot
}

// composeCalibrationPreviewSnapshot applies the transient Before/After choice
// only to interactive previews. Export and edit paths must use
// composeCalibrationSnapshot directly so a dialog comparison cannot alter
// exported calibration semantics.
func composeCalibrationPreviewSnapshot(state models.ColorCalibrationState, save bool, after *bool) *models.ColorCalibrationState {
	disable := !save
	if after != nil {
		disable = !*after
	}
	return composeCalibrationSnapshot(state, disable)
}

func updateComposeGaiaCachePath(state *models.ColorCalibrationState, path string) {
	if state != nil {
		state.Gaia.CachePath = path
	}
}

// retainComposeProjectGaiaCachePath keeps runtime-resolved cache metadata out
// of persisted project state while retaining an explicit project override.
func retainComposeProjectGaiaCachePath(project, resolved models.GaiaCalibrationSettings) models.GaiaCalibrationSettings {
	resolved.CachePath = project.CachePath
	return resolved
}

func markComposeCalibrationStale(state *models.ColorCalibrationState) {
	if state == nil || state.Status == models.CalibrationDisabled {
		return
	}
	state.Status = models.CalibrationStale
	state.Diagnostics.Message = "Settings or input changed; calculate again."
}

func composeCalibrationStatusText(state models.ColorCalibrationState) string {
	status := state.Status
	if status == "" {
		status = models.CalibrationDisabled
	}
	message := state.Diagnostics.Message
	if message == "" {
		message = map[models.CalibrationStatus]string{
			models.CalibrationDisabled:    "Calibration is off.",
			models.CalibrationCalculating: "Calculating…",
			models.CalibrationValid:       "Valid calibration.",
			models.CalibrationStale:       "Stale; calculate again.",
			models.CalibrationUnsupported: "Unsupported metadata or settings.",
			models.CalibrationCancelled:   "Cancelled; previous valid result retained.",
			models.CalibrationFailed:      "Calculation failed.",
		}[status]
	}
	if message == "" {
		message = fmt.Sprintf("Status: %s", status)
	}
	return fmt.Sprintf("Calibration: %s — %s", status, message)
}

func clearComposeOrigPixels(origPixels *[][]float32, idxs ...int) {
	if origPixels == nil {
		return
	}
	for _, idx := range idxs {
		if idx < 0 || idx >= len(*origPixels) {
			continue
		}
		(*origPixels)[idx] = nil
	}
}

// replaceComposeChannelImage installs a new RGB image while preserving the
// current channel orientation. Project loading and Reset Data intentionally
// restore their saved state directly instead.
func replaceComposeChannelImage(imgs []*models.LoadedImage, idx int, replacement *models.LoadedImage) {
	if idx < 0 || idx >= len(imgs) {
		return
	}
	rotation90 := 0
	if imgs[idx] != nil {
		rotation90 = imgs[idx].Rotation90
	}
	imgs[idx] = replacement
	restoreComposeChannelRotation(replacement, rotation90)
}

func rotateComposeChannel90CW(img *models.LoadedImage) {
	if img == nil {
		return
	}
	img.HDU.Data = processing.RotateImageData90CW(img.HDU.Data)
	img.Rotation90 = (img.Rotation90 + 1) % 4
}

func restoreComposeChannelRotation(img *models.LoadedImage, rotation90 int) {
	if img == nil {
		return
	}
	img.Rotation90 = 0
	for turns := rotation90 % 4; turns > 0; turns-- {
		rotateComposeChannel90CW(img)
	}
}

func clearComposeChannelAlignment(img *models.LoadedImage) {
	if img == nil {
		return
	}
	img.HasAlignTransform = false
	img.AlignA, img.AlignB, img.AlignC = 0, 0, 0
	img.AlignD, img.AlignE, img.AlignF = 0, 0, 0
}
