package processing

import (
	"context"
	"errors"
	"testing"
)

func TestCrossChannelCleanTiledPreservesUnequalOutsideRegion(t *testing.T) {
	chs := [][]float32{make([]float32, 12*10), make([]float32, 10*9), make([]float32, 11*8)}
	chs[0][4*12+4], chs[1][4*10+4], chs[2][4*11+4] = 100, 100, 100
	out, err := CrossChannelCleanTiled(chs, []int{12, 10, 11}, []int{10, 9, 8}, []float64{1, 1, 1}, CrossChannelCleanOptions{TileWidth: 4, TileHeight: 3, Halo: 6})
	if err != nil {
		t.Fatal(err)
	}
	if len(out[0]) != len(chs[0]) || out[0][9*12+11] != chs[0][9*12+11] {
		t.Fatal("outside shared region changed")
	}
}

func TestCrossChannelCleanTiledCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CrossChannelCleanTiled([][]float32{make([]float32, 16), make([]float32, 16), make([]float32, 16)}, []int{4, 4, 4}, []int{4, 4, 4}, []float64{1, 1, 1}, CrossChannelCleanOptions{Context: ctx})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

func TestCrossChannelCleanTiledRejectsTruncated(t *testing.T) {
	_, err := CrossChannelCleanTiled([][]float32{make([]float32, 3), make([]float32, 4), make([]float32, 4)}, []int{2, 2, 2}, []int{2, 2, 2}, []float64{1, 1, 1}, CrossChannelCleanOptions{})
	if err == nil {
		t.Fatal("expected truncated data error")
	}
}

func TestCrossChannelCleanTiledMatchesWholeRegionAcrossSeam(t *testing.T) {
	const w, h = 24, 12
	chs := make([][]float32, 3)
	for c := range chs {
		chs[c] = make([]float32, w*h)
		for i := range chs[c] {
			chs[c][i] = 10
		}
	}
	// A connected component deliberately crosses several tile boundaries and
	// is large enough for the cleaner's star classification to depend on the
	// complete component rather than an arbitrary halo.
	for y := 3; y < 9; y++ {
		for x := 1; x < 23; x++ {
			for c := range chs {
				chs[c][y*w+x] = 100
			}
		}
	}
	whole := make([][]float32, 3)
	sigmas := []float64{1, 1, 1}
	masks := BuildLayerStarMasks(chs, w, h, sigmas)
	for c := range whole {
		whole[c] = RemoveCosmicRays(chs[c], w, h, sigmas[c], 2, masks[c])
	}
	tiled, err := CrossChannelCleanTiled(chs, []int{w, w, w}, []int{h, h, h}, sigmas, CrossChannelCleanOptions{TileWidth: 4, TileHeight: 3, Halo: 1})
	if err != nil {
		t.Fatal(err)
	}
	for c := range whole {
		for i := range whole[c] {
			if tiled[c][i] != whole[c][i] {
				t.Fatalf("channel %d pixel %d differs: tiled=%v whole=%v", c, i, tiled[c][i], whole[c][i])
			}
		}
	}
}

func TestCrossChannelCleanDiskBoundedRows(t *testing.T) {
	const w, h = 9, 7
	src := make([][]float32, 3)
	dst := make([][]float32, 3)
	for c := range src {
		src[c] = make([]float32, w*h)
		dst[c] = make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 10
		}
	}
	for c := range src {
		src[c][3*w+4] = 100
	}
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: 4, TileHeight: 3, Halo: 6, Passes: 1}
	var progress []CrossChannelCleanProgress
	opts.Progress = func(p CrossChannelCleanProgress) { progress = append(progress, p) }
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(dst[cc][y*w:(y+1)*w], row); return nil }
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	if len(dst[0]) != w*h || dst[0][0] != src[0][0] {
		t.Fatal("disk cleaner did not preserve rows")
	}
	if len(progress) == 0 {
		t.Fatal("disk cleaner did not report progress")
	}
	for _, p := range progress {
		if p.Stage == "" || p.Total <= 0 || p.Completed < 1 || p.Completed > p.Total {
			t.Fatalf("invalid progress: %#v", p)
		}
	}
}

