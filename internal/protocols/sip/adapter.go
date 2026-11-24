package sip

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/riftcell/cellrift/internal/config"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/impair"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
)

// viaTokens maps transport type names to the token used in the SIP Via header.
var viaTokens = map[string]string{
	"udp": "UDP",
	"tcp": "TCP",
	"tls": "TLS",
}

// Adapter implements common.ProtocolAdapter for SIP.
type Adapter struct {
	transport     common.Transport
	dialogs       *DialogManager
	timeout       time.Duration
	transportType string // "udp", "tcp", or "tls"

	// secrets holds the credential-bearing strings this adapter produced, for
	// the run's masker. secretsMu guards it because the sub-steps of a
	// `parallel` group share this one adapter and may each answer a challenge
	// at the same moment.
	secretsMu sync.Mutex
	secrets   []string
}

// NewAdapter creates a SIP adapter with the given transport type and local address.
func NewAdapter(transportType, localAddr string, timeout time.Duration) (*Adapter, error) {
	tr, err := common.NewTransport(transportType, localAddr)
	if err != nil {
		return nil, fmt.Errorf("sip adapter: %w", err)
	}
	return NewAdapterWithTransport(tr, transportType, timeout), nil
}

// NewAdapterTLS creates a SIP adapter using TLS transport.
// tlsCfg controls client cert, server CA verification, and TLS version.
func NewAdapterTLS(localAddr string, tlsCfg *tls.Config, timeout time.Duration) (*Adapter, error) {
	tr, err := common.NewTLSTransport(localAddr, tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("sip adapter tls: %w", err)
	}
	return NewAdapterWithTransport(tr, "tls", timeout), nil
}

// NewAdapterWithTransport creates a SIP adapter over a transport the caller
// has already built. This is the seam the production binary uses when the
// transport needs decorating before the adapter ever sees it — for example
// wrapping it with delivery-timing impairment — and it is also what tests
// use to drive the adapter over a mock socket. It is production code, not a
// test helper, which is why it lives here rather than alongside the mocks.
//
// transportType is the Via header's transport token ("udp", "tcp", or
// "tls"); it must describe what tr actually is, because the Via header a
// peer reads is built from it. An unrecognised value falls back to UDP, the
// same default viaToken has always applied.
func NewAdapterWithTransport(tr common.Transport, transportType string, timeout time.Duration) *Adapter {
	return &Adapter{
		transport:     tr,
		dialogs:       NewDialogManager(),
		timeout:       timeout,
		transportType: transportType,
	}
}

// viaToken returns the Via header transport token (UDP, TCP, or TLS).
func (a *Adapter) viaToken() string {
	if token, ok := viaTokens[a.transportType]; ok {
		return token
	}
	return "UDP"
}

// Execute runs a single SIP step.
//
// Every result a send step produces leaves through EnsureTimedOut, so
// response.timed_out is present on all of them — false unless
// waitForFinalResponse classified a real peer-silence timeout and stamped
// true. Stamping here, once, rather than at each of executeSend's ten
// StepResult construction sites is what keeps the guarantee true for failure
// branches added later.
//
// They also leave through ensureAuthChallenged, so response.auth.challenged is
// present on every one of them (false unless the peer challenged), which is what
// lets a scenario assert the negative.
//
// assert_state is deliberately excluded. Its timeout is a dialog-state poll
// deadline against in-process state this adapter already updated from
// earlier responses — it never touches the wire and has no peer-answer
// concept at all, so reusing the same key for it would make
// response.timed_out mean two different things depending on the action.
//
// raw is excluded from both for the same reason, and neither assert_state nor
// raw carries response.auth: with no peer answer to be challenged by, reusing
// the key would make response.auth.challenged mean two things.
func (a *Adapter) Execute(action, target, message string, fields map[string]string, config map[string]string) (*common.StepResult, error) {
	switch action {
	case "send":
		// The scenario's own declared password is already a literal here (its
		// template was resolved when the step config was built), so it is
		// registered whether or not a challenge ever arrives: an author who
		// typed a password into the scenario must not see it in a report just
		// because the peer did not ask for it. Passwords that come only from a
		// credentials_file or CELLRIFT_SIP_PASSWORD are registered when resolved.
		a.recordSecret(config["password"])
		result, err := a.executeSend(target, message, fields, config)
		result = ensureAuthChallenged(result)
		return impair.EnsureTimedOut(result), err
	case "assert_state":
		return a.executeAssertState(fields, config)
	case "raw":
		// Deliberately NOT routed through EnsureTimedOut: executeRaw performs
		// no receive, so it has no peer-silence outcome to classify — see
		// executeRaw's own doc comment, and the identical note on the
		// Diameter adapter's Execute.
		return a.executeRaw(target, config)
	default:
		return nil, fmt.Errorf("sip adapter: unsupported action %q", action)
	}
}

