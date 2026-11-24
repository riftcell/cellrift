package scenario_test

// dispatch_audit_test.go — mechanical proof that every action the validator
// accepts is actually executed by something.
//
// This repository has shipped the defect this file guards against: an action
// (`wait`) was accepted by both the Go validator and the published JSON
// schema, but no runner special-case and no protocol adapter's Execute
// switch handled it, so a scenario using it failed at runtime with
// "no adapter registered for protocol \"\"" instead of at validation time.
// A second action (`receive`) was accepted the same way and dispatched
// nowhere at all, since the repository's initial commit. This test turns
// "validated but not dispatched" into a build failure instead of a runtime
// surprise, so neither class of defect can be reintroduced silently.

import (
	"os"
	"strconv"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dispatchSite records where one action is actually executed. An action
// counts as dispatched if ANY site handles it — not all of them.
// assert_state is dispatched by SIP alone and that is correct by design:
// its check values are SIP dialog states with no Diameter analog. A
// predicate demanding every adapter would fail on it wrongly.
type dispatchSite struct {
	file    string // path relative to this package, re-read to keep the claim honest
	actions []string
}

var dispatchSites = []dispatchSite{
	{file: "../engine/runner.go", actions: []string{"parallel", "wait", "reorder"}},
	{file: "../protocols/sip/adapter.go", actions: []string{"send", "assert_state", "raw"}},
	{file: "../protocols/diameter/adapter.go", actions: []string{"send", "raw"}},
}

// dispatchedActions returns the union of every site's claimed actions.
func dispatchedActions() map[string]bool {
	out := make(map[string]bool)
	for _, site := range dispatchSites {
		for _, action := range site.actions {
			out[action] = true
		}
	}
	return out
}

// TestAllowedActions_AllHaveARealDispatchPath proves the validator's
// allow-list and the dispatch table agree in both directions: nothing
// accepted goes nowhere, and nothing dispatched is unreachable through the
// validator.
func TestAllowedActions_AllHaveARealDispatchPath(t *testing.T) {
	dispatched := dispatchedActions()

	for action := range scenario.AllowedActions() {
		assert.True(t, dispatched[action],
			"action %q is accepted by the validator but no runner special-case or adapter Execute switch dispatches it — a scenario using it fails at runtime with 'unsupported action', the same way 'wait' silently did",
			action)
	}

	allowed := scenario.AllowedActions()
	for action := range dispatched {
		assert.True(t, allowed[action],
			"action %q is dispatched by a runner special-case or adapter Execute switch but the validator rejects it — a scenario can never reach code that runs", action)
	}
}

// TestDispatchSites_MatchSource is a tripwire, not a parser: it proves the
// quoted action string is still present in the file that claims to
// dispatch it, which is enough to catch the table rotting into a comforting
// lie after a refactor moves or deletes a case. It cannot prove the action
// is reachable or wired correctly — TestAllowedActions_AllHaveARealDispatchPath
// combined with each package's own adapter/runner tests do that.
func TestDispatchSites_MatchSource(t *testing.T) {
	for _, site := range dispatchSites {
		site := site
		t.Run(site.file, func(t *testing.T) {
			data, err := os.ReadFile(site.file)
			require.NoError(t, err, "dispatch_audit_test.go's dispatchSites table names a file that no longer exists")
			contents := string(data)
			for _, action := range site.actions {
				assert.Contains(t, contents, strconv.Quote(action),
					"dispatchSites claims %q dispatches action %q, but the quoted string is no longer in the file",
					site.file, action)
			}
		})
	}
}

// TestDispatchAudit_IsOROfSites_NotAND pins the audit's pass condition
// explicitly: an action is dispatched if AT LEAST ONE site handles it, never
// "every site handles it". assert_state is the concrete proof — its `check`
// enum (registered/established/terminated) is SIP dialog states with no
// Diameter analog, so it is deliberately absent from the Diameter adapter's
// Execute switch. A future rewrite of dispatchedActions to an intersection
// (AND) across sites must fail this test loudly, since it would wrongly
// reject assert_state as undispatched.
func TestDispatchAudit_IsOROfSites_NotAND(t *testing.T) {
	var sipActions, diameterActions map[string]bool
	for _, site := range dispatchSites {
		switch site.file {
		case "../protocols/sip/adapter.go":
			sipActions = toSet(site.actions)
		case "../protocols/diameter/adapter.go":
			diameterActions = toSet(site.actions)
		}
	}
	require.NotNil(t, sipActions, "sip adapter dispatch site missing from dispatchSites")
	require.NotNil(t, diameterActions, "diameter adapter dispatch site missing from dispatchSites")

	assert.True(t, sipActions["assert_state"], "assert_state must be dispatched by SIP")
	assert.False(t, diameterActions["assert_state"], "assert_state must NOT be dispatched by Diameter — it has no dialog-state analog")

	dispatched := dispatchedActions()
	assert.True(t, dispatched["assert_state"],
		"assert_state must count as dispatched (OR across sites) even though only one of the three sites handles it")
}

// TestDispatchAudit_RawRequiresBothAdapters pins what the generic
// OR-across-sites audit deliberately cannot: action: raw must be dispatched
// by BOTH protocol adapters, not merely by one. Its whole purpose is writing
// one protocol's bytes onto the other protocol's connection, so a version
// shipped on only one side is half a feature that the OR-based
// TestAllowedActions_AllHaveARealDispatchPath would report as complete. This
// is the inverse of assert_state's pin in TestDispatchAudit_IsOROfSites_NotAND:
// there, exactly one adapter is correct; here, both are mandatory.
func TestDispatchAudit_RawRequiresBothAdapters(t *testing.T) {
	var sipActions, diameterActions map[string]bool
	for _, site := range dispatchSites {
		switch site.file {
		case "../protocols/sip/adapter.go":
			sipActions = toSet(site.actions)
		case "../protocols/diameter/adapter.go":
			diameterActions = toSet(site.actions)
		}
	}
	require.NotNil(t, sipActions, "sip adapter dispatch site missing from dispatchSites")
	require.NotNil(t, diameterActions, "diameter adapter dispatch site missing from dispatchSites")

	assert.True(t, sipActions["raw"], "action: raw must be dispatched by the SIP adapter")
	assert.True(t, diameterActions["raw"], "action: raw must be dispatched by the Diameter adapter")
}

// toSet converts a []string into a lookup set.
func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

// TestAllowedActions_ReturnsDefensiveCopy proves AllowedActions() returns a
// copy: mutating what one call returns must not change what a later call
// returns, or a test elsewhere that ranges over the result and deletes
// entries could corrupt the validator's own live allow-list.
func TestAllowedActions_ReturnsDefensiveCopy(t *testing.T) {
	first := scenario.AllowedActions()
	require.NotEmpty(t, first)

	var removed string
	for action := range first {
		removed = action
		break
	}
	delete(first, removed)

	second := scenario.AllowedActions()
	assert.True(t, second[removed],
		"deleting an entry from one AllowedActions() call's result affected a later call — the accessor is not returning a defensive copy")
}
