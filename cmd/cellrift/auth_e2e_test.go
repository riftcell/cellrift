package main

// auth_e2e_test.go is the production-path proof for SIP digest authentication.
// The adapter tests in internal/protocols/sip prove the retry, the grammar and
// the capture against a mock the test itself drives. This file proves the same
// behaviour survives cmd/cellrift/main.go's own wiring: the adapter
// construction, the protocol-config flattening, the per-step template
// resolution and the assertion engine. That is the only level at which
// response.auth.* and the credential precedence are observable the way a user
// observes them.

import (
	"crypto/md5" //nolint:gosec // RFC 2617 MD5 digest, computed here to verify the peer side independently
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The scenarios are assembled from named constant fragments, never by
// string replacement on one another: a silent non-match in a Replace would make
// a test assert the wrong thing.
const (
	authPrologue = `version: "1.0"
name: auth-e2e
description: an authenticated REGISTER plus an unchallenged OPTIONS, through the real run path
protocols:
  sip:
    transport: udp
    username: impi-user
`
	authPasswordTemplated    = "    password: \"{{ sip_password }}\"\n"
	authPasswordUnresolvable = "    password: \"{{ nope }}\"\n"
	authPasswordAbsent       = ""

	authVarsShared = `variables:
  peer: "peer.lab.local:5060"
  sip_password: "shared-pw"
`
	authVarsWrong = `variables:
  peer: "peer.lab.local:5060"
  sip_password: "wrong-pw"
`
	authVarsNoPassword = `variables:
  peer: "peer.lab.local:5060"
`

	authSteps = `steps:
  - id: register_authenticated
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: REGISTER
    fields:
      Request-URI: "sip:447700900201@registrar.invalid"
      From: "<sip:447700900201@ims.local>;tag=e2e017"
      To: "<sip:447700900201@ims.local>"
      Contact: "<sip:447700900201@10.0.0.1:5060>"
    assert:
      - response.status_code == 200
      - response.auth.challenged == true
      - response.auth.retried == true
      - response.auth.realm == cellrift.interop
      - response.auth.algorithm == MD5
      - response.auth.nonce != null
  - id: options_unchallenged
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: OPTIONS
    fields:
      From: "<sip:447700900201@ims.local>;tag=e2e017b"
      To: "<sip:ims.local>"
    assert:
      - response.status_code == 200
      - response.auth.challenged == false
`

	// authReachableScenario's ONLY assertion is the nested path, so its
	// verdict is exactly the assertion engine's ability to descend into the
	// auth map.
	authReachableScenario = authPrologue + authPasswordTemplated + authVarsShared + `steps:
  - id: register_reachable
    protocol: sip
    action: send
    target: "{{ peer }}"
    message: REGISTER
    fields:
      Request-URI: "sip:447700900201@registrar.invalid"
      From: "<sip:447700900201@ims.local>;tag=e2e017c"
      To: "<sip:447700900201@ims.local>"
      Contact: "<sip:447700900201@10.0.0.1:5060>"
    assert:
      - response.auth.realm == cellrift.interop
`
)

const (
	e2eRealm = "cellrift.interop"
	e2eNonce = "e2e-nonce-01"
	e2eChal  = `Digest realm="cellrift.interop",nonce="e2e-nonce-01",opaque="e2e-opaque",algorithm=MD5,qop="auth"`
)

var digestParam = regexp.MustCompile(`(\w+)=(?:"([^"]*)"|([^,\s]+))`)

// md5Hex is the verifier's own MD5; it does not call production code.
func md5Hex(s string) string {
	sum := md5.Sum([]byte(s)) //nolint:gosec // see import
	return hex.EncodeToString(sum[:])
}

// verifyDigest recomputes response= per RFC 2617 qop=auth from the retry's own
// header, with the password the peer actually knows.
func verifyDigest(req *sip.SIPMessage, password string) error {
	hdr, ok := req.Headers["Authorization"]
	if !ok {
		return fmt.Errorf("no Authorization header")
	}
	p := map[string]string{}
	for _, m := range digestParam.FindAllStringSubmatch(hdr, -1) {
		p[strings.ToLower(m[1])] = m[2] + m[3]
	}
	ha1 := md5Hex(p["username"] + ":" + e2eRealm + ":" + password)
	ha2 := md5Hex(req.Method + ":" + p["uri"])
	want := md5Hex(strings.Join([]string{ha1, e2eNonce, p["nc"], p["cnonce"], p["qop"], ha2}, ":"))
	if p["response"] != want {
		return fmt.Errorf("digest mismatch")
	}
	return nil
}

// authPeer answers a REGISTER with a digest challenge until the request carries
// a correct digest for password, and every other method with a plain 200, so
// the unchallenged OPTIONS step has something to be unchallenged by.
func authPeer(password string) func(*sip.SIPMessage) []*sip.SIPMessage {
	var mu sync.Mutex
	first := true
	return func(req *sip.SIPMessage) []*sip.SIPMessage {
		h := map[string]string{
			"From": req.Headers["From"], "To": req.Headers["To"] + ";tag=e2e-srv",
			"Call-ID": req.Headers["Call-ID"], "CSeq": req.Headers["CSeq"], "Via": req.Headers["Via"],
		}
		mu.Lock()
		challenge := req.Method == "REGISTER" && (first || verifyDigest(req, password) != nil)
		if req.Method == "REGISTER" {
			first = false
		}
		mu.Unlock()
		if challenge {
			h["WWW-Authenticate"] = e2eChal
			return []*sip.SIPMessage{sip.BuildResponse(401, "Unauthorized", h)}
		}
		return []*sip.SIPMessage{sip.BuildResponse(200, "OK", h)}
	}
}

// runAuthScenario writes yaml to a temp file and runs it against a fresh peer
// that knows peerPassword, with the given --var overrides.
func runAuthScenario(t *testing.T, yaml, peerPassword string, extraVars ...string) *engine.RunResult {
	t.Helper()
	return runAuthScenarioWith(t, yaml, authPeer(peerPassword), extraVars...)
}

// runAuthScenarioWith is runAuthScenario against a caller-supplied responder.
func runAuthScenarioWith(t *testing.T, yaml string, responder func(*sip.SIPMessage) []*sip.SIPMessage, extraVars ...string) *engine.RunResult {
	t.Helper()
	// Layer 3 reads the process environment: a box exporting it must not
	// authenticate a row that is supposed to have no password.
	t.Setenv("CELLRIFT_SIP_PASSWORD", "")
	uas := sip.NewMockUAS(t)
	t.Cleanup(uas.Close)
	uas.SetResponder(responder)
	path := filepath.Join(t.TempDir(), "auth.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	cfg := &runConfig{timeout: 2 * time.Second, vars: append([]string{"peer=" + uas.Addr()}, extraVars...)}
	result := cfg.runScenario(path)
	require.NotNil(t, result)
	return result
}

func requirePassed(t *testing.T, result *engine.RunResult) {
	t.Helper()
	for _, s := range result.Steps {
		require.NotNil(t, s.Result, s.StepID)
		require.Equal(t, common.StatusPassed, s.Result.Status, "%s: %s %v", s.StepID, s.Result.Error, s.Error)
	}
	require.Equal(t, common.StatusPassed, result.Status)
}

// TestRunScenario_AuthenticatedRegisterSurvivesProductionWiring also covers the
// unchallenged-step case: step 2's `response.auth.challenged == false` is the
// load-bearing assertion, since an absent key would make `== false` fail.
func TestRunScenario_AuthenticatedRegisterSurvivesProductionWiring(t *testing.T) {
	result := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsShared+authSteps, "shared-pw")
	requirePassed(t, result)
	assert.Len(t, result.Steps, 2)
}

// --var wins over the scenario's own (wrong) variable, so a pass proves the
// override reached the credential template.
func TestRunScenario_AuthPasswordFromVarOverride(t *testing.T) {
	result := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsWrong+authSteps, "shared-pw", "sip_password=shared-pw")
	requirePassed(t, result)
}

