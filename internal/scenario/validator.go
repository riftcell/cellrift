package scenario

import (
	"encoding/hex"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/protocols/diameter"
)

// allowedProtocols lists the protocols supported in the current phase.
var allowedProtocols = map[string]bool{
	"sip":      true,
	"diameter": true,
}

// TransportSet is the set of transport names valid for one protocol.
// Kept as a named type because the same concept is cross-checked against the
// transport enums published in docs/scenario.schema.json — see
// TestSchema_TransportEnumsMatchValidator.
type TransportSet map[string]bool

// allowedTransports lists the transports each protocol accepts, keyed by
// protocol name. This is the authoritative source: docs/scenario.schema.json
// mirrors it for IDE tooling, and a test fails if the two drift apart.
var allowedTransports = map[string]TransportSet{
	"sip":      {"udp": true, "tcp": true, "tls": true},
	"diameter": {"sctp": true, "tcp": true},
}

// ActionSet is the set of step action names the validator accepts. Named
// because the same concept is cross-checked in two places outside this
// file: the action enum published in docs/scenario.schema.json, and the
// dispatch audit that proves every accepted action is actually executed
// by the engine or an adapter.
type ActionSet map[string]bool

// allowedActions lists valid step action values. Authoritative: the JSON
// schema mirrors it, and tests fail if the two drift apart or if an entry
// here has no dispatch path.
var allowedActions = ActionSet{
	"send":         true,
	"assert_state": true,
	"wait":         true,
	"parallel":     true,
	"reorder":      true,
	"raw":          true,
}

// AllowedActions returns a copy of the action names the validator accepts.
// Exported for the same reason AllowedTransports is: tests outside this
// package must assert against the authoritative set, not a duplicated
// literal that drifts.
func AllowedActions() ActionSet {
	out := make(ActionSet, len(allowedActions))
	maps.Copy(out, allowedActions)
	return out
}

// AllowedTransports returns a copy of the transport sets each protocol accepts,
// keyed by protocol name. Exported so tests outside this package — notably the
// schema cross-check in library_test.go — can assert against the authoritative
// set rather than a duplicated literal that drifts.
func AllowedTransports() map[string]TransportSet {
	out := make(map[string]TransportSet, len(allowedTransports))
	for protocol, transports := range allowedTransports {
		set := make(TransportSet, len(transports))
		maps.Copy(set, transports)
		out[protocol] = set
	}
	return out
}

// validateTransport checks a protocol block's transport against the set that
// protocol accepts. An empty transport is valid — the adapter applies its
// default (UDP for SIP, SCTP for Diameter).
func validateTransport(protocol string, cfg *ProtocolConfig) []ValidationError {
	if cfg == nil || cfg.Transport == "" {
		return nil
	}
	valid, ok := allowedTransports[protocol]
	if !ok || valid[cfg.Transport] {
		return nil
	}
	return []ValidationError{{
		Field: "protocols." + protocol + ".transport",
		Message: fmt.Sprintf("unsupported transport %q for %s (allowed: %s)",
			cfg.Transport, protocol, strings.Join(sortedKeys(valid), ", ")),
	}}
}

// credentialKeys are the ProtocolConfig fields that mean something only to SIP,
// in the stable order their errors are reported. The Go struct is shared by
// both protocols, so nothing structural stops a diameter: block from carrying a
// password, and the schema's additionalProperties cannot see the Go side. This
// table plus validateCredentialScope is the only thing that makes "credentials
// are SIP-only" true. get returns the raw field so presence is all that is
// examined; a value is never read into a message.
var credentialKeys = []struct {
	key string
	get func(*ProtocolConfig) string
}{
	{"username", func(c *ProtocolConfig) string { return c.Username }},
	{"password", func(c *ProtocolConfig) string { return c.Password }},
	{"credentials_file", func(c *ProtocolConfig) string { return c.CredentialsFile }},
}

// validateCredentialScope rejects credential keys on any protocol but sip, one
// error per offending key. The message names the field only, never its value.
func validateCredentialScope(protocol string, cfg *ProtocolConfig) []ValidationError {
	if cfg == nil || protocol == "sip" {
		return nil
	}
	var errs []ValidationError
	for _, c := range credentialKeys {
		if c.get(cfg) == "" {
			continue
		}
		errs = append(errs, ValidationError{
			Field:   "protocols." + protocol + "." + c.key,
			Message: fmt.Sprintf("%s is a credential field and is supported for sip only", c.key),
		})
	}
	return errs
}

