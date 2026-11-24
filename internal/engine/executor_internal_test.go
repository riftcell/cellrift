package engine

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveStepInputs and applyStepOutcome are unexported, so their dedicated
// tests live here (package engine) rather than in executor_test.go (package
// engine_test) — the same internal-test-file split adapter_internal_test.go
// and registry_internal_test.go already established elsewhere in this
// codebase for unexported symbols.

// TestResolveStepInputs_NeverDispatchesToAdapter proves resolveStepInputs
// only resolves — it computes stepInputs without ever calling Execute on an
// adapter, which is the seam a reorder group depends on to build all of its
// sub-steps' messages before any of them reaches the wire. Its signature
// takes no adapter argument at all, which is the mechanical proof: there is
// nothing to call.
func TestResolveStepInputs_NeverDispatchesToAdapter(t *testing.T) {
	state := NewState()
	state.Set("host", "hss.lab.local")

	step := &scenario.Step{
		ID:        "step1",
		Protocol:  "diameter",
		Action:    "send",
		Target:    "{{ host }}:3868",
		Message:   "UAR",
		Fields:    map[string]string{"User-Name": "test"},
		TimeoutMs: 500,
		Impair:    &scenario.ImpairSpec{LatencyMs: 100},
		Body: &scenario.StepBody{
			ContentType: "application/sdp",
			Content:     "v=0",
		},
	}

	in, err := resolveStepInputs(step, state, map[string]string{"origin_realm": "cellrift.local"})
	require.NoError(t, err)
	assert.Equal(t, "hss.lab.local:3868", in.Target)
	assert.Equal(t, "UAR", in.Message)
	assert.Equal(t, "test", in.Fields["User-Name"])
	assert.Equal(t, "application/sdp", in.Fields["__body_content_type"])
	assert.Equal(t, "v=0", in.Fields["__body_content"])
	assert.Equal(t, "cellrift.local", in.Config["origin_realm"])
	assert.Equal(t, "500", in.Config["timeout_ms"])
	assert.Equal(t, "100", in.Config["latency_ms"])
}

