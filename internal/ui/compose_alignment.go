package ui

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"gofitsv3/internal/debuglog"
	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

// composeAlignmentChannel describes one original channel. Pixels and dimensions
// are retained here so the production matcher can be adapted without making
// the coordinator aware of models or UI state.
type composeAlignmentChannel struct {
	Index          int
	OriginalPixels []float32
	Width          int
	Height         int
	Footprint      composeAlignmentFootprint
	UsableStars    []processing.Star
}

type composeAlignmentFootprint struct {
	MinX, MinY float64
	MaxX, MaxY float64
}

// composeAlignmentMatch must contain a transform from the target channel's
// original pixel frame to the reference channel's original pixel frame. The
// frame dimensions make this contract checkable by the coordinator. A
// production adapter must convert AlignChannelByStars' resized-target result
// into this frame; the processing matcher itself remains unchanged.
type composeAlignmentMatch struct {
	Forward              processing.AffineTransform
	Stats                processing.AlignStats
	TargetFrameWidth     int
	TargetFrameHeight    int
	ReferenceFrameWidth  int
	ReferenceFrameHeight int
}

type composeAlignmentMatcher func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error)

type composeAlignmentFallbackEligibility func(target, reference composeAlignmentChannel) (bool, error)

// composeAlignmentMatchFromFittedAffine converts the matcher affine, which is
// fitted against a target resized to the reference dimensions, back to the
// target's original pixel frame.
func composeAlignmentMatchFromFittedAffine(target, reference composeAlignmentChannel, fitted processing.AffineTransform, stats processing.AlignStats) composeAlignmentMatch {
	resizeToReference := processing.AffineTransform{
		A: float64(reference.Width) / float64(target.Width),
		E: float64(reference.Height) / float64(target.Height),
	}
	return composeAlignmentMatch{
		Forward:              processing.ComposeAffineTransforms(fitted, resizeToReference),
		Stats:                stats,
		TargetFrameWidth:     target.Width,
		TargetFrameHeight:    target.Height,
		ReferenceFrameWidth:  reference.Width,
		ReferenceFrameHeight: reference.Height,
	}
}

func composeAlignmentResultError(result composeAlignmentResult, targetIndex int, channel composeAlignmentChannelResult) error {
	details := make([]string, 0, len(channel.Errors))
	for _, attempt := range result.Attempts {
		if attempt.TargetIndex != targetIndex || attempt.Err == nil {
			continue
		}
		route := fmt.Sprintf("fallback-via-Channel-%d", attempt.ReferenceIndex)
		if attempt.Direct {
			route = fmt.Sprintf("direct-to-Channel-%d", attempt.ReferenceIndex)
		}
		details = append(details, fmt.Sprintf("%s: %v", route, attempt.Err))
	}
	if len(details) == 0 {
		if len(channel.Errors) == 0 {
			return errors.New("alignment failed")
		}
		return errors.New(channel.Errors[len(channel.Errors)-1].Error())
	}
	return errors.New(strings.Join(details, "\n"))
}

type composeAlignmentAttempt struct {
	TargetIndex    int
	ReferenceIndex int
	Pass           int
	Direct         bool
	Forward        processing.AffineTransform
	Stats          processing.AlignStats
	Err            error
	Skipped        bool
}

type composeAlignmentChannelResult struct {
	Applicable     bool
	Forward        processing.AffineTransform
	Backward       processing.AffineTransform
	Stats          processing.AlignStats
	ReferenceIndex int
	Direct         bool
	Errors         []error
}

type composeAlignmentResult struct {
	RootIndex int
	Channels  map[int]composeAlignmentChannelResult
	Attempts  []composeAlignmentAttempt
}

// composeLargeAlignmentReferenceCurrent is the commit guard for a catalog
// alignment job. The reference descriptor must be unchanged before any target
// affine is installed.
func composeLargeAlignmentReferenceCurrent(current, expected composeArtifactDescriptor) bool {
	return expected.Path != "" && current.Path == expected.Path && current.Generation == expected.Generation
}

func resetComposeAlignmentOffsets(control *models.ChannelControl) {
	if control == nil {
		return
	}
	if control.XOffsetEntry != nil {
		control.XOffsetEntry.SetValue(0)
	}
	if control.YOffsetEntry != nil {
		control.YOffsetEntry.SetValue(0)
	}
	if control.RotOffsetEntry != nil {
		control.RotOffsetEntry.SetValue(0)
	}
}

