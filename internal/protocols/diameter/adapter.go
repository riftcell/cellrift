package diameter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
)

// Adapter implements common.ProtocolAdapter for Diameter.
type Adapter struct {
	transport common.Transport
	timeout   time.Duration

	// peer tracks the client-only RFC 6733 §5.6 handshake state. Its zero
	// value is peerClosed, so no constructor change is needed.
	peer peer
	// sess is the receive half of the held connection (session.go), built
	// once Connect's transport-level dial succeeds. Nil until the first
	// Connect call — Execute's own dial-per-send path (unchanged by this
	// plan) never touches it.
	sess *session
	// originHost/originRealm are the identity resolved by Connect via
	// ResolveOriginIdentity, used to build the CER and to answer any DWR
	// this session receives.
	originHost  string
	originRealm string
	// peerRealm is the realm the peer itself advertised in its CEA's
	// Origin-Realm AVP (handshake.go's acceptCEA), captured once the
	// handshake completes and used to derive the RFC 6733 §6.6
	// Destination-Realm on every application request that follows
	// (appendDestinationRealm).
	peerRealm string
	// warnings holds the transport-substitution warning returned by
	// NewSCTPOrFallbackTransport, if any, which Warnings() exposes so it
	// reaches the scenario's RunResult.Warnings.
	warnings []string
}

// DefaultOriginHost and DefaultOriginRealm are the identity Cellrift
// presents when neither scenario config nor a step's own fields supply one.
// They are FQDN-shaped so in-repo tests need no configuration, but an
// Origin-Host a real HSS has not provisioned is the single
// highest-likelihood cause of a handshake that fails with no useful error
// text — these defaults will NOT interoperate with real gear. Set
// origin_host/origin_realm explicitly in scenario config against a real
// peer; do not rely on these outside tests.
const (
	DefaultOriginHost  = "cellrift.diameter.invalid"
	DefaultOriginRealm = "diameter.invalid"
)

// ResolveOriginIdentity resolves the Origin-Host/Origin-Realm identity used
// for the CER, in this precedence:
//
//  1. cfg["origin_host"] / cfg["origin_realm"] — what a scenario declares
//     explicitly, and what the interop scenarios set against Open5GS.
//  2. fields["Origin-Host"] / fields["Origin-Realm"] — the identity the
//     scenario already states for the application request. Connect itself
//     only receives cfg (fields is always nil from Connect's own call);
//     executeSend is what merges this fallback into a cfg copy
//     before calling Connect, which is what keeps the many existing tests
//     calling Execute(..., nil) working once the handshake is wired in.
//  3. DefaultOriginHost / DefaultOriginRealm.
//
// Exported so the precedence chain is directly testable without a live
// handshake (TestAdapter_IdentityPrecedence).
func ResolveOriginIdentity(cfg, fields map[string]string) (host, realm string) {
	host = firstNonEmpty(cfg["origin_host"], fields["Origin-Host"], DefaultOriginHost)
	realm = firstNonEmpty(cfg["origin_realm"], fields["Origin-Realm"], DefaultOriginRealm)
	return host, realm
}

// firstNonEmpty returns the first non-empty string in vals.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// NewAdapter creates a Diameter adapter over SCTP, falling back to TCP when
// SCTP is unavailable. The returned warnings slice is non-empty exactly when
// that substitution happened; the caller is expected to attach it to the run
// result so reporters can show which transport actually carried the run.
func NewAdapter(localAddr string, timeout time.Duration) (*Adapter, []string, error) {
	tr, warnings, err := common.NewSCTPOrFallbackTransport(localAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("diameter adapter: %w", err)
	}
	return &Adapter{
		transport: tr,
		timeout:   timeout,
		warnings:  warnings,
	}, warnings, nil
}

// NewAdapterWithTransport creates a Diameter adapter with a pre-built
// transport. This is used both for testing with a specific transport
// instance, and as the constructor a scenario's explicit `transport: tcp`
// takes (via buildDiameterAdapter in cmd/cellrift/main.go) — it is not
// test-only code.
func NewAdapterWithTransport(tr common.Transport, timeout time.Duration) *Adapter {
	return &Adapter{
		transport: tr,
		timeout:   timeout,
	}
}

// Warnings returns run-level observations recorded when this adapter was
// built — currently only a transport substitution. Empty for an adapter built
// over a transport the caller supplied directly.
func (a *Adapter) Warnings() []string { return a.warnings }

// Execute runs a single Diameter step.
//
// Every result a send step produces leaves through EnsureTimedOut, so
// response.timed_out is present on all of them — false unless finishSend
// classified a real peer-silence timeout and stamped true. Stamping here,
// once, rather than at each of executeSend's ten StepResult construction
// sites is what keeps the guarantee true for failure branches added later.
//
// action: raw deliberately does NOT route through EnsureTimedOut: executeRaw
// performs no Receive, so it has no peer-silence outcome to classify, and
// stamping response.timed_out = false on it would imply a wait that never
// happened.
func (a *Adapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	switch action {
	case "send":
		result, err := a.executeSend(target, message, fields, config)
		return impair.EnsureTimedOut(result), err
	case "raw":
		return a.executeRaw(target, config)
	default:
		return nil, fmt.Errorf("diameter adapter: unsupported action %q", action)
	}
}

// executeRaw writes config's declared raw_payload bytes directly onto this
// adapter's own connection, bypassing the Diameter encoder entirely. Three
// deliberate omissions distinguish it from executeSend:
//
//   - it bypasses resolveCommand, the AVP dictionary and BuildRequest
//     entirely — the payload IS the message, which is the whole point of a
//     cross-protocol injection;
//   - it reuses ensureConnected, so a raw step after a normal send writes
//     onto the SAME handshaked RFC 6733 connection, and a raw step with no
//     prior send handshakes first rather than failing — no
//     connect-without-handshake path is invented;
//   - it never calls Receive and never calls ParseMessage: bytes of another
//     protocol have no correlated Diameter answer, and feeding ParseMessage
//     garbage would produce either an error or a misleading field map. A
//     scenario that wants to observe the peer's reaction follows the raw
//     step with an ordinary one.
func (a *Adapter) executeRaw(target string, config map[string]string) (*common.StepResult, error) {
	start := time.Now()

	payload, ok := malform.RawPayload(config)
	if !ok {
		return nil, fmt.Errorf("diameter raw: step declares action: raw but carries no payload")
	}

	impair.Clear(a.transport)
	if err := a.ensureConnected(target, config, nil); err != nil {
		return failedStep(start, "diameter raw: %s", err), nil
	}
	if err := a.transport.Send([]byte(payload), target); err != nil {
		return failedStep(start, "raw send failed: %s", err), nil
	}
	return malform.RawResult(start, len(payload)), nil
}

