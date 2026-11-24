package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/riftcell/cellrift/internal/reporter"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── filterNil ────────────────────────────────────────────────────────────────

func TestFilterNil_AllNonNil(t *testing.T) {
	r1 := &engine.RunResult{Status: common.StatusPassed}
	r2 := &engine.RunResult{Status: common.StatusPassed}
	results, execErr := filterNil([]*engine.RunResult{r1, r2})
	assert.Equal(t, []*engine.RunResult{r1, r2}, results)
	assert.False(t, execErr)
}

func TestFilterNil_AllNil(t *testing.T) {
	results, execErr := filterNil([]*engine.RunResult{nil, nil})
	assert.Empty(t, results)
	assert.True(t, execErr)
}

func TestFilterNil_Mixed(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	results, execErr := filterNil([]*engine.RunResult{r, nil, r})
	assert.Len(t, results, 2)
	assert.True(t, execErr)
}

func TestFilterNil_Empty(t *testing.T) {
	results, execErr := filterNil(nil)
	assert.Empty(t, results)
	assert.False(t, execErr)
}

// ── determineExitCode ─────────────────────────────────────────────────────────

func TestDetermineExitCode_AllPassed(t *testing.T) {
	results := []*engine.RunResult{{Status: common.StatusPassed}}
	err := determineExitCode(results, false)
	assert.NoError(t, err)
}

func TestDetermineExitCode_OneFailedResult(t *testing.T) {
	results := []*engine.RunResult{{Status: common.StatusFailed}}
	err := determineExitCode(results, false)
	require.Error(t, err)
	var e *exitError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, exitTestFailure, e.code)
}

func TestDetermineExitCode_ExecutionErrorNoResults(t *testing.T) {
	err := determineExitCode(nil, true)
	require.Error(t, err)
	var e *exitError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, exitExecutionError, e.code)
}

func TestDetermineExitCode_ExecutionErrorWithPassedResults(t *testing.T) {
	// Some scenarios passed but others had execution errors.
	results := []*engine.RunResult{{Status: common.StatusPassed}}
	err := determineExitCode(results, true)
	require.Error(t, err)
	var e *exitError
	require.ErrorAs(t, err, &e)
	assert.Equal(t, exitExecutionError, e.code)
}

func TestDetermineExitCode_NoResultsNoError(t *testing.T) {
	// Edge case: empty results, no execution error.
	err := determineExitCode(nil, false)
	assert.NoError(t, err)
}

// ── exitError ─────────────────────────────────────────────────────────────────

func TestExitError_ErrorString(t *testing.T) {
	e := &exitError{code: exitTestFailure, msg: "tests failed"}
	assert.Equal(t, "tests failed", e.Error())
}

// ── selectReporter ────────────────────────────────────────────────────────────

func TestSelectReporter_Terminal(t *testing.T) {
	r := selectReporter("terminal", os.Stdout, false, false)
	_, ok := r.(*reporter.TerminalReporter)
	assert.True(t, ok, "expected TerminalReporter")
}

func TestSelectReporter_Unknown_DefaultsToTerminal(t *testing.T) {
	r := selectReporter("invalid", os.Stdout, false, false)
	_, ok := r.(*reporter.TerminalReporter)
	assert.True(t, ok, "unknown format should default to TerminalReporter")
}

func TestSelectReporter_JUnit(t *testing.T) {
	r := selectReporter("junit", os.Stdout, false, false)
	_, ok := r.(*reporter.JUnitReporter)
	assert.True(t, ok, "expected JUnitReporter")
}

func TestSelectReporter_JSON(t *testing.T) {
	r := selectReporter("json", os.Stdout, false, false)
	_, ok := r.(*reporter.JSONReporter)
	assert.True(t, ok, "expected JSONReporter")
}

// ── openOutputWriter ──────────────────────────────────────────────────────────

func TestOpenOutputWriter_Stdout(t *testing.T) {
	w, close, err := openOutputWriter("")
	require.NoError(t, err)
	defer close()
	assert.Equal(t, os.Stdout, w)
}

