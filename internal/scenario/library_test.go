package scenario_test

// library_test.go — CI/CD schema validation tests for the built-in scenario library.
//
// Two complementary strategies are used, both with zero dependencies beyond the
// standard library and the packages already in go.mod:
//
//  1. Dynamic scenario discovery: walk scenarios/ and run ParseFile + Validate on
//     every *.yaml file found. New scenarios are covered automatically without
//     needing to update a test list.
//
//  2. JSON schema consistency: load docs/scenario.schema.json with encoding/json
//     and verify it is well-formed, has the expected top-level structure, and
//     that its protocol/action enum values match the Go validator's allow-lists.
//     This catches drift between the schema file (used by IDEs) and the Go
//     validation logic (the canonical source of truth).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// moduleRoot returns the path two levels above this package (internal/scenario/ → repo root).
func moduleRoot() string {
	return filepath.Join("..", "..")
}

// ---- Dynamic scenario library tests ----------------------------------------

// TestLibrary_AllScenariosParseAndValidate walks scenarios/ recursively and
// asserts every *.yaml file parses without error and passes Validate.
// This test runs automatically over any newly added scenario files.
func TestLibrary_AllScenariosParseAndValidate(t *testing.T) {
	dir := filepath.Join(moduleRoot(), "scenarios")

	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && (filepath.Ext(path) == ".yaml" || filepath.Ext(path) == ".yml") {
			files = append(files, path)
		}
		return nil
	})
	require.NoError(t, err, "walking scenarios/ directory failed")
	require.NotEmpty(t, files, "no scenario files found under scenarios/ — at least one required")

	sort.Strings(files) // deterministic subtest ordering

	for _, path := range files {
		rel, _ := filepath.Rel(filepath.Join(moduleRoot(), "scenarios"), path)
		t.Run(rel, func(t *testing.T) {
			s, parseErr := scenario.ParseFile(path)
			require.NoError(t, parseErr, "ParseFile should succeed")

			valErrs := scenario.Validate(s)
			assert.Empty(t, valErrs, "Validate should return no errors")

			// Every library scenario must have a name, version, and at least 2 steps.
			assert.NotEmpty(t, s.Name, "scenario must have a name")
			assert.NotEmpty(t, s.Version, "scenario must have a version")
			assert.GreaterOrEqual(t, len(s.Steps), 2, "library scenarios should have at least 2 steps")
		})
	}
}

// TestLibrary_ScenarioCount ensures the expected number of built-in scenarios exist.
// Update the constant when adding or removing scenarios deliberately.
const expectedScenarioCount = 19 // selftest/loopback.yaml, diameter/sctp_abort.yaml, ims/register_parallel_wait.yaml and ims/register_digest_auth.yaml added

func TestLibrary_ScenarioCount(t *testing.T) {
	dir := filepath.Join(moduleRoot(), "scenarios")
	var count int
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(path) == ".yaml" {
			count++
		}
		return nil
	})
	assert.Equal(t, expectedScenarioCount, count,
		"expected %d built-in scenario files; update expectedScenarioCount if this is intentional",
		expectedScenarioCount)
}

// ---- JSON schema consistency tests -----------------------------------------

// loadSchema parses docs/scenario.schema.json into a generic map for inspection.
func loadSchema(t *testing.T) map[string]interface{} {
	t.Helper()
	path := filepath.Join(moduleRoot(), "docs", "scenario.schema.json")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "docs/scenario.schema.json must exist")

	var schema map[string]interface{}
	require.NoError(t, json.Unmarshal(data, &schema), "docs/scenario.schema.json must be valid JSON")
	return schema
}

// TestSchema_IsValidJSON verifies the schema file exists and parses as valid JSON.
func TestSchema_IsValidJSON(t *testing.T) {
	loadSchema(t) // fails the test if missing or malformed
}

