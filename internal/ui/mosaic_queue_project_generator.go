package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/widget"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

type generatedProjectGroup struct {
	Filter string
	Files  []string
}

func generatedProjectGroups(filesByFilter map[string][]mosaic.FilterFile, productType string) []generatedProjectGroup {
	filters := make([]string, 0, len(filesByFilter))
	for filter := range filesByFilter {
		filters = append(filters, filter)
	}
	sort.Strings(filters)
	groups := make([]generatedProjectGroup, 0, len(filters))
	for _, filter := range filters {
		paths := make([]string, 0, len(filesByFilter[filter]))
		for _, file := range filesByFilter[filter] {
			if productType != "" && mosaic.ProductType(file.Path) != productType {
				continue
			}
			absPath, err := filepath.Abs(file.Path)
			if err == nil {
				paths = append(paths, absPath)
			}
		}
		if len(paths) == 0 {
			continue
		}
		sort.Strings(paths)
		groups = append(groups, generatedProjectGroup{Filter: filter, Files: paths})
	}
	return groups
}

func generatedProjectPath(dir, filter string) (string, error) {
	safeFilter := sanitizeDrizzleFilter(filter)
	if safeFilter == "" || strings.Trim(safeFilter, "_ .") == "" {
		return "", fmt.Errorf("filter %q is invalid for a project filename", filter)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(absDir, safeFilter+"_project.json"), nil
}

// buildGeneratedMosaicProject copies processing settings from the template and
// replaces its input set with one filter group. Alignment/offset state from the
// template is deliberately not copied because it belongs to different frames.
func buildGeneratedMosaicProject(template models.MosaicProject, templatePath, outputPath, filter string, paths []string) models.MosaicProject {
	project := models.MosaicProject{
		DrizzleSettings:      template.DrizzleSettings,
		DrizzleSettingsSet:   true,
		AlignmentSettings:    template.AlignmentSettings,
		AlignmentSettingsSet: template.AlignmentSettingsSet,
		SkysubSettings:       template.SkysubSettings,
		SkysubSettingsSet:    true,
		ActiveFilter:         filter,
		ExposureNormMode:     template.ExposureNormMode,
		ReferenceSCIExt:      template.ReferenceSCIExt,
	}
	if template.ReferencePath != "" {
		project.ReferencePath = resolveProjectRelativePath(templatePath, template.ReferencePath)
		if abs, err := filepath.Abs(project.ReferencePath); err == nil {
			project.ReferencePath = abs
		}
	}
	for _, path := range paths {
		project.Inputs = append(project.Inputs, models.MosaicInputState{Path: path})
	}
	resolvedSky := resolveSkysubSettingsForProject(template.SkysubSettings, templatePath)
	project.SkysubSettings.RowDestripeMaskDir = encodeProjectRelativePath(outputPath, resolvedSky.RowDestripeMaskDir)
	project.SkysubSettings.MIRIArtifactMaskDir = encodeProjectRelativePath(outputPath, resolvedSky.MIRIArtifactMaskDir)
	return project
}

func writeGeneratedMosaicProject(path string, project models.MosaicProject) error {
	data, err := json.MarshalIndent(project, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	return f.Close()
}

func (q *drizzleQueueWindow) addGeneratedProject(path string, project models.MosaicProject, runAlign bool) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, existing := range q.jobs {
		if existing.ProjectPath == absPath {
			return false
		}
	}
	q.jobs = append(q.jobs, drizzleQueueJob{
		ProjectPath: absPath,
		Project:     project,
		Filter:      strings.TrimSpace(project.ActiveFilter),
		RunAlign:    runAlign,
		Status:      queuePending,
	})
	if q.selected < 0 {
		q.selected = 0
	}
	return true
}

func (q *drizzleQueueWindow) openProjectGenerator() {
	if q.running {
		return
	}
	fd := dialog.NewFileOpen(func(reader fyne.URIReadCloser, err error) {
		if err != nil || reader == nil {
			return
		}
		templatePath := reader.URI().Path()
		_ = reader.Close()
		template, absTemplatePath, readErr := readMosaicProject(templatePath)
		if readErr != nil {
			dialog.ShowError(readErr, q.win)
			return
		}
		q.selectProjectSourceDirectory(template, absTemplatePath)
	}, q.win)
	fd.SetFilter(storage.NewExtensionFileFilter([]string{".json"}))
	q.ws.configureLastDir(fd)
	sizeFileDialog(fd)
	fd.Show()
}

func (q *drizzleQueueWindow) selectProjectSourceDirectory(template models.MosaicProject, templatePath string) {
	fd := dialog.NewFolderOpen(func(listable fyne.ListableURI, err error) {
		if err != nil || listable == nil {
			return
		}
		dir := listable.Path()
		q.ws.app.Preferences().SetString("lastDir", dir)
		progress := dialog.NewCustomWithoutButtons("Scanning FITS Files", container.NewVBox(widget.NewLabel("Reading FITS headers...")), q.win)
		progress.Show()
		go func() {
			filesByFilter, scanErr := mosaic.DiscoverFilterFiles(dir)
			fyne.Do(func() {
				progress.Hide()
				if scanErr != nil {
					dialog.ShowError(scanErr, q.win)
					return
				}
				q.showProjectGeneratorDialog(template, templatePath, dir, filesByFilter)
			})
		}()
	}, q.win)
	q.ws.configureLastDir(fd)
	sizeFileDialog(fd)
	fd.Show()
}

func (q *drizzleQueueWindow) showProjectGeneratorDialog(template models.MosaicProject, templatePath, dir string, filesByFilter map[string][]mosaic.FilterFile) {
	hasFLC, hasFLT, hasCal := mosaic.AvailableProductTypes(filesByFilter)
	typeOptions := []string{"All"}
	if hasFLC {
		typeOptions = append(typeOptions, ".flc")
	}
	if hasFLT {
		typeOptions = append(typeOptions, ".flt")
	}
	if hasCal {
		typeOptions = append(typeOptions, ".cal")
	}
	productSelect := NewSafeSelect(typeOptions, nil)
	if len(typeOptions) > 1 {
		productSelect.SetSelected(typeOptions[1])
	} else {
		productSelect.SetSelected(typeOptions[0])
	}
	if len(typeOptions) == 2 {
		productSelect.Disable()
	}
	alignCheck := widget.NewCheck("Enable alignment for all generated jobs", nil)
	rows := container.NewVBox()
	selected := map[string]bool{}
	for filter := range filesByFilter {
		selected[filter] = true
	}
	var groups []generatedProjectGroup
	refreshRows := func() {
		product := ""
		if productSelect.Selected != "All" {
			product = strings.TrimPrefix(productSelect.Selected, ".")
		}
		groups = generatedProjectGroups(filesByFilter, product)
		rows.Objects = nil
		for _, group := range groups {
			group := group
			check := widget.NewCheck(fmt.Sprintf("%s  (%d files)", group.Filter, len(group.Files)), func(value bool) {
				selected[group.Filter] = value
			})
			check.SetChecked(selected[group.Filter])
			rows.Add(check)
		}
		if len(groups) == 0 {
			rows.Add(widget.NewLabel("No files match the selected product type."))
		}
		rows.Refresh()
	}
	productSelect.OnChanged = func(string) { refreshRows() }
	refreshRows()

	content := container.NewVBox(
		widget.NewLabel(fmt.Sprintf("Template: %s", filepath.Base(templatePath))),
		widget.NewLabel(fmt.Sprintf("Projects will be saved in: %s", dir)),
		widget.NewForm(widget.NewFormItem("Product type", productSelect)),
		alignCheck,
		widget.NewButton("Check All", func() {
			for _, group := range groups {
				selected[group.Filter] = true
			}
			refreshRows()
		}),
		widget.NewButton("Uncheck All", func() {
			for _, group := range groups {
				selected[group.Filter] = false
			}
			refreshRows()
		}),
		container.NewVScroll(rows),
	)
	var projectDialog dialog.Dialog
	projectDialog = dialog.NewCustomConfirm("Create Filter Projects", "Create Projects & Add to Queue", "Cancel", content, func(ok bool) {
		if !ok {
			return
		}
		product := ""
		if productSelect.Selected != "All" {
			product = strings.TrimPrefix(productSelect.Selected, ".")
		}
		groups = generatedProjectGroups(filesByFilter, product)
		created := 0
		var errorsFound []string
		for _, group := range groups {
			if !selected[group.Filter] {
				continue
			}
			path, pathErr := generatedProjectPath(dir, group.Filter)
			if pathErr != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("%s: %v", group.Filter, pathErr))
				continue
			}
			project := buildGeneratedMosaicProject(template, templatePath, path, group.Filter, group.Files)
			if writeErr := writeGeneratedMosaicProject(path, project); writeErr != nil {
				errorsFound = append(errorsFound, fmt.Sprintf("%s: %v", group.Filter, writeErr))
				continue
			}
			if q.addGeneratedProject(path, project, alignCheck.Checked) {
				created++
			}
		}
		projectDialog.Hide()
		q.refresh()
		message := fmt.Sprintf("Created and queued %d project(s).", created)
		if len(errorsFound) > 0 {
			message += "\n\n" + strings.Join(errorsFound, "\n")
		}
		if created == 0 || len(errorsFound) > 0 {
			dialog.ShowInformation("Filter Projects", message, q.win)
		}
	}, q.win)
	projectDialog.Resize(fyne.NewSize(620, 560))
	projectDialog.Show()
}
