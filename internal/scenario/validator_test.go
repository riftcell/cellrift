package scenario_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidate_ImpairAcceptsDropAndDuplicateSeparately pins that the two new
// delivery-impairment keys validate clean on their own, each on action: send
// — the pre-existing send-only gate in validateImpair covers them for free,
// but that fact is assumed nowhere else and must be pinned by a test.
func TestValidate_ImpairAcceptsDropAndDuplicateSeparately(t *testing.T) {
	cases := []struct {
		name string
		spec *scenario.ImpairSpec
	}{
		{"drop", &scenario.ImpairSpec{Drop: true}},
		{"duplicate", &scenario.ImpairSpec{Duplicate: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validScenario()
			s.Steps[0].Impair = tc.spec
			errs := scenario.Validate(s)
			assert.Empty(t, errs)
		})
	}
}

// TestValidate_ImpairRejectsDropAndDuplicateTogether pins the mutual-
// exclusion rule: a step cannot declare both drop and duplicate, since a
// message that is never sent cannot also be sent twice.
func TestValidate_ImpairRejectsDropAndDuplicateTogether(t *testing.T) {
	s := validScenario()
	s.Steps[0].Impair = &scenario.ImpairSpec{Drop: true, Duplicate: true}
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "impair", errs[0].Field)
	assert.Equal(t, "drop and duplicate are mutually exclusive: a message that is never sent cannot be sent twice", errs[0].Message)
}

// TestValidate_ImpairDropRejectedInParallelSubStep pins that
// validateImpairTree's existing one-level descent into a parallel group's
// sub-steps applies to the new drop/duplicate keys too, not just the
// pre-existing millisecond keys — without this test, deleting the descent
// would only be caught for the timing keys.
func TestValidate_ImpairDropRejectedInParallelSubStep(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{
			ID:     "parallel_group",
			Action: "parallel",
			Steps: []scenario.Step{
				{ID: "pause", Action: "wait", Duration: 500, Impair: &scenario.ImpairSpec{Drop: true}},
			},
		},
	}
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "impair", errs[0].Field)
	assert.Contains(t, errs[0].Message, "send")
	assert.Contains(t, errs[0].Message, "wait")
}

// TestValidate_ImpairRejectsNegative_NoRegression re-pins that the key-set
// refactor (impairFieldValues -> ImpairKeySet plus impairRangeErrors) left
// the pre-existing negative-value rejection for the three timing keys
// unchanged.
func TestValidate_ImpairRejectsNegative_NoRegression(t *testing.T) {
	cases := []struct {
		name  string
		spec  *scenario.ImpairSpec
		field string
	}{
		{"latency_ms", &scenario.ImpairSpec{LatencyMs: -1}, "impair.latency_ms"},
		{"jitter_ms", &scenario.ImpairSpec{JitterMs: -1}, "impair.jitter_ms"},
		{"delay_ms", &scenario.ImpairSpec{DelayMs: -1}, "impair.delay_ms"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validScenario()
			s.Steps[0].Impair = tc.spec
			errs := scenario.Validate(s)
			assertHasFieldError(t, errs, tc.field)
		})
	}
}

// ── action: reorder ──────────────────────────────────────────────────────

// reorderScenario builds a scenario whose sole step is a well-formed
// two-member reorder group (same protocol, send_order 1 and 2), so each test
// below only needs to mutate the one property it is pinning.
func reorderScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:    "Test reorder scenario",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip": {Transport: "udp"},
		},
		Steps: []scenario.Step{
			{
				ID:     "reorder_group",
				Action: "reorder",
				Steps: []scenario.Step{
					{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
					{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
				},
			},
		},
	}
}

func TestValidate_ReorderWellFormedGroupValidatesClean(t *testing.T) {
	errs := scenario.Validate(reorderScenario())
	assert.Empty(t, errs)
}

func TestValidate_ReorderRejectsFewerThanTwoSubSteps(t *testing.T) {
	s := reorderScenario()
	s.Steps[0].Steps = s.Steps[0].Steps[:1]
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	var found bool
	for _, e := range errs {
		if e.Field == "steps" && e.Message == "reorder step requires at least 2 sub-steps" {
			found = true
		}
	}
	assert.True(t, found, "expected the exact 'at least 2 sub-steps' error, got: %v", errs)
}

func TestValidate_ReorderRejectsNonSendSubStep(t *testing.T) {
	s := reorderScenario()
	s.Steps[0].Steps[1].Action = "assert_state"
	errs := scenario.Validate(s)
	assertMessageContains(t, errs, "reorder sub-steps must use action: send")
}

