package engine

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/protocols/common/malform"
	"github.com/riftcell/cellrift/internal/scenario"
)

// AssertionError indicates a step assertion failed.
type AssertionError struct {
	Expression string
	Actual     string
}

func (e *AssertionError) Error() string {
	return fmt.Sprintf("assertion failed: %s (actual: %s)", e.Expression, e.Actual)
}

// foldImpairConfig copies a step's declared impairment into the config map
// the adapter receives, exactly as check and timeout_ms are folded. Zero
// values are omitted rather than written as "0" (and false is omitted
// rather than written as "false"): an absent key and a zero/false value
// mean the same thing to the adapter, and omitting keeps the map free of
// noise for the overwhelmingly common unimpaired step.
//
// latency_ms and jitter_ms are consumed by the impairment transport
// decorator. delay_ms, drop and duplicate are consumed by the adapter
// itself: delay_ms after a complete response has been parsed, drop before
// any Send happens, and duplicate as a second Send of the identical
// already-built bytes. The four connection-fault keys (reset, truncate,
// abrupt_close, sctp_abort) are also adapter-consumed, fired after the
// step's own Send succeeds and before its Receive — except truncate, which
// replaces the Send itself with a deliberate short write. All nine travel
// the same way because config is the one channel a step already has into
// Execute.
func foldImpairConfig(config map[string]string, spec *scenario.ImpairSpec) {
	if spec == nil {
		return
	}
	for key, v := range map[string]int{
		"latency_ms": spec.LatencyMs,
		"jitter_ms":  spec.JitterMs,
		"delay_ms":   spec.DelayMs,
	} {
		if v > 0 {
			config[key] = strconv.Itoa(v)
		}
	}
	foldImpairFlags(config, spec)
}

// foldImpairFlags folds the boolean delivery- and connection-fault-
// impairment keys (drop, duplicate, reset, truncate, abrupt_close,
// sctp_abort) into config. Split out of foldImpairConfig because the omit
// rule for a bool ("if v") differs from the omit rule for a millisecond
// count ("if v > 0"), so the two cannot share one range loop, and keeping
// both loops inline would push foldImpairConfig past CLAUDE.md's 40-line cap.
func foldImpairFlags(config map[string]string, spec *scenario.ImpairSpec) {
	for key, v := range map[string]bool{
		"drop":         spec.Drop,
		"duplicate":    spec.Duplicate,
		"reset":        spec.Reset,
		"truncate":     spec.Truncate,
		"abrupt_close": spec.AbruptClose,
		"sctp_abort":   spec.SCTPAbort,
	} {
		if v {
			config[key] = "true"
		}
	}
}

// foldMalformConfig copies a step's declared malform: block into the config
// map the adapter receives, exactly as impair: is folded. Unlike
// foldImpairConfig's "if v > 0" omit rule, a declared malformation is
// written even when its numeric value is zero: msg_length: 0 is a
// legitimate, deliberately-wrong declaration, not an absent one, and
// avp_length.value has no zero-is-absent ambiguity to begin with since
// AVPLength is itself a pointer. state is threaded in — which
// foldImpairConfig does not need — solely because sip_header.value is
// author-written free text that may carry a {{ var }} reference.
func foldMalformConfig(config map[string]string, spec *scenario.MalformSpec, state *State) error {
	if spec == nil {
		return nil
	}
	if spec.AVPLength != nil {
		config[malform.AVPLengthNameKey] = spec.AVPLength.Name
		config[malform.AVPLengthValueKey] = strconv.Itoa(spec.AVPLength.Value)
	}
	if spec.MsgLength != nil {
		config[malform.MsgLengthKey] = strconv.Itoa(*spec.MsgLength)
	}
	if spec.SIPHeader != nil {
		return foldSIPHeaderMalform(config, spec.SIPHeader, state)
	}
	return nil
}

// foldSIPHeaderMalform writes a declared sip_header override's name and op
// unconditionally, and its value only when non-empty (empty is legitimate
// for op: missing, which never reads it) — resolved through state.Resolve
// first, since it is the one malform: field a scenario author can reference
// a captured variable from.
func foldSIPHeaderMalform(config map[string]string, h *scenario.SIPHeaderMalform, state *State) error {
	config[malform.SIPHeaderNameKey] = h.Name
	config[malform.SIPHeaderOpKey] = h.Op
	if h.Value == "" {
		return nil
	}
	resolved, err := state.Resolve(h.Value)
	if err != nil {
		return fmt.Errorf("resolve malform.sip_header.value: %w", err)
	}
	config[malform.SIPHeaderValueKey] = resolved
	return nil
}

