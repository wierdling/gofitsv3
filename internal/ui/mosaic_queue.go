package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"gofitsv3/internal/models"
	"gofitsv3/internal/mosaic"
)

type drizzleQueueStatus int

const (
	queuePending drizzleQueueStatus = iota
	queueLoading
	queueAligning
	queueDrizzling
	queueSaving
	queueSucceeded
	queueFailed
	queueCancelled
)

func (s drizzleQueueStatus) String() string {
	switch s {
	case queuePending:
		return "Pending"
	case queueLoading:
		return "Loading"
	case queueAligning:
		return "Aligning"
	case queueDrizzling:
		return "Drizzling"
	case queueSaving:
		return "Saving"
	case queueSucceeded:
		return "Succeeded"
	case queueFailed:
		return "Failed"
	case queueCancelled:
		return "Cancelled"
	default:
		return "Unknown"
	}
}

type drizzleQueueJob struct {
	ProjectPath string
	Project     models.MosaicProject
	Filter      string
	RunAlign    bool
	Status      drizzleQueueStatus
	OutputPath  string
	Err         string
}

type drizzleQueueExecutor interface {
	Execute(ctx context.Context, job drizzleQueueJob, progress func(string, int, int)) (string, error)
}

type drizzleQueueEvent struct {
	Index       int
	Status      drizzleQueueStatus
	Stage       string
	Done, Total int
	OutputPath  string
	Err         string
}

type drizzleQueueRunner struct {
	executor drizzleQueueExecutor
	onEvent  func(drizzleQueueEvent)
	onDone   func()

	mu          sync.Mutex
	currentStop context.CancelFunc
	stopAfter   bool
}

func (r *drizzleQueueRunner) CancelCurrent() {
	r.mu.Lock()
	cancel := r.currentStop
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (r *drizzleQueueRunner) StopAfterCurrent() {
	r.mu.Lock()
	r.stopAfter = true
	r.mu.Unlock()
}

func (r *drizzleQueueRunner) shouldStop() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopAfter
}

func (r *drizzleQueueRunner) clearCurrent() {
	r.mu.Lock()
	r.currentStop = nil
	r.mu.Unlock()
}

func (r *drizzleQueueRunner) Run(ctx context.Context, jobs []drizzleQueueJob) {
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		if r.onDone != nil {
			r.onDone()
		}
	}()
	// Own the work slice so progress callbacks never race the queue window's
	// editable jobs. Results are published only as immutable events.
	work := append([]drizzleQueueJob(nil), jobs...)
	for i := range work {
		if work[i].Status != queuePending {
			continue
		}
		if r.shouldStop() || (ctx != nil && ctx.Err() != nil) {
			return
		}
		job := &work[i]
		job.Status = queueLoading
		r.emit(i, queueLoading, "Loading project", 0, 0)
		jobCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		r.currentStop = cancel
		r.mu.Unlock()
		output, err := r.executor.Execute(jobCtx, *job, func(stage string, done, total int) {
			status := queueDrizzling
			if strings.HasPrefix(stage, "Loading") {
				status = queueLoading
			} else if strings.HasPrefix(stage, "Aligning") {
				status = queueAligning
			} else if strings.HasPrefix(stage, "Saving") {
				status = queueSaving
			}
			job.Status = status
			r.emitResult(i, status, stage, done, total, "", "")
		})
		wasCancelled := errors.Is(err, mosaic.ErrCancelled) || jobCtx.Err() != nil
		cancel()
		r.clearCurrent()
		if wasCancelled {
			job.Status = queueCancelled
			job.Err = "cancelled"
			r.emitResult(i, queueCancelled, "Cancelled", 0, 0, "", job.Err)
			if i < len(jobs) {
				jobs[i] = *job
			}
			continue
		}
		if err != nil {
			job.Status = queueFailed
			job.Err = err.Error()
			r.emitResult(i, queueFailed, err.Error(), 0, 0, "", job.Err)
			if i < len(jobs) {
				jobs[i] = *job
			}
			continue
		}
		job.Status = queueSucceeded
		job.OutputPath = output
		job.Err = ""
		r.emitResult(i, queueSucceeded, "Saved", 1, 1, job.OutputPath, "")
		// Preserve the historical direct-runner API for non-UI callers. The UI
		// passes its own copy, so its editable jobs remain main-thread owned.
		if i < len(jobs) {
			jobs[i] = *job
		}
	}
}

func (r *drizzleQueueRunner) emit(index int, status drizzleQueueStatus, stage string, done, total int) {
	r.emitResult(index, status, stage, done, total, "", "")
}

func (r *drizzleQueueRunner) emitResult(index int, status drizzleQueueStatus, stage string, done, total int, outputPath, errText string) {
	if r.onEvent != nil {
		r.onEvent(drizzleQueueEvent{Index: index, Status: status, Stage: stage, Done: done, Total: total, OutputPath: outputPath, Err: errText})
	}
}

type defaultDrizzleQueueExecutor struct{}