// executeRaw writes config's declared raw_payload bytes directly onto this
// adapter's own transport, bypassing the SIP encoder entirely. Three
// deliberate omissions distinguish it from executeSend:
//
//   - no buildRequestMessage — the payload IS the message: no Via, no
//     Call-ID, no dialog state is injected, because injecting SIP headers
//     into a cross-protocol payload would defeat the point of action: raw's
//     injection;
//   - no writeRequest — that helper applies the truncate/duplicate
//     impairments, which belong to action: send's vocabulary and are not
//     part of a raw injection;
//   - no waitForFinalResponse — bytes of another protocol have no SIP
//     response to correlate, and ParseMessage on whatever came back would be
//     noise. A scenario that wants to observe the peer's reaction follows
//     the raw step with an ordinary one.
//
// impair.Clear still runs, so nothing a previous step armed leaks into this
// one — the same discipline prepareRequest/executeSend already follow.
func (a *Adapter) executeRaw(target string, config map[string]string) (*common.StepResult, error) {
	start := time.Now()

	payload, ok := malform.RawPayload(config)
	if !ok {
		return nil, fmt.Errorf("sip raw: step declares action: raw but carries no payload")
	}

	impair.Clear(a.transport)
	if err := a.transport.Send([]byte(payload), target); err != nil {
		return failedStep(start, "raw send failed: %s", err), nil
	}
	return malform.RawResult(start, len(payload)), nil
}

// resolveRequestURI determines the Request-URI for a send step. An explicit
// Request-URI field wins; otherwise the To: header's user part is combined
// with target so the Request-URI carries the dialled number
// (e.g. sip:100@127.0.0.1:5060). Without a user part Asterisk extracts an
// empty extension and returns 404.
func resolveRequestURI(target string, fields map[string]string) string {
	if uri, ok := fields["Request-URI"]; ok {
		return uri
	}
	if to, ok := fields["To"]; ok {
		if user := extractSIPUser(to); user != "" {
			return "sip:" + user + "@" + target
		}
	}
	return "sip:" + target
}

// resolveBody expands a named SDP template or takes raw __body_content,
// deleting the internal key the executor injected so it never leaks into
// headers. Returns a failed StepResult if template expansion fails.
func resolveBody(fields map[string]string) ([]byte, *common.StepResult) {
	if tmpl, ok := fields["__body_template"]; ok {
		content, err := SDPTemplate(tmpl)
		if err != nil {
			return nil, &common.StepResult{
				Status: common.StatusFailed,
				Error:  fmt.Sprintf("body template: %s", err),
			}
		}
		delete(fields, "__body_template")
		return []byte(content), nil
	}
	if content, ok := fields["__body_content"]; ok {
		delete(fields, "__body_content")
		return []byte(content), nil
	}
	return nil, nil
}