func TestValidate_ReorderRejectsMixedProtocols(t *testing.T) {
	s := reorderScenario()
	s.Protocols["diameter"] = &scenario.ProtocolConfig{Transport: "tcp"}
	s.Steps[0].Steps[1].Protocol = "diameter"
	s.Steps[0].Steps[1].Message = "UAR"
	errs := scenario.Validate(s)
	assertMessageContains(t, errs, "reorder sub-steps must all use the same protocol")
}

func TestValidate_ReorderRejectsNonPermutationSendOrder(t *testing.T) {
	cases := []struct {
		name   string
		orders []int
	}{
		{"gap", []int{1, 3}},
		{"duplicate", []int{1, 1}},
		{"missing", []int{0, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := reorderScenario()
			s.Steps[0].Steps[0].SendOrder = tc.orders[0]
			s.Steps[0].Steps[1].SendOrder = tc.orders[1]
			errs := scenario.Validate(s)
			assertMessageContains(t, errs, "send_order values must be a permutation of 1..2")
		})
	}
}

// TestValidate_SendOrderRejectedOutsideReorder pins that send_order is
// meaningless (and rejected) both on a bare top-level step and on a
// sub-step of a parallel group — the same "declared but never read" defect
// class impair: on a wait step already guards against.
func TestValidate_SendOrderRejectedOutsideReorder(t *testing.T) {
	t.Run("top-level step", func(t *testing.T) {
		s := validScenario()
		s.Steps[0].SendOrder = 1
		errs := scenario.Validate(s)
		require.NotEmpty(t, errs)
		found := false
		for _, e := range errs {
			if e.Field == "send_order" && e.Message == "send_order is only valid on a reorder sub-step" {
				found = true
			}
		}
		assert.True(t, found, "expected the exact send_order-outside-reorder error, got: %v", errs)
	})

	t.Run("parallel sub-step", func(t *testing.T) {
		s := validScenario()
		s.Steps = []scenario.Step{
			{
				ID:     "parallel_group",
				Action: "parallel",
				Steps: []scenario.Step{
					{ID: "sip_step", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
					{ID: "sip_step2", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE"},
				},
			},
		}
		errs := scenario.Validate(s)
		assertHasFieldError(t, errs, "send_order")
	})
}

// TestValidate_ReorderCannotNestInsideParallel pins the container-nesting
// rule: a parallel group dispatches each sub-step independently, which is
// exactly what a reorder group must not do.
func TestValidate_ReorderCannotNestInsideParallel(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{
			ID:     "parallel_group",
			Action: "parallel",
			Steps: []scenario.Step{
				{
					ID:     "nested_reorder",
					Action: "reorder",
					Steps: []scenario.Step{
						{ID: "msg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER", SendOrder: 1},
						{ID: "msg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "INVITE", SendOrder: 2},
					},
				},
				{ID: "sip_step2", Protocol: "sip", Action: "send", Target: "h:5060", Message: "BYE"},
			},
		},
	}
	errs := scenario.Validate(s)
	assertMessageContains(t, errs, "reorder cannot be nested inside parallel")
}

// TestValidate_ImpairOnReorderStepItselfRejected pins (rather than assumes)
// that the pre-existing send-only impair: rule already covers the reorder
// container step itself, since its action is "reorder", not "send".
func TestValidate_ImpairOnReorderStepItselfRejected(t *testing.T) {
	s := reorderScenario()
	s.Steps[0].Impair = &scenario.ImpairSpec{LatencyMs: 100}
	errs := scenario.Validate(s)
	assertMessageContains(t, errs, "impair is only valid on action: send")
}

// TestValidate_ImpairDropDuplicateAcceptedOnReorderSubStep pins a
// deliberate design choice: a reorder sub-step MAY also declare drop: true or
// duplicate: true — the composition must work, not be forbidden.
func TestValidate_ImpairDropDuplicateAcceptedOnReorderSubStep(t *testing.T) {
	cases := []struct {
		name string
		spec *scenario.ImpairSpec
	}{
		{"drop", &scenario.ImpairSpec{Drop: true}},
		{"duplicate", &scenario.ImpairSpec{Duplicate: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := reorderScenario()
			s.Steps[0].Steps[1].Impair = tc.spec
			errs := scenario.Validate(s)
			assert.Empty(t, errs)
		})
	}
}

// TestAllowedActions_ContainsReorder pins that the validator's authoritative
// action set now accepts "reorder" — the dispatch audit
// (dispatch_audit_test.go) proves it also has a real dispatch path.
func TestAllowedActions_ContainsReorder(t *testing.T) {
	assert.True(t, scenario.AllowedActions()["reorder"])
}

// assertMessageContains fails unless at least one ValidationError's message
// contains substr — used where the exact field name matters less than the
// wording a scenario author would actually see.
func assertMessageContains(t *testing.T, errs []scenario.ValidationError, substr string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Message, substr) {
			return
		}
	}
	t.Errorf("expected an error message containing %q, got: %v", substr, errs)
}

// TestImpairKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent pins
// ImpairKeys' full accepted key set and its defensive-copy contract, the
// same guarantee AllowedActions/AllowedTransports already make.
func TestImpairKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent(t *testing.T) {
	want := scenario.ImpairKeySet{
		"latency_ms":   true,
		"jitter_ms":    true,
		"delay_ms":     true,
		"drop":         true,
		"duplicate":    true,
		"reset":        true,
		"truncate":     true,
		"abrupt_close": true,
		"sctp_abort":   true,
	}
	got := scenario.ImpairKeys()
	assert.Equal(t, want, got)

	// Mutating the returned map must not affect a later call.
	got["extra"] = true
	again := scenario.ImpairKeys()
	assert.Equal(t, want, again)
}

// ── connection-level fault keys (reset/truncate/abrupt_close/sctp_abort) ────

// impairScenario builds a single-step scenario for one protocol/transport
// pair, minimal enough that only the impair: block itself is under test —
// no fields, no variable references, nothing else validateImpair's callers
// could object to.
func impairScenario(protocol, transport string) *scenario.Scenario {
	cfg := &scenario.ProtocolConfig{}
	if transport != "" {
		cfg.Transport = transport
	}
	target, message := "peer:5060", "REGISTER"
	if protocol == "diameter" {
		target, message = "peer:3868", "UAR"
	}
	return &scenario.Scenario{
		Name:      "Test impair scenario",
		Version:   "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{protocol: cfg},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: protocol, Action: "send", Target: target, Message: message},
		},
	}
}

// TestValidate_ImpairAcceptsConnectionFaultKeysOnSend pins that each of the
// three connection-fault keys with no transport restriction (reset,
// truncate, abrupt_close) validates clean on a send step.
func TestValidate_ImpairAcceptsConnectionFaultKeysOnSend(t *testing.T) {
	cases := []struct {
		name string
		spec *scenario.ImpairSpec
	}{
		{"reset", &scenario.ImpairSpec{Reset: true}},
		{"truncate", &scenario.ImpairSpec{Truncate: true}},
		{"abrupt_close", &scenario.ImpairSpec{AbruptClose: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := impairScenario("sip", "udp")
			s.Steps[0].Impair = tc.spec
			errs := scenario.Validate(s)
			assert.Empty(t, errs)
		})
	}
}

// TestValidate_ImpairFaultKeyRejectedOnNonSendAction pins that a
// connection-fault key is rejected on a non-send action with the same
// pre-existing message drop/duplicate already use.
func TestValidate_ImpairFaultKeyRejectedOnNonSendAction(t *testing.T) {
	s := impairScenario("sip", "udp")
	s.Steps[0].Action = "wait"
	s.Steps[0].Duration = 500
	s.Steps[0].Impair = &scenario.ImpairSpec{Reset: true}
	errs := scenario.Validate(s)
	assertMessageContains(t, errs, "impair is only valid on action: send")
}

// TestValidate_ImpairRejectsTwoFaultKeys pins that any two boolean impair:
// keys together are rejected, naming both in sorted order, whether the pair
// is two connection faults or a delivery key mixed with a connection fault.
func TestValidate_ImpairRejectsTwoFaultKeys(t *testing.T) {
	cases := []struct {
		name string
		spec *scenario.ImpairSpec
		want string
	}{
		{"reset+truncate", &scenario.ImpairSpec{Reset: true, Truncate: true}, "reset, truncate"},
		{"drop+reset", &scenario.ImpairSpec{Drop: true, Reset: true}, "drop, reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := impairScenario("sip", "udp")
			s.Steps[0].Impair = tc.spec
			errs := scenario.Validate(s)
			require.Len(t, errs, 1)
			assert.Equal(t, "impair", errs[0].Field)
			assert.Contains(t, errs[0].Message, tc.want)
		})
	}
}

// TestValidate_ImpairKeepsDropDuplicateMessage re-pins, under the new
// generalized exclusion rule, that drop+duplicate keeps its original,
// more specific wording rather than falling through to the generic
// "at most one delivery or connection-fault key" message.
func TestValidate_ImpairKeepsDropDuplicateMessage(t *testing.T) {
	s := impairScenario("sip", "udp")
	s.Steps[0].Impair = &scenario.ImpairSpec{Drop: true, Duplicate: true}
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "drop and duplicate are mutually exclusive: a message that is never sent cannot be sent twice", errs[0].Message)
}

