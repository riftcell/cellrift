package main

// loss_reorder_e2e_test.go is the last-mile proof for drop, duplicate and
// reorder. Each is covered by adapter-level tests and by engine-level
// dispatch tests, but neither layer catches the production path failing to
// connect them: action: reorder in particular is dispatched by a runner
// branch that no adapter test exercises, and a scenario using it would fail
// at runtime with an "unsupported action" error that every package-level
// test would still report green. This test runs the real checked-in
// scenario file through runScenario — the same entry point `cellrift run`
// uses — and asserts on what the mock peer actually received.
//
// The critical design point, and the reason this test is worth its length:
// assert on mock.ReceivedRequests(), not only on step results. A step result
// saying dropped == true proves the adapter set a map key. Only the absence
// of that request's Call-ID from the peer's arrival history proves the bytes
// never reached the wire. Likewise, only the arrival ORDER of the two
// reorder sub-steps' Call-IDs proves reordering happened; two passed step
// reports would look identical whether or not send_order was honoured.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Call-IDs are the explicit identity register_loss_reorder.yaml assigns
// each request. Named here once so a future edit to the scenario file only
// has to be reconciled in one place.
const (
	lossReorderDroppedCallID    = "loss-reorder-dropped@lab.local"
	lossReorderDuplicatedCallID = "loss-reorder-duplicated@lab.local"
	lossReorderFirstCallID      = "reorder-first-on-the-wire@lab.local"
	lossReorderSecondCallID     = "reorder-second-on-the-wire@lab.local"
	lossReorderNormalCallID     = "loss-reorder-normal@lab.local"
)

// arrivalsWithCallID returns the mock's recorded arrivals whose Call-ID is
// callID, in arrival order. Call-ID is the identity this scenario assigns
// each request explicitly, which is what makes "this exact request never
// arrived" and "this exact request arrived twice" directly assertable.
func arrivalsWithCallID(reqs []sip.RecordedSIPRequest, callID string) []sip.RecordedSIPRequest {
	var out []sip.RecordedSIPRequest
	for _, r := range reqs {
		if r.CallID == callID {
			out = append(out, r)
		}
	}
	return out
}

// firstArrivalIndex returns the index within reqs of the first arrival whose
// Call-ID is callID, or -1 if it never arrived. Used to compare arrival
// ORDER between two known-unique Call-IDs, which is the only way to prove
// send_order — not declaration order — decided the wire sequence.
func firstArrivalIndex(reqs []sip.RecordedSIPRequest, callID string) int {
	for i, r := range reqs {
		if r.CallID == callID {
			return i
		}
	}
	return -1
}

// TestRunScenario_LossReorderSurvivesProductionWiring runs
// scenarios/ims/register_loss_reorder.yaml through the real cfg.runScenario
// path and asserts every one of drop/duplicate/reorder's claims against the
// mock UAS's own arrival history, not against step results alone.
func TestRunScenario_LossReorderSurvivesProductionWiring(t *testing.T) {
	mock := sip.NewMockUAS(t)
	defer mock.Close()

	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	runStart := time.Now()
	result := cfg.runScenario(filepath.Join(scenarioDir(), "ims", "register_loss_reorder.yaml"))
	elapsed := time.Since(runStart)

	require.NotNil(t, result)
	assert.Less(t, elapsed, 5*time.Second,
		"a regression that made a dropped or reordered step burn its timeout must fail loudly here")
	require.Equal(t, common.StatusPassed, result.Status)

	// Sleep a short settle window before reading the mock's arrival history.
	// This is what makes the NEGATIVE assertion below (the dropped request
	// never arrived) meaningful rather than merely fast: without a settle
	// window, "not observed yet" and "will never arrive" look identical.
	time.Sleep(200 * time.Millisecond)
	reqs := mock.ReceivedRequests()

	dropped := findStep(t, result, "register_dropped")
	require.NotNil(t, dropped.Result)
	assert.Equal(t, common.StatusPassed, dropped.Result.Status)
	assert.Equal(t, true, dropped.Result.Response[impair.DroppedKey])
	assert.Empty(t, arrivalsWithCallID(reqs, lossReorderDroppedCallID),
		"the dropped step's Call-ID must never appear in the mock's arrival history")

	duplicated := findStep(t, result, "register_duplicated")
	require.NotNil(t, duplicated.Result)
	assert.Equal(t, common.StatusPassed, duplicated.Result.Status)
	assert.Equal(t, true, duplicated.Result.Response[impair.DuplicatedKey])
	dupArrivals := arrivalsWithCallID(reqs, lossReorderDuplicatedCallID)
	require.Len(t, dupArrivals, 2, "the duplicated step's Call-ID must arrive exactly twice")
	assert.True(t, bytes.Equal(dupArrivals[0].Raw, dupArrivals[1].Raw),
		"a duplicated request's two arrivals must be byte-identical")

	// The reorder group's two sub-steps come back as two separate StepReports
	// in DECLARATION order — the order the YAML lists them in — even though
	// they were written to the wire in the reverse order.
	second := findStep(t, result, "register_second_on_the_wire")
	require.NotNil(t, second.Result)
	assert.Equal(t, common.StatusPassed, second.Result.Status, "error: %v", second.Result.Error)
	first := findStep(t, result, "register_first_on_the_wire")
	require.NotNil(t, first.Result)
	assert.Equal(t, common.StatusPassed, first.Result.Status, "error: %v", first.Result.Error)

	firstArrivalIdx := firstArrivalIndex(reqs, lossReorderFirstCallID)
	secondArrivalIdx := firstArrivalIndex(reqs, lossReorderSecondCallID)
	require.NotEqual(t, -1, firstArrivalIdx, "the send_order:1 member must have reached the wire")
	require.NotEqual(t, -1, secondArrivalIdx, "the send_order:2 member must have reached the wire")
	assert.Less(t, firstArrivalIdx, secondArrivalIdx,
		"the send_order:1 member (declared SECOND in the YAML) must arrive before the send_order:2 member (declared FIRST) — this is send_order, not declaration order, deciding wire arrival")

	normal := findStep(t, result, "register_normal")
	require.NotNil(t, normal.Result)
	assert.Equal(t, common.StatusPassed, normal.Result.Status)
	_, hasDropped := normal.Result.Response[impair.DroppedKey]
	assert.False(t, hasDropped, "the unimpaired step must not carry response.dropped")
	_, hasDuplicated := normal.Result.Response[impair.DuplicatedKey]
	assert.False(t, hasDuplicated, "the unimpaired step must not carry response.duplicated")
	_, hasDelay := normal.Result.Response[impair.ImpairDelayKey]
	assert.False(t, hasDelay, "the unimpaired step must not carry response.impair_delay_ms")

	assert.NotEmpty(t, arrivalsWithCallID(reqs, lossReorderNormalCallID),
		"the final unimpaired step must still reach the wire")
}