func coordinateComposeAlignment(channels []composeAlignmentChannel, rootIndex int, match composeAlignmentMatcher) composeAlignmentResult {
	return coordinateComposeAlignmentWithEligibility(channels, rootIndex, match, func(composeAlignmentChannel, composeAlignmentChannel) (bool, error) {
		return true, nil
	})
}

func coordinateComposeAlignmentWithEligibility(channels []composeAlignmentChannel, rootIndex int, match composeAlignmentMatcher, eligible composeAlignmentFallbackEligibility) composeAlignmentResult {
	ordered := append([]composeAlignmentChannel(nil), channels...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })

	result := composeAlignmentResult{
		RootIndex: rootIndex,
		Channels:  make(map[int]composeAlignmentChannelResult, len(ordered)),
	}
	for _, channel := range ordered {
		result.Channels[channel.Index] = composeAlignmentChannelResult{}
	}
	if root, ok := result.Channels[rootIndex]; ok {
		root.Applicable = true
		root.Forward = processing.IdentityTransform()
		root.Backward = processing.IdentityTransform()
		root.ReferenceIndex = rootIndex
		result.Channels[rootIndex] = root
	}

	resolved := make(map[int]bool, len(ordered))
	if _, ok := result.Channels[rootIndex]; ok {
		resolved[rootIndex] = true
	}
	attempted := make(map[[2]int]bool, len(ordered)*len(ordered))

	for _, target := range ordered {
		if target.Index == rootIndex {
			continue
		}
		tryComposeAlignment(&result, resolved, attempted, target, rootIndex, rootIndex, true, 1, ordered, match, eligible)
	}

	pass := 2
	for {
		progress := false
		for _, target := range ordered {
			if target.Index == rootIndex || resolved[target.Index] {
				continue
			}
			for _, intermediary := range ordered {
				if intermediary.Index == rootIndex || !resolved[intermediary.Index] {
					continue
				}
				if tryComposeAlignment(&result, resolved, attempted, target, rootIndex, intermediary.Index, false, pass, ordered, match, eligible) {
					progress = true
					break
				}
			}
		}
		if !progress {
			break
		}
		pass++
	}

	logComposeAlignmentSummary(&result, ordered, rootIndex)
	return result
}