// TestValidate_SCTPAbortRequiresSCTPTransport pins that sctp_abort is only
// valid on a step whose effective transport is sctp — Diameter's own
// default, but neither SIP's default nor an explicit tcp override on either
// protocol.
func TestValidate_SCTPAbortRequiresSCTPTransport(t *testing.T) {
	cases := []struct {
		name       string
		protocol   string
		transport  string
		wantAccept bool
		wantNamed  string
	}{
		{"diameter unset defaults to sctp", "diameter", "", true, ""},
		{"diameter explicit sctp", "diameter", "sctp", true, ""},
		{"diameter tcp rejected", "diameter", "tcp", false, "tcp"},
		{"sip unset defaults to udp, rejected", "sip", "", false, "udp"},
		{"sip explicit tcp rejected", "sip", "tcp", false, "tcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := impairScenario(tc.protocol, tc.transport)
			s.Steps[0].Impair = &scenario.ImpairSpec{SCTPAbort: true}
			errs := scenario.Validate(s)
			if tc.wantAccept {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1)
			assert.Equal(t, "impair.sctp_abort", errs[0].Field)
			assert.Contains(t, errs[0].Message, tc.wantNamed)
		})
	}
}

// TestValidate_SCTPAbortRejectedInSIPParallelSubStep pins that
// validateImpairTree's descent into a parallel group's sub-steps applies
// the transport rule too, not only the action rule — a send sub-step whose
// own protocol is sip cannot declare sctp_abort even nested inside a
// parallel group.
func TestValidate_SCTPAbortRejectedInSIPParallelSubStep(t *testing.T) {
	s := impairScenario("sip", "udp")
	s.Steps = []scenario.Step{
		{
			ID:     "parallel_group",
			Action: "parallel",
			Steps: []scenario.Step{
				{ID: "sub1", Protocol: "sip", Action: "send", Target: "peer:5060", Message: "REGISTER", Impair: &scenario.ImpairSpec{SCTPAbort: true}},
			},
		},
	}
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "impair.sctp_abort", errs[0].Field)
	assert.Contains(t, errs[0].Message, "udp")
}

// ── malform: ──────────────────────────────────────────────────────────────

// malformDiameterScenario builds a single-step diameter/tcp scenario whose
// sole step is a well-formed send, so each malform: test below only needs
// to attach the one block it is pinning.
func malformDiameterScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "Test malform diameter scenario",
		Version:   "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{"diameter": {Transport: "tcp"}},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "diameter", Action: "send", Target: "peer:3868", Message: "UAR"},
		},
	}
}

// malformSIPScenario builds a single-step sip/udp scenario whose sole step
// is a well-formed send, so each malform: test below only needs to attach
// the one block it is pinning.
func malformSIPScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "Test malform sip scenario",
		Version:   "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{"sip": {Transport: "udp"}},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "sip", Action: "send", Target: "peer:5060", Message: "REGISTER"},
		},
	}
}

func TestValidate_MalformAVPLengthValidatesClean(t *testing.T) {
	s := malformDiameterScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		AVPLength: &scenario.AVPLengthMalform{Name: "Session-Id", Value: 9999},
	}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_MalformMsgLengthValidatesClean(t *testing.T) {
	s := malformDiameterScenario()
	bad := 9999
	s.Steps[0].Malform = &scenario.MalformSpec{MsgLength: &bad}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

func TestValidate_MalformSIPHeaderValidatesClean(t *testing.T) {
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: "overwrite", Value: "x"},
	}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