// swapReorderSendOrder returns a copy of register_loss_reorder.yaml's raw
// text with its two send_order values exchanged, via a placeholder swap so
// neither replacement collides with the other. It is a string-level
// transform, deliberately not a YAML re-encode, so nothing else in the file
// (including the two Call-ID values used to detect the swap) is touched.
func swapReorderSendOrder(raw string) string {
	const placeholder = "__CELLRIFT_TEST_SEND_ORDER_PLACEHOLDER__"
	swapped := strings.ReplaceAll(raw, "send_order: 1", placeholder)
	swapped = strings.ReplaceAll(swapped, "send_order: 2", "send_order: 1")
	return strings.ReplaceAll(swapped, placeholder, "send_order: 2")
}

// TestRunScenario_LossReorder_SendOrderAssertionDetectsInversion is the
// mutation test for the send-order assertion above: an order assertion that
// holds under both orderings is not an order assertion. It writes a copy of
// register_loss_reorder.yaml with the two reorder sub-steps' send_order
// values swapped, runs it against a fresh mock, and confirms the arrival
// order inverts — register_second_on_the_wire's Call-ID (now send_order:1)
// arrives before register_first_on_the_wire's (now send_order:2).
func TestRunScenario_LossReorder_SendOrderAssertionDetectsInversion(t *testing.T) {
	original, err := os.ReadFile(filepath.Join(scenarioDir(), "ims", "register_loss_reorder.yaml"))
	require.NoError(t, err)

	swapped := swapReorderSendOrder(string(original))
	require.NotEqual(t, string(original), swapped, "the swap must actually change the file")

	path := filepath.Join(t.TempDir(), "register_loss_reorder_swapped.yaml")
	require.NoError(t, os.WriteFile(path, []byte(swapped), 0o644))

	mock := sip.NewMockUAS(t)
	defer mock.Close()
	cfg := &runConfig{timeout: 5 * time.Second, targetHost: mock.Addr()}

	result := cfg.runScenario(path)
	require.NotNil(t, result)
	require.Equal(t, common.StatusPassed, result.Status)

	time.Sleep(200 * time.Millisecond)
	reqs := mock.ReceivedRequests()

	firstArrivalIdx := firstArrivalIndex(reqs, lossReorderFirstCallID)
	secondArrivalIdx := firstArrivalIndex(reqs, lossReorderSecondCallID)
	require.NotEqual(t, -1, firstArrivalIdx)
	require.NotEqual(t, -1, secondArrivalIdx)
	assert.Greater(t, firstArrivalIdx, secondArrivalIdx,
		"with send_order swapped, register_second_on_the_wire (now send_order:1) must arrive BEFORE register_first_on_the_wire (now send_order:2) — the inverse of the unmutated scenario's own assertion")
}