func TestCrossChannelCleanDiskMatchesWholeSingleTile(t *testing.T) {
	const w, h = 18, 14
	src := make([][]float32, 3)
	for c := range src {
		src[c] = make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 10
		}
	}
	for y := 5; y < 7; y++ {
		for x := 7; x < 9; x++ {
			for c := range src {
				src[c][y*w+x] = 100
			}
		}
	}
	sigmas := []float64{1, 1, 1}
	masks := BuildLayerStarMasks(src, w, h, sigmas)
	want := make([][]float32, 3)
	for c := range want {
		want[c] = RemoveCosmicRays(src[c], w, h, sigmas[c], 1, masks[c])
	}
	got := make([][]float32, 3)
	for c := range got {
		got[c] = make([]float32, w*h)
		cc := c
		opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: w, TileHeight: h, Halo: 6, Passes: 1}
		for i := 0; i < 3; i++ {
			ci := i
			opts.ReadRow[i] = func(y int, row []float32) error {
				copy(row, src[ci][y*w:(y+1)*w])
				return nil
			}
			opts.WriteRow[i] = func(y int, row []float32) error {
				if i == cc {
					copy(got[cc][y*w:(y+1)*w], row)
				}
				return nil
			}
		}
		if err := CrossChannelCleanDisk(opts); err != nil {
			t.Fatal(err)
		}
	}
	for c := range got {
		for i := range got[c] {
			if got[c][i] != want[c][i] {
				t.Fatalf("channel %d pixel %d differs: got=%v want=%v", c, i, got[c][i], want[c][i])
			}
		}
	}
}

func TestCrossChannelCleanDiskMatchesWholeAcrossMultipleTiles(t *testing.T) {
	const w, h = 24, 12
	src := make([][]float32, 3)
	for c := range src {
		src[c] = make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 10
		}
	}
	// Shared connected component crosses both horizontal and vertical seams.
	for y := 3; y < 9; y++ {
		for x := 1; x < 23; x++ {
			for c := range src {
				src[c][y*w+x] = 100
			}
		}
	}
	sigmas := []float64{1, 1, 1}
	masks := BuildLayerStarMasks(src, w, h, sigmas)
	want := make([][]float32, 3)
	for c := range want {
		want[c] = RemoveCosmicRays(src[c], w, h, sigmas[c], 1, masks[c])
	}
	got := make([][]float32, 3)
	for c := range got {
		got[c] = make([]float32, w*h)
	}
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: 4, TileHeight: 3, Halo: 6, Passes: 1}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(got[cc][y*w:(y+1)*w], row); return nil }
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	for c := range got {
		for i := range got[c] {
			if got[c][i] != want[c][i] {
				t.Fatalf("channel %d pixel %d differs: got=%v want=%v", c, i, got[c][i], want[c][i])
			}
		}
	}
}

func TestCrossChannelCleanDiskUsesDeferredMaskRows(t *testing.T) {
	const w, h = 8, 6
	src, dst := make([][]float32, 3), make([][]float32, 3)
	masks := make([][]byte, 3)
	scratch := make([][]byte, 3)
	for c := 0; c < 3; c++ {
		src[c], dst[c], masks[c], scratch[c] = make([]float32, w*h), make([]float32, w*h), make([]byte, w*h), make([]byte, w*h)
		for i := range src[c] {
			src[c][i] = 1
		}
	}
	for c := 0; c < 3; c++ {
		src[c][2*w+3] = 20
	}
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: 4, TileHeight: 3, Halo: 2, Passes: 1}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(dst[cc][y*w:(y+1)*w], row); return nil }
		opts.MaskReadRow[c] = func(y int, row []byte) error { copy(row, masks[cc][y*w:(y+1)*w]); return nil }
		opts.MaskWriteRow[c] = func(y int, row []byte) error { copy(masks[cc][y*w:(y+1)*w], row); return nil }
		opts.MaskScratchReadRow[c] = func(y int, row []byte) error { copy(row, scratch[cc][y*w:(y+1)*w]); return nil }
		opts.MaskScratchWriteRow[c] = func(y int, row []byte) error { copy(scratch[cc][y*w:(y+1)*w], row); return nil }
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	seen := false
	for c := range masks {
		for _, v := range masks[c] {
			if v != 0 {
				seen = true
			}
		}
	}
	if !seen {
		t.Fatal("deferred mask callbacks were not populated")
	}
	scratchSeen := false
	for c := range scratch {
		for _, v := range scratch[c] {
			if v != 0 {
				scratchSeen = true
			}
		}
	}
	if !scratchSeen {
		t.Fatal("mask scratch callbacks did not receive the primary mask")
	}
}

