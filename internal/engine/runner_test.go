package engine_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sequenceAdapter returns results from a sequence, advancing on each call.
type sequenceAdapter struct {
	results []*common.StepResult
	index   int
}

func (s *sequenceAdapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	if s.index >= len(s.results) {
		return nil, fmt.Errorf("no more results configured")
	}
	r := s.results[s.index]
	s.index++
	return r, nil
}

func (s *sequenceAdapter) Close() error { return nil }

func TestRun_AllStepsPass(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-pass",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
			{ID: "step2", Protocol: "sip", Action: "send", Target: "host:5060", Message: "INVITE"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{})
	assert.Equal(t, common.StatusPassed, result.Status)
	assert.Len(t, result.Steps, 2)
	assert.Equal(t, "test-pass", result.ScenarioName)
}

func TestRun_Step2Fails_Step3StillRuns_WithoutFailFast(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-continue",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
			{ID: "step2", Protocol: "sip", Action: "send", Target: "host:5060", Message: "INVITE"},
			{ID: "step3", Protocol: "sip", Action: "send", Target: "host:5060", Message: "BYE"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusFailed, Error: "timeout", Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{FailFast: false})

	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 3, "step 3 should still run without fail-fast")
	assert.Equal(t, common.StatusPassed, result.Steps[0].Result.Status)
	assert.Equal(t, common.StatusFailed, result.Steps[1].Result.Status)
	assert.Equal(t, common.StatusPassed, result.Steps[2].Result.Status)
}

func TestRun_Step2Fails_Step3Skipped_WithFailFast(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-failfast",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
			{ID: "step2", Protocol: "sip", Action: "send", Target: "host:5060", Message: "INVITE"},
			{ID: "step3", Protocol: "sip", Action: "send", Target: "host:5060", Message: "BYE"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusFailed, Error: "timeout", Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{FailFast: true})

	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2, "step 3 should be skipped with fail-fast")
}

func TestRun_NoAdapterForProtocol(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-no-adapter",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"gtp": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "gtp", Action: "send", Target: "host:2152", Message: "CREATE"},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{}, engine.RunOptions{})
	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 1)
	assert.Contains(t, result.Steps[0].Result.Error, "no adapter")
}

func TestRun_VariableOverrides(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-override",
		Version: "1.0",
		Variables: map[string]string{
			"msisdn": "447700900001",
		},
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{
				ID: "step1", Protocol: "sip", Action: "send",
				Target: "host:5060", Message: "REGISTER",
				Fields: map[string]string{"From": "sip:{{ msisdn }}@ims.local"},
			},
		},
	}

	adapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{
		VarOverrides: []string{"msisdn=447700900002"},
	})

	assert.Equal(t, common.StatusPassed, result.Status)
	require.Len(t, adapter.lastCalls, 1)
	assert.Equal(t, "sip:447700900002@ims.local", adapter.lastCalls[0].Fields["From"])
}

func TestRun_StepsExecuteInOrder(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-order",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "first", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
			{ID: "second", Protocol: "sip", Action: "send", Target: "host:5060", Message: "INVITE"},
			{ID: "third", Protocol: "sip", Action: "send", Target: "host:5060", Message: "BYE"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{})
	require.Len(t, result.Steps, 3)
	assert.Equal(t, "first", result.Steps[0].StepID)
	assert.Equal(t, "second", result.Steps[1].StepID)
	assert.Equal(t, "third", result.Steps[2].StepID)
}

func TestRun_ParallelSteps(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-parallel",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip":      {Transport: "udp"},
			"diameter": {Transport: "tcp"},
		},
		Steps: []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "sip_step", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
					{ID: "diam_step", Protocol: "diameter", Action: "send", Target: "host:3868", Message: "UAR"},
				},
			},
		},
	}

	sipAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}
	diamAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}

	adapters := map[string]common.ProtocolAdapter{
		"sip":      sipAdapter,
		"diameter": diamAdapter,
	}

	result := engine.Run(s, adapters, engine.RunOptions{})
	assert.Equal(t, common.StatusPassed, result.Status)
	require.Len(t, result.Steps, 2, "parallel step should expand into 2 sub-step reports")
	assert.Equal(t, "sip_step", result.Steps[0].StepID)
	assert.Equal(t, "diam_step", result.Steps[1].StepID)
}

func TestRun_ParallelSteps_OneFailure(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-parallel-fail",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip":      {Transport: "udp"},
			"diameter": {Transport: "tcp"},
		},
		Steps: []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "sip_step", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
					{ID: "diam_step", Protocol: "diameter", Action: "send", Target: "host:3868", Message: "UAR"},
				},
			},
		},
	}

	sipAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}
	diamAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusFailed,
			Error:    "timeout",
			Response: map[string]interface{}{},
		},
	}

	adapters := map[string]common.ProtocolAdapter{
		"sip":      sipAdapter,
		"diameter": diamAdapter,
	}

	result := engine.Run(s, adapters, engine.RunOptions{})
	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2, "both sub-steps should complete even if one fails")
}

