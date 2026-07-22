package mosaic

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gofitsv3/internal/processing"
)

const alignmentSidecarVersion = 1

// AlignmentSidecarPath returns the per-image alignment filename.  A multi-SCI
// FITS file has one sidecar containing an entry for each SCI extension.
func AlignmentSidecarPath(input Input) string {
	base := filepath.Base(input.Path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(filepath.Dir(input.Path), stem+"_alignment.json")
}

// AlignmentImageIdentity is the file information used to reject transforms
// recorded for an image that has subsequently changed.
type AlignmentImageIdentity struct {
	Path            string `json:"path"`
	SCIExt          int    `json:"sciExt"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	Size            int64  `json:"size"`
	ModTimeUnixNano int64  `json:"modTimeUnixNano"`
}

// AlignmentDiagnostics records alignment-quality information for display and
// future validation. Zero values mean the alignment method did not report it.
type AlignmentDiagnostics struct {
	MatchedStars int     `json:"matchedStars,omitempty"`
	RMS          float64 `json:"rms,omitempty"`
	MedianError  float64 `json:"medianError,omitempty"`
	MaxError     float64 `json:"maxError,omitempty"`
}

// AlignmentSidecarEntry is one target SCI extension's transform into a
// reference image's coordinate system.
type AlignmentSidecarEntry struct {
	Target             AlignmentImageIdentity     `json:"target"`
	Reference          AlignmentImageIdentity     `json:"reference"`
	OffsetX            float64                    `json:"offsetX"`
	OffsetY            float64                    `json:"offsetY"`
	ManualTransform    processing.AffineTransform `json:"transform,omitempty"`
	HasManualTransform bool                       `json:"hasTransform,omitempty"`
	Diagnostics        AlignmentDiagnostics       `json:"diagnostics,omitempty"`
}

type alignmentSidecarFile struct {
	Version int                     `json:"version"`
	Entries []AlignmentSidecarEntry `json:"entries"`
}

// AlignmentSidecarLoadStatus describes why a stored alignment was or was not
// usable. Callers can turn ReferenceChanged into the user-facing warning.
type AlignmentSidecarLoadStatus int

const (
	AlignmentSidecarNotFound AlignmentSidecarLoadStatus = iota
	AlignmentSidecarLoaded
	AlignmentSidecarTargetChanged
	AlignmentSidecarReferenceChanged
	AlignmentSidecarInvalid
)

// AlignmentSidecarLoadOutcome is a structured, non-fatal load result.
type AlignmentSidecarLoadOutcome struct {
	Status      AlignmentSidecarLoadStatus
	SidecarPath string
	Entry       *AlignmentSidecarEntry
	Err         error
}

// BuildAlignmentSidecarEntry captures the current file identities and an
// accepted star-alignment result. It deliberately rejects incomplete results.
func BuildAlignmentSidecarEntry(target, reference Input, result StarAlignmentResult) (AlignmentSidecarEntry, error) {
	if !result.Applied {
		return AlignmentSidecarEntry{}, errors.New("alignment result was not applied")
	}
	targetID, err := alignmentImageIdentity(target)
	if err != nil {
		return AlignmentSidecarEntry{}, fmt.Errorf("identify target: %w", err)
	}
	referenceID, err := alignmentImageIdentity(reference)
	if err != nil {
		return AlignmentSidecarEntry{}, fmt.Errorf("identify reference: %w", err)
	}
	if !finiteAlignment(result) {
		return AlignmentSidecarEntry{}, errors.New("alignment contains non-finite values")
	}
	return AlignmentSidecarEntry{
		Target: targetID, Reference: referenceID,
		OffsetX: result.OffsetX, OffsetY: result.OffsetY,
		ManualTransform: result.ManualTransform, HasManualTransform: result.HasManualTransform,
		Diagnostics: AlignmentDiagnostics{MatchedStars: result.MatchedStars, RMS: result.RMS, MedianError: result.MedianError, MaxError: result.MaxError},
	}, nil
}

// MergeSaveAlignmentSidecar replaces the entry for target's SCI extension and
// preserves entries for the file's other extensions. Writes are atomic.
func MergeSaveAlignmentSidecar(target, reference Input, result StarAlignmentResult) error {
	entry, err := BuildAlignmentSidecarEntry(target, reference, result)
	if err != nil {
		return err
	}
	path := AlignmentSidecarPath(target)
	file := alignmentSidecarFile{Version: alignmentSidecarVersion}
	if data, readErr := os.ReadFile(path); readErr == nil {
		if err := json.Unmarshal(data, &file); err != nil {
			return fmt.Errorf("read existing alignment sidecar %s: %w", filepath.Base(path), err)
		}
		if file.Version != alignmentSidecarVersion {
			return fmt.Errorf("unsupported alignment sidecar version %d", file.Version)
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}

	entries := make([]AlignmentSidecarEntry, 0, len(file.Entries)+1)
	for _, existing := range file.Entries {
		if existing.Target.SCIExt != target.SCIExt {
			entries = append(entries, existing)
		}
	}
	entries = append(entries, entry)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Target.SCIExt < entries[j].Target.SCIExt })
	file.Entries = entries
	return writeAlignmentSidecar(path, file)
}

// LoadValidatedAlignmentSidecar loads the entry for target and verifies both
// images still match the recorded identities.
func LoadValidatedAlignmentSidecar(target, reference Input) AlignmentSidecarLoadOutcome {
	path := AlignmentSidecarPath(target)
	outcome := AlignmentSidecarLoadOutcome{Status: AlignmentSidecarNotFound, SidecarPath: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return outcome
	}
	if err != nil {
		outcome.Status, outcome.Err = AlignmentSidecarInvalid, err
		return outcome
	}
	var file alignmentSidecarFile
	if err := json.Unmarshal(data, &file); err != nil || file.Version != alignmentSidecarVersion {
		outcome.Status = AlignmentSidecarInvalid
		if err != nil {
			outcome.Err = err
		} else {
			outcome.Err = fmt.Errorf("unsupported alignment sidecar version %d", file.Version)
		}
		return outcome
	}
	for i := range file.Entries {
		entry := &file.Entries[i]
		if entry.Target.SCIExt != target.SCIExt {
			continue
		}
		if err := validateAlignmentEntry(*entry); err != nil {
			outcome.Status, outcome.Err = AlignmentSidecarInvalid, err
			return outcome
		}
		if !sameAlignmentIdentity(entry.Target, target) {
			outcome.Status = AlignmentSidecarTargetChanged
			return outcome
		}
		if !sameAlignmentIdentity(entry.Reference, reference) {
			outcome.Status = AlignmentSidecarReferenceChanged
			return outcome
		}
		outcome.Status, outcome.Entry = AlignmentSidecarLoaded, entry
		return outcome
	}
	return outcome
}

// DeleteAlignmentEntriesForReference removes entries based on reference from
// sidecars in directory. Empty sidecars are removed. It returns entry count.
func DeleteAlignmentEntriesForReference(directory string, reference Input) (int, error) {
	refID, err := alignmentImageIdentity(reference)
	if err != nil {
		return 0, fmt.Errorf("identify reference: %w", err)
	}
	paths, err := filepath.Glob(filepath.Join(directory, "*_alignment.json"))
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return deleted, err
		}
		var file alignmentSidecarFile
		if err := json.Unmarshal(data, &file); err != nil || file.Version != alignmentSidecarVersion {
			continue // Never delete a file we cannot safely interpret.
		}
		kept := file.Entries[:0]
		for _, entry := range file.Entries {
			if sameRecordedIdentity(entry.Reference, refID) {
				deleted++
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == len(file.Entries) {
			continue
		}
		if len(kept) == 0 {
			if err := os.Remove(path); err != nil {
				return deleted, err
			}
			continue
		}
		file.Entries = kept
		if err := writeAlignmentSidecar(path, file); err != nil {
			return deleted, err
		}
	}
	return deleted, nil
}

func alignmentImageIdentity(input Input) (AlignmentImageIdentity, error) {
	info, err := os.Stat(input.Path)
	if err != nil {
		return AlignmentImageIdentity{}, err
	}
	width, height := referenceFrameDims(input)
	return AlignmentImageIdentity{Path: filepath.Clean(input.Path), SCIExt: input.SCIExt, Width: width, Height: height, Size: info.Size(), ModTimeUnixNano: info.ModTime().UnixNano()}, nil
}

func sameAlignmentIdentity(recorded AlignmentImageIdentity, input Input) bool {
	current, err := alignmentImageIdentity(input)
	return err == nil && sameRecordedIdentity(recorded, current)
}

func sameRecordedIdentity(a, b AlignmentImageIdentity) bool {
	return filepath.Clean(a.Path) == filepath.Clean(b.Path) && a.SCIExt == b.SCIExt && a.Width == b.Width && a.Height == b.Height && a.Size == b.Size && a.ModTimeUnixNano == b.ModTimeUnixNano
}

func validateAlignmentEntry(entry AlignmentSidecarEntry) error {
	if entry.Target.Path == "" || entry.Reference.Path == "" || entry.Target.Width < 0 || entry.Target.Height < 0 || entry.Reference.Width < 0 || entry.Reference.Height < 0 || !finiteAlignmentResult(entry) {
		return errors.New("invalid alignment sidecar entry")
	}
	return nil
}

func finiteAlignment(result StarAlignmentResult) bool {
	return finiteAlignmentResult(AlignmentSidecarEntry{OffsetX: result.OffsetX, OffsetY: result.OffsetY, ManualTransform: result.ManualTransform, Diagnostics: AlignmentDiagnostics{RMS: result.RMS, MedianError: result.MedianError, MaxError: result.MaxError}})
}

func finiteAlignmentResult(entry AlignmentSidecarEntry) bool {
	values := []float64{entry.OffsetX, entry.OffsetY, entry.ManualTransform.A, entry.ManualTransform.B, entry.ManualTransform.C, entry.ManualTransform.D, entry.ManualTransform.E, entry.ManualTransform.F, entry.Diagnostics.RMS, entry.Diagnostics.MedianError, entry.Diagnostics.MaxError}
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

func writeAlignmentSidecar(path string, file alignmentSidecarFile) error {
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".alignment-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