// sortedKeys returns a TransportSet's keys in stable order so validation
// messages don't vary between runs.
func sortedKeys(m TransportSet) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateStepShape checks the per-action structural requirements of one
// step. Each action requires a different shape, so this is a switch rather
// than a lookup table: "parallel" needs sub-steps, "wait" needs a duration,
// and every protocol action needs a protocol. It only reports; it never
// mutates the step.
func validateStepShape(stepLabel string, step Step) []ValidationError {
	switch step.Action {
	case "parallel":
		if len(step.Steps) == 0 {
			return []ValidationError{{StepID: stepLabel, Field: "steps", Message: "parallel step requires sub-steps"}}
		}
		return parallelProtocolErrors(stepLabel, step)
	case "reorder":
		return reorderShapeErrors(stepLabel, step)
	case "wait":
		// A wait is dispatched by the engine, not by an adapter — waiting is
		// neither a SIP nor a Diameter operation, so no protocol applies. A
		// missing or non-positive duration is an error rather than a silent
		// zero-length sleep.
		if step.Duration <= 0 {
			return []ValidationError{{StepID: stepLabel, Field: "duration_ms", Message: "wait step requires duration_ms greater than 0"}}
		}
		return nil
	case "assert_state":
		return assertStateErrors(stepLabel, step)
	default:
		if step.Protocol == "" {
			return []ValidationError{{StepID: stepLabel, Field: "protocol", Message: "required field is missing"}}
		}
		if !allowedProtocols[step.Protocol] {
			return []ValidationError{{StepID: stepLabel, Field: "protocol", Message: fmt.Sprintf("unknown protocol %q", step.Protocol)}}
		}
		return nil
	}
}

// StateSet is the set of dialog-state names an assert_state step may
// check. It mirrors the SIP adapter's DialogState constants that a scenario
// can meaningfully wait for; the schema's `check` enum is cross-checked
// against it by a test so the two cannot drift.
type StateSet map[string]bool

var allowedChecks = StateSet{"registered": true, "established": true, "terminated": true}

// AllowedChecks returns a copy of the assert_state check names the
// validator accepts.
func AllowedChecks() StateSet {
	out := make(StateSet, len(allowedChecks))
	maps.Copy(out, allowedChecks)
	return out
}

// assertStateErrors checks an assert_state step's shape: a SIP protocol and
// a check naming a state the dialog tracker can actually reach. The
// shipped register_basic.yaml waited on a state that did not exist for a long
// time because nothing validated this field.
func assertStateErrors(stepLabel string, step Step) []ValidationError {
	if step.Protocol == "" {
		return []ValidationError{{StepID: stepLabel, Field: "protocol", Message: "required field is missing"}}
	}
	if step.Protocol != "sip" {
		return []ValidationError{{StepID: stepLabel, Field: "protocol", Message: fmt.Sprintf("assert_state is a SIP-only action, not %q", step.Protocol)}}
	}
	if step.Check == "" {
		return []ValidationError{{StepID: stepLabel, Field: "check", Message: "assert_state requires a check"}}
	}
	if !allowedChecks[step.Check] {
		names := make([]string, 0, len(allowedChecks))
		for k := range allowedChecks {
			names = append(names, k)
		}
		sort.Strings(names)
		return []ValidationError{{StepID: stepLabel, Field: "check", Message: fmt.Sprintf("unknown state %q (allowed: %s)", step.Check, strings.Join(names, ", "))}}
	}
	return nil
}

// parallelProtocolErrors rejects a parallel group in which two sub-steps
// declare the same protocol. The runner hands every sub-step
// adapters[s.Protocol] — one shared adapter instance per protocol — and
// neither adapter carries a mutex, so two concurrent Diameter sub-steps
// interleave on one session buffer and steal each other's answers by
// Hop-by-Hop ID: torn framing on one side, a false timeout on the other. No
// shipped scenario does this, and the race detector never sees it because
// the existing parallel tests use a different protocol per sub-step, but
// "send two requests at once" is the first thing a user will try. Until the
// runner gives each sub-step its own adapter (a new connection, and for
// Diameter a new CER/CEA, per sub-step — a semantic change worth deciding
// deliberately), the shape is rejected here so the race is a validate-time
// error rather than a nondeterministic runtime result.
func parallelProtocolErrors(stepLabel string, step Step) []ValidationError {
	seen := make(map[string]string, len(step.Steps))
	for _, sub := range step.Steps {
		if sub.Action == "wait" || sub.Protocol == "" {
			continue
		}
		if first, dup := seen[sub.Protocol]; dup {
			return []ValidationError{{StepID: stepLabel, Field: "steps", Message: fmt.Sprintf(
				"parallel sub-steps %q and %q both use protocol %q; concurrent sub-steps share one adapter per protocol and would race — use one sub-step per protocol, or run them sequentially",
				first, sub.ID, sub.Protocol)}}
		}
		seen[sub.Protocol] = sub.ID
	}
	return nil
}

// reorderShapeErrors enforces the structural rules a reorder group must
// satisfy. All four are structural, not runtime: a group that mixes
// protocols, contains a non-send sub-step, or declares a send_order set
// that is not a clean permutation has no coherent execution — and this
// codebase has repeatedly shipped the "accepted by the validator, surprising
// at runtime" defect, so each of these is an error here rather than a
// surprise later.
func reorderShapeErrors(stepLabel string, step Step) []ValidationError {
	if len(step.Steps) < 2 {
		return []ValidationError{{StepID: stepLabel, Field: "steps", Message: "reorder step requires at least 2 sub-steps"}}
	}
	var errs []ValidationError
	errs = append(errs, reorderSubStepErrors(stepLabel, step)...)
	errs = append(errs, sendOrderPermutationErrors(stepLabel, step)...)
	return errs
}