// foldRawConfig decodes a step's declared raw: byte source into the single
// config key both adapters' executeRaw read (malform.RawPayloadKey). Nil
// spec is a no-op. Hex is decoded HERE, once, so neither adapter ever
// handles an encoding — the same "decode once, in the engine" discipline
// applies uniformly to every raw step regardless of which protocol dispatches
// it. A ref resolves through state.Resolve exactly like every other templated
// step field; a []byte captured through response.__raw survives state.Resolve
// unchanged because formatValue's []byte branch is a raw string(val) copy
// with no UTF-8 validation, so the resolved string carries the original wire
// bytes verbatim even when they are not valid UTF-8.
func foldRawConfig(config map[string]string, spec *scenario.RawSpec, state *State) error {
	if spec == nil {
		return nil
	}
	if spec.Hex != "" {
		decoded, err := hex.DecodeString(spec.Hex)
		if err != nil {
			return fmt.Errorf("decode raw.hex: %w", err)
		}
		config[malform.RawPayloadKey] = string(decoded)
		return nil
	}
	resolved, err := state.Resolve(spec.Ref)
	if err != nil {
		return fmt.Errorf("resolve raw.ref: %w", err)
	}
	config[malform.RawPayloadKey] = resolved
	return nil
}

// stepInputs is one step's fully resolved dispatch arguments: everything
// adapter.Execute takes, computed but not yet handed to any adapter. It
// exists because a reorder group must resolve every sub-step BEFORE any of
// them reaches the wire — resolving and dispatching in one breath, as
// ExecuteStep does, would send each sub-step individually and defeat the
// entire point of controlling their wire order.
type stepInputs struct {
	Target  string
	Message string
	Fields  map[string]string
	Config  map[string]string
}

// resolveStepInputs performs every template-variable resolution and config
// fold ExecuteStep used to do inline. Behaviour is unchanged: the error
// texts, the __body_* reserved-key injection and the check/timeout_ms/
// impair folds are lifted verbatim, not rewritten.
func resolveStepInputs(step *scenario.Step, state *State, protocolConfig map[string]string) (stepInputs, error) {
	target, err := state.Resolve(step.Target)
	if err != nil {
		return stepInputs{}, fmt.Errorf("step %s: resolve target: %w", step.ID, err)
	}

	message, err := state.Resolve(step.Message)
	if err != nil {
		return stepInputs{}, fmt.Errorf("step %s: resolve message: %w", step.ID, err)
	}

	fields, err := state.ResolveMap(step.Fields)
	if err != nil {
		return stepInputs{}, fmt.Errorf("step %s: resolve fields: %w", step.ID, err)
	}

	check, err := state.Resolve(step.Check)
	if err != nil {
		return stepInputs{}, fmt.Errorf("step %s: resolve check: %w", step.ID, err)
	}

	config, err := buildStepConfig(step, protocolConfig, check, state)
	if err != nil {
		return stepInputs{}, err
	}

	fields = injectBodyFields(fields, step.Body)

	return stepInputs{Target: target, Message: message, Fields: fields, Config: config}, nil
}

// buildStepConfig merges protocolConfig with every step-level override
// (check, timeout_ms, impair:, malform:, raw:) into the single config map
// the adapter's Execute receives. Extracted from resolveStepInputs to keep
// that function under the file's 40-line convention once malform:/raw:
// folding grew the merge logic beyond a single inline block.
func buildStepConfig(step *scenario.Step, protocolConfig map[string]string, check string, state *State) (map[string]string, error) {
	config := make(map[string]string)
	for k, v := range protocolConfig {
		config[k] = v
	}
	if err := resolveCredentialTemplates(config, step.ID, state); err != nil {
		return nil, err
	}
	if check != "" {
		config["check"] = check
	}
	if step.TimeoutMs > 0 {
		config["timeout_ms"] = strconv.Itoa(step.TimeoutMs)
	}
	foldImpairConfig(config, step.Impair)
	if err := foldMalformConfig(config, step.Malform, state); err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	if err := foldRawConfig(config, step.Raw, state); err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}
	return config, nil
}