func TestOpenOutputWriter_File(t *testing.T) {
	path := filepath.Join(t.TempDir(), "output.xml")
	w, close, err := openOutputWriter(path)
	require.NoError(t, err)
	defer close()
	assert.NotNil(t, w)
	assert.FileExists(t, path)
}

func TestOpenOutputWriter_InvalidPath(t *testing.T) {
	_, _, err := openOutputWriter("/nonexistent/dir/output.xml")
	assert.Error(t, err)
}

// ── collectSequential / collectParallel ───────────────────────────────────────

func TestCollectSequential_AllSucceed(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	runOne := func(path string) *engine.RunResult { return r }

	results, execErr := collectSequential([]string{"a.yaml", "b.yaml"}, runOne)
	assert.Len(t, results, 2)
	assert.False(t, execErr)
}

func TestCollectSequential_OneNil(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	runOne := func(path string) *engine.RunResult {
		if path == "fail.yaml" {
			return nil
		}
		return r
	}

	results, execErr := collectSequential([]string{"ok.yaml", "fail.yaml"}, runOne)
	assert.Len(t, results, 1)
	assert.True(t, execErr)
}

func TestCollectParallel_AllSucceed(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	runOne := func(path string) *engine.RunResult { return r }

	results, execErr := collectParallel([]string{"a.yaml", "b.yaml", "c.yaml"}, runOne)
	assert.Len(t, results, 3)
	assert.False(t, execErr)
}

func TestCollectResults_SequentialWhenSingleArg(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	runOne := func(path string) *engine.RunResult { return r }

	results, execErr := collectResults([]string{"only.yaml"}, true, runOne)
	assert.Len(t, results, 1)
	assert.False(t, execErr)
}

func TestCollectResults_ParallelWithMultipleArgs(t *testing.T) {
	r := &engine.RunResult{Status: common.StatusPassed}
	runOne := func(path string) *engine.RunResult { return r }

	results, execErr := collectResults([]string{"a.yaml", "b.yaml", "c.yaml"}, true, runOne)
	assert.Len(t, results, 3)
	assert.False(t, execErr)
}

// ── parseAndValidate ──────────────────────────────────────────────────────────

func TestParseAndValidate_ValidFile(t *testing.T) {
	path := filepath.Join(scenarioDir(), "ims", "register_basic.yaml")
	s, err := parseAndValidate(path, "")
	require.NoError(t, err)
	assert.NotEmpty(t, s.Name)
}

func TestParseAndValidate_AppliesTargetHostOverride(t *testing.T) {
	path := filepath.Join(scenarioDir(), "ims", "basic_call.yaml")
	s, err := parseAndValidate(path, "override.host:9999")
	require.NoError(t, err)
	for _, step := range s.Steps {
		if step.Target != "" {
			assert.Equal(t, "override.host:9999", step.Target)
		}
	}
}

func TestParseAndValidate_MissingFile(t *testing.T) {
	_, err := parseAndValidate("/nonexistent/scenario.yaml", "")
	assert.Error(t, err)
}

func TestParseAndValidate_InvalidScenario(t *testing.T) {
	bad := writeFile(t, t.TempDir(), "bad.yaml", "steps:\n  - id: s1\n    protocol: gtp\n    action: send\n")
	_, err := parseAndValidate(bad, "")
	assert.Error(t, err)
}

// ── buildSIPAdapter ───────────────────────────────────────────────────────────

func TestBuildSIPAdapter_UDP(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Transport: "udp"}
	a, _, err := buildSIPAdapter(cfg, time.Second)
	require.NoError(t, err)
	defer a.Close()
	assert.NotNil(t, a)
}

func TestBuildSIPAdapter_EmptyDefaultsToUDP(t *testing.T) {
	cfg := &scenario.ProtocolConfig{}
	a, _, err := buildSIPAdapter(cfg, time.Second)
	require.NoError(t, err)
	defer a.Close()
	assert.NotNil(t, a)
}

func TestBuildSIPAdapter_TCP(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Transport: "tcp"}
	a, _, err := buildSIPAdapter(cfg, time.Second)
	require.NoError(t, err)
	defer a.Close()
	assert.NotNil(t, a)
}