// prepareRequest resolves the command, runs the automatic handshake, gates
// on peer application support, and builds the request's wire bytes.
// Extracted out of executeSend so executeSend itself stays within CLAUDE.md's
// 40-line cap once impairment wiring is added; resolveCommand stays first —
// TestAdapter_UnknownMessageType expects a Go error for an unknown message
// type before any connection is attempted.
//
// impair.Clear runs here, before ensureConnected, so the CER/CEA handshake —
// which belongs to no scenario step — never pays a step's declared latency,
// and so nothing armed by a previous step survives into this one. That
// second half matters even when this step's own answer never calls
// Receive at all: session.nextMessage's buffered fast path can hand back an
// already-assembled message with zero Receive calls, which is exactly the
// case an armed-but-never-consumed profile would otherwise leak through.
//
// A declared malform: block is applied last, to BuildRequest's already-
// correct output — the encoder itself has no corrupt mode.
func (a *Adapter) prepareRequest(start time.Time, target, msgType string, fields, config map[string]string) (raw []byte, hopByHop uint32, failure *common.StepResult, err error) {
	cmd, err := resolveCommand(msgType)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("diameter send: %w", err)
	}

	impair.Clear(a.transport)

	// A peer rejecting (or never answering) the CER is a protocol-level
	// outcome for THIS step, not an unrecoverable infrastructure failure
	// (CLAUDE.md's adapter convention) — the three distinct handshake error
	// strings from doHandshake survive verbatim into result.Error below.
	if err := a.ensureConnected(target, config, fields); err != nil {
		return nil, 0, failedStep(start, "diameter handshake: %s", err), nil
	}

	if !a.peer.supports(cmd.AppID) {
		return nil, 0, failedStep(start, "peer does not support application %d; CEA advertised %v", cmd.AppID, a.peer.appList()), nil
	}

	avps, err := a.buildRequestAVPs(cmd, fields)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("diameter send: %w", err)
	}

	raw, hopByHop, err = BuildRequest(cmd.Code, cmd.AppID, avps)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("diameter send: build message: %w", err)
	}

	raw, err = applyMalform(raw, config)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("diameter send: malform: %w", err)
	}

	return raw, hopByHop, nil, nil
}

// executeSend sends one application request, transparently handshaking first
// via prepareRequest, then either short-circuits a dropped step before
// anything is armed or written, or arms this step's impairment immediately
// before the bytes go out and delegates the whole write side — the ordinary
// Send (or a short one, for truncate), the duplicate copy, and any declared
// connection-lifecycle fault — to sendBytes. A connection fault fires AFTER
// this step's own Send has already succeeded and BEFORE any Receive is
// attempted, which is what makes the fault attributable to the step that
// actually put bytes on the wire rather than to whichever step runs next.
// RecordDelay is nil-safe and a no-op when nothing was armed.
func (a *Adapter) executeSend(target, msgType string, fields, config map[string]string) (*common.StepResult, error) {
	start := time.Now()

	raw, hopByHop, failure, err := a.prepareRequest(start, target, msgType, fields, config)
	if err != nil {
		return nil, err
	}
	if failure != nil {
		return failure, nil
	}

	// A dropped step builds its request in full — resolveCommand, the
	// handshake, the AVP dictionary and BuildRequest all still run, so an
	// encoding or connection error still fails the step rather than being
	// masked by the impairment — and then never writes those bytes to the
	// wire and never waits for an answer. Returning here, before
	// impair.Configure, is deliberate: nothing is armed on the transport,
	// so there is no profile left behind for the NEXT step to consume. That
	// is a different situation from the armed-but-never-consumed leak the
	// coalesced-fast-path fix addressed, where a Send did happen.
	if impair.DropRequested(config) {
		return impair.DroppedResult(start), nil
	}

	// Arm impairment before the bytes go out; no-op for an unimpaired step.
	if err := impair.Configure(a.transport, config); err != nil {
		return nil, fmt.Errorf("diameter send: %w", err)
	}

	if failure := a.sendBytes(start, raw, target, config); failure != nil {
		return failure, nil
	}

	return a.finishSend(start, hopByHop, config, impair.DuplicateRequested(config))
}

// sendBytes puts this step's already-encoded request on the wire — short, if
// the step declared truncation — re-sends it for a duplicated step, and then
// applies any declared connection-lifecycle fault. It returns nil once every
// required write has succeeded and no fault was declared, or the failed
// StepResult to report otherwise.
//
// Extracted from executeSend so all three write-side impairments live
// together without breaching CLAUDE.md's 40-line cap, and so the two
// distinct fault call sites are visible side by side: truncation REPLACES
// the Send, while reset/abrupt_close/sctp_abort FOLLOW a successful one.
func (a *Adapter) sendBytes(start time.Time, raw []byte, target string, config map[string]string) *common.StepResult {
	truncated, err := impair.SendMaybeTruncated(a.transport, config, raw, target)
	switch {
	case err != nil && truncated:
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, impair.ConnStateTruncated, "truncated send failed: %s", err), 0)
	case err != nil:
		return impair.RecordDelay(a.transport, failedStep(start, "send failed: %s", err), 0)
	case truncated:
		// Truncation closes the connection just as the lifecycle faults do
		// (SendTruncated tears it down), so the same peer-state reset
		// applies — see applyConnFault's doc comment for why BOTH connState
		// and peer state must be cleared.
		a.peer.setState(peerClosed)
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, impair.ConnStateTruncated,
				"wrote %d bytes of a %d-byte request, then closed the connection; no answer was read",
				min(len(raw), impair.TruncatedBytes), len(raw)), 0)
	}

	if _, dupFailure := a.sendDuplicateCopy(start, raw, target, config); dupFailure != nil {
		return dupFailure
	}
	return a.applyConnFault(start, config)
}