// reorderSubStepErrors checks each sub-step's action and protocol. A
// reorder group's sub-steps must all be action: send — anything else has no
// wire order to control — and must all share one protocol: "arrival order"
// is only meaningful relative to one peer on one connection, so a group
// spanning two unrelated connections has no coherent interpretation of wire
// order.
func reorderSubStepErrors(stepLabel string, step Step) []ValidationError {
	var errs []ValidationError
	protocol := step.Steps[0].Protocol
	for _, sub := range step.Steps {
		if sub.Action != "send" {
			errs = append(errs, ValidationError{
				StepID:  stepLabel,
				Field:   "steps",
				Message: fmt.Sprintf("reorder sub-steps must use action: send, not %q", sub.Action),
			})
		}
		if sub.Protocol != protocol {
			errs = append(errs, ValidationError{
				StepID:  stepLabel,
				Field:   "steps",
				Message: fmt.Sprintf("reorder sub-steps must all use the same protocol; %q differs from %q", sub.Protocol, protocol),
			})
		}
	}
	return errs
}

// sendOrderPermutationErrors collects every sub-step's SendOrder and checks
// the whole set is a clean permutation of 1..N. One error for the whole
// group, not one per sub-step: the values are only meaningful together — a
// missing 1 is only a problem because of what else is (or isn't) present.
func sendOrderPermutationErrors(stepLabel string, step Step) []ValidationError {
	n := len(step.Steps)
	seen := make(map[int]bool, n)
	values := make([]int, n)
	for i, sub := range step.Steps {
		values[i] = sub.SendOrder
		seen[sub.SendOrder] = true
	}
	isPermutation := len(seen) == n
	if isPermutation {
		for i := 1; i <= n; i++ {
			if !seen[i] {
				isPermutation = false
				break
			}
		}
	}
	if isPermutation {
		return nil
	}
	return []ValidationError{{
		StepID:  stepLabel,
		Field:   "send_order",
		Message: fmt.Sprintf("reorder send_order values must be a permutation of 1..%d, got %v", n, values),
	}}
}

// validateSendOrderTree rejects send_order anywhere it does nothing: on a
// top-level step, and on a sub-step of a parallel group. Only a reorder
// group's own sub-steps consume it. Without this, send_order on an ordinary
// send step would be silently ignored — the same "declared but never read"
// defect class as impair: on a wait step.
//
// It also rejects a sub-step whose Action is "reorder" nested inside a
// parallel group: a parallel group dispatches each sub-step independently,
// which is exactly what a reorder group must not do, so the two containers
// cannot nest that way.
func validateSendOrderTree(stepLabel string, step Step) []ValidationError {
	var errs []ValidationError
	if step.Action != "reorder" && step.SendOrder != 0 {
		errs = append(errs, ValidationError{
			StepID:  stepLabel,
			Field:   "send_order",
			Message: "send_order is only valid on a reorder sub-step",
		})
	}
	if step.Action == "reorder" {
		return errs
	}
	for j, sub := range step.Steps {
		subLabel := sub.ID
		if subLabel == "" {
			subLabel = fmt.Sprintf("%s[%d]", stepLabel, j)
		}
		if sub.SendOrder != 0 {
			errs = append(errs, ValidationError{
				StepID:  subLabel,
				Field:   "send_order",
				Message: "send_order is only valid on a reorder sub-step",
			})
		}
		if step.Action == "parallel" && sub.Action == "reorder" {
			errs = append(errs, ValidationError{
				StepID:  subLabel,
				Field:   "action",
				Message: "reorder cannot be nested inside parallel: a parallel group dispatches each sub-step independently, which is exactly what a reorder group must not do",
			})
		}
	}
	return errs
}

// ImpairKeySet is the set of sub-keys an impair: block accepts. Named
// because the same concept is cross-checked outside this package: the
// impair properties published in docs/scenario.schema.json must match it
// exactly, and a test fails if the two drift apart.
type ImpairKeySet map[string]bool

// impairKeys is the authoritative set. Two families live here: the
// millisecond timing keys, whose values impairFieldValues additionally
// range-checks, and the boolean delivery/connection-fault keys, which have
// no numeric constraint to check.
var impairKeys = ImpairKeySet{
	"latency_ms":   true,
	"jitter_ms":    true,
	"delay_ms":     true,
	"drop":         true,
	"duplicate":    true,
	"reset":        true,
	"truncate":     true,
	"abrupt_close": true,
	"sctp_abort":   true,
}

// ImpairKeys returns a copy of the impair: sub-keys the validator accepts.
// Exported for the same reason AllowedActions is: the schema drift guard
// must assert against the authoritative set, not a duplicated literal that
// silently rots.
func ImpairKeys() ImpairKeySet {
	out := make(ImpairKeySet, len(impairKeys))
	maps.Copy(out, impairKeys)
	return out
}

// MalformKeySet is the set of sub-keys a malform: block accepts. Named
// because the same concept is cross-checked outside this package: the
// malform properties published in docs/scenario.schema.json must match it
// exactly, and a test fails if the two drift apart.
type MalformKeySet map[string]bool

// malformKeys is the authoritative set: two Diameter-only keys and one
// SIP-only key, gated by validateMalform's protocol check rather than by
// this set itself.
var malformKeys = MalformKeySet{
	"avp_length": true,
	"msg_length": true,
	"sip_header": true,
}