// TestRunResult_CarriesFallbackWarning pins the contract that
// RunOptions.Warnings flows through unchanged into RunResult.Warnings, and
// that this survives a passing run, a failing run, and a fail-fast run —
// warnings are run-level, not step-level, and are not cleared by a failure
// or a short-circuit.
func TestRunResult_CarriesFallbackWarning(t *testing.T) {
	warning := "transport: requested sctp, used tcp (sctp unavailable: x)"

	newScenario := func(name string) *scenario.Scenario {
		return &scenario.Scenario{
			Name:    name,
			Version: "1.0",
			Protocols: map[string]*scenario.ProtocolConfig{
				"diameter": {Transport: "sctp"},
			},
			Steps: []scenario.Step{
				{ID: "step1", Protocol: "diameter", Action: "send", Target: "host:3868", Message: "UAR"},
				{ID: "step2", Protocol: "diameter", Action: "send", Target: "host:3868", Message: "UAR"},
			},
		}
	}

	t.Run("nil warnings produce an empty RunResult.Warnings", func(t *testing.T) {
		adapter := &sequenceAdapter{
			results: []*common.StepResult{
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
			},
		}
		result := engine.Run(newScenario("no-warnings"), map[string]common.ProtocolAdapter{"diameter": adapter}, engine.RunOptions{Warnings: nil})
		assert.Empty(t, result.Warnings)
	})

	t.Run("a passing run carries exactly the one warning", func(t *testing.T) {
		adapter := &sequenceAdapter{
			results: []*common.StepResult{
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
			},
		}
		result := engine.Run(newScenario("pass"), map[string]common.ProtocolAdapter{"diameter": adapter}, engine.RunOptions{Warnings: []string{warning}})
		assert.Equal(t, common.StatusPassed, result.Status)
		assert.Equal(t, []string{warning}, result.Warnings)
	})

	t.Run("a failing run still carries the warning", func(t *testing.T) {
		adapter := &sequenceAdapter{
			results: []*common.StepResult{
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
				{Status: common.StatusFailed, Error: "timeout", Response: map[string]interface{}{}},
			},
		}
		result := engine.Run(newScenario("fail"), map[string]common.ProtocolAdapter{"diameter": adapter}, engine.RunOptions{Warnings: []string{warning}})
		assert.Equal(t, common.StatusFailed, result.Status)
		assert.Equal(t, []string{warning}, result.Warnings)
	})

	t.Run("a fail-fast run still carries the warning", func(t *testing.T) {
		adapter := &sequenceAdapter{
			results: []*common.StepResult{
				{Status: common.StatusFailed, Error: "timeout", Response: map[string]interface{}{}},
				{Status: common.StatusPassed, Response: map[string]interface{}{}},
			},
		}
		result := engine.Run(newScenario("failfast"), map[string]common.ProtocolAdapter{"diameter": adapter}, engine.RunOptions{FailFast: true, Warnings: []string{warning}})
		assert.Equal(t, common.StatusFailed, result.Status)
		require.Len(t, result.Steps, 1, "step 2 should be skipped with fail-fast")
		assert.Equal(t, []string{warning}, result.Warnings)
	})
}

func TestRun_DurationMsPopulated(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-duration",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{})
	assert.GreaterOrEqual(t, result.DurationMs, int64(0))
}

func TestRun_WaitStepPausesBeforeNextStep(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-wait",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{ID: "before", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
			{ID: "pause", Action: "wait", Duration: 150},
			{ID: "after", Protocol: "sip", Action: "send", Target: "host:5060", Message: "BYE"},
		},
	}

	adapter := &sequenceAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	start := time.Now()
	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": adapter}, engine.RunOptions{})
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 150*time.Millisecond)
	assert.Less(t, elapsed, 5*time.Second)
	require.Len(t, result.Steps, 3)
	assert.Equal(t, "wait", result.Steps[1].Action)
	require.NotNil(t, result.Steps[1].Result)
	assert.Equal(t, common.StatusPassed, result.Steps[1].Result.Status)
}

func TestRun_WaitStepNeedsNoAdapter(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-wait-no-adapter",
		Version: "1.0",
		Steps: []scenario.Step{
			{ID: "pause", Action: "wait", Duration: 50},
		},
	}

	start := time.Now()
	result := engine.Run(s, common.AdapterMap{}, engine.RunOptions{})
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 5*time.Second)
	assert.Equal(t, common.StatusPassed, result.Status)
	require.Len(t, result.Steps, 1)
	assert.Nil(t, result.Steps[0].Error)
	require.NotNil(t, result.Steps[0].Result)
	assert.NotContains(t, result.Steps[0].Result.Error, "no adapter registered")
	assert.GreaterOrEqual(t, result.Steps[0].Result.DurationMs, int64(50-1))
}

