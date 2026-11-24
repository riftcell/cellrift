package engine

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
)

// waitAction is the engine-level pause action. Like "parallel" it is not a
// protocol operation: no adapter can dispatch it, so both the main step
// loop and the parallel sub-step loop must intercept it before the adapter
// lookup, or it resolves to the empty protocol and fails with "no adapter
// registered".
const waitAction = "wait"

// reorderAction is the second engine-level container action. Like parallel
// and wait it is intercepted before the adapter lookup, but unlike them it
// does reach an adapter — through the narrow ReorderSender interface rather
// than through ProtocolAdapter.Execute, because a reorder group is one
// batched call, not N independent step dispatches.
const reorderAction = "reorder"

// RunOptions configures scenario execution.
type RunOptions struct {
	FailFast     bool
	Timeout      time.Duration
	VarOverrides []string // "key=value" pairs from --var flags.
	Environ      []string // os.Environ() for CELLRIFT_VAR_ lookups.

	// Warnings are run-level observations gathered before execution starts —
	// currently a Diameter transport substitution, which happens at adapter
	// construction time, before any step runs. They are passed in rather than
	// collected per step because nothing about them is step-scoped.
	Warnings []string
}

// StepReport holds the result of one step within a run.
type StepReport struct {
	StepID   string
	Protocol string
	Action   string
	Result   *common.StepResult
	Error    error
}

// RunResult holds the outcome of running a full scenario.
type RunResult struct {
	ScenarioName string
	Status       common.StepStatus
	Steps        []StepReport
	DurationMs   int64

	// Warnings are non-fatal, run-level observations reporters must surface.
	// Most are gathered before execution starts (RunOptions.Warnings); Run adds
	// the short-credential warning after the steps, once secrets are known.
	// A run can pass every step and still carry one: a Diameter scenario that
	// asked for SCTP and got TCP produced correct results over a transport the
	// author did not choose, and a report that hides that is misleading.
	Warnings []string

	// Secrets are credential-bearing strings this run produced, collected from
	// any adapter implementing common.SecretSource. Reporters register them with
	// their Masker so they cannot reach a report. They are never written to a
	// report themselves, in any field, and must not be formatted.
	Secrets []string
}

// Run executes a scenario: resolves variables, iterates steps in declaration
// order, and collects results.
func Run(s *scenario.Scenario, adapters common.AdapterMap, opts RunOptions) *RunResult {
	start := time.Now()

	result := &RunResult{
		ScenarioName: s.Name,
		Status:       common.StatusPassed,
		Warnings:     opts.Warnings,
	}

	// Build state: scenario variables → env overrides → CLI overrides (highest priority).
	state := NewState()
	state.LoadVariables(s.Variables)
	state.LoadEnvOverrides(opts.Environ)
	state.LoadOverrides(opts.VarOverrides)

	for _, step := range s.Steps {
		// Handle parallel step groups.
		if step.Action == "parallel" {
			reports := executeParallel(step, state, adapters, s.Protocols)
			for _, report := range reports {
				result.Steps = append(result.Steps, report)
				failed := report.Error != nil || (report.Result != nil && report.Result.Status == common.StatusFailed)
				if failed {
					result.Status = common.StatusFailed
				}
			}
			if result.Status == common.StatusFailed && opts.FailFast {
				break
			}
			continue
		}

		if step.Action == reorderAction {
			reports := executeReorder(step, state, adapters, s.Protocols)
			for _, report := range reports {
				result.Steps = append(result.Steps, report)
				failed := report.Error != nil || (report.Result != nil && report.Result.Status == common.StatusFailed)
				if failed {
					result.Status = common.StatusFailed
				}
			}
			if result.Status == common.StatusFailed && opts.FailFast {
				break
			}
			continue
		}

		if step.Action == waitAction {
			result.Steps = append(result.Steps, dispatchWait(step))
			continue
		}

		adapter, ok := adapters[step.Protocol]
		if !ok {
			report := StepReport{
				StepID:   step.ID,
				Protocol: step.Protocol,
				Action:   step.Action,
				Error:    fmt.Errorf("no adapter registered for protocol %q", step.Protocol),
				Result: &common.StepResult{
					Status: common.StatusFailed,
					Error:  fmt.Sprintf("no adapter for protocol %q", step.Protocol),
				},
			}
			result.Steps = append(result.Steps, report)
			result.Status = common.StatusFailed

			if opts.FailFast {
				break
			}
			continue
		}

		// Build protocol config map from scenario protocol settings.
		protocolConfig := buildProtocolConfig(s.Protocols[step.Protocol])

		stepResult, err := ExecuteStep(&step, state, adapter, protocolConfig)

		report := StepReport{
			StepID:   step.ID,
			Protocol: step.Protocol,
			Action:   step.Action,
			Result:   stepResult,
			Error:    err,
		}

		// If ExecuteStep returned an error but no result, create a failed result.
		if stepResult == nil && err != nil {
			report.Result = &common.StepResult{
				Status: common.StatusFailed,
				Error:  err.Error(),
			}
		}

		result.Steps = append(result.Steps, report)

		// Check for failure.
		failed := err != nil || (stepResult != nil && stepResult.Status == common.StatusFailed)
		if failed {
			result.Status = common.StatusFailed
			if opts.FailFast {
				break
			}
		}
	}

	collectSecrets(result, adapters)
	result.DurationMs = time.Since(start).Milliseconds()
	return result
}