func (defaultDrizzleQueueExecutor) Execute(ctx context.Context, job drizzleQueueJob, progress func(string, int, int)) (string, error) {
	loaded, err := loadMosaicProjectData(ctx, job.ProjectPath, progress)
	if err != nil {
		return "", err
	}
	defer freeInputPixelsFor(loaded.Inputs, loaded.ReferenceInput)
	filter := strings.TrimSpace(job.Filter)
	if filter == "" {
		filter, err = projectFilter(loaded.Project, loaded.Inputs)
		if err != nil {
			return "", err
		}
	}
	if job.RunAlign {
		if progress != nil {
			progress("Aligning", 0, len(loaded.Inputs))
		}
		if err := alignLoadedProject(ctx, loaded.Inputs, loaded.ReferenceInput, loaded.Project.AlignmentSettings, progress); err != nil {
			return "", err
		}
	}

	if ctx != nil && ctx.Err() != nil {
		return "", mosaic.ErrCancelled
	}
	buildInputs := standaloneInputsWithReference(loaded.Inputs, loaded.ReferenceInput)
	sky := resolveSkysubSettingsForProject(loaded.Project.SkysubSettings, loaded.ProjectPath)
	options := drizzleOptionsFromSettings(loaded.Project.DrizzleSettings, sky, "", progress, ctx)
	options.DebugOutputDir = ""
	if progress != nil {
		progress("Drizzling", 0, 0)
	}
	result, err := mosaic.Build(buildInputs, options)
	if err != nil {
		return "", err
	}
	if result == nil {
		return "", fmt.Errorf("drizzle returned no result")
	}
	outputPath, err := drizzleQueueOutputPath(loaded.ProjectPath, filter, time.Now())
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(outputPath); err == nil {
		return "", fmt.Errorf("output already exists: %s", outputPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("check output %s: %w", outputPath, err)
	}
	if progress != nil {
		progress("Saving", 0, 1)
	}
	saveErr := mosaic.SaveResultFITS(outputPath, result)
	result = nil
	if saveErr != nil {
		if _, statErr := os.Stat(outputPath); statErr == nil {
			_ = os.Remove(outputPath)
		}
		return "", saveErr
	}
	return outputPath, nil
}

func standaloneInputsWithReference(inputs []mosaic.Input, reference *mosaic.Input) []mosaic.Input {
	active := make([]mosaic.Input, 0, len(inputs)+1)
	for _, input := range inputs {
		if !input.Excluded {
			active = append(active, input)
		}
	}
	if reference == nil {
		return active
	}
	ref := *reference
	ref.ReferenceOnly = true
	return append([]mosaic.Input{ref}, active...)
}

func alignLoadedProject(ctx context.Context, inputs []mosaic.Input, reference *mosaic.Input, settings models.AlignmentSettings, progress func(string, int, int)) error {
	activeIndices := make([]int, 0, len(inputs))
	active := make([]mosaic.Input, 0, len(inputs))
	for i := range inputs {
		if !inputs[i].Excluded {
			activeIndices = append(activeIndices, i)
			active = append(active, inputs[i])
		}
	}
	if len(active) < 2 && reference == nil {
		return nil
	}
	alignInputs := active
	if reference != nil {
		ref := *reference
		ref.ReferenceOnly = true
		alignInputs = append([]mosaic.Input{ref}, active...)
	}
	mode := mosaic.AlignmentMode(settings.AlignmentMode)
	if !mosaic.AlignmentStreamsPixels(mode) {
		if err := ensureInputPixelsLoadedFor(alignInputs, nil); err != nil {
			return err
		}
	}
	numRefs := settings.NumRefs
	if numRefs < 1 {
		numRefs = 1
	}
	if reference != nil {
		numRefs = 1
	}
	results, err := mosaic.AlignInputsByStarsWithMode(alignInputs, numRefs, mode, settings.SearchRadiusArcsec, mosaic.AlignProgress{
		Ctx: ctx,
		Progress: func(done, total int) {
			if progress != nil {
				progress("Aligning", done, total)
			}
		},
	})
	if err != nil {
		return err
	}
	resultOffset := 0
	if reference != nil {
		resultOffset = 1
	}
	applyLoadedProjectAlignmentResults(inputs, activeIndices, results, resultOffset)
	return nil
}

func applyLoadedProjectAlignmentResults(inputs []mosaic.Input, activeIndices []int, results []mosaic.StarAlignmentResult, resultOffset int) {
	for resultIndex := resultOffset; resultIndex < len(results); resultIndex++ {
		activeIndex := resultIndex - resultOffset
		if activeIndex >= len(activeIndices) {
			break
		}
		dstIndex := activeIndices[activeIndex]
		if inputs[dstIndex].OffsetLocked {
			continue
		}
		result := results[resultIndex]
		if !result.Applied {
			continue
		}
		inputs[dstIndex].OffsetX = result.OffsetX
		inputs[dstIndex].OffsetY = result.OffsetY
		inputs[dstIndex].ManualTransform = result.ManualTransform
		inputs[dstIndex].HasManualTransform = result.HasManualTransform
	}
}

func drizzleQueueOutputPath(projectPath, filter string, when time.Time) (string, error) {
	if strings.TrimSpace(projectPath) == "" {
		return "", fmt.Errorf("project path is empty")
	}
	filter = sanitizeDrizzleFilter(filter)
	if filter == "" || strings.Trim(filter, "_ .") == "" {
		return "", fmt.Errorf("filter is empty or invalid")
	}
	name := fmt.Sprintf("%s_%s_drizzle.fits", filter, when.Local().Format("20060102_150405"))
	return filepath.Join(filepath.Dir(projectPath), name), nil
}

func sanitizeDrizzleFilter(filter string) string {
	filter = strings.TrimSpace(filter)
	filter = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune("<>:\"/\\|?*", r) {
			return '_'
		}
		return r
	}, filter)
	return strings.TrimRight(filter, ". ")
}