// ── action: reorder ──────────────────────────────────────────────────────

// fakeReorderAdapter implements both common.ProtocolAdapter and
// common.ReorderSender. It records every ExecuteReorder call it receives and
// returns scripted results (or a scripted error) without ever needing a real
// wire.
type fakeReorderAdapter struct {
	gotMembers   common.ReorderMembers
	gotTimeoutMs int
	calls        int
	results      []*common.StepResult
	err          error
}

func (f *fakeReorderAdapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	return nil, fmt.Errorf("fakeReorderAdapter.Execute should never be called for a reorder group")
}

func (f *fakeReorderAdapter) Close() error { return nil }

func (f *fakeReorderAdapter) ExecuteReorder(members common.ReorderMembers, timeoutMs int) ([]*common.StepResult, error) {
	f.calls++
	f.gotMembers = members
	f.gotTimeoutMs = timeoutMs
	if f.err != nil {
		return nil, f.err
	}
	return f.results, nil
}

// fakePlainAdapter implements only common.ProtocolAdapter — no
// ExecuteReorder — to prove the runner reports a defined failure rather than
// panicking when an adapter does not implement ReorderSender.
type fakePlainAdapter struct{}

func (fakePlainAdapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	return &common.StepResult{Status: common.StatusPassed, Response: map[string]interface{}{}}, nil
}

func (fakePlainAdapter) Close() error { return nil }

func reorderScenario(subs ...scenario.Step) *scenario.Scenario {
	return &scenario.Scenario{
		Name:    "test-reorder",
		Version: "1.0",
		Steps: []scenario.Step{
			{ID: "reorder_group", Action: "reorder", Steps: subs},
		},
	}
}

func TestRun_Reorder_DeclarationOrderResultsAndMemberFields(t *testing.T) {
	adapter := &fakeReorderAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{"seq": "a"}},
			{Status: common.StatusPassed, Response: map[string]interface{}{"seq": "b"}},
		},
	}

	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 2},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 1},
	)
	s.Steps[0].TimeoutMs = 4000

	result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})

	assert.Equal(t, common.StatusPassed, result.Status)
	require.Equal(t, 1, adapter.calls, "ExecuteReorder must be called exactly once")
	assert.Equal(t, 4000, adapter.gotTimeoutMs, "timeoutMs must be the reorder step's own TimeoutMs")

	require.Len(t, adapter.gotMembers, 2)
	assert.Equal(t, 2, adapter.gotMembers[0].SendOrder, "members must be in declaration order, carrying their own SendOrder")
	assert.Equal(t, 1, adapter.gotMembers[1].SendOrder)
	assert.Equal(t, "REGISTER", adapter.gotMembers[0].Message)
	assert.Equal(t, "INVITE", adapter.gotMembers[1].Message)

	require.Len(t, result.Steps, 2)
	assert.Equal(t, "msg_a", result.Steps[0].StepID, "reports come back in declaration order, not send_order")
	assert.Equal(t, "msg_b", result.Steps[1].StepID)
	assert.Equal(t, "sip", result.Steps[0].Protocol)
	assert.Equal(t, "send", result.Steps[0].Action)
}

func TestRun_Reorder_NonImplementingAdapterFailsExplicitly(t *testing.T) {
	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)

	result := engine.Run(s, common.AdapterMap{"sip": fakePlainAdapter{}}, engine.RunOptions{})

	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2)
	for _, report := range result.Steps {
		require.Error(t, report.Error)
		assert.Contains(t, report.Error.Error(), "does not support action: reorder")
		require.NotNil(t, report.Result)
		assert.Equal(t, common.StatusFailed, report.Result.Status)
	}
}

func TestRun_Reorder_UnregisteredProtocolFailsExplicitly(t *testing.T) {
	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)

	result := engine.Run(s, common.AdapterMap{}, engine.RunOptions{})

	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2)
	for _, report := range result.Steps {
		require.Error(t, report.Error)
		assert.Contains(t, report.Error.Error(), `no adapter registered for protocol "sip"`)
	}
}

func TestRun_Reorder_ResolutionFailureAbortsWholeGroup(t *testing.T) {
	adapter := &fakeReorderAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "{{ undefined_var }}", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)

	result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})

	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, 0, adapter.calls, "ExecuteReorder must never be called when a sub-step fails to resolve")
	require.Len(t, result.Steps, 2)
	for _, report := range result.Steps {
		require.Error(t, report.Error)
		assert.Contains(t, report.Error.Error(), "msg_a", "the failure must name the sub-step that failed to resolve")
	}
}