// buildHeaders assembles the SIP header set for a send step: fields (minus
// __body_* keys), dialog state for in-dialog methods, and required defaults.
func (a *Adapter) buildHeaders(method string, fields map[string]string, bodyBytes []byte) map[string]string {
	headers := make(map[string]string)
	for k, v := range fields {
		if k != "__body_content_type" {
			headers[k] = v
		}
	}
	if ct, ok := fields["__body_content_type"]; ok && len(bodyBytes) > 0 {
		headers[HeaderContentType] = ct
	}

	// Inject dialog state for in-dialog methods (BYE, ACK).
	if method == "BYE" || method == "ACK" {
		a.injectDialogHeaders(method, headers)
	}

	// Fill in required defaults if not already set.
	if _, ok := headers[HeaderCallID]; !ok {
		headers[HeaderCallID] = generateCallID()
	}
	if _, ok := headers[HeaderCSeq]; !ok {
		headers[HeaderCSeq] = "1 " + method
	}
	if _, ok := headers[HeaderVia]; !ok {
		headers[HeaderVia] = fmt.Sprintf("SIP/2.0/%s %s;branch=%s",
			a.viaToken(), a.transport.LocalAddr(), generateBranch())
	}
	return headers
}

// buildRequestMessage resolves the Request-URI, body, and headers for a SIP
// send step and returns the fully built request. Split out of executeSend
// (via resolveRequestURI/resolveBody/buildHeaders) to keep every function
// under CLAUDE.md's line cap; the extraction changes nothing about what gets
// built on the wire.
func (a *Adapter) buildRequestMessage(target, method string, fields map[string]string) (*SIPMessage, *common.StepResult) {
	requestURI := resolveRequestURI(target, fields)

	bodyBytes, errResult := resolveBody(fields)
	if errResult != nil {
		return nil, errResult
	}

	headers := a.buildHeaders(method, fields, bodyBytes)

	msg := BuildRequest(method, requestURI, headers)
	if len(bodyBytes) > 0 {
		msg.Body = bodyBytes
		msg.Headers[HeaderContentLength] = strconv.Itoa(len(bodyBytes))
	}
	return msg, nil
}

// serialiseWithMalform runs Serialise -- the real, unmodified encoder,
// producing a complete and correct message -- and only then applies any
// malform: sip_header corruption declared on this step. Serialise itself
// must never grow a corrupt mode: every corruption this package performs
// operates strictly on Serialise's already-correct output, never before or
// during encoding.
func serialiseWithMalform(msg *SIPMessage, config map[string]string) ([]byte, error) {
	return applyHeaderMalform(Serialise(msg), config)
}

func (a *Adapter) executeSend(target, method string, fields, config map[string]string) (*common.StepResult, error) {
	// Hoisted above buildRequestMessage so a dropped step, which returns
	// before any wire I/O, still reports a real DurationMs.
	start := time.Now()

	msg, errResult := a.buildRequestMessage(target, method, fields)
	if errResult != nil {
		return errResult, nil
	}

	// A dropped step still builds its request in full (a build error still
	// fails the step) but never reaches the wire. Returning before
	// impair.Configure means nothing gets armed for the next step to inherit.
	if impair.DropRequested(config) {
		return impair.DroppedResult(start), nil
	}

	// Arm this step's impairment on every send, impaired or not — this call
	// is what clears anything the previous step armed, since the transport
	// cannot infer on its own that a step has ended.
	if err := impair.Configure(a.transport, config); err != nil {
		return nil, fmt.Errorf("sip send: %w", err)
	}

	raw, err := serialiseWithMalform(msg, config)
	if err != nil {
		return nil, fmt.Errorf("sip send: %w", err)
	}
	if result := a.writeRequest(start, target, raw, config); result != nil {
		return result, nil
	}
	if result := a.applyConnFault(start, config); result != nil {
		return result, nil
	}

	return a.receiveAndFinish(start, target, msg, config), nil
}