// applyConnFault performs the connection-lifecycle fault this step declared,
// AFTER the step's own Send has already succeeded and BEFORE any answer is
// read. Returns the failed StepResult that fault reports, or nil when no
// fault was declared and the step should proceed to its ordinary receive.
//
// Firing after Send is what makes the fault meaningful: the peer really did
// receive a complete request and then watched the connection die under it.
// A fault fired before Send would be indistinguishable from drop.
//
// Two pieces of bookkeeping must be reset, not one. The transport clears its
// own connState (ConnFaultInjector's contract) — without that,
// connState.connect's idempotency check would see a non-nil but dead conn
// for the same addr and skip the redial entirely. And this adapter resets
// peer state, because ensureConnected gates re-handshaking SOLELY on
// peer.requireState(peerOpen) and never consults connState at all; without
// it the NEXT step would skip Connect and fail on a connection this step
// deliberately destroyed, reporting THIS step's fault against a later,
// innocent one.
//
// Peer state is reset whenever the connection was actually torn down, which
// is every outcome EXCEPT common.ErrConnFaultNotAttempted (a refusal that
// never touched the connection, e.g. SCTP's ResetConn) — not only the err ==
// nil case. SCTPTransport.AbortConn's own conns.dropConn() runs
// unconditionally, so its SCTPWrite can report a real, kernel-dependent
// error while the connection is already gone; gating the peer-state reset on
// err == nil alone would leave peer state Open against a connection that no
// longer exists, exactly the reconnection desync already fixed once for
// the success path, now reachable from the error path too.
//
// The step deliberately does NOT attempt a Receive afterwards. For Diameter
// alone, attempting one would be harmless — receiveFromHeldConn returns
// "connection to X is gone, reconnect required" immediately. But the SIP
// adapter's transport has no held connection (addr stays empty), so after
// dropConn its Receive falls into TCPTransport's listener-Accept branch and
// blocks for the FULL step timeout before reporting an accept failure that
// says nothing about the fault. Reporting the fault directly, in both
// adapters, is deterministic, fast, and identical across protocols.
func (a *Adapter) applyConnFault(start time.Time, config map[string]string) *common.StepResult {
	state, err := impair.ApplyConnFault(a.transport, config)
	if state == "" {
		return nil
	}
	if !errors.Is(err, common.ErrConnFaultNotAttempted) {
		a.peer.setState(peerClosed)
	}
	if err != nil {
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, state, "connection fault (%s) failed to arm: %s", state, err), 0)
	}
	return impair.RecordDelay(a.transport,
		impair.FaultedResult(start, state, "connection deliberately faulted (%s) after send; no answer was read", state), 0)
}

// sendDuplicateCopy re-sends the SAME already-built bytes when config
// declares impair: { duplicate: true }: same Hop-by-Hop ID, same
// End-to-End ID, byte-identical on the wire, which is what makes a peer's
// RFC 6733 §9.3 duplicate-request detection the thing under test. Exactly
// one receive cycle follows, in finishSend — no correlation change is
// needed: a single request means there is no "which answer matches which
// request" ambiguity, and if the peer answers twice, the second answer
// carries the same, already-consumed Hop-by-Hop ID and is discarded by
// whichever receiveAnswer runs next — the existing discard behaviour
// working correctly, not a gap. Only the FIRST Send (executeSend's own,
// already made before this is called) pays any declared latency/jitter:
// impair.Configure arms the transport once, that first Send consumes the
// arming, and this second Send fires immediately — a consequence of the
// one-shot arming design, pinned by a test rather than left to be
// discovered. Returns whether duplication was requested, plus a non-nil
// failure result if the second Send itself failed — distinguishable from a
// first-send failure by its own error text.
func (a *Adapter) sendDuplicateCopy(start time.Time, raw []byte, target string, config map[string]string) (bool, *common.StepResult) {
	if !impair.DuplicateRequested(config) {
		return false, nil
	}
	if err := a.transport.Send(raw, target); err != nil {
		return true, impair.RecordDelay(a.transport, failedStep(start, "duplicate send failed: %s", err), 0)
	}
	return true, nil
}

