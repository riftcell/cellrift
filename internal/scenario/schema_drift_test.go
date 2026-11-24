package scenario_test

// schema_drift_test.go — the guard that did not exist.
//
// library_test.go compares the schema's ENUMS to the validator's allow-lists.
// Nothing compared the schema's protocolConfig PROPERTIES to the Go struct, which
// is why cert_file, key_file and ca_file agree in both places only because
// someone remembered. Both drift directions are real and they fail differently:
// a field in the struct but not the schema validates in Go while every editor
// flags the scenario; a property in the schema but not the struct is REJECTED at
// parse time by KnownFields(true), with an error that names the key but not the
// cause. Both are checked here.
//
// The documentation assertions are in the same file on purpose. The schema and
// cli-reference.md are two renderings of one vocabulary, and a phase that adds a
// key to one and not the other is the ordinary way this drifts.

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// protocolConfigFieldCount is the number of yaml-tagged fields on
// scenario.ProtocolConfig: transport, local_port, origin_host, origin_realm,
// cert_file, key_file, ca_file, insecure_skip_verify, username, password,
// credentials_file. Change it deliberately when a field is added.
const protocolConfigFieldCount = 11

// credentialKeyNames are the three SIP-only digest credential keys.
var credentialKeyNames = []string{"username", "password", "credentials_file"}

// propertySet is a set of property names: either a $def's schema properties or
// the yaml tags of ProtocolConfig. It exists so the two sides of each drift
// comparison have the same type and the diff helper can be shared.
type propertySet map[string]bool

func (p propertySet) sorted() []string {
	out := make([]string, 0, len(p))
	for k := range p {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// protocolConfigTags returns the yaml tags of scenario.ProtocolConfig.
func protocolConfigTags(t *testing.T) propertySet {
	t.Helper()
	typ := reflect.TypeOf(scenario.ProtocolConfig{})
	tags := propertySet{}
	for i := 0; i < typ.NumField(); i++ {
		name := strings.TrimSpace(strings.Split(typ.Field(i).Tag.Get("yaml"), ",")[0])
		if name == "" || name == "-" {
			continue
		}
		tags[name] = true
	}
	// A vacuous guard is worse than no guard: it reports OK forever. Pin the
	// field count so an extraction bug fails loudly instead of checking nothing.
	require.Len(t, tags, protocolConfigFieldCount,
		"yaml-tag extraction found %v; if a field was added or removed, update protocolConfigFieldCount", tags.sorted())
	return tags
}

// schemaDef returns one $defs entry of the schema as a generic map.
func schemaDef(t *testing.T, name string) map[string]interface{} {
	t.Helper()
	defs, ok := loadSchema(t)["$defs"].(map[string]interface{})
	require.True(t, ok, "schema must have $defs")
	def, ok := defs[name].(map[string]interface{})
	require.True(t, ok, "schema $defs must contain %s", name)
	return def
}

func schemaProperties(t *testing.T, def string) propertySet {
	t.Helper()
	props, ok := schemaDef(t, def)["properties"].(map[string]interface{})
	require.True(t, ok, "%s must have properties", def)
	set := propertySet{}
	for k := range props {
		set[k] = true
	}
	require.NotEmpty(t, set, "%s has no properties; the drift checks would pass vacuously", def)
	return set
}

var protocolConfigDefs = []string{"protocolConfig", "diameterProtocolConfig"}

// Coverage, forward: a Go field that no schema def mentions. This says nothing
// about WHERE a property lives; TestSchemaDrift_CredentialKeysAreSipOnly does.
func TestSchemaDrift_EveryProtocolConfigYamlTagIsASchemaProperty(t *testing.T) {
	union := propertySet{}
	for _, def := range protocolConfigDefs {
		for k := range schemaProperties(t, def) {
			union[k] = true
		}
	}
	for tag := range protocolConfigTags(t) {
		assert.True(t, union[tag],
			"yaml tag %q on scenario.ProtocolConfig is in no schema $def — add it to docs/scenario.schema.json; editors will flag a scenario that uses it", tag)
	}
}

// Coverage, reverse: a schema property with no Go field.
func TestSchemaDrift_EverySchemaPropertyIsAProtocolConfigYamlTag(t *testing.T) {
	tags := protocolConfigTags(t)
	for _, def := range protocolConfigDefs {
		for prop := range schemaProperties(t, def) {
			assert.True(t, tags[prop],
				"schema property %q under %s has no yaml tag on scenario.ProtocolConfig — add the Go field or remove the schema property; a property with no field is rejected at parse time by KnownFields(true)", prop, def)
		}
	}
}

// Placement: a separate assertion from coverage, because "in at least one def"
// is satisfied by a credential that appears only under Diameter.
func TestSchemaDrift_CredentialKeysAreSipOnly(t *testing.T) {
	sip := schemaProperties(t, "protocolConfig")
	diameter := schemaProperties(t, "diameterProtocolConfig")
	for _, key := range credentialKeyNames {
		assert.True(t, sip[key], "%s must be a property of the SIP protocolConfig", key)
		assert.False(t, diameter[key], "%s must NOT be a property of diameterProtocolConfig", key)
	}
}

// The schema and the Go validator express one rule twice. Prove they agree: each
// key the schema keeps out of diameter is one the validator rejects there.
func TestSchemaDrift_SchemaAndValidatorAgreeCredentialsAreSipOnly(t *testing.T) {
	diameter := schemaProperties(t, "diameterProtocolConfig")
	for _, key := range credentialKeyNames {
		require.False(t, diameter[key], "%s is in the diameter schema def", key)
		errs := validateDiameterWith(t, key)
		want := "protocols.diameter." + key
		found := false
		for _, e := range errs {
			found = found || e.Field == want
		}
		assert.True(t, found, "schema excludes %s from diameter but Validate does not reject it (errors: %v)", key, errs)
	}
}

func validateDiameterWith(t *testing.T, key string) []scenario.ValidationError {
	t.Helper()
	yml := fmt.Sprintf("name: x\nprotocols:\n  diameter:\n    %s: v\nsteps:\n  - id: s\n    protocol: diameter\n    action: wait\n    duration_ms: 1\n", key)
	s, err := scenario.Parse([]byte(yml), "drift.yaml")
	require.NoError(t, err)
	return scenario.Validate(s)
}

func TestSchemaDrift_PasswordDescriptionStatesThePrecedence(t *testing.T) {
	props := schemaDef(t, "protocolConfig")["properties"].(map[string]interface{})
	pw := props["password"].(map[string]interface{})
	desc, ok := pw["description"].(string)
	require.True(t, ok, "password must have a description")
	for _, want := range []string{"credentials_file", "CELLRIFT_SIP_PASSWORD", "CELLRIFT_VAR_", "recedence", "does NOT fall through"} {
		assert.Contains(t, desc, want, "the password description must state the precedence rules")
	}
}

func TestSchemaDrift_BothProtocolConfigsForbidAdditionalProperties(t *testing.T) {
	for _, def := range protocolConfigDefs {
		assert.Equal(t, false, schemaDef(t, def)["additionalProperties"],
			"%s must set additionalProperties:false; every other drift check assumes its property list is exhaustive", def)
	}
}

func readCliReference(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot(), "docs", "cli-reference.md"))
	require.NoError(t, err, "docs/cli-reference.md must exist")
	return string(data)
}