// TestValidate_MalformRejectedOnNonSendActions pins the action gate: a
// malform: block is meaningless on wait/assert_state/parallel, mirroring
// impair:'s own send-only rule.
func TestValidate_MalformRejectedOnNonSendActions(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*scenario.Scenario)
	}{
		{"wait", func(s *scenario.Scenario) {
			s.Steps[0].Action = "wait"
			s.Steps[0].Duration = 500
		}},
		{"assert_state", func(s *scenario.Scenario) {
			s.Steps[0].Action = "assert_state"
			s.Steps[0].Protocol = "sip"
			s.Protocols["sip"] = &scenario.ProtocolConfig{Transport: "udp"}
			s.Steps[0].Check = "registered"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := malformDiameterScenario()
			bad := 9999
			s.Steps[0].Malform = &scenario.MalformSpec{MsgLength: &bad}
			tc.mutate(s)
			errs := scenario.Validate(s)
			require.Len(t, errs, 1)
			assert.Equal(t, "malform", errs[0].Field)
			assert.Contains(t, errs[0].Message, tc.name)
		})
	}

	t.Run("parallel", func(t *testing.T) {
		s := malformDiameterScenario()
		bad := 9999
		s.Steps = []scenario.Step{
			{
				ID:      "parallel_group",
				Action:  "parallel",
				Malform: &scenario.MalformSpec{MsgLength: &bad},
				Steps: []scenario.Step{
					{ID: "sub1", Protocol: "diameter", Action: "send", Target: "peer:3868", Message: "UAR"},
				},
			},
		}
		errs := scenario.Validate(s)
		require.Len(t, errs, 1)
		assert.Equal(t, "malform", errs[0].Field)
		assert.Contains(t, errs[0].Message, "parallel")
	})
}

// TestValidate_MalformSIPHeaderOnDiameterStepRejected pins the protocol
// gate: sip_header has no meaning on a diameter step.
func TestValidate_MalformSIPHeaderOnDiameterStepRejected(t *testing.T) {
	s := malformDiameterScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: "missing"},
	}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "malform.sip_header")
}

// TestValidate_MalformDiameterKeysOnSIPStepRejected pins the protocol gate
// the other direction: avp_length and msg_length have no meaning on a sip
// step, each reported under its own field.
func TestValidate_MalformDiameterKeysOnSIPStepRejected(t *testing.T) {
	bad := 9999
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		AVPLength: &scenario.AVPLengthMalform{Name: "Session-Id", Value: 9999},
		MsgLength: &bad,
	}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "malform.avp_length")
	assertHasFieldError(t, errs, "malform.msg_length")
}

// TestValidate_MalformAVPLengthUnknownNameRejected pins that avp_length.name
// is checked against the same embedded AVP dictionary validateAVPFields
// already uses, no second name table.
func TestValidate_MalformAVPLengthUnknownNameRejected(t *testing.T) {
	s := malformDiameterScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		AVPLength: &scenario.AVPLengthMalform{Name: "Not-A-Real-AVP", Value: 100},
	}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "malform.avp_length.name")
}

// TestValidate_MalformAVPLengthValueOutOfRangeRejected pins the shared
// 3-byte bound (malform.MaxThreeByteLength) on both ends.
func TestValidate_MalformAVPLengthValueOutOfRangeRejected(t *testing.T) {
	cases := []int{-1, 16777216}
	for _, v := range cases {
		t.Run(fmt.Sprintf("%d", v), func(t *testing.T) {
			s := malformDiameterScenario()
			s.Steps[0].Malform = &scenario.MalformSpec{
				AVPLength: &scenario.AVPLengthMalform{Name: "Session-Id", Value: v},
			}
			errs := scenario.Validate(s)
			assertHasFieldError(t, errs, "malform.avp_length.value")
		})
	}
}

// TestValidate_MalformMsgLengthValueOutOfRangeRejected mirrors the
// avp_length.value bound check for msg_length.
func TestValidate_MalformMsgLengthValueOutOfRangeRejected(t *testing.T) {
	cases := []int{-1, 16777216}
	for _, v := range cases {
		t.Run(fmt.Sprintf("%d", v), func(t *testing.T) {
			s := malformDiameterScenario()
			bad := v
			s.Steps[0].Malform = &scenario.MalformSpec{MsgLength: &bad}
			errs := scenario.Validate(s)
			assertHasFieldError(t, errs, "malform.msg_length")
		})
	}
}

// TestValidate_MalformSIPHeaderUnknownOpRejected pins that an unrecognised
// op is rejected and the error message lists all three accepted ops.
func TestValidate_MalformSIPHeaderUnknownOpRejected(t *testing.T) {
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: "scramble"},
	}
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	var found bool
	for _, e := range errs {
		if e.Field == "malform.sip_header.op" {
			found = true
			assert.Contains(t, e.Message, "duplicate")
			assert.Contains(t, e.Message, "missing")
			assert.Contains(t, e.Message, "overwrite")
		}
	}
	assert.True(t, found, "expected malform.sip_header.op error, got: %v", errs)
}

// TestValidate_MalformSIPHeaderOverwriteRequiresValue pins that op:
// overwrite with an empty value is rejected under its own field.
func TestValidate_MalformSIPHeaderOverwriteRequiresValue(t *testing.T) {
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: "overwrite"},
	}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "malform.sip_header.value")
}

