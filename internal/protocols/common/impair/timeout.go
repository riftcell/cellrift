package impair

import (
	"github.com/riftcell/cellrift/internal/protocols/common"
)

// TimedOutKey is the ResponseFields key recording that this step's peer never
// answered within the step's own timeout_ms. Scenario authors reach it as
// `response.timed_out` through the ordinary capture/assert syntax — a plain
// map key, exactly like ImpairDelayKey, DroppedKey and ConnectionStateKey, so
// no assertion grammar changes are needed. Both protocol adapters populate
// this same key.
//
// Unlike ConnectionStateKey, which appears only when a fault fired, this key
// is present on EVERY send step: false on success, true on a genuine
// peer-silence timeout. That matches DroppedKey/DuplicatedKey's
// always-present convention and lets a scenario assert
// `response.timed_out == false` as a positive liveness check, rather than
// depending on absence-of-key semantics, which the assertion grammar cannot
// distinguish from an empty value.
//
// It is deliberately narrow. A connection-level fault (reset, truncate,
// abrupt_close, sctp_abort) reports ConnectionStateKey and leaves this key
// false: connection_state says HOW the connection died, timed_out says the
// peer simply stayed silent. The two can never both be true on one step,
// because every fault fires after Send and before any Receive is attempted,
// and the adapter returns immediately rather than falling through to a
// receive.
const TimedOutKey = "timed_out"

// MarkTimedOut stamps response.timed_out on result, allocating Response when
// nil and leaving every existing key (notably ImpairDelayKey and
// ConnectionStateKey) untouched. Returns result so call sites can wrap
// inline, mirroring RecordDelay and MarkDuplicated. Nil-safe.
//
// It takes the bool explicitly, unlike MarkDuplicated's always-true case,
// because false is a meaningful stamped value here rather than an omission.
//
// Call this only where the original typed error is still in hand: once an
// error has been formatted into StepResult.Error it is a string, and no
// errors.Is check is possible any more.
func MarkTimedOut(result *common.StepResult, timedOut bool) *common.StepResult {
	if result == nil {
		return nil
	}
	if result.Response == nil {
		result.Response = common.ResponseFields{}
	}
	result.Response[TimedOutKey] = timedOut
	return result
}

// EnsureTimedOut stamps response.timed_out == false on result unless a value
// is already present, and returns result. Nil-safe.
//
// This is the funnel that makes the key's always-present guarantee
// structural. Each adapter calls it exactly once, on the single return path
// of its send action, instead of repeating a false stamp at each of the
// ~10 StepResult construction sites a send step can return from — the
// copy-pasted-block shape this project's conventions exist to prevent, and
// one a future new failure branch would silently forget to join.
//
// It never overwrites: the one site that classified a real timeout has
// already stamped true (or an explicit false) via MarkTimedOut by the time a
// result reaches here.
func EnsureTimedOut(result *common.StepResult) *common.StepResult {
	if result == nil {
		return nil
	}
	if result.Response == nil {
		result.Response = common.ResponseFields{}
	}
	if _, ok := result.Response[TimedOutKey]; !ok {
		result.Response[TimedOutKey] = false
	}
	return result
}
