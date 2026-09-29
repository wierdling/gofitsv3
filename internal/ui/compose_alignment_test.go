package ui

import (
	"errors"
	"math"
	"reflect"
	"testing"

	"gofitsv3/internal/models"
	"gofitsv3/internal/processing"
)

func TestComposeAlignmentDirectMatchesDoNotFallback(t *testing.T) {
	channels := composeAlignmentTestChannels()
	transforms := map[[2]int]processing.AffineTransform{
		{1, 2}: affine(1.2, 0.3, 4, -0.2, 0.8, -2),
		{3, 2}: affine(0.7, -0.4, -3, 0.5, 1.1, 5),
	}
	var attempts [][2]int

	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		attempts = append(attempts, [2]int{target.Index, reference.Index})
		return testMatch(target, reference, transforms[[2]int{target.Index, reference.Index}], processing.AlignStats{MatchedStars: 3}), nil
	})

	if want := [][2]int{{1, 2}, {3, 2}}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempt order = %v, want %v", attempts, want)
	}
	if result.RootIndex != 2 {
		t.Fatalf("root index = %d, want original Channel 2", result.RootIndex)
	}
	root, ok := result.Channels[2]
	if !ok || !root.Applicable || root.ReferenceIndex != 2 || root.Forward != processing.IdentityTransform() || root.Backward != processing.IdentityTransform() {
		t.Fatalf("root result = %+v, want applicable identity result for Channel 2", root)
	}
	for _, index := range []int{1, 3} {
		got := result.Channels[index]
		if !got.Applicable || !got.Direct || got.ReferenceIndex != 2 {
			t.Fatalf("channel %d result = %+v, want applicable direct result rooted at 2", index, got)
		}
		if got.Forward != transforms[[2]int{index, 2}] {
			t.Fatalf("channel %d forward transform = %+v, want direct transform %+v", index, got.Forward, transforms[[2]int{index, 2}])
		}
		assertInverseOnce(t, got.Forward, got.Backward)
	}
	if len(result.Attempts) != 2 {
		t.Fatalf("attempt count = %d, want 2", len(result.Attempts))
	}
}

func TestComposeAlignmentSlotsIncludesLoadedExtraLayers(t *testing.T) {
	images := make([]*models.LoadedImage, 7)
	for _, index := range []int{0, 1, 2, 4, 6} {
		images[index] = &models.LoadedImage{}
	}

	got := composeAlignmentSlots(images, []int{4, 5, 6})
	want := []int{0, 1, 2, 4, 6}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alignment slots = %v, want RGB plus loaded extras %v", got, want)
	}
}

func TestComposeAlignmentMatchFromFittedAffineConvertsOriginalTargetFrame(t *testing.T) {
	target := composeAlignmentChannel{Index: 1, Width: 100, Height: 50}
	reference := composeAlignmentChannel{Index: 2, Width: 200, Height: 100}
	fitted := affine(1.1, 0.2, 3, -0.1, 0.9, 4)
	got := composeAlignmentMatchFromFittedAffine(target, reference, fitted, processing.AlignStats{})
	want := processing.ComposeAffineTransforms(fitted, processing.AffineTransform{A: 2, E: 2})
	if got.Forward != want {
		t.Fatalf("original-target forward = %+v, want fitted composed with resize = %+v", got.Forward, want)
	}
	if got.TargetFrameWidth != target.Width || got.TargetFrameHeight != target.Height || got.ReferenceFrameWidth != reference.Width || got.ReferenceFrameHeight != reference.Height {
		t.Fatalf("coordinate frames = %+v, want original target and reference dimensions", got)
	}
	backward, err := processing.InvertAffineTransform(got.Forward)
	if err != nil {
		t.Fatalf("converted affine should be invertible: %v", err)
	}
	for _, point := range [][2]float64{{0, 0}, {25, 10}, {99, 49}} {
		resizedX, resizedY := float64(point[0])*2, float64(point[1])*2
		wantX, wantY := processing.ApplyAffineTransform(fitted, resizedX, resizedY)
		gotX, gotY := processing.ApplyAffineTransform(got.Forward, point[0], point[1])
		if math.Abs(gotX-wantX) > 1e-12 || math.Abs(gotY-wantY) > 1e-12 {
			t.Fatalf("forward map at %v = (%v, %v), want (%v, %v)", point, gotX, gotY, wantX, wantY)
		}
		originalX, originalY := processing.ApplyAffineTransform(backward, wantX, wantY)
		if math.Abs(originalX-point[0]) > 1e-12 || math.Abs(originalY-point[1]) > 1e-12 {
			t.Fatalf("backward sampling at fitted result = (%v, %v), want original point %v", originalX, originalY, point)
		}
	}
}