// TestValidate_MalformSIPHeaderEmptyNameRejected pins that an empty
// sip_header.name is rejected under its own field.
func TestValidate_MalformSIPHeaderEmptyNameRejected(t *testing.T) {
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "", Op: "missing"},
	}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "malform.sip_header.name")
}

// TestValidate_MalformRejectedInNonSendParallelSubStep proves
// validateMalformTree's descent into a parallel group's sub-steps by
// mutation: deleting the sub-step loop in validateMalformTree makes this
// test — and only this test — fail (recorded in the plan's summary).
func TestValidate_MalformRejectedInNonSendParallelSubStep(t *testing.T) {
	bad := 9999
	s := malformDiameterScenario()
	s.Steps = []scenario.Step{
		{
			ID:     "parallel_group",
			Action: "parallel",
			Steps: []scenario.Step{
				{ID: "pause", Action: "wait", Duration: 500, Malform: &scenario.MalformSpec{MsgLength: &bad}},
			},
		},
	}
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "malform", errs[0].Field)
	assert.Contains(t, errs[0].Message, "wait")
}

// TestValidate_MalformSIPHeaderValueUndefinedVarRejected pins that
// collectStringFields now includes malform.sip_header.value, so a
// {{ undefined_var }} reference inside it is reported as an undefined
// variable, exactly like any other author-written string field.
func TestValidate_MalformSIPHeaderValueUndefinedVarRejected(t *testing.T) {
	s := malformSIPScenario()
	s.Steps[0].Malform = &scenario.MalformSpec{
		SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: "overwrite", Value: "{{ undefined_var }}"},
	}
	errs := scenario.Validate(s)
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Message, `undefined variable reference "undefined_var"`) {
			found = true
		}
	}
	assert.True(t, found, "expected an undefined variable reference error, got: %v", errs)
}

// TestMalformKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent pins
// MalformKeys' full accepted key set and its defensive-copy contract, the
// same guarantee ImpairKeys already makes.
func TestMalformKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent(t *testing.T) {
	want := scenario.MalformKeySet{
		"avp_length": true,
		"msg_length": true,
		"sip_header": true,
	}
	got := scenario.MalformKeys()
	assert.Equal(t, want, got)

	got["extra"] = true
	again := scenario.MalformKeys()
	assert.Equal(t, want, again)
}

// ── raw: ──────────────────────────────────────────────────────────────────

// rawScenario builds a single-step diameter/tcp action: raw scenario with no
// raw: block yet, so each test below only needs to attach the raw: shape it
// is pinning.
func rawScenario() *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "Test raw scenario",
		Version:   "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{"diameter": {Transport: "tcp"}},
		Steps: []scenario.Step{
			{ID: "step1", Protocol: "diameter", Action: "raw", Target: "peer:3868"},
		},
	}
}

func TestValidate_RawHexValidatesClean(t *testing.T) {
	s := rawScenario()
	s.Steps[0].Raw = &scenario.RawSpec{Hex: "0100001480000101000000000011111122222222"}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

// TestValidate_RawRefToPriorCaptureValidatesClean pins that a raw.ref
// referencing a prior step's captured value (in the established
// step_id.captured_key dotted form every other templated field in this
// codebase uses to reference a capture — see register_basic.yaml's
// Authorization: "{{ diameter_auth.server_name }}") validates clean.
func TestValidate_RawRefToPriorCaptureValidatesClean(t *testing.T) {
	s := rawScenario()
	s.Steps = []scenario.Step{
		{
			ID: "send_sip", Protocol: "sip", Action: "send", Target: "peer:5060", Message: "REGISTER",
			Capture: map[string]string{"sip_wire": "response.__raw"},
		},
		{ID: "step2", Protocol: "diameter", Action: "raw", Target: "peer:3868",
			Raw: &scenario.RawSpec{Ref: "{{ send_sip.sip_wire }}"}},
	}
	s.Protocols["sip"] = &scenario.ProtocolConfig{Transport: "tcp"}
	errs := scenario.Validate(s)
	assert.Empty(t, errs)
}

// TestValidate_RawWithoutBlockRejected pins that action: raw requires a raw:
// block naming its byte source.
func TestValidate_RawWithoutBlockRejected(t *testing.T) {
	s := rawScenario()
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "raw")
}

// TestValidate_RawBlockOnNonRawActionRejected pins that a raw: block is
// meaningless on any action other than raw.
func TestValidate_RawBlockOnNonRawActionRejected(t *testing.T) {
	s := rawScenario()
	s.Steps[0].Action = "send"
	s.Steps[0].Message = "UAR"
	s.Steps[0].Raw = &scenario.RawSpec{Hex: "0011"}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "raw")
}