// receiveAndFinish waits for the final response and produces the step's
// result. Extracted out of executeSend to keep it under CLAUDE.md's 40-line
// cap once the malform call site was added.
//
// The digest retry sits HERE, between the first wait and finishSend, as a
// second waitForFinalResponse rather than inside the first:
//   - inside waitForFinalResponse it would also run for every reorder member
//     (readSIPReorderMember shares that function) and break the group's
//     positional write/read ordering;
//   - inside the provisional loop it would spend the first transaction's
//     budget on the second;
//   - after finishSend it would run after delay_ms and the duplicate drain.
//
// Here each transaction gets its own provisional budget, and delay_ms and the
// drain still run last, on the FINAL response. Both waits share one absolute
// deadline, so a retry never doubles timeout_ms. req is the request just sent.
func (a *Adapter) receiveAndFinish(start time.Time, target string, req *SIPMessage, config map[string]string) *common.StepResult {
	deadline := time.Now().Add(a.stepTimeout(config))
	respMsg, respRaw, errResult := a.waitForFinalResponse(start, deadline, 0)
	if errResult != nil {
		return impair.RecordDelay(a.transport, errResult, 0)
	}

	st, isChallenge := a.inspectChallenge(respMsg, config)
	retried := isChallenge && st.Blocked == ""
	if retried {
		respMsg, respRaw, errResult = a.answerChallenge(start, target, deadline, req, st)
		if errResult != nil {
			return impair.RecordDelay(a.transport, errResult, 0)
		}
	}

	result := a.finishSend(start, config, respRaw, respMsg)
	if isChallenge {
		result = annotateChallengeFailure(result, st, retried, respMsg)
		result = stampAuthFields(result, st, retried)
	}
	return result
}

// failedStep builds a failed StepResult with a formatted message, mirroring
// the identical helper in internal/protocols/diameter/adapter.go. Only the
// bare Status/DurationMs/Error shape routes through here: the results in
// waitForFinalResponse additionally carry RawResponse and Response and are
// deliberately left as their own literals rather than widened into a
// variadic-option helper nothing else would use.
func failedStep(start time.Time, format string, args ...any) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusFailed,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      fmt.Sprintf(format, args...),
	}
}

// writeRequest sends raw once — deliberately short, when the step declared
// truncation — and, for a duplicated step, sends the exact same bytes again
// immediately afterward. Returns a failed StepResult naming which write
// failed, the truncated step's own outcome, or nil once every required write
// has succeeded.
//
// Truncation REPLACES the ordinary Send rather than following it: there is
// no meaningful "send the whole message, then send it short". It is also the
// one connection fault that works identically over every transport
// including UDP — a short datagram needs no connection lifecycle at all,
// since the datagram boundary is itself the frame boundary.
func (a *Adapter) writeRequest(start time.Time, target string, raw []byte, config map[string]string) *common.StepResult {
	truncated, err := impair.SendMaybeTruncated(a.transport, config, raw, target)
	switch {
	case truncated && err != nil:
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, impair.ConnStateTruncated, "truncated send failed: %s", err), 0)
	case truncated:
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, impair.ConnStateTruncated,
				"wrote %d bytes of a %d-byte request; no response was read",
				min(len(raw), impair.TruncatedBytes), len(raw)), 0)
	case err != nil:
		return impair.RecordDelay(a.transport, failedStep(start, "send failed: %s", err), 0)
	}

	// duplicate re-sends the SAME bytes: same Call-ID, same CSeq, same Via
	// branch, byte-identical on the wire, which is what makes the peer's own
	// retransmission handling (RFC 3261 §17) the thing under test. Only the
	// first Send pays any declared latency/jitter — impair.Configure arms the
	// transport once and the first Send consumes that arming.
	if impair.DuplicateRequested(config) {
		if err := a.transport.Send(raw, target); err != nil {
			return impair.RecordDelay(a.transport, failedStep(start, "duplicate send failed: %s", err), 0)
		}
	}
	return nil
}

