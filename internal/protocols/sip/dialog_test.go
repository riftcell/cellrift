package sip_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDialog_ValidTransitions(t *testing.T) {
	d := &sip.Dialog{State: sip.StateInitial, CallID: "c1", FromTag: "f1", ToTag: "t1"}

	assert.NoError(t, d.Transition(sip.StateTrying))
	assert.Equal(t, sip.StateTrying, d.State)

	assert.NoError(t, d.Transition(sip.StateRinging))
	assert.Equal(t, sip.StateRinging, d.State)

	assert.NoError(t, d.Transition(sip.StateEstablished))
	assert.Equal(t, sip.StateEstablished, d.State)

	assert.NoError(t, d.Transition(sip.StateTerminated))
	assert.Equal(t, sip.StateTerminated, d.State)
}

func TestDialog_InvalidTransition(t *testing.T) {
	d := &sip.Dialog{State: sip.StateTerminated, CallID: "c1", FromTag: "f1", ToTag: "t1"}
	err := d.Transition(sip.StateEstablished)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid dialog transition")
}

func TestDialog_InitialToEstablished(t *testing.T) {
	// REGISTER: Initial → Established directly on 200 OK (no Trying/Ringing).
	d := &sip.Dialog{State: sip.StateInitial, CallID: "reg1", FromTag: "f1", ToTag: "t1"}
	assert.NoError(t, d.Transition(sip.StateEstablished))
	assert.Equal(t, sip.StateEstablished, d.State)
}

// TestDialog_MultipleProvisionalsAreLegal pins that a dialog accepts any
// number of provisional responses, in any order, before a final. The
// canonical VoLTE INVITE flow is 100 Trying → 183 Session Progress → 180
// Ringing → 200 OK (183 maps to the trying state, so that is trying→trying,
// then trying→ringing), and retransmitted 180s are routine. The transition
// table used to reject a repeated provisional as "invalid dialog transition
// from ringing to ringing", failing the step on an ordinary call — masked
// only because the mock UAS sends exactly one 180 before its 200.
func TestDialog_MultipleProvisionalsAreLegal(t *testing.T) {
	cases := []struct {
		name  string
		codes []int
	}{
		{"canonical VoLTE: 100, 183, 180, 200", []int{100, 183, 180, 200}},
		{"retransmitted 180", []int{180, 180, 180, 200}},
		{"repeated 100 Trying", []int{100, 100, 200}},
		{"183 after 180 (early media after alerting)", []int{180, 183, 200}},
		{"100 then 183 then 183", []int{100, 183, 183, 200}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dm := sip.NewDialogManager()
			for _, code := range tc.codes {
				msg := buildResponseMsg(code, "x", "call-multi", "from1", "to1")
				require.NoError(t, dm.UpdateFromResponse(msg), "response %d must be accepted", code)
			}
			d := dm.Lookup("call-multi", "from1", "to1")
			require.NotNil(t, d)
			assert.Equal(t, sip.StateEstablished, d.State, "the final 200 must still establish the dialog")
		})
	}
}

// TestDialog_ProvisionalAfterFinalStillRejected is the control: loosening
// the provisional states must not have loosened the final ones. A 180 after
// a dialog is already established or terminated remains invalid.
func TestDialog_ProvisionalAfterFinalStillRejected(t *testing.T) {
	for _, from := range []sip.DialogState{sip.StateEstablished, sip.StateTerminated} {
		t.Run(string(from), func(t *testing.T) {
			d := &sip.Dialog{State: from, CallID: "c", FromTag: "f", ToTag: "t"}
			err := d.Transition(sip.StateRinging)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "invalid dialog transition")
		})
	}
}

func TestDialog_InviteLifecycle(t *testing.T) {
	dm := sip.NewDialogManager()

	// Send INVITE → 100 Trying.
	trying := buildResponseMsg(100, "Trying", "call-001", "def456", "")
	require.NoError(t, dm.UpdateFromResponse(trying))

	// 180 Ringing.
	ringing := buildResponseMsg(180, "Ringing", "call-001", "def456", "ring001")
	require.NoError(t, dm.UpdateFromResponse(ringing))

	d := dm.Lookup("call-001", "def456", "ring001")
	require.NotNil(t, d)
	assert.Equal(t, sip.StateRinging, d.State)

	// 200 OK.
	ok := buildResponseMsg(200, "OK", "call-001", "def456", "ring001")
	require.NoError(t, dm.UpdateFromResponse(ok))
	assert.Equal(t, sip.StateEstablished, d.State)
}

func TestDialog_RegisterLifecycle(t *testing.T) {
	dm := sip.NewDialogManager()

	// REGISTER → 200 OK directly.
	ok := buildResponseMsg(200, "OK", "reg-001", "abc123", "srv789")
	require.NoError(t, dm.UpdateFromResponse(ok))

	d := dm.Lookup("reg-001", "abc123", "srv789")
	require.NotNil(t, d)
	assert.Equal(t, sip.StateEstablished, d.State)
}

func TestDialog_FailedCall(t *testing.T) {
	dm := sip.NewDialogManager()

	busy := buildResponseMsg(486, "Busy Here", "call-002", "aaa", "bbb")
	require.NoError(t, dm.UpdateFromResponse(busy))

	d := dm.Lookup("call-002", "aaa", "bbb")
	require.NotNil(t, d)
	assert.Equal(t, sip.StateTerminated, d.State)
}

func TestDialogManager_GetOrCreate(t *testing.T) {
	dm := sip.NewDialogManager()

	d1 := dm.GetOrCreate("c1", "f1", "t1")
	assert.Equal(t, sip.StateInitial, d1.State)

	d2 := dm.GetOrCreate("c1", "f1", "t1")
	assert.Same(t, d1, d2, "should return the same dialog instance")
}

func TestDialogManager_LookupMissing(t *testing.T) {
	dm := sip.NewDialogManager()
	d := dm.Lookup("nonexistent", "f", "t")
	assert.Nil(t, d)
}

// helper to build a minimal response SIPMessage for dialog tests.
func buildResponseMsg(code int, reason, callID, fromTag, toTag string) *sip.SIPMessage {
	from := "<sip:user@ims.local>"
	if fromTag != "" {
		from += ";tag=" + fromTag
	}
	to := "<sip:user@ims.local>"
	if toTag != "" {
		to += ";tag=" + toTag
	}
	return &sip.SIPMessage{
		Version:      "SIP/2.0",
		StatusCode:   code,
		ReasonPhrase: reason,
		Headers: map[string]string{
			"Call-ID": callID,
			"From":    from,
			"To":      to,
		},
	}
}
