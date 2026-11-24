package reporter_test

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleResults() []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "VoLTE registration",
			Status:       common.StatusFailed,
			DurationMs:   412,
			Steps: []engine.StepReport{
				{
					StepID:   "diameter_auth",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 15,
					},
				},
				{
					StepID:   "sip_register",
					Protocol: "sip",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusFailed,
						DurationMs: 350,
						Error:      "assertion failed: response.status_code == 200 (actual: 403)",
					},
				},
				{
					StepID:   "verify_state",
					Protocol: "sip",
					Action:   "assert_state",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 0,
					},
				},
			},
		},
	}
}

func allPassedResults() []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "Diameter UAR",
			Status:       common.StatusPassed,
			DurationMs:   50,
			Steps: []engine.StepReport{
				{
					StepID:   "send_uar",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 25,
					},
				},
				{
					StepID:   "check_result",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 20,
					},
				},
			},
		},
	}
}

// resultsWithWarning and resultsWithoutWarning are the fixtures the
// three warning-rendering tests share: identical except for the run-level
// Warnings field, so a diff in output is attributable to that field alone.
func resultsWithWarning() []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "Diameter transport fallback",
			Status:       common.StatusPassed,
			DurationMs:   50,
			Warnings:     []string{"transport: requested sctp, used tcp (sctp unavailable: no kernel support)"},
			Steps: []engine.StepReport{
				{
					StepID:   "send_uar",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 25,
					},
				},
			},
		},
	}
}

func resultsWithoutWarning() []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "Diameter transport fallback",
			Status:       common.StatusPassed,
			DurationMs:   50,
			Steps: []engine.StepReport{
				{
					StepID:   "send_uar",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 25,
					},
				},
			},
		},
	}
}

func TestJSONReporter_RendersWarnings(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, resultsWithWarning()))

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))

	warnings, ok := parsed["warnings"].([]interface{})
	require.True(t, ok, "warnings key must be present and an array")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "requested sctp, used tcp")

	var buf2 bytes.Buffer
	require.NoError(t, r.Report(&buf2, resultsWithoutWarning()))
	assert.NotContains(t, buf2.String(), `"warnings"`, "the key must be omitted entirely when there are no warnings")
}

func TestJUnitReporter_RendersWarnings(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, resultsWithWarning()))

	type property struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	}
	type properties struct {
		Properties []property `xml:"property"`
	}
	type testsuite struct {
		Properties properties `xml:"properties"`
	}
	type testsuites struct {
		Suites []testsuite `xml:"testsuite"`
	}
	var parsed testsuites
	require.NoError(t, xml.Unmarshal(buf.Bytes(), &parsed))

	require.Len(t, parsed.Suites, 1)
	require.Len(t, parsed.Suites[0].Properties.Properties, 1)
	assert.Equal(t, "cellrift.warning", parsed.Suites[0].Properties.Properties[0].Name)
	assert.Contains(t, parsed.Suites[0].Properties.Properties[0].Value, "requested sctp, used tcp")

	var buf2 bytes.Buffer
	require.NoError(t, r.Report(&buf2, resultsWithoutWarning()))
	assert.NotContains(t, buf2.String(), "<properties>", "no properties element when there are no warnings")
}

func TestTerminalReporter_RendersWarnings(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, resultsWithWarning()))

	out := buf.String()
	assert.Contains(t, out, "WARN: transport: requested sctp, used tcp")

	warnIdx := strings.Index(out, "WARN:")
	summaryIdx := strings.Index(out, "steps passed")
	require.NotEqual(t, -1, warnIdx)
	require.NotEqual(t, -1, summaryIdx)
	assert.Less(t, warnIdx, summaryIdx, "the warning line must appear before the steps-passed summary line")

	var buf2 bytes.Buffer
	r2 := reporter.NewTerminalReporter(&buf2, false)
	require.NoError(t, r2.Report(&buf2, resultsWithoutWarning()))
	assert.NotContains(t, buf2.String(), "WARN:", "no warning line when there are no warnings")
}

// --- impairDelayMs ---

func resultsWithImpairDelay(ms int64) []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "impaired step",
			Status:       common.StatusPassed,
			DurationMs:   50,
			Steps: []engine.StepReport{
				{
					StepID:   "impaired_send",
					Protocol: "sip",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 350,
						Response:   common.ResponseFields{"impair_delay_ms": ms},
					},
				},
			},
		},
	}
}