// applyConnFault performs the connection-lifecycle fault this step declared,
// AFTER the step's own Send has already succeeded and BEFORE any response is
// read. Returns the failed StepResult that fault reports, or nil when no
// fault was declared.
//
// SCOPE DISCLOSURE — SIP's connection faults are NOT Diameter's. This
// adapter never calls common.Connector.Connect anywhere, so connState.addr
// stays permanently empty and TCPTransport.Send's held-connection branch is
// never taken: every SIP Send dials a brand-new connection, and Receive
// reuses only the socket that same Send just stashed. A fault here can
// therefore only ever affect THIS step's own dial-send-receive cycle. There
// is no cross-step connection for a later step to inherit a stale reference
// to, which means:
//
//   - the misattributed-to-a-later-step concern is structurally impossible
//     for SIP, and needs no peer-state fix of the kind
//     diameter.Adapter.applyConnFault performs — SIP has no handshake state
//     machine to desynchronise;
//   - equally, `impair: { reset: true }` on a SIP step is NOT a mid-session
//     reset in the sense Diameter's is. It resets a connection this step
//     itself opened moments ago. It still exercises the peer's own
//     handling of a request followed by an abrupt connection death, which
//     is the useful part — but do not read it as testing anything about
//     long-lived SIP connection reuse, because this client has none.
//
// This is a genuine, disclosed difference between the two protocols' fault
// semantics, in the same spirit as this adapter's positional-only reordering
// disclosure, not a gap to be quietly closed by a future reader.
//
// The step does NOT attempt a Receive afterwards, and for SIP that is not
// merely a stylistic choice: ConnFaultInjector clears connState, after which
// TCPTransport.Receive sees conn == nil AND connectedAddr == "" and falls
// into its listener-Accept branch, blocking for the entire step timeout
// before returning an accept failure that says nothing about the fault.
// Reporting the fault directly is deterministic, fast, and matches the
// Diameter adapter exactly.
func (a *Adapter) applyConnFault(start time.Time, config map[string]string) *common.StepResult {
	state, err := impair.ApplyConnFault(a.transport, config)
	if state == "" {
		return nil
	}
	if err != nil {
		return impair.RecordDelay(a.transport,
			impair.FaultedResult(start, state, "connection fault (%s) failed to arm: %s", state, err), 0)
	}
	return impair.RecordDelay(a.transport,
		impair.FaultedResult(start, state, "connection deliberately faulted (%s) after send; no response was read", state), 0)
}

// finalResponseResult turns a parsed final response into the step's result,
// gating pass/fail on the SIP status class exactly as Diameter's
// buildStepResult gates on Result-Code. Only a 2xx is success; every other
// final class (3xx redirect, 4xx client error, 5xx server error, 6xx global
// failure) fails the step and names the code in Error. Before this gate the
// step was stamped passed unconditionally, so a peer answering
// "500 Server Internal Error" produced a green step and exit 0 — the exact
// false-PASS a regression gate exists to prevent. A scenario that
// deliberately expects a non-2xx can still express that as an assertion on
// response.status_code.
func finalResponseResult(start time.Time, respRaw []byte, respMsg *SIPMessage) *common.StepResult {
	result := &common.StepResult{
		Status:      common.StatusPassed,
		DurationMs:  time.Since(start).Milliseconds(),
		RawResponse: respRaw,
		Response:    ResponseFieldsMap(respMsg),
	}
	if respMsg.StatusCode < 200 || respMsg.StatusCode >= 300 {
		result.Status = common.StatusFailed
		result.Error = fmt.Sprintf("SIP %d %s (expected 2xx)", respMsg.StatusCode, respMsg.ReasonPhrase)
	}
	return result
}