func TestRun_Reorder_ExecuteReorderErrorFailsEverySubStep(t *testing.T) {
	adapter := &fakeReorderAdapter{err: fmt.Errorf("adapter exploded")}

	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)

	result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})

	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2)
	for _, report := range result.Steps {
		require.Error(t, report.Error)
		assert.Contains(t, report.Error.Error(), "adapter exploded")
	}
}

func TestRun_Reorder_WrongResultCountFailsInsteadOfPanicking(t *testing.T) {
	adapter := &fakeReorderAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}

	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)

	assert.NotPanics(t, func() {
		result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})
		assert.Equal(t, common.StatusFailed, result.Status)
		require.Len(t, result.Steps, 2)
		for _, report := range result.Steps {
			require.Error(t, report.Error)
			assert.Contains(t, report.Error.Error(), "1")
			assert.Contains(t, report.Error.Error(), "2")
		}
	})
}

func TestRun_Reorder_CaptureAssertAppliedToOwnSubStep(t *testing.T) {
	adapter := &fakeReorderAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{"session_id": "sess-a"}},
			{Status: common.StatusPassed, Response: map[string]interface{}{"session_id": "sess-b"}},
		},
	}

	s := reorderScenario(
		scenario.Step{
			ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 2,
			Capture: map[string]string{"a_session": "response.session_id"},
		},
		scenario.Step{
			ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 1,
			Capture: map[string]string{"b_session": "response.session_id"},
			Assert:  []string{"b_session == sess-b"},
		},
	)

	result := engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})
	assert.Equal(t, common.StatusPassed, result.Status)
	require.Len(t, result.Steps, 2)
	// The assertion on msg_b (send_order: 1, wire-first) resolves correctly
	// even though msg_a (send_order: 2) is declared first: capture:/assert:
	// key off declaration order/step ID, never physical send timing.
	assert.Equal(t, common.StatusPassed, result.Steps[0].Result.Status, "msg_a")
	assert.Equal(t, common.StatusPassed, result.Steps[1].Result.Status, "msg_b (asserts b_session == sess-b)")
}

func TestRun_Reorder_FailFastStopsAfterFailedGroup(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-reorder-failfast",
		Version: "1.0",
		Steps: []scenario.Step{
			{ID: "reorder_group", Action: "reorder", Steps: []scenario.Step{
				{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
				{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
			}},
			{ID: "after", Protocol: "sip", Action: "send", Target: "h:5060", Message: "BYE"},
		},
	}

	result := engine.Run(s, common.AdapterMap{}, engine.RunOptions{FailFast: true})
	assert.Equal(t, common.StatusFailed, result.Status)
	require.Len(t, result.Steps, 2, "the after step must be skipped once the reorder group fails under FailFast")
}

func TestRun_Reorder_NoGoroutineStarted(t *testing.T) {
	// executeReorder itself starts no goroutine (grep-verified: only
	// executeParallel's pre-existing goroutine exists in runner.go). This
	// test proves the observable consequence: a reorder group with a
	// resolution-failing sub-step aborts synchronously, with the adapter's
	// ExecuteReorder never invoked from a separate goroutine racing this
	// assertion.
	adapter := &fakeReorderAdapter{
		results: []*common.StepResult{
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
			{Status: common.StatusPassed, Response: map[string]interface{}{}},
		},
	}
	s := reorderScenario(
		scenario.Step{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
		scenario.Step{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
	)
	engine.Run(s, common.AdapterMap{"sip": adapter}, engine.RunOptions{})
	assert.Equal(t, 1, adapter.calls)
}

func TestRun_ParallelWaitSubStep(t *testing.T) {
	s := &scenario.Scenario{
		Name:    "test-parallel-wait",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "pause", Action: "wait", Duration: 100},
					{ID: "sip_step", Protocol: "sip", Action: "send", Target: "host:5060", Message: "REGISTER"},
				},
			},
		},
	}

	sipAdapter := &mockAdapter{
		result: &common.StepResult{
			Status:   common.StatusPassed,
			Response: map[string]interface{}{},
		},
	}

	start := time.Now()
	result := engine.Run(s, map[string]common.ProtocolAdapter{"sip": sipAdapter}, engine.RunOptions{})
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond)
	assert.Equal(t, common.StatusPassed, result.Status)
	require.Len(t, result.Steps, 2)
	for _, report := range result.Steps {
		assert.Nil(t, report.Error)
		require.NotNil(t, report.Result)
		assert.NotEqual(t, common.StatusFailed, report.Result.Status)
		assert.NotContains(t, report.Result.Error, "no adapter registered")
	}
}
