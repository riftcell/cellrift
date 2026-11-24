package reporter_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/reporter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateGolden = flag.Bool("update-golden", false, "regenerate golden files in docs/examples/")

// goldenDir returns the path to docs/examples/ relative to this test file.
func goldenDir() string {
	return filepath.Join("..", "..", "docs", "examples")
}

// goldenResults returns a realistic multi-scenario result set with both
// passed and failed steps, suitable for demonstrating all output formats.
func goldenResults() []*engine.RunResult {
	return []*engine.RunResult{
		{
			ScenarioName: "VoLTE basic registration",
			Status:       common.StatusFailed,
			DurationMs:   412,
			Steps: []engine.StepReport{
				{
					StepID:   "diameter_auth",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 23,
					},
				},
				{
					StepID:   "sip_register",
					Protocol: "sip",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusFailed,
						DurationMs: 384,
						Error:      "assertion failed: response.status_code == 200 (actual: 403)",
					},
				},
				{
					StepID:   "verify_registration",
					Protocol: "sip",
					Action:   "assert_state",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 5,
					},
				},
			},
		},
		{
			ScenarioName: "Diameter UAR/UAA",
			Status:       common.StatusPassed,
			DurationMs:   47,
			Steps: []engine.StepReport{
				{
					StepID:   "send_uar",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 18,
					},
				},
				{
					StepID:   "uar_repeat",
					Protocol: "diameter",
					Action:   "send",
					Result: &common.StepResult{
						Status:     common.StatusPassed,
						DurationMs: 22,
					},
				},
			},
		},
	}
}

func TestGolden_Terminal(t *testing.T) {
	var buf bytes.Buffer
	// Use non-TTY writer so output is plain text (no ANSI codes).
	r := reporter.NewTerminalReporter(&buf, false)
	require.NoError(t, r.Report(&buf, goldenResults()))

	golden := filepath.Join(goldenDir(), "terminal.txt")
	checkOrUpdateGolden(t, golden, buf.Bytes())
}

func TestGolden_JUnit(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JUnitReporter{}
	require.NoError(t, r.Report(&buf, goldenResults()))

	golden := filepath.Join(goldenDir(), "junit.xml")
	checkOrUpdateGolden(t, golden, buf.Bytes())
}

func TestGolden_JSON(t *testing.T) {
	var buf bytes.Buffer
	r := &reporter.JSONReporter{}
	require.NoError(t, r.Report(&buf, goldenResults()))

	golden := filepath.Join(goldenDir(), "report.json")
	checkOrUpdateGolden(t, golden, buf.Bytes())
}

// checkOrUpdateGolden compares actual output against a golden file. If
// -update-golden is set, it writes the file instead. This keeps the
// sample outputs in docs/examples/ in sync with the reporter code.
func checkOrUpdateGolden(t *testing.T, path string, actual []byte) {
	t.Helper()

	if *updateGolden {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, actual, 0o644))
		t.Logf("updated golden file: %s", path)
		return
	}

	expected, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		t.Fatalf("golden file %s does not exist; run with -update-golden to create it", path)
	}
	require.NoError(t, err)
	assert.Equal(t, string(expected), string(actual),
		"golden file %s is stale; run `go test ./internal/reporter/ -update-golden` to regenerate", path)
}