// finishSend applies delay_ms, drains a duplicated step's stray second
// response, and records the total self-inflicted delay for a completed
// send. Split out of executeSend to keep that function under CLAUDE.md's
// line cap.
//
// delay_ms is applied here, after a complete final response has been
// received and parsed, and is deliberately not bounded by the step timeout:
// its purpose is to make Cellrift sit on a good answer long enough to
// exercise the peer's own timeout and retry behaviour. That is a different
// thing from latency_ms, which the transport applies before the bytes ever
// reach the wire.
func (a *Adapter) finishSend(start time.Time, config map[string]string, respRaw []byte, respMsg *SIPMessage) *common.StepResult {
	postDelay := impair.PostReceiveDelay(config)
	result := impair.RecordDelay(a.transport, finalResponseResult(start, respRaw, respMsg), postDelay)

	// The drain runs AFTER postDelay's sleep, not before: delay_ms exists to
	// hold a good answer, and the peer's stray response to the duplicate's
	// second copy is still sitting in the socket buffer either way — running
	// the drain afterward changes only when it is discarded, not what is
	// discarded.
	if impair.DuplicateRequested(config) {
		a.drainDuplicateEcho()
		result = impair.MarkDuplicated(result)
	}
	return result
}

// drainDuplicateEcho discards the response(s) a peer sent for the SECOND
// copy of a duplicated request, and reports how many it discarded.
//
// It exists because this adapter has no request/response correlation:
// waitForFinalResponse returns whatever Receive produces next, checking only
// StatusCode >= 200. Without this drain, a peer that answers both copies
// leaves one response sitting in the socket buffer, and the NEXT step reads
// it — silently attributing one request's response to a different request.
//
// Bounded twice over: by SIPDuplicateDrainMs, so a peer that says nothing
// costs a fixed budget rather than the full step timeout, and by
// SIPMaxProvisional, the same runaway guard waitForFinalResponse already
// uses, so a peer that answers endlessly cannot hold the step open.
//
// This is best-effort by nature. A peer that answers the duplicate LATER
// than the budget will still leak one response into the next step. That is
// an inherent consequence of SIP's missing correlation, not something this
// drain can fully solve; building real Call-ID/CSeq correlation is a
// separate, larger change affecting every SIP step, not just this one.
func (a *Adapter) drainDuplicateEcho() int {
	deadline := time.Now().Add(config.SIPDuplicateDrainMs())
	drained := 0
	for range config.SIPMaxProvisional() {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return drained
		}
		if _, _, err := a.transport.Receive(remaining); err != nil {
			return drained // timeout or closed — nothing more to discard
		}
		drained++
	}
	return drained
}

// injectDialogHeaders adds Call-ID, To-tag, and CSeq for in-dialog requests
// (BYE, ACK) by reusing the established dialog's state. No-op if there is
// no established dialog or the header is already set.
//
// Without this: new Call-ID → 481 Call/Transaction Does Not Exist;
// duplicate CSeq → 500 Invalid CSeq.
func (a *Adapter) injectDialogHeaders(method string, headers map[string]string) {
	dlg := a.dialogs.EstablishedDialog()
	if dlg == nil {
		return
	}
	if _, ok := headers[HeaderCallID]; !ok {
		headers[HeaderCallID] = dlg.CallID
	}
	if to, ok := headers[HeaderTo]; ok && dlg.ToTag != "" && !strings.Contains(to, "tag=") {
		headers[HeaderTo] = to + ";tag=" + dlg.ToTag
	}
	if _, ok := headers[HeaderCSeq]; !ok {
		headers[HeaderCSeq] = fmt.Sprintf("%d %s", a.dialogs.NextCSeq(), method)
	}
}

