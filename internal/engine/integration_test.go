package engine_test

import (
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_MultiProtocol_VoLTERegistration tests a multi-protocol
// scenario that mirrors scenarios/ims/register_basic.yaml:
//
//  1. Diameter UAR → HSS returns UAA with server_name
//  2. SIP REGISTER → UAS returns 200 OK
//  3. SIP assert_state → verifies registration state
//
// This is the key MVP integration test: it proves that captured values from
// a Diameter step can be substituted into a subsequent SIP step's fields.
func TestIntegration_MultiProtocol_VoLTERegistration(t *testing.T) {
	// Start mock Diameter HSS.
	hss := diameter.NewMockHSS(t)
	defer hss.Close()

	// Start mock SIP UAS.
	uas := sip.NewMockUAS(t)
	defer uas.Close()

	// Create protocol adapters pointing at mock servers.
	sipTransport, err := common.NewUDPTransport(":0")
	require.NoError(t, err)
	sipAdapter := sip.NewTrackingAdapter(sipTransport, 2*time.Second)
	defer sipAdapter.Close()

	diamTransport, err := common.NewTCPTransport(":0")
	require.NoError(t, err)
	diamAdapter := diameter.NewAdapterWithTransport(diamTransport, 2*time.Second)
	defer diamAdapter.Close()

	adapters := map[string]common.ProtocolAdapter{
		"sip":      sipAdapter,
		"diameter": diamAdapter,
	}

	// Build the scenario programmatically (mirrors register_basic.yaml).
	s := &scenario.Scenario{
		Name:    "VoLTE basic registration",
		Version: "1.0",
		Protocols: map[string]*scenario.ProtocolConfig{
			"sip":      {Transport: "udp"},
			"diameter": {Transport: "tcp"},
		},
		Variables: map[string]string{
			"subscriber_msisdn": "447700900001",
			"hss_host":          hss.Addr(),
			"pcscf_host":        uas.Addr(),
		},
		Steps: []scenario.Step{
			{
				ID:       "diameter_auth",
				Protocol: "diameter",
				Action:   "send",
				Target:   "{{ hss_host }}",
				Message:  "UAR",
				Fields: map[string]string{
					"Session-Id":      "cellrift;integration;001",
					"Origin-Host":     "cellrift.ims.local",
					"Origin-Realm":    "ims.local",
					"User-Name":       "{{ subscriber_msisdn }}",
					"Public-Identity": "sip:{{ subscriber_msisdn }}@ims.local",
				},
				Capture: map[string]string{
					// A Cx UAA reports through Experimental-Result and carries
					// no base Result-Code — see the mock's buildUAA and
					// test/interop/EVIDENCE.md run 4.
					"result_code": "response.Experimental-Result-Code",
					"server_name": "response.avp.Server-Name",
				},
				Assert: []string{
					"result_code == 2001",
					"server_name != null",
				},
			},
			{
				ID:       "sip_register",
				Protocol: "sip",
				Action:   "send",
				Target:   "{{ pcscf_host }}",
				Message:  "REGISTER",
				Fields: map[string]string{
					"From":          "sip:{{ subscriber_msisdn }}@ims.local",
					"To":            "sip:{{ subscriber_msisdn }}@ims.local",
					"Authorization": "{{ diameter_auth.server_name }}",
				},
				Assert: []string{
					"response.status_code == 200",
				},
			},
			{
				ID:        "verify_registration",
				Protocol:  "sip",
				Action:    "assert_state",
				Check:     "registered",
				TimeoutMs: 2000,
			},
		},
	}

	opts := engine.RunOptions{FailFast: true}
	result := engine.Run(s, adapters, opts)

	// All steps should pass.
	assert.Equal(t, common.StatusPassed, result.Status,
		"scenario should pass, got: %+v", formatStepErrors(result))
	require.Len(t, result.Steps, 3)

	for _, step := range result.Steps {
		assert.Equal(t, common.StatusPassed, step.Result.Status,
			"step %s failed: %s", step.StepID, step.Result.Error)
	}

	// Verify the SIP REGISTER step received the server_name from Diameter.
	calls := sipAdapter.Calls()
	require.NotEmpty(t, calls)
	// Find the REGISTER call.
	var registerCall *sip.AdapterCall
	for i, c := range calls {
		if c.Message == "REGISTER" {
			registerCall = &calls[i]
			break
		}
	}
	require.NotNil(t, registerCall, "expected a REGISTER call to the SIP adapter")
	assert.Equal(t, "sip:scscf.ims.local:5060", registerCall.Fields["Authorization"],
		"Authorization header should contain the server_name captured from Diameter UAA")
}

// formatStepErrors collects step errors for diagnostic output.
func formatStepErrors(result *engine.RunResult) string {
	msg := ""
	for _, s := range result.Steps {
		if s.Result.Error != "" {
			msg += s.StepID + ": " + s.Result.Error + "; "
		}
		if s.Error != nil {
			msg += s.StepID + " (err): " + s.Error.Error() + "; "
		}
	}
	if msg == "" {
		return "no errors"
	}
	return msg
}