// CELLRIFT_VAR_SIP_PASSWORD feeds a {{ sip_password }} template (layer 1); the
// scenario declares no such variable.
func TestRunScenario_AuthPasswordFromCellriftVarEnv(t *testing.T) {
	t.Setenv("CELLRIFT_VAR_SIP_PASSWORD", "shared-pw")
	result := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsNoPassword+authSteps, "shared-pw")
	requirePassed(t, result)
}

func TestRunScenario_AuthWrongPasswordFailsTheRun(t *testing.T) {
	result := runAuthScenario(t, authPrologue+authPasswordTemplated+authVarsWrong+authSteps, "shared-pw")
	assert.Equal(t, common.StatusFailed, result.Status)
	step := findStep(t, result, "register_authenticated")
	require.NotNil(t, step.Result)
	assert.Equal(t, common.StatusFailed, step.Result.Status)
	assert.Contains(t, step.Result.Error, "credentials rejected")
}

// CELLRIFT_SIP_PASSWORD is layer 3, distinct from CELLRIFT_VAR_SIP_PASSWORD:
// the scenario carries no password key at all.
func TestRunScenario_AuthPasswordFromCellriftSipPasswordEnv(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()
	uas.SetResponder(authPeer("shared-pw"))
	path := filepath.Join(t.TempDir(), "auth.yaml")
	require.NoError(t, os.WriteFile(path, []byte(authPrologue+authPasswordAbsent+authVarsNoPassword+authSteps), 0o600))
	t.Setenv("CELLRIFT_SIP_PASSWORD", "shared-pw")
	cfg := &runConfig{timeout: 2 * time.Second, vars: []string{"peer=" + uas.Addr()}}
	requirePassed(t, cfg.runScenario(path))
}