func TestComposeAlignmentResultErrorIncludesAllRouteContexts(t *testing.T) {
	result := composeAlignmentResult{
		Attempts: []composeAlignmentAttempt{
			{TargetIndex: 1, ReferenceIndex: 2, Direct: true, Err: errors.New("direct failed")},
			{TargetIndex: 1, ReferenceIndex: 3, Direct: false, Err: errors.New("fallback failed")},
		},
	}
	channel := composeAlignmentChannelResult{Errors: []error{errors.New("direct failed"), errors.New("fallback failed")}}
	got := composeAlignmentResultError(result, 1, channel).Error()
	want := "direct-to-Channel-2: direct failed\nfallback-via-Channel-3: fallback failed"
	if got != want {
		t.Fatalf("route error = %q, want %q", got, want)
	}
}

func TestComposeAlignmentFallsBackThroughResolvedSibling(t *testing.T) {
	channels := composeAlignmentTestChannels()
	direct := affine(1.2, 0.3, 10, -0.2, 0.8, 1)
	via := affine(0.7, -0.4, -2, 0.5, 1.1, 7)
	var attempts [][2]int

	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		pair := [2]int{target.Index, reference.Index}
		attempts = append(attempts, pair)
		switch pair {
		case [2]int{1, 2}:
			return composeAlignmentMatch{}, errors.New("direct 1->2 failed")
		case [2]int{3, 2}:
			return testMatch(target, reference, direct, processing.AlignStats{MatchedStars: 5}), nil
		case [2]int{1, 3}:
			return testMatch(target, reference, via, processing.AlignStats{MatchedStars: 4}), nil
		default:
			t.Fatalf("unexpected matcher call %v", pair)
			return composeAlignmentMatch{}, nil
		}
	})

	if want := [][2]int{{1, 2}, {3, 2}, {1, 3}}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempt order = %v, want %v", attempts, want)
	}
	got := result.Channels[1]
	wantForward := processing.ComposeAffineTransforms(direct, via)
	if !got.Applicable || got.Direct || got.ReferenceIndex != 3 {
		t.Fatalf("fallback result = %+v, want applicable fallback via 3", got)
	}
	if got.Forward != wantForward {
		t.Fatalf("composed forward = %+v, want T_3_root ∘ T_1_3 = %+v", got.Forward, wantForward)
	}
	for _, point := range [][2]float64{{0, 0}, {2.5, -1}, {11, 8}} {
		gotX, gotY := processing.ApplyAffineTransform(got.Forward, point[0], point[1])
		viaX, viaY := mustPoint(via, point[0], point[1])
		wantX, wantY := processing.ApplyAffineTransform(direct, viaX, viaY)
		if gotX != wantX || gotY != wantY {
			t.Fatalf("composed transform at %v = (%v, %v), want (%v, %v)", point, gotX, gotY, wantX, wantY)
		}
	}
	assertInverseOnce(t, got.Forward, got.Backward)
	if len(got.Errors) != 1 || got.Errors[0].Error() != "direct 1->2 failed" {
		t.Fatalf("retained errors = %v, want direct failure", got.Errors)
	}
}

func TestComposeAlignmentDoesNotUseUnresolvedIntermediary(t *testing.T) {
	channels := composeAlignmentTestChannels()
	var attempts [][2]int
	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		attempts = append(attempts, [2]int{target.Index, reference.Index})
		return composeAlignmentMatch{}, errors.New("direct match failed")
	})

	if want := [][2]int{{1, 2}, {3, 2}}; !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempt order = %v, want %v", attempts, want)
	}
	if len(result.Attempts) != 2 || result.Channels[1].Applicable || result.Channels[3].Applicable {
		t.Fatalf("result = %+v, want only the two failed direct attempts and no fallback", result)
	}
	for _, index := range []int{1, 3} {
		if len(result.Channels[index].Errors) != 1 || result.Channels[index].Errors[0].Error() != "direct match failed" {
			t.Fatalf("channel %d errors = %v, want retained direct-attempt error", index, result.Channels[index].Errors)
		}
	}
}