func TestTerminalReporter_RendersImpairDelayWithoutVerbose(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, resultsWithImpairDelay(320)))

	assert.Contains(t, buf.String(), "(impaired +320ms)")
}

func TestTerminalReporter_OmitsImpairDelayWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, allPassedResults()))

	assert.NotContains(t, buf.String(), "impaired")
}

// --- Terminal Reporter ---

func TestTerminalReporter_PassedStep(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false) // non-TTY → no colour
	require.NoError(t, r.Report(&buf, allPassedResults()))

	out := buf.String()
	assert.Contains(t, out, "\u2713") // green tick (no ANSI because non-TTY)
	assert.Contains(t, out, "send_uar")
	assert.Contains(t, out, "2/2 steps passed")
	assert.Contains(t, out, "50ms")
}

func TestTerminalReporter_FailedStep(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, sampleResults()))

	out := buf.String()
	assert.Contains(t, out, "\u2717") // red cross
	assert.Contains(t, out, "sip_register")
	assert.Contains(t, out, "assertion failed")
	assert.Contains(t, out, "2/3 steps passed")
}

func TestTerminalReporter_SummaryLine(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, allPassedResults()))

	out := buf.String()
	assert.Contains(t, out, "2/2 steps passed in 50ms")
}

func TestTerminalReporter_NoColourNonTTY(t *testing.T) {
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false) // bytes.Buffer is not a TTY
	require.NoError(t, r.Report(&buf, allPassedResults()))

	out := buf.String()
	assert.NotContains(t, out, "\033[") // no ANSI escape codes
}

func TestTerminalReporter_VerboseShowsRawResponse(t *testing.T) {
	results := []*engine.RunResult{
		{
			ScenarioName: "verbose test",
			Status:       common.StatusPassed,
			DurationMs:   10,
			Steps: []engine.StepReport{
				{
					StepID:   "step1",
					Protocol: "sip",
					Action:   "send",
					Result: &common.StepResult{
						Status:      common.StatusPassed,
						DurationMs:  5,
						RawResponse: []byte("SIP/2.0 200 OK\r\n"),
					},
				},
			},
		},
	}

	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, true)
	require.NoError(t, r.Report(&buf, results))

	out := buf.String()
	assert.Contains(t, out, "raw response")
	assert.Contains(t, out, "SIP/2.0 200 OK")
}

// --- JUnit XML Reporter ---

func TestJUnitReporter_ValidXML(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	// Must parse as valid XML.
	var suites struct {
		XMLName xml.Name `xml:"testsuites"`
	}
	require.NoError(t, xml.Unmarshal(buf.Bytes(), &suites))
	assert.Equal(t, "testsuites", suites.XMLName.Local)
}

func TestJUnitReporter_RootIsTestsuites(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	out := buf.String()
	assert.Contains(t, out, "<testsuites>")
	assert.Contains(t, out, "</testsuites>")
}

func TestJUnitReporter_TestsuiteAttributes(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	type testsuite struct {
		Name     string `xml:"name,attr"`
		Tests    int    `xml:"tests,attr"`
		Failures int    `xml:"failures,attr"`
		Time     string `xml:"time,attr"`
	}
	type testsuites struct {
		Suites []testsuite `xml:"testsuite"`
	}
	var parsed testsuites
	require.NoError(t, xml.Unmarshal(buf.Bytes(), &parsed))

	require.Len(t, parsed.Suites, 1)
	assert.Equal(t, "VoLTE registration", parsed.Suites[0].Name)
	assert.Equal(t, 3, parsed.Suites[0].Tests)
	assert.Equal(t, 1, parsed.Suites[0].Failures)
}

func TestJUnitReporter_TestcaseAttributes(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	type failure struct {
		Message string `xml:"message,attr"`
		Body    string `xml:",chardata"`
	}
	type testcase struct {
		Name      string   `xml:"name,attr"`
		Classname string   `xml:"classname,attr"`
		Time      string   `xml:"time,attr"`
		Failure   *failure `xml:"failure"`
	}
	type testsuite struct {
		Cases []testcase `xml:"testcase"`
	}
	type testsuites struct {
		Suites []testsuite `xml:"testsuite"`
	}
	var parsed testsuites
	require.NoError(t, xml.Unmarshal(buf.Bytes(), &parsed))

	cases := parsed.Suites[0].Cases
	require.Len(t, cases, 3)

	// First case: passed.
	assert.Equal(t, "diameter_auth", cases[0].Name)
	assert.Equal(t, "diameter.send", cases[0].Classname)
	assert.Nil(t, cases[0].Failure)

	// Second case: failed with failure element.
	assert.Equal(t, "sip_register", cases[1].Name)
	require.NotNil(t, cases[1].Failure)
	assert.Contains(t, cases[1].Failure.Message, "assertion failed")
}