// stepTimeout resolves the answer-wait budget for one step: the step's own
// timeout_ms when declared, otherwise the adapter's default. The scenario
// schema and validator accepted timeout_ms on any step, but this adapter
// never read it back — a Diameter step declaring timeout_ms: 500 against a
// silent peer waited the full run-level default while SIP honoured the
// same key. Mirrors sip.Adapter.stepTimeout so the key means one thing on
// both protocols. Connect is deliberately not bounded here: a dial timeout
// is a transport concern, governed by CELLRIFT_TCP_CONNECT_TIMEOUT_MS /
// CELLRIFT_SCTP_CONNECT_TIMEOUT_MS.
func (a *Adapter) stepTimeout(config map[string]string) time.Duration {
	if ms, ok := config["timeout_ms"]; ok {
		if v, err := strconv.Atoi(ms); err == nil && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return a.timeout
}

// finishSend completes a send once the wire bytes (and, for a duplicated
// step, the second identical copy) are out: it waits for the correlated
// answer, applies delay_ms, and stamps response.duplicated on a successful
// result only — a failed step, whose answer never arrived, has nothing
// useful to say about duplication. Extracted out of executeSend so the
// drop/duplicate branches there could be added without breaching CLAUDE.md's
// 40-line function cap.
func (a *Adapter) finishSend(start time.Time, hopByHop uint32, config map[string]string, duplicate bool) (*common.StepResult, error) {
	respMsg, respRaw, err := a.sess.receiveAnswer(hopByHop, time.Now().Add(a.stepTimeout(config)))
	if err != nil {
		// A peer-initiated close (the peer could not frame or parse what was
		// sent, and closed the connection on its own initiative — never a
		// scenario-declared impair fault) drops connState's own held
		// connection already (common.IsDeadConnErr, checked inside
		// receiveFromHeldConn). Without also resetting peer state here, the
		// NEXT step's ensureConnected would skip reconnecting — it gates
		// solely on peer.requireState(peerOpen) — and fail forever against a
		// connection this step already lost, the same reconnection desync
		// applyConnFault fixes for the self-inflicted-fault path.
		if common.IsDeadConnErr(err) {
			a.peer.setState(peerClosed)
		}
		result := impair.MarkTimedOut(failedStep(start, "receive timeout: %s", err), answerTimedOut(err))
		return impair.RecordDelay(a.transport, result, 0), nil
	}

	// delay_ms lands here: after a complete answer has been received,
	// correlated by Hop-by-Hop ID, and parsed — never inside the transport,
	// which only ever sees byte chunks and cannot know a whole message
	// exists. Deliberately not clamped to the step timeout, unlike the
	// receive-side latency the transport applies: its purpose is to hold a
	// good answer long enough to exercise the peer's own retry behaviour.
	postDelay := impair.PostReceiveDelay(config)

	result := buildStepResult(start, respMsg, respRaw)
	if duplicate {
		result = impair.MarkDuplicated(result)
	}
	return impair.RecordDelay(a.transport, result, postDelay), nil
}

// answerTimedOut reports whether err means the peer simply never answered
// within this step's own timeout_ms, as opposed to any other receive-side
// failure reaching the same catch-all branch (a dead connection, a codec
// error inside receiveAnswer, a watchdog-answer failure).
//
// Both clauses are load-bearing. errAnswerTimeout fires only when
// receiveAnswer's OWN outer budget expires between nextMessage calls — the
// "peer answered a few DWRs and then went silent" case. In the ordinary
// "peer never answers at all" case, nextMessage's single Receive burns the
// whole budget and its raw, unwrapped transport deadline error propagates
// here untouched, satisfying os.ErrDeadlineExceeded but not errAnswerTimeout.
// Checking either one alone silently misclassifies the other case.
func answerTimedOut(err error) bool {
	return common.IsTimeoutErr(err) || errors.Is(err, errAnswerTimeout)
}

// appList returns the peer's currently advertised applications, for the
// unsupported-application gate's error message naming what the CEA actually
// advertised. Defined here (adapter.go) rather than peer.go — Go permits a type's methods to live in any file of the
// same package.
func (p *peer) appList() []uint32 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.apps
}

// ensureConnected is the automatic-handshake hook: the handshake fires
// automatically on the first Diameter step, since no scenario syntax exists
// to trigger it explicitly. It is a no-op once the peer is Open, so a second
// step on the same adapter never re-handshakes. Otherwise it merges fields'
// Origin-Host / Origin-Realm into a COPY of config (fields take precedence over config is
// NOT the rule here — Connect's own ResolveOriginIdentity already prefers
// cfg over fields; this merge only fills in cfg keys Connect would otherwise
// never see, since Connect(_, nil-fields) always resolves to the package
// default). The caller's config map is never mutated.
func (a *Adapter) ensureConnected(target string, config, fields map[string]string) error {
	if a.peer.requireState(peerOpen) == nil {
		return nil // already handshaked against this adapter — idempotent
	}

	cfg := make(map[string]string, len(config)+2)
	for k, v := range config {
		cfg[k] = v
	}
	if _, ok := cfg["origin_host"]; !ok {
		if h := fields["Origin-Host"]; h != "" {
			cfg["origin_host"] = h
		}
	}
	if _, ok := cfg["origin_realm"]; !ok {
		if r := fields["Origin-Realm"]; r != "" {
			cfg["origin_realm"] = r
		}
	}

	return a.Connect(target, cfg)
}

// failedStep builds a failed StepResult with a formatted message, extracted
// per CLAUDE.md's no-copy-pasted-error-blocks rule: the same four-line
// StepResult literal would otherwise appear five times in this file.
func failedStep(start time.Time, format string, args ...any) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusFailed,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      fmt.Sprintf(format, args...),
	}
}

// resultVerdict is an answer's outcome and where it came from. RFC 6733
// gives an answer two possible verdict fields, and a Cx answer uses the
// second one exclusively, so "what did the peer say" is not a single AVP
// lookup.
type resultVerdict struct {
	Code         uint32
	Experimental bool
}

// Success reports whether the code is in RFC 6733 §7.1.3's 2xxx Success
// class. The class, not the exact value, decides: the base protocol's 2001
// DIAMETER_SUCCESS and 3GPP's 2001 DIAMETER_FIRST_REGISTRATION are
// different codes in different namespaces, and both mean the request
// succeeded.
func (v resultVerdict) Success() bool {
	return v.Code >= 2000 && v.Code < 3000
}

// Error describes a non-success verdict, naming which field carried it so a
// 3GPP application error is never mistaken for a base-protocol one.
func (v resultVerdict) Error() string {
	if v.Experimental {
		return fmt.Sprintf("Experimental-Result-Code %d (expected a 2xxx success code)", v.Code)
	}
	return fmt.Sprintf("Result-Code %d (expected %d)", v.Code, ResultSuccess)
}

// answerVerdict returns the answer's outcome, preferring the base
// Result-Code and falling back to the Experimental-Result-Code an
// application reports its own results through (RFC 6733 §7.6/§7.7).
//
// The fallback is not a nicety. A real Open5GS HSS answers a successful Cx
// UAR with Experimental-Result-Code 2001 DIAMETER_FIRST_REGISTRATION and NO
// base Result-Code (test/interop/EVIDENCE.md, run 4). Cellrift read only
// Result-Code, found nothing, and reported the step PASSED — so the same
// peer answering 5001 DIAMETER_ERROR_USER_UNKNOWN in that field would also
// have produced a green step and exit 0. That is the false-pass class this
// project exists to eliminate, in the protocol family it is built for.
//
// The second return is false when the answer carries no verdict at all, in
// which case there is nothing to judge and the step's own assertions decide.
func answerVerdict(msg *Message) (resultVerdict, bool) {
	if rc := msg.ResultCode(); rc != 0 {
		return resultVerdict{Code: rc}, true
	}
	if rc, ok := experimentalResultCode(msg); ok {
		return resultVerdict{Code: rc, Experimental: true}, true
	}
	return resultVerdict{}, false
}