// TestResolveStepInputs_WrapsResolutionErrors pins the exact wrapped-error
// texts ExecuteStep relied on before the split, so a scenario author's error
// output does not silently change shape.
func TestResolveStepInputs_WrapsResolutionErrors(t *testing.T) {
	state := NewState()

	cases := []struct {
		name string
		step *scenario.Step
		want string
	}{
		{name: "target", step: &scenario.Step{ID: "s1", Target: "{{ missing }}:5060"}, want: "step s1: resolve target:"},
		{name: "message", step: &scenario.Step{ID: "s1", Message: "{{ missing }}"}, want: "step s1: resolve message:"},
		{name: "fields", step: &scenario.Step{ID: "s1", Fields: map[string]string{"a": "{{ missing }}"}}, want: "step s1: resolve fields:"},
		{name: "check", step: &scenario.Step{ID: "s1", Check: "{{ missing }}"}, want: "step s1: resolve check:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveStepInputs(tc.step, state, nil)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestApplyStepOutcome_NilResultDoesNotPanic proves applyStepOutcome is
// nil-safe: a reorder sub-step the adapter could not produce a result for
// has nothing to capture or assert against, and must not crash the group.
func TestApplyStepOutcome_NilResultDoesNotPanic(t *testing.T) {
	state := NewState()
	step := &scenario.Step{
		ID:      "step1",
		Capture: map[string]string{"x": "response.field"},
		Assert:  []string{"x != null"},
	}

	assert.NotPanics(t, func() {
		err := applyStepOutcome(step, state, nil)
		assert.NoError(t, err)
	})
}

// TestFoldMalformConfig_NilSpecLeavesConfigUntouched pins that a step with
// no malform: block is a true no-op: no keys written, no error.
func TestFoldMalformConfig_NilSpecLeavesConfigUntouched(t *testing.T) {
	config := map[string]string{"existing": "value"}
	err := foldMalformConfig(config, nil, NewState())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"existing": "value"}, config)
}

// TestFoldMalformConfig_AVPLength pins that both avp_length keys are
// written from the shared malform constants, not re-spelled string
// literals.
func TestFoldMalformConfig_AVPLength(t *testing.T) {
	config := map[string]string{}
	spec := &scenario.MalformSpec{AVPLength: &scenario.AVPLengthMalform{Name: "Session-Id", Value: 9999}}
	err := foldMalformConfig(config, spec, NewState())
	require.NoError(t, err)
	assert.Equal(t, "Session-Id", config[malform.AVPLengthNameKey])
	assert.Equal(t, "9999", config[malform.AVPLengthValueKey])
}

// TestFoldMalformConfig_MsgLengthZeroIsWritten pins the *int pointer's whole
// reason for existing: a declared zero override is written, not omitted —
// the opposite of foldImpairConfig's "if v > 0" rule for ordinary
// millisecond keys.
func TestFoldMalformConfig_MsgLengthZeroIsWritten(t *testing.T) {
	config := map[string]string{}
	zero := 0
	spec := &scenario.MalformSpec{MsgLength: &zero}
	err := foldMalformConfig(config, spec, NewState())
	require.NoError(t, err)
	assert.Equal(t, "0", config[malform.MsgLengthKey])
}

// TestFoldMalformConfig_MsgLengthNilWritesNoKey pins the flip side: a step
// with no msg_length declared writes no key at all, distinguishing
// "declared zero" from "not declared".
func TestFoldMalformConfig_MsgLengthNilWritesNoKey(t *testing.T) {
	config := map[string]string{}
	spec := &scenario.MalformSpec{}
	err := foldMalformConfig(config, spec, NewState())
	require.NoError(t, err)
	_, ok := config[malform.MsgLengthKey]
	assert.False(t, ok)
}

// TestFoldMalformConfig_SIPHeaderMissingWritesNoValueKey pins that op:
// missing writes name and op but no value key at all — an empty value key
// would be indistinguishable from a deliberately empty overwrite value.
func TestFoldMalformConfig_SIPHeaderMissingWritesNoValueKey(t *testing.T) {
	config := map[string]string{}
	spec := &scenario.MalformSpec{SIPHeader: &scenario.SIPHeaderMalform{Name: "Via", Op: malform.OpMissing}}
	err := foldMalformConfig(config, spec, NewState())
	require.NoError(t, err)
	assert.Equal(t, "Via", config[malform.SIPHeaderNameKey])
	assert.Equal(t, malform.OpMissing, config[malform.SIPHeaderOpKey])
	_, ok := config[malform.SIPHeaderValueKey]
	assert.False(t, ok)
}

// TestFoldMalformConfig_SIPHeaderOverwriteResolvesValue pins that a
// {{ var }} reference inside sip_header.value is resolved through state
// before being written to config.
func TestFoldMalformConfig_SIPHeaderOverwriteResolvesValue(t *testing.T) {
	state := NewState()
	state.Set("bad_via", "SIP/2.0/UDP corrupted.example")
	config := map[string]string{}
	spec := &scenario.MalformSpec{SIPHeader: &scenario.SIPHeaderMalform{
		Name: "Via", Op: malform.OpOverwrite, Value: "{{ bad_via }}",
	}}
	err := foldMalformConfig(config, spec, state)
	require.NoError(t, err)
	assert.Equal(t, "SIP/2.0/UDP corrupted.example", config[malform.SIPHeaderValueKey])
}

// TestFoldMalformConfig_SIPHeaderUnresolvableValueErrors pins that an
// undefined {{ var }} reference in sip_header.value returns an error naming
// the step, mirroring resolveStepInputs' other resolution-error wrapping.
func TestFoldMalformConfig_SIPHeaderUnresolvableValueErrors(t *testing.T) {
	state := NewState()
	step := &scenario.Step{
		ID:       "step1",
		Protocol: "sip",
		Action:   "send",
		Malform: &scenario.MalformSpec{SIPHeader: &scenario.SIPHeaderMalform{
			Name: "Via", Op: malform.OpOverwrite, Value: "{{ missing_var }}",
		}},
	}
	_, err := resolveStepInputs(step, state, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step step1")
}

// TestFoldRawConfig_NilSpecLeavesConfigUntouched pins that a step with no
// raw: block is a true no-op: no key written, no error.
func TestFoldRawConfig_NilSpecLeavesConfigUntouched(t *testing.T) {
	config := map[string]string{"existing": "value"}
	err := foldRawConfig(config, nil, NewState())
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"existing": "value"}, config)
}