// TestSchema_TopLevelStructure verifies the schema has the keys expected by
// JSON Schema draft-07 and the properties Cellrift scenarios require.
func TestSchema_TopLevelStructure(t *testing.T) {
	schema := loadSchema(t)

	assert.Equal(t, "http://json-schema.org/draft-07/schema#", schema["$schema"],
		"schema must declare JSON Schema draft-07")
	assert.Equal(t, "object", schema["type"])

	required, ok := schema["required"].([]interface{})
	require.True(t, ok, "required must be an array")
	requiredSet := make(map[string]bool)
	for _, v := range required {
		requiredSet[v.(string)] = true
	}
	for _, field := range []string{"name", "version", "steps"} {
		assert.True(t, requiredSet[field], "schema must require field %q", field)
	}

	props, ok := schema["properties"].(map[string]interface{})
	require.True(t, ok, "schema must have properties")
	for _, key := range []string{"name", "version", "description", "protocols", "variables", "steps"} {
		assert.Contains(t, props, key, "schema properties must include %q", key)
	}

	assert.Contains(t, schema, "$defs", "schema must have $defs for reusable definitions")
}

// TestSchema_ProtocolEnumsMatchValidator verifies the protocol enum values in the
// JSON schema match the protocols the Go validator accepts. Catches drift between
// docs/scenario.schema.json (IDE tooling) and internal/scenario/validator.go.
func TestSchema_ProtocolEnumsMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	// Navigate: properties → protocols → properties → (sip|diameter)
	props := schema["properties"].(map[string]interface{})
	protocols, ok := props["protocols"].(map[string]interface{})
	require.True(t, ok, "properties.protocols must exist")
	protocolProps, ok := protocols["properties"].(map[string]interface{})
	require.True(t, ok, "properties.protocols.properties must exist")

	// The Go validator allows exactly "sip" and "diameter" today.
	goAllowed := map[string]bool{"sip": true, "diameter": true}
	for name := range goAllowed {
		assert.Contains(t, protocolProps, name,
			"JSON schema protocols.properties must include %q (allowed by Go validator)", name)
	}
	for name := range protocolProps {
		assert.True(t, goAllowed[name],
			"JSON schema protocol %q not in Go validator allowedProtocols — update one or the other", name)
	}
}

// TestSchema_TransportEnumsMatchValidator verifies the per-protocol transport
// enums in the JSON schema match the transports the Go validator accepts.
//
// This guard exists because the two drifted apart: the schema allowed only
// udp/tcp for SIP while scenarios/ims/basic_call_tls.yaml shipped with
// transport: tls, so every IDE loading the published schema flagged the
// project's own example as invalid while `cellrift validate` passed it.
func TestSchema_TransportEnumsMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")

	// Maps the Go validator's protocol name to the $defs key holding its config.
	defKeys := map[string]string{
		"sip":      "protocolConfig",
		"diameter": "diameterProtocolConfig",
	}

	for protocol, goAllowed := range scenario.AllowedTransports() {
		defKey, ok := defKeys[protocol]
		require.True(t, ok, "no schema $defs key mapped for protocol %q", protocol)

		def, ok := defs[defKey].(map[string]interface{})
		require.True(t, ok, "$defs.%s must exist", defKey)
		defProps, ok := def["properties"].(map[string]interface{})
		require.True(t, ok, "$defs.%s.properties must exist", defKey)
		transportDef, ok := defProps["transport"].(map[string]interface{})
		require.True(t, ok, "$defs.%s.properties.transport must exist", defKey)
		enumRaw, ok := transportDef["enum"].([]interface{})
		require.True(t, ok, "$defs.%s transport must have an enum array", defKey)

		schemaTransports := make(map[string]bool)
		for _, v := range enumRaw {
			schemaTransports[v.(string)] = true
		}

		for transport := range goAllowed {
			assert.True(t, schemaTransports[transport],
				"Go validator allows %s transport %q but the JSON schema enum omits it — update docs/scenario.schema.json",
				protocol, transport)
		}
		for transport := range schemaTransports {
			assert.True(t, goAllowed[transport],
				"JSON schema allows %s transport %q but the Go validator rejects it — update internal/scenario/validator.go",
				protocol, transport)
		}
	}
}

