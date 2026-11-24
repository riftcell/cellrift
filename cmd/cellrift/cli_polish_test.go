package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplayCmd_StubExitsNonZero pins that the unimplemented replay command
// fails loudly. It used to print "not yet implemented" and exit 0, so a CI
// step that invoked it succeeded while doing nothing at all. It is also
// hidden from --help so it is not mistaken for a shipped feature.
func TestReplayCmd_StubExitsNonZero(t *testing.T) {
	bin := binaryPath(t)
	out, err := exec.Command(bin, "replay", "--protocol", "sip", "capture.pcap").CombinedOutput()
	require.Error(t, err, "an unimplemented command must not exit 0")
	assert.Contains(t, string(out), "not yet implemented")

	help, _ := exec.Command(bin, "--help").CombinedOutput()
	assert.NotContains(t, string(help), "replay", "a stub must not be advertised in --help")
}

// TestValidateCmd_StrictFlagRemoved pins that the dead --strict flag is gone.
// It was declared, documented, and never read; strictness is now
// unconditional (unknown keys, absent assertion operands and undeclared
// protocols are all rejected), so a flag promising it would only imply its
// absence meant something looser.
func TestValidateCmd_StrictFlagRemoved(t *testing.T) {
	bin := binaryPath(t)
	path := filepath.Join(scenarioDir(), "ims", "basic_call.yaml")
	out, err := exec.Command(bin, "validate", "--strict", path).CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(out), "unknown flag: --strict")
}

// TestRunCmd_NoMaskFlagDisablesMasking pins --no-mask through the real
// binary: against your own lab gear the masked From/To in a --verbose dump
// hid the very MSISDN under test, with no way to turn it off. Masking stays
// the default; the flag is an explicit per-run opt-out.
func TestRunCmd_NoMaskFlagDisablesMasking(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()

	scenario := `version: "1.0"
name: nomask
protocols:
  sip:
    transport: udp
variables:
  peer: "peer.lab.local:5060"
steps:
  - id: opts
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    fields:
      From: "<sip:447700900123@ims.local>;tag=nm1"
      To: "<sip:ims.local>"
`
	path := filepath.Join(t.TempDir(), "nomask.yaml")
	require.NoError(t, os.WriteFile(path, []byte(scenario), 0o600))
	bin := binaryPath(t)
	common := []string{"run", "--verbose", "--var", "peer=" + uas.Addr(), "--timeout", "2000", path}

	masked, err := exec.Command(bin, common...).CombinedOutput()
	require.NoError(t, err, "%s", masked)
	assert.NotContains(t, string(masked), "447700900123", "default output must mask the MSISDN")
	assert.Contains(t, string(masked), "[REDACTED-")

	raw, err := exec.Command(bin, append([]string{"run", "--no-mask"}, common[1:]...)...).CombinedOutput()
	require.NoError(t, err, "%s", raw)
	assert.Contains(t, string(raw), "447700900123", "--no-mask must show the real number")
	assert.NotContains(t, string(raw), "[REDACTED-")

}