// MalformKeys returns a copy of the malform: sub-keys the validator
// accepts. Exported for the same reason ImpairKeys is: the schema drift
// guard must assert against the authoritative set, not a duplicated literal
// that silently rots.
func MalformKeys() MalformKeySet {
	out := make(MalformKeySet, len(malformKeys))
	maps.Copy(out, malformKeys)
	return out
}

// impairFieldValues names each impair: millisecond sub-key alongside its
// declared value, so the non-negative check is one loop rather than three
// copy-pasted if-blocks (CLAUDE.md's no-copy-pasted-error-blocks rule).
// This is the numeric-range subset of ImpairKeys, not the full accepted key
// set — drop/duplicate are booleans and need no range check.
func impairFieldValues(spec *ImpairSpec) map[string]int {
	return map[string]int{
		"impair.latency_ms": spec.LatencyMs,
		"impair.jitter_ms":  spec.JitterMs,
		"impair.delay_ms":   spec.DelayMs,
	}
}

// impairRangeErrors checks the millisecond impair: keys for negative values.
// Extracted from validateImpair to keep that function within CLAUDE.md's
// 40-line cap once the drop/duplicate mutual-exclusion rule was added.
func impairRangeErrors(stepLabel string, spec *ImpairSpec) []ValidationError {
	var errs []ValidationError
	for field, v := range impairFieldValues(spec) {
		if v < 0 {
			errs = append(errs, ValidationError{StepID: stepLabel, Field: field, Message: "must not be negative"})
		}
	}
	// Map iteration is random; sort so validation output is deterministic.
	sort.Slice(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs
}

// ImpairFlagSet is the set of boolean impair: sub-keys, keyed by the name a
// scenario writes in YAML and valued by whether this step declared it.
// Named because it is the authoritative input to the mutual-exclusion rule
// AND to the error message that names the offending keys — the two must
// never disagree about which keys are "delivery or fault" flags.
type ImpairFlagSet map[string]bool

// impairFlagValues names every boolean impair: sub-key alongside its declared
// value. Table-driven rather than a chain of && comparisons, per CLAUDE.md:
// six keys performing the same structural check (is it set?) is exactly the
// case a table is for, and a seventh fault key added later must only be
// registered here, not woven into a growing boolean expression.
func impairFlagValues(spec *ImpairSpec) ImpairFlagSet {
	return ImpairFlagSet{
		"drop":         spec.Drop,
		"duplicate":    spec.Duplicate,
		"reset":        spec.Reset,
		"truncate":     spec.Truncate,
		"abrupt_close": spec.AbruptClose,
		"sctp_abort":   spec.SCTPAbort,
	}
}

// declaredImpairFlags returns the boolean impair: keys this step set to
// true, in sorted order so an error message naming them is deterministic.
func declaredImpairFlags(spec *ImpairSpec) []string {
	var set []string
	for key, v := range impairFlagValues(spec) {
		if v {
			set = append(set, key)
		}
	}
	sort.Strings(set)
	return set
}

// impairExclusionError enforces that a step declares at most ONE boolean
// impair: key. All six describe a different thing to do with the step's one
// message, and two of them at once has no coherent meaning: a message that
// is never sent cannot also be truncated, and a connection cannot be both
// reset and aborted.
//
// The drop+duplicate pair keeps its original, more specific wording rather
// than falling through to the generic message. That exact string is asserted
// by TestValidate_ImpairRejectsDropAndDuplicateTogether and says
// something the generic message does not — losing it would be a regression
// in error quality, not a simplification.
func impairExclusionError(stepLabel string, spec *ImpairSpec) []ValidationError {
	set := declaredImpairFlags(spec)
	if len(set) < 2 {
		return nil
	}
	msg := fmt.Sprintf("at most one delivery or connection-fault key may be declared per step, got %s", strings.Join(set, ", "))
	if len(set) == 2 && set[0] == "drop" && set[1] == "duplicate" {
		msg = "drop and duplicate are mutually exclusive: a message that is never sent cannot be sent twice"
	}
	return []ValidationError{{StepID: stepLabel, Field: "impair", Message: msg}}
}

// TransportDefaults is the transport each protocol's adapter builds when a
// scenario's protocols: block names none. It mirrors cmd/cellrift/main.go's
// buildSIPAdapter ("" -> udp) and buildDiameterAdapter ("" -> an SCTP
// attempt) exactly, so validation reasons about the transport that will
// ACTUALLY carry the step rather than only the one the YAML spells out.
type TransportDefaults map[string]string

var protocolDefaultTransport = TransportDefaults{
	"sip":      "udp",
	"diameter": "sctp",
}

// effectiveTransport is the transport a step's protocol will run over:
// whatever protocols.<name>.transport declares, or that protocol's adapter
// default. It is DECLARED intent, not observed reality — Diameter's sctp may
// still fall back to TCP at runtime on a kernel without the sctp module
// (NewSCTPOrFallbackTransport), which is exactly why that substitution
// carries its own RunResult.Warnings line and is not something a validator
// can or should predict.
func effectiveTransport(s *Scenario, protocol string) string {
	if cfg, ok := s.Protocols[protocol]; ok && cfg != nil && cfg.Transport != "" {
		return cfg.Transport
	}
	return protocolDefaultTransport[protocol]
}

// impairTransportError rejects sctp_abort on a step that will not run over
// SCTP. A real RFC 4960 ABORT chunk has no meaning on
// a TCP, TLS or UDP socket, and catching that at validate time names the
// offending step and key — the validator's established convention — instead
// of surfacing as a confusing mid-scenario runtime error.
func impairTransportError(stepLabel string, spec *ImpairSpec, transport string) []ValidationError {
	if !spec.SCTPAbort || transport == "sctp" {
		return nil
	}
	return []ValidationError{{
		StepID:  stepLabel,
		Field:   "impair.sctp_abort",
		Message: fmt.Sprintf("sctp_abort requires an SCTP transport, but this step runs over %q", transport),
	}}
}

// validateImpair enforces the rules an impair: block must satisfy.
//
// Rule 1, the important one: impair: is meaningful only on action: send.
// A wait or parallel step never reaches an adapter, and assert_state only
// polls in-memory dialog state — none of them perform the wire I/O an
// impairment would decorate. Accepting the block there would silently do
// nothing, which is the same "validated but never dispatched" defect class
// that made action: wait a no-op for three phases. This mirrors how
// duration_ms is pinned to action: wait in validateStepShape.
//
// Rule 2: at most one boolean impair: key may be declared (drop, duplicate,
// reset, truncate, abrupt_close, sctp_abort) — a hard early return, since
// once two contradict each other there is nothing coherent left to validate.
//
// Rule 3: sctp_abort requires the step's effective transport to be sctp
// (a deliberate design choice), appended alongside range errors since it is an
// independent mistake from a negative millisecond value.
//
// Rule 4: negative milliseconds are rejected. The transport rejects them
// too, but failing at validate time names the offending step and key.
func validateImpair(stepLabel string, step Step, transport string) []ValidationError {
	if step.Impair == nil {
		return nil
	}
	if step.Action != "send" {
		return []ValidationError{{
			StepID:  stepLabel,
			Field:   "impair",
			Message: fmt.Sprintf("impair is only valid on action: send, not %q", step.Action),
		}}
	}
	if errs := impairExclusionError(stepLabel, step.Impair); errs != nil {
		return errs
	}
	errs := impairTransportError(stepLabel, step.Impair, transport)
	return append(errs, impairRangeErrors(stepLabel, step.Impair)...)
}

// validateImpairTree runs validateImpair on a step and, since Validate's
// per-step loop does not otherwise descend into a parallel step's sub-steps,
// on each of its sub-steps too. Without this explicit descent, impair: on a
// non-send sub-step of a parallel block would silently pass — the same
// "validated but does nothing" defect class one level of nesting deeper. s
// resolves each (sub)step's own effective transport since a parallel group's
// sub-steps may name different protocols.
func validateImpairTree(stepLabel string, step Step, s *Scenario) []ValidationError {
	errs := validateImpair(stepLabel, step, effectiveTransport(s, step.Protocol))
	for j, sub := range step.Steps {
		subLabel := sub.ID
		if subLabel == "" {
			subLabel = fmt.Sprintf("%s[%d]", stepLabel, j)
		}
		errs = append(errs, validateImpair(subLabel, sub, effectiveTransport(s, sub.Protocol))...)
	}
	return errs
}

// sipHeaderOps is the table-driven set of allowed malform.sip_header.op
// values, built from the shared malform.Op* constants — never a
// hand-written literal, per CLAUDE.md's table-driven-over-switch rule. A
// fourth op added later is registered here only.
var sipHeaderOps = map[string]bool{
	malform.OpMissing:   true,
	malform.OpDuplicate: true,
	malform.OpOverwrite: true,
}

// sortedSIPHeaderOps returns sipHeaderOps' keys in stable sorted order, so
// an "unknown op" error message lists the allowed values deterministically
// rather than in Go's randomised map iteration order.
func sortedSIPHeaderOps() []string {
	keys := make([]string, 0, len(sipHeaderOps))
	for k := range sipHeaderOps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// malformProtocolErrors enforces that avp_length/msg_length only appear on
// a diameter step and sip_header only on a sip step. Each violation is
// reported against its own sub-key field, since a step could in principle
// declare more than one mismatched key at once.
func malformProtocolErrors(stepLabel string, step Step) []ValidationError {
	var errs []ValidationError
	spec := step.Malform
	if spec.AVPLength != nil && step.Protocol != "diameter" {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.avp_length",
			Message: fmt.Sprintf("avp_length corrupts a Diameter AVP header and has no meaning on a %q step", step.Protocol),
		})
	}
	if spec.MsgLength != nil && step.Protocol != "diameter" {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.msg_length",
			Message: fmt.Sprintf("msg_length corrupts a Diameter Message Length header and has no meaning on a %q step", step.Protocol),
		})
	}
	if spec.SIPHeader != nil && step.Protocol != "sip" {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.sip_header",
			Message: fmt.Sprintf("sip_header corrupts a SIP header and has no meaning on a %q step", step.Protocol),
		})
	}
	return errs
}

