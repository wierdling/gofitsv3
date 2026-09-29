package ui

import (
	"gofitsv3/internal/models"
	"gofitsv3/internal/stretch"
)

func channelStateFromImage(img *models.LoadedImage) models.ChannelState {
	return models.ChannelState{
		Path:       img.Path,
		Mode:       modeToLabel(img.Mode),
		Black:      img.Black,
		White:      img.White,
		Background: img.Background,
		Peak:       img.Peak,
		ScaledPeak: img.ScaledPeak,
		ShowClip:   img.ShowClip,
		Rotation90: img.Rotation90,

		HasAlign: img.HasAlignTransform,
		AlignA:   img.AlignA,
		AlignB:   img.AlignB,
		AlignC:   img.AlignC,
		AlignD:   img.AlignD,
		AlignE:   img.AlignE,
		AlignF:   img.AlignF,

		AsinhScale:  img.AsinhScale,
		MTFMidtone:  img.MTFMidtone,
		GHSStretch:  img.GHSStretch,
		GHSLocal:    img.GHSLocal,
		GHSSymmetry: img.GHSSymmetry,

		StarStretch: starStretchStateForProject(img.StarStretch),
	}
}

// starStretchStateForProject persists the setting only when it is on, so
// projects that never used it are unchanged.
func starStretchStateForProject(s models.StarStretchState) *models.StarStretchState {
	if !s.Enabled {
		return nil
	}
	copy := s
	return &copy
}

func starStretchStateFromProject(s *models.StarStretchState) models.StarStretchState {
	if s == nil {
		return models.StarStretchState{}
	}
	return *s
}

func applyChannelState(idx int, state models.ChannelState, imgs []*models.LoadedImage, views []*viewport, controls []*models.ChannelControl) {
	img := imgs[idx]
	if img == nil {
		return
	}
	img.Mode = labelToMode(state.Mode)
	img.Black = state.Black
	img.White = state.White
	img.Background = state.Background
	img.Peak = state.Peak
	img.ScaledPeak = state.ScaledPeak
	img.ShowClip = state.ShowClip

	// Restore the full star-alignment affine (applied underneath the Manual Offset
	// at render time). A freshly loaded/reset channel carries HasAlign=false.
	img.HasAlignTransform = state.HasAlign
	img.AlignA, img.AlignB, img.AlignC = state.AlignA, state.AlignB, state.AlignC
	img.AlignD, img.AlignE, img.AlignF = state.AlignD, state.AlignE, state.AlignF
	img.AsinhScale = state.AsinhScale
	img.MTFMidtone = state.MTFMidtone
	img.GHSStretch = state.GHSStretch
	img.GHSLocal = state.GHSLocal
	img.GHSSymmetry = state.GHSSymmetry
	img.StarStretch = starStretchStateFromProject(state.StarStretch)

	controls[idx].ModeSelect.SetSelected(modeToLabel(img.Mode))
	controls[idx].BackgroundEntry.SetValue(img.Background)
	controls[idx].PeakEntry.SetValue(img.Peak)
	controls[idx].ScaledPeakEntry.SetValue(img.ScaledPeak)
	setStretchParamEntries(controls[idx], img)
	controls[idx].ShowClip.SetChecked(img.ShowClip)

	// Manual Offset fields are the source of truth for placement. Restore them from
	// the state (saved projects carry offsets); a freshly loaded/reset channel has
	// zero offsets in its state, so no stale shift is carried over.
	if controls[idx].XOffsetEntry != nil {
		controls[idx].XOffsetEntry.SetValue(state.OffsetX)
	}
	if controls[idx].YOffsetEntry != nil {
		controls[idx].YOffsetEntry.SetValue(state.OffsetY)
	}
	if controls[idx].RotOffsetEntry != nil {
		controls[idx].RotOffsetEntry.SetValue(state.OffsetRot)
	}

	views[idx].blackBox.SetValue(img.Black)
	views[idx].whiteBox.SetValue(img.White)
}

func applyChannelStateToImage(img *models.LoadedImage, st models.ChannelState) {
	if img == nil {
		return
	}
	img.Mode = labelToMode(st.Mode)
	img.Black, img.White = st.Black, st.White
	img.Background, img.Peak, img.ScaledPeak, img.ShowClip = st.Background, st.Peak, st.ScaledPeak, st.ShowClip
	img.AsinhScale, img.MTFMidtone = st.AsinhScale, st.MTFMidtone
	img.GHSStretch, img.GHSLocal, img.GHSSymmetry = st.GHSStretch, st.GHSLocal, st.GHSSymmetry
	img.StarStretch = starStretchStateFromProject(st.StarStretch)
}

func defaultRGBLevels() *models.RgbLevels {
	return &models.RgbLevels{
		Min: [3]float64{0, 0, 0},
		Max: [3]float64{255, 255, 255},
	}
}

// setStretchParamEntries populates a channel's stretch-parameter entry widgets
// from a LoadedImage, substituting defaults for unset (zero) values so the
// fields always show a meaningful number.
func setStretchParamEntries(control *models.ChannelControl, img *models.LoadedImage) {
	if control == nil || img == nil {
		return
	}
	asinh := img.AsinhScale
	if asinh <= 0 {
		asinh = stretch.DefaultAsinhScale
	}
	mtf := img.MTFMidtone
	if mtf <= 0 || mtf >= 1 {
		mtf = stretch.DefaultMTFMidtone
	}
	d := img.GHSStretch
	if d <= 0 {
		d = stretch.DefaultGHSStretch
	}
	sp := img.GHSSymmetry
	if sp <= 0 || sp >= 1 {
		sp = stretch.DefaultGHSSymmetry
	}
	if control.AsinhScaleEntry != nil {
		control.AsinhScaleEntry.SetValue(asinh)
	}
	if control.MTFMidtoneEntry != nil {
		control.MTFMidtoneEntry.SetValue(mtf)
	}
	if control.GHSStretchEntry != nil {
		control.GHSStretchEntry.SetValue(d)
	}
	if control.GHSLocalEntry != nil {
		control.GHSLocalEntry.SetValue(img.GHSLocal)
	}
	if control.GHSSymmetryEntry != nil {
		control.GHSSymmetryEntry.SetValue(sp)
	}
}

func modeToLabel(m stretch.Mode) string {
	switch m {
	case stretch.Linear:
		return "Linear"
	case stretch.Log:
		return "Log"
	case stretch.Asinh:
		return "Asinh"
	case stretch.Sqrt:
		return "Sqrt"
	case stretch.HistEq:
		return "HistEq"
	case stretch.MTF:
		return "MTF"
	case stretch.GHS:
		return "GHS"
	default:
		return "Linear"
	}
}