// docsRequiredStrings are the vocabulary cli-reference.md must mention. The
// response.auth.* list is a third copy of the implementation's key set: this
// test proves the docs mention each key, not that the adapter emits it.
var docsRequiredStrings = []string{
	"username", "password", "credentials_file",
	"CELLRIFT_SIP_PASSWORD", "CELLRIFT_VAR_",
	"response.auth.challenged", "response.auth.retried", "response.auth.challenge_header",
	"response.auth.challenge_status", "response.auth.realm", "response.auth.nonce",
	"response.auth.qop", "response.auth.algorithm", "response.auth.opaque",
	"Proxy-Authenticate", "Proxy-Authorization",
	"removed from terminal, JSON and JUnit output under both `--no-mask` states, including when the peer reflects them",
	"never falls through",
	// The Diameter identity defaults, copied from diameter.DefaultOriginHost and
	// DefaultOriginRealm so a change to either shows up here.
	"`cellrift.diameter.invalid`", "`diameter.invalid`",
	"not currently used to bind",
}

// layerAnchors are the three credential layers' list-item openers, in the order
// the documentation must present them (most explicit first).
var layerAnchors = []string{
	"1. `protocols.sip.password`",
	"2. `protocols.sip.credentials_file`",
	"3. `CELLRIFT_SIP_PASSWORD`: the ambient fallback",
}

func TestDocsDrift_CliReferenceDocumentsTheCredentialVocabulary(t *testing.T) {
	doc := readCliReference(t)
	for _, want := range docsRequiredStrings {
		assert.Contains(t, doc, want, "docs/cli-reference.md must document this")
	}
	assert.NotContains(t, doc, ".planning", "a tracked doc must not cite the gitignored planning directory")
}

// A Contains-only check would pass on a scrambled list, so compare positions.
// Each anchor must also occur exactly once, so it cannot match incidentally.
func TestDocsDrift_CredentialLayersAppearInPrecedenceOrder(t *testing.T) {
	doc := readCliReference(t)
	prev := -1
	for _, anchor := range layerAnchors {
		require.Equal(t, 1, strings.Count(doc, anchor), "anchor %q must occur exactly once", anchor)
		idx := strings.Index(doc, anchor)
		require.Greater(t, idx, prev, "layer %q is out of order: layers must read 1, 2, 3", anchor)
		prev = idx
	}
}

// DOCUMENTATION-SYNC TRIPWIRE, not a prohibition. local_port is accepted,
// validated and copied into the step config by runner.go, but no adapter reads
// it, so the schema and cli-reference.md say it is not used to bind. The day
// someone wires it up this test fails; that is the cue to correct those
// descriptions in the same change and then update this test. Implementing the
// feature is welcome.
func TestDocsSync_LocalPortIsNotConsumedByAnyAdapterSoItsDocsSayItIsUnused(t *testing.T) {
	allowed := map[string]bool{
		filepath.Join("internal", "engine", "runner.go"):  true, // forwards it into the step config
		filepath.Join("internal", "scenario", "types.go"): true, // the yaml tag
	}
	var consumers []string
	for _, dir := range []string{"internal", "cmd"} {
		root := filepath.Join(moduleRoot(), dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(moduleRoot(), path)
			data, rerr := os.ReadFile(path)
			if rerr == nil && !allowed[rel] && strings.Contains(string(data), `"local_port"`) {
				consumers = append(consumers, rel)
			}
			return rerr
		})
		require.NoError(t, err)
	}
	assert.Empty(t, consumers,
		"local_port is now read by %v: correct the local_port descriptions in docs/scenario.schema.json and docs/cli-reference.md (they say it is not used to bind), then update this tripwire", consumers)
}
