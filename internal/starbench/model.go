// Package starbench provides local, independent annotation and evaluation of
// star-map outputs. It never writes labels into the detector's source catalog.
package starbench

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SchemaVersion = 1

type Region struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	X                    int    `json:"x"`
	Y                    int    `json:"y"`
	Width                int    `json:"width"`
	Height               int    `json:"height"`
	Split                string `json:"split"`
	Reviewed             bool   `json:"reviewed"`
	RevealedAt           string `json:"revealedAt,omitempty"`
	RevealedBeforeReview bool   `json:"revealedBeforeReview,omitempty"`
	EditedAfterReveal    bool   `json:"editedAfterReveal,omitempty"`
}
type Label struct {
	ID       string   `json:"id"`
	RegionID string   `json:"regionId"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	Kind     string   `json:"kind"`
	Tags     []string `json:"tags"`
	Note     string   `json:"note"`
}
type Protected struct {
	ID       string `json:"id"`
	RegionID string `json:"regionId"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}
type Project struct {
	Version     int         `json:"version"`
	DatasetHash string      `json:"datasetHash"`
	Revision    int         `json:"revision"`
	Regions     []Region    `json:"regions"`
	Labels      []Label     `json:"labels"`
	Protected   []Protected `json:"protected"`
}
type Dataset struct {
	Version     int    `json:"version"`
	SciencePath string `json:"sciencePath"`
	SHA256      string `json:"sha256"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	Filter      string `json:"filter"`
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func newID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func inside(r Region, x, y float64, margin float64) bool {
	return x >= float64(r.X)+margin && y >= float64(r.Y)+margin && x < float64(r.X+r.Width)-margin && y < float64(r.Y+r.Height)-margin
}
func validRect(x, y, w, h, maxW, maxH int) bool {
	return x >= 0 && y >= 0 && w > 0 && h > 0 && w <= maxW && h <= maxH && x <= maxW-w && y <= maxH-h
}

func DefaultProject(d Dataset) Project {
	p := Project{Version: SchemaVersion, DatasetHash: d.SHA256, Regions: []Region{}, Labels: []Label{}, Protected: []Protected{}}
	cols, rows := min(4, max(1, d.Width/128)), min(3, max(1, d.Height/128))
	w, h := min(512, d.Width/cols), min(512, d.Height/rows)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			split := "development"
			if i%3 == 2 {
				split = "validation"
			}
			p.Regions = append(p.Regions, Region{ID: fmt.Sprintf("r%02d", i+1), Name: fmt.Sprintf("Region %02d", i+1), X: (x*d.Width/cols + (x+1)*d.Width/cols - w) / 2, Y: (y*d.Height/rows + (y+1)*d.Height/rows - h) / 2, Width: w, Height: h, Split: split})
		}
	}
	return p
}

func ValidateProject(p Project, d Dataset) error {
	if p.Version != SchemaVersion || p.DatasetHash != d.SHA256 {
		return fmt.Errorf("labels belong to another dataset or schema")
	}
	if len(p.Regions) > 200 || len(p.Labels) > 100000 || len(p.Protected) > 10000 {
		return fmt.Errorf("annotation limit exceeded")
	}
	regions := map[string]Region{}
	ids := map[string]bool{}
	for _, r := range p.Regions {
		if r.ID == "" || len(r.ID) > 80 || len(r.Name) > 160 || r.Name == "" || regions[r.ID].ID != "" {
			return fmt.Errorf("invalid or duplicate region identity")
		}
		if !validRect(r.X, r.Y, r.Width, r.Height, d.Width, d.Height) || r.Width < 16 || r.Height < 16 || r.Width > 2048 || r.Height > 2048 {
			return fmt.Errorf("region %s has invalid bounds", r.Name)
		}
		if r.Split != "development" && r.Split != "validation" {
			return fmt.Errorf("invalid split for %s", r.Name)
		}
		for _, other := range regions {
			if r.X < other.X+other.Width && r.X+r.Width > other.X && r.Y < other.Y+other.Height && r.Y+r.Height > other.Y {
				return fmt.Errorf("regions %s and %s overlap", r.Name, other.Name)
			}
		}
		regions[r.ID] = r
	}
	for _, l := range p.Labels {
		r, ok := regions[l.RegionID]
		if !ok || l.ID == "" || len(l.ID) > 80 || ids[l.ID] || !finite(l.X) || !finite(l.Y) || !inside(r, l.X, l.Y, 0) {
			return fmt.Errorf("invalid label %s", l.ID)
		}
		ids[l.ID] = true
		switch l.Kind {
		case "star", "nonstar", "ambiguous", "unusable":
		default:
			return fmt.Errorf("invalid label class")
		}
		if len(l.Note) > 1000 || len(l.Tags) > 8 {
			return fmt.Errorf("label text is too long")
		}
		seenTags := map[string]bool{}
		for _, tag := range l.Tags {
			if seenTags[tag] {
				return fmt.Errorf("duplicate label tag %s", tag)
			}
			seenTags[tag] = true
			switch tag {
			case "bright", "saturated", "on-nebula", "blended", "artifact":
			default:
				return fmt.Errorf("unknown label tag %s", tag)
			}
		}
	}
	for _, box := range p.Protected {
		r, ok := regions[box.RegionID]
		if !ok || box.ID == "" || ids[box.ID] || !validRect(box.X-r.X, box.Y-r.Y, box.Width, box.Height, r.Width, r.Height) {
			return fmt.Errorf("invalid protected area %s", box.ID)
		}
		ids[box.ID] = true
	}
	return nil
}

// MergeProject protects reveal history and optimistic revisions. The client
// cannot erase evidence that labels were changed after seeing a detector run.
func MergeProject(old, next Project, d Dataset) (Project, error) {
	if next.Revision != old.Revision {
		return Project{}, fmt.Errorf("labels changed in another browser; reload before editing")
	}
	if err := ValidateProject(next, d); err != nil {
		return Project{}, err
	}
	for i := range next.Regions {
		r := &next.Regions[i]
		for _, previous := range old.Regions {
			if previous.ID != r.ID {
				continue
			}
			if previous.RevealedAt != "" && (previous.X != r.X || previous.Y != r.Y || previous.Width != r.Width || previous.Height != r.Height || previous.Split != r.Split) {
				return Project{}, fmt.Errorf("revealed region geometry and split cannot change; create a new benchmark to change its partition")
			}
			r.RevealedAt = previous.RevealedAt
			r.EditedAfterReveal = previous.EditedAfterReveal
			r.RevealedBeforeReview = previous.RevealedBeforeReview
			if previous.RevealedAt != "" && regionAnnotations(old, r.ID) != regionAnnotations(next, r.ID) {
				r.EditedAfterReveal = true
			}
		}
	}
	// Revealed regions cannot be deleted and recreated to clear audit history.
	for _, r := range old.Regions {
		if r.RevealedAt == "" {
			continue
		}
		found := false
		for _, n := range next.Regions {
			if n.ID == r.ID {
				found = true
			}
		}
		if !found {
			return Project{}, fmt.Errorf("revealed regions cannot be deleted")
		}
	}
	// New regions never accept client-supplied audit metadata.
	for i := range next.Regions {
		found := false
		for _, r := range old.Regions {
			if r.ID == next.Regions[i].ID {
				found = true
			}
		}
		if !found {
			next.Regions[i].RevealedAt = ""
			next.Regions[i].EditedAfterReveal = false
			next.Regions[i].RevealedBeforeReview = false
		}
	}
	next.Revision = old.Revision + 1
	return next, nil
}
func regionAnnotations(p Project, id string) string {
	var labels []Label
	var boxes []Protected
	for _, l := range p.Labels {
		if l.RegionID == id {
			labels = append(labels, l)
		}
	}
	for _, b := range p.Protected {
		if b.RegionID == id {
			boxes = append(boxes, b)
		}
	}
	b, _ := json.Marshal(struct {
		Labels []Label
		Boxes  []Protected
	}{labels, boxes})
	return string(b)
}

// writeJSON keeps an immutable revision history in addition to labels.json.
// The fallback replacement handles Windows without deleting the only old copy.
func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}
func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".benchmark-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err == nil {
		return nil
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return err
	}
	backup := path + ".backup-" + newID()
	if err = os.Rename(path, backup); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		if restore := os.Rename(backup, path); restore != nil {
			return fmt.Errorf("publish failed: %v; old labels retained at %s (restore: %v)", err, backup, restore)
		}
		return err
	}
	_ = os.Remove(backup)
	return nil
}
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one JSON value in %s", path)
	}
	return nil
}