// templatedProtocolKeys are the protocol-config keys whose values may be a
// {{ var }} template. The protocol block is otherwise copied verbatim:
// transport, ports and certificate paths have never been templated, so this is
// a deliberately short, named list rather than a blanket resolve over the whole
// map. Resolving everything would silently change the meaning of a cert path
// containing braces.
//
// A credential template that does not resolve is an ERROR. It must not fall
// through to the credentials file or to CELLRIFT_SIP_PASSWORD: a typo in
// {{ sip_password }} would otherwise quietly authenticate with the ambient
// credential instead of the one the scenario named. (This is layer 1 of the
// credential precedence; CELLRIFT_VAR_<KEY> feeds these templates through
// State.LoadEnvOverrides and is distinct from CELLRIFT_SIP_PASSWORD.)
var templatedProtocolKeys = []string{"username", "password", "credentials_file"}

// resolveCredentialTemplates resolves the {{ var }} templates in
// templatedProtocolKeys, in place. The resolved value is never formatted into
// an error, only the key name (and State.Resolve names the variable, not its
// value).
func resolveCredentialTemplates(config map[string]string, stepID string, state *State) error {
	for _, k := range templatedProtocolKeys {
		v := config[k]
		if v == "" {
			continue
		}
		resolved, err := state.Resolve(v)
		if err != nil {
			return fmt.Errorf("step %s: resolve protocol credential %q: %w", stepID, k, err)
		}
		config[k] = resolved
	}
	return nil
}

// injectBodyFields adds a step's optional body as special fields so the
// adapter can attach it to the message. Keys are prefixed with "__body_" to
// avoid collision with protocol headers. Extracted from resolveStepInputs
// because it is a distinct responsibility (body injection) from template
// resolution and config folding.
func injectBodyFields(fields map[string]string, body *scenario.StepBody) map[string]string {
	if body == nil {
		return fields
	}
	if fields == nil {
		fields = make(map[string]string)
	}
	if body.ContentType != "" {
		fields["__body_content_type"] = body.ContentType
	}
	if body.Template != "" {
		fields["__body_template"] = body.Template
	} else if body.Content != "" {
		fields["__body_content"] = body.Content
	}
	return fields
}

// applyStepOutcome runs a step's capture: and assert: against a result,
// whatever produced that result. Split out of ExecuteStep's tail because a
// reorder group's sub-step results come back from a batch call, not from
// adapter.Execute, and must still be captured and asserted against their
// OWN declared step — a scenario author capturing step_b.session_id expects
// it keyed to step_b regardless of when step_b's message physically went
// out. Nil-safe on result: a sub-step the adapter could not produce a
// result for has nothing to capture.
func applyStepOutcome(step *scenario.Step, state *State, result *common.StepResult) error {
	if result == nil {
		return nil
	}
	// Capture is attempted even with no parsed response map: response.__raw
	// is exactly the path an unparseable response is captured through.
	if len(step.Capture) > 0 {
		captureFromResponse(step, state, result)
	}
	if len(step.Assert) == 0 {
		return nil
	}
	// Assertions run regardless of the step's own status. impair.FaultedResult
	// has always promised "the step's assertions still run", but this used to
	// be gated on StatusPassed, so every assertion on a faulted or errored
	// step was dead code — the shipped connection-fault scenario's three
	// `response.connection_state == ...` checks never executed once. Running
	// them can only ADD a failure: a passing assertion does not rescue a step
	// the adapter already failed (that would silently change what a fault
	// step means), and a failing one is appended to the adapter's own error
	// rather than overwriting it, so the root cause is never lost.
	err := evaluateAssertions(step, state, result)
	if err == nil {
		return nil
	}
	if result.Status == common.StatusFailed && result.Error != "" {
		result.Error = result.Error + "; " + err.Error()
	} else {
		result.Error = err.Error()
	}
	result.Status = common.StatusFailed
	return err
}

// ExecuteStep resolves variables, dispatches to the protocol adapter, captures
// values, and evaluates assertions.
//
// Preserved subtlety: on assertion failure this returns (result, err) with
// result already mutated to failed status — never (nil, err). applyStepOutcome
// mutates result in place and this function returns that same result
// alongside the error, exactly as the pre-split code did; the runner's own
// failure accounting (StepReport.Result vs StepReport.Error) depends on this.
func ExecuteStep(step *scenario.Step, state *State, adapter common.ProtocolAdapter, protocolConfig map[string]string) (*common.StepResult, error) {
	in, err := resolveStepInputs(step, state, protocolConfig)
	if err != nil {
		return nil, err
	}

	result, err := adapter.Execute(step.Action, in.Target, in.Message, in.Fields, in.Config)
	if err != nil {
		return nil, fmt.Errorf("step %s: %w", step.ID, err)
	}

	if err := applyStepOutcome(step, state, result); err != nil {
		return result, err
	}

	return result, nil
}

