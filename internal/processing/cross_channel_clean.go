package processing

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// CrossChannelCleanOptions bounds the working set of the cross-channel cleaner.
// Tile dimensions describe the committed interior; Halo is added on every side.
type CrossChannelCleanOptions struct {
	TileWidth, TileHeight int
	Halo                  int
	Passes                int
	Context               context.Context
	ObserveTile           func(width, height int)
}

// CrossChannelCleanDiskOptions describes bounded row access for disk-backed
// cross-channel cleaning. Readers must fill dst with one complete row.
type CrossChannelCleanDiskOptions struct {
	Widths, Heights []int
	ReadRow         [3]func(y int, dst []float32) error
	WriteRow        [3]func(y int, src []float32) error
	TileWidth       int
	TileHeight      int
	Halo            int
	Passes          int
	Context         context.Context
	// Mask callbacks provide a session-owned temporary byte-per-pixel mask
	// artifact. They are optional for small in-memory callers; disk-backed
	// callers should always provide them so no full bool plane is retained.
	MaskReadRow  [3]func(y int, dst []byte) error
	MaskWriteRow [3]func(y int, src []byte) error
	// MaskScratchReadRow/MaskScratchWriteRow provide an immutable source and
	// separate destination for morphology. Without these callbacks callers
	// retain the legacy in-place behavior for compatibility.
	MaskScratchReadRow  [3]func(y int, dst []byte) error
	MaskScratchWriteRow [3]func(y int, src []byte) error
	// ObserveTile, when non-nil, is called once for each bounded halo tile
	// before it is materialized. It is intended for deterministic tests and
	// diagnostics; production callers may leave it nil.
	ObserveTile func(width, height int)
	// ComponentScratch optionally persists row-major connected-component labels
	// and members. The cleaner remains compatible with callers that only need
	// the byte mask, while disk-backed callers can replay components without
	// retaining a full label plane in memory.
	ComponentScratch [3]CrossChannelComponentScratch
	// CRCandidateScratch separately persists global cosmic-ray candidate
	// labels/members. It is not the star-mask component store.
	CRCandidateScratch [3]GlobalCRCandidateScratch
	CosmicScratch      [3]CosmicRayScratch
	// Progress reports the completed rows for the current disk-backed pass.
	// Callbacks run on the cleaning goroutine and must return promptly.
	Progress func(CrossChannelCleanProgress)
}

// CrossChannelCleanProgress describes one measurable row-oriented pass.
type CrossChannelCleanProgress struct {
	Stage            string
	Completed, Total int
}

func crossChannelCleanProgressReporter(progress []func(string, int, int)) func(string, int, int) {
	if len(progress) == 0 || progress[0] == nil {
		return func(string, int, int) {}
	}
	return progress[0]
}

type CrossChannelComponentScratch struct {
	LabelReadRow  func(y int, dst []uint32) error
	LabelWriteRow func(y int, src []uint32) error
	AppendMember  func(label uint32, index int) error
	// ConsumeMember, when supplied, is called for every canonical component
	// member during replay.  It lets disk-backed callers make an explicit
	// replacement decision without retaining a member list in memory.
	ConsumeMember func(label uint32, index int) error
	// Equivalence callbacks persist the global union table outside Go memory.
	// ReadEquivalence returns (canonical, true, nil) when a mapping exists.
	WriteEquivalence func(label, canonical uint32) error
	ReadEquivalence  func(label uint32) (canonical uint32, ok bool, err error)
}

// GlobalCRCandidateScratch is a row-addressable store for global
// cosmic-ray candidate components. Labels are written in one pass and
// members replayed in another, so no full label/member map is retained.
type GlobalCRCandidateScratch struct {
	LabelReadRow  func(y int, dst []uint32) error
	LabelWriteRow func(y int, src []uint32) error
	AppendMember  func(label uint32, index int) error
	ConsumeMember func(label uint32, index int) error
	// AccumulateComponent is called once for every canonical member before any
	// replacement decisions. Callers should persist compact geometry externally
	// (for example in a fixed-width record keyed by label), rather than retaining
	// an O(component) map in this package.
	AccumulateComponent func(label uint32, index int) error
	// ReadComponentStats returns externally persisted geometry for a label:
	// area, minX, maxX, minY, maxY. When absent, the component is conservatively
	// treated as a cosmic-ray candidate.
	ReadComponentStats func(label uint32) (area, minX, maxX, minY, maxY int, ok bool, err error)
	// Optional external component decision store. DecideComponent is called
	// only when ReadComponentValue reports that no value is persisted yet;
	// WriteMemberValue receives the canonical replacement for every member.
	DecideComponent    func(label uint32, firstMember int) (float32, error)
	ReadComponentValue func(label uint32) (float32, bool, error)
	WriteMemberValue   func(index int, replacement float32) error
	// Equivalence callbacks persist the global union table outside Go memory.
	WriteEquivalence func(label, canonical uint32) error
	ReadEquivalence  func(label uint32) (canonical uint32, ok bool, err error)
}

// CRCandidateScratch is a concise alias for callers that prefer the
// operation name.
type CRCandidateScratch = GlobalCRCandidateScratch

// externalEquivalence resolves global component labels without retaining an
// O(component) table in Go memory.  Disk-backed callers provide callbacks;
// small in-memory callers transparently use the fallback map.
type externalEquivalence struct {
	read  func(uint32) (uint32, bool, error)
	write func(uint32, uint32) error
	mem   map[uint32]uint32
}

func newExternalEquivalence(read func(uint32) (uint32, bool, error), write func(uint32, uint32) error) *externalEquivalence {
	e := &externalEquivalence{read: read, write: write}
	if read == nil {
		e.mem = make(map[uint32]uint32)
	}
	return e
}

func (e *externalEquivalence) resolve(label uint32) (uint32, error) {
	if label == 0 {
		return 0, nil
	}
	cur := label
	for steps := 0; steps < 1<<20; steps++ { // malformed stores cannot loop forever
		var next uint32
		var ok bool
		var err error
		if e.read != nil {
			next, ok, err = e.read(cur)
			if err != nil {
				return 0, err
			}
		} else {
			next, ok = e.mem[cur]
		}
		if !ok || next == 0 || next == cur {
			return cur, nil
		}
		cur = next
	}
	return 0, fmt.Errorf("component equivalence cycle at label %d", label)
}