// experimentalResultCode digs the Experimental-Result-Code out of the
// Experimental-Result group (RFC 6733 §7.6), which carries a Vendor-Id
// naming the namespace alongside the code itself.
func experimentalResultCode(msg *Message) (uint32, bool) {
	group := msg.FindAVP(AVPCodeExperimentalResult)
	if group == nil {
		return 0, false
	}
	decoded := *group
	if err := DecodeGroupedAVP(&decoded); err != nil {
		return 0, false
	}
	for _, member := range decoded.GroupedAVPs {
		if member.Code == AVPCodeExperimentalResultCode && len(member.Data) == 4 {
			return binary.BigEndian.Uint32(member.Data), true
		}
	}
	return 0, false
}

// buildStepResult turns a successfully-received answer into a passed (or
// verdict-failed) StepResult, capturing the raw bytes and parsed fields
// scenarios assert against.
func buildStepResult(start time.Time, respMsg *Message, respRaw []byte) *common.StepResult {
	result := &common.StepResult{
		Status:      common.StatusPassed,
		DurationMs:  time.Since(start).Milliseconds(),
		RawResponse: respRaw,
		Response:    responseFieldsMap(respMsg),
	}

	if verdict, ok := answerVerdict(respMsg); ok && !verdict.Success() {
		result.Status = common.StatusFailed
		result.Error = verdict.Error()
	}

	return result
}

// Connect establishes and holds a connection to target for the life of the adapter,
// when the underlying transport supports it (common.Connector). Diameter is
// connection-oriented: RFC 6733 §5.3 requires the CER/CEA capabilities exchange and
// every application request that follows it to happen on the same connection.
//
// cfg widens Connect because cfg wins over fields, which win over the
// default identity (see ResolveOriginIdentity) — origin_host/origin_realm,
// already plumbed from scenario YAML through internal/engine/runner.go, are
// read here for the first time.
//
// Connect is idempotent: a second call to a target that has already completed the
// handshake (peer is Open) skips doHandshake entirely — the transport-level
// Connect is itself idempotent (connState.connect), and re-sending a CER onto an
// already-Open peer would be a bug, not a retry (peer.requireState's contract,
// which doHandshake checks first).
//
// executeSend reaches Connect through ensureConnected, so the handshake runs
// automatically on a scenario's first Diameter step.
func (a *Adapter) Connect(target string, cfg map[string]string) error {
	c, ok := a.transport.(common.Connector)
	if !ok {
		return fmt.Errorf("diameter connect: transport %T does not support connection establishment (Diameter requires a connection-oriented transport)", a.transport)
	}
	if err := c.Connect(target); err != nil {
		return fmt.Errorf("diameter connect %q: %w", target, err)
	}

	a.originHost, a.originRealm = ResolveOriginIdentity(cfg, nil)

	if a.peer.requireState(peerClosed) != nil {
		return nil // already handshaked (or in progress) against target — idempotent no-op
	}
	return a.doHandshake(target)
}

// Close releases adapter resources.
func (a *Adapter) Close() error {
	return a.transport.Close()
}

// CommandDef is one entry in the Diameter command registry: the command
// code and Application-Id a scenario's message type resolves to.
type CommandDef struct {
	Code  uint32
	AppID uint32
	// NeedsAuthSessionState requests the RFC 6733 §8.11 Auth-Session-State
	// AVP be injected when the scenario did not supply one. True for every
	// command whose ABNF marks it mandatory. UAR was excluded when the
	// registry was written, on the grounds that adding an AVP would change
	// bytes a regression test pinned — but TS 29.229 §6.1.1 marks
	// Auth-Session-State mandatory in the UAR too, so the pin was pinning a
	// defect. The pin was updated instead. CCR keeps it false: RFC 4006
	// §3.1's CCR ABNF does not list Auth-Session-State among its
	// mandatory AVPs.
	NeedsAuthSessionState bool
}

// CommandRegistry maps a canonical (upper-case) scenario message type to
// the command/application pair it is sent as. Table-driven rather than a
// switch: the case count grows from two to six here, and two of the old
// switch's cases existed only to hand-spell a lower-case variant, which a
// single strings.ToUpper canonicalisation removes entirely.
type CommandRegistry map[string]CommandDef

var commandRegistry = CommandRegistry{
	"UAR": {Code: CmdUserAuthorization, AppID: AppIDCx, NeedsAuthSessionState: true},
	"CCR": {Code: CmdCreditControl, AppID: AppIDCreditControl},
	"AIR": {Code: CmdAuthenticationInformation, AppID: AppIDS6a, NeedsAuthSessionState: true},
	"ULR": {Code: CmdUpdateLocation, AppID: AppIDS6a, NeedsAuthSessionState: true},
	"MAR": {Code: CmdMultimediaAuth, AppID: AppIDCx, NeedsAuthSessionState: true},
	"SAR": {Code: CmdServerAssignment, AppID: AppIDCx, NeedsAuthSessionState: true},
}

// resolveCommand maps a scenario message type to its registry entry.
func resolveCommand(msgType string) (CommandDef, error) {
	def, ok := commandRegistry[strings.ToUpper(msgType)]
	if !ok {
		return CommandDef{}, fmt.Errorf("unknown diameter message type %q", msgType)
	}
	return def, nil
}

// avpFieldEncoder builds one AVP from a scenario field's string value, given
// the AVP's dictionary definition.
type avpFieldEncoder func(def AVPDefinition, val string) (AVP, error)

// AVPFieldEncoders maps a declared AVP data type to the encoder that can
// build an AVP of that type from a scenario field's string value. It is the
// encode path's capability table: a type present here can be produced from a
// scenario's `fields:` map, a type absent from it is recorded in the
// dictionary but cannot yet be built, and naming one is an error rather than
// a silent omission.
type AVPFieldEncoders map[AVPKind]avpFieldEncoder

