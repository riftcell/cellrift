package sip

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
)

// challengeState is what one received 401/407 means for this step. It holds the
// resolved credentials, so it is NEVER formatted with %v, %+v or %#v and never
// reaches an error string: only Realm and Algorithm, both the peer's own
// values, are quoted in a diagnosis.
type challengeState struct {
	Status  int // the challenging response's own status: 401 or 407
	Pair    challengeHeaderPair
	Chosen  Challenge
	Offered []Challenge
	Creds   Credentials
	Blocked string // "" when a retry should be attempted; otherwise why it cannot be
}

// inspectChallenge decides, for one received final response, whether this step
// can answer it. The second return is false when the response is not an
// authentication challenge at all, which is the overwhelmingly common case and
// costs one map lookup: credentials are resolved only once a challenge exists,
// so an unauthenticated scenario never touches the credential sources.
//
// It returns (state, true) with a non-empty Blocked for a challenge Cellrift
// will not answer, because that case still needs a diagnosis appended to the
// unchanged 2xx-gate failure. Blocked is a string, not an error: the step's own
// failure is already decided by the status gate.
func (a *Adapter) inspectChallenge(msg *SIPMessage, config map[string]string) (challengeState, bool) {
	pair, ok := challengeHeaders[msg.StatusCode]
	if !ok {
		return challengeState{}, false
	}
	st := challengeState{Status: msg.StatusCode, Pair: pair, Offered: parseChallenges(pair.Challenge, msg.Headers[pair.Challenge])}
	chosen, usable := selectChallenge(st.Offered)
	if !usable {
		st.Blocked = "no usable challenge offered (" + describeOfferedChallenges(st.Offered) + ")"
		return st, true
	}
	st.Chosen = chosen
	st.Creds, st.Blocked = resolveForChallenge(chosen, config)
	a.recordSecret(st.Creds.Password)
	return st, true
}

// recordSecret stores a credential-bearing string for the run's masker. It is
// called at the two moments such a string comes into existence: when
// credentials are resolved (the password) and when a credential header value is
// built.
//
// The credential header is recorded as well as the password. The digest
// response= is not the password, but a captured digest together with the
// username, realm and nonce is material. Recording it has no test behind it
// that a reporter alone could fail: no reporter echoes a request, so the header
// value can reach a report only if a peer reflects it back; it is defence in
// depth, exercised by the reflecting-peer end-to-end test.
//
// An empty string is skipped and a repeat is ignored, the same two rules the
// reporter's Masker applies and for the same reasons: an empty secret would
// garble a report, and a password resolved once per step should register once.
func (a *Adapter) recordSecret(s string) {
	if s == "" {
		return
	}
	a.secretsMu.Lock()
	defer a.secretsMu.Unlock()
	for _, have := range a.secrets {
		if have == s {
			return
		}
	}
	a.secrets = append(a.secrets, s)
}

// Secrets implements common.SecretSource. It returns a copy, so a caller cannot
// alias the adapter's list.
func (a *Adapter) Secrets() []string {
	a.secretsMu.Lock()
	defer a.secretsMu.Unlock()
	return append([]string(nil), a.secrets...)
}

var _ common.SecretSource = (*Adapter)(nil)

// resolveForChallenge resolves credentials for one chosen challenge. It yields
// the credentials, or a Blocked reason quoting only the challenge's own realm
// and algorithm. A missing configuration (ErrNoCredentials) is the benign case;
// any other error is a real misconfiguration, such as an unreadable
// credentials_file, and is reported as itself, never relabelled "no credentials
// are configured".
func resolveForChallenge(c Challenge, config map[string]string) (Credentials, string) {
	creds, err := ResolveCredentialsFromConfig(config)
	switch {
	case err == nil:
		return creds, ""
	case errors.Is(err, ErrNoCredentials):
		return Credentials{}, fmt.Sprintf("the peer challenged (realm=%q, algorithm=%s) and no credentials are configured", c.Realm, c.Algorithm)
	default:
		return Credentials{}, "the peer challenged but the configured credentials could not be loaded: " + err.Error()
	}
}

// answerChallenge sends ONE retry and reads its final response. Exactly one
// retry, ever: a peer that challenges the retry has rejected the credentials,
// and looping would turn a wrong password into a request storm. orig is the
// ORIGINAL REQUEST, not the challenge.
//
// The retry is Cellrift's own protocol machinery, so impair.Clear runs first
// and the bytes are plain Serialise output, not serialiseWithMalform: the first
// request carries the step's declared impairment and malformation because the
// author asked for it, and a malform: block aimed at that request must not
// corrupt the retry. The read shares the step's single absolute deadline: a
// transparent retry is internal to a step and must not double timeout_ms.
func (a *Adapter) answerChallenge(start time.Time, target string, deadline time.Time,
	orig *SIPMessage, st challengeState) (*SIPMessage, []byte, *common.StepResult) {
	retry, err := a.buildRetryRequest(orig, st)
	if err != nil {
		return nil, nil, stampAuthFields(failedStep(start, "digest retry: %s", err), st, false)
	}
	impair.Clear(a.transport)
	if err := a.transport.Send(Serialise(retry), target); err != nil {
		return nil, nil, stampAuthFields(failedStep(start, "digest retry send failed: %s", err), st, false)
	}
	n, _, _ := parseCSeq(retry.Headers[HeaderCSeq])
	msg, raw, errResult := a.waitForFinalResponse(start, deadline, n)
	if errResult != nil {
		stampAuthFields(errResult, st, true) // the retry went out; only its answer is missing
	}
	return msg, raw, errResult
}

