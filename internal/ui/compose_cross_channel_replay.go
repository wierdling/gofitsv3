package ui

import (
	"encoding/binary"
	"math"
)

// applyCrossChannelReplacementRow replays the sparse cosmic-ray replacements
// from one disk row. replacement is little-endian float32 data and valid marks
// the pixels that have a replacement; both are bounded to the shared region.
func applyCrossChannelReplacementRow(row []float32, replacement, valid []byte) bool {
	limit := len(row)
	if len(valid) < limit {
		limit = len(valid)
	}
	if len(replacement)/4 < limit {
		limit = len(replacement) / 4
	}
	dirty := false
	for x := 0; x < limit; x++ {
		if valid[x] == 0 {
			continue
		}
		row[x] = math.Float32frombits(binary.LittleEndian.Uint32(replacement[x*4:]))
		dirty = true
	}
	return dirty
}

const crossChannelCleanReplayBatchBytes = 100 * 1024 * 1024

// crossChannelCleanReplayRowsPerBatch keeps the deferred replacement replay at
// about 100 MiB of pixel data while ensuring narrow images still make progress.
func crossChannelCleanReplayRowsPerBatch(width, height int) int {
	if width <= 0 || height <= 0 {
		return 1
	}
	rows := int(int64(crossChannelCleanReplayBatchBytes) / (int64(width) * 4))
	if rows < 1 {
		rows = 1
	}
	return min(rows, height)
}