func TestComposeAlignmentFallbackAllowsSmallOverlapWithThreeStars(t *testing.T) {
	channels := eligibleComposeAlignmentTestChannels(3, 3)
	var calls [][2]int
	result := coordinateComposeAlignmentWithEligibility(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		pair := [2]int{target.Index, reference.Index}
		calls = append(calls, pair)
		if pair == [2]int{1, 2} {
			return composeAlignmentMatch{}, errors.New("direct failed")
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	}, composeAlignmentFallbackPairEligible)

	if !result.Channels[1].Applicable || result.Channels[1].Direct {
		t.Fatalf("fallback result = %+v, want applicable fallback", result.Channels[1])
	}
	if !reflect.DeepEqual(calls, [][2]int{{1, 2}, {3, 2}, {1, 3}}) {
		t.Fatalf("matcher calls = %v, want direct attempts followed by eligible fallback", calls)
	}
}

func TestComposeAlignmentFallbackScalesTargetIntoIntermediaryFrame(t *testing.T) {
	stars := []processing.Star{{X: 6, Y: 4}, {X: 7, Y: 5}, {X: 8, Y: 6}}
	channels := []composeAlignmentChannel{
		{Index: 1, Width: 10, Height: 8, Footprint: composeAlignmentFootprint{MaxX: 10, MaxY: 8}, UsableStars: stars},
		{Index: 2, Width: 12, Height: 9},
		{Index: 3, Width: 20, Height: 16, Footprint: composeAlignmentFootprint{MinX: 10, MinY: 6, MaxX: 20, MaxY: 16}, UsableStars: []processing.Star{{X: 12, Y: 8}, {X: 14, Y: 10}, {X: 16, Y: 12}}},
	}
	var calls [][2]int
	result := coordinateComposeAlignmentWithEligibility(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		pair := [2]int{target.Index, reference.Index}
		calls = append(calls, pair)
		if pair == [2]int{1, 2} {
			return composeAlignmentMatch{}, errors.New("direct failed")
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	}, composeAlignmentFallbackPairEligible)

	if !result.Channels[1].Applicable || result.Channels[1].Direct || result.Channels[1].ReferenceIndex != 3 {
		t.Fatalf("fallback result = %+v, want eligible scaled fallback via Channel 3", result.Channels[1])
	}
	if !reflect.DeepEqual(calls, [][2]int{{1, 2}, {3, 2}, {1, 3}}) {
		t.Fatalf("matcher calls = %v, want fallback attempted after direct passes", calls)
	}
}

func TestComposeAlignmentFallbackSkipsFewerThanThreeStarsWithoutMatching(t *testing.T) {
	channels := eligibleComposeAlignmentTestChannels(2, 3)
	var calls [][2]int
	result := coordinateComposeAlignmentWithEligibility(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		calls = append(calls, [2]int{target.Index, reference.Index})
		if target.Index == 1 {
			return composeAlignmentMatch{}, errors.New("direct failed")
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	}, composeAlignmentFallbackPairEligible)

	if !reflect.DeepEqual(calls, [][2]int{{1, 2}, {3, 2}}) {
		t.Fatalf("matcher calls = %v, want rejected fallback omitted", calls)
	}
	if result.Channels[1].Applicable || len(result.Channels[1].Errors) != 2 || !result.Attempts[2].Skipped {
		t.Fatalf("rejected fallback result = %+v, attempts = %+v", result.Channels[1], result.Attempts)
	}
}

func TestComposeAlignmentFallbackSkipsNoOverlapWithoutMatching(t *testing.T) {
	channels := eligibleComposeAlignmentTestChannels(3, 3)
	channels[0].Footprint = composeAlignmentFootprint{MinX: 20, MinY: 20, MaxX: 30, MaxY: 30}
	var calls [][2]int
	result := coordinateComposeAlignmentWithEligibility(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		calls = append(calls, [2]int{target.Index, reference.Index})
		if target.Index == 1 {
			return composeAlignmentMatch{}, errors.New("direct failed")
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	}, composeAlignmentFallbackPairEligible)

	if !reflect.DeepEqual(calls, [][2]int{{1, 2}, {3, 2}}) {
		t.Fatalf("matcher calls = %v, want no matcher call for non-overlap fallback", calls)
	}
	if result.Channels[1].Applicable || len(result.Channels[1].Errors) != 2 || !result.Attempts[2].Skipped {
		t.Fatalf("no-overlap result = %+v, attempts = %+v", result.Channels[1], result.Attempts)
	}
}

