package ui

import "fmt"

func mosaicStatsText(filename string, mean, std float64, width, height int) string {
	stats := fmt.Sprintf("Mean: %.4f | Std: %.4f | Size: %dx%d", mean, std, width, height)
	if filename == "" {
		return stats
	}
	return fmt.Sprintf("File: %s | %s", filename, stats)
}

func mosaicEmptyStatsText() string {
	return "Mean: -- | Std: -- | Size: --"
}