func (e *externalEquivalence) union(a, b uint32) error {
	ra, err := e.resolve(a)
	if err != nil {
		return err
	}
	rb, err := e.resolve(b)
	if err != nil {
		return err
	}
	if ra == rb {
		return nil
	}
	if rb < ra {
		ra, rb = rb, ra
	}
	if e.write != nil {
		if e.mem != nil {
			e.mem[rb] = ra
		}
		return e.write(rb, ra)
	}
	e.mem[rb] = ra
	return nil
}

// CosmicRayScratch is a row-addressable working store used to replay cosmic
// ray passes without retaining a full float raster in memory.
type CosmicRayScratch struct {
	ReadRow  func(y int, dst []float32) error
	WriteRow func(y int, src []float32) error
	// SeedRow initializes the active immutable input before the first pass.
	// When omitted, WriteRow is used for backwards compatibility.
	SeedRow func(y int, src []float32) error
	// Swap atomically exchanges the immutable input scratch with the output
	// scratch for the next pass.  It is required when passes > 1; keeping the
	// source immutable during a pass is what makes row replay independent of
	// scan order.  A nil Swap is accepted for a single pass for compatibility.
	Swap func() error
}

// CrossChannelCleanDisk applies cross-channel cleaning without materializing
// full float planes. Star-mask candidates are deferred through bounded row
// callbacks, while source values and cosmic-ray work remain bounded to halo
// tiles; output interiors are written sequentially through the supplied callbacks.
func CrossChannelCleanDisk(opts CrossChannelCleanDiskOptions) error {
	if len(opts.Widths) < 3 || len(opts.Heights) < 3 {
		return fmt.Errorf("cross-channel clean requires three channels")
	}
	sharedW, sharedH := opts.Widths[0], opts.Heights[0]
	for i := 0; i < 3; i++ {
		if opts.Widths[i] <= 0 || opts.Heights[i] <= 0 || opts.ReadRow[i] == nil || opts.WriteRow[i] == nil {
			return fmt.Errorf("invalid channel %d", i+1)
		}
		if opts.Widths[i] < sharedW {
			sharedW = opts.Widths[i]
		}
		if opts.Heights[i] < sharedH {
			sharedH = opts.Heights[i]
		}
	}
	if sharedW <= 0 || sharedH <= 0 {
		return fmt.Errorf("empty shared region")
	}
	ctx := opts.Context
	check := func() error {
		if ctx == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	report := func(stage string, completed, total int) {
		if opts.Progress != nil {
			opts.Progress(CrossChannelCleanProgress{Stage: stage, Completed: completed, Total: total})
		}
	}
	// Copy each channel through bounded rows first; this preserves pixels outside
	// the shared region in transactional output artifacts.
	for c := 0; c < 3; c++ {
		row := make([]float32, opts.Widths[c])
		for y := 0; y < opts.Heights[c]; y++ {
			if err := check(); err != nil {
				return err
			}
			if err := opts.ReadRow[c](y, row); err != nil {
				return err
			}
			if err := opts.WriteRow[c](y, row); err != nil {
				return err
			}
			report(fmt.Sprintf("Copying channel %d", c+1), y+1, opts.Heights[c])
		}
	}
	// Build streaming global statistics from sampled rows. The mask pass below
	// retains only boolean component state; float samples remain row/tile bounded.
	sigmas := make([]float64, 3)
	bgs := make([]float64, 3)
	for c := 0; c < 3; c++ {
		row := make([]float32, opts.Widths[c])
		// Match EstimateBackground exactly: sample every linear pixel at the
		// same stride (rather than sampling the first N values of selected rows).
		// Statistics must use the same shared top-left rectangle as the mask
		// operation; trailing pixels from a larger channel are not comparable.
		step := sharedSampleStep(sharedW, sharedH)
		if step < 1 {
			step = 1
		}
		sample := make([]float32, 0, 100000)
		for y := 0; y < sharedH; y++ {
			if err := check(); err != nil {
				return err
			}
			if err := opts.ReadRow[c](y, row); err != nil {
				return err
			}
			base := sharedSampleIndex(y, 0, sharedW)
			for x, v := range row[:sharedW] {
				// Match EstimateBackground's strided walk exactly, including the
				// short tail when the raster size is not divisible by the stride.
				if (base+x)%step == 0 {
					sample = append(sample, v)
				}
			}
			report(fmt.Sprintf("Measuring channel %d", c+1), y+1, opts.Heights[c])
		}
		bgs[c], sigmas[c] = EstimateBackground(sample)
	}
	tw, th, halo := opts.TileWidth, opts.TileHeight, opts.Halo
	if tw <= 0 {
		tw = 256
	}
	if th <= 0 {
		th = 256
	}
	if halo < 6 {
		halo = 6
	}
	passes := opts.Passes
	if passes <= 0 {
		passes = 2
	}
	// Report the bounded tile geometry for instrumentation/UI progress even
	// though computation below is global; no tile is used as a correctness
	// boundary.
	if opts.ObserveTile != nil {
		reportHalo := opts.Halo
		if reportHalo < 0 {
			reportHalo = 0
		}
		for y := 0; y < sharedH; y += th {
			for x := 0; x < sharedW; x += tw {
				x1, y1 := x+tw, y+th
				if x1 > sharedW {
					x1 = sharedW
				}
				if y1 > sharedH {
					y1 = sharedH
				}
				sx0, sy0 := x-reportHalo, y-reportHalo
				if sx0 < 0 {
					sx0 = 0
				}
				if sy0 < 0 {
					sy0 = 0
				}
				sx1, sy1 := x1+reportHalo, y1+reportHalo
				if sx1 > sharedW {
					sx1 = sharedW
				}
				if sy1 > sharedH {
					sy1 = sharedH
				}
				opts.ObserveTile(sx1-sx0, sy1-sy0)
			}
		}
	}
	// Build each channel's star mask by a disk-backed connected-component pass.
	// Masks are written/read one row at a time; no full bool plane or flood-fill
	// queue is retained in Go memory. Repeated propagation converges exactly to
	// the 8-connected closure used by BuildLayerStarMasks.
	var fallback [3][]byte
	for c := 0; c < 3; c++ {
		if opts.MaskReadRow[c] == nil || opts.MaskWriteRow[c] == nil {
			fallback[c] = make([]byte, sharedW*sharedH)
		}
	}
	for c := 0; c < 3; c++ {
		progress := func(stage string, completed, total int) {
			report(fmt.Sprintf("%s (channel %d)", stage, c+1), completed, total)
		}
		if err := buildDiskStarMask(c, sharedW, sharedH, sigmas, bgs, opts.ReadRow, opts.ReadRow[c], opts.MaskReadRow[c], opts.MaskWriteRow[c], opts.MaskScratchReadRow[c], opts.MaskScratchWriteRow[c], fallback[c], check, progress); err != nil {
			return err
		}
		if s := opts.ComponentScratch[c]; s.LabelWriteRow != nil || s.AppendMember != nil {
			if err := replayDiskComponents(sharedW, sharedH, opts.MaskReadRow[c], fallback[c], s, check, progress); err != nil {
				return err
			}
		}
		if s := opts.CRCandidateScratch[c]; s.LabelWriteRow != nil || s.AppendMember != nil {
			maskReader := func(y int, dst []byte) error {
				if y < 0 || y >= sharedH {
					for i := range dst {
						dst[i] = 0
					}
					return nil
				}
				if opts.MaskReadRow[c] != nil {
					return opts.MaskReadRow[c](y, dst)
				}
				copy(dst, fallback[c][y*sharedW:(y+1)*sharedW])
				return nil
			}
			if err := labelGlobalCRCandidates(sharedW, sharedH, opts.ReadRow[c], maskReader, sigmas[c], s, check, progress); err != nil {
				return err
			}
		}
	}
	// Apply the global candidate mask in one channel pass.  This deliberately
	// avoids invoking RemoveCosmicRays independently on halo tiles: connected
	// components (and their star-vs-CR classification) must be formed over the
	// complete shared region so a component crossing a tile seam is unchanged.
	for c := 0; c < 3; c++ {
		if err := check(); err != nil {
			return err
		}
		row := make([]float32, opts.Widths[c])
		if opts.CosmicScratch[c].ReadRow != nil && opts.CosmicScratch[c].WriteRow != nil {
			maskReader := func(y int, dst []byte) error {
				if opts.CRCandidateScratch[c].LabelReadRow != nil {
					labels := make([]uint32, sharedW)
					if err := opts.CRCandidateScratch[c].LabelReadRow(y, labels); err != nil {
						return err
					}
					for i := range dst {
						// Candidate labels are the authoritative replacement set;
						// star labels remain excluded below when no candidate exists.
						dst[i] = 1
						if i < len(labels) && labels[i] != 0 {
							dst[i] = 0
						}
					}
					return nil
				}
				if opts.ComponentScratch[c].LabelReadRow != nil {
					labels := make([]uint32, sharedW)
					if err := opts.ComponentScratch[c].LabelReadRow(y, labels); err != nil {
						return err
					}
					for i := range dst {
						dst[i] = 0
						if labels[i] != 0 {
							dst[i] = 1
						}
					}
					return nil
				}
				if opts.MaskReadRow[c] != nil {
					return opts.MaskReadRow[c](y, dst)
				}
				copy(dst, fallback[c][y*sharedW:(y+1)*sharedW])
				return nil
			}
			for y := 0; y < sharedH; y++ {
				if err := opts.ReadRow[c](y, row); err != nil {
					return err
				}
				seed := opts.CosmicScratch[c].WriteRow
				if opts.CosmicScratch[c].SeedRow != nil {
					seed = opts.CosmicScratch[c].SeedRow
				}
				if err := seed(y, row[:sharedW]); err != nil {
					return err
				}
				report(fmt.Sprintf("Preparing cosmic-ray pass (channel %d)", c+1), y+1, sharedH)
			}
			if err := removeCosmicRaysRows(opts.CosmicScratch[c], sharedW, sharedH, sigmas[c], passes, maskReader, check, func(stage string, completed, total int) {
				report(fmt.Sprintf("%s (channel %d)", stage, c+1), completed, total)
			}); err != nil {
				return err
			}
			for y := 0; y < sharedH; y++ {
				if err := opts.ReadRow[c](y, row); err != nil {
					return err
				}
				if err := opts.CosmicScratch[c].ReadRow(y, row[:sharedW]); err != nil {
					return err
				}
				if err := opts.WriteRow[c](y, row); err != nil {
					return err
				}
				report(fmt.Sprintf("Writing cleaned channel %d", c+1), y+1, sharedH)
			}
			continue
		}
		// Callers that do not provide a row-addressable scratch store are still
		// kept bounded for the single-pass compatibility case. A second pass
		// would require an immutable replacement store; fail explicitly instead
		// of silently materializing a full shared plane.
		if passes > 1 {
			return fmt.Errorf("cross-channel clean requires cosmic scratch for %d passes", passes)
		}
		maskReader := func(y int, dst []byte) error {
			if y < 0 || y >= sharedH {
				for i := range dst {
					dst[i] = 0
				}
				return nil
			}
			if opts.CRCandidateScratch[c].LabelReadRow != nil {
				labels := make([]uint32, sharedW)
				if err := opts.CRCandidateScratch[c].LabelReadRow(y, labels); err != nil {
					return err
				}
				for i := range dst {
					dst[i] = 1
					if i < len(labels) && labels[i] != 0 {
						dst[i] = 0
					}
				}
				return nil
			}
			if opts.ComponentScratch[c].LabelReadRow != nil {
				labels := make([]uint32, sharedW)
				if err := opts.ComponentScratch[c].LabelReadRow(y, labels); err != nil {
					return err
				}
				for i := range dst {
					dst[i] = 0
					if i < len(labels) && labels[i] != 0 {
						dst[i] = 1
					}
				}
				return nil
			}
			if opts.MaskReadRow[c] != nil {
				return opts.MaskReadRow[c](y, dst)
			}
			copy(dst, fallback[c][y*sharedW:(y+1)*sharedW])
			return nil
		}
		direct := CosmicRayScratch{
			ReadRow: func(y int, dst []float32) error {
				if y < 0 || y >= sharedH {
					return fmt.Errorf("row %d outside shared region", y)
				}
				return opts.ReadRow[c](y, dst)
			},
			WriteRow: func(y int, src []float32) error {
				if err := opts.ReadRow[c](y, row); err != nil {
					return err
				}
				copy(row[:sharedW], src[:sharedW])
				return opts.WriteRow[c](y, row)
			},
		}
		if err := removeCosmicRaysRows(direct, sharedW, sharedH, sigmas[c], 1, maskReader, check, func(stage string, completed, total int) {
			report(fmt.Sprintf("%s (channel %d)", stage, c+1), completed, total)
		}); err != nil {
			return err
		}
	}
	return nil
}

// sharedSampleRows returns the compact statistic sample over the shared
// top-left rectangle. The linear base intentionally uses sharedW, not the
// source channel width.
func sharedSampleStep(sharedW, sharedH int) int {
	step := (sharedW * sharedH) / 100000
	if step < 1 {
		step = 1
	}
	return step
}

func sharedSampleIndex(y, x, sharedW int) int { return y*sharedW + x }

// removeCosmicRaysRows replays the cosmic-ray detector over a row-addressable
// working store. It keeps an 11-row halo and two bounded output rows; the
// scratch store is updated pass-by-pass, so memory is independent of image
// height. The candidate mask is read from the persisted component/mask rows.
func removeCosmicRaysRows(s CosmicRayScratch, width, height int, sigma float64, passes int, maskRead func(int, []byte) error, check func() error, progress ...func(string, int, int)) error {
	report := crossChannelCleanProgressReporter(progress)
	if passes <= 0 {
		return nil
	}
	if s.ReadRow == nil || s.WriteRow == nil {
		return fmt.Errorf("cosmic-ray scratch requires row reader and writer")
	}
	if passes > 1 && s.Swap == nil {
		return fmt.Errorf("cosmic-ray scratch requires immutable pass swap")
	}
	if sigma < 0 {
		sigma = 0
	}
	rows := make([][]float32, 11)
	for i := range rows {
		rows[i] = make([]float32, width)
	}
	out := make([]float32, width)
	mask := make([]byte, width)
	for pass := 0; pass < passes; pass++ {
		for y := 0; y < height; y++ {
			if err := check(); err != nil {
				return err
			}
			for k := -5; k <= 5; k++ {
				yy := y + k
				if yy < 0 {
					yy = 0
				}
				if yy >= height {
					yy = height - 1
				}
				if err := s.ReadRow(yy, rows[k+5]); err != nil {
					return err
				}
			}
			if err := maskRead(y, mask); err != nil {
				return err
			}
			copy(out, rows[5])
			for x := 2; x < width-2; x++ {
				if mask[x] != 0 {
					continue
				}
				v := float64(rows[5][x])
				if math.IsNaN(v) || v <= 0 {
					continue
				}
				up, dn := float64(rows[4][x]), float64(rows[6][x])
				l, r := float64(rows[5][x-1]), float64(rows[5][x+1])
				if math.IsNaN(up) {
					up = v
				}
				if math.IsNaN(dn) {
					dn = v
				}
				if math.IsNaN(l) {
					l = v
				}
				if math.IsNaN(r) {
					r = v
				}
				if 4*v-(up+dn+l+r) <= sigma*10 {
					continue
				}
				var vals [121]float64
				n := 0
				for yy := 0; yy < 11; yy++ {
					for xx := maxInt(0, x-5); xx <= minInt(width-1, x+5); xx++ {
						z := float64(rows[yy][xx])
						if !math.IsNaN(z) {
							vals[n] = z
							n++
						}
					}
				}
				if n == 0 {
					continue
				}
				sort.Float64s(vals[:n])
				out[x] = float32(vals[n/2])
			}
			if err := s.WriteRow(y, out); err != nil {
				return err
			}
			report(fmt.Sprintf("Removing cosmic rays (pass %d of %d)", pass+1, passes), y+1, height)
		}
		// Publish the completed immutable output as the next input.  Swap even
		// after the final pass so callers can read the latest artifact through
		// ReadRow without needing a separate "active" flag.
		if s.Swap != nil {
			if err := s.Swap(); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildDiskStarMask mirrors BuildLayerStarMasks while obtaining samples through
// row callbacks. The returned mask is consumed immediately by the bounded tile
// pass; disk callers should provide MaskWriteRow so it is not retained by the
// runtime slot.
func buildDiskStarMask(channel, width, height int, sigmas, bgs []float64, all [3]func(int, []float32) error, read func(int, []float32) error, maskRead func(int, []byte) error, maskWrite func(int, []byte) error, scratchRead func(int, []byte) error, scratchWrite func(int, []byte) error, fallback []byte, check func() error, progress ...func(string, int, int)) error {
	report := crossChannelCleanProgressReporter(progress)
	rows := [3][]float32{make([]float32, width), make([]float32, width), make([]float32, width)}
	mask, prev, next := make([]byte, width), make([]byte, width), make([]byte, width)
	dilated := make([]byte, width)
	origPrev := make([]byte, width)
	readMask := func(y int, dst []byte) error {
		if y < 0 || y >= height {
			for i := range dst {
				dst[i] = 0
			}
			return nil
		}
		if maskRead != nil {
			return maskRead(y, dst)
		}
		copy(dst, fallback[y*width:(y+1)*width])
		return nil
	}
	writeMask := func(y int, src []byte) error {
		if maskWrite != nil {
			return maskWrite(y, src)
		}
		copy(fallback[y*width:(y+1)*width], src)
		return nil
	}
	useScratch := scratchRead != nil && scratchWrite != nil
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		for c := 0; c < 3; c++ {
			if err := all[c](y, rows[c]); err != nil {
				return err
			}
		}
		for x := 0; x < width; x++ {
			n := 0
			for c := 0; c < 3; c++ {
				v := float64(rows[c][x])
				if !math.IsNaN(v) && v > bgs[c]+2*sigmas[c] {
					n++
				}
			}
			v := float64(rows[channel][x])
			if n >= 2 && !math.IsNaN(v) && v > bgs[channel]+sigmas[channel] {
				mask[x] = 1
			} else {
				mask[x] = 0
			}
		}
		if err := writeMask(y, mask); err != nil {
			return err
		}
		report("Building star mask", y+1, height)
	}
	// Preserve the completed primary mask as an immutable source before any
	// morphology pass. Copying scratch before this loop would snapshot the
	// caller's empty artifact and silently disable star-mask dilation.
	if useScratch {
		for y := 0; y < height; y++ {
			if err := check(); err != nil {
				return err
			}
			if err := readMask(y, mask); err != nil {
				return err
			}
			if err := scratchWrite(y, mask); err != nil {
				return err
			}
			report("Preparing star mask", y+1, height)
		}
		readMask = func(y int, dst []byte) error {
			if y < 0 || y >= height {
				clear(dst)
				return nil
			}
			return scratchRead(y, dst)
		}
	}
	for pass, changed := 1, true; changed; pass++ {
		changed = false
		for y := 0; y < height; y++ {
			if err := check(); err != nil {
				return err
			}
			if err := read(y, rows[channel]); err != nil {
				return err
			}
			if !useScratch {
				if y == 0 {
					clear(prev)
				} else {
					copy(prev, origPrev)
				}
			} else if err := readMask(y-1, prev); err != nil {
				return err
			}
			if err := readMask(y, mask); err != nil {
				return err
			}
			if err := readMask(y+1, next); err != nil {
				return err
			}
			if !useScratch {
				copy(origPrev, mask)
			}
			for x := 0; x < width; x++ {
				v := float64(rows[channel][x])
				if math.IsNaN(v) || v <= bgs[channel]+sigmas[channel] || mask[x] != 0 {
					continue
				}
				found := false
				for nx := maxInt(0, x-1); nx <= minInt(width-1, x+1) && !found; nx++ {
					if prev[nx] != 0 || next[nx] != 0 || (nx != x && mask[nx] != 0) {
						found = true
					}
				}
				if found {
					mask[x] = 1
					changed = true
				}
			}
			if err := writeMask(y, mask); err != nil {
				return err
			}
			report(fmt.Sprintf("Growing star mask (pass %d)", pass), y+1, height)
		}
		// When a scratch store is available, publish the completed morphology
		// pass only after every row has been read from the immutable source.
		// This prevents in-place row updates from feeding newly dilated pixels
		// back into the same pass.
		if scratchRead != nil && scratchWrite != nil {
			for y := 0; y < height; y++ {
				if maskRead != nil {
					if err := maskRead(y, mask); err != nil {
						return err
					}
				} else {
					copy(mask, fallback[y*width:(y+1)*width])
				}
				if err := scratchWrite(y, mask); err != nil {
					return err
				}
				report(fmt.Sprintf("Publishing star mask (pass %d)", pass), y+1, height)
			}
		}
	}
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		if scratchRead == nil || scratchWrite == nil {
			if y == 0 {
				clear(prev)
			} else {
				copy(prev, origPrev)
			}
		} else if err := readMask(y-1, prev); err != nil {
			return err
		}
		if err := readMask(y, mask); err != nil {
			return err
		}
		if err := readMask(y+1, next); err != nil {
			return err
		}
		// With no immutable scratch callback, writeMask may update the same
		// backing rows read by readMask. Preserve the source row needed by the
		// next iteration before mutating it, so dilation remains one-pass and
		// deterministic instead of cascading through newly written pixels.
		if scratchRead == nil || scratchWrite == nil {
			copy(origPrev, mask)
		}
		copy(dilated, mask)
		// Match dilateMask exactly: only interior source pixels expand, but
		// their neighbors may include the image border.
		for sy := maxInt(1, y-1); sy <= minInt(height-2, y+1); sy++ {
			var src []byte
			switch sy - y {
			case -1:
				src = prev
			case 0:
				src = mask
			default:
				src = next
			}
			for sx := 1; sx < width-1; sx++ {
				if src[sx] == 0 {
					continue
				}
				for x := maxInt(0, sx-1); x <= minInt(width-1, sx+1); x++ {
					dilated[x] = 1
				}
			}
		}
		if err := writeMask(y, dilated); err != nil {
			return err
		}
		report("Dilating star mask", y+1, height)
	}
	return nil
}

// labelGlobalCRCandidates records the cosmic-ray seed components globally.
// It uses a bounded 11-row window plus a compact union table; component
// members are streamed to the caller-owned scratch store on replay.
func labelGlobalCRCandidates(width, height int, read func(int, []float32) error, maskRead func(int, []byte) error, sigma float64, s GlobalCRCandidateScratch, check func() error, progress ...func(string, int, int)) error {
	report := crossChannelCleanProgressReporter(progress)
	if width <= 0 || height <= 0 || read == nil || (s.LabelWriteRow == nil && s.AppendMember == nil && s.ConsumeMember == nil) {
		return fmt.Errorf("invalid CR candidate scratch geometry")
	}
	prev, curr := make([]uint32, width), make([]uint32, width)
	// The union table is retired at every scanline.  Only labels occurring in
	// the previous/current rows can still connect to future pixels, so keeping
	// the table bounded by two row widths is exact and avoids O(image) growth.
	parent := []uint32{0}
	global := []uint32{0}
	var nextGlobal uint32 = 1
	equiv := newExternalEquivalence(s.ReadEquivalence, s.WriteEquivalence)
	find := func(x uint32) uint32 {
		for x != parent[x] {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b uint32) error {
		ra, rb := find(a), find(b)
		if ra != rb {
			ga, err := equiv.resolve(global[ra])
			if err != nil {
				return err
			}
			gb, err := equiv.resolve(global[rb])
			if err != nil {
				return err
			}
			canonical := ga
			if gb < canonical {
				canonical = gb
			}
			if ga != gb {
				if err := equiv.union(ga, gb); err != nil {
					return err
				}
			}
			if ra < rb {
				parent[rb] = ra
				global[ra] = canonical
			} else {
				parent[ra] = rb
				global[rb] = canonical
			}
		}
		return nil
	}
	rows := make([][]float32, 11)
	for i := range rows {
		rows[i] = make([]float32, width)
	}
	mask := make([]byte, width)
	// A weak CR streak member is allowed to join an already-seeded component
	// when it clears the same grow floor used by RemoveCosmicRays.  Keeping the
	// test local to the current/previous rows preserves the bounded scanline
	// implementation while retaining 8-connected growth (including diagonals).
	var local [121]float64
	localBackground := func(x, y int) float64 {
		n := 0
		for dy := -5; dy <= 5; dy++ {
			yy := y + dy
			if yy < 0 {
				yy = 0
			} else if yy >= height {
				yy = height - 1
			}
			row := rows[yy-y+5]
			for dx := -5; dx <= 5; dx++ {
				xx := x + dx
				if xx < 0 || xx >= width {
					continue
				}
				v := float64(row[xx])
				if !math.IsNaN(v) {
					local[n] = v
					n++
				}
			}
		}
		if n == 0 {
			return 0
		}
		sort.Float64s(local[:n])
		return local[n/2]
	}
	growThreshold := sigma * 2
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		for k := -5; k <= 5; k++ {
			yy := y + k
			row := rows[k+5]
			if yy < 0 || yy >= height {
				for i := range row {
					row[i] = float32(math.NaN())
				}
			} else if err := read(yy, row); err != nil {
				return err
			}
		}
		if err := maskRead(y, mask); err != nil {
			return err
		}
		for x := range curr {
			curr[x] = 0
			v := float64(rows[5][x])
			if mask[x] != 0 || math.IsNaN(v) || v <= 0 || x < 2 || x >= width-2 || y < 2 || y >= height-2 {
				continue
			}
			up, dn := float64(rows[4][x]), float64(rows[6][x])
			l, r := float64(rows[5][x-1]), float64(rows[5][x+1])
			if math.IsNaN(up) {
				up = v
			}
			if math.IsNaN(dn) {
				dn = v
			}
			if math.IsNaN(l) {
				l = v
			}
			if math.IsNaN(r) {
				r = v
			}
			seed := 4*v-(up+dn+l+r) > sigma*10
			grow := false
			if !seed && v > localBackground(x, y)+growThreshold {
				// Existing labels in the 8-connected predecessor neighborhood
				// make this a member of a seeded component.
				grow = (x > 0 && curr[x-1] != 0) || prev[x] != 0 ||
					(x > 0 && prev[x-1] != 0) || (x+1 < width && prev[x+1] != 0)
			}
			if !seed && !grow {
				continue
			}
			var label uint32
			if x > 0 {
				label = curr[x-1]
			}
			if prev[x] != 0 && (label == 0 || prev[x] < label) {
				label = prev[x]
			}
			if x > 0 && prev[x-1] != 0 && (label == 0 || prev[x-1] < label) {
				label = prev[x-1]
			}
			if x+1 < width && prev[x+1] != 0 && (label == 0 || prev[x+1] < label) {
				label = prev[x+1]
			}
			if label == 0 {
				label = uint32(len(parent))
				parent = append(parent, label)
				global = append(global, nextGlobal)
				nextGlobal++
			}
			curr[x] = label
			if x > 0 && curr[x-1] != 0 {
				if err := union(label, curr[x-1]); err != nil {
					return err
				}
			}
			if prev[x] != 0 {
				if err := union(label, prev[x]); err != nil {
					return err
				}
			}
			if x > 0 && prev[x-1] != 0 {
				if err := union(label, prev[x-1]); err != nil {
					return err
				}
			}
			if x+1 < width && prev[x+1] != 0 {
				if err := union(label, prev[x+1]); err != nil {
					return err
				}
			}
		}
		// Once row y has been scanned, row y-1 cannot acquire new unions.
		// Canonicalize and persist it, then compact the labels still live in the
		// current row and retire the old union table.
		if y > 0 {
			for x, label := range prev {
				if label != 0 {
					var err error
					prev[x], err = equiv.resolve(global[find(label)])
					if err != nil {
						return err
					}
				}
			}
			if s.LabelWriteRow != nil {
				if err := s.LabelWriteRow(y-1, prev); err != nil {
					return err
				}
			}
		}
		roots := make(map[uint32]uint32, width)
		oldParent := parent
		oldGlobal := global
		oldFind := func(x uint32) uint32 {
			for x != oldParent[x] {
				oldParent[x] = oldParent[oldParent[x]]
				x = oldParent[x]
			}
			return x
		}
		parent = []uint32{0}
		global = []uint32{0}
		next := uint32(1)
		for x, label := range curr {
			if label == 0 {
				continue
			}
			root := oldFind(label)
			id, ok := roots[root]
			if !ok {
				id = next
				next++
				roots[root] = id
				parent = append(parent, id)
				global = append(global, oldGlobal[root])
			}
			curr[x] = id
		}
		prev, curr = curr, prev
		report("Finding cosmic-ray candidates", y+1, height)
	}
	if height > 0 {
		for x, label := range prev {
			if label != 0 {
				var err error
				prev[x], err = equiv.resolve(global[find(label)])
				if err != nil {
					return err
				}
			}
		}
		if s.LabelWriteRow != nil {
			if err := s.LabelWriteRow(height-1, prev); err != nil {
				return err
			}
		}
		report("Finding cosmic-ray candidates", height, height)
	}
	if s.AppendMember == nil && s.ConsumeMember == nil && s.AccumulateComponent == nil {
		return nil
	}
	if s.LabelReadRow == nil {
		return fmt.Errorf("CR candidate member replay requires label reader")
	}
	// Compute compact component geometry before invoking replacement callbacks.
	// Large, compact components are stars and must remain untouched; only CR
	// components receive one canonical replacement value per member.
	labels := make([]uint32, width)
	// Replay all members before making any component decision.  Decisions may
	// consult an external/disk member index, so invoking one during this pass
	// would observe only a prefix of the component and could choose a different
	// replacement for a component whose equivalence was discovered later.
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		clear(labels)
		if err := s.LabelReadRow(y, labels); err != nil {
			return err
		}
		for x, label := range labels {
			if label != 0 {
				var err error
				label, err = equiv.resolve(label)
				if err != nil {
					return err
				}
				labels[x] = label
			}
		}
		// Persist the resolved roots before replaying members. A component may
		// have been merged only after an earlier row was written, so retaining
		// provisional labels here would make later callbacks observe split
		// components.
		if s.LabelWriteRow != nil {
			if err := s.LabelWriteRow(y, labels); err != nil {
				return err
			}
		}
		report("Resolving cosmic-ray candidates", y+1, height)
	}
	labels = make([]uint32, width)
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		clear(labels)
		if err := s.LabelReadRow(y, labels); err != nil {
			return err
		}
		for x, label := range labels {
			if label != 0 {
				root, err := equiv.resolve(label)
				if err != nil {
					return err
				}
				index := y*width + x
				if s.AppendMember != nil {
					if err := s.AppendMember(root, index); err != nil {
						return err
					}
				}
				if s.AccumulateComponent != nil {
					if err := s.AccumulateComponent(root, index); err != nil {
						return err
					}
				}
				if s.ConsumeMember != nil {
					if err := s.ConsumeMember(root, index); err != nil {
						return err
					}
				}
				labels[x] = root
			}
		}
		// Rewrite the persisted row after all unions are known.  Consumers never
		// observe provisional labels that later merge across a scanline seam.
		if s.LabelWriteRow != nil {
			if err := s.LabelWriteRow(y, labels); err != nil {
				return err
			}
		}
		report("Classifying cosmic-ray candidates", y+1, height)
	}
	if s.WriteMemberValue == nil {
		return nil
	}
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		clear(labels)
		if err := s.LabelReadRow(y, labels); err != nil {
			return err
		}
		for x, root := range labels {
			if root == 0 {
				continue
			}
			isStar := false
			if s.ReadComponentStats != nil {
				area, minX, maxX, minY, maxY, ok, err := s.ReadComponentStats(root)
				if err != nil {
					return err
				}
				if ok {
					boxW, boxH := maxX-minX+1, maxY-minY+1
					boxArea := boxW * boxH
					isStar = area > 45 || (area > 8 && boxArea > 0 && float64(area)/float64(boxArea) > 0.65 && boxW > 2 && boxH > 2)
				}
			}
			if isStar {
				continue
			}
			var replacement float32
			var ok bool
			var err error
			if s.ReadComponentValue != nil {
				replacement, ok, err = s.ReadComponentValue(root)
				if err != nil {
					return err
				}
			}
			if !ok {
				if s.DecideComponent == nil {
					return fmt.Errorf("component replacement requires decision callback")
				}
				replacement, err = s.DecideComponent(root, y*width+x)
				if err != nil {
					return err
				}
			}
			if err := s.WriteMemberValue(y*width+x, replacement); err != nil {
				return err
			}
		}
		report("Recording cosmic-ray replacements", y+1, height)
	}
	return nil
}

