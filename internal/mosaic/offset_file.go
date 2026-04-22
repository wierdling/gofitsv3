package mosaic

import (
	"bufio"
	"encoding/json"
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
	FileName           string                     `json:"fileName"`
	OffsetX            float64                    `json:"offsetX"`
	OffsetY            float64                    `json:"offsetY"`
	ManualTransform    processing.AffineTransform `json:"transform,omitempty"`
	HasManualTransform bool                       `json:"hasTransform,omitempty"`
	Locked             bool                       `json:"locked,omitempty"`
	Excluded           bool                       `json:"excluded,omitempty"`
}

type offsetFile struct {
	Filter  string         `json:"filter"`
	Offsets []OffsetRecord `json:"offsets"`
}

func FilterNameForInput(input Input) string {
	filter := fitsio.FilterString(input.PrimaryHeader)
	if filter == "" {
		filter = fitsio.FilterString(input.HDU.Header)
	}
	return normalizeFilterName(filter)
}

func inputRecordKey(input Input) string {
	if input.SCIExt > 0 {
		return fmt.Sprintf("%s[sci,%d]", filepath.Base(input.Path), input.SCIExt)
	}
	return filepath.Base(input.Path)
}

func matchingOffsetRecord(records map[string]OffsetRecord, input Input) (OffsetRecord, bool) {
	if rec, ok := records[inputRecordKey(input)]; ok {
		return rec, true
	}
	rec, ok := records[filepath.Base(input.Path)]
	return rec, ok
}

func OffsetFileName(filter string) string {
	filter = normalizeFilterName(filter)
	return filter + "_offsets.json"
}