func TestCrossChannelCleanDiskReportsBoundedHaloTiles(t *testing.T) {
	const w, h = 11, 9
	src := make([][]float32, 3)
	dst := make([][]float32, 3)
	for c := 0; c < 3; c++ {
		src[c], dst[c] = make([]float32, w*h), make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 1
		}
	}
	maxW, maxH := 0, 0
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: 4, TileHeight: 3, Halo: 2, Passes: 1,
		ObserveTile: func(tw, th int) {
			if tw > maxW {
				maxW = tw
			}
			if th > maxH {
				maxH = th
			}
		}}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error {
			copy(row, src[cc][y*w:(y+1)*w])
			return nil
		}
		opts.WriteRow[c] = func(y int, row []float32) error {
			copy(dst[cc][y*w:(y+1)*w], row)
			return nil
		}
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	if maxW > opts.TileWidth+2*opts.Halo || maxH > opts.TileHeight+2*opts.Halo {
		t.Fatalf("halo tile exceeded bound: got %dx%d", maxW, maxH)
	}
}

func TestCrossChannelCleanDiskReplaysGlobalComponentMembers(t *testing.T) {
	const w, h = 7, 5
	src, dst := make([][]float32, 3), make([][]float32, 3)
	for c := 0; c < 3; c++ {
		src[c], dst[c] = make([]float32, w*h), make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 1
		}
		src[c][2*w+2], src[c][2*w+3], src[c][3*w+3] = 50, 50, 50
	}
	labels := make([][]uint32, 3)
	masks := make([][]byte, 3)
	for c := range labels {
		labels[c] = make([]uint32, w*h)
		masks[c] = make([]byte, w*h)
	}
	memberCount := 0
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, TileWidth: 4, TileHeight: 3, Passes: 1}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(dst[cc][y*w:(y+1)*w], row); return nil }
		opts.MaskReadRow[c] = func(y int, row []byte) error { copy(row, masks[cc][y*w:(y+1)*w]); return nil }
		opts.MaskWriteRow[c] = func(y int, row []byte) error { copy(masks[cc][y*w:(y+1)*w], row); return nil }
		opts.ComponentScratch[c] = CrossChannelComponentScratch{
			LabelReadRow:  func(y int, row []uint32) error { copy(row, labels[cc][y*w:(y+1)*w]); return nil },
			LabelWriteRow: func(y int, row []uint32) error { copy(labels[cc][y*w:(y+1)*w], row); return nil },
			AppendMember: func(label uint32, index int) error {
				if label != 0 {
					memberCount++
				}
				return nil
			},
		}
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	if memberCount == 0 {
		t.Fatal("expected component members to be replayed")
	}
}

func TestCrossChannelCleanDiskSeparatesGlobalCRCandidateScratch(t *testing.T) {
	const w, h = 9, 9
	src := make([][]float32, 3)
	dst := make([][]float32, 3)
	labels := make([]uint32, w*h)
	members := 0
	decisions := make(map[uint32]float32)
	writes := 0
	for c := 0; c < 3; c++ {
		src[c], dst[c] = make([]float32, w*h), make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 1
		}
	}
	// A CR candidate is channel-local; if all channels contain the same bright
	// component the cross-channel star mask intentionally suppresses it.
	src[0][4*w+4], src[0][4*w+5] = 40, 40
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, Passes: 1}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(dst[cc][y*w:(y+1)*w], row); return nil }
		opts.CRCandidateScratch[c] = GlobalCRCandidateScratch{
			LabelReadRow:  func(y int, row []uint32) error { copy(row, labels[y*w:(y+1)*w]); return nil },
			LabelWriteRow: func(y int, row []uint32) error { copy(labels[y*w:(y+1)*w], row); return nil },
			AppendMember: func(label uint32, _ int) error {
				if label != 0 {
					members++
				}
				return nil
			},
		}
		if c == 0 {
			opts.CRCandidateScratch[c].ReadComponentValue = func(label uint32) (float32, bool, error) {
				v, ok := decisions[label]
				return v, ok, nil
			}
			opts.CRCandidateScratch[c].DecideComponent = func(label uint32, _ int) (float32, error) {
				decisions[label] = 7.5
				return 7.5, nil
			}
			opts.CRCandidateScratch[c].WriteMemberValue = func(_ int, replacement float32) error {
				if replacement != 7.5 {
					t.Fatalf("replacement=%v, want 7.5", replacement)
				}
				writes++
				return nil
			}
		}
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	if members == 0 {
		t.Fatal("expected global CR candidate members")
	}
	if len(decisions) == 0 || writes == 0 {
		t.Fatalf("expected persisted CR decisions and member writes, decisions=%d writes=%d", len(decisions), writes)
	}
}

