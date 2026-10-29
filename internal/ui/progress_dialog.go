package ui

import (
	"context"
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// progressTracker wraps a determinate progress dialog with a stage label and a
// Cancel button backed by a context. Its progress method may be passed directly
// as a mosaic.Options.Progress callback and is safe to call off the main thread.
type progressTracker struct {
	dialog dialog.Dialog
	bar    *widget.ProgressBar
	label  *widget.Label
	ctx    context.Context
	cancel context.CancelFunc
}

// newProgressTracker builds and shows a progress dialog from a worker
// goroutine. The returned tracker carries a context that is cancelled when the
// user taps Cancel; pass tracker.ctx to the long-running operation. Call hide()
// when the work completes.
func newProgressTracker(title, initialMessage string, win fyne.Window) *progressTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return newProgressTrackerWithContext(title, initialMessage, win, ctx, cancel)
}

// newProgressTrackerWithContext binds the Cancel button to an already-owned
// operation context (for example a workspace generation coordinator). It is
// safe to call from a worker goroutine.
func newProgressTrackerWithContext(title, initialMessage string, win fyne.Window, ctx context.Context, cancel context.CancelFunc) *progressTracker {
	var pt *progressTracker
	fyne.DoAndWait(func() {
		pt = newProgressTrackerWithContextOnUI(title, initialMessage, win, ctx, cancel)
	})
	return pt
}

// newProgressTrackerWithContextOnUI is for UI event handlers. It must be
// called on Fyne's UI goroutine; worker goroutines should use
// newProgressTrackerWithContext instead.
func newProgressTrackerWithContextOnUI(title, initialMessage string, win fyne.Window, ctx context.Context, cancel context.CancelFunc) *progressTracker {
	if ctx == nil {
		ctx = context.Background()
	}
	pt := &progressTracker{
		bar:    widget.NewProgressBar(),
		label:  widget.NewLabel(initialMessage),
		ctx:    ctx,
		cancel: cancel,
	}
	cancelBtn := widget.NewButton("Cancel", func() {
		pt.cancel()
		pt.label.SetText("Cancelling...")
	})
	content := container.NewVBox(pt.label, pt.bar, cancelBtn)
	pt.dialog = dialog.NewCustomWithoutButtons(title, content, win)
	pt.dialog.Show()
	return pt
}

// progress updates the dialog from a progress callback. When total is zero the
// phase is indeterminate and only the stage label is updated. Safe to call off
// the main thread.
func (pt *progressTracker) progress(stage string, done, total int) {
	fyne.Do(func() {
		if total > 0 {
			pt.bar.Max = float64(total)
			pt.bar.SetValue(float64(done))
			pt.label.SetText(fmt.Sprintf("%s  %d / %d", stage, done, total))
		} else {
			pt.label.SetText(stage)
		}
	})
}

// hide dismisses the dialog on the main thread.
func (pt *progressTracker) hide() {
	fyne.Do(func() {
		if pt.dialog != nil {
			pt.dialog.Hide()
		}
	})
}

// newBusyTrackerOnUI is newProgressTrackerWithContextOnUI with an
// indeterminate bar, for jobs that report no progress. The dialog has no
// dismiss button: Cancel is the only way to close it early, and it cancels
// ctx. UI goroutine only.
func newBusyTrackerOnUI(title, message string, win fyne.Window, ctx context.Context, cancel context.CancelFunc) *progressTracker {
	pt := &progressTracker{
		bar:    widget.NewProgressBar(),
		label:  widget.NewLabel(message),
		ctx:    ctx,
		cancel: cancel,
	}
	cancelBtn := widget.NewButton("Cancel", func() {
		pt.cancel()
		pt.label.SetText("Cancelling...")
	})
	content := container.NewVBox(pt.label, widget.NewProgressBarInfinite(), cancelBtn)
	pt.dialog = dialog.NewCustomWithoutButtons(title, content, win)
	pt.dialog.Show()
	return pt
}

// uiJob is a cancellable background job started from a UI handler: a busy
// dialog whose Cancel button cancels ctx, and a generation from a shared
// jobGeneration so a newer job (or an invalidate on reset) supersedes it.
type uiJob struct {
	ctx    context.Context
	cancel context.CancelFunc
	gen    uint64
	gens   *jobGeneration
	pt     *progressTracker
}

// startUIJob shows the busy dialog and starts a generation in gens. A nil
// gens gives the job a private generation that only Cancel can end. UI
// goroutine only.
func startUIJob(gens *jobGeneration, title, message string, win fyne.Window) *uiJob {
	if gens == nil {
		gens = &jobGeneration{}
	}
	genCtx, gen := gens.begin(nil)
	ctx, cancel := context.WithCancel(genCtx)
	j := &uiJob{ctx: ctx, cancel: cancel, gen: gen, gens: gens}
	j.pt = newBusyTrackerOnUI(title, message, win, ctx, cancel)
	return j
}

// setMessage updates the dialog's stage label. Safe off the UI goroutine.
func (j *uiJob) setMessage(message string) {
	j.pt.progress(message, 0, 0)
}

// finish is called once from the worker. On the UI goroutine it hides the
// dialog, then runs apply when the job is still current and was not
// cancelled, or dropped (when non-nil) otherwise, for cleanup of results that
// will not be applied.
func (j *uiJob) finish(apply, dropped func()) {
	if apply == nil {
		apply = func() {}
	}
	fyne.Do(func() {
		defer j.cancel()
		if j.pt.dialog != nil {
			j.pt.dialog.Hide()
		}
		if !j.gens.publishIfCurrent(j.ctx, j.gen, apply) && dropped != nil {
			dropped()
		}
	})
}
