package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

// loadedMosaicProject is the non-visual form of a saved project. Inputs are
// metadata-only until an alignment mode requires resident pixels; Build loads
// frame pixels on demand.
type loadedMosaicProject struct {
	Project        models.MosaicProject
	ProjectPath    string
	Inputs         []mosaic.Input
	Statuses       []mosaic.InputStatus
	ReferenceInput *mosaic.Input
	MigratedCount  int
}

func readMosaicProject(path string) (models.MosaicProject, string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return models.MosaicProject{}, "", err
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return models.MosaicProject{}, absPath, err
	}
	var project models.MosaicProject
	if err := json.Unmarshal(data, &project); err != nil {
		return models.MosaicProject{}, absPath, err
	}
	return project, absPath, nil
}

// loadMosaicProjectData loads one project without touching Fyne or workspace
// state. Unlike the interactive loader, queue execution treats any required
// input/reference error as fatal so it cannot silently drizzle a partial set.
func loadMosaicProjectData(ctx context.Context, projectPath string, progress func(string, int, int)) (*loadedMosaicProject, error) {
	project, absPath, err := readMosaicProject(projectPath)
	if err != nil {
		return nil, err
	}
	if len(project.Inputs) == 0 {
		return nil, fmt.Errorf("project %s contains no inputs", filepath.Base(absPath))
	}
	if progress == nil {
		progress = func(string, int, int) {}
	}

	loaded := &loadedMosaicProject{Project: project, ProjectPath: absPath}
	var order []string
	groups := map[string][]models.MosaicInputState{}
	for _, state := range project.Inputs {
		path := resolveProjectRelativePath(absPath, state.Path)
		if _, ok := groups[path]; !ok {
			order = append(order, path)
		}
		groups[path] = append(groups[path], state)
	}
	for i, path := range order {
		if ctx != nil && ctx.Err() != nil {
			return nil, mosaic.ErrCancelled
		}
		if path == "" {
			return nil, fmt.Errorf("project %s contains an empty input path", filepath.Base(absPath))
		}
		progress(fmt.Sprintf("Loading: %s", filepath.Base(path)), i+1, len(order))
		states := groups[path]
		representative := lowestSCIExtState(states)
		inputs, combined, _, loadErr := mosaic.LoadInputsForPipeline(path, mosaic.CombineOptions{
			Ctx: ctx,
			Progress: func(stage string, done, total int) {
				progress(fmt.Sprintf("%s: %s", filepath.Base(path), stage), done, total)
			},
		})
		if loadErr != nil {
			return nil, fmt.Errorf("load %s: %w", filepath.Base(path), loadErr)
		}
		if len(inputs) == 0 {
			return nil, fmt.Errorf("no inputs loaded from %s", filepath.Base(path))
		}
		if len(inputs) == 1 {
			applyInputState(&inputs[0], representative)
			loaded.Inputs = append(loaded.Inputs, inputs[0])
			loaded.Statuses = append(loaded.Statuses, mosaic.InputStatus{Path: mosaic.InputKey(inputs[0]), Included: !inputs[0].Excluded, Status: "loaded"})
			if combined && len(states) > 1 {
				loaded.MigratedCount++
			}
			continue
		}
		for i := range inputs {
			applyInputState(&inputs[i], stateForSCIExt(states, inputs[i].SCIExt, representative))
			loaded.Inputs = append(loaded.Inputs, inputs[i])
			loaded.Statuses = append(loaded.Statuses, mosaic.InputStatus{Path: mosaic.InputKey(inputs[i]), Included: !inputs[i].Excluded, Status: "loaded"})
		}
	}

	if project.ReferencePath != "" {
		if ctx != nil && ctx.Err() != nil {
			return nil, mosaic.ErrCancelled
		}
		project.ReferencePath = resolveProjectRelativePath(absPath, project.ReferencePath)
		refInputs, combined, _, refErr := mosaic.LoadInputsForPipeline(project.ReferencePath, mosaic.CombineOptions{Ctx: ctx})
		if refErr != nil {
			return nil, fmt.Errorf("load reference %s: %w", filepath.Base(project.ReferencePath), refErr)
		}
		if len(refInputs) == 0 {
			return nil, fmt.Errorf("no inputs loaded from reference %s", filepath.Base(project.ReferencePath))
		}
		chosen := refInputs[0]
		if !combined && project.ReferenceSCIExt != 0 {
			for _, candidate := range refInputs {
				if candidate.SCIExt == project.ReferenceSCIExt {
					chosen = candidate
					break
				}
			}
		}
		chosen.ReferenceOnly = true
		loaded.ReferenceInput = &chosen
	}
	return loaded, nil
}

func projectFilter(project models.MosaicProject, inputs []mosaic.Input) (string, error) {
	if strings.TrimSpace(project.ActiveFilter) != "" {
		return strings.TrimSpace(project.ActiveFilter), nil
	}
	filter := ""
	for _, input := range inputs {
		if input.Excluded {
			continue
		}
		current := mosaic.FilterNameForInput(input)
		if current == "" {
			return "", fmt.Errorf("filter is not recorded in input %s", mosaic.InputLabel(input))
		}
		if filter == "" {
			filter = current
		} else if filter != current {
			return "", fmt.Errorf("project contains multiple filters (%s and %s)", filter, current)
		}
	}
	if filter == "" {
		return "", fmt.Errorf("project has no active inputs from which to determine a filter")
	}
	return filter, nil
}