// A declared-but-unresolvable template is an error, never a fall-through to the
// ambient password: the author asked for a specific credential.
func TestRunScenario_AuthUnresolvableTemplateDoesNotFallThroughToTheEnv(t *testing.T) {
	uas := sip.NewMockUAS(t)
	defer uas.Close()
	uas.SetResponder(authPeer("shared-pw"))
	path := filepath.Join(t.TempDir(), "auth.yaml")
	require.NoError(t, os.WriteFile(path, []byte(authPrologue+authPasswordUnresolvable+authVarsNoPassword+authSteps), 0o600))
	t.Setenv("CELLRIFT_SIP_PASSWORD", "shared-pw")
	cfg := &runConfig{timeout: 2 * time.Second, vars: []string{"peer=" + uas.Addr()}}
	result := cfg.runScenario(path)
	require.NotNil(t, result)
	assert.Equal(t, common.StatusFailed, result.Status)
	assert.Equal(t, 0, len(uas.ReceivedRequests()), "an unresolvable credential must not reach the wire")
}

// TestAuthFields_ReachableByTheAssertionEngine lives in cmd/cellrift, not in the
// sip package, because internal/engine's extractResponseValue is unexported and
// the only way to exercise the REAL walker is through cfg.runScenario.
//
// It is the test for a trap that is invisible by inspection. extractResponseValue
// walks nested maps with current.(map[string]interface{}), and a Go type
// assertion to map[string]interface{} is FALSE for a value of a named map type.
// Storing a named map type under "auth" makes every response.auth.* path resolve
// to "absent": `response.auth.realm == ...` fails for an implementation that
// reads as completely correct and whose own unit tests all pass.
//
// It asserts through a SCENARIO assertion, never by indexing
// StepResult.Response from Go: a Go read does not perform the type assertion
// and would pass under the mutation.
func TestAuthFields_ReachableByTheAssertionEngine(t *testing.T) {
	result := runAuthScenario(t, authReachableScenario, "shared-pw")
	requirePassed(t, result)
	require.Len(t, result.Steps, 1)
}