// avpLengthValueErrors checks malform.avp_length's own value rules: the
// name must resolve via the same embedded AVP dictionary validateAVPFields
// already uses (no second name table), and the declared length must fit the
// AVP Length field's 3-byte width.
func avpLengthValueErrors(stepLabel string, m *AVPLengthMalform) []ValidationError {
	if m == nil {
		return nil
	}
	var errs []ValidationError
	if _, ok := diameter.LookupAVP(m.Name); m.Name == "" || !ok {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.avp_length.name",
			Message: fmt.Sprintf("unknown AVP name %q", m.Name),
		})
	}
	if m.Value < 0 || m.Value > malform.MaxThreeByteLength {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.avp_length.value",
			Message: "must be between 0 and 16777215 (the AVP Length field is 3 bytes wide)",
		})
	}
	return errs
}

// msgLengthValueErrors checks that a declared msg_length fits the Message
// Length field's 3-byte width. nil (not declared) is not an error.
func msgLengthValueErrors(stepLabel string, msgLength *int) []ValidationError {
	if msgLength == nil {
		return nil
	}
	if *msgLength < 0 || *msgLength > malform.MaxThreeByteLength {
		return []ValidationError{{
			StepID: stepLabel, Field: "malform.msg_length",
			Message: "must be between 0 and 16777215 (the Message Length field is 3 bytes wide)",
		}}
	}
	return nil
}