// TestValidate_RawHexAndRefTogetherRejected pins the mutual-exclusion rule:
// hex and ref cannot both be declared.
func TestValidate_RawHexAndRefTogetherRejected(t *testing.T) {
	s := rawScenario()
	s.Steps[0].Raw = &scenario.RawSpec{Hex: "0011", Ref: "{{ some_var }}"}
	s.Variables = map[string]string{"some_var": "x"}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "raw")
}

// TestValidate_RawNeitherHexNorRefRejected pins that an empty raw: block
// (declared but naming no byte source) is rejected.
func TestValidate_RawNeitherHexNorRefRejected(t *testing.T) {
	s := rawScenario()
	s.Steps[0].Raw = &scenario.RawSpec{}
	errs := scenario.Validate(s)
	assertHasFieldError(t, errs, "raw")
}

// TestValidate_RawInvalidHexRejected pins that an undecodable hex literal
// (odd length, or non-hex characters) is rejected under raw.hex specifically.
func TestValidate_RawInvalidHexRejected(t *testing.T) {
	cases := []string{"zz", "abc"}
	for _, hex := range cases {
		t.Run(hex, func(t *testing.T) {
			s := rawScenario()
			s.Steps[0].Raw = &scenario.RawSpec{Hex: hex}
			errs := scenario.Validate(s)
			assertHasFieldError(t, errs, "raw.hex")
		})
	}
}

// TestValidate_RawRefUndefinedVarRejected pins that collectStringFields now
// includes raw.ref, so a {{ undefined_thing }} reference inside it fires the
// existing undefined-variable check.
func TestValidate_RawRefUndefinedVarRejected(t *testing.T) {
	s := rawScenario()
	s.Steps[0].Raw = &scenario.RawSpec{Ref: "{{ undefined_thing }}"}
	errs := scenario.Validate(s)
	var found bool
	for _, e := range errs {
		if strings.Contains(e.Message, `undefined variable reference "undefined_thing"`) {
			found = true
		}
	}
	assert.True(t, found, "expected an undefined variable reference error, got: %v", errs)
}

// TestAllowedActions_ContainsRaw pins that "raw" is a member of the
// validator's authoritative action set.
func TestAllowedActions_ContainsRaw(t *testing.T) {
	assert.True(t, scenario.AllowedActions()["raw"])
}

// TestRawKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent pins RawKeys'
// full accepted key set and its defensive-copy contract, the same guarantee
// ImpairKeys/MalformKeys already make.
func TestRawKeys_ReturnsAuthoritativeSetAndCopiesAreIndependent(t *testing.T) {
	want := scenario.RawKeySet{
		"hex": true,
		"ref": true,
	}
	got := scenario.RawKeys()
	assert.Equal(t, want, got)

	got["extra"] = true
	again := scenario.RawKeys()
	assert.Equal(t, want, again)
}

