package reporter

import (
	"fmt"
	"io"
	"os"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// ANSI colour codes.
const (
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
	colorYellow = "\033[33m"
	colorBold   = "\033[1m"
)

// TerminalReporter prints coloured step-by-step output to a terminal.
type TerminalReporter struct {
	Verbose bool
	// NoMask disables E.164 masking for this reporter (see
	// NewPassthroughMasker). False — masked — by default.
	NoMask bool
	colour bool
}

// NewTerminalReporter creates a terminal reporter. Colours are enabled
// only when w is a TTY (i.e. os.Stdout connected to a terminal).
func NewTerminalReporter(w io.Writer, verbose bool) *TerminalReporter {
	return &TerminalReporter{
		Verbose: verbose,
		colour:  isTTY(w),
	}
}

// Report writes coloured results for each scenario.
func (t *TerminalReporter) Report(w io.Writer, results []*engine.RunResult) error {
	masker := maskerFor(t.NoMask, results)
	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		if err := t.reportOne(w, r, masker); err != nil {
			return err
		}
	}
	return nil
}

func (t *TerminalReporter) reportOne(w io.Writer, r *engine.RunResult, masker *Masker) error {
	fmt.Fprintf(w, "%sScenario: %s%s\n", t.bold(), r.ScenarioName, t.reset())

	passed := t.printSteps(w, r, masker)
	t.printWarnings(w, r, masker)

	total := len(r.Steps)
	colour := t.green()
	if passed < total {
		colour = t.red()
	}
	fmt.Fprintf(w, "%s%d/%d steps passed%s in %dms\n", colour, passed, total, t.reset(), r.DurationMs)
	return nil
}

// printSteps prints one line (plus any error detail or verbose raw-response
// dump) per step and returns how many passed. Extracted from reportOne per
// CLAUDE.md's 40-line cap.
func (t *TerminalReporter) printSteps(w io.Writer, r *engine.RunResult, masker *Masker) int {
	passed := 0
	for _, step := range r.Steps {
		if step.Result.Status == common.StatusPassed {
			passed++
			fmt.Fprintf(w, "  %s\u2713%s [%s] %s %s",
				t.green(), t.reset(), step.Protocol, step.Action, step.StepID)
		} else {
			fmt.Fprintf(w, "  %s\u2717%s [%s] %s %s",
				t.red(), t.reset(), step.Protocol, step.Action, step.StepID)
		}
		// Always shown, even at 0ms: a sub-millisecond step against an
		// in-process peer is real, and a line with no duration next to lines
		// that have one reads as a rendering bug rather than a fast step.
		fmt.Fprintf(w, " (%dms)", step.Result.DurationMs)
		if ms, ok := impairDelayMs(step.Result); ok {
			fmt.Fprintf(w, " %s(impaired +%dms)%s", t.yellow(), ms, t.reset())
		}
		fmt.Fprintln(w)

		// One Error line, not two. An assertion failure sets BOTH
		// Result.Error and the report's Error to the same text (ExecuteStep
		// returns the mutated result alongside the error), so two independent
		// ifs printed every assertion failure twice — the golden file shows
		// it once. The second branch now covers only an error the result
		// itself does not already carry.
		if step.Result.Status == common.StatusFailed && step.Result.Error != "" {
			fmt.Fprintf(w, "    %sError: %s%s\n", t.red(), masker.Mask(step.Result.Error), t.reset())
		} else if step.Error != nil {
			fmt.Fprintf(w, "    %sError: %s%s\n", t.red(), masker.Mask(step.Error.Error()), t.reset())
		}

		if t.Verbose && len(step.Result.RawResponse) > 0 {
			fmt.Fprintf(w, "    %s--- raw response ---%s\n", t.yellow(), t.reset())
			fmt.Fprintf(w, "    %s\n", masker.Mask(string(step.Result.RawResponse)))
		}
	}
	return passed
}

// printWarnings prints one yellow WARN line per run-level warning, extracted
// from reportOne to keep it under CLAUDE.md's 40-line cap.
func (t *TerminalReporter) printWarnings(w io.Writer, r *engine.RunResult, masker *Masker) {
	for _, warning := range r.Warnings {
		fmt.Fprintf(w, "  %sWARN: %s%s\n", t.yellow(), masker.Mask(warning), t.reset())
	}
}

func (t *TerminalReporter) green() string {
	if !t.colour {
		return ""
	}
	return colorGreen
}

func (t *TerminalReporter) red() string {
	if !t.colour {
		return ""
	}
	return colorRed
}

func (t *TerminalReporter) yellow() string {
	if !t.colour {
		return ""
	}
	return colorYellow
}

func (t *TerminalReporter) bold() string {
	if !t.colour {
		return ""
	}
	return colorBold
}

func (t *TerminalReporter) reset() string {
	if !t.colour {
		return ""
	}
	return colorReset
}

// isTTY checks if the writer is connected to a terminal.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