// avpFieldEncoders is the single populated instance of that table.
//
// Deliberate scope boundary: the dictionary records many more data types
// than this table can build. Grouped is now buildable from a nested
// scenario field — see GroupDottedFields/encodeGroupedField (grouped.go) and
// appendGroupedAVPs below, which handle it outside this table entirely
// since its encoder shape (multiple sub-fields, not one string) does not
// fit avpFieldEncoder's signature. Time, Address, 64-bit and IPFilterRule
// still have no encoder; naming one of those is still an explicit error.
var avpFieldEncoders AVPFieldEncoders = map[AVPKind]avpFieldEncoder{
	AVPKindOctetString:      encodeStringField,
	AVPKindUTF8String:       encodeStringField,
	AVPKindDiameterIdentity: encodeStringField,
	AVPKindDiameterURI:      encodeStringField,
	AVPKindUnsigned32:       encodeUint32Field,
	AVPKindInteger32:        encodeUint32Field,
	AVPKindEnumerated:       encodeUint32Field,
}

// encodeStringField builds a string-typed AVP. newScopedStringAVP (avp.go)
// is the single lookup site deciding vendor vs. base encoding: a 3GPP Cx field like Public-Identity sent plain-M is what a
// real Open5GS peer rejected with 5001 DIAMETER_AVP_UNSUPPORTED.
func encodeStringField(def AVPDefinition, val string) (AVP, error) {
	return newScopedStringAVP(def.Code, val), nil
}

// encodeUint32Field builds a four-byte big-endian AVP. Unsigned32, Integer32
// and Enumerated share this encoder because all three are four big-endian
// bytes on the wire. Routed through newScopedUint32AVP, not bare
// NewUint32AVP, for the same reason encodeStringField routes through
// newScopedStringAVP: a vendor-scoped numeric AVP (e.g. RAT-Type) sent
// plain-M lands in the wrong namespace on a real peer.
func encodeUint32Field(def AVPDefinition, val string) (AVP, error) {
	v, err := strconv.ParseUint(val, 10, 32)
	if err != nil {
		return AVP{}, fmt.Errorf("field %s: invalid uint32 %q: %w", def.Name, val, err)
	}
	return newScopedUint32AVP(def.Code, uint32(v)), nil
}

// buildAVPsFromFields converts step fields into Diameter AVPs, resolving
// every field name through the AVP dictionary — the dictionary is the only
// place a field name maps to a code. An unknown name or a type this package
// cannot yet encode is an error, not a silent drop. Dotted-path keys
// (produced by scenario.StepFields flattening a nested `fields:` mapping)
// are regrouped by GroupDottedFields and encoded as Grouped AVPs separately
// from the flat scalar loop, since a Grouped AVP has no single string value
// for avpFieldEncoder's signature to take.
func buildAVPsFromFields(fields map[string]string) ([]AVP, error) {
	flat, grouped := GroupDottedFields(fields)
	avps, err := buildScalarAVPs(flat)
	if err != nil {
		return nil, err
	}
	return appendGroupedAVPs(avps, grouped)
}

// buildScalarAVPs is the original per-field encode loop: every AVP kind
// with an entry in avpFieldEncoders, built one field at a time.
func buildScalarAVPs(fields map[string]string) ([]AVP, error) {
	var avps []AVP
	for key, val := range fields {
		// The executor injects body content into every protocol's field map
		// under reserved __body_* keys (internal/engine/executor.go). Those
		// are never AVP names, so a Diameter step that carries a body must
		// not be rejected as naming three unknown AVPs.
		if strings.HasPrefix(key, "__") {
			continue
		}
		def, ok := LookupAVP(key)
		if !ok {
			return nil, fmt.Errorf("unknown Diameter AVP %q", key)
		}
		enc, ok := avpFieldEncoders[def.Kind]
		if !ok {
			return nil, fmt.Errorf("AVP %q has type %s, which cannot yet be built from a scenario field", key, def.Kind)
		}
		avp, err := enc(def, val)
		if err != nil {
			return nil, err
		}
		avps = append(avps, avp)
	}
	return avps, nil
}