// replayDiskComponents labels the byte mask in row-major order using only two
// scanlines plus a compact union table. Labels and members are immediately
// replayed to caller-owned scratch storage, so no full component plane is kept
// in memory. This pass is deliberately separate from mask construction: the
// cleaner can continue to use its bounded halo replay while callers that need
// exact component provenance can persist and inspect the global labels.
func replayDiskComponents(width, height int, maskRead func(int, []byte) error, fallback []byte, s CrossChannelComponentScratch, check func() error, progress ...func(string, int, int)) error {
	report := crossChannelCleanProgressReporter(progress)
	if width <= 0 || height <= 0 || (maskRead == nil && len(fallback) < width*height) {
		return fmt.Errorf("invalid component scratch geometry")
	}
	read := func(y int, dst []byte) error {
		if maskRead != nil {
			return maskRead(y, dst)
		}
		copy(dst, fallback[y*width:(y+1)*width])
		return nil
	}
	prev, curr := make([]uint32, width), make([]uint32, width)
	parent := []uint32{0}
	global := []uint32{0}
	var nextGlobal uint32 = 1
	equiv := newExternalEquivalence(s.ReadEquivalence, s.WriteEquivalence)
	find := func(x uint32) uint32 {
		for x != parent[x] {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b uint32) error {
		ra, rb := find(a), find(b)
		if ra != rb {
			ga, err := equiv.resolve(global[ra])
			if err != nil {
				return err
			}
			gb, err := equiv.resolve(global[rb])
			if err != nil {
				return err
			}
			canonical := ga
			if gb < canonical {
				canonical = gb
			}
			if ga != gb {
				if err := equiv.union(ga, gb); err != nil {
					return err
				}
			}
			if ra < rb {
				parent[rb] = ra
				global[ra] = canonical
			} else {
				parent[ra] = rb
				global[rb] = canonical
			}
		}
		return nil
	}
	mask := make([]byte, width)
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		clear(mask)
		if err := read(y, mask); err != nil {
			return err
		}
		for x := range curr {
			curr[x] = 0
			if mask[x] == 0 {
				continue
			}
			var left, up, upLeft, upRight uint32
			if x > 0 {
				left = curr[x-1]
			}
			up = prev[x]
			if x > 0 {
				upLeft = prev[x-1]
			}
			if x+1 < width {
				upRight = prev[x+1]
			}
			label := left
			for _, n := range [...]uint32{up, upLeft, upRight} {
				if n != 0 && (label == 0 || n < label) {
					label = n
				}
			}
			if label == 0 {
				label = uint32(len(parent))
				parent = append(parent, label)
				global = append(global, nextGlobal)
				nextGlobal++
			}
			curr[x] = label
			for _, n := range [...]uint32{left, up, upLeft, upRight} {
				if n != 0 {
					if err := union(label, n); err != nil {
						return err
					}
				}
			}
		}
		if y > 0 {
			for x, label := range prev {
				if label != 0 {
					var err error
					prev[x], err = equiv.resolve(global[find(label)])
					if err != nil {
						return err
					}
				}
			}
			if s.LabelWriteRow != nil {
				if err := s.LabelWriteRow(y-1, prev); err != nil {
					return err
				}
			}
		}
		oldParent := parent
		oldGlobal := global
		oldFind := func(x uint32) uint32 {
			for x != oldParent[x] {
				oldParent[x] = oldParent[oldParent[x]]
				x = oldParent[x]
			}
			return x
		}
		roots := make(map[uint32]uint32, width)
		parent = []uint32{0}
		global = []uint32{0}
		next := uint32(1)
		for x, label := range curr {
			if label == 0 {
				continue
			}
			root := oldFind(label)
			id, ok := roots[root]
			if !ok {
				id = next
				next++
				roots[root] = id
				parent = append(parent, id)
				global = append(global, oldGlobal[root])
			}
			curr[x] = id
		}
		prev, curr = curr, prev
		report("Labelling star components", y+1, height)
	}
	if height > 0 {
		for x, label := range prev {
			if label != 0 {
				var err error
				prev[x], err = equiv.resolve(global[find(label)])
				if err != nil {
					return err
				}
			}
		}
		if s.LabelWriteRow != nil {
			if err := s.LabelWriteRow(height-1, prev); err != nil {
				return err
			}
		}
		report("Labelling star components", height, height)
	}
	if s.LabelReadRow == nil && s.AppendMember == nil && s.ConsumeMember == nil {
		return nil
	}
	labels := make([]uint32, width)
	for y := 0; y < height; y++ {
		if err := check(); err != nil {
			return err
		}
		if s.LabelReadRow == nil {
			return fmt.Errorf("component member replay requires label reader")
		}
		if err := s.LabelReadRow(y, labels); err != nil {
			return err
		}
		for x, label := range labels {
			if label == 0 {
				continue
			}
			root, err := equiv.resolve(label)
			if err != nil {
				return err
			}
			if s.AppendMember != nil {
				if err := s.AppendMember(root, y*width+x); err != nil {
					return err
				}
			}
			if s.ConsumeMember != nil {
				if err := s.ConsumeMember(root, y*width+x); err != nil {
					return err
				}
			}
			labels[x] = root
		}
		if s.LabelWriteRow != nil {
			if err := s.LabelWriteRow(y, labels); err != nil {
				return err
			}
		}
		report("Replaying star components", y+1, height)
	}
	return nil
}