// collectSecrets gathers credentials after the steps have run, not before:
// credentials are resolved lazily, when a challenge actually arrives, so a
// scenario that names a credentials_file it never needs does not have to be
// able to read it. One collection covers parallel sub-steps and reorder members
// too, because executeParallel and executeReorder receive this same adapters
// map and never build their own.
func collectSecrets(result *RunResult, adapters common.AdapterMap) {
	for _, ad := range adapters {
		if src, ok := ad.(common.SecretSource); ok {
			result.Secrets = append(result.Secrets, src.Secrets()...)
		}
	}
	if hasShortSecret(result.Secrets) {
		// A fresh slice: Warnings may alias the caller's RunOptions.Warnings.
		result.Warnings = append(append([]string(nil), result.Warnings...), shortSecretWarning)
	}
}

// shortSecretMinLen is the length below which a secret is likely to occur by
// chance in unrelated report text. Ports are at most 5 digits, durations
// typically 3 to 5, and short words such as "lab" or "sip" are common in
// hostnames, so anything under 8 characters can plausibly match them; at 8 and
// above an accidental match is implausible.
const shortSecretMinLen = 8

// shortSecretWarning names neither the secret nor its length. It is added once
// per run, in the same Warnings slice every reporter already surfaces. The
// threshold is interpolated from shortSecretMinLen rather than written out, so
// raising the constant cannot leave the operator reading a number that no
// longer matches what the code does.
var shortSecretWarning = fmt.Sprintf(
	"a configured credential is shorter than %d characters, so this report may contain redactions of unrelated text",
	shortSecretMinLen)

func hasShortSecret(secrets []string) bool {
	for _, s := range secrets {
		if len(s) < shortSecretMinLen {
			return true
		}
	}
	return false
}

// dispatchWait sleeps for the step's declared duration and reports the
// pause as a passed step. Shared by Run and executeParallel so a wait
// nested inside a parallel group behaves identically to a top-level one.
// A wait cannot fail: there is no peer, no I/O, and validation has already
// rejected a non-positive duration.
func dispatchWait(step scenario.Step) StepReport {
	start := time.Now()
	time.Sleep(time.Duration(step.Duration) * time.Millisecond)
	return StepReport{
		StepID: step.ID,
		Action: step.Action,
		Result: &common.StepResult{
			Status:     common.StatusPassed,
			DurationMs: time.Since(start).Milliseconds(),
		},
	}
}

// executeParallel runs all sub-steps of a parallel step concurrently.
// Each sub-step gets its own goroutine. Captured values are merged into
// the shared state after all sub-steps complete.
func executeParallel(step scenario.Step, state *State, adapters common.AdapterMap, protocols map[string]*scenario.ProtocolConfig) []StepReport {
	reports := make([]StepReport, len(step.Steps))
	var wg sync.WaitGroup

	for i, sub := range step.Steps {
		wg.Add(1)
		go func(idx int, s scenario.Step) {
			defer wg.Done()

			if s.Action == waitAction {
				reports[idx] = dispatchWait(s)
				return
			}

			adapter, ok := adapters[s.Protocol]
			if !ok {
				reports[idx] = StepReport{
					StepID:   s.ID,
					Protocol: s.Protocol,
					Action:   s.Action,
					Error:    fmt.Errorf("no adapter registered for protocol %q", s.Protocol),
					Result: &common.StepResult{
						Status: common.StatusFailed,
						Error:  fmt.Sprintf("no adapter for protocol %q", s.Protocol),
					},
				}
				return
			}

			protocolConfig := buildProtocolConfig(protocols[s.Protocol])
			stepResult, err := ExecuteStep(&s, state, adapter, protocolConfig)

			report := StepReport{
				StepID:   s.ID,
				Protocol: s.Protocol,
				Action:   s.Action,
				Result:   stepResult,
				Error:    err,
			}
			if stepResult == nil && err != nil {
				report.Result = &common.StepResult{
					Status: common.StatusFailed,
					Error:  err.Error(),
				}
			}
			reports[idx] = report
		}(i, sub)
	}

	wg.Wait()
	return reports
}