func tryComposeAlignment(result *composeAlignmentResult, resolved map[int]bool, attempted map[[2]int]bool, target composeAlignmentChannel, rootIndex, referenceIndex int, direct bool, pass int, channels []composeAlignmentChannel, match composeAlignmentMatcher, eligible composeAlignmentFallbackEligibility) bool {
	pair := [2]int{target.Index, referenceIndex}
	if attempted[pair] {
		return false
	}
	attempted[pair] = true
	debuglog.Log(fmt.Sprintf("compose alignment attempt: kind=%s pass=%d target=Channel %d reference=Channel %d", composeAlignmentPassKind(direct), pass, target.Index, referenceIndex))

	reference := channelByIndex(channels, referenceIndex)
	if !direct {
		allowed, err := eligible(target, reference)
		if !allowed {
			if err == nil {
				err = errors.New("fallback pair is ineligible")
			}
			attempt := composeAlignmentAttempt{TargetIndex: target.Index, ReferenceIndex: referenceIndex, Pass: pass, Err: err, Skipped: true}
			result.Attempts = append(result.Attempts, attempt)
			appendComposeAlignmentError(result, target.Index, err)
			debuglog.Log(fmt.Sprintf("compose alignment skipped: kind=fallback pass=%d target=Channel %d reference=Channel %d reason=%v", pass, target.Index, referenceIndex, err))
			return false
		}
	}
	matched, err := match(target, reference)
	intermediate := matched.Forward
	attempt := composeAlignmentAttempt{
		TargetIndex: target.Index, ReferenceIndex: referenceIndex, Pass: pass,
		Direct: direct, Forward: intermediate, Stats: matched.Stats, Err: err,
	}
	if err != nil {
		logComposeAlignmentFailure(target.Index, referenceIndex, direct, pass, err, hasAnotherResolvedIntermediary(resolved, rootIndex, target.Index, referenceIndex))
		appendComposeAlignmentError(result, target.Index, err)
		result.Attempts = append(result.Attempts, attempt)
		return false
	}
	if matched.TargetFrameWidth != target.Width || matched.TargetFrameHeight != target.Height ||
		matched.ReferenceFrameWidth != reference.Width || matched.ReferenceFrameHeight != reference.Height {
		err = fmt.Errorf("matcher returned transform in frame %dx%d -> %dx%d, want original frame %dx%d -> %dx%d",
			matched.TargetFrameWidth, matched.TargetFrameHeight, matched.ReferenceFrameWidth, matched.ReferenceFrameHeight,
			target.Width, target.Height, reference.Width, reference.Height)
		attempt.Err = err
		logComposeAlignmentFailure(target.Index, referenceIndex, direct, pass, err, hasAnotherResolvedIntermediary(resolved, rootIndex, target.Index, referenceIndex))
		appendComposeAlignmentError(result, target.Index, err)
		result.Attempts = append(result.Attempts, attempt)
		return false
	}
	if !finiteAffine(intermediate) {
		err = errors.New("matcher returned non-finite affine transform")
		attempt.Err = err
		logComposeAlignmentFailure(target.Index, referenceIndex, direct, pass, err, hasAnotherResolvedIntermediary(resolved, rootIndex, target.Index, referenceIndex))
		appendComposeAlignmentError(result, target.Index, err)
		result.Attempts = append(result.Attempts, attempt)
		return false
	}

	forward := intermediate
	if !direct {
		parent := result.Channels[referenceIndex]
		forward = processing.ComposeAffineTransforms(parent.Forward, intermediate)
		if !finiteAffine(forward) {
			err = errors.New("composed affine transform is non-finite")
			attempt.Err = err
			logComposeAlignmentFailure(target.Index, referenceIndex, direct, pass, err, hasAnotherResolvedIntermediary(resolved, rootIndex, target.Index, referenceIndex))
			appendComposeAlignmentError(result, target.Index, err)
			result.Attempts = append(result.Attempts, attempt)
			return false
		}
	}
	backward, err := processing.InvertAffineTransform(forward)
	if err != nil || !finiteAffine(backward) {
		if err == nil {
			err = errors.New("inverted affine transform is non-finite")
		}
		attempt.Err = err
		logComposeAlignmentFailure(target.Index, referenceIndex, direct, pass, err, hasAnotherResolvedIntermediary(resolved, rootIndex, target.Index, referenceIndex))
		appendComposeAlignmentError(result, target.Index, err)
		result.Attempts = append(result.Attempts, attempt)
		return false
	}

	attempt.Forward = forward
	debuglog.Log(fmt.Sprintf("compose alignment success: kind=%s pass=%d target=Channel %d reference=Channel %d matched-stars=%d global-inliers=%d rms=%.3f max-error=%.3f composed=%t", composeAlignmentPassKind(direct), pass, target.Index, referenceIndex, matched.Stats.MatchedStars, matched.Stats.GlobalInliers, matched.Stats.RMS, matched.Stats.MaxError, !direct))
	result.Attempts = append(result.Attempts, attempt)
	result.Channels[target.Index] = composeAlignmentChannelResult{
		Applicable: true, Forward: forward, Backward: backward, Stats: matched.Stats,
		ReferenceIndex: referenceIndex, Direct: direct,
		Errors: result.Channels[target.Index].Errors,
	}
	resolved[target.Index] = true
	if !direct {
		debuglog.Log(fmt.Sprintf("compose alignment accepted fallback: target=Channel %d via=Channel %d root=Channel %d target-to-Channel-2-affine=[%.9g %.9g %.9g %.9g %.9g %.9g]", target.Index, referenceIndex, rootIndex, forward.A, forward.B, forward.C, forward.D, forward.E, forward.F))
	}
	return true
}

func composeAlignmentPassKind(direct bool) string {
	if direct {
		return "direct"
	}
	return "fallback"
}

func hasAnotherResolvedIntermediary(resolved map[int]bool, rootIndex, targetIndex, referenceIndex int) bool {
	for index, isResolved := range resolved {
		if isResolved && index != rootIndex && index != targetIndex && index != referenceIndex {
			return true
		}
	}
	return false
}

func logComposeAlignmentFailure(targetIndex, referenceIndex int, direct bool, pass int, err error, anotherIntermediary bool) {
	debuglog.Log(fmt.Sprintf("compose alignment failure: kind=%s pass=%d target=Channel %d reference=Channel %d error=%v another-resolved-intermediary=%t", composeAlignmentPassKind(direct), pass, targetIndex, referenceIndex, err, anotherIntermediary))
}