// TestValidate_ParallelRejectsSameProtocolSubSteps pins the guard against
// the runner's shared-adapter race: executeParallel hands every sub-step
// adapters[s.Protocol], one instance per protocol with no mutex, so two
// concurrent Diameter sub-steps interleave on one session buffer and steal
// each other's answers. No shipped scenario has this shape and the race
// detector never sees it (existing parallel tests use one protocol per
// sub-step), but it is the first thing a user will try. The rule turns a
// nondeterministic runtime result into a validate-time error naming both
// sub-steps.
func TestValidate_ParallelRejectsSameProtocolSubSteps(t *testing.T) {
	s := validScenario()
	s.Steps = []scenario.Step{
		{
			ID:     "burst",
			Action: "parallel",
			Steps: []scenario.Step{
				{ID: "reg_a", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER"},
				{ID: "reg_b", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER"},
			},
		},
	}
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertMessageContains(t, errs, `"reg_a" and "reg_b" both use protocol "sip"`)
	assertMessageContains(t, errs, "share one adapter")
}

// TestValidate_ParallelAcceptsDistinctProtocolsAndWaits is the control: one
// sub-step per protocol is the supported shape, and a wait sub-step (which
// touches no adapter) may sit alongside anything.
func TestValidate_ParallelAcceptsDistinctProtocolsAndWaits(t *testing.T) {
	s := validScenario()
	s.Protocols["diameter"] = &scenario.ProtocolConfig{Transport: "tcp", OriginHost: "h", OriginRealm: "r"}
	s.Steps = []scenario.Step{
		{
			ID:     "mixed",
			Action: "parallel",
			Steps: []scenario.Step{
				{ID: "sip_reg", Protocol: "sip", Action: "send", Target: "h:5060", Message: "REGISTER"},
				{ID: "dia_uar", Protocol: "diameter", Action: "send", Target: "h:3868", Message: "UAR",
					Fields: scenario.StepFields{"Session-Id": "s", "Origin-Host": "h", "Origin-Realm": "r", "Public-Identity": "sip:x@r"}},
				{ID: "pause", Action: "wait", Duration: 100},
			},
		},
	}
	errs := scenario.Validate(s)
	assert.Empty(t, errs, "%v", errs)
}

// TestValidate_UndeclaredProtocolIsAValidateTimeError pins that a step using
// a known protocol with no matching protocols: entry is rejected at validate
// time. Adapters are built only for declared protocols, so before this the
// scenario validated clean and failed at run time with
// `no adapter for protocol "sip"` — which reads like a broken build rather
// than a missing four-line block. The message must name the protocol and
// point at the block to add.
func TestValidate_UndeclaredProtocolIsAValidateTimeError(t *testing.T) {
	s := validScenario()
	s.Protocols = map[string]*scenario.ProtocolConfig{} // nothing declared
	errs := scenario.Validate(s)
	require.NotEmpty(t, errs)
	assertMessageContains(t, errs, `no "sip" entry under protocols:`)
	assertMessageContains(t, errs, "add one")

	// A sub-step inside a parallel group is caught too.
	s = validScenario()
	s.Protocols = map[string]*scenario.ProtocolConfig{"sip": {Transport: "udp"}}
	s.Steps = []scenario.Step{{
		ID: "grp", Action: "parallel",
		Steps: []scenario.Step{
			{ID: "ok_sip", Protocol: "sip", Action: "send", Target: "h:5060", Message: "OPTIONS"},
			{ID: "undeclared_dia", Protocol: "diameter", Action: "send", Target: "h:3868", Message: "UAR",
				Fields: scenario.StepFields{"Session-Id": "s", "Origin-Host": "h", "Origin-Realm": "r", "Public-Identity": "sip:x@r"}},
		},
	}}
	errs = scenario.Validate(s)
	assertMessageContains(t, errs, `no "diameter" entry under protocols:`)
}

// TestValidate_UnknownProtocolNotDoubleReported is the control: a protocol
// that is not a known name at all gets validateStepShape's "unknown protocol"
// error only, not a second "not declared" error on top of it.
func TestValidate_UnknownProtocolNotDoubleReported(t *testing.T) {
	s := validScenario()
	s.Steps[0].Protocol = "gtp"
	errs := scenario.Validate(s)
	count := 0
	for _, e := range errs {
		if e.StepID == s.Steps[0].ID && e.Field == "protocol" {
			count++
		}
	}
	assert.Equal(t, 1, count, "exactly one protocol error, got: %v", errs)
}

// assertStateScenario builds a minimal sip/udp scenario whose sole step is
// an assert_state with the given check.
func assertStateScenario(check string) *scenario.Scenario {
	return &scenario.Scenario{
		Name:      "t",
		Version:   "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{"sip": {Transport: "udp"}},
		Steps: []scenario.Step{{
			ID: "s1", Protocol: "sip", Action: "assert_state", Check: check,
		}},
	}
}

// TestValidate_AssertStateCheck pins the check: enum. The shipped
// register_basic.yaml waited on `check: registered` for a long time while
// the dialog tracker had no such state, because nothing validated the field:
// the step could never pass and nothing said so before run time.
func TestValidate_AssertStateCheck(t *testing.T) {
	for check := range scenario.AllowedChecks() {
		assert.Empty(t, scenario.Validate(assertStateScenario(check)), "check %q must validate", check)
	}

	errs := scenario.Validate(assertStateScenario("registerd"))
	require.Len(t, errs, 1)
	assert.Equal(t, "check", errs[0].Field)
	assert.Contains(t, errs[0].Message, `unknown state "registerd"`)
	assert.Contains(t, errs[0].Message, "registered")

	errs = scenario.Validate(assertStateScenario(""))
	require.Len(t, errs, 1)
	assert.Equal(t, "check", errs[0].Field)
}

func TestValidate_AssertStateIsSIPOnly(t *testing.T) {
	s := assertStateScenario("registered")
	s.Protocols["diameter"] = &scenario.ProtocolConfig{Transport: "tcp"}
	s.Steps[0].Protocol = "diameter"
	errs := scenario.Validate(s)
	require.Len(t, errs, 1)
	assert.Equal(t, "protocol", errs[0].Field)
	assert.Contains(t, errs[0].Message, "SIP-only")
}