// executeReorder dispatches one reorder group. It resolves every sub-step
// first, hands the whole batch to the adapter in one call, then applies
// each sub-step's own capture:/assert: to its own result.
//
// No goroutine is started here, and none is needed: reordering the write
// order of messages that are all built before any of them is written is a
// buffer-then-flush inside one sequential call path, not a concurrency
// problem. The adapter's existing single-reader design is therefore
// untouched.
func executeReorder(step scenario.Step, state *State, adapters common.AdapterMap, protocols map[string]*scenario.ProtocolConfig) []StepReport {
	// Every sub-step shares one protocol — the validator has already proven
	// this (reorderSubStepErrors), so reading step.Steps[0].Protocol here is
	// a safe read, not a first-wins guess.
	protocol := step.Steps[0].Protocol

	adapter, ok := adapters[protocol]
	if !ok {
		return reorderFailure(step, "no adapter registered for protocol %q", protocol)
	}
	sender, ok := adapter.(common.ReorderSender)
	if !ok {
		return reorderFailure(step, "adapter for protocol %q does not support action: reorder", protocol)
	}

	protocolConfig := buildProtocolConfig(protocols[protocol])
	members, err := resolveReorderMembers(step, state, protocolConfig)
	if err != nil {
		return reorderFailure(step, "%s", err.Error())
	}

	results, err := sender.ExecuteReorder(members, step.TimeoutMs)
	if err != nil {
		return reorderFailure(step, "%s", err.Error())
	}
	if len(results) != len(step.Steps) {
		return reorderFailure(step, "adapter for protocol %q returned %d results for %d sub-steps", protocol, len(results), len(step.Steps))
	}

	return reorderReports(step, state, results)
}

// reorderFailure builds one identical failed report per sub-step, for the
// four whole-group failure modes: unregistered protocol, adapter that does
// not implement ReorderSender, a sub-step that failed to resolve, and an
// ExecuteReorder call that returned an error.
func reorderFailure(step scenario.Step, format string, args ...any) []StepReport {
	msg := fmt.Sprintf(format, args...)
	reports := make([]StepReport, len(step.Steps))
	for i, sub := range step.Steps {
		reports[i] = StepReport{
			StepID:   sub.ID,
			Protocol: sub.Protocol,
			Action:   sub.Action,
			Error:    errors.New(msg),
			Result: &common.StepResult{
				Status: common.StatusFailed,
				Error:  msg,
			},
		}
	}
	return reports
}

// resolveReorderMembers resolves every sub-step's inputs up front, in
// declaration order. Returns an error naming the first sub-step that failed,
// aborting the whole group: none of the group's messages reach the wire if
// any one of them cannot be built, because a partially-sent group produces a
// wire trace no scenario author could reliably reason about.
func resolveReorderMembers(step scenario.Step, state *State, protocolConfig map[string]string) (common.ReorderMembers, error) {
	members := make(common.ReorderMembers, len(step.Steps))
	for i, sub := range step.Steps {
		sub := sub
		in, err := resolveStepInputs(&sub, state, protocolConfig)
		if err != nil {
			return nil, fmt.Errorf("reorder sub-step %s: %w", sub.ID, err)
		}
		members[i] = common.ReorderMember{
			Target:    in.Target,
			Message:   in.Message,
			Fields:    in.Fields,
			Config:    in.Config,
			SendOrder: sub.SendOrder,
		}
	}
	return members, nil
}

// reorderReports pairs each returned result with the sub-step that declared
// it — by index, since ExecuteReorder returns results in declaration order —
// and applies that sub-step's capture:/assert:.
func reorderReports(step scenario.Step, state *State, results []*common.StepResult) []StepReport {
	reports := make([]StepReport, len(step.Steps))
	for i, sub := range step.Steps {
		sub := sub
		result := results[i]
		err := applyStepOutcome(&sub, state, result)
		reports[i] = StepReport{
			StepID:   sub.ID,
			Protocol: sub.Protocol,
			Action:   sub.Action,
			Result:   result,
			Error:    err,
		}
	}
	return reports
}

// buildProtocolConfig converts a ProtocolConfig into a flat string map
// for passing to protocol adapters.
func buildProtocolConfig(pc *scenario.ProtocolConfig) map[string]string {
	if pc == nil {
		return nil
	}
	m := make(map[string]string)
	if pc.Transport != "" {
		m["transport"] = pc.Transport
	}
	if pc.OriginHost != "" {
		m["origin_host"] = pc.OriginHost
	}
	if pc.OriginRealm != "" {
		m["origin_realm"] = pc.OriginRealm
	}
	if pc.LocalPort > 0 {
		m["local_port"] = fmt.Sprintf("%d", pc.LocalPort)
	}
	for k, v := range map[string]string{
		"username":         pc.Username,
		"password":         pc.Password,
		"credentials_file": pc.CredentialsFile,
	} {
		if v != "" {
			m[k] = v
		}
	}
	return m
}