// TestBuildSIPAdapter_TLS_NoCredentials pins the secure default: a bare
// `transport: tls` with no cert block builds a verifying client and emits NO
// warning. This inverts the previous expectation, under which the same
// config silently set InsecureSkipVerify — the most natural TLS
// configuration produced an unauthenticated connection with no signal
// anywhere but a source comment.
func TestBuildSIPAdapter_TLS_NoCredentials(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Transport: "tls"}
	a, warnings, err := buildSIPAdapter(cfg, time.Second)
	require.NoError(t, err)
	defer a.Close()
	assert.NotNil(t, a)
	assert.Empty(t, warnings, "a verifying TLS client is the default and must not warn")
}

// TestBuildSIPAdapter_TLS_InsecureSkipVerifyWarns pins the explicit opt-out:
// disabling verification is allowed, but it must surface as a run-level
// warning that names the key, so it reaches every reporter and a CI log
// reader can find the scenario line that caused it.
func TestBuildSIPAdapter_TLS_InsecureSkipVerifyWarns(t *testing.T) {
	cfg := &scenario.ProtocolConfig{Transport: "tls", InsecureSkipVerify: true}
	a, warnings, err := buildSIPAdapter(cfg, time.Second)
	require.NoError(t, err)
	defer a.Close()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "insecure_skip_verify")
	assert.Contains(t, warnings[0], "NOT verified")
}

func TestBuildSIPAdapter_TLS_InvalidCertFile(t *testing.T) {
	cfg := &scenario.ProtocolConfig{
		Transport: "tls",
		CertFile:  "/nonexistent/cert.pem",
		KeyFile:   "/nonexistent/key.pem",
	}
	_, _, err := buildSIPAdapter(cfg, time.Second)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TLS config")
}

// TestBuildSIPAdapter_TCPKeepsTCPViaToken is the regression guard for the
// downgrade buildSIPAdapter's rewrite could have reintroduced: before
// NewAdapterWithTransport was widened with a transportType parameter, any
// externally-supplied transport was hardcoded to "udp", so a tcp scenario
// built through buildSIPAdapter would have silently emitted SIP/2.0/UDP.
// Asserted here at the buildSIPAdapter level, not only at the constructor
// level internal/protocols/sip's own test already covers.
func TestBuildSIPAdapter_TCPKeepsTCPViaToken(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer mock.Close()

	cfg := &scenario.ProtocolConfig{Transport: "tcp"}
	a, _, err := buildSIPAdapter(cfg, 2*time.Second)
	require.NoError(t, err)
	defer a.Close()

	result, err := a.Execute("send", mock.Addr(), "REGISTER", map[string]string{
		"From": "<sip:447700900001@ims.local>;tag=via-tcp-001",
		"To":   "<sip:ims.local>",
	}, nil)
	require.NoError(t, err)
	require.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)

	headers, ok := result.Response["header"].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, headers["Via"].(string), "SIP/2.0/TCP")
}

// ── buildDiameterAdapter ──────────────────────────────────────────────────────

