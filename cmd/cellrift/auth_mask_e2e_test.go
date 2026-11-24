package main

// auth_mask_e2e_test.go is the production-path proof for password masking.

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Both reflecting scenarios REGISTER, get challenged, and then assert on a
// response header the peer fills with a secret. The assertion cannot match, so
// the engine's failure text quotes the header's actual value, which is the real
// production path by which a resolved credential reaches a report string.
const (
	maskStepsFmt = `steps:
  - id: register_reflected
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: REGISTER
    fields:
      Request-URI: "sip:447700900201@registrar.invalid"
      From: "<sip:447700900201@ims.local>;tag=mask017"
      To: "<sip:447700900201@ims.local>"
      Contact: "<sip:447700900201@10.0.0.1:5060>"
    assert:
      - response.header.%s == never-equal
`
	maskReflectPassword = "X-Echo-Pw"
	maskReflectHeader   = "X-Echo-Auth"
)

// reflectingPeer behaves like authPeer and, on the final 200, copies the
// password and the credential header it was sent into two response headers.
func reflectingPeer(password string) func(*sip.SIPMessage) []*sip.SIPMessage {
	inner := authPeer(password)
	return func(req *sip.SIPMessage) []*sip.SIPMessage {
		out := inner(req)
		if len(out) == 1 && out[0].StatusCode == 200 && req.Method == "REGISTER" {
			out[0].Headers[maskReflectPassword] = password
			out[0].Headers[maskReflectHeader] = req.Headers["Authorization"]
		}
		return out
	}
}

// maskRun runs scenario with sip_password overridden to clientPassword against
// a peer that knows peerPassword.
func maskRun(t *testing.T, header, peerPassword, clientPassword string) *engine.RunResult {
	t.Helper()
	yaml := authPrologue + authPasswordTemplated + authVarsNoPassword + fmt.Sprintf(maskStepsFmt, header)
	return runAuthScenarioWith(t, yaml, reflectingPeer(peerPassword), "sip_password="+clientPassword)
}

func renderE2E(t *testing.T, res *engine.RunResult, format string, verbose, noMask bool) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, selectReporter(format, &b, verbose, noMask).Report(&b, []*engine.RunResult{res}))
	return b.String()
}

func escapedE2E(t *testing.T, pw string) []string {
	j, err := json.Marshal(pw)
	require.NoError(t, err)
	var x bytes.Buffer
	require.NoError(t, xml.EscapeText(&x, []byte(pw)))
	return []string{pw, string(j[1 : len(j)-1]), x.String()}
}

type maskFormat struct {
	format  string
	verbose bool
}

var maskFormats = []maskFormat{{"terminal", false}, {"terminal", true}, {"json", false}, {"junit", false}}

// requireCleanEverywhere renders res in every format under both flag states
// and asserts none of the secrets, nor their escaped forms, appear.
func requireCleanEverywhere(t *testing.T, res *engine.RunResult, secrets ...string) {
	t.Helper()
	for _, f := range maskFormats {
		for _, noMask := range []bool{false, true} {
			out := renderE2E(t, res, f.format, f.verbose, noMask)
			for _, s := range secrets {
				for _, form := range escapedE2E(t, s) {
					assert.NotContains(t, out, form, "%s verbose=%v noMask=%v", f.format, f.verbose, noMask)
				}
			}
		}
	}
}

// TestRunScenario_PasswordReachesNoReport is the end of the masking chain. The
// reporter tests prove the Masker removes a registered secret; the adapter
// tests prove the password is registered. Only this file proves the two are
// connected by the runner's collection, RunResult.Secrets and maskerFor's
// union, which is the level at which a user would discover a leak: in an
// uploaded CI artifact.
//
// Each reflecting row first requires that the UNMASKED error text really does
// carry the secret, so the row cannot pass merely because nothing was printed.
func TestRunScenario_PasswordReachesNoReport(t *testing.T) {
	for _, pw := range []string{"shared-pw", "447700900999", `pw"<&'pw`} {
		for _, header := range []string{maskReflectPassword, maskReflectHeader} {
			t.Run(pw+"/"+header, func(t *testing.T) {
				res := maskRun(t, header, pw, pw)
				step := findStep(t, res, "register_reflected")
				require.NotNil(t, step.Error, "the assertion must fail and quote the reflected value")
				require.NotEmpty(t, res.Secrets, "the run must hand the reporters the credentials it resolved")
				secret := pw
				if header == maskReflectHeader {
					secret = res.Secrets[len(res.Secrets)-1]
					require.True(t, strings.HasPrefix(secret, "Digest "), "last recorded secret is the credential header")
				}
				require.Contains(t, step.Error.Error(), secret, "precondition: the leak is reachable before masking")
				requireCleanEverywhere(t, res, secret)
			})
		}
	}
}

