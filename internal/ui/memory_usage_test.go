package ui

import (
	"runtime"
	"testing"
)

func TestFormatMemoryBytes(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}

	for _, test := range tests {
		if got := formatMemoryBytes(test.bytes); got != test.want {
			t.Errorf("formatMemoryBytes(%d) = %q, want %q", test.bytes, got, test.want)
		}
	}
}

func TestMemoryUsageText(t *testing.T) {
	stats := runtime.MemStats{HeapAlloc: 2 * 1024 * 1024, Sys: 3 * 1024 * 1024}
	if got, want := memoryUsageText(stats), "Memory: 2.0 MiB heap / 3.0 MiB runtime"; got != want {
		t.Fatalf("memoryUsageText() = %q, want %q", got, want)
	}
}
