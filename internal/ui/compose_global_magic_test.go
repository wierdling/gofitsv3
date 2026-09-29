package ui

import (
	"context"
	"testing"

	"gofitsv3/internal/processing"
	"gofitsv3/internal/stretch"
)

func TestPrepareComposeGlobalMagicBuildsIndependentChannelPreview(t *testing.T) {
	base := composeMagicTestImage("blue.fits")
	extra := composeMagicTestImage("extra.fits")

	results, err := prepareComposeGlobalMagic(context.Background(), []composeGlobalMagicTarget{
		{index: 0, image: *base},
		{index: 7, image: *extra, independent: true},
	}, processing.MagicBalanced)
	if err != nil {
		t.Fatalf("prepareComposeGlobalMagic returned %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].preview != nil {
		t.Fatal("base channel unexpectedly received an independent preview")
	}
	independent := results[1]
	if independent.index != 7 {
		t.Fatalf("independent index = %d, want sparse slot 7", independent.index)
	}
	if independent.image.Mode != stretch.MTF || independent.image.MTFMidtone <= 0 {
		t.Fatalf("independent stretch = (%v, %v), want valid MTF", independent.image.Mode, independent.image.MTFMidtone)
	}
	if independent.preview == nil || independent.preview.image == nil {
		t.Fatal("independent channel preview was not prepared")
	}
	if independent.preview.width != extra.HDU.Data.Width || independent.preview.height != extra.HDU.Data.Height {
		t.Fatalf("independent preview size = %dx%d, want %dx%d", independent.preview.width, independent.preview.height, extra.HDU.Data.Width, extra.HDU.Data.Height)
	}
}

func TestPrepareComposeGlobalMagicHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := prepareComposeGlobalMagic(ctx, []composeGlobalMagicTarget{{
		index: 3, image: *composeMagicTestImage("extra.fits"), independent: true,
	}}, processing.MagicBalanced)
	if err == nil {
		t.Fatal("prepareComposeGlobalMagic succeeded after cancellation")
	}
	if results != nil {
		t.Fatalf("canceled results = %#v, want nil", results)
	}
}

func TestComposeLoadRequestCurrentRejectsSupersededIndependentLoad(t *testing.T) {
	tests := []struct {
		name                           string
		currentSession, requestSession uint64
		currentRequest, request        uint64
		want                           bool
	}{
		{name: "current", currentSession: 4, requestSession: 4, currentRequest: 9, request: 9, want: true},
		{name: "newer request", currentSession: 4, requestSession: 4, currentRequest: 10, request: 9},
		{name: "newer session", currentSession: 5, requestSession: 4, currentRequest: 9, request: 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeLoadRequestCurrent(tt.currentSession, tt.requestSession, tt.currentRequest, tt.request); got != tt.want {
				t.Fatalf("composeLoadRequestCurrent() = %v, want %v", got, tt.want)
			}
		})
	}
}
