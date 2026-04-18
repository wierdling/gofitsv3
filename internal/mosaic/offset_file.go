package mosaic

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gofitsv3/internal/fitsio"
	"gofitsv3/internal/processing"
)

type OffsetRecord struct {
	FileName           string
	OffsetX            float64
	OffsetY            float64
	ManualTransform    processing.AffineTransform
	HasManualTransform bool
}

func FilterNameForInput(input Input) string {
	filter := fitsio.FilterString(input.PrimaryHeader)
	if filter == "" {
		filter = fitsio.FilterString(input.HDU.Header)
	}
	return normalizeFilterName(filter)
}

func OffsetFileName(filter string) string {
	filter = normalizeFilterName(filter)
	return filter + "_offsets.txt"
}

func SaveOffsetsForInputs(path string, filter string, inputs []Input) error {
	if len(inputs) == 0 {
		return fmt.Errorf("no FITS inputs loaded")
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	if _, err := fmt.Fprintln(w, normalizeFilterName(filter)); err != nil {
		return err
	}
	for _, input := range inputs {
		if input.HasManualTransform {
			t := input.ManualTransform
			if _, err := fmt.Fprintf(w, "%s\t%.6f\t%.6f\t%.12g\t%.12g\t%.12g\t%.12g\t%.12g\t%.12g\n",
				filepath.Base(input.Path), input.OffsetX, input.OffsetY,
				t.A, t.B, t.C, t.D, t.E, t.F); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(w, "%s\t%.6f\t%.6f\n", filepath.Base(input.Path), input.OffsetX, input.OffsetY); err != nil {
				return err
			}
		}
	}
	return w.Flush()
}

func LoadOffsets(path string) (string, map[string]OffsetRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", nil, err
		}
		return "", nil, fmt.Errorf("offset file is empty")
	}
	filter := normalizeFilterName(scanner.Text())
	records := map[string]OffsetRecord{}
	lineNo := 1
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 3 {
			return "", nil, fmt.Errorf("invalid offset file line %d", lineNo)
		}
		// Try 9-column format first: name ox oy A B C D E F
		if len(parts) >= 9 {
			affineVals := make([]float64, 6)
			affineOk := true
			for k := 0; k < 6; k++ {
				v, err := strconv.ParseFloat(parts[len(parts)-6+k], 64)
				if err != nil {
					affineOk = false
					break
				}
				affineVals[k] = v
			}
			if affineOk {
				ox, errX := strconv.ParseFloat(parts[len(parts)-8], 64)
				oy, errY := strconv.ParseFloat(parts[len(parts)-7], 64)
				if errX != nil || errY != nil {
					return "", nil, fmt.Errorf("invalid offsets on line %d", lineNo)
				}
				name := strings.Join(parts[:len(parts)-8], " ")
				records[name] = OffsetRecord{
					FileName: name, OffsetX: ox, OffsetY: oy,
					ManualTransform: processing.AffineTransform{
						A: affineVals[0], B: affineVals[1], C: affineVals[2],
						D: affineVals[3], E: affineVals[4], F: affineVals[5],
					},
					HasManualTransform: true,
				}
				continue
			}
		}
		// Fall back to 3-column format: name ox oy
		ox, errX := strconv.ParseFloat(parts[len(parts)-2], 64)
		oy, errY := strconv.ParseFloat(parts[len(parts)-1], 64)
		if errX != nil || errY != nil {
			return "", nil, fmt.Errorf("invalid offsets on line %d", lineNo)
		}
		name := strings.Join(parts[:len(parts)-2], " ")
		records[name] = OffsetRecord{FileName: name, OffsetX: ox, OffsetY: oy}
	}
	if err := scanner.Err(); err != nil {
		return "", nil, err
	}
	return filter, records, nil
}

func ApplyOffsetsToInputs(inputs []Input, filter string, records map[string]OffsetRecord) int {
	filter = normalizeFilterName(filter)
	applied := 0
	for i := range inputs {
		if filter != "" && FilterNameForInput(inputs[i]) != filter {
			continue
		}
		if rec, ok := records[filepath.Base(inputs[i].Path)]; ok {
			inputs[i].OffsetX = rec.OffsetX
			inputs[i].OffsetY = rec.OffsetY
			inputs[i].ManualTransform = rec.ManualTransform
			inputs[i].HasManualTransform = rec.HasManualTransform
			applied++
		}
	}
	return applied
}

func AutoLoadOffsets(inputs []Input) (int, []string) {
	groups := map[string][]int{}
	for i, input := range inputs {
		dir := filepath.Dir(input.Path)
		filter := FilterNameForInput(input)
		key := dir + "\n" + filter
		groups[key] = append(groups[key], i)
	}

	appliedTotal := 0
	messages := []string{}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		parts := strings.SplitN(key, "\n", 2)
		dir := parts[0]
		filter := parts[1]
		path := filepath.Join(dir, OffsetFileName(filter))
		if _, err := os.Stat(path); err != nil {
			continue
		}
		loadedFilter, records, err := LoadOffsets(path)
		if err != nil {
			messages = append(messages, fmt.Sprintf("Failed to load %s: %v", filepath.Base(path), err))
			continue
		}
		if loadedFilter != "" && loadedFilter != filter {
			messages = append(messages, fmt.Sprintf("Skipped %s because it is for filter %s, not %s", filepath.Base(path), loadedFilter, filter))
			continue
		}
		applied := 0
		for _, idx := range groups[key] {
			if rec, ok := records[filepath.Base(inputs[idx].Path)]; ok {
				inputs[idx].OffsetX = rec.OffsetX
				inputs[idx].OffsetY = rec.OffsetY
				inputs[idx].ManualTransform = rec.ManualTransform
				inputs[idx].HasManualTransform = rec.HasManualTransform
				applied++
			}
		}
		if applied > 0 {
			appliedTotal += applied
			messages = append(messages, fmt.Sprintf("Loaded %d saved offsets from %s", applied, filepath.Base(path)))
		}
	}

	return appliedTotal, messages
}

func normalizeFilterName(filter string) string {
	filter = strings.TrimSpace(filter)
	filter = strings.Trim(filter, "'")
	filter = strings.TrimSpace(filter)
	if idx := strings.Index(filter, "/"); idx >= 0 {
		filter = strings.TrimSpace(filter[:idx])
	}
	filter = strings.Trim(filter, "'")
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return "Unknown"
	}
	return filter
}
