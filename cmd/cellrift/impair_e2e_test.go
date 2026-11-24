package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/require"
)

// findStep returns the StepReport with the given StepID, failing the test if
// it is not found — a missing step means the scenario shape changed under
// this test's feet, which should fail loudly rather than nil-pointer later.
func findStep(t *testing.T, result *engine.RunResult, stepID string) engine.StepReport {
	t.Helper()
	for _, step := range result.Steps {
		if step.StepID == stepID {
			return step
		}
	}
	t.Fatalf("scenario result has no step %q", stepID)
	return engine.StepReport{}
}

// controlScenarioWithoutImpair is register_impaired.yaml's register_impaired
// step, minus its impair: block — the negative control that proves the test
// isn't passing for an unrelated reason (e.g. the mock itself being slow).
const controlScenarioWithoutImpair = `
name: "SIP REGISTER, no impairment (control)"
version: "1.0"
protocols:
  sip:
    transport: udp
variables:
  subscriber_msisdn: "447700900123"
  pcscf_host: "pcscf.lab.local"
steps:
  - id: register_impaired
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: REGISTER
    fields:
      From: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      To: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      Contact: "<sip:{{ subscriber_msisdn }}@10.0.0.1:5060>"
    assert:
      - response.status_code == 200
  - id: register_normal
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: REGISTER
    fields:
      From: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      To: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      Contact: "<sip:{{ subscriber_msisdn }}@10.0.0.1:5060>"
    assert:
      - response.status_code == 200
`

// TestRunScenario_ImpairmentSurvivesProductionWiring is the last-mile proof.
// The impairment decorator is tested in isolation, and both adapters are
// tested for calling SetImpairment correctly — but neither of those catches
// the production binary handing an adapter an UNWRAPPED transport, which
// fails completely and silently: impair: blocks simply do nothing, with no
// error anywhere. This test runs a real checked-in scenario file through
// runScenario against an in-process mock UAS and measures the result.
func TestRunScenario_ImpairmentSurvivesProductionWiring(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_impaired.yaml"))
	require.NotNil(t, result)
	require.Equal(t, common.StatusPassed, result.Status)

	impaired := findStep(t, result, "register_impaired")
	require.NotNil(t, impaired.Result)
	require.GreaterOrEqual(t, impaired.Result.DurationMs, int64(290),
		"the impaired step's declared latency_ms:300 must measurably delay it")

	delay, ok := impaired.Result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "the impaired step must report impair_delay_ms")
	require.GreaterOrEqual(t, delay.(int64), int64(300))

	normal := findStep(t, result, "register_normal")
	require.NotNil(t, normal.Result)
	require.Less(t, normal.Result.DurationMs, int64(120),
		"the unimpaired step must not inherit the previous step's delay")

	_, ok = normal.Result.Response[impair.ImpairDelayKey]
	require.False(t, ok, "the unimpaired step must carry no impair_delay_ms key at all")

	// Control: the same scenario shape with no impair: block anywhere must
	// complete quickly overall, so the assertions above cannot be explained
	// by an unrelated slow mock or slow test environment.
	controlPath := filepath.Join(t.TempDir(), "register_impaired_control.yaml")
	require.NoError(t, os.WriteFile(controlPath, []byte(controlScenarioWithoutImpair), 0o644))

	controlStart := time.Now()
	controlResult := cfg.runScenario(controlPath)
	controlElapsed := time.Since(controlStart)

	require.NotNil(t, controlResult)
	require.Equal(t, common.StatusPassed, controlResult.Status)
	require.Less(t, controlElapsed, 150*time.Millisecond,
		"the control run (no impair: block at all) must complete quickly")
}

// delayMsScenario declares delay_ms only (no latency_ms/jitter_ms), so this
// test cannot pass by accident via the transport-level decorator the main
// test above already covers — delay_ms is adapter-owned, applied strictly
// after Receive returns a parsed response (impair.PostReceiveDelay).
const delayMsScenario = `
name: "SIP REGISTER under injected post-receive delay"
version: "1.0"
protocols:
  sip:
    transport: udp
variables:
  subscriber_msisdn: "447700900124"
  pcscf_host: "pcscf.lab.local"
steps:
  - id: register_delayed
    protocol: sip
    action: send
    target: "{{ pcscf_host }}:5060"
    message: REGISTER
    impair:
      delay_ms: 250
    fields:
      From: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      To: "sip:{{ subscriber_msisdn }}@ims.lab.local"
      Contact: "<sip:{{ subscriber_msisdn }}@10.0.0.1:5060>"
    assert:
      - response.status_code == 200
`

// TestRunScenario_DelayMsSurvivesProductionWiring closes delay_ms's
// remaining gap: TestRunScenario_ImpairmentSurvivesProductionWiring above
// only exercises latency_ms/jitter_ms (the transport-decorator half of
// impair:). delay_ms is applied by the adapter itself, independent of
// whether the transport is wrapped, so it needs its own production-path
// proof rather than being assumed to work because the transport is wrapped.
func TestRunScenario_DelayMsSurvivesProductionWiring(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	path := filepath.Join(t.TempDir(), "register_delay_ms.yaml")
	require.NoError(t, os.WriteFile(path, []byte(delayMsScenario), 0o644))

	result := cfg.runScenario(path)
	require.NotNil(t, result)
	require.Equal(t, common.StatusPassed, result.Status)

	delayed := findStep(t, result, "register_delayed")
	require.NotNil(t, delayed.Result)
	require.GreaterOrEqual(t, delayed.Result.DurationMs, int64(240),
		"delay_ms:250 must measurably delay the step through the real runScenario path")

	delay, ok := delayed.Result.Response[impair.ImpairDelayKey]
	require.True(t, ok, "a delay_ms step must report impair_delay_ms")
	require.GreaterOrEqual(t, delay.(int64), int64(250))
}