// captureFromResponse extracts values from the step result response and
// stores them in state. Capture map format: stateKey → response.path
//
// Example: "server_name": "response.Server-Name" captures the Server-Name
// AVP value from the response and stores it as "server_name" in state.
// The captured value is also stored with step ID prefix for cross-step
// references: "diameter_auth.server_name".
//
// A capture whose path is absent is skipped rather than failed. Capture runs
// on failed steps too (a timed-out step has no response at all), so erroring
// here would bury the step's real failure under a pile of "nothing to
// capture" noise. The vacuous-pass risk this used to leave open — capture
// silently skips, a later `assert: x != 0` passes against nothing — is
// closed downstream instead: evaluateAssertion now errors when an operand
// is absent, so the missing capture surfaces at the assertion that
// depended on it.
func captureFromResponse(step *scenario.Step, state *State, result *common.StepResult) {
	for stateKey, responsePath := range step.Capture {
		val, found := extractResponseValue(result, responsePath)
		if found {
			state.Set(stateKey, val)
			// Also store with step ID prefix for cross-step references.
			state.Set(step.ID+"."+stateKey, val)
		}
	}
}

// rawResponsePath is the reserved capture/assert path that yields the raw
// response bytes instead of a field from the parsed response map. The "__"
// prefix follows the same reserved-key convention as the "__body_" fields
// the executor injects into a step's field map: a double underscore marks a
// name the engine owns, which no protocol field will ever collide with.
const rawResponsePath = "response.__raw"