// buildRetryRequest clones orig, takes the CSeq from ITS OWN header plus one,
// generates a fresh Via branch, and adds the credential header. Everything else
// (Call-ID, From, To without the challenge's tag, Request-URI, Contact, body)
// is reused, as RFC 3261 §22.2 requires for a re-sent request.
//
// It does NOT use DialogManager.NextCSeq(): that returns the next CSeq of the
// first dialog in StateEstablished and a bare 1 when there is none, and a
// REGISTER never establishes a dialog, so every retry would reuse CSeq 1,
// which §22.2 forbids and which yields a second challenge instead of a 200.
//
// A credential header the author declared as a step field is REPLACED: silently
// merging two credentials is worse than replacing, and erroring would fail a
// scenario that deliberately sent a stale credential to provoke the challenge.
func (a *Adapter) buildRetryRequest(orig *SIPMessage, st challengeState) (*SIPMessage, error) {
	n, method, err := parseCSeq(orig.Headers[HeaderCSeq])
	if err != nil {
		return nil, err
	}
	p := digestParamsFor(orig, st)
	response, err := computeDigestResponse(p)
	if err != nil {
		return nil, err
	}
	m := cloneMessage(orig)
	m.Headers[HeaderCSeq] = fmt.Sprintf("%d %s", n+1, method)
	m.Headers[HeaderVia] = withFreshBranch(orig.Headers[HeaderVia])
	m.Headers[st.Pair.Credential] = formatAuthorization(p, response, st.Chosen.Opaque)
	a.recordSecret(m.Headers[st.Pair.Credential])
	return m, nil
}

// digestParamsFor is the only place nc, cnonce and the single qop token are
// chosen. The URI is the request line's own string, after any target rewriting,
// and NOT the To header: RFC 3261 §22.4 item 3 makes digest-uri the
// Request-URI. A uri differing from the request line is one of the deviations
// the pinned Asterisk accepts, which is why a unit test, not an interop run,
// pins it. nc is 1 because exactly one request is ever sent per nonce.
func digestParamsFor(orig *SIPMessage, st challengeState) DigestParams {
	return DigestParams{
		Algorithm: st.Chosen.Algorithm, Username: st.Creds.Username,
		Realm: st.Chosen.Realm, Password: st.Creds.Password,
		Method: orig.Method, URI: orig.RequestURI, Nonce: st.Chosen.Nonce,
		NC: formatNC(1), CNonce: newCNonce(), QOP: "auth",
	}
}

// newCNonce returns a fresh client nonce: 8 random bytes as 16 lowercase hex
// characters. It must differ per request (RFC 7616 §3.4 names the cnonce a
// chosen-plaintext defence), hence crypto/rand and not the math/rand that
// generateBranch uses, since a branch is not a security parameter. It is a
// package variable solely so a test can pin it and compare the digest against
// a literal hex string computed outside this package; production never
// reassigns it.
var newCNonce = func() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// cloneMessage deep-copies headers and body so the original is never mutated.
func cloneMessage(m *SIPMessage) *SIPMessage {
	c := *m
	c.Headers = make(map[string]string, len(m.Headers))
	for k, v := range m.Headers {
		c.Headers[k] = v
	}
	c.Body = append([]byte(nil), m.Body...)
	return &c
}

var viaBranchParam = regexp.MustCompile(`(?i);\s*branch=[^;,\s]*`)

// withFreshBranch swaps only the first branch= parameter of a Via value (the
// topmost Via, the one Cellrift wrote), preserving every other parameter an
// author supplied. A Via with no branch gets one appended.
func withFreshBranch(via string) string {
	fresh := ";branch=" + generateBranch()
	loc := viaBranchParam.FindStringIndex(via)
	if loc == nil {
		return via + fresh
	}
	return via[:loc[0]] + fresh + via[loc[1]:]
}

// parseCSeq splits a "N METHOD" CSeq header. An author-supplied malformed CSeq
// must fail the step with a message, never panic.
func parseCSeq(v string) (int, string, error) {
	f := strings.Fields(v)
	if len(f) != 2 {
		return 0, "", fmt.Errorf("malformed CSeq header %q: want \"<number> <METHOD>\"", v)
	}
	n, err := strconv.Atoi(f[0])
	if err != nil || n < 0 {
		return 0, "", fmt.Errorf("malformed CSeq header %q: %q is not a sequence number", v, f[0])
	}
	return n, f[1], nil
}