func TestCrossChannelCleanDiskCRCallbacksPreserveZeroReplacementAndMemberIndex(t *testing.T) {
	const w, h = 7, 7
	src := make([][]float32, 3)
	dst := make([][]float32, 3)
	for c := 0; c < 3; c++ {
		src[c], dst[c] = make([]float32, w*h), make([]float32, w*h)
		for i := range src[c] {
			src[c][i] = 1
		}
	}
	// Isolated high pixels produce a candidate component in channel 0.
	src[0][3*w+3] = 100
	labels := make([]uint32, w*h)
	var memberIndices []int
	decided := false
	written := false
	opts := CrossChannelCleanDiskOptions{Widths: []int{w, w, w}, Heights: []int{h, h, h}, Passes: 1}
	for c := 0; c < 3; c++ {
		cc := c
		opts.ReadRow[c] = func(y int, row []float32) error { copy(row, src[cc][y*w:(y+1)*w]); return nil }
		opts.WriteRow[c] = func(y int, row []float32) error { copy(dst[cc][y*w:(y+1)*w], row); return nil }
		if c != 0 {
			continue
		}
		opts.CRCandidateScratch[c] = GlobalCRCandidateScratch{
			LabelReadRow:  func(y int, row []uint32) error { copy(row, labels[y*w:(y+1)*w]); return nil },
			LabelWriteRow: func(y int, row []uint32) error { copy(labels[y*w:(y+1)*w], row); return nil },
			AppendMember: func(label uint32, index int) error {
				if label != 0 {
					memberIndices = append(memberIndices, index)
				}
				return nil
			},
			DecideComponent: func(_ uint32, firstMember int) (float32, error) {
				if firstMember != 3*w+3 {
					t.Fatalf("first member index=%d, want %d", firstMember, 3*w+3)
				}
				decided = true
				return 0, nil
			},
			WriteMemberValue: func(index int, replacement float32) error {
				if replacement != 0 {
					t.Fatalf("replacement=%v, want zero", replacement)
				}
				if index != 3*w+3 {
					t.Fatalf("member index=%d, want %d", index, 3*w+3)
				}
				written = true
				return nil
			},
		}
	}
	if err := CrossChannelCleanDisk(opts); err != nil {
		t.Fatal(err)
	}
	if !decided || !written || len(memberIndices) == 0 {
		t.Fatalf("callbacks not invoked: decided=%v written=%v members=%v", decided, written, memberIndices)
	}
}

