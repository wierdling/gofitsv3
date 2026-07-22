package ui

import (
	"reflect"
	"testing"

	"gofitsv3/internal/mosaic"
)

func TestConfirmReferenceFrameChangeSetWaitsForConfirmationAndDeletesBeforeApply(t *testing.T) {
	inputs := []mosaic.Input{{Path: `C:\work\input\image.fits`}}
	oldReference := &mosaic.Input{Path: `C:\work\old\baseline.fits`}
	newReference := &mosaic.Input{Path: `C:\work\new\baseline.fits`}
	current := oldReference
	var decide func(bool)
	var deleted []string

	confirmReferenceFrameChangeWith(inputs, current, newReference, func() { current = newReference }, func(_ string, callback func(bool)) {
		decide = callback
	}, func(directory string, reference mosaic.Input) (int, error) {
		if !sameMosaicAlignmentInput(reference, *oldReference) {
			t.Fatalf("delete reference = %#v, want %#v", reference, *oldReference)
		}
		deleted = append(deleted, directory)
		return 1, nil
	}, func(err error) { t.Fatalf("unexpected error: %v", err) })

	if current != oldReference || decide == nil || len(deleted) != 0 {
		t.Fatal("reference changed or sidecars deleted before confirmation")
	}
	decide(true)
	if current != newReference {
		t.Fatal("reference was not updated after confirmed deletion")
	}
	wantDirectories := []string{`C:\work\input`, `C:\work\old`}
	if !reflect.DeepEqual(deleted, wantDirectories) {
		t.Fatalf("deleted directories = %#v, want %#v", deleted, wantDirectories)
	}
}

func TestConfirmReferenceFrameChangeClearNoRetainsSidecars(t *testing.T) {
	inputs := []mosaic.Input{{Path: `C:\work\input\image.fits`}}
	oldReference := &mosaic.Input{Path: `C:\work\old\baseline.fits`}
	current := oldReference
	deleted := false

	confirmReferenceFrameChangeWith(inputs, current, nil, func() { current = nil }, func(_ string, decide func(bool)) {
		decide(false)
	}, func(string, mosaic.Input) (int, error) {
		deleted = true
		return 0, nil
	}, func(err error) { t.Fatalf("unexpected error: %v", err) })

	if current != nil {
		t.Fatal("clear reference was not applied after choosing No")
	}
	if deleted {
		t.Fatal("sidecars were deleted after choosing No")
	}
}

func TestConfirmReferenceFrameChangeCancellationLeavesReferenceUntouched(t *testing.T) {
	inputs := []mosaic.Input{{Path: `C:\work\input\image.fits`}}
	oldReference := &mosaic.Input{Path: `C:\work\old\baseline.fits`}
	newReference := &mosaic.Input{Path: `C:\work\new\baseline.fits`}
	current := oldReference

	confirmReferenceFrameChangeWith(inputs, current, newReference, func() { current = newReference }, func(_ string, _ func(bool)) {
		// Simulate dismissing the dialog without choosing either option.
	}, func(string, mosaic.Input) (int, error) {
		t.Fatal("delete must not run when the dialog is dismissed")
		return 0, nil
	}, func(err error) { t.Fatalf("unexpected error: %v", err) })

	if current != oldReference {
		t.Fatal("reference changed after the dialog was dismissed")
	}
}

func TestMosaicAlignmentSidecarDirectories(t *testing.T) {
	inputs := []mosaic.Input{
		{Path: `C:\work\blue\one.fits`},
		{Path: `C:\work\red\two.fits`},
		{Path: `C:\work\blue\three.fits`},
	}
	reference := mosaic.Input{Path: `C:\work\reference\baseline.fits`}

	got := mosaicAlignmentSidecarDirectories(inputs, reference)
	want := []string{`C:\work\blue`, `C:\work\red`, `C:\work\reference`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mosaicAlignmentSidecarDirectories() = %#v, want %#v", got, want)
	}
}

func TestMosaicAlignmentSidecarDirectoriesIncludesReferenceDirectoryOnce(t *testing.T) {
	inputs := []mosaic.Input{{Path: `C:\work\reference\target.fits`}}
	reference := mosaic.Input{Path: `C:\work\reference\baseline.fits`}

	got := mosaicAlignmentSidecarDirectories(inputs, reference)
	want := []string{`C:\work\reference`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mosaicAlignmentSidecarDirectories() = %#v, want %#v", got, want)
	}
}