func logComposeAlignmentSummary(result *composeAlignmentResult, channels []composeAlignmentChannel, rootIndex int) {
	directSuccesses := 0
	fallbackSuccesses := 0
	for _, attempt := range result.Attempts {
		if attempt.Err != nil {
			continue
		}
		if attempt.Direct {
			directSuccesses++
		} else {
			fallbackSuccesses++
		}
	}
	unresolved := make([]int, 0, len(channels))
	for _, channel := range channels {
		if channel.Index != rootIndex && !result.Channels[channel.Index].Applicable {
			unresolved = append(unresolved, channel.Index)
		}
	}
	debuglog.Log(fmt.Sprintf("compose alignment summary: direct-successes=%d fallback-successes=%d unresolved-channels=%v", directSuccesses, fallbackSuccesses, unresolved))
}

func finiteAffine(t processing.AffineTransform) bool {
	return finite(t.A) && finite(t.B) && finite(t.C) && finite(t.D) && finite(t.E) && finite(t.F)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func composeAlignmentFallbackPairEligible(target, reference composeAlignmentChannel) (bool, error) {
	if target.Width <= 0 || target.Height <= 0 || reference.Width <= 0 || reference.Height <= 0 {
		return false, errors.New("target and intermediary dimensions must be positive")
	}
	targetFootprint := target.Footprint
	if targetFootprint.MaxX <= targetFootprint.MinX || targetFootprint.MaxY <= targetFootprint.MinY {
		targetFootprint = composeAlignmentFootprint{MaxX: float64(target.Width), MaxY: float64(target.Height)}
	}
	targetFootprint = scaleComposeAlignmentFootprint(targetFootprint, target.Width, target.Height, reference.Width, reference.Height)
	referenceFootprint := reference.Footprint
	if referenceFootprint.MaxX <= referenceFootprint.MinX || referenceFootprint.MaxY <= referenceFootprint.MinY {
		referenceFootprint = composeAlignmentFootprint{MaxX: float64(reference.Width), MaxY: float64(reference.Height)}
	}
	shared := composeAlignmentFootprint{
		MinX: math.Max(targetFootprint.MinX, referenceFootprint.MinX),
		MinY: math.Max(targetFootprint.MinY, referenceFootprint.MinY),
		MaxX: math.Min(targetFootprint.MaxX, referenceFootprint.MaxX),
		MaxY: math.Min(targetFootprint.MaxY, referenceFootprint.MaxY),
	}
	if shared.MaxX <= shared.MinX || shared.MaxY <= shared.MinY {
		return false, errors.New("target and intermediary footprints do not overlap")
	}
	if countScaledStarsInFootprint(target.UsableStars, target.Width, target.Height, reference.Width, reference.Height, shared) < 3 || countStarsInFootprint(reference.UsableStars, shared) < 3 {
		return false, errors.New("shared footprint contains fewer than 3 usable stars")
	}
	return true, nil
}

func scaleComposeAlignmentFootprint(footprint composeAlignmentFootprint, sourceWidth, sourceHeight, destinationWidth, destinationHeight int) composeAlignmentFootprint {
	xScale := float64(destinationWidth) / float64(sourceWidth)
	yScale := float64(destinationHeight) / float64(sourceHeight)
	return composeAlignmentFootprint{
		MinX: footprint.MinX * xScale,
		MinY: footprint.MinY * yScale,
		MaxX: footprint.MaxX * xScale,
		MaxY: footprint.MaxY * yScale,
	}
}

func countStarsInFootprint(stars []processing.Star, footprint composeAlignmentFootprint) int {
	count := 0
	for _, star := range stars {
		if star.X >= footprint.MinX && star.X < footprint.MaxX && star.Y >= footprint.MinY && star.Y < footprint.MaxY {
			count++
		}
	}
	return count
}

func countScaledStarsInFootprint(stars []processing.Star, sourceWidth, sourceHeight, destinationWidth, destinationHeight int, footprint composeAlignmentFootprint) int {
	xScale := float64(destinationWidth) / float64(sourceWidth)
	yScale := float64(destinationHeight) / float64(sourceHeight)
	count := 0
	for _, star := range stars {
		x, y := star.X*xScale, star.Y*yScale
		if x >= footprint.MinX && x < footprint.MaxX && y >= footprint.MinY && y < footprint.MaxY {
			count++
		}
	}
	return count
}

func appendComposeAlignmentError(result *composeAlignmentResult, index int, err error) {
	channel := result.Channels[index]
	channel.Errors = append(channel.Errors, err)
	result.Channels[index] = channel
}

func channelByIndex(channels []composeAlignmentChannel, index int) composeAlignmentChannel {
	for _, channel := range channels {
		if channel.Index == index {
			return channel
		}
	}
	return composeAlignmentChannel{Index: index}
}