// waitForFinalResponse consumes provisional (1xx) responses until a final
// response (2xx–6xx) arrives. A guard of 50 iterations prevents an infinite
// loop if a misbehaving peer sends endless provisionals.
// Returns a non-nil errResult on any transport, parse, or dialog error;
// the caller should return it directly in that case.
//
// The timeout stamp lands on this branch specifically, while err is still a
// typed error: once it has been formatted into StepResult.Error it is a
// plain string and no errors.Is check is possible any more.
//
// deadline is ONE absolute point in time for the whole wait, shared across
// every provisional response, exactly as Diameter's receiveAnswer treats its
// deadline. The previous version handed the full step timeout to every
// Receive call afresh, so a peer trickling 180 Ringing could hold a step for
// SIPMaxProvisional × timeout (25 minutes at a 30 s timeout), and when the
// provisional budget ran out it fell through returning the last 1xx with a
// nil error — which finishSend then stamped passed. A peer stuck in a
// provisional loop looked healthy. Both paths now fail, and both are
// stamped timed_out, honouring the documented contract that exhausting the
// provisional budget IS a timeout.
//
// wantCSeq of 0 is the behaviour every pre-existing caller relies on: take the
// next final response off the wire, whatever it answers. A non-zero wantCSeq
// skips any response whose CSeq NUMBER differs, which is what the digest retry
// needs: this package has no transaction matching, so without it a duplicated
// or retransmitted 401 from the FIRST request reads as the retry's answer and
// the step reports rejected credentials for a password that was never tried.
//
// A skipped response spends one iteration of the same provisional budget. That
// is deliberate: the budget is the bound on "how many things may arrive that
// are not my answer", and an unbounded skip loop would be a new way to hang a
// step.
func (a *Adapter) waitForFinalResponse(start time.Time, deadline time.Time, wantCSeq int) (msg *SIPMessage, raw []byte, errResult *common.StepResult) {
	maxProvisional := config.SIPMaxProvisional()
	for range maxProvisional {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil, timedOutResult(start, true, "receive timeout: step deadline reached while waiting for a final response")
		}
		var err error
		raw, _, err = a.transport.Receive(remaining)
		if err != nil {
			return nil, nil, timedOutResult(start, common.IsTimeoutErr(err), "receive timeout: %s", err)
		}
		msg, errResult = a.parseAndTrackResponse(start, raw)
		if errResult != nil {
			return nil, raw, errResult
		}
		if isOtherTransaction(msg, wantCSeq) {
			continue
		}
		if msg.StatusCode >= 200 {
			return msg, raw, nil
		}
	}
	return nil, raw, timedOutResult(start, true,
		"no final response after %d provisional responses (CELLRIFT_SIP_MAX_PROVISIONAL)", maxProvisional)
}

// isOtherTransaction reports whether msg answers a different CSeq than the one
// wanted. A response whose CSeq cannot be parsed is NOT treated as foreign:
// an unparseable CSeq is the peer's problem, and silently dropping such
// responses would be a new way to hang a step.
func isOtherTransaction(msg *SIPMessage, wantCSeq int) bool {
	if wantCSeq == 0 {
		return false
	}
	n, ok := responseCSeqNumber(msg)
	return ok && n != wantCSeq
}

// responseCSeqNumber returns the number of msg's CSeq header.
func responseCSeqNumber(msg *SIPMessage) (int, bool) {
	n, _, err := parseCSeq(msg.Headers[HeaderCSeq])
	return n, err == nil
}

// parseAndTrackResponse parses one received message and folds it into the
// dialog tracker, returning a failed result for either failure so the
// caller's loop stays a flat sequence of checks.
func (a *Adapter) parseAndTrackResponse(start time.Time, raw []byte) (*SIPMessage, *common.StepResult) {
	msg, err := ParseMessage(raw)
	if err != nil {
		return nil, &common.StepResult{
			Status:      common.StatusFailed,
			DurationMs:  time.Since(start).Milliseconds(),
			RawResponse: raw,
			Error:       fmt.Sprintf("parse response: %s", err),
		}
	}
	// A 401/407 is a challenge, not a dialog termination. UpdateFromResponse
	// maps any status >= 300 to Terminated, and Terminated has no outgoing
	// transitions, so folding a challenge in makes the 200 that answers the
	// retry fail with "invalid dialog transition from terminated to registered"
	// whenever the peer reuses its To-tag. It also made `assert_state:
	// terminated` pass vacuously after any challenge, a false PASS. Every OTHER
	// >= 300 status still terminates: scenarios/ims/call_rejected.yaml asserts
	// exactly that for a 486 and must keep passing.
	if _, isChallenge := challengeHeaders[msg.StatusCode]; isChallenge {
		return msg, nil
	}
	if err := a.dialogs.UpdateFromResponse(msg); err != nil {
		return nil, &common.StepResult{
			Status:      common.StatusFailed,
			DurationMs:  time.Since(start).Milliseconds(),
			RawResponse: raw,
			Response:    ResponseFieldsMap(msg),
			Error:       fmt.Sprintf("dialog update: %s", err),
		}
	}
	return msg, nil
}

