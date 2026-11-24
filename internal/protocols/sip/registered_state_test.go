package sip_test

// registered_state_test.go pins the "registered" dialog state. Until it
// existed, a 2xx to REGISTER landed in "established" — the same state a
// call's 200 OK produces — so scenarios/ims/register_basic.yaml's
// `assert_state: check: registered` could never pass (the schema listed the
// state, the tracker had never heard of it), and EstablishedDialog could
// hand an in-dialog BYE a registration's Call-ID instead of the call's.

import (
	"fmt"
	"testing"

	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func successFor(method, callID string) *sip.SIPMessage {
	return sip.BuildResponse(200, "OK", map[string]string{
		"Call-ID": callID,
		"From":    "<sip:a@ims.local>;tag=f1",
		"To":      "<sip:a@ims.local>;tag=t1",
		"CSeq":    fmt.Sprintf("1 %s", method),
	})
}

func TestDialog_TwoHundredToRegisterIsRegisteredNotEstablished(t *testing.T) {
	dm := sip.NewDialogManager()
	require.NoError(t, dm.UpdateFromResponse(successFor("REGISTER", "reg-1")))

	assert.True(t, dm.HasDialogInState(sip.StateRegistered))
	assert.False(t, dm.HasDialogInState(sip.StateEstablished),
		"a registration is not a call; EstablishedDialog must never pick it for a BYE")
	assert.Nil(t, dm.EstablishedDialog())
}

func TestDialog_TwoHundredToInviteIsStillEstablished(t *testing.T) {
	dm := sip.NewDialogManager()
	require.NoError(t, dm.UpdateFromResponse(successFor("INVITE", "call-1")))

	assert.True(t, dm.HasDialogInState(sip.StateEstablished))
	assert.False(t, dm.HasDialogInState(sip.StateRegistered))
}

// A refresh REGISTER (or a de-registration with Expires: 0) is answered 2xx
// on the same Call-ID; the tracker cannot see the request's Expires, so the
// only correct behaviour is to accept registered → registered.
func TestDialog_RefreshRegisterIsAccepted(t *testing.T) {
	dm := sip.NewDialogManager()
	require.NoError(t, dm.UpdateFromResponse(successFor("REGISTER", "reg-1")))
	require.NoError(t, dm.UpdateFromResponse(successFor("REGISTER", "reg-1")))
	assert.True(t, dm.HasDialogInState(sip.StateRegistered))
}

// A 100 Trying before the 200 must not strand the registration in trying.
func TestDialog_TryingThenRegistered(t *testing.T) {
	dm := sip.NewDialogManager()
	trying := successFor("REGISTER", "reg-1")
	trying.StatusCode, trying.ReasonPhrase = 100, "Trying"
	require.NoError(t, dm.UpdateFromResponse(trying))
	require.NoError(t, dm.UpdateFromResponse(successFor("REGISTER", "reg-1")))
	assert.True(t, dm.HasDialogInState(sip.StateRegistered))
}
