package main

// conn_fault_e2e_test.go is the last-mile proof for reset, truncate and
// abrupt_close. Each is covered by adapter-level tests, but no adapter test
// touches cmd/cellrift/main.go's own wiring — and two production-only defects have
// already been found exactly there: adapters constructed
// without impair.Wrap, and --target-host failing to recurse into sub-steps.
// A connection fault is especially exposed to that class of bug, because
// impair.Wrap is what carries ConnFaultInjector through to the real socket:
// an unwrapped transport would still satisfy the interface and still fault,
// so nothing below the production path would notice the wrapper was missing.
//
// The assertions therefore target the mock peer's own RawReads(), not step
// result keys. A result saying connection_state == "truncated" proves the
// adapter set a map key. Only an 8-byte entry in the peer's read history
// proves fewer bytes actually reached the wire.

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Call-IDs are the explicit identity register_conn_fault.yaml assigns each
// request. Named here once so a future edit to the scenario file only has to
// be reconciled in one place.
const (
	connFaultTruncatedCallID   = "conn-fault-truncated@lab.local"
	connFaultResetCallID       = "conn-fault-reset@lab.local"
	connFaultAbruptCloseCallID = "conn-fault-abrupt-close@lab.local"
	connFaultNormalCallID      = "conn-fault-normal@lab.local"
)

// readsContaining returns the mock's raw reads whose bytes contain callID.
// TCPMockUASServer records pre-parse bytes, so this works even for a read
// too short to be a SIP message at all — which is exactly the case the
// truncation assertion below depends on.
func readsContaining(reads [][]byte, callID string) [][]byte {
	var out [][]byte
	needle := []byte(callID)
	for _, r := range reads {
		if bytes.Contains(r, needle) {
			out = append(out, r)
		}
	}
	return out
}

// readsOfLength returns the mock's raw reads whose length is exactly n.
func readsOfLength(reads [][]byte, n int) [][]byte {
	var out [][]byte
	for _, r := range reads {
		if len(r) == n {
			out = append(out, r)
		}
	}
	return out
}

// TestRunScenario_ConnectionFaultsSurviveProductionWiring runs
// scenarios/ims/register_conn_fault.yaml through the real cfg.runScenario
// path and asserts every one of truncate/reset/abrupt_close's claims
// against the mock peer's own arrival history, not against step results
// alone.
func TestRunScenario_ConnectionFaultsSurviveProductionWiring(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	runStart := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_conn_fault.yaml"))
	elapsed := time.Since(runStart)

	require.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second,
		"a faulted step that ends up waiting for a response it can never receive must fail loudly here")
	assert.Equal(t, common.StatusFailed, result.Status,
		"three of this scenario's four steps destroy their own connection and report failed by design; "+
			"the run-level status reflects that, and the per-step assertions below are what actually matter")

	// Settle window before reading arrival history, so "not observed yet"
	// and "will never arrive" are distinguishable.
	time.Sleep(200 * time.Millisecond)
	reads := mock.RawReads()

	truncated := findStep(t, result, "register_truncated")
	require.NotNil(t, truncated.Result)
	assert.Equal(t, common.StatusFailed, truncated.Result.Status)
	assert.Equal(t, impair.ConnStateTruncated, truncated.Result.Response[impair.ConnectionStateKey])
	truncatedLenMatches := readsOfLength(reads, impair.TruncatedBytes)
	require.Len(t, truncatedLenMatches, 1,
		"exactly one raw read of TruncatedBytes length must exist — the truncated step's own short write")
	assert.Empty(t, readsContaining(reads, connFaultTruncatedCallID),
		"a truncated send is too short to carry its own Call-ID — the peer never saw the whole request")

	reset := findStep(t, result, "register_reset")
	require.NotNil(t, reset.Result)
	assert.Equal(t, common.StatusFailed, reset.Result.Status)
	assert.Equal(t, impair.ConnStateReset, reset.Result.Response[impair.ConnectionStateKey])
	resetArrivals := readsContaining(reads, connFaultResetCallID)
	require.Len(t, resetArrivals, 1,
		"the peer DID receive the whole request before the connection died")

	abruptClose := findStep(t, result, "register_abrupt_close")
	require.NotNil(t, abruptClose.Result)
	assert.Equal(t, common.StatusFailed, abruptClose.Result.Status)
	assert.Equal(t, impair.ConnStateClosed, abruptClose.Result.Response[impair.ConnectionStateKey])
	abruptCloseArrivals := readsContaining(reads, connFaultAbruptCloseCallID)
	require.Len(t, abruptCloseArrivals, 1,
		"the peer DID receive the whole request before the connection died")

	normal := findStep(t, result, "register_normal")
	require.NotNil(t, normal.Result)
	assert.Equal(t, common.StatusPassed, normal.Result.Status, "error: %v", normal.Result.Error)
	normalArrivals := readsContaining(reads, connFaultNormalCallID)
	require.Len(t, normalArrivals, 1, "the run recovered and the final step reached the wire")

	// Deliberately NOT asserted: the mock's own recorded transport errors.
	// With TCPFaultNone the mock answers every request it parses, and for
	// the reset/abrupt_close steps it writes that answer into a connection
	// the client has already destroyed — a legitimate mock-side write
	// failure that is a consequence of the fault, not a defect. Asserting
	// that error list is empty here would fail for the wrong reason.
}

// TestRunScenario_ConnFaultAssertionsActuallyRunAndPass pins that the three
// `response.connection_state == "..."` assertions in register_conn_fault.yaml
// execute and pass through the production path. They were dead code for the
// scenario's whole life: assertion evaluation was gated on StatusPassed, so a
// fault step's assertions never ran — and had they run, their quoted
// right-hand sides (`"truncated"`) would have failed against the unquoted
// value. The test above cannot tell the difference: it checks
// connection_state via Response, which is right whether or not the
// scenario's own assert: line ever executed. This test checks the one thing
// that distinguishes a live, passing assertion from a dead or failing one —
// a failing assertion is appended to the step's Error.
func TestRunScenario_ConnFaultAssertionsActuallyRunAndPass(t *testing.T) {
	mock := sip.NewTCPMockUAS(t, sip.TCPFaultNone)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_conn_fault.yaml"))
	require.NotNil(t, result)

	for _, id := range []string{"register_truncated", "register_reset", "register_abrupt_close"} {
		step := findStep(t, result, id)
		require.NotNil(t, step.Result)
		assert.Equal(t, common.StatusFailed, step.Result.Status, "%s: the fault itself still fails the step", id)
		assert.NotContains(t, step.Result.Error, "assertion failed",
			"%s: the scenario's connection_state assertion must have run AND passed — a quoting or gating regression would append a failure here", id)
		assert.NotContains(t, step.Result.Error, "is not present",
			"%s: connection_state must be present on a faulted step for its assertion to evaluate", id)
	}
}
