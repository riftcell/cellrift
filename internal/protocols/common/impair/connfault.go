package impair

import (
	"fmt"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// ResetConfigKey, TruncateConfigKey, AbruptCloseConfigKey and
// SCTPAbortConfigKey are the keys the engine's foldImpairFlags writes into
// an adapter's config map from a step's impair: block. They are the
// DECLARATION side of the vocabulary, exactly like DropConfigKey.
const (
	ResetConfigKey       = "reset"
	TruncateConfigKey    = "truncate"
	AbruptCloseConfigKey = "abrupt_close"
	SCTPAbortConfigKey   = "sctp_abort"
)

// ConnectionStateKey is the ResponseFields key recording which
// connection-level fault Cellrift deliberately inflicted on itself. Scenario
// authors reach it as `response.connection_state` through the ordinary
// capture/assert syntax — a plain map key, exactly like ImpairDelayKey and
// DroppedKey, so no assertion grammar changes are needed. Both protocol
// adapters populate this same key.
//
// It is deliberately NOT the same field as RunResult.Warnings, which Phase
// 4's SCTP-to-TCP fallback populates: Warnings is run-level and written at
// adapter-construction time, connection_state is step-level and written by
// the fault-handling code. A deliberate sctp_abort and a silent
// SCTP-unavailable substitution therefore live in structurally different
// places and can never be confused for one another.
const ConnectionStateKey = "connection_state"

// The connection_state values, one per fault type so a scenario can assert
// precisely WHICH fault occurred rather than only that one did. Short and
// lowercase, following dropped/duplicated's naming precedent.
const (
	ConnStateReset     = "reset"
	ConnStateTruncated = "truncated"
	ConnStateClosed    = "closed"
	ConnStateAborted   = "aborted"
)

// TruncatedBytes is how many bytes of an already-encoded message a truncated
// send actually writes. A fixed internal constant, not scenario-configurable:
// it is deliberately shorter than the shortest well-formed frame either
// protocol can produce (Diameter's header alone is 20 bytes; no SIP request
// line fits in 8), so the peer's short read is a deterministic outcome rather
// than a probabilistic one. This mirrors the already-proven
// truncatedAnswerBytes/truncatedStatusLine mock-side constants, applied
// self-inflicted on the send side.
const TruncatedBytes = 8

// ConnFault is one connection-lifecycle fault: the impair: key that declares
// it, the response.connection_state value it reports, and the
// ConnFaultInjector method that performs it.
type ConnFault struct {
	ConfigKey string
	State     string
	Inject    func(common.ConnFaultInjector) error
}

// connFaults is the authoritative table of connection-lifecycle faults, in
// the order they are checked. Table-driven rather than a switch, per
// CLAUDE.md: three rows performing the same structural operation (read a
// config flag, call one injector method, report one state string), and a
// fourth fault type added later is one new row rather than a new branch in
// two adapters.
//
// truncate is deliberately absent. It is not a connection-lifecycle fault:
// it REPLACES the step's Send with a short write rather than acting on the
// connection after a successful Send, so it has a different call site and a
// different signature. See SendMaybeTruncated.
var connFaults = []ConnFault{
	{ConfigKey: ResetConfigKey, State: ConnStateReset, Inject: common.ConnFaultInjector.ResetConn},
	{ConfigKey: AbruptCloseConfigKey, State: ConnStateClosed, Inject: common.ConnFaultInjector.AbruptCloseConn},
	{ConfigKey: SCTPAbortConfigKey, State: ConnStateAborted, Inject: common.ConnFaultInjector.AbortConn},
}

// ResetRequested / AbruptCloseRequested / SCTPAbortRequested / TruncateRequested
// report whether this step declared the matching impair: key, mirroring
// DropRequested's exact-match discipline.
func ResetRequested(cfg map[string]string) bool { return cfg[ResetConfigKey] == deliveryFlag }
func AbruptCloseRequested(cfg map[string]string) bool {
	return cfg[AbruptCloseConfigKey] == deliveryFlag
}
func SCTPAbortRequested(cfg map[string]string) bool { return cfg[SCTPAbortConfigKey] == deliveryFlag }
func TruncateRequested(cfg map[string]string) bool  { return cfg[TruncateConfigKey] == deliveryFlag }

// RequestedConnFault returns the connection-lifecycle fault cfg declares.
// The validator already rejects two declared at once, so the first match is
// the only match; checking in table order keeps behaviour defined even for a
// config map built outside the validator (a unit test, say).
func RequestedConnFault(cfg map[string]string) (ConnFault, bool) {
	for _, f := range connFaults {
		if cfg[f.ConfigKey] == deliveryFlag {
			return f, true
		}
	}
	return ConnFault{}, false
}

// ApplyConnFault performs whatever connection-lifecycle fault cfg declares,
// returning the connection_state value that fault reports and any error from
// arming it. ("", nil) means no fault was declared and nothing happened.
//
// The state string is returned even on error, so a caller can still record
// WHICH fault was attempted on the failed step rather than losing that
// information to the error text.
//
// Both adapters call this at the same point — after their own Send has
// succeeded and before any Receive — which is what makes the fault
// attributable to the step that actually sent something. A fault fired
// before Send would be indistinguishable from drop, and for SIP would have
// no connection to act on at all, since sip.Adapter never calls Connect and
// its transport dials fresh on every Send.
func ApplyConnFault(tr common.Transport, cfg map[string]string) (string, error) {
	fault, ok := RequestedConnFault(cfg)
	if !ok {
		return "", nil
	}
	injector, ok := tr.(common.ConnFaultInjector)
	if !ok {
		return fault.State, fmt.Errorf("impair: %s requires a connection-oriented transport, got %T", fault.ConfigKey, tr)
	}
	return fault.State, fault.Inject(injector)
}

// SendMaybeTruncated writes msg to addr, short if cfg declares
// impair: { truncate: true } and in full otherwise. It reports whether the
// write was truncated, so the caller can stamp connection_state without
// re-reading the config map.
//
// Truncation replaces the ordinary Send rather than following it: there is
// no meaningful "send the whole message, then send it short".
func SendMaybeTruncated(tr common.Transport, cfg map[string]string, msg []byte, addr string) (bool, error) {
	if !TruncateRequested(cfg) {
		return false, tr.Send(msg, addr)
	}
	ts, ok := tr.(common.TruncatingSender)
	if !ok {
		return true, fmt.Errorf("impair: truncate requires a transport that supports short writes, got %T", tr)
	}
	return true, ts.SendTruncated(msg, addr, min(len(msg), TruncatedBytes))
}

// FaultedResult is the StepResult a connection-faulted step reports: FAILED,
// with response.connection_state naming which fault Cellrift inflicted.
//
// Failed, not passed — unlike DroppedResult. A dropped step passes because
// the impairment did exactly what was declared and no response was ever
// expected; a faulted step DID send a message and then destroyed its own
// means of hearing the answer, so there is a genuine absence of result to
// report. The step's assertions still run — adapters return (result, nil)
// rather than a Go error precisely so a scenario can assert on the outcome —
// but they can only add a failure, never rescue one: a passing
// `connection_state == "reset"` confirms the fault happened and is appended
// nowhere, while a failing one is appended to this Error. The step stays
// failed either way.
//
// Shared by both adapters so the two can never report a fault differently.
func FaultedResult(start time.Time, state, format string, args ...any) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusFailed,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      fmt.Sprintf(format, args...),
		Response:   common.ResponseFields{ConnectionStateKey: state},
	}
}