func SaveOffsetsForInputs(path string, filter string, inputs []Input) error {
	if len(inputs) == 0 {
		return fmt.Errorf("no FITS inputs loaded")
	}
	records := make([]OffsetRecord, 0, len(inputs))
	for _, inp := range inputs {
		rec := OffsetRecord{
			FileName:           inputRecordKey(inp),
			OffsetX:            inp.OffsetX,
			OffsetY:            inp.OffsetY,
			HasManualTransform: inp.HasManualTransform,
			ManualTransform:    inp.ManualTransform,
			Locked:             inp.OffsetLocked,
			Excluded:           inp.Excluded,
		}
		records = append(records, rec)
	}
	of := offsetFile{Filter: normalizeFilterName(filter), Offsets: records}
	data, err := json.MarshalIndent(of, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// LoadOffsets loads an offset file in either JSON (new) or legacy tab-separated (old) format.
// Returns the filter name and a map keyed by base filename.
func LoadOffsets(path string) (string, map[string]OffsetRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}

	// Try JSON first (new format).
	if strings.HasSuffix(strings.ToLower(path), ".json") || (len(data) > 0 && data[0] == '{') {
		var of offsetFile
		if err := json.Unmarshal(data, &of); err != nil {
			return "", nil, fmt.Errorf("parsing offset JSON: %w", err)
		}
		records := make(map[string]OffsetRecord, len(of.Offsets))
		for _, r := range of.Offsets {
			records[r.FileName] = r
		}
		return of.Filter, records, nil
	}

	// Legacy tab-separated text format.
	return loadOffsetsTxt(data)
}

func loadOffsetsTxt(data []byte) (string, map[string]OffsetRecord, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	if !scanner.Scan() {
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
		if rec, ok := matchingOffsetRecord(records, inputs[i]); ok {
			inputs[i].OffsetX = rec.OffsetX
			inputs[i].OffsetY = rec.OffsetY
			inputs[i].ManualTransform = rec.ManualTransform
			inputs[i].HasManualTransform = rec.HasManualTransform
			inputs[i].OffsetLocked = rec.Locked
			inputs[i].Excluded = rec.Excluded
			applied++
		}
	}
	return applied
}

// MasterOffsetFile is the filename used for the directory-wide master offset list.
const MasterOffsetFile = "master_offsets.json"

// UpdateMasterOffsets merges the offsets of inputs into master_offsets.json in dir,
// adding new entries and updating existing ones while preserving entries for files
// not currently loaded.
func UpdateMasterOffsets(dir string, inputs []Input) error {
	path := filepath.Join(dir, MasterOffsetFile)

	records := map[string]OffsetRecord{}
	if _, err := os.Stat(path); err == nil {
		if _, existing, err := LoadOffsets(path); err == nil {
			records = existing
		}
	} else {
		// Try legacy txt master file.
		oldPath := filepath.Join(dir, "master_offsets.txt")
		if _, err2 := os.Stat(oldPath); err2 == nil {
			if _, existing, err2 := LoadOffsets(oldPath); err2 == nil {
				records = existing
			}
		}
	}

	for _, inp := range inputs {
		name := inputRecordKey(inp)
		records[name] = OffsetRecord{
			FileName:           name,
			OffsetX:            inp.OffsetX,
			OffsetY:            inp.OffsetY,
			ManualTransform:    inp.ManualTransform,
			HasManualTransform: inp.HasManualTransform,
			Locked:             inp.OffsetLocked,
			Excluded:           inp.Excluded,
		}
	}

	names := make([]string, 0, len(records))
	for n := range records {
		names = append(names, n)
	}
	sort.Strings(names)

	ordered := make([]OffsetRecord, 0, len(names))
	for _, n := range names {
		ordered = append(ordered, records[n])
	}

	of := offsetFile{Filter: "MASTER", Offsets: ordered}
	data, err := json.MarshalIndent(of, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func AutoLoadOffsets(inputs []Input) (int, []string) {
	dirGroups := map[string][]int{}
	filterGroups := map[string][]int{}
	for i, input := range inputs {
		dir := filepath.Dir(input.Path)
		filter := FilterNameForInput(input)
		dirGroups[dir] = append(dirGroups[dir], i)
		filterGroups[dir+"\n"+filter] = append(filterGroups[dir+"\n"+filter], i)
	}

	appliedTotal := 0
	messages := []string{}
	handledByMaster := map[int]bool{}

	dirs := make([]string, 0, len(dirGroups))
	for dir := range dirGroups {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		masterPath := filepath.Join(dir, MasterOffsetFile)
		if _, err := os.Stat(masterPath); err != nil {
			// Try legacy txt master.
			masterPath = filepath.Join(dir, "master_offsets.txt")
			if _, err2 := os.Stat(masterPath); err2 != nil {
				continue
			}
		}
		_, records, err := LoadOffsets(masterPath)
		if err != nil {
			messages = append(messages, fmt.Sprintf("Failed to load master offsets: %v", err))
			continue
		}
		applied := 0
		for _, idx := range dirGroups[dir] {
			if rec, ok := matchingOffsetRecord(records, inputs[idx]); ok {
				inputs[idx].OffsetX = rec.OffsetX
				inputs[idx].OffsetY = rec.OffsetY
				inputs[idx].ManualTransform = rec.ManualTransform
				inputs[idx].HasManualTransform = rec.HasManualTransform
				inputs[idx].OffsetLocked = rec.Locked
				inputs[idx].Excluded = rec.Excluded
				handledByMaster[idx] = true
				applied++
			}
		}
		if applied > 0 {
			appliedTotal += applied
			messages = append(messages, fmt.Sprintf("Loaded %d saved offsets from master_offsets", applied))
		}
	}

	keys := make([]string, 0, len(filterGroups))
	for key := range filterGroups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		parts := strings.SplitN(key, "\n", 2)
		dir := parts[0]
		filter := parts[1]

		indices := filterGroups[key]
		remaining := indices[:0]
		for _, idx := range indices {
			if !handledByMaster[idx] {
				remaining = append(remaining, idx)
			}
		}
		if len(remaining) == 0 {
			continue
		}

		// Try JSON first, then legacy txt.
		path := filepath.Join(dir, OffsetFileName(filter))
		if _, err := os.Stat(path); err != nil {
			path = filepath.Join(dir, filter+"_offsets.txt")
			if _, err2 := os.Stat(path); err2 != nil {
				continue
			}
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
		for _, idx := range remaining {
			if rec, ok := matchingOffsetRecord(records, inputs[idx]); ok {
				inputs[idx].OffsetX = rec.OffsetX
				inputs[idx].OffsetY = rec.OffsetY
				inputs[idx].ManualTransform = rec.ManualTransform
				inputs[idx].HasManualTransform = rec.HasManualTransform
				inputs[idx].OffsetLocked = rec.Locked
				inputs[idx].Excluded = rec.Excluded
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
