package runsummary

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/vercel/turbo/cli/internal/chrometracing"
	"github.com/vercel/turbo/cli/internal/fs"
	"github.com/vercel/turbo/cli/internal/ui"
	"github.com/vercel/turbo/cli/internal/util"

	"github.com/fatih/color"
	"github.com/mitchellh/cli"
)

// ExecutionSummary is the state of the entire `turbo run`. Individual task state in `Tasks` field
// TODO(mehulkar): Can this be combined with the RunSummary?
type ExecutionSummary struct {
	Mu      sync.Mutex
	Tasks   map[string]*TaskExecutionSummary
	Success int
	Failure int
	// Is the output streaming?
	Cached    int
	Attempted int

	startedAt time.Time

	profileFilename string
}

// TaskExecutionSummary contains data about the state of a single task in a turbo run.
// Some fields are updated over time as the task prepares to execute and finishes execution.
type TaskExecutionSummary struct {
	TaskID      string        `json:"-"`
	Start       time.Time     `json:"start"`
	Duration    time.Duration `json:"duration"`
	Status      string        `json:"status"` // Its current status
	Err         error         `json:"error"`  // Error, only populated for failure statuses
	ExitCode    int           `json:"exitCode"`
	execSummary *ExecutionSummary
	tracer      *chrometracing.PendingEvent
}

// NewExecutionSummary creates a ExecutionSummary instance to track events in a `turbo run`.`
func NewExecutionSummary(start time.Time, tracingProfile string) *ExecutionSummary {
	if tracingProfile != "" {
		chrometracing.EnableTracing()
	}

	return &ExecutionSummary{
		Success:         0,
		Failure:         0,
		Cached:          0,
		Attempted:       0,
		Tasks:           make(map[string]*TaskExecutionSummary),
		startedAt:       start,
		profileFilename: tracingProfile,
	}
}

// ExecutionEventName represents the status of a target when we log a build result.
type ExecutionEventName int

// The collection of expected build result statuses.
const (
	TargetBuilding ExecutionEventName = iota
	TargetBuildStopped
	TargetBuilt
	TargetCached
	TargetBuildFailed
)

func (een ExecutionEventName) toString() string {
	switch een {
	case TargetBuilding:
		return "building"
	case TargetBuildStopped:
		return "buildStopped"
	case TargetBuilt:
		return "built"
	case TargetCached:
		return "cached"
	case TargetBuildFailed:
		return "buildFailed"
	}

	return ""
}

// ExecutionEvent represents a single event in the build process, i.e. a starting or finishing
// building, or reaching some milestone within those steps.
type ExecutionEvent struct {
	Time     time.Time          // Timestamp of the event
	Duration time.Duration      // Duration of the event
	Name     ExecutionEventName // The name of the event
	Err      error              // Error, only populated for failure statuses
}

// Run starts the Execution of a single task. It returns a function that can
// be used to add ExecutionEvents to the TaskExecutionSummary for the given taskID.
func (es *ExecutionSummary) Run(taskID string) *TaskExecutionSummary {
	startAt := time.Now()

	taskExecSummary := &TaskExecutionSummary{
		TaskID:      taskID,
		Start:       startAt,
		execSummary: es,
	}

	es.Tasks[taskID] = taskExecSummary

	taskExecSummary.Add(TargetBuilding, nil, nil)
	taskExecSummary.tracer = chrometracing.Event(taskID) // TOOD: defer .tracer.Done(0)
	return taskExecSummary
}

func (t *TaskExecutionSummary) start(start time.Time) {

}

func (t *TaskExecutionSummary) Add(name ExecutionEventName, err error, exitCode *int) {
	es := t.execSummary
	es.Mu.Lock()
	defer es.Mu.Unlock()

	// Update some fields on the TaskExecutionSummary
	t.Status = name.toString()
	t.Duration = time.Now().Sub(t.Start)
	t.Err = err

	if exitCode != nil {
		t.ExitCode = *exitCode
	}

	// Update some bubbled up counts on the ExecutionSummary
	switch {
	case name == TargetBuildFailed:
		es.Failure++
		es.Attempted++
	case name == TargetCached:
		es.Cached++
		es.Attempted++
	case name == TargetBuilt:
		t.ExitCode = 0 // Task was successful if TargetBuilt
		es.Success++
		es.Attempted++
	}
}

// Close finishes a trace of a turbo run. The tracing file will be written if applicable,
// and run stats are written to the terminal
func (es *ExecutionSummary) Close(terminal cli.Ui) error {
	if err := writeChrometracing(es.profileFilename, terminal); err != nil {
		terminal.Error(fmt.Sprintf("Error writing tracing data: %v", err))
	}

	maybeFullTurbo := ""
	if es.Cached == es.Attempted && es.Attempted > 0 {
		terminalProgram := os.Getenv("TERM_PROGRAM")
		// On the macOS Terminal, the rainbow colors show up as a magenta background
		// with a gray background on a single letter. Instead, we print in bold magenta
		if terminalProgram == "Apple_Terminal" {
			fallbackTurboColor := color.New(color.FgHiMagenta, color.Bold).SprintFunc()
			maybeFullTurbo = fallbackTurboColor(">>> FULL TURBO")
		} else {
			maybeFullTurbo = ui.Rainbow(">>> FULL TURBO")
		}
	}

	if es.Attempted == 0 {
		terminal.Output("") // Clear the line
		terminal.Warn("No tasks were executed as part of this run.")
	}

	terminal.Output("") // Clear the line
	terminal.Output(util.Sprintf("${BOLD} Tasks:${BOLD_GREEN}    %v successful${RESET}${GRAY}, %v total${RESET}", es.Cached+es.Success, es.Attempted))
	terminal.Output(util.Sprintf("${BOLD}Cached:    %v cached${RESET}${GRAY}, %v total${RESET}", es.Cached, es.Attempted))
	terminal.Output(util.Sprintf("${BOLD}  Time:    %v${RESET} %v${RESET}", time.Since(es.startedAt).Truncate(time.Millisecond), maybeFullTurbo))
	terminal.Output("")
	return nil
}

// writeChromeTracing writes to a profile name if the `--profile` flag was passed to turbo run
func writeChrometracing(filename string, terminal cli.Ui) error {
	outputPath := chrometracing.Path()
	if outputPath == "" {
		// tracing wasn't enabled
		return nil
	}

	name := fmt.Sprintf("turbo-%s.trace", time.Now().Format(time.RFC3339))
	if filename != "" {
		name = filename
	}
	if err := chrometracing.Close(); err != nil {
		terminal.Warn(fmt.Sprintf("Failed to flush tracing data: %v", err))
	}
	cwdRaw, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := fs.GetCwd(cwdRaw)
	if err != nil {
		return err
	}
	// chrometracing.Path() is absolute by default, but can still be relative if overriden via $CHROMETRACING_DIR
	// so we have to account for that before converting to turbopath.AbsoluteSystemPath
	if err := fs.CopyFile(&fs.LstatCachedFile{Path: fs.ResolveUnknownPath(root, outputPath)}, name); err != nil {
		return err
	}
	return nil
}