// A failing run, where the engine renders an error for a rejected credential.
func TestRunScenario_PasswordReachesNoReport_WrongPassword(t *testing.T) {
	res := maskRun(t, maskReflectPassword, "shared-pw", "wrong-pw")
	require.Equal(t, common.StatusFailed, res.Status)
	assert.Contains(t, findStep(t, res, "register_reflected").Result.Error, "credentials rejected")
	requireCleanEverywhere(t, res, "wrong-pw", "shared-pw")
}

// maskOptionsOnly never meets a challenge, so no credential is ever resolved.
const maskOptionsOnly = `steps:
  - id: options_only
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    fields:
      From: "<sip:447700900201@ims.local>;tag=mask017b"
      To: "<sip:ims.local>"
    assert:
      - response.status_code == 200
`

// A run that declares no password and never authenticates registers nothing, so nothing is replaced and
// the report carries no secret token.
func TestRunScenario_NoAuthenticationNoSecretsNoToken(t *testing.T) {
	res := runAuthScenario(t, authPrologue+authPasswordAbsent+authVarsNoPassword+maskOptionsOnly, "shared-pw")
	requirePassed(t, res)
	assert.Empty(t, res.Secrets)
	for _, f := range maskFormats {
		assert.NotContains(t, renderE2E(t, res, f.format, f.verbose, false), "REDACTED-SECRET")
	}
}

// maskOptionsReflected sends an OPTIONS the peer never challenges and asserts on
// a header the peer fills with the declared password, so the assertion failure
// quotes it.
const maskOptionsReflected = `steps:
  - id: options_reflected
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    fields:
      From: "<sip:447700900201@ims.local>;tag=mask017c"
      To: "<sip:ims.local>"
    assert:
      - response.header.X-Echo-Pw == never-equal
`

// TestRunScenario_DeclaredPasswordIsMaskedWithoutAChallenge: the scenario
// declares a password, the peer never challenges, so no credential is ever
// resolved by the digest path. The password must still be removed.
func TestRunScenario_DeclaredPasswordIsMaskedWithoutAChallenge(t *testing.T) {
	for _, pw := range []string{"shared-pw", `pw"<&'pw`} {
		t.Run(pw, func(t *testing.T) {
			responder := func(req *sip.SIPMessage) []*sip.SIPMessage {
				h := map[string]string{"From": req.Headers["From"], "To": req.Headers["To"], "Call-ID": req.Headers["Call-ID"],
					"CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"], maskReflectPassword: pw}
				return []*sip.SIPMessage{sip.BuildResponse(200, "OK", h)}
			}
			yaml := authPrologue + authPasswordTemplated + authVarsNoPassword + maskOptionsReflected
			res := runAuthScenarioWith(t, yaml, responder, "sip_password="+pw)
			step := findStep(t, res, "options_reflected")
			require.NotNil(t, step.Error)
			require.Contains(t, step.Error.Error(), pw, "precondition: the leak is reachable before masking")
			requireCleanEverywhere(t, res, pw)
		})
	}
}

// TestRunScenario_ShortPasswordWarningReachesEveryReporter: the warning is added
// by Run after the steps, so this proves it is not dropped on the way to the
// three reporters, and that a long password adds none.
func TestRunScenario_ShortPasswordWarningReachesEveryReporter(t *testing.T) {
	const warning = "may contain redactions of unrelated text"
	short := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsNoPassword+maskOptionsOnly, "x", "sip_password=lab")
	long := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsNoPassword+maskOptionsOnly, "x", "sip_password=a-long-enough-pw")
	for _, f := range maskFormats {
		out := renderE2E(t, short, f.format, f.verbose, true)
		assert.Equal(t, 1, strings.Count(out, warning), "%s verbose=%v", f.format, f.verbose)
		assert.NotContains(t, renderE2E(t, long, f.format, f.verbose, true), warning)
	}
}
