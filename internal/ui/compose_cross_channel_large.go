package ui

import (
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"io"
	"math"
	"os"
	"sort"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

// crossChannelCleanLarge runs cross-channel cleaning for large-file mode, where
// channels are disk-backed artifacts in ws.largeStore.
func (ws *composeWorkspace) crossChannelCleanLarge(savedStates []vpState) {
	expected := make([]composeArtifactDescriptor, 3)
	for i := 0; i < 3; i++ {
		d, ok := ws.largeArtifacts[i]
		if !ok || d.Path == "" {
			dialog.ShowInformation("Missing Channels", "Load all three channels before cleaning.", ws.win)
			return
		}
		expected[i] = d
	}
	cleanCtx, cancelClean := context.WithCancel(context.Background())
	cancelButton := widget.NewButton("Cancel", cancelClean)
	progressLabel := widget.NewLabel("Preparing cleaner…")
	progressBar := widget.NewProgressBar()
	progressDialog := dialog.NewCustom("Cleaning", "", container.NewBorder(nil, cancelButton, nil, nil, container.NewVBox(progressLabel, progressBar)), ws.win)
	progressDialog.Show()
	go func() {
		defer cancelClean()
		lastProgressStage, lastProgressPercent := "", -1
		updateProgress := func(stage string, completed, total int) {
			if total <= 0 {
				return
			}
			percent := min(100, completed*100/total)
			if stage == lastProgressStage && percent == lastProgressPercent {
				return
			}
			lastProgressStage, lastProgressPercent = stage, percent
			fyne.Do(func() {
				progressLabel.SetText(fmt.Sprintf("%s — %d%%", stage, percent))
				progressBar.SetValue(float64(completed) / float64(total))
			})
		}
		widths := []int{expected[0].Width, expected[1].Width, expected[2].Width}
		heights := []int{expected[0].Height, expected[1].Height, expected[2].Height}
		sharedW, sharedH := widths[0], heights[0]
		for i := 1; i < 3; i++ {
			if widths[i] < sharedW {
				sharedW = widths[i]
			}
			if heights[i] < sharedH {
				sharedH = heights[i]
			}
		}
		var maskFiles [3]*os.File
		var maskScratchFiles [3]*os.File
		var labelFiles [3]*os.File
		var equivFiles [3]*os.File
		var memberFiles [3]*os.File
		var crLabelFiles [3]*os.File
		var crEquivFiles [3]*os.File
		var crMemberFiles [3]*os.File
		var crStatsFiles [3]*os.File
		var crDecisionFiles [3]*os.File
		var crDecisionValidFiles [3]*os.File
		var crReplacementFiles [3]*os.File
		var crReplacementValidFiles [3]*os.File
		for i := range maskFiles {
			f, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-mask-%d-*.bin", i))
			if e != nil {
				for _, m := range maskFiles {
					if m != nil {
						_ = m.Close()
					}
				}
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			maskFiles[i] = f
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(f)
			sf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-mask-scratch-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			maskScratchFiles[i] = sf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(sf)
			lf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-label-%d-*.bin", i))
			if e != nil {
				for _, m := range maskFiles {
					if m != nil {
						_ = m.Close()
					}
				}
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			labelFiles[i] = lf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(lf)
			ef, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-equiv-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			equivFiles[i] = ef
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(ef)
			mf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-member-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			memberFiles[i] = mf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(mf)
			clf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-label-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crLabelFiles[i] = clf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(clf)
			cef, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-equiv-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crEquivFiles[i] = cef
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(cef)
			cmf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-member-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crMemberFiles[i] = cmf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(cmf)
			stf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-stats-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crStatsFiles[i] = stf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(stf)
			df, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-decision-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crDecisionFiles[i] = df
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(df)
			dvf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-decision-valid-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crDecisionValidFiles[i] = dvf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(dvf)
			rf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-replacement-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crReplacementFiles[i] = rf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(rf)
			rvf, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-cr-replacement-valid-%d-*.bin", i))
			if e != nil {
				fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(e, ws.win) })
				return
			}
			crReplacementValidFiles[i] = rvf
			defer func(f *os.File) { _ = f.Close(); _ = os.Remove(f.Name()) }(rvf)
		}
		var stagedPreviews [3]*image.RGBA
		next, err := ws.largeStore.ReplaceManyIfCurrentArtifactsPrepared(expected, func(outs [3]*fitsio.Float32Artifact) error {
			var readers [3]*fitsio.Float32Artifact
			for i := range readers {
				var e error
				readers[i], e = fitsio.OpenFloat32ArtifactReadOnly(expected[i].Path)
				if e != nil {
					for _, r := range readers {
						if r != nil {
							_ = r.Close()
						}
					}
					return e
				}
				defer readers[i].Close()
			}
			ro := processing.CrossChannelCleanDiskOptions{Widths: widths, Heights: heights, Passes: 2, TileWidth: 256, TileHeight: 256, Halo: 6, Context: cleanCtx, Progress: func(p processing.CrossChannelCleanProgress) {
				updateProgress(p.Stage, p.Completed, p.Total)
			}}
			var scratch [3][2]*fitsio.Float32Artifact
			var scratchPaths [3][2]string
			var scratchActive [3]int
			for i := 0; i < 3; i++ {
				for pass := 0; pass < 2; pass++ {
					sp, e := os.CreateTemp(ws.largeStore.root, fmt.Sprintf("clean-scratch-%d-%d-*.bin", i, pass))
					if e != nil {
						return e
					}
					scratchPaths[i][pass] = sp.Name()
					_ = sp.Close()
					_ = os.Remove(scratchPaths[i][pass])
					scratch[i][pass], e = fitsio.CreateFloat32Artifact(scratchPaths[i][pass], widths[i], heights[i])
					if e != nil {
						return e
					}
					defer func(a *fitsio.Float32Artifact, p string) { _ = a.Close(); _ = os.Remove(p) }(scratch[i][pass], scratchPaths[i][pass])
				}
				idx := i
				ro.CosmicScratch[i] = processing.CosmicRayScratch{
					ReadRow:  func(y int, dst []float32) error { return scratch[idx][scratchActive[idx]].ReadRow(y, dst) },
					WriteRow: func(y int, src []float32) error { return scratch[idx][1-scratchActive[idx]].WriteRow(y, src) },
					SeedRow:  func(y int, src []float32) error { return scratch[idx][scratchActive[idx]].WriteRow(y, src) },
					Swap:     func() error { scratchActive[idx] = 1 - scratchActive[idx]; return nil },
				}
			}
			for i := 0; i < 3; i++ {
				idx := i
				ro.MaskReadRow[i] = func(y int, dst []byte) error {
					buf := dst[:sharedW]
					n, e := maskFiles[idx].ReadAt(buf, int64(y*sharedW))
					if n < len(buf) {
						clear(buf[n:])
					}
					if e == io.EOF {
						return nil
					}
					return e
				}
				ro.MaskWriteRow[i] = func(y int, src []byte) error {
					_, e := maskFiles[idx].WriteAt(src[:sharedW], int64(y*sharedW))
					return e
				}
				ro.MaskScratchReadRow[i] = func(y int, dst []byte) error {
					buf := dst[:sharedW]
					n, e := maskScratchFiles[idx].ReadAt(buf, int64(y*sharedW))
					if n < len(buf) {
						clear(buf[n:])
					}
					if e == io.EOF {
						return nil
					}
					return e
				}
				ro.MaskScratchWriteRow[i] = func(y int, src []byte) error {
					_, e := maskScratchFiles[idx].WriteAt(src[:sharedW], int64(y*sharedW))
					return e
				}
				ro.ComponentScratch[i] = processing.CrossChannelComponentScratch{
					ReadEquivalence: func(label uint32) (uint32, bool, error) {
						var b [4]byte
						n, e := equivFiles[idx].ReadAt(b[:], int64(label)*4)
						if e != nil && e != io.EOF {
							return 0, false, e
						}
						if n != 4 {
							return 0, false, nil
						}
						v := binary.LittleEndian.Uint32(b[:])
						return v, v != 0 && v != label, nil
					},
					WriteEquivalence: func(label, canonical uint32) error {
						var b [4]byte
						binary.LittleEndian.PutUint32(b[:], canonical)
						_, e := equivFiles[idx].WriteAt(b[:], int64(label)*4)
						return e
					},
					LabelReadRow: func(y int, dst []uint32) error {
						buf := make([]byte, sharedW*4)
						n, e := labelFiles[idx].ReadAt(buf, int64(y*sharedW*4))
						if e != nil && e != io.EOF {
							return e
						}
						if n < len(buf) {
							clear(buf[n:])
						}
						for j := 0; j < sharedW && j < len(dst); j++ {
							dst[j] = binary.LittleEndian.Uint32(buf[j*4:])
						}
						return nil
					},
					LabelWriteRow: func(y int, src []uint32) error {
						buf := make([]byte, sharedW*4)
						for j := 0; j < sharedW && j < len(src); j++ {
							binary.LittleEndian.PutUint32(buf[j*4:], src[j])
						}
						_, e := labelFiles[idx].WriteAt(buf, int64(y*sharedW*4))
						return e
					},
					AppendMember: func(label uint32, pixel int) error {
						var rec [8]byte
						binary.LittleEndian.PutUint32(rec[0:4], label)
						binary.LittleEndian.PutUint32(rec[4:8], uint32(pixel))
						_, e := memberFiles[idx].Write(rec[:])
						return e
					},
				}
				ro.CRCandidateScratch[i] = processing.GlobalCRCandidateScratch{
					ReadEquivalence: func(label uint32) (uint32, bool, error) {
						var b [4]byte
						n, e := crEquivFiles[idx].ReadAt(b[:], int64(label)*4)
						if e != nil && e != io.EOF {
							return 0, false, e
						}
						if n != 4 {
							return 0, false, nil
						}
						v := binary.LittleEndian.Uint32(b[:])
						return v, v != 0 && v != label, nil
					},
					WriteEquivalence: func(label, canonical uint32) error {
						var b [4]byte
						binary.LittleEndian.PutUint32(b[:], canonical)
						_, e := crEquivFiles[idx].WriteAt(b[:], int64(label)*4)
						return e
					},
					LabelReadRow: func(y int, dst []uint32) error {
						buf := make([]byte, sharedW*4)
						n, e := crLabelFiles[idx].ReadAt(buf, int64(y*sharedW*4))
						if e != nil && e != io.EOF {
							return e
						}
						if n < len(buf) {
							clear(buf[n:])
						}
						for j := 0; j < sharedW && j < len(dst); j++ {
							dst[j] = binary.LittleEndian.Uint32(buf[j*4:])
						}
						return nil
					},
					LabelWriteRow: func(y int, src []uint32) error {
						buf := make([]byte, sharedW*4)
						for j := 0; j < sharedW && j < len(src); j++ {
							binary.LittleEndian.PutUint32(buf[j*4:], src[j])
						}
						_, e := crLabelFiles[idx].WriteAt(buf, int64(y*sharedW*4))
						return e
					},
					AppendMember: func(label uint32, pixel int) error {
						var rec [8]byte
						binary.LittleEndian.PutUint32(rec[0:4], label)
						binary.LittleEndian.PutUint32(rec[4:8], uint32(pixel))
						_, e := crMemberFiles[idx].Write(rec[:])
						return e
					},
					// Persist compact geometry keyed by canonical label. This keeps
					// replacement classification O(1) per component instead of
					// rescanning the entire member stream for every label.
					AccumulateComponent: func(label uint32, pixel int) error {
						const recSize = int64(40)
						var rec [5]int64
						buf := make([]byte, recSize)
						n, e := crStatsFiles[idx].ReadAt(buf, int64(label)*recSize)
						if e != nil && e != io.EOF {
							return e
						}
						if n == int(recSize) {
							for j := range rec {
								rec[j] = int64(binary.LittleEndian.Uint64(buf[j*8:]))
							}
						} else {
							rec[1], rec[3] = int64(sharedW), int64(sharedH)
							rec[2], rec[4] = -1, -1
						}
						x, y := pixel%sharedW, pixel/sharedW
						rec[0]++
						if int64(x) < rec[1] {
							rec[1] = int64(x)
						}
						if int64(x) > rec[2] {
							rec[2] = int64(x)
						}
						if int64(y) < rec[3] {
							rec[3] = int64(y)
						}
						if int64(y) > rec[4] {
							rec[4] = int64(y)
						}
						for j := range rec {
							binary.LittleEndian.PutUint64(buf[j*8:], uint64(rec[j]))
						}
						_, e = crStatsFiles[idx].WriteAt(buf, int64(label)*recSize)
						return e
					},
					ReadComponentStats: func(label uint32) (int, int, int, int, int, bool, error) {
						const recSize = int64(40)
						var buf [40]byte
						n, e := crStatsFiles[idx].ReadAt(buf[:], int64(label)*recSize)
						if e != nil && e != io.EOF {
							return 0, 0, 0, 0, 0, false, e
						}
						if n != len(buf) {
							return 0, 0, 0, 0, 0, false, nil
						}
						return int(int64(binary.LittleEndian.Uint64(buf[0:8]))), int(int64(binary.LittleEndian.Uint64(buf[8:16]))), int(int64(binary.LittleEndian.Uint64(buf[16:24]))), int(int64(binary.LittleEndian.Uint64(buf[24:32]))), int(int64(binary.LittleEndian.Uint64(buf[32:40]))), true, nil
					},
				}
				// Persist one replacement value per canonical candidate component.
				// A missing record is represented by a short ReadAt, so zero is a
				// valid replacement value as well.
				decisionFile := crDecisionFiles[idx]
				decisionValidFile := crDecisionValidFiles[idx]
				ro.CRCandidateScratch[i].ReadComponentValue = func(label uint32) (float32, bool, error) {
					var buf [4]byte
					var valid [1]byte
					vn, ve := decisionValidFile.ReadAt(valid[:], int64(label))
					if ve != nil && ve != io.EOF {
						return 0, false, ve
					}
					if vn != 1 || valid[0] == 0 {
						return 0, false, nil
					}
					n, e := decisionFile.ReadAt(buf[:], int64(label)*4)
					if e != nil && e != io.EOF {
						return 0, false, e
					}
					if n != len(buf) {
						return 0, false, nil
					}
					return math.Float32frombits(binary.LittleEndian.Uint32(buf[:])), true, nil
				}
				ro.CRCandidateScratch[i].DecideComponent = func(label uint32, firstMember int) (float32, error) {
					x, y := firstMember%sharedW, firstMember/sharedW
					var vals [121]float64
					n := 0
					window := make([]float32, 11)
					for k := 0; k < 11; k++ {
						yy := y + k - 5
						if yy < 0 || yy >= sharedH {
							continue
						}
						sx0, sx1 := x-5, x+6
						if sx0 < 0 {
							sx0 = 0
						}
						if sx1 > sharedW {
							sx1 = sharedW
						}
						if e := readers[idx].ReadRange(yy, sx0, sx1, window[:sx1-sx0]); e != nil {
							return 0, e
						}
						for xx := sx0; xx < sx1; xx++ {
							v := float64(window[xx-sx0])
							if !math.IsNaN(v) {
								vals[n] = v
								n++
							}
						}
					}
					if n == 0 {
						return 0, nil
					}
					sort.Float64s(vals[:n])
					replacement := float32(vals[n/2])
					var buf [4]byte
					binary.LittleEndian.PutUint32(buf[:], math.Float32bits(replacement))
					if _, e := decisionFile.WriteAt(buf[:], int64(label)*4); e != nil {
						return 0, e
					}
					_, e := decisionValidFile.WriteAt([]byte{1}, int64(label))
					return replacement, e
				}
				replacementFile := crReplacementFiles[idx]
				replacementValidFile := crReplacementValidFiles[idx]
				ro.CRCandidateScratch[i].WriteMemberValue = func(pixel int, replacement float32) error {
					if pixel < 0 {
						return fmt.Errorf("negative replacement member index %d", pixel)
					}
					var buf [4]byte
					binary.LittleEndian.PutUint32(buf[:], math.Float32bits(replacement))
					if _, err := replacementFile.WriteAt(buf[:], int64(pixel)*4); err != nil {
						return err
					}
					_, err := replacementValidFile.WriteAt([]byte{1}, int64(pixel))
					return err
				}
			}
			for i := 0; i < 3; i++ {
				idx := i
				ro.ReadRow[i] = func(y int, row []float32) error { return readers[idx].ReadRow(y, row) }
				ro.WriteRow[i] = func(y int, row []float32) error { return outs[idx].WriteRow(y, row) }
			}
			if e := processing.CrossChannelCleanDisk(ro); e != nil {
				return e
			}
			// Cosmic-ray scratch output is now final. Replay deferred component
			// replacements afterward so they cannot be overwritten by the final
			// scratch-to-output copy above.
			for c := 0; c < 3; c++ {
				rowsPerBatch := crossChannelCleanReplayRowsPerBatch(sharedW, sharedH)
				rows := make([]float32, rowsPerBatch*widths[c])
				replacements := make([]byte, rowsPerBatch*sharedW*4)
				valid := make([]byte, rowsPerBatch*sharedW)
				for y := 0; y < sharedH; y += rowsPerBatch {
					if e := cleanCtx.Err(); e != nil {
						return e
					}
					rowCount := min(rowsPerBatch, sharedH-y)
					batchRows := rows[:rowCount*widths[c]]
					batchReplacements := replacements[:rowCount*sharedW*4]
					batchValid := valid[:rowCount*sharedW]
					if e := outs[c].ReadRows(y, rowCount, batchRows); e != nil {
						return e
					}
					validN, e := crReplacementValidFiles[c].ReadAt(batchValid, int64(y*sharedW))
					if e != nil && e != io.EOF {
						return e
					}
					if validN < len(batchValid) {
						clear(batchValid[validN:])
					}
					replacementN, e := crReplacementFiles[c].ReadAt(batchReplacements, int64(y*sharedW)*4)
					if e != nil && e != io.EOF {
						return e
					}
					if replacementN < len(batchReplacements) {
						clear(batchReplacements[replacementN:])
						// Match the former per-pixel behavior: a valid marker
						// without all four replacement bytes must not turn into
						// a zero-valued replacement.
						if complete := replacementN / 4; complete < len(batchValid) {
							clear(batchValid[complete:])
						}
					}
					dirty := false
					for row := 0; row < rowCount; row++ {
						rowStart := row * widths[c]
						sharedStart := row * sharedW
						if applyCrossChannelReplacementRow(batchRows[rowStart:rowStart+sharedW], batchReplacements[sharedStart*4:], batchValid[sharedStart:]) {
							dirty = true
						}
					}
					if dirty {
						if e := outs[c].WriteRows(y, rowCount, batchRows); e != nil {
							return e
						}
					}
					updateProgress(fmt.Sprintf("Applying replacements (channel %d)", c+1), y+rowCount, sharedH)
				}
			}
			return nil
		}, func(staged []composeArtifactDescriptor) error {
			for i := range staged {
				preview, _, _, e := composeLargeStretchedPreview(staged[i].Path, ws.imgs[i])
				if e != nil {
					return e
				}
				stagedPreviews[i] = preview
			}
			return nil
		})
		if err == nil {
			fyne.Do(func() {
				for i := range next {
					ws.largeArtifacts[i] = next[i]
					ws.largePreviews[i] = stagedPreviews[i]
					ws.imgs[i].HDU.Data.Pixels = nil
					clearComposeOrigPixels(&ws.origPixels, i)
				}
				progressDialog.Hide()
				ws.refresh()
				ws.restoreViewportStates(savedStates)
				dialog.ShowInformation("Complete", "Star masks generated and cosmic rays eradicated in the shared region.", ws.win)
			})
		} else {
			fyne.Do(func() { progressDialog.Hide(); dialog.ShowError(err, ws.win) })
		}
		return
		/* unreachable legacy duplicate implementation
		writes := make([]func(*fitsio.Float32Artifact) error, 3)
		for i := 0; i < 3; i++ {
			idx := i
			writes[i] = func(out *fitsio.Float32Artifact) error {
				if err := cleanCtx.Err(); err != nil {
					return err
				}
				src := make([]*fitsio.Float32Artifact, 3)
				for c := range src {
					var err error
					src[c], err = fitsio.OpenFloat32ArtifactReadOnly(expected[c].Path)
					if err != nil {
						for _, a := range src {
							if a != nil {
								_ = a.Close()
							}
						}
						return err
					}
					defer src[c].Close()
				}
				sharedW, sharedH := widths[0], heights[0]
				for c := 1; c < 3; c++ {
					if widths[c] < sharedW {
						sharedW = widths[c]
					}
					if heights[c] < sharedH {
						sharedH = heights[c]
					}
				}
				row := make([]float32, widths[idx])
				for y := 0; y < heights[idx]; y++ {
					if err := cleanCtx.Err(); err != nil {
						return err
					}
					if err := src[idx].ReadRow(y, row); err != nil {
						return err
					}
					if err := out.WriteRow(y, row); err != nil {
						return err
					}
				}
				sigmas := make([]float64, 3)
				sample := make([]float32, 0, 65536)
				maxWidth := widths[0]
				for c := 1; c < 3; c++ {
					if widths[c] > maxWidth {
						maxWidth = widths[c]
					}
				}
				sampleRow := make([]float32, maxWidth)
				for c := 0; c < 3; c++ {
					step := heights[c] / 64
					if step < 1 {
						step = 1
					}
					for y := 0; y < heights[c] && len(sample) < cap(sample); y += step {
						if err := src[c].ReadRow(y, sampleRow[:widths[c]]); err != nil {
							return err
						}
						remain := cap(sample) - len(sample)
						if remain > widths[c] {
							remain = widths[c]
						}
						sample = append(sample, sampleRow[:remain]...)
					}
					_, sigmas[c] = processing.EstimateBackground(sample)
					sample = sample[:0]
				}
				for y0 := 0; y0 < sharedH; y0 += 256 {
					for x0 := 0; x0 < sharedW; x0 += 256 {
						if err := cleanCtx.Err(); err != nil {
							return err
						}
						x1, y1 := x0+256, y0+256
						if x1 > sharedW {
							x1 = sharedW
						}
						if y1 > sharedH {
							y1 = sharedH
						}
						sx0, sy0 := x0-6, y0-6
						if sx0 < 0 {
							sx0 = 0
						}
						if sy0 < 0 {
							sy0 = 0
						}
						sx1, sy1 := x1+6, y1+6
						if sx1 > sharedW {
							sx1 = sharedW
						}
						if sy1 > sharedH {
							sy1 = sharedH
						}
						tw, th := sx1-sx0, sy1-sy0
						tile := make([][]float32, 3)
						for c := 0; c < 3; c++ {
							tile[c] = make([]float32, tw*th)
							rr := make([]float32, widths[c])
							for yy := sy0; yy < sy1; yy++ {
								if err := src[c].ReadRow(yy, rr); err != nil {
									return err
								}
								copy(tile[c][(yy-sy0)*tw:], rr[sx0:sx1])
							}
						}
						cleaned, err := processing.CrossChannelCleanTile(tile, tw, th, sigmas, 2)
						if err != nil {
							return err
						}
						for y := y0; y < y1; y++ {
							if err := out.ReadRow(y, row); err != nil {
								return err
							}
							copy(row[x0:x1], cleaned[idx][(y-sy0)*tw+x0-sx0:(y-sy0)*tw+x1-sx0])
							if err := out.WriteRow(y, row); err != nil {
								return err
							}
						}
					}
				}
				return nil
			}
		}
		previews := make([]*image.RGBA, 3)
		next, err := largeStore.ReplaceManyIfCurrentPrepared(expected, writes, func(staged []composeArtifactDescriptor) error {
			for i := range staged {
				if cancelErr := cleanCtx.Err(); cancelErr != nil {
					return cancelErr
				}
				var previewErr error
				previews[i], _, _, previewErr = composeLargeStretchedPreview(staged[i].Path, imgs[i])
				if previewErr != nil {
					return previewErr
				}
			}
			return nil
		})
		*/
	}()
}
