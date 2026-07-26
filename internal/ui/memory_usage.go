package ui

import (
	"fmt"
	"runtime"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"
)

const memoryUsageRefreshInterval = time.Second

// newMemoryUsageLabel creates the application-wide memory indicator. Heap is
// live Go allocation; runtime is the total memory the Go runtime has obtained
// from the operating system for heaps, stacks, and runtime bookkeeping.
func newMemoryUsageLabel() *widget.Label {
	return widget.NewLabel(memoryUsageText(currentMemoryUsage()))
}

func currentMemoryUsage() runtime.MemStats {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats
}

func memoryUsageText(stats runtime.MemStats) string {
	return fmt.Sprintf("Memory: %s heap / %s runtime", formatMemoryBytes(stats.HeapAlloc), formatMemoryBytes(stats.Sys))
}

func formatMemoryBytes(bytes uint64) string {
	const unit = 1024
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	value := float64(bytes)
	unitIndex := 0
	for value >= unit && unitIndex < len(units)-1 {
		value /= unit
		unitIndex++
	}
	if unitIndex == 0 {
		return fmt.Sprintf("%d %s", bytes, units[unitIndex])
	}
	return fmt.Sprintf("%.1f %s", value, units[unitIndex])
}

// monitorMemoryUsage refreshes label on the UI thread until stop is called.
func monitorMemoryUsage(label *widget.Label) func() {
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		ticker := time.NewTicker(memoryUsageRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				text := memoryUsageText(currentMemoryUsage())
				fyne.Do(func() { label.SetText(text) })
			case <-stop:
				return
			}
		}
	}()

	return func() { once.Do(func() { close(stop) }) }
}