// TestFoldRawConfig_HexDecodesIntoRawPayloadKey pins that hex is decoded
// exactly once, here, into literal bytes under RawPayloadKey.
func TestFoldRawConfig_HexDecodesIntoRawPayloadKey(t *testing.T) {
	config := map[string]string{}
	spec := &scenario.RawSpec{Hex: "0100001480000101"}
	err := foldRawConfig(config, spec, NewState())
	require.NoError(t, err)
	assert.Equal(t, "\x01\x00\x00\x14\x80\x00\x01\x01", config[malform.RawPayloadKey])
}

// TestFoldRawConfig_InvalidHexErrors pins that an undecodable hex literal
// returns a wrapped error naming raw.hex — the validator should already have
// rejected this scenario, but the fold itself must not panic or silently
// write garbage.
func TestFoldRawConfig_InvalidHexErrors(t *testing.T) {
	config := map[string]string{}
	spec := &scenario.RawSpec{Hex: "zz"}
	err := foldRawConfig(config, spec, NewState())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode raw.hex")
}

// TestFoldRawConfig_RefResolvesThroughState pins that a ref is resolved
// through state.Resolve exactly like every other templated step field.
func TestFoldRawConfig_RefResolvesThroughState(t *testing.T) {
	state := NewState()
	state.Set("captured_wire", "hello")
	config := map[string]string{}
	spec := &scenario.RawSpec{Ref: "{{ captured_wire }}"}
	err := foldRawConfig(config, spec, state)
	require.NoError(t, err)
	assert.Equal(t, "hello", config[malform.RawPayloadKey])
}

// TestFoldRawConfig_RefPreservesNonUTF8CapturedBytes pins the lossless
// round-trip action: raw's captured-response.__raw byte source depends on: a
// []byte captured through response.__raw survives state.Resolve unchanged
// even when it is not valid UTF-8, because formatValue's []byte branch is a
// raw string(val) copy with no UTF-8 validation.
func TestFoldRawConfig_RefPreservesNonUTF8CapturedBytes(t *testing.T) {
	state := NewState()
	nonUTF8 := []byte{0xFF, 0xFE, 0x00, 0x80, 0xC0, 0xC1}
	state.Set("sip_wire", nonUTF8)
	config := map[string]string{}
	spec := &scenario.RawSpec{Ref: "{{ sip_wire }}"}
	err := foldRawConfig(config, spec, state)
	require.NoError(t, err)
	assert.Equal(t, string(nonUTF8), config[malform.RawPayloadKey])
}

// TestFoldRawConfig_UnresolvableRefErrors pins that an undefined {{ var }}
// reference in raw.ref returns a wrapped error naming the step, mirroring
// foldMalformConfig's identical sip_header.value resolution-error wrapping.
func TestFoldRawConfig_UnresolvableRefErrors(t *testing.T) {
	state := NewState()
	step := &scenario.Step{
		ID:       "step1",
		Protocol: "diameter",
		Action:   "raw",
		Raw:      &scenario.RawSpec{Ref: "{{ missing_var }}"},
	}
	_, err := resolveStepInputs(step, state, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "step step1")
}
