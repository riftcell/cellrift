package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// binaryPath builds the cellrift binary once and returns its path.
func binaryPath(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "cellrift")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = filepath.Join("..", "..", "cmd", "cellrift")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build failed: %s", out)
	return bin
}

func scenarioDir() string {
	return filepath.Join("..", "..", "scenarios")
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	return path
}

func TestValidateCmd_ValidFile_ExitsZero(t *testing.T) {
	bin := binaryPath(t)
	path := filepath.Join(scenarioDir(), "ims", "register_basic.yaml")

	cmd := exec.Command(bin, "validate", path)
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for valid file, got: %s", out)
	assert.Contains(t, string(out), path+": OK", "a valid file must be confirmed, not silently accepted")
}

func TestValidateCmd_MultipleValidFiles_ExitsZero(t *testing.T) {
	bin := binaryPath(t)
	cmd := exec.Command(bin, "validate",
		filepath.Join(scenarioDir(), "ims", "register_basic.yaml"),
		filepath.Join(scenarioDir(), "diameter", "uar_uaa.yaml"),
	)
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "expected exit 0 for valid files, got: %s", out)
}

func TestValidateCmd_InvalidFile_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", `
version: "1.0"
steps:
  - id: dup
    protocol: gtp
    action: send
  - id: dup
    protocol: sip
    action: send
`)

	cmd := exec.Command(bin, "validate", bad)
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 for invalid file")
	output := string(out)
	assert.Contains(t, output, "name")
	assert.Contains(t, output, "gtp")
	assert.Contains(t, output, "duplicate")
}

func TestValidateCmd_MissingFile_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	cmd := exec.Command(bin, "validate", "/nonexistent/path.yaml")
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 for missing file")
	assert.Contains(t, string(out), "nonexistent")
}

func TestValidateCmd_MixedValidAndInvalid_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", `
steps:
  - id: x
    protocol: sip
    action: send
`)
	good := filepath.Join(scenarioDir(), "ims", "register_basic.yaml")

	cmd := exec.Command(bin, "validate", good, bad)
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 when any file fails")
	output := string(out)
	// Should report errors for bad.yaml but not for the valid file.
	assert.Contains(t, output, "bad.yaml")
}

func TestValidateCmd_MalformedYAML_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	bad := writeFile(t, dir, "malformed.yaml", `
name: test
steps:
  - id: [broken
`)

	cmd := exec.Command(bin, "validate", bad)
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 for malformed YAML")
	assert.Contains(t, string(out), "malformed.yaml")
}

func TestValidateCmd_UndefinedVariable_ExitsOne(t *testing.T) {
	bin := binaryPath(t)
	dir := t.TempDir()
	bad := writeFile(t, dir, "undef_var.yaml", `
name: test
version: "1.0"
protocols:
  sip:
    transport: udp
steps:
  - id: s1
    protocol: sip
    action: send
    target: "{{ mystery_host }}:5060"
    message: REGISTER
`)

	cmd := exec.Command(bin, "validate", bad)
	out, err := cmd.CombinedOutput()
	assert.Error(t, err, "expected exit 1 for undefined variable")
	assert.Contains(t, string(out), "mystery_host")
}

// TestValidateCmd_ConfirmsEachValidFile inverts the previous expectation of
// no output on success. A silent exit 0 was indistinguishable from "nothing
// ran", and validate is the first command in the quickstart; each valid
// file is now confirmed on its own line, on stdout, with nothing on stderr.
func TestValidateCmd_ConfirmsEachValidFile(t *testing.T) {
	bin := binaryPath(t)
	a := filepath.Join(scenarioDir(), "diameter", "uar_uaa.yaml")
	b := filepath.Join(scenarioDir(), "ims", "basic_call.yaml")
	cmd := exec.Command(bin, "validate", a, b)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	require.NoError(t, cmd.Run(), "stderr: %s", stderr.String())
	assert.Contains(t, stdout.String(), a+": OK")
	assert.Contains(t, stdout.String(), b+": OK")
	assert.Empty(t, stderr.String(), "a clean validation must write nothing to stderr")
}