// TestSchema_ActionEnumsMatchValidator verifies the action enum values in the JSON
// schema match the actions the Go validator accepts.
func TestSchema_ActionEnumsMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	// Navigate: $defs → step → properties → action → enum
	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")
	actionDef, ok := stepProps["action"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.action must exist")
	enumRaw, ok := actionDef["enum"].([]interface{})
	require.True(t, ok, "action must have an enum array")

	schemaActions := make(map[string]bool)
	for _, v := range enumRaw {
		schemaActions[v.(string)] = true
	}

	// The Go validator's own allow-list, not a copy of it: a literal here
	// would have to be edited in lockstep with validator.go, which is the
	// exact drift this test exists to catch.
	goAllowed := scenario.AllowedActions()
	for action := range goAllowed {
		assert.True(t, schemaActions[action],
			"Go validator action %q missing from JSON schema enum — update docs/scenario.schema.json", action)
	}
	for action := range schemaActions {
		assert.True(t, goAllowed[action],
			"JSON schema action %q not in Go validator allowedActions — update internal/scenario/validator.go", action)
	}
}

// TestSchema_WaitStepShapeMatchesValidator guards against split-brain drift
// between the Go validator's validateStepShape (internal/scenario/validator.go)
// and this schema's wait-step branch: both encode the same rule — a wait
// step needs no protocol but a positive duration_ms — and an IDE that flags a
// scenario `cellrift validate` accepts (or vice versa) is exactly the bug
// this test exists to catch.
func TestSchema_WaitStepShapeMatchesValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")

	durationDef, ok := stepProps["duration_ms"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.duration_ms must exist")
	assert.Equal(t, "integer", durationDef["type"])
	assert.Equal(t, float64(1), durationDef["minimum"])
	_, hasDefault := durationDef["default"]
	assert.False(t, hasDefault, "duration_ms must not carry a default — a missing wait duration is an error, not a silent no-op")

	elseBranch, ok := stepDef["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else must exist")
	elseIf, ok := elseBranch["if"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.if must exist")
	elseIfProps, ok := elseIf["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.if.properties must exist")
	actionConst, ok := elseIfProps["action"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.if.properties.action must exist")
	assert.Equal(t, "wait", actionConst["const"])

	elseThen, ok := elseBranch["then"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.then must exist")
	elseThenRequired := toStringSlice(t, elseThen["required"])
	assert.Contains(t, elseThenRequired, "duration_ms")
	assert.NotContains(t, elseThenRequired, "protocol")

	// elseBranch.else nests a further if/then/else for action: raw (this
	// plan's own addition — TestSchema_RawStepShapeMatchesValidator covers
	// that branch directly), so the plain "requires protocol only" default
	// now lives one level deeper, at elseBranch.else.else, rather than
	// directly on elseBranch.else itself.
	elseElse, ok := elseBranch["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else must exist")
	elseElseElse, ok := elseElse["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.else must exist")
	elseElseElseRequired := toStringSlice(t, elseElseElse["required"])
	assert.Contains(t, elseElseElseRequired, "protocol")
}

// TestSchema_ReorderStepShapeMatchesValidator guards against split-brain
// drift between the Go validator's reorder rules (internal/scenario/
// validator.go's allowedActions, reorderShapeErrors, validateSendOrderTree)
// and this schema's reorder branch: both encode the same rule — a reorder
// step needs sub-steps and a send_order field exists, minimum 1 — modeled on
// TestSchema_WaitStepShapeMatchesValidator's approach for the sibling
// container action.
func TestSchema_ReorderStepShapeMatchesValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")

	actionDef, ok := stepProps["action"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.action must exist")
	enumRaw, ok := actionDef["enum"].([]interface{})
	require.True(t, ok, "action must have an enum array")
	var hasReorder bool
	for _, v := range enumRaw {
		if v == "reorder" {
			hasReorder = true
		}
	}
	assert.True(t, hasReorder, "action enum must include \"reorder\"")

	sendOrderDef, ok := stepProps["send_order"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.send_order must exist")
	assert.Equal(t, "integer", sendOrderDef["type"])
	assert.Equal(t, float64(1), sendOrderDef["minimum"])

	ifBranch, ok := stepDef["if"].(map[string]interface{})
	require.True(t, ok, "$defs.step.if must exist")
	ifProps, ok := ifBranch["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.if.properties must exist")
	actionCond, ok := ifProps["action"].(map[string]interface{})
	require.True(t, ok, "$defs.step.if.properties.action must exist")
	actionEnum := toStringSlice(t, actionCond["enum"])
	assert.Contains(t, actionEnum, "reorder", "the parallel/reorder shared if-branch must cover reorder")

	thenBranch, ok := stepDef["then"].(map[string]interface{})
	require.True(t, ok, "$defs.step.then must exist")
	thenRequired := toStringSlice(t, thenBranch["required"])
	assert.Contains(t, thenRequired, "steps", "a reorder step requires steps, the same as parallel")
}

// toStringSlice converts a decoded JSON array (of strings) into []string.
func toStringSlice(t *testing.T, v interface{}) []string {
	t.Helper()
	raw, ok := v.([]interface{})
	require.True(t, ok, "expected a JSON array, got %T", v)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := item.(string)
		require.True(t, ok, "expected array of strings, got element %T", item)
		out = append(out, s)
	}
	return out
}

// TestSchema_FieldsDescriptionDefersToValidator pins a deliberate decision:
// the schema's step `fields` property stays loosely typed (no enum, no
// propertyNames pattern) and its description says `cellrift validate` is the
// authoritative source for AVP-name correctness. The dictionary is
// approaching two hundred entries and growing; a future well-meaning change
// that starts hand-syncing an AVP enum here has to argue with this test
// first.
func TestSchema_FieldsDescriptionDefersToValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")
	fieldsDef, ok := stepProps["fields"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.fields must exist")

	description, ok := fieldsDef["description"].(string)
	require.True(t, ok, "fields.description must be a string")
	assert.Contains(t, description, "cellrift validate",
		"fields description must defer AVP-name correctness to cellrift validate")

	_, hasEnum := fieldsDef["enum"]
	assert.False(t, hasEnum, "fields must not carry an enum of AVP names")
	_, hasPropertyNames := fieldsDef["propertyNames"]
	assert.False(t, hasPropertyNames, "fields must not carry a propertyNames pattern of AVP names")
}

// TestSchema_ImpairKeysMatchValidator verifies the impair: sub-key set
// published in docs/scenario.schema.json matches scenario.ImpairKeys(), the
// Go validator's authoritative accepted-key set. impairFieldValues is only
// the numeric-range subset of that set (it excludes drop/duplicate, which
// have no range to check) and is no longer the source of truth here. If
// either side gains or loses a key without the other, update
// docs/scenario.schema.json's $defs.step.properties.impair.properties or
// internal/scenario/validator.go's impairKeys so they agree again.
func TestSchema_ImpairKeysMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")
	impairDef, ok := stepProps["impair"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.impair must exist")
	impairProps, ok := impairDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.impair.properties must exist")

	schemaKeys := make(scenario.ImpairKeySet, len(impairProps))
	for k := range impairProps {
		schemaKeys[k] = true
	}

	// The Go validator's own authoritative set, not a copy of it: a literal
	// here would have to be edited in lockstep with validator.go, which is
	// the exact drift this test exists to catch.
	assert.Equal(t, scenario.ImpairKeys(), schemaKeys,
		"docs/scenario.schema.json impair keys must match validator.go's ImpairKeys() exactly")
}

// TestSchema_MalformKeysMatchValidator verifies the malform: sub-key set
// published in docs/scenario.schema.json matches scenario.MalformKeys(),
// the Go validator's authoritative accepted-key set. If either side gains
// or loses a key without the other, update
// docs/scenario.schema.json's $defs.step.properties.malform.properties or
// internal/scenario/validator.go's malformKeys so they agree again.
func TestSchema_MalformKeysMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")
	malformDef, ok := stepProps["malform"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.malform must exist")
	malformProps, ok := malformDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.malform.properties must exist")

	schemaKeys := make(scenario.MalformKeySet, len(malformProps))
	for k := range malformProps {
		schemaKeys[k] = true
	}

	// The Go validator's own authoritative set, not a copy of it: a literal
	// here would have to be edited in lockstep with validator.go, which is
	// the exact drift this test exists to catch.
	assert.Equal(t, scenario.MalformKeys(), schemaKeys,
		"docs/scenario.schema.json malform keys must match validator.go's MalformKeys() exactly")
}

// TestSchema_RawKeysMatchValidator verifies the raw: sub-key set published
// in docs/scenario.schema.json matches scenario.RawKeys(), the Go
// validator's authoritative accepted-key set, mirroring
// TestSchema_MalformKeysMatchValidator's structure exactly.
func TestSchema_RawKeysMatchValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")
	stepProps, ok := stepDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties must exist")
	rawDef, ok := stepProps["raw"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.raw must exist")
	rawProps, ok := rawDef["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.properties.raw.properties must exist")

	schemaKeys := make(scenario.RawKeySet, len(rawProps))
	for k := range rawProps {
		schemaKeys[k] = true
	}

	assert.Equal(t, scenario.RawKeys(), schemaKeys,
		"docs/scenario.schema.json raw keys must match validator.go's RawKeys() exactly")
}

// TestSchema_RawStepShapeMatchesValidator guards against split-brain drift
// between the Go validator's validateRaw (internal/scenario/validator.go)
// and this schema's raw branch, nested inside the else/else chain wait's own
// branch nests into: both encode the same rule — action: raw requires a
// raw: block, modeled on TestSchema_WaitStepShapeMatchesValidator's approach.
func TestSchema_RawStepShapeMatchesValidator(t *testing.T) {
	schema := loadSchema(t)

	defs, ok := schema["$defs"].(map[string]interface{})
	require.True(t, ok, "$defs must be a map")
	stepDef, ok := defs["step"].(map[string]interface{})
	require.True(t, ok, "$defs.step must exist")

	elseBranch, ok := stepDef["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else must exist")
	elseElse, ok := elseBranch["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else must exist")

	rawIf, ok := elseElse["if"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.if must exist")
	rawIfProps, ok := rawIf["properties"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.if.properties must exist")
	actionConst, ok := rawIfProps["action"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.if.properties.action must exist")
	assert.Equal(t, "raw", actionConst["const"])

	rawThen, ok := elseElse["then"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.then must exist")
	rawThenRequired := toStringSlice(t, rawThen["required"])
	assert.ElementsMatch(t, []string{"id", "action", "protocol", "raw"}, rawThenRequired)

	rawElse, ok := elseElse["else"].(map[string]interface{})
	require.True(t, ok, "$defs.step.else.else.else must exist")
	rawElseRequired := toStringSlice(t, rawElse["required"])
	assert.NotContains(t, rawElseRequired, "raw")
}

// TestSchema_CheckEnumMatchesValidator keeps docs/scenario.schema.json's
// `check` enum and the Go validator's allowedChecks identical — the schema
// advertised "registered" for a long time before the state existed.
func TestSchema_CheckEnumMatchesValidator(t *testing.T) {
	schema := loadSchema(t)
	defs := schema["$defs"].(map[string]interface{})
	step := defs["step"].(map[string]interface{})
	props := step["properties"].(map[string]interface{})
	check := props["check"].(map[string]interface{})
	enumRaw, ok := check["enum"].([]interface{})
	require.True(t, ok, "$defs.step.properties.check must have an enum")

	schemaChecks := make(map[string]bool)
	for _, v := range enumRaw {
		schemaChecks[v.(string)] = true
	}
	assert.Equal(t, map[string]bool(scenario.AllowedChecks()), schemaChecks,
		"schema check enum and validator allowedChecks must match — update one or the other")
}