// canonicalLabel validates and resolves a provisional scanline label.  A
// persisted label row can outlive the in-memory union table only on malformed
// or stale scratch input; report that condition instead of panicking.
func canonicalLabel(label uint32, parent []uint32, find func(uint32) uint32) (uint32, error) {
	if label == 0 {
		return 0, nil
	}
	if int(label) >= len(parent) {
		return 0, fmt.Errorf("component label %d outside union table", label)
	}
	return find(label), nil
}

// CrossChannelCleanTile cleans one halo tile. The input slices are bounded
// tiles with identical dimensions; callers own the returned slices.
func CrossChannelCleanTile(channels [][]float32, width, height int, sigmas []float64, passes int) ([][]float32, error) {
	if len(channels) < 3 || len(sigmas) < 3 || width <= 0 || height <= 0 {
		return nil, fmt.Errorf("cross-channel clean tile requires three channels")
	}
	for i := 0; i < 3; i++ {
		if len(channels[i]) < width*height {
			return nil, fmt.Errorf("channel %d tile is truncated", i+1)
		}
	}
	if passes <= 0 {
		passes = 2
	}
	s := make([]float64, 3)
	for i := range s {
		_, fallback := EstimateBackground(channels[i])
		s[i] = fallback
		if sigmas[i] > 0 {
			s[i] = sigmas[i]
		}
	}
	masks := BuildLayerStarMasks(channels, width, height, s)
	out := make([][]float32, 3)
	for i := 0; i < 3; i++ {
		out[i] = RemoveCosmicRays(channels[i], width, height, s[i], passes, masks[i])
	}
	return out, nil
}

