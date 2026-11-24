package malform

import (
	"time"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// RawPayloadKey is the DECLARATION side of action: raw's byte source — the
// single config key the engine's foldRawConfig writes, carrying the already-
// decoded literal bytes as a Go string. Go strings hold arbitrary bytes, the
// same lossless conversion formatValue (internal/engine/state.go) already
// uses for a captured []byte, so no adapter ever has to treat a string
// specially to preserve non-UTF-8 content.
//
// Hex decoding happens exactly once, in the engine's foldRawConfig, before
// this key is ever written — neither adapter that reads RawPayload back out
// ever sees an encoding, only the literal bytes to send.
const RawPayloadKey = "raw_payload"

// RawBytesSentKey is the OBSERVATION side of action: raw — the
// response.raw_bytes_sent key a scenario asserts on to confirm how many
// bytes actually reached the wire, following DroppedKey/DuplicatedKey's
// past-tense observation-side convention (impair/helpers.go).
const RawBytesSentKey = "raw_bytes_sent"

// RawPayload reads a declared raw_payload back out of cfg. An absent or
// empty value is ok == false: an empty payload is not a payload, the same
// convention DropRequested/DuplicateRequested apply to their own flags.
func RawPayload(cfg map[string]string) (string, bool) {
	payload, ok := cfg[RawPayloadKey]
	if !ok || payload == "" {
		return "", false
	}
	return payload, true
}

// RawResult is the shared passed-result constructor both adapters' executeRaw
// use, so a raw send can never be reported differently between protocols —
// the same rationale impair.DroppedResult carries for a dropped step.
// RawResponse is left nil deliberately: a raw step writes bytes of another
// protocol's shape onto this step's connection and never attempts to parse
// whatever comes back as its own protocol, so there is no parsed response to
// report here.
func RawResult(start time.Time, sent int) *common.StepResult {
	return &common.StepResult{
		Status:     common.StatusPassed,
		DurationMs: time.Since(start).Milliseconds(),
		Response:   common.ResponseFields{RawBytesSentKey: sent},
	}
}
