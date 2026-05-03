package ui

func clearComposeOrigPixels(origPixels *[3][]float32, idxs ...int) {
	if origPixels == nil {
		return
	}
	for _, idx := range idxs {
		if idx < 0 || idx >= len(origPixels) {
			continue
		}
		origPixels[idx] = nil
	}
}
