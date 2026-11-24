package impair

import (
	"strconv"
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// DropConfigKey and DuplicateConfigKey are the keys the engine's
// foldImpairConfig writes into an adapter's config map from a step's
// impair: block. They are the DECLARATION side of the vocabulary.
const (
	DropConfigKey      = "drop"
	DuplicateConfigKey = "duplicate"
)

// DroppedKey and DuplicatedKey are the ResponseFields keys recording that a
// step's message was deliberately withheld from the wire, or deliberately
// written to it twice. They are the OBSERVATION side, deliberately spelled
// in the past tense and therefore NOT the same strings as the two config
// keys above — a scenario declares `impair: { drop: true }` and reads back
// `response.dropped`. Scenario authors reach them through the ordinary
// capture/assert syntax — plain map keys, exactly like ImpairDelayKey, so
// no assertion grammar changes are needed and no new StepStatus value is
// introduced. Both protocol adapters populate these same keys.
const (
	DroppedKey    = "dropped"
	DuplicatedKey = "duplicated"
)

// deliveryFlag is the literal an adapter config map carries for a boolean
// delivery-impairment key. The engine's foldImpairConfig writes exactly this
// string and nothing else, so an exact match is the correct test — a lenient
// parse would accept values the engine can never produce and silently widen
// the contract.
const deliveryFlag = "true"

// DropRequested reports whether this step declared impair: { drop: true }.
// The adapter — not the transport — must own this decision: only the adapter
// knows whether to wait for a response afterwards, and a step that "passes"
// only after burning its whole receive timeout is the hang this impairment
// exists to avoid.
func DropRequested(cfg map[string]string) bool { return cfg[DropConfigKey] == deliveryFlag }

// DuplicateRequested reports whether this step declared
// impair: { duplicate: true }.
func DuplicateRequested(cfg map[string]string) bool { return cfg[DuplicateConfigKey] == deliveryFlag }

// DroppedResult is the StepResult a deliberately-dropped step reports:
// passed, because the impairment succeeded at doing what was declared, with
// response.dropped == true recording the fact. Shared by both adapters so
// the two can never report a drop differently.
func DroppedResult(start time.Time) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusPassed,
		DurationMs: time.Since(start).Milliseconds(),
		Response:   common.ResponseFields{DroppedKey: true},
	}
}

// MarkDuplicated stamps response.duplicated == true on an otherwise ordinary
// step result, allocating Response when nil and leaving every existing key
// (notably ImpairDelayKey) untouched. Returns result so call sites can wrap
// inline, mirroring RecordDelay. Nil-safe.
func MarkDuplicated(result *common.StepResult) *common.StepResult {
	if result == nil {
		return nil
	}
	if result.Response == nil {
		result.Response = common.ResponseFields{}
	}
	result.Response[DuplicatedKey] = true
	return result
}

// ImpairDelayKey is the ResponseFields key carrying how many milliseconds of
// this step's duration Cellrift inflicted on itself. Scenario authors reach
// it as `response.impair_delay_ms` through the ordinary capture/assert
// syntax — it is a plain map key, so no assertion grammar changes are
// needed. Both protocol adapters populate this same key.
const ImpairDelayKey = "impair_delay_ms"

// delayReporter is the accessor an impairment-capable transport exposes for
// how much delay it actually applied. Kept out of common.Transport
// deliberately: it is an observability detail, not part of the wire
// contract every transport must satisfy.
type delayReporter interface {
	LastDelay() time.Duration
}

// Configure is called unconditionally at the top of every send, impaired
// step or not — this call is what stops one step's profile from surviving
// into the next, and there is no safe way for the transport to infer that
// on its own. A transport that does not implement common.ImpairmentControl
// is simply never impaired: this is a no-op, never an error.
func Configure(tr common.Transport, cfg map[string]string) error {
	ic, ok := tr.(common.ImpairmentControl)
	if !ok {
		return nil
	}
	return ic.SetImpairment(cfg)
}

// Clear disarms any impairment left over from a previous step. Used before
// work that must never be impaired, such as the Diameter CER/CEA handshake,
// which belongs to no scenario step.
func Clear(tr common.Transport) {
	_ = Configure(tr, nil)
}

// PostReceiveDelay reads delay_ms from cfg and sleeps that many
// milliseconds, returning the duration slept (0 on any parse failure or a
// non-positive value).
//
// delay_ms is applied by the adapter after a complete response has been
// received and parsed, never inside impair.Transport — the transport only
// ever sees raw byte chunks and cannot know when a whole message exists.
// Unlike the receive-side latency this package's Transport applies, this
// delay is deliberately NOT clamped to any timeout: its entire purpose is
// to make Cellrift sit on a fully-received answer long enough to exercise
// the peer's own timeout and retry behaviour.
func PostReceiveDelay(cfg map[string]string) time.Duration {
	raw, ok := cfg["delay_ms"]
	if !ok {
		return 0
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0
	}
	d := time.Duration(ms) * time.Millisecond
	time.Sleep(d)
	return d
}

// RecordDelay sums tr's LastDelay (0 if tr does not report one) with
// postReceive and, when the total is positive, records it under
// ImpairDelayKey in result.Response — allocating a fresh ResponseFields map
// when result.Response is nil, and leaving any existing keys untouched
// otherwise. Returns result unchanged so call sites can wrap inline, e.g.
// `return impair.RecordDelay(a.transport, failedStep(...), 0), nil`. Nil-safe
// on result and on a tr that isn't a delayReporter.
func RecordDelay(tr common.Transport, result *common.StepResult, postReceive time.Duration) *common.StepResult {
	if result == nil {
		return nil
	}

	var transportDelay time.Duration
	if dr, ok := tr.(delayReporter); ok {
		transportDelay = dr.LastDelay()
	}

	total := (transportDelay + postReceive).Milliseconds()
	if total <= 0 {
		return result
	}

	if result.Response == nil {
		result.Response = common.ResponseFields{}
	}
	result.Response[ImpairDelayKey] = total
	return result
}