// sipHeaderValueErrors checks malform.sip_header's own value rules: a
// non-empty name, an op drawn from sipHeaderOps, and a non-empty value when
// op is overwrite (missing never reads Value; duplicate's own byte-splicing
// implementation is a later plan's concern, not this validator's).
func sipHeaderValueErrors(stepLabel string, h *SIPHeaderMalform) []ValidationError {
	if h == nil {
		return nil
	}
	var errs []ValidationError
	if h.Name == "" {
		errs = append(errs, ValidationError{StepID: stepLabel, Field: "malform.sip_header.name", Message: "required"})
	}
	if !sipHeaderOps[h.Op] {
		errs = append(errs, ValidationError{
			StepID: stepLabel, Field: "malform.sip_header.op",
			Message: fmt.Sprintf("unknown op %q (allowed: %s)", h.Op, strings.Join(sortedSIPHeaderOps(), ", ")),
		})
	} else if h.Op == malform.OpOverwrite && h.Value == "" {
		errs = append(errs, ValidationError{StepID: stepLabel, Field: "malform.sip_header.value", Message: "required when op is overwrite"})
	}
	return errs
}

// malformValueErrors runs every malform: sub-key's own value rules,
// independent of the protocol gate in malformProtocolErrors — a step could
// fail both at once (e.g. sip_header declared on a diameter step with an
// unknown op), and a scenario author should see everything wrong in one
// validate run rather than fixing one mistake at a time.
func malformValueErrors(stepLabel string, spec *MalformSpec) []ValidationError {
	var errs []ValidationError
	errs = append(errs, avpLengthValueErrors(stepLabel, spec.AVPLength)...)
	errs = append(errs, msgLengthValueErrors(stepLabel, spec.MsgLength)...)
	errs = append(errs, sipHeaderValueErrors(stepLabel, spec.SIPHeader)...)
	return errs
}

// validateMalform enforces the rules a malform: block must satisfy.
//
// Rule 1, the important one: malform: is meaningful only on action: send.
// Corruption is applied to an encoder's output, and a step that never
// encodes a message has nothing to corrupt — the same "validated but never
// dispatched" defect class the action-gate on impair: exists to prevent.
//
// Rule 2: avp_length/msg_length require a diameter step; sip_header
// requires a sip step (malformProtocolErrors).
//
// Rule 3: every declared sub-key's own value rules — AVP-name lookup,
// 3-byte range bounds, the sip_header op enum, and overwrite's required
// value (malformValueErrors).
//
// Every multi-error return is sorted by Field so validation output is
// deterministic, matching impairRangeErrors' own discipline.
func validateMalform(stepLabel string, step Step) []ValidationError {
	if step.Malform == nil {
		return nil
	}
	if step.Action != "send" {
		return []ValidationError{{
			StepID:  stepLabel,
			Field:   "malform",
			Message: fmt.Sprintf("malform is only valid on action: send, not %q", step.Action),
		}}
	}
	errs := malformProtocolErrors(stepLabel, step)
	errs = append(errs, malformValueErrors(stepLabel, step.Malform)...)
	sort.Slice(errs, func(i, j int) bool { return errs[i].Field < errs[j].Field })
	return errs
}

// validateDeclaredProtocol reports a step whose protocol is a known one but
// is not declared in this scenario's protocols: block. validateStepShape
// already rejects an UNKNOWN protocol name; this catches the other, more
// common mistake — a valid name with no matching entry. Adapters are built
// only for declared protocols, so before this the scenario validated clean
// and then failed at run time with `no adapter for protocol "sip"`, which
// reads like a broken build rather than a missing four-line block.
func validateDeclaredProtocol(stepLabel string, step Step, s *Scenario) []ValidationError {
	if step.Protocol == "" || !allowedProtocols[step.Protocol] {
		return nil // missing or unknown is validateStepShape's report, not a duplicate here
	}
	if _, declared := s.Protocols[step.Protocol]; declared {
		return nil
	}
	return []ValidationError{{StepID: stepLabel, Field: "protocol", Message: fmt.Sprintf(
		"step uses protocol %q but there is no %q entry under protocols: — add one (e.g. protocols: { %s: { transport: ... } })",
		step.Protocol, step.Protocol, step.Protocol)}}
}

