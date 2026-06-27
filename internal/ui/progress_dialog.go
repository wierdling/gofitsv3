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

// newProgressTracker builds and shows a progress dialog on the main thread. The
// returned tracker carries a context that is cancelled when the user taps
// Cancel; pass tracker.ctx to the long-running operation. Call hide() when the
// work completes.
func newProgressTracker(title, initialMessage string, win fyne.Window) *progressTracker {
	ctx, cancel := context.WithCancel(context.Background())
	pt := &progressTracker{
		bar:    widget.NewProgressBar(),
		label:  widget.NewLabel(initialMessage),
		ctx:    ctx,
		cancel: cancel,
	}
	fyne.DoAndWait(func() {
		cancelBtn := widget.NewButton("Cancel", func() {
			pt.cancel()
			pt.label.SetText("Cancelling...")
		})
		content := container.NewVBox(pt.label, pt.bar, cancelBtn)
		pt.dialog = dialog.NewCustomWithoutButtons(title, content, win)
		pt.dialog.Show()
	})
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