func TestJUnitReporter_RendersImpairDelayProperty(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, resultsWithImpairDelay(320)))

	out := buf.String()
	assert.Contains(t, out, `name="cellrift.impair_delay_ms"`)
	assert.Contains(t, out, `value="320"`)

	var buf2 bytes.Buffer
	require.NoError(t, r.Report(&buf2, allPassedResults()))
	assert.NotContains(t, buf2.String(), "cellrift.impair_delay_ms", "no property when there was no impairment")
}

// --- JSON Reporter ---

func TestJSONReporter_ValidJSON(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))
}

func TestJSONReporter_TopLevelKeys(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))

	assert.Contains(t, parsed, "scenario")
	assert.Contains(t, parsed, "status")
	assert.Contains(t, parsed, "duration_ms")
	assert.Contains(t, parsed, "steps")

	assert.Equal(t, "VoLTE registration", parsed["scenario"])
	assert.Equal(t, "failed", parsed["status"])
	assert.Equal(t, float64(412), parsed["duration_ms"])
}

func TestJSONReporter_StepFields(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, sampleResults()))

	var parsed map[string]interface{}
	require.NoError(t, json.Unmarshal(buf.Bytes(), &parsed))

	steps, ok := parsed["steps"].([]interface{})
	require.True(t, ok)
	require.Len(t, steps, 3)

	step0 := steps[0].(map[string]interface{})
	assert.Equal(t, "diameter_auth", step0["id"])
	assert.Equal(t, "diameter", step0["protocol"])
	assert.Equal(t, "passed", step0["status"])
	assert.Equal(t, float64(15), step0["duration_ms"])
	// error is null for passed steps.
	assert.Nil(t, step0["error"])

	step1 := steps[1].(map[string]interface{})
	assert.Equal(t, "sip_register", step1["id"])
	assert.Equal(t, "failed", step1["status"])
	require.NotNil(t, step1["error"])
	assert.True(t, strings.Contains(step1["error"].(string), "assertion failed"))
}

// allReporters is the table every cross-reporter test ranges over, so a
// subtest failure names the reporter whose error precedence diverged.
func allReporters() []struct {
	name string
	new  func(w *bytes.Buffer) reporter.Reporter
} {
	return []struct {
		name string
		new  func(w *bytes.Buffer) reporter.Reporter
	}{
		{"terminal", func(w *bytes.Buffer) reporter.Reporter { return reporter.NewTerminalReporter(w, false) }},
		{"json", func(*bytes.Buffer) reporter.Reporter { return &reporter.JSONReporter{} }},
		{"junit", func(*bytes.Buffer) reporter.Reporter { return &reporter.JUnitReporter{} }},
	}
}

// renderFailedStep renders one failed step through a reporter and returns the
// raw output, so one assertion shape covers text, JSON and XML alike.
func renderFailedStep(t *testing.T, rep reporter.Reporter, buf *bytes.Buffer, resultErr string, stepErr error) string {
	t.Helper()
	results := []*engine.RunResult{{
		ScenarioName: "precedence",
		Status:       common.StatusFailed,
		Steps: []engine.StepReport{{
			StepID: "s", Protocol: "sip", Action: "send",
			Result: &common.StepResult{Status: common.StatusFailed, Error: resultErr},
			Error:  stepErr,
		}},
	}}
	require.NoError(t, rep.Report(buf, results))
	return buf.String()
}