// validateDeclaredProtocolTree applies validateDeclaredProtocol to a step and
// one level of parallel/reorder sub-steps, mirroring validateMalformTree.
func validateDeclaredProtocolTree(stepLabel string, step Step, s *Scenario) []ValidationError {
	errs := validateDeclaredProtocol(stepLabel, step, s)
	for j, sub := range step.Steps {
		subLabel := sub.ID
		if subLabel == "" {
			subLabel = fmt.Sprintf("%s[%d]", stepLabel, j)
		}
		errs = append(errs, validateDeclaredProtocol(subLabel, sub, s)...)
	}
	return errs
}

// validateMalformTree runs validateMalform on a step and, since Validate's
// per-step loop does not otherwise descend into a parallel step's
// sub-steps, on each of its sub-steps too — mirroring validateImpairTree
// exactly. It takes no *Scenario because no malform rule depends on
// transport (unlike sctp_abort's impair.transport rule).
func validateMalformTree(stepLabel string, step Step) []ValidationError {
	errs := validateMalform(stepLabel, step)
	for j, sub := range step.Steps {
		subLabel := sub.ID
		if subLabel == "" {
			subLabel = fmt.Sprintf("%s[%d]", stepLabel, j)
		}
		errs = append(errs, validateMalform(subLabel, sub)...)
	}
	return errs
}

// RawKeySet is the set of sub-keys a raw: block accepts. Named because the
// same concept is cross-checked outside this package: the raw properties
// published in docs/scenario.schema.json must match it exactly, and a test
// fails if the two drift apart.
type RawKeySet map[string]bool

// rawKeys is the authoritative set: exactly one of the two is required per
// step, enforced by validateRaw rather than by this set itself.
var rawKeys = RawKeySet{
	"hex": true,
	"ref": true,
}

// RawKeys returns a copy of the raw: sub-keys the validator accepts.
// Exported for the same reason MalformKeys is: the schema drift guard must
// assert against the authoritative set, not a duplicated literal that
// silently rots.
func RawKeys() RawKeySet {
	out := make(RawKeySet, len(rawKeys))
	maps.Copy(out, rawKeys)
	return out
}

// rawSourceErrors enforces the mutually-exclusive byte-source rule a raw:
// block must satisfy once we already know one is declared on an
// action: raw step: exactly one of hex or ref, and a non-empty hex must
// decode. Extracted from validateRaw to keep that function under CLAUDE.md's
// 40-line cap.
func rawSourceErrors(stepLabel string, r *RawSpec) []ValidationError {
	switch {
	case r.Hex != "" && r.Ref != "":
		return []ValidationError{{
			StepID: stepLabel, Field: "raw",
			Message: "hex and ref are mutually exclusive: a raw step has exactly one byte source",
		}}
	case r.Hex == "" && r.Ref == "":
		return []ValidationError{{StepID: stepLabel, Field: "raw", Message: "exactly one of hex or ref is required"}}
	}
	if r.Hex != "" {
		if _, err := hex.DecodeString(r.Hex); err != nil {
			return []ValidationError{{
				StepID: stepLabel, Field: "raw.hex",
				Message: fmt.Sprintf("invalid hex payload: %s", err),
			}}
		}
	}
	return nil
}

// validateRaw enforces the rules a raw: block must satisfy: action: raw
// requires one, a raw: block is meaningless on any other action, and
// rawSourceErrors checks the block's own byte-source shape.
func validateRaw(stepLabel string, step Step) []ValidationError {
	if step.Action == "raw" && step.Raw == nil {
		return []ValidationError{{
			StepID: stepLabel, Field: "raw",
			Message: "action: raw requires a raw: block naming its byte source (hex or ref)",
		}}
	}
	if step.Raw == nil {
		return nil
	}
	if step.Action != "raw" {
		return []ValidationError{{
			StepID: stepLabel, Field: "raw",
			Message: fmt.Sprintf("raw is only valid on action: raw, not %q", step.Action),
		}}
	}
	return rawSourceErrors(stepLabel, step.Raw)
}

// varRefPattern matches {{ variable_name }} template references.
var varRefPattern = regexp.MustCompile(`\{\{\s*(\S+?)\s*\}\}`)

// assertPattern matches simple assertion expressions like "x == 1" or "x != null".
var assertPattern = regexp.MustCompile(`^\s*\S+\s+(==|!=|>=|<=|>|<)\s+\S+\s*$`)

// ValidationError describes a single validation problem.
type ValidationError struct {
	Field   string
	StepID  string
	Message string
}

func (e *ValidationError) Error() string {
	if e.StepID != "" {
		return fmt.Sprintf("step %q field %s: %s", e.StepID, e.Field, e.Message)
	}
	return fmt.Sprintf("field %s: %s", e.Field, e.Message)
}