func TestComposeAlignmentFallbackSelectionSkipsIneligibleCandidateDeterministically(t *testing.T) {
	channels := eligibleComposeAlignmentTestChannels(3, 3)
	channels = append(channels, channels[2])
	channels[3].Index = 4
	var calls [][2]int
	result := coordinateComposeAlignmentWithEligibility(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		pair := [2]int{target.Index, reference.Index}
		calls = append(calls, pair)
		if pair == [2]int{1, 2} {
			return composeAlignmentMatch{}, errors.New("direct failed")
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	}, func(target, reference composeAlignmentChannel) (bool, error) {
		if reference.Index == 3 {
			return false, errors.New("candidate rejected")
		}
		return composeAlignmentFallbackPairEligible(target, reference)
	})

	wantCalls := [][2]int{{1, 2}, {3, 2}, {4, 2}, {1, 4}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("matcher calls = %v, want deterministic skip/selection order %v", calls, wantCalls)
	}
	if !result.Channels[1].Applicable || result.Channels[1].ReferenceIndex != 4 {
		t.Fatalf("selected fallback = %+v, want Channel 4 route", result.Channels[1])
	}
	if !result.Attempts[3].Skipped || result.Attempts[3].Err == nil {
		t.Fatalf("rejected candidate attempt = %+v, want retained diagnostic", result.Attempts[3])
	}
}

func TestComposeAlignmentFallbackFailureRetainsIndependentSuccess(t *testing.T) {
	channels := composeAlignmentTestChannels()
	directErr := errors.New("direct 1->2 failed")
	fallbackErr := errors.New("fallback 1->3 failed")
	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		switch [2]int{target.Index, reference.Index} {
		case [2]int{1, 2}:
			return composeAlignmentMatch{}, directErr
		case [2]int{3, 2}:
			return testMatch(target, reference, translation(3, 4), processing.AlignStats{MatchedStars: 6}), nil
		case [2]int{1, 3}:
			return composeAlignmentMatch{}, fallbackErr
		default:
			t.Fatalf("unexpected matcher call")
			return composeAlignmentMatch{}, nil
		}
	})

	if !result.Channels[3].Applicable || result.Channels[3].Forward != translation(3, 4) {
		t.Fatalf("independent direct success = %+v, want retained", result.Channels[3])
	}
	got := result.Channels[1]
	if got.Applicable || !reflect.DeepEqual(got.Errors, []error{directErr, fallbackErr}) {
		t.Fatalf("failed fallback result = %+v, want both errors retained", got)
	}
}

func TestComposeAlignmentReportsNonInvertibleComposedTransform(t *testing.T) {
	channels := composeAlignmentTestChannels()
	directErr := errors.New("direct 1->2 failed")
	singular := processing.AffineTransform{A: 1, B: 2, D: 2, E: 4}
	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		switch [2]int{target.Index, reference.Index} {
		case [2]int{1, 2}:
			return composeAlignmentMatch{}, directErr
		case [2]int{3, 2}:
			return testMatch(target, reference, translation(5, 6), processing.AlignStats{MatchedStars: 3}), nil
		case [2]int{1, 3}:
			return testMatch(target, reference, singular, processing.AlignStats{MatchedStars: 3}), nil
		default:
			t.Fatalf("unexpected matcher call")
			return composeAlignmentMatch{}, nil
		}
	})

	got := result.Channels[1]
	if got.Applicable || got.Backward != (processing.AffineTransform{}) {
		t.Fatalf("non-invertible result = %+v, want no applicable backward transform", got)
	}
	if len(got.Errors) != 2 || got.Errors[0] != directErr || got.Errors[1].Error() != "singular affine transform" {
		t.Fatalf("non-invertible errors = %v, want direct and inversion errors", got.Errors)
	}
	if !result.Channels[3].Applicable {
		t.Fatal("independent direct success was discarded")
	}
}

func TestComposeAlignmentRejectsMismatchedCoordinateFrame(t *testing.T) {
	channels := composeAlignmentTestChannels()
	result := coordinateComposeAlignment(channels, 2, func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
		if target.Index == 1 {
			return composeAlignmentMatch{
				Forward: processing.IdentityTransform(), Stats: processing.AlignStats{MatchedStars: 3},
				TargetFrameWidth: reference.Width, TargetFrameHeight: reference.Height,
				ReferenceFrameWidth: reference.Width, ReferenceFrameHeight: reference.Height,
			}, nil
		}
		return testMatch(target, reference, processing.IdentityTransform(), processing.AlignStats{MatchedStars: 3}), nil
	})

	got := result.Channels[1]
	if got.Applicable || len(got.Errors) != 2 {
		t.Fatalf("mismatched-frame result = %+v, want inapplicable result with direct and fallback errors", got)
	}
	if !result.Channels[3].Applicable {
		t.Fatal("independent direct result became inapplicable")
	}
	if result.Attempts[0].Err == nil || result.Attempts[2].Err == nil {
		t.Fatalf("attempts = %+v, want frame-contract errors", result.Attempts)
	}
}