// TestReporters_KeepTransportErrorWhenAssertionAlsoFailed pins that a send
// failure (Result.Error) is not overwritten by the generic assertion failure
// that followed it (StepReport.Error) in any of the three reporters.
func TestReporters_KeepTransportErrorWhenAssertionAlsoFailed(t *testing.T) {
	const transport = `send failed: dial tls "127.0.0.2:15061": tls: failed to verify certificate: x509: certificate is valid for 127.0.0.1, not 127.0.0.2`
	assertion := errors.New(`assertion "response.status_code == 200": "response.status_code" is not present`)
	for _, rep := range allReporters() {
		t.Run(rep.name, func(t *testing.T) {
			var buf bytes.Buffer
			out := renderFailedStep(t, rep.new(&buf), &buf, transport, assertion)
			assert.Contains(t, out, "x509: certificate is valid for 127.0.0.1")
			assert.NotContains(t, out, "is not present")
		})
	}
}

// TestReporters_InfraErrorWithoutResultErrorStillReported is the control for
// the fallback branch: without it the fix could drop StepReport.Error and pass.
func TestReporters_InfraErrorWithoutResultErrorStillReported(t *testing.T) {
	for _, rep := range allReporters() {
		t.Run(rep.name, func(t *testing.T) {
			var buf bytes.Buffer
			out := renderFailedStep(t, rep.new(&buf), &buf, "", errors.New("dial tcp: connection refused"))
			assert.Contains(t, out, "connection refused")
		})
	}
}

func TestJSONReporter_RendersImpairDelayMs(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, resultsWithImpairDelay(320)))

	assert.Contains(t, buf.String(), `"impair_delay_ms": 320`)

	var buf2 bytes.Buffer
	require.NoError(t, r.Report(&buf2, allPassedResults()))
	assert.NotContains(t, buf2.String(), "impair_delay_ms", "the key must be omitted entirely when there was no impairment")
}

func TestJSONReporter_ErrorNullForPassedSteps(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, allPassedResults()))

	// Verify the raw JSON contains "error": null (not omitted).
	out := buf.String()
	assert.Contains(t, out, `"error": null`)
}

// TestTerminalReporter_AssertionFailurePrintsErrorOnce pins the shape the
// runner actually produces for an assertion failure: ExecuteStep returns the
// mutated result AND an error carrying the same text, so both Result.Error
// and StepReport.Error are set. Two independent ifs printed that once each,
// duplicating every assertion-failure line; the golden file shows it once.
func TestTerminalReporter_AssertionFailurePrintsErrorOnce(t *testing.T) {
	const msg = "assertion failed: response.status_code == 200 (actual: 403)"
	results := []*engine.RunResult{{
		ScenarioName: "dup",
		Status:       common.StatusFailed,
		DurationMs:   10,
		Steps: []engine.StepReport{{
			StepID: "s", Protocol: "sip", Action: "send",
			Result: &common.StepResult{Status: common.StatusFailed, DurationMs: 10, Error: msg},
			Error:  errors.New(msg),
		}},
	}}
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, results))

	assert.Equal(t, 1, strings.Count(buf.String(), "Error:"), "one Error line, not two:\n%s", buf.String())
}

// TestTerminalReporter_InfraErrorWithoutResultErrorStillShown is the control
// for the else-if: an error the result itself does not carry must still be
// printed.
func TestTerminalReporter_InfraErrorWithoutResultErrorStillShown(t *testing.T) {
	results := []*engine.RunResult{{
		ScenarioName: "infra",
		Status:       common.StatusFailed,
		Steps: []engine.StepReport{{
			StepID: "s", Protocol: "sip", Action: "send",
			Result: &common.StepResult{Status: common.StatusFailed},
			Error:  errors.New("dial tcp: connection refused"),
		}},
	}}
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, results))

	assert.Contains(t, buf.String(), "connection refused")
	assert.Equal(t, 1, strings.Count(buf.String(), "Error:"))
}

// TestTerminalReporter_ZeroDurationStillShown pins that a step whose
// duration rounds to 0ms still gets its duration printed. A sub-millisecond
// step against an in-process peer is real, and a line with no duration next
// to lines that have one reads as a rendering bug.
func TestTerminalReporter_ZeroDurationStillShown(t *testing.T) {
	results := []*engine.RunResult{{
		ScenarioName: "fast",
		Status:       common.StatusPassed,
		Steps: []engine.StepReport{{
			StepID: "s", Protocol: "sip", Action: "send",
			Result: &common.StepResult{Status: common.StatusPassed, DurationMs: 0},
		}},
	}}
	var buf bytes.Buffer
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, results))
	assert.Contains(t, buf.String(), "send s (0ms)")
}