// TestBuildDiameterAdapter_TCPBranchStillHandshakes proves, at the
// production construction site rather than only inside
// internal/protocols/diameter's own tests, that impair.Wrap does not cost
// the tcp branch's transport its common.Connector identity — which would
// break the CER/CEA handshake with "transport ... does not support
// connection establishment".
func TestBuildDiameterAdapter_TCPBranchStillHandshakes(t *testing.T) {
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	cfg := &scenario.ProtocolConfig{Transport: "tcp"}
	a, warnings, err := buildDiameterAdapter(cfg, 2*time.Second)
	require.NoError(t, err)
	defer a.Close()
	assert.Empty(t, warnings, "transport: tcp must not produce a substitution warning")

	result, err := a.Execute("send", hss.Addr(), "UAR", map[string]string{
		"Session-Id":      "cellrift;test;helpers",
		"Origin-Host":     "cellrift.ims.local",
		"Origin-Realm":    "ims.local",
		"Public-Identity": "sip:447700900001@ims.local",
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, common.StatusPassed, result.Status, "error: %s", result.Error)
}

// ── mechanical audit: every adapter construction site wraps with impair ────

// TestAdapterConstruction_AlwaysWrapsWithImpair is a source-level audit, in
// the same spirit as internal/scenario's dispatch audit: it reads main.go
// and fails if the production binary can ever hand a protocol adapter a
// transport that was not wrapped.
//
// This defect class is invisible at runtime. Every unit test would stay
// green, every scenario would still run, and impair: blocks would simply do
// nothing — the decorator is a pass-through, so an unwrapped path produces
// no error, no warning, and no failing assertion anywhere else.
func TestAdapterConstruction_AlwaysWrapsWithImpair(t *testing.T) {
	data, err := os.ReadFile("main.go")
	require.NoError(t, err, "this audit reads main.go directly; it must exist alongside this test file")

	forbidden := []string{"sip.NewAdapter(", "sip.NewAdapterTLS(", "diameter.NewAdapter("}
	sawSIP, sawDiameter := false, false

	for i, line := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		for _, f := range forbidden {
			assert.NotContains(t, line, f,
				"main.go:%d calls %s, which builds its transport internally and offers no seam to decorate it — build the transport directly and wrap it with impair.Wrap instead",
				lineNo, f)
		}
		if !strings.Contains(line, "NewAdapterWithTransport(") {
			continue
		}
		assert.Contains(t, line, "impair.Wrap(",
			"main.go:%d calls NewAdapterWithTransport without impair.Wrap — every adapter construction site must hand the adapter an already-wrapped transport: %s",
			lineNo, strings.TrimSpace(line))
		sawSIP = sawSIP || strings.Contains(line, "sip.")
		sawDiameter = sawDiameter || strings.Contains(line, "diameter.")
	}

	assert.True(t, sawSIP, "no sip.NewAdapterWithTransport( call found in main.go — this audit would pass vacuously against a file that constructs no SIP adapter at all")
	assert.True(t, sawDiameter, "no diameter.NewAdapterWithTransport( call found in main.go — this audit would pass vacuously against a file that constructs no Diameter adapter at all")
}

// ── buildAdapters ─────────────────────────────────────────────────────────────

func TestBuildAdapters_SIPOnly(t *testing.T) {
	s := &scenario.Scenario{
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
	}
	adapters, _, cleanup, err := buildAdapters(s, time.Second)
	require.NoError(t, err)
	defer cleanup()
	assert.Contains(t, adapters, "sip")
}

func TestBuildAdapters_DiameterOnly(t *testing.T) {
	s := &scenario.Scenario{
		Protocols: map[string]*scenario.ProtocolConfig{
			"diameter": {Transport: "tcp"},
		},
	}
	adapters, warnings, cleanup, err := buildAdapters(s, time.Second)
	require.NoError(t, err)
	defer cleanup()
	assert.Contains(t, adapters, "diameter")
	assert.Empty(t, warnings, "transport: tcp must not produce a substitution warning")
}

func TestBuildAdapters_Empty(t *testing.T) {
	s := &scenario.Scenario{Protocols: map[string]*scenario.ProtocolConfig{}}
	adapters, _, cleanup, err := buildAdapters(s, time.Second)
	require.NoError(t, err)
	defer cleanup()
	assert.Empty(t, adapters)
}

// ── reporter output format smoke tests ───────────────────────────────────────

func TestSelectReporter_JUnitWritesXML(t *testing.T) {
	var buf bytes.Buffer
	r := selectReporter("junit", &buf, false, false)
	results := []*engine.RunResult{
		{
			ScenarioName: "test",
			Status:       common.StatusPassed,
			Steps: []engine.StepReport{
				{StepID: "s1", Result: &common.StepResult{Status: common.StatusPassed}},
			},
		},
	}
	err := r.Report(&buf, results)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "<testsuites")
}

func TestSelectReporter_JSONWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	r := selectReporter("json", &buf, false, false)
	results := []*engine.RunResult{
		{ScenarioName: "test-a", Status: common.StatusPassed, Steps: []engine.StepReport{}},
		{ScenarioName: "test-b", Status: common.StatusPassed, Steps: []engine.StepReport{}},
	}
	err := r.Report(&buf, results)
	require.NoError(t, err)
	// Two results → JSON array.
	assert.True(t, strings.HasPrefix(strings.TrimSpace(buf.String()), "["), "two results should produce a JSON array")
}