func TestLabelGlobalCRCandidatesReplaysLateMergeWithCanonicalRoot(t *testing.T) {
	const w, h = 9, 7
	values := make([][]float32, h)
	for y := range values {
		values[y] = make([]float32, w)
		for x := range values[y] {
			values[y][x] = 1
		}
	}
	// Two seed components are written on row 2. The bright bridge on row 3
	// joins them only after row 2 has been persisted, exercising late union
	// replay and canonical-root rewriting.
	values[2][2], values[2][4] = 100, 100
	values[3][3] = 20
	labels := make([][]uint32, h)
	for y := range labels {
		labels[y] = make([]uint32, w)
	}
	equiv := make(map[uint32]uint32)
	var members []struct {
		label uint32
		index int
	}
	decisions := make(map[uint32]float32)
	writes := make([]struct {
		label uint32
		index int
	}, 0, 3)
	scratch := GlobalCRCandidateScratch{
		LabelReadRow: func(y int, row []uint32) error {
			copy(row, labels[y])
			return nil
		},
		LabelWriteRow: func(y int, row []uint32) error {
			copy(labels[y], row)
			return nil
		},
		AppendMember: func(label uint32, index int) error {
			members = append(members, struct {
				label uint32
				index int
			}{label, index})
			return nil
		},
		WriteEquivalence: func(label, canonical uint32) error {
			equiv[label] = canonical
			return nil
		},
		ReadEquivalence: func(label uint32) (uint32, bool, error) {
			canonical, ok := equiv[label]
			return canonical, ok, nil
		},
		DecideComponent: func(label uint32, _ int) (float32, error) {
			decisions[label] = 0
			return 0, nil
		},
		WriteMemberValue: func(index int, replacement float32) error {
			if replacement != 0 {
				t.Fatalf("replacement=%v, want 0", replacement)
			}
			writes = append(writes, struct {
				label uint32
				index int
			}{labels[index/w][index%w], index})
			return nil
		},
	}
	if err := labelGlobalCRCandidates(w, h,
		func(y int, row []float32) error { copy(row, values[y]); return nil },
		func(_ int, row []byte) error { clear(row); return nil }, 5, scratch,
		func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if equiv[2] != 1 {
		t.Fatalf("late union map=%v, want label 2 -> canonical 1", equiv)
	}
	if len(members) != 3 {
		t.Fatalf("members=%v, want three late-merged members", members)
	}
	for _, member := range members {
		if member.label != 1 {
			t.Fatalf("member %+v uses provisional root, want canonical 1", member)
		}
	}
	if len(decisions) != 1 || len(writes) != 3 {
		t.Fatalf("decisions=%v writes=%v, want one decision and three writes", decisions, writes)
	}
}

func TestReplayDiskComponentsCanonicalizesUBridgeBeforeMembers(t *testing.T) {
	const w, h = 5, 3
	mask := [][]byte{
		{1, 0, 0, 0, 1},
		{0, 1, 0, 1, 0},
		{0, 0, 1, 0, 0},
	}
	labels := make([][]uint32, h)
	for y := range labels {
		labels[y] = make([]uint32, w)
	}
	members := make(map[uint32][]int)
	scratch := CrossChannelComponentScratch{
		LabelReadRow: func(y int, row []uint32) error {
			copy(row, labels[y])
			return nil
		},
		LabelWriteRow: func(y int, row []uint32) error {
			copy(labels[y], row)
			return nil
		},
		AppendMember: func(label uint32, index int) error {
			members[label] = append(members[label], index)
			return nil
		},
	}
	if err := replayDiskComponents(w, h, func(y int, row []byte) error {
		copy(row, mask[y])
		return nil
	}, nil, scratch, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("U bridge split into %d components: %#v", len(members), members)
	}
	if got := len(members[1]); got != 5 {
		t.Fatalf("bridge component member count=%d, want 5", got)
	}
	for y := range labels {
		for x, label := range labels[y] {
			want := mask[y][x] != 0
			if (label != 0) != want {
				t.Fatalf("label presence at (%d,%d)=%d, want %v", x, y, label, want)
			}
			if want && label != 1 {
				t.Fatalf("bridge label at (%d,%d)=%d, want canonical 1", x, y, label)
			}
		}
	}
}

func TestReplayDiskComponentsCanonicalizesBoundaryBridge(t *testing.T) {
	const w, h = 4, 4
	mask := [][]byte{
		{1, 1, 0, 0},
		{0, 1, 0, 0},
		{1, 1, 0, 0},
		{0, 0, 0, 0},
	}
	labels := make([][]uint32, h)
	for y := range labels {
		labels[y] = make([]uint32, w)
	}
	counts := map[uint32]int{}
	err := replayDiskComponents(w, h, func(y int, row []byte) error { copy(row, mask[y]); return nil }, nil,
		CrossChannelComponentScratch{
			LabelReadRow:  func(y int, row []uint32) error { copy(row, labels[y]); return nil },
			LabelWriteRow: func(y int, row []uint32) error { copy(labels[y], row); return nil },
			AppendMember:  func(label uint32, _ int) error { counts[label]++; return nil },
		}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(counts) != 1 || counts[1] != 5 {
		t.Fatalf("boundary bridge components=%v, want one canonical component of five", counts)
	}
	for y := range mask {
		for x := range mask[y] {
			if mask[y][x] != 0 && labels[y][x] != 1 {
				t.Fatalf("label at (%d,%d)=%d, want 1", x, y, labels[y][x])
			}
		}
	}
}
