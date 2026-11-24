package reporter

import (
	"encoding/json"
	"io"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// jsonReport is the top-level JSON output structure.
type jsonReport struct {
	Scenario   string     `json:"scenario"`
	Status     string     `json:"status"`
	DurationMs int64      `json:"duration_ms"`
	Warnings   []string   `json:"warnings,omitempty"`
	Steps      []jsonStep `json:"steps"`
}

// jsonStep represents one step in the JSON output.
type jsonStep struct {
	ID         string `json:"id"`
	Protocol   string `json:"protocol"`
	Status     string `json:"status"`
	DurationMs int64  `json:"duration_ms"`
	// ImpairDelayMs is how much of DurationMs was deliberately injected by
	// this step's own impair: block, omitted entirely when the step declared
	// none. A consumer subtracts it from DurationMs to recover the peer's
	// real contribution.
	ImpairDelayMs *int64  `json:"impair_delay_ms,omitempty"`
	Error         *string `json:"error"` // null for passed steps, not omitted.
}

// JSONReporter produces structured JSON output.
type JSONReporter struct {
	// NoMask disables E.164 masking (see NewPassthroughMasker).
	NoMask bool
}

// Report writes JSON for all scenario results.
func (j *JSONReporter) Report(w io.Writer, results []*engine.RunResult) error {
	reports := make([]jsonReport, 0, len(results))
	masker := maskerFor(j.NoMask, results)

	for _, r := range results {
		reports = append(reports, buildJSONReport(r, masker))
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// If single result, output a single object; otherwise an array.
	if len(reports) == 1 {
		return enc.Encode(reports[0])
	}
	return enc.Encode(reports)
}

// buildJSONReport converts one RunResult into its JSON shape, masking every
// string it emits (step errors and run-level warnings alike) through the
// report's single masker. Extracted from Report per CLAUDE.md's 40-line cap.
func buildJSONReport(r *engine.RunResult, masker *Masker) jsonReport {
	jr := jsonReport{
		Scenario:   r.ScenarioName,
		Status:     string(r.Status),
		DurationMs: r.DurationMs,
		Steps:      make([]jsonStep, 0, len(r.Steps)),
	}

	for _, warning := range r.Warnings {
		jr.Warnings = append(jr.Warnings, masker.Mask(warning))
	}

	for _, step := range r.Steps {
		jr.Steps = append(jr.Steps, buildJSONStep(step, masker))
	}

	return jr
}

// buildJSONStep converts one StepReport into its JSON shape.
func buildJSONStep(step engine.StepReport, masker *Masker) jsonStep {
	js := jsonStep{
		ID:         step.StepID,
		Protocol:   step.Protocol,
		Status:     string(step.Result.Status),
		DurationMs: step.Result.DurationMs,
	}

	if ms, ok := impairDelayMs(step.Result); ok {
		js.ImpairDelayMs = &ms
	}

	if step.Result.Status == common.StatusFailed {
		// Result.Error wins; StepReport.Error is only the fallback. An
		// assertion failure sets both to the same text, so the order looks
		// free, but a send that failed BEFORE any assertion ran leaves the
		// specific cause in Result.Error (e.g. `send failed: ... x509:
		// certificate is valid for 127.0.0.1, not 127.0.0.2`) while
		// StepReport.Error carries only the generic assertion failure that
		// followed it. Overwriting deleted the only text naming the real
		// cause, so a harness reading this report could not tell a rejected
		// certificate from a silent peer. terminal.go and junit.go use the
		// same rule.
		errMsg := masker.Mask(step.Result.Error)
		if step.Result.Error == "" && step.Error != nil {
			errMsg = masker.Mask(step.Error.Error())
		}
		js.Error = &errMsg
	}

	return js
}