// extractResponseValue navigates a dotted path like "response.header.Contact"
// through the response map, with one reserved exception: rawResponsePath
// returns the unparsed response bytes. Raw bytes are the only observable
// output when a response cannot be parsed at all, which is precisely the
// case a malformed- or cross-protocol-traffic scenario creates.
//
// The second return distinguishes "absent" from "present". Callers must not
// infer absence from a nil value: an absent path used to collapse into nil,
// and nil formatted as the string "<nil>", so `missing_field != 503`
// compared "<nil>" against "503" and passed — a typo'd field name produced a
// green assertion forever.
func extractResponseValue(result *common.StepResult, path string) (interface{}, bool) {
	if path == rawResponsePath {
		if len(result.RawResponse) == 0 {
			return nil, false // absent, not an empty capture — keeps "!= null" honest
		}
		return result.RawResponse, true
	}

	// Strip the "response." prefix if present.
	path = strings.TrimPrefix(path, "response.")

	parts := strings.Split(path, ".")
	// Convert to map[string]interface{} so type assertions inside the loop work
	// uniformly for both the top-level ResponseFields and any nested sub-maps.
	// A nil result.Response still satisfies the type assertion and yields
	// !ok on lookup, so walking it here is safe.
	var current interface{} = map[string]interface{}(result.Response)

	for _, part := range parts {
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// evaluateAssertions checks all assertion expressions against the current
// state and step result. Returns an AssertionError on the first failure.
//
// Supported formats:
//   - "variable == value"           — equality check
//   - "variable != null"            — non-null check
//   - "response.field == value"     — response field check
func evaluateAssertions(step *scenario.Step, state *State, result *common.StepResult) error {
	for _, expr := range step.Assert {
		if err := evaluateAssertion(expr, state, result); err != nil {
			return err
		}
	}
	return nil
}

// operators lists the supported assertion operators in precedence order.
// Longer tokens are checked first so that ">=" is not mismatched as ">".
var operators = []string{" >= ", " <= ", " != ", " == ", " > ", " < "}

func evaluateAssertion(expr string, state *State, result *common.StepResult) error {
	lhs, op, rhs, err := splitAssertion(expr)
	if err != nil {
		return err
	}

	actualVal, found := resolveAssertionValue(lhs, state, result)

	// A null comparison is the ONE sanctioned way to talk about an absent
	// operand, so it is evaluated before the presence check below.
	if rhs == "null" {
		return compareNull(expr, op, actualVal)
	}

	// Any other comparison against something that does not exist is an
	// authoring error, never a verdict. Before this check an absent operand
	// was compared as the string "<nil>", so `missing != 503` passed and
	// `missing == 200` failed with an unhelpful actual — a typo'd field name
	// or a capture that found nothing produced a permanently green test.
	if !found {
		return fmt.Errorf("assertion %q: %q is not present in the response or captured state (use \"== null\" to assert absence)", expr, lhs)
	}

	return compareValue(expr, op, formatValue(actualVal), rhs)
}

// splitAssertion parses "lhs op rhs", trying longer operators first so that
// ">=" is never mismatched as ">". A right-hand side wrapped in one matching
// pair of quotes has them removed: in YAML, `- x == "truncated"` is a plain
// scalar whose quotes are literal characters, so the comparison used to be
// against the six characters `"truncated"` and could never match the value
// `truncated`. Every shipped connection-fault assertion was written this way.
// Only a fully wrapped pair is stripped; a value that merely starts or ends
// with a quote (a quoted SIP display name inside a longer header) is left
// alone.
func splitAssertion(expr string) (lhs, op, rhs string, err error) {
	for _, candidate := range operators {
		if idx := strings.Index(expr, candidate); idx >= 0 {
			rhs = unquote(strings.TrimSpace(expr[idx+len(candidate):]))
			return strings.TrimSpace(expr[:idx]), strings.TrimSpace(candidate), rhs, nil
		}
	}
	return "", "", "", fmt.Errorf("unsupported assertion syntax: %s", expr)
}

// unquote removes one matching pair of surrounding double or single quotes.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// compareNull evaluates `x == null` / `x != null`. An absent value and an
// empty string both count as null, matching how a scenario author reads
// "the field is not there".
func compareNull(expr, op string, actualVal interface{}) error {
	isNull := actualVal == nil
	if s, ok := actualVal.(string); ok && s == "" {
		isNull = true
	}
	switch op {
	case "==":
		if !isNull {
			return &AssertionError{Expression: expr, Actual: fmt.Sprintf("%v", actualVal)}
		}
	case "!=":
		if isNull {
			return &AssertionError{Expression: expr, Actual: "null"}
		}
	default:
		return fmt.Errorf("operator %s cannot be used with null: %s", op, expr)
	}
	return nil
}

// compareValue evaluates a present operand against a non-null right-hand
// side: string equality for == and !=, float comparison for the ordering
// operators.
func compareValue(expr, op, actualStr, rhs string) error {
	switch op {
	case "==":
		if actualStr != rhs {
			return &AssertionError{Expression: expr, Actual: actualStr}
		}
	case "!=":
		if actualStr == rhs {
			return &AssertionError{Expression: expr, Actual: actualStr}
		}
	case ">=", "<=", ">", "<":
		actualNum, errA := strconv.ParseFloat(actualStr, 64)
		expectedNum, errE := strconv.ParseFloat(rhs, 64)
		if errA != nil || errE != nil {
			return fmt.Errorf("numeric assertion %q: cannot compare %q and %q as numbers", expr, actualStr, rhs)
		}
		if !numericCompare(op, actualNum, expectedNum) {
			return &AssertionError{Expression: expr, Actual: actualStr}
		}
	}
	return nil
}

// numericCompare evaluates a two-operand numeric comparison.
func numericCompare(op string, a, b float64) bool {
	switch op {
	case ">=":
		return a >= b
	case "<=":
		return a <= b
	case ">":
		return a > b
	default: // "<"
		return a < b
	}
}

// EvaluateAssertionPublic evaluates a single assertion expression against
// the given state. Used by the `cellrift assert` command.
func EvaluateAssertionPublic(expr string, state *State) error {
	return evaluateAssertion(expr, state, &common.StepResult{
		Response: common.ResponseFields{},
	})
}

// resolveAssertionValue looks up the value for an assertion operand,
// checking response fields first, then state variables. The second return
// reports whether the operand exists anywhere; evaluateAssertion treats a
// missing operand as an authoring error rather than comparing against nil.
func resolveAssertionValue(key string, state *State, result *common.StepResult) (interface{}, bool) {
	// Check response fields via dotted path (e.g. "response.status_code").
	// No guard suppresses response.__raw here: capture: and assert: share
	// this one lookup function by design, so a raw-response assertion is a
	// documented capability, not an accident.
	if strings.HasPrefix(key, "response.") {
		if val, found := extractResponseValue(result, key); found {
			return val, true
		}
	}

	// Check state variables (includes captured values).
	if val, ok := state.Get(key); ok {
		return val, true
	}

	return nil, false
}
