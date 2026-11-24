package main

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAssertCmd_PassingAssertion_ExitsZero(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"result_code": 2001, "server_name": "sip:scscf.ims.local"}`)

	cmd := exec.Command(bin, "assert", "--input", input, "result_code == 2001")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for passing assertion, got: %s", out)
	assert.Contains(t, string(out), "PASS")
}

func TestAssertCmd_FailingAssertion_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"result_code": 2001}`)

	cmd := exec.Command(bin, "assert", "--input", input, "result_code == 9999")
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 for failing assertion")
	assert.Contains(t, string(out), "FAIL")
	assert.Contains(t, string(out), "2001")
}

// TestAssertCmd_MasksPhoneNumbersInOutput pins that the standalone assert
// command masks E.164 numbers exactly as the run reporters do. AssertionError
// embeds the ACTUAL value it compared, so before this an unmasked FAIL line
// put a live MSISDN from the input file straight into a CI log — the one
// output path in the binary that skipped the Masker. The PASS line echoes the
// user's expression and is masked too, since it lands in the same log.
func TestAssertCmd_MasksPhoneNumbersInOutput(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"from_number": "+447700900001", "to_number": "+447700900002"}`)

	// A failing comparison forces the actual value into the FAIL line.
	cmd := exec.Command(bin, "assert", "--input", input,
		"from_number == +447700900099",
		"to_number == +447700900002")
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "the first assertion fails, so the command must exit non-zero")

	s := string(out)
	assert.NotContains(t, s, "447700900001", "the actual value in the FAIL line must be masked")
	assert.NotContains(t, s, "447700900002", "the PASS line's expression must be masked")
	assert.NotContains(t, s, "447700900099", "the expected value in the expression must be masked")
	assert.Contains(t, s, "[REDACTED-")
	assert.Contains(t, s, "FAIL")
	assert.Contains(t, s, "PASS")
}

func TestAssertCmd_NotNullAssertion(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"server_name": "sip:scscf.ims.local"}`)

	cmd := exec.Command(bin, "assert", "--input", input, "server_name != null")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for non-null assertion, got: %s", out)
	assert.Contains(t, string(out), "PASS")
}

func TestAssertCmd_NumericComparison(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"latency_ms": 150}`)

	cmd := exec.Command(bin, "assert", "--input", input, "latency_ms < 500")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for numeric comparison, got: %s", out)
	assert.Contains(t, string(out), "PASS")
}

func TestAssertCmd_MultipleExpressions_MixedResults(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.json", `{"result_code": 2001, "latency_ms": 999}`)

	cmd := exec.Command(bin, "assert", "--input", input,
		"result_code == 2001",
		"latency_ms < 500",
	)
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 when any assertion fails")
	output := string(out)
	assert.Contains(t, output, "PASS: result_code == 2001")
	assert.Contains(t, output, "FAIL")
}

func TestAssertCmd_YAMLInput(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	input := writeFile(t, dir, "input.yaml", "result_code: 2001\nserver_name: sip:scscf.ims.local\n")

	cmd := exec.Command(bin, "assert", "--input", input, "result_code == 2001")
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for YAML input, got: %s", out)
	assert.Contains(t, string(out), "PASS")
}

func TestAssertCmd_MissingInputFile_ExitsTwo(t *testing.T) {
	bin := binaryPath(t)

	cmd := exec.Command(bin, "assert", "--input", "/nonexistent/file.json", "x == 1")
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected failure for missing input file")
	assert.Contains(t, string(out), "nonexistent")
}

func TestAssertCmd_NoExpressions_ShowsError(t *testing.T) {
	bin := binaryPath(t)

	cmd := exec.Command(bin, "assert")
	_, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected error when no expressions provided")
}