// appendGroupedAVPs encodes each dotted-field group (regrouped by
// GroupDottedFields) into one Grouped AVP and appends it to avps. Prefixes
// are sorted before encoding so the emitted AVP order is deterministic —
// Go randomises map iteration order, and a flaky wire order would make
// byte-level assertions unreliable.
func appendGroupedAVPs(avps []AVP, groups DottedFieldGroups) ([]AVP, error) {
	prefixes := make([]string, 0, len(groups))
	for prefix := range groups {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	for _, prefix := range prefixes {
		def, ok := LookupAVP(prefix)
		if !ok {
			return nil, fmt.Errorf("unknown Diameter AVP %q", prefix)
		}
		if !isGroupedKind(def.Kind) {
			return nil, fmt.Errorf("AVP %q is not a Grouped AVP and cannot carry members", prefix)
		}
		avp, err := encodeGroupedField(def, groups[prefix])
		if err != nil {
			return nil, err
		}
		avps = append(avps, avp)
	}
	return avps, nil
}

// isGroupedKind reports whether an AVPKind is Grouped. A one-line named
// predicate rather than an inline comparison, since two call sites ask the
// same "is this kind buildable, and how" question: buildScalarAVPs' fallback
// error treats Grouped the same as every other kind avpFieldEncoders has no
// entry for (a scenario naming a Grouped AVP as a flat field, not a nested
// one, still gets that error), and appendGroupedAVPs' guard above needs the
// positive form of the same check before routing a dotted-field prefix into
// the Grouped-AVP encode path.
func isGroupedKind(k AVPKind) bool {
	return k == AVPKindGrouped
}

// appendDestinationRealm adds the RFC 6733 §6.6 routing AVP unless the step
// already supplied one (Destination-Realm is a known AVP, so
// buildAVPsFromFields already emitted it) or the peer never advertised a
// realm. A request without it is "non-routable" to a real Diameter agent:
// freeDiameter answered exactly this shape with 3001.
func appendDestinationRealm(avps []AVP, fields map[string]string, peerRealm string) []AVP {
	if fields["Destination-Realm"] != "" || peerRealm == "" {
		return avps
	}
	return append(avps, NewStringAVP(AVPCodeDestinationRealm, peerRealm))
}

// appendAuthSessionState adds RFC 6733 §8.11's Auth-Session-State AVP for
// commands whose ABNF marks it mandatory (cmd.NeedsAuthSessionState), unless
// the step's own fields already name one. Mirrors appendDestinationRealm's
// shape: an AVP a scenario field can already produce is never duplicated by
// this injection path, and UAR/CCR (NeedsAuthSessionState false) are
// untouched — see CommandDef's doc comment for why that split is
// deliberate.
func appendAuthSessionState(avps []AVP, cmd CommandDef, fields map[string]string) []AVP {
	if !cmd.NeedsAuthSessionState || fields["Auth-Session-State"] != "" {
		return avps
	}
	return append(avps, NewUint32AVP(AVPCodeAuthSessionState, AuthSessionStateNoStateMaintained))
}

// appendOriginIdentity adds the Origin-Host (264) and Origin-Realm (296)
// AVPs, unless the step already supplied them. RFC 6733 §6.3 and §6.4 make
// both mandatory in EVERY Diameter message — not merely in the CER, which
// is the only place Cellrift used to put them. A real Open5GS HSS answered
// 5005 DIAMETER_MISSING_AVP with a Failed-AVP naming Origin-Host and
// "the number of occurences (0) is < the rule min (1) for 'Origin-Host'",
// having already accepted the Session-Id and Vendor-Specific-Application-Id
// that earlier runs added. The identity is the one Connect resolved for the
// handshake, so a request and the CER that preceded it always agree about
// who this client is — two different identities on one connection is its
// own interop defect.
func appendOriginIdentity(avps []AVP, host, realm string) []AVP {
	if host == "" {
		host = DefaultOriginHost
	}
	if realm == "" {
		realm = DefaultOriginRealm
	}
	if FindAVP(avps, AVPCodeOriginHost) == nil {
		avps = append(avps, NewStringAVP(AVPCodeOriginHost, host))
	}
	if FindAVP(avps, AVPCodeOriginRealm) == nil {
		avps = append(avps, NewStringAVP(AVPCodeOriginRealm, realm))
	}
	return avps
}

// appendAuthApplicationID adds the Auth-Application-Id (AVP 258) a command
// under a BARE application needs, unless the step supplied one.
//
// The split mirrors appendVendorSpecificApplicationID exactly, and the two
// are mutually exclusive by construction: a vendor-scoped command carries
// its Auth-Application-Id *inside* the Vendor-Specific-Application-Id group,
// so emitting a second one at the top level would be its own interop defect.
// Credit-Control is the bare case, and RFC 4006 §3.1 marks this AVP
// mandatory in the CCR — a real SigScale OCS answers 5005
// DIAMETER_MISSING_AVP without it (test/interop/EVIDENCE.md, run 10).
func appendAuthApplicationID(avps []AVP, appID uint32) []AVP {
	if _, vendorScoped := vendorSpecificAppFor(appID); vendorScoped {
		return avps
	}
	if FindAVP(avps, AVPCodeAuthApplicationID) != nil {
		return avps
	}
	return append(avps, NewUint32AVP(AVPCodeAuthApplicationID, appID))
}

// appendVendorSpecificApplicationID adds AVP 260 for a command sent under a
// vendor-scoped application (Cx: UAR/MAR/SAR, S6a: AIR/ULR), mirroring the
// exact {Vendor-Id, Auth-Application-Id} shape buildCER already sends via
// vendorSpecificApplicationAVPs. A real 3GPP-profiled peer's dictionary
// marks this AVP mandatory inside the request body itself, not just inside
// CER — omitting it here is why UAR was answered 5005
// DIAMETER_MISSING_AVP by a real Open5GS/freeDiameter Cx peer despite the
// CER/CEA handshake having already advertised Cx successfully. Commands
// under a bare application (Credit-Control) correctly get nothing added:
// vendorSpecificAppFor(AppIDCreditControl) reports false.
func appendVendorSpecificApplicationID(avps []AVP, appID uint32) []AVP {
	vsa, ok := vendorSpecificAppFor(appID)
	if !ok {
		return avps
	}
	return append(avps, NewGroupedAVP(AVPCodeVendorSpecificApplicationID, []AVP{
		NewUint32AVP(AVPCodeVendorID, vsa.VendorID),
		NewUint32AVP(AVPCodeAuthApplicationID, vsa.AppID),
	}))
}

// sessionIDCounter is the low-32 half of every generated Session-Id. An
// atomic counter rather than a random draw so two requests built in the
// same second are never assigned the same id.
var sessionIDCounter atomic.Uint32

// sessionIDNonce distinguishes two Cellrift processes that start within the
// same second and generate the same counter values — the counter alone
// restarts at zero with the process. Drawn once, at package init.
var sessionIDNonce = strconv.FormatUint(uint64(rand.Uint32()), 16)

// newSessionID builds an identifier in the format RFC 6733 §8.8 recommends,
// <DiameterIdentity>;<high 32 bits>;<low 32 bits>;<optional>, which the spec
// requires to be globally and eternally unique.
func newSessionID(originHost string) string {
	if originHost == "" {
		originHost = DefaultOriginHost
	}
	return fmt.Sprintf("%s;%d;%d;%s", originHost, time.Now().Unix(), sessionIDCounter.Add(1), sessionIDNonce)
}

// ensureSessionID guarantees the request carries exactly one Session-Id and
// that it is the FIRST AVP in the message, which RFC 6733 §8.8 states
// ("The Session-Id MUST appear immediately following the Diameter Header")
// and every application ABNF this adapter can send — Cx (TS 29.229 §6.1),
// S6a (TS 29.272 §7.2) and Credit-Control (RFC 4006 §3) — marks mandatory.
//
// Cellrift omitted it entirely, and a real Open5GS/freeDiameter Cx peer
// answered UAR with 5005 DIAMETER_MISSING_AVP and "the number of occurences
// (0) is < the rule min (1) for 'Session-Id'". That peer could not even
// build a well-formed answer afterwards: its own UAA has no Session-Id to
// echo, so it failed its own parse rules on the way out.
//
// A Session-Id the scenario supplied is never replaced, only hoisted. That
// is load-bearing for Credit-Control, where CCR-I, CCR-U and CCR-T belong
// to ONE session and must carry one identifier across three separate steps
// — scenarios/diameter/credit_control.yaml declares it as a variable for
// exactly that reason. Generating one per step would silently split the
// session in three.
func ensureSessionID(avps []AVP, originHost string) []AVP {
	for i, avp := range avps {
		if avp.Code != AVPCodeSessionID {
			continue
		}
		if i == 0 {
			return avps
		}
		hoisted := make([]AVP, 0, len(avps))
		hoisted = append(hoisted, avp)
		hoisted = append(hoisted, avps[:i]...)
		return append(hoisted, avps[i+1:]...)
	}
	return append([]AVP{NewStringAVP(AVPCodeSessionID, newSessionID(originHost))}, avps...)
}

// buildRequestAVPs builds an application request's AVP list: the step's own
// fields, plus the routing, application-scoping, and session-state AVPs no
// scenario field supplies. Ordering is load-bearing: this is only correct
// once ensureConnected has already run (executeSend's call site guarantees
// this), since a.peerRealm does not exist until the handshake has completed
// — a future reordering would silently drop the AVP.
func (a *Adapter) buildRequestAVPs(cmd CommandDef, fields map[string]string) ([]AVP, error) {
	avps, err := buildAVPsFromFields(fields)
	if err != nil {
		return nil, err
	}
	avps = appendOriginIdentity(avps, a.originHost, a.originRealm)
	avps = appendDestinationRealm(avps, fields, a.peerRealm)
	avps = appendVendorSpecificApplicationID(avps, cmd.AppID)
	avps = appendAuthApplicationID(avps, cmd.AppID)
	avps = appendAuthSessionState(avps, cmd, fields)
	// Last, so that Session-Id ends up first in the finished list whatever
	// the steps above appended.
	return ensureSessionID(avps, a.originHost), nil
}

// responseFieldsMap builds a ResponseFields map from a Diameter answer for
// capture/assertion.
//
// Every AVP is stamped at BOTH keys: nested under "avp" (the shape existing
// Go-level adapter tests already read directly, e.g.
// result.Response["avp"].(map[string]interface{})["Result-Code"]) AND at the
// map's own top level under its own name (e.g. result.Response["Result-Code"]).
// The top-level copy is load-bearing, not decorative: every built-in
// Diameter scenario's capture/assert convention is the flat form
// (`response.Result-Code`, `response.Server-Name`, `response.CC-Request-Type`
// — see scenarios/diameter/*.yaml and scenarios/ims/register_*.yaml), which
// the engine's extractResponseValue resolves as a single-level lookup with
// no fallback into a nested sub-map. Before this fix every one of those
// scenario asserts silently resolved to nil and would have failed the
// instant it was ever executed against a live mock rather than merely
// validated for shape — a gap invisible until a production-path test
// actually ran one end to end. AVP names never collide with this function's
// own reserved keys (command_code/app_id/is_request/result_code/avp): every
// dictionary AVP name is TitleCase-with-hyphens, none of these are.
func responseFieldsMap(msg *Message) common.ResponseFields {
	fields := common.ResponseFields{
		"command_code": msg.CommandCode,
		"app_id":       msg.AppID,
		"is_request":   msg.IsRequest(),
	}

	avpMap := make(map[string]interface{})
	for _, a := range msg.AVPs {
		name := AVPCodeName(a.Code)
		value := responseAVPValue(a)
		avpMap[name] = value
		fields[name] = value
		addGroupedMembers(fields, avpMap, a)
	}
	fields["avp"] = avpMap

	rc := msg.ResultCode()
	if rc != 0 {
		fields["result_code"] = rc
	}

	return fields
}

// addGroupedMembers flattens a Grouped AVP's members into the same flat
// namespace its parent occupies, so `response.Experimental-Result-Code`
// resolves without any new assertion grammar — the shape every built-in
// scenario already uses for a top-level AVP.
//
// This exists because a real Cx answer puts its ONLY verdict inside a
// Grouped AVP: an Open5GS HSS answers a successful UAR with
// Experimental-Result { Vendor-Id, Experimental-Result-Code 2001 } and no
// base Result-Code at all (test/interop/EVIDENCE.md, run 4). Before this,
// the group decoded to one opaque string and nothing inside it could be
// captured or asserted on.
//
// One level deep, deliberately: every Grouped AVP this client can receive
// today nests exactly once, and a recursive flatten would need a naming
// rule for collisions that no scenario yet needs.
func addGroupedMembers(fields common.ResponseFields, avpMap map[string]interface{}, a AVP) {
	if !isGroupedKind(lookupKind(a.Code)) {
		return
	}
	decoded := a // DecodeGroupedAVP mutates, so never touch the caller's copy
	if err := DecodeGroupedAVP(&decoded); err != nil {
		return
	}
	for _, member := range decoded.GroupedAVPs {
		name := AVPCodeName(member.Code)
		value := responseAVPValue(member)
		if _, taken := fields[name]; taken {
			continue // a top-level AVP of the same name wins
		}
		fields[name] = value
		avpMap[name] = value
	}
}

// lookupKind returns the dictionary Kind for code, or an empty Kind when
// the AVP is not in the dictionary.
func lookupKind(code uint32) AVPKind {
	if def, ok := LookupAVPByCode(code); ok {
		return def.Kind
	}
	return ""
}

// responseAVPValue extracts a's decoded value in the type responseFieldsMap's
// consumers expect: a Uint32Val for Unsigned32 AVPs, its StringVal
// otherwise. Named distinctly from avp.go's own avpValue (re-encoding's
// value extraction, a different shape entirely) so the two are never
// confused; extracted here so the single value computation is never
// duplicated between the nested "avp" map and the flat top-level copy.
func responseAVPValue(a AVP) interface{} {
	if a.DataType == DataTypeUnsigned32 {
		return a.Uint32Val
	}
	return a.StringVal
}