// CrossChannelCleanTiled applies the same star-mask/cosmic-ray operation as the
// normal cleaner to the shared top-left region. Each tile is read with a halo,
// but only its interior is copied to the replacement planes. The returned
// planes are complete copies, making it safe for callers to commit all three
// channels atomically after success.
func CrossChannelCleanTiled(channels [][]float32, widths, heights []int, sigmas []float64, opts CrossChannelCleanOptions) ([][]float32, error) {
	if len(channels) < 3 || len(widths) < 3 || len(heights) < 3 || len(sigmas) < 3 {
		return nil, fmt.Errorf("cross-channel clean requires three channels")
	}
	sharedW, sharedH := widths[0], heights[0]
	if sharedW <= 0 || sharedH <= 0 {
		return nil, fmt.Errorf("invalid channel dimensions")
	}
	for i := 0; i < 3; i++ {
		if widths[i] <= 0 || heights[i] <= 0 || len(channels[i]) < widths[i]*heights[i] {
			return nil, fmt.Errorf("channel %d has truncated pixel data", i+1)
		}
		if widths[i] < sharedW {
			sharedW = widths[i]
		}
		if heights[i] < sharedH {
			sharedH = heights[i]
		}
	}
	if sharedW <= 0 || sharedH <= 0 {
		return nil, fmt.Errorf("empty shared region")
	}
	// Each halo tile computes only its interior ownership region. Callers should
	// choose a halo large enough for morphology and cosmic-ray neighborhoods.
	if opts.Context != nil {
		select {
		case <-opts.Context.Done():
			return nil, opts.Context.Err()
		default:
		}
	}
	passes := opts.Passes
	if passes <= 0 {
		passes = 2
	}
	out := make([][]float32, 3)
	for c := range out {
		out[c] = append([]float32(nil), channels[c]...)
	}
	tw, th, halo := opts.TileWidth, opts.TileHeight, opts.Halo
	if tw <= 0 {
		tw = 256
	}
	if th <= 0 {
		th = 256
	}
	if halo < 6 {
		halo = 6
	}
	for y0 := 0; y0 < sharedH; y0 += th {
		for x0 := 0; x0 < sharedW; x0 += tw {
			if opts.Context != nil {
				select {
				case <-opts.Context.Done():
					return nil, opts.Context.Err()
				default:
				}
			}
			if opts.ObserveTile != nil {
				opts.ObserveTile(minInt(tw, sharedW-x0), minInt(th, sharedH-y0))
			}
			x1, y1 := x0+tw, y0+th
			if x1 > sharedW {
				x1 = sharedW
			}
			if y1 > sharedH {
				y1 = sharedH
			}
			sx0, sy0 := x0-halo, y0-halo
			if sx0 < 0 {
				sx0 = 0
			}
			if sy0 < 0 {
				sy0 = 0
			}
			sx1, sy1 := x1+halo, y1+halo
			if sx1 > sharedW {
				sx1 = sharedW
			}
			if sy1 > sharedH {
				sy1 = sharedH
			}
			w, h := sx1-sx0, sy1-sy0
			tile := make([][]float32, 3)
			for c := 0; c < 3; c++ {
				tile[c] = make([]float32, w*h)
				for yy := 0; yy < h; yy++ {
					copy(tile[c][yy*w:(yy+1)*w], channels[c][(sy0+yy)*widths[c]+sx0:(sy0+yy)*widths[c]+sx1])
				}
			}
			cleaned, err := CrossChannelCleanTile(tile, w, h, sigmas, passes)
			if err != nil {
				return nil, err
			}
			for c := 0; c < 3; c++ {
				for yy := y0; yy < y1; yy++ {
					src := (yy-sy0)*w + x0 - sx0
					copy(out[c][yy*widths[c]+x0:yy*widths[c]+x1], cleaned[c][src:src+x1-x0])
				}
			}
		}
	}
	return out, nil

	/*
		// Legacy halo-tile implementation retained for reference while the disk
		// path is migrated to the deferred connected-component implementation.
		tw, th := opts.TileWidth, opts.TileHeight
		if tw <= 0 {
			tw = 256
		}
		if th <= 0 {
			th = 256
		}
		halo := opts.Halo
		if halo < 6 { // 11x11 background plus morphology neighborhood.
			halo = 6
		}
		passes := opts.Passes
		if passes <= 0 {
			passes = 2
		}
		out := make([][]float32, 3)
		for i := range out {
			out[i] = append([]float32(nil), channels[i]...)
		}
		for y0 := 0; y0 < sharedH; y0 += th {
			for x0 := 0; x0 < sharedW; x0 += tw {
				if opts.Context != nil {
					select {
					case <-opts.Context.Done():
						return nil, opts.Context.Err()
					default:
					}
				}
				x1, y1 := minInt(x0+tw, sharedW), minInt(y0+th, sharedH)
				sx0, sy0 := maxInt(0, x0-halo), maxInt(0, y0-halo)
				sx1, sy1 := minInt(sharedW, x1+halo), minInt(sharedH, y1+halo)
				w, h := sx1-sx0, sy1-sy0
				tile := make([][]float32, 3)
				for c := 0; c < 3; c++ {
					tile[c] = make([]float32, w*h)
					for y := 0; y < h; y++ {
						copy(tile[c][y*w:(y+1)*w], channels[c][(sy0+y)*widths[c]+sx0:(sy0+y)*widths[c]+sx1])
					}
				}
				cleaned, err := CrossChannelCleanTile(tile, w, h, sigmas, passes)
				if err != nil {
					return nil, err
				}
				for c := 0; c < 3; c++ {
					for y := y0; y < y1; y++ {
						src := (y-sy0)*w + (x0 - sx0)
						dst := y*widths[c] + x0
						copy(out[c][dst:dst+(x1-x0)], cleaned[c][src:src+(x1-x0)])
					}
				}
			}
		}
		return out, nil
	*/
}