// diagnosisKey classifies the end of a challenged step: whether a retry went
// out, and whether the FINAL response was itself a challenge.
type diagnosisKey struct{ Retried, FinalIsChallenge bool }

// diagnosisTable maps a diagnosisKey to the cause appended to the failed
// result. The diagnosis is chosen from the final response's own status, not
// from whether a retry was sent: a retry answered 500 is not a rejected
// credential, and claiming it is sends the reader after the password. Every
// text quotes only the peer's own realm, algorithm, status and reason.
type diagnosisTable map[diagnosisKey]func(st challengeState, final *SIPMessage) string

var challengeDiagnoses = diagnosisTable{
	{Retried: false, FinalIsChallenge: true}:  blockedDiagnosis,
	{Retried: false, FinalIsChallenge: false}: blockedDiagnosis,
	{Retried: true, FinalIsChallenge: true}: func(st challengeState, _ *SIPMessage) string {
		return fmt.Sprintf("credentials rejected after one digest retry (realm=%q, algorithm=%s)", st.Chosen.Realm, st.Chosen.Algorithm)
	},
	{Retried: true, FinalIsChallenge: false}: func(_ challengeState, final *SIPMessage) string {
		return fmt.Sprintf("the digest challenge was answered and the peer then returned %d %s; this is not a credential failure",
			final.StatusCode, final.ReasonPhrase)
	},
}

func blockedDiagnosis(st challengeState, _ *SIPMessage) string { return st.Blocked }

// annotateChallengeFailure appends the CAUSE to a result the 2xx gate has
// already failed. It never changes Status and never runs on a passed result.
func annotateChallengeFailure(result *common.StepResult, st challengeState, retried bool, final *SIPMessage) *common.StepResult {
	if result.Status != common.StatusFailed {
		return result
	}
	_, finalIsChallenge := challengeHeaders[final.StatusCode]
	if d := challengeDiagnoses[diagnosisKey{retried, finalIsChallenge}](st, final); d != "" {
		result.Error += "; " + d
	}
	return result
}

// authResponseKey is the response key a scenario reads as response.auth.*.
const authResponseKey = "auth"

// authQOP is the single qop token Cellrift ever answers with.
const authQOP = "auth"

// stampAuthFields writes the full authentication outcome onto a result and
// returns it. It records the peer's own challenge parameters (realm, nonce,
// algorithm, opaque, header, status) and whether a retry went out, and NOTHING
// derived from a credential: no username, no password, no cnonce, no computed
// response=. A realm and nonce are the peer's own values; a captured digest
// beside them would be material in any report artefact.
//
// Keys: challenged, retried, challenge_header, challenge_status, and, when a
// challenge was selected, realm, nonce, qop, algorithm and (only when the peer
// sent one) opaque. It is named challenge_status, not status_code, because the
// top-level response.status_code is the FINAL response's and two keys of one
// name at different depths would mislead an author.
func stampAuthFields(result *common.StepResult, st challengeState, retried bool) *common.StepResult {
	f := map[string]interface{}{
		"challenged":       true,
		"retried":          retried,
		"challenge_header": st.Pair.Challenge,
		"challenge_status": st.Status,
	}
	addChosenChallenge(f, st.Chosen)
	return setAuthFields(result, f)
}

// addChosenChallenge adds the selected challenge's own parameters. A challenge
// with no usable algorithm was never selected, so nothing is added for it.
func addChosenChallenge(f map[string]interface{}, c Challenge) {
	if c.Algorithm == "" {
		return
	}
	f["realm"], f["nonce"], f["qop"], f["algorithm"] = c.Realm, c.Nonce, authQOP, c.Algorithm
	if c.Opaque != "" {
		f["opaque"] = c.Opaque
	}
}

// ensureAuthChallenged stamps response.auth.challenged == false unless an auth
// map is already present, and returns result. Nil-safe.
//
// This is the funnel that makes the key's always-present guarantee structural,
// as impair.EnsureTimedOut does for response.timed_out: one call on the send
// action's single return path instead of a stamp at each of executeSend's
// StepResult construction sites. Without it `response.auth.challenged == false`
// would be unassertable, since an absent path is absent, not false.
func ensureAuthChallenged(result *common.StepResult) *common.StepResult {
	if result == nil {
		return nil
	}
	if _, ok := result.Response[authResponseKey]; ok {
		return result
	}
	return setAuthFields(result, map[string]interface{}{"challenged": false})
}

// setAuthFields is the one place the auth map is stored. The value MUST be a
// plain map[string]interface{}, never a named map type: the assertion engine's
// dotted-path walk (extractResponseValue in internal/engine) type-asserts every
// nested value to map[string]interface{}, and that assertion is false for a
// named map type, so a named type would make every response.auth.* path read as
// absent instead of failing loudly.
func setAuthFields(result *common.StepResult, f map[string]interface{}) *common.StepResult {
	if result.Response == nil {
		result.Response = common.ResponseFields{}
	}
	result.Response[authResponseKey] = f
	return result
}