func TestComposeAlignmentRejectsNonFiniteTransforms(t *testing.T) {
	tests := []struct {
		name  string
		match func(composeAlignmentChannel, composeAlignmentChannel) (composeAlignmentMatch, error)
	}{
		{
			name: "forward",
			match: func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
				return testMatch(target, reference, processing.AffineTransform{A: math.NaN(), E: 1}, processing.AlignStats{}), nil
			},
		},
		{
			name: "composed",
			match: func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
				if target.Index == 1 && reference.Index == 2 {
					return composeAlignmentMatch{}, errors.New("direct failure")
				}
				if target.Index == 3 {
					return testMatch(target, reference, processing.AffineTransform{A: math.MaxFloat64, E: 1}, processing.AlignStats{}), nil
				}
				return testMatch(target, reference, processing.AffineTransform{A: 2, E: 1}, processing.AlignStats{}), nil
			},
		},
		{
			name: "inverse",
			match: func(target, reference composeAlignmentChannel) (composeAlignmentMatch, error) {
				return testMatch(target, reference, processing.AffineTransform{A: math.MaxFloat64, C: math.MaxFloat64, E: math.MaxFloat64}, processing.AlignStats{}), nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := coordinateComposeAlignment(composeAlignmentTestChannels(), 2, tt.match)
			for _, index := range []int{1, 3} {
				wantApplicable := tt.name == "composed" && index == 3
				if result.Channels[index].Applicable != wantApplicable {
					t.Fatalf("channel %d result = %+v, want inapplicable", index, result.Channels[index])
				}
				if !wantApplicable && len(result.Channels[index].Errors) == 0 {
					t.Fatalf("channel %d has no retained non-finite error", index)
				}
			}
		})
	}
}

func composeAlignmentTestChannels() []composeAlignmentChannel {
	return []composeAlignmentChannel{{Index: 1, Width: 10, Height: 8}, {Index: 2, Width: 12, Height: 9}, {Index: 3, Width: 14, Height: 11}}
}

func eligibleComposeAlignmentTestChannels(targetStars, referenceStars int) []composeAlignmentChannel {
	stars := func(count int) []processing.Star {
		out := make([]processing.Star, count)
		for i := range out {
			out[i] = processing.Star{X: float64(i + 1), Y: float64(i + 1)}
		}
		return out
	}
	return []composeAlignmentChannel{
		{Index: 1, Width: 10, Height: 8, Footprint: composeAlignmentFootprint{MaxX: 4, MaxY: 4}, UsableStars: stars(targetStars)},
		{Index: 2, Width: 10, Height: 8, Footprint: composeAlignmentFootprint{MaxX: 4, MaxY: 4}, UsableStars: stars(referenceStars)},
		{Index: 3, Width: 10, Height: 8, Footprint: composeAlignmentFootprint{MaxX: 4, MaxY: 4}, UsableStars: stars(3)},
	}
}

func testMatch(target, reference composeAlignmentChannel, forward processing.AffineTransform, stats processing.AlignStats) composeAlignmentMatch {
	return composeAlignmentMatch{Forward: forward, Stats: stats, TargetFrameWidth: target.Width, TargetFrameHeight: target.Height, ReferenceFrameWidth: reference.Width, ReferenceFrameHeight: reference.Height}
}

func translation(x, y float64) processing.AffineTransform {
	return processing.AffineTransform{A: 1, C: x, D: 0, E: 1, F: y}
}

func affine(a, b, c, d, e, f float64) processing.AffineTransform {
	return processing.AffineTransform{A: a, B: b, C: c, D: d, E: e, F: f}
}

func mustPoint(t processing.AffineTransform, x, y float64) (float64, float64) {
	return processing.ApplyAffineTransform(t, x, y)
}

func assertInverseOnce(t *testing.T, forward, backward processing.AffineTransform) {
	t.Helper()
	want, err := processing.InvertAffineTransform(forward)
	if err != nil {
		t.Fatalf("forward transform = %+v is not invertible: %v", forward, err)
	}
	if backward != want {
		t.Fatalf("backward transform = %+v, want coefficient-for-coefficient inverse %+v", backward, want)
	}
}
