package ui

import (
	"fmt"
	"path/filepath"
	"sort"

	"fyne.io/fyne/v2/dialog"

	"gofitsv3/internal/mosaic"
)

// confirmReferenceFrameChange runs apply after the user has decided whether to
// remove transforms based on the current effective reference. A retained
// sidecar cannot be loaded against the new reference and is therefore safe to
// leave in place for a later return to the original reference frame.
func (ws *mosaicWorkspace) confirmReferenceFrameChange(next *mosaic.Input, apply func()) {
	confirmReferenceFrameChangeWith(ws.state.inputs, ws.state.referenceInput, next, func() {
		ws.cancelMosaicBuild()
		ws.cancelMosaicAlignment()
		apply()
	},
		func(message string, decide func(bool)) {
			dialog.ShowConfirm("Reference Frame Changed", message, decide, ws.win)
		},
		mosaic.DeleteAlignmentEntriesForReference,
		func(err error) { dialog.ShowError(err, ws.win) },
	)
}

type referenceFrameChangeConfirmation func(message string, decide func(remove bool))
type alignmentReferenceDeleter func(directory string, reference mosaic.Input) (int, error)

// confirmReferenceFrameChangeWith contains the state-transition sequencing for
// a reference change. The UI supplies the dialog and error display, keeping
// this behavior testable without a live Fyne window.
func confirmReferenceFrameChangeWith(inputs []mosaic.Input, current, next *mosaic.Input, apply func(), confirm referenceFrameChangeConfirmation, deleteEntries alignmentReferenceDeleter, showError func(error)) {
	oldReference, hasOldReference := effectiveMosaicAlignmentReference(inputs, current)
	newReference, hasNewReference := effectiveMosaicAlignmentReference(inputs, next)
	if !hasOldReference || !hasNewReference || sameMosaicAlignmentInput(oldReference, newReference) {
		apply()
		return
	}

	message := fmt.Sprintf("The reference frame is changing. Delete the saved alignment files that were created against %s? Their transforms will no longer be valid.", mosaic.InputLabel(oldReference))
	confirm(message, func(remove bool) {
		if remove {
			for _, directory := range mosaicAlignmentSidecarDirectories(inputs, oldReference) {
				if _, err := deleteEntries(directory, oldReference); err != nil {
					showError(fmt.Errorf("delete saved alignments: %w", err))
					return
				}
			}
		}
		apply()
	})
}

// mosaicAlignmentSidecarDirectories returns every directory in which an
// alignment target sidecar for this workspace may exist, plus the reference's
// directory. Sorting keeps deletion failures reproducible.
func mosaicAlignmentSidecarDirectories(inputs []mosaic.Input, reference mosaic.Input) []string {
	directories := map[string]struct{}{filepath.Dir(reference.Path): {}}
	for _, input := range inputs {
		directories[filepath.Dir(input.Path)] = struct{}{}
	}
	result := make([]string, 0, len(directories))
	for directory := range directories {
		result = append(result, directory)
	}
	sort.Strings(result)
	return result
}
