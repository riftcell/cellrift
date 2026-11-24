package main

// status_gate_e2e_test.go is the production-path proof for the SIP status
// gate. The adapter-level tests in internal/protocols/sip/status_gate_test.go
// prove finishSend now fails a non-2xx; this file proves that failure
// survives cmd/cellrift/main.go's own wiring all the way to the run result a
// CI job's exit code is derived from. It exists because the original defect
// was only ever visible at exactly this level: every adapter test passed while
// `cellrift run` printed "1/1 steps passed" and exited 0 against a peer
// answering 500 Server Internal Error.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const errorPeerScenario = `version: "1.0"
name: status-gate-e2e
description: a single SIP step with no assert block, against a peer that answers 500
protocols:
  sip:
    transport: udp
variables:
  peer: "peer.lab.local:5060"
steps:
  - id: options_to_broken_peer
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    fields:
      From: "<sip:447700900001@ims.local>;tag=e2e001"
      To: "<sip:ims.local>"
`

// TestRunScenario_NonSuccessFinalFailsTheRun drives a scenario with NO
// assertions — the case that used to be a guaranteed false pass — through
// cfg.runScenario against a mock forced to answer 500, and requires the run
// to fail. A scenario author who writes no assertions must still get a red
// build when the peer errors.
func TestRunScenario_NonSuccessFinalFailsTheRun(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()
	uas.SetResponder(sip.ErrorResponder(500, "Server Internal Error"))

	path := filepath.Join(t.TempDir(), "status_gate.yaml")
	require.NoError(t, os.WriteFile(path, []byte(errorPeerScenario), 0o600))

	cfg := &runConfig{
		timeout: 2 * time.Second,
		vars:    []string{"peer=" + uas.Addr()},
	}
	result := cfg.runScenario(path)
	require.NotNil(t, result)

	assert.Equal(t, common.StatusFailed, result.Status,
		"a 500 from the peer must fail the run even with no assert: block")

	step := findStep(t, result, "options_to_broken_peer")
	require.NotNil(t, step.Result)
	assert.Equal(t, common.StatusFailed, step.Result.Status)
	assert.Contains(t, step.Result.Error, "500")
	assert.Equal(t, 500, step.Result.Response["status_code"],
		"the real status is still captured for response.status_code assertions")
}

// TestRunScenario_SuccessFinalStillPassesTheRun is the control for the test
// above, using the identical scenario against the mock's default success
// responses: the gate must fail only genuine errors, never a healthy 200.
func TestRunScenario_SuccessFinalStillPassesTheRun(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()

	path := filepath.Join(t.TempDir(), "status_gate.yaml")
	require.NoError(t, os.WriteFile(path, []byte(errorPeerScenario), 0o600))

	cfg := &runConfig{
		timeout: 2 * time.Second,
		vars:    []string{"peer=" + uas.Addr()},
	}
	result := cfg.runScenario(path)
	require.NotNil(t, result)
	assert.Equal(t, common.StatusPassed, result.Status)
}