// timedOutResult is the ONE place in this file that stamps response.timed_out
// on a receive failure, so the classification can never be copied somewhere
// the typed transport error is no longer available (a source audit pins the
// count at exactly one). The transport-error branch passes
// common.IsTimeoutErr(err); the two budget-exhaustion branches — the
// absolute deadline passing between reads, and the provisional-response
// budget running out — have no error to classify and pass true directly,
// since both are timeouts by the documented contract.
func timedOutResult(start time.Time, timedOut bool, format string, args ...any) *common.StepResult {
	return impair.MarkTimedOut(failedStep(start, format, args...), timedOut)
}

// stepTimeout resolves the receive budget for one step: the step's own
// timeout_ms when declared, otherwise the adapter's default. Shared by the
// send path and assert_state so a per-step timeout means the same thing on
// both; before this the send path read only the adapter default and a
// step's declared timeout_ms was silently ignored.
func (a *Adapter) stepTimeout(stepCfg map[string]string) time.Duration {
	if ms, ok := stepCfg["timeout_ms"]; ok {
		if v, err := strconv.Atoi(ms); err == nil && v > 0 {
			return time.Duration(v) * time.Millisecond
		}
	}
	return a.timeout
}

func (a *Adapter) executeAssertState(_ map[string]string, stepCfg map[string]string) (*common.StepResult, error) {
	// The check field specifies the expected state name.
	expected := stepCfg["check"]
	if expected == "" {
		return nil, fmt.Errorf("sip assert_state: missing 'check' field")
	}

	// Find the most recently updated dialog and check its state.
	// In a real implementation we'd use callID, but for now check any dialog.
	expectedState := DialogState(expected)

	// Poll for the expected state.
	deadline := time.Now().Add(a.stepTimeout(stepCfg))
	for time.Now().Before(deadline) {
		if a.dialogs.HasDialogInState(expectedState) {
			return &common.StepResult{
				Status:     common.StatusPassed,
				DurationMs: 0,
			}, nil
		}
		time.Sleep(config.SIPPollIntervalMs())
	}

	return &common.StepResult{
		Status: common.StatusFailed,
		Error:  fmt.Sprintf("no dialog reached state %q within timeout", expected),
	}, nil
}

// Close releases adapter resources.
func (a *Adapter) Close() error {
	return a.transport.Close()
}

// Dialogs returns the adapter's dialog manager for testing.
func (a *Adapter) Dialogs() *DialogManager {
	return a.dialogs
}

// extractSIPUser returns the user part from a SIP URI or address field.
// Handles bare URIs ("sip:user@host"), angle-bracket form ("<sip:user@host>"),
// and display-name form ("Name <sip:user@host>"). Returns "" if not found.
func extractSIPUser(addr string) string {
	// Strip angle brackets and anything outside them.
	if start := strings.Index(addr, "<"); start != -1 {
		if end := strings.Index(addr, ">"); end > start {
			addr = addr[start+1 : end]
		}
	}
	// addr is now a bare URI like "sip:user@host" or "sip:host".
	if after, ok := strings.CutPrefix(addr, "sip:"); ok {
		// user@host — take everything before the first '@'.
		if user, _, found := strings.Cut(after, "@"); found {
			return user
		}
	}
	return ""
}

func generateCallID() string {
	return fmt.Sprintf("cellrift-%d@localhost", rand.Int63())
}

func generateBranch() string {
	return fmt.Sprintf("z9hG4bK%d", rand.Int63())
}