// Validate checks a parsed Scenario for structural and semantic errors.
func Validate(s *Scenario) []ValidationError {
	var errs []ValidationError

	// Required top-level fields.
	if s.Name == "" {
		errs = append(errs, ValidationError{Field: "name", Message: "required field is missing"})
	}
	if s.Version == "" {
		errs = append(errs, ValidationError{Field: "version", Message: "required field is missing"})
	}
	if len(s.Steps) == 0 {
		errs = append(errs, ValidationError{Field: "steps", Message: "at least one step is required"})
	}

	// Protocol configuration.
	for name, cfg := range s.Protocols {
		if !allowedProtocols[name] {
			errs = append(errs, ValidationError{
				Field:   "protocols." + name,
				Message: fmt.Sprintf("unknown protocol %q (allowed: sip, diameter)", name),
			})
			continue
		}
		errs = append(errs, validateTransport(name, cfg)...)
		errs = append(errs, validateCredentialScope(name, cfg)...)
	}

	// Build the set of known variable names: declared variables + step IDs (for capture references).
	known := make(map[string]bool)
	for k := range s.Variables {
		known[k] = true
	}
	// Protocol config keys are also resolvable.
	for name := range s.Protocols {
		known[name] = true
	}

	// Steps.
	seenIDs := make(map[string]bool)
	for i, step := range s.Steps {
		stepLabel := step.ID
		if stepLabel == "" {
			stepLabel = fmt.Sprintf("[%d]", i)
			errs = append(errs, ValidationError{
				StepID:  stepLabel,
				Field:   "id",
				Message: "required field is missing",
			})
		}

		if seenIDs[step.ID] && step.ID != "" {
			errs = append(errs, ValidationError{
				StepID:  step.ID,
				Field:   "id",
				Message: "duplicate step id",
			})
		}
		seenIDs[step.ID] = true

		if step.Action == "" {
			errs = append(errs, ValidationError{StepID: stepLabel, Field: "action", Message: "required field is missing"})
		} else if !allowedActions[step.Action] {
			errs = append(errs, ValidationError{
				StepID:  stepLabel,
				Field:   "action",
				Message: fmt.Sprintf("unknown action %q", step.Action),
			})
		}

		errs = append(errs, validateStepShape(stepLabel, step)...)
		errs = append(errs, validateDeclaredProtocolTree(stepLabel, step, s)...)
		errs = append(errs, validateImpairTree(stepLabel, step, s)...)
		errs = append(errs, validateMalformTree(stepLabel, step)...)
		errs = append(errs, validateRaw(stepLabel, step)...)
		errs = append(errs, validateSendOrderTree(stepLabel, step)...)

		// Diameter AVP field names against the embedded dictionary — before
		// the variable-reference check, so a typo'd field name is reported
		// as itself rather than only as an "undefined variable" miss.
		if step.Protocol == "diameter" && len(step.Fields) > 0 {
			errs = append(errs, validateAVPFields(stepLabel, step.Fields)...)
		}

		// Check variable references in string fields.
		for _, s := range collectStringFields(step) {
			for _, ref := range extractVarRefs(s) {
				// Allow dotted references like "diameter_auth.server_name" — check the root.
				root := strings.SplitN(ref, ".", 2)[0]
				if !known[root] {
					errs = append(errs, ValidationError{
						StepID:  stepLabel,
						Field:   "fields/target",
						Message: fmt.Sprintf("undefined variable reference %q", ref),
					})
				}
			}
		}

		// Body validation.
		if step.Body != nil {
			if step.Body.ContentType == "" {
				errs = append(errs, ValidationError{StepID: stepLabel, Field: "body.content_type", Message: "required when body is specified"})
			}
			if step.Body.Template == "" && step.Body.Content == "" {
				errs = append(errs, ValidationError{StepID: stepLabel, Field: "body", Message: "either template or content must be specified"})
			}
			if step.Body.Template != "" && step.Body.Content != "" {
				errs = append(errs, ValidationError{StepID: stepLabel, Field: "body", Message: "template and content are mutually exclusive"})
			}
		}

		// Assertions.
		for j, expr := range step.Assert {
			if !assertPattern.MatchString(expr) {
				errs = append(errs, ValidationError{
					StepID:  stepLabel,
					Field:   fmt.Sprintf("assert[%d]", j),
					Message: fmt.Sprintf("malformed assertion expression: %q", expr),
				})
			}
		}

		// Register step ID as a known variable root for subsequent steps.
		if step.ID != "" {
			known[step.ID] = true
		}
	}

	return errs
}

// collectStringFields gathers all user-facing string values from a step that may contain variable references.
func collectStringFields(s Step) []string {
	var out []string
	out = append(out, s.Target, s.Message, s.Check)
	for _, v := range s.Fields {
		out = append(out, v)
	}
	for _, v := range s.Capture {
		out = append(out, v)
	}
	// avp_length.name is a dictionary identifier, never templated, so only
	// sip_header.value (author-written free text) is checked here — guard
	// both pointer levels since either may be nil independently.
	if s.Malform != nil && s.Malform.SIPHeader != nil {
		out = append(out, s.Malform.SIPHeader.Value)
	}
	// raw.hex is a literal wire payload, never templated; raw.ref is the one
	// field a raw step's byte source may reference a captured variable
	// through, so only it is checked here.
	if s.Raw != nil {
		out = append(out, s.Raw.Ref)
	}
	return out
}

// extractVarRefs returns all variable names referenced via {{ ... }} in a string.
func extractVarRefs(s string) []string {
	matches := varRefPattern.FindAllStringSubmatch(s, -1)
	refs := make([]string, 0, len(matches))
	for _, m := range matches {
		refs = append(refs, m[1])
	}
	return refs
}
