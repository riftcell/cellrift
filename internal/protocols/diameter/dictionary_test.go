package diameter

import (
	"sort"
	"testing"
)

// wantS6aCount is the count of S6a-application dictionary entries: the 175
// spec-verified TS 29.272 Table 7.3.1/1 entries, transcribed in full across
// two chunks and confirmed by a code-level boundary sweep against the raw
// spec table text, plus RAT-Type (1032) -- a TS 29.212 AVP reused by TS
// 29.272 §7.3.13 and declared in this file outside the table's own
// transcribed range.
const wantS6aCount = 176

// wantCxCount is the final, spec-verified count of TS 29.229 Table 6.3.1 AVP
// entries in the 600-655 range: 54 (codes 627 and 636 are unused in the
// table itself). Codes 656-658 are real Rel-13 assignments deliberately kept
// outside this file's stated range. Not expected to change without a
// deliberate dictionary update.
const wantCxCount = 54

// countByApplication returns the number of Dictionary entries whose
// Application field equals app.
func countByApplication(app string) int {
	count := 0
	for _, def := range Dictionary {
		if def.Application == app {
			count++
		}
	}
	return count
}

func TestAVPDictionary_Loads(t *testing.T) {
	if len(Dictionary) == 0 {
		t.Fatal("Dictionary is empty after package init")
	}
	if _, ok := Dictionary["Session-Id"]; !ok {
		t.Error("Dictionary missing \"Session-Id\"")
	}
}

func TestAVPDictionary_DefinitionFieldsPopulated(t *testing.T) {
	sessionID, ok := Dictionary["Session-Id"]
	if !ok {
		t.Fatal("Dictionary missing \"Session-Id\"")
	}
	if sessionID.Code != 263 {
		t.Errorf("Session-Id: Code = %d, want 263", sessionID.Code)
	}
	if sessionID.VendorID != 0 {
		t.Errorf("Session-Id: VendorID = %d, want 0", sessionID.VendorID)
	}
	if sessionID.Kind != AVPKindUTF8String {
		t.Errorf("Session-Id: Kind = %q, want %q", sessionID.Kind, AVPKindUTF8String)
	}
	if sessionID.Vendor {
		t.Error("Session-Id: Vendor = true, want false")
	}
	if sessionID.Application != "Base" {
		t.Errorf("Session-Id: Application = %q, want \"Base\"", sessionID.Application)
	}

	publicIdentity, ok := Dictionary["Public-Identity"]
	if !ok {
		t.Fatal("Dictionary missing \"Public-Identity\"")
	}
	if publicIdentity.Code != 601 {
		t.Errorf("Public-Identity: Code = %d, want 601", publicIdentity.Code)
	}
	if publicIdentity.VendorID != 10415 {
		t.Errorf("Public-Identity: VendorID = %d, want 10415", publicIdentity.VendorID)
	}
	if publicIdentity.Kind != AVPKindUTF8String {
		t.Errorf("Public-Identity: Kind = %q, want %q", publicIdentity.Kind, AVPKindUTF8String)
	}
	if !publicIdentity.Vendor {
		t.Error("Public-Identity: Vendor = false, want true")
	}
	if publicIdentity.Application != "Cx" {
		t.Errorf("Public-Identity: Application = %q, want \"Cx\"", publicIdentity.Application)
	}
}

func TestAVPDictionary_NoDuplicateCodes(t *testing.T) {
	seen := map[uint32]string{}
	for name, def := range Dictionary {
		if other, exists := seen[def.Code]; exists {
			t.Errorf("code %d claimed by both %q and %q", def.Code, other, name)
			continue
		}
		seen[def.Code] = name
	}
}

func TestAVPDictionary_BaseCount(t *testing.T) {
	// 25 after Service-Context-Id (461) and Multiple-Services-Credit-Control
	// (456) joined Experimental-Result (297) and Experimental-Result-Code
	// (298). Each was added because a real peer named it: the Cx UAA reports
	// its verdict through Experimental-Result and carries no base
	// Result-Code, and a SigScale OCS answers 5005 for a CCR with no
	// Service-Context-Id and 5001 for a Rating-Group sent outside
	// Multiple-Services-Credit-Control.
	if got := countByApplication("Base"); got != 25 {
		t.Errorf("Base entry count = %d, want 25", got)
	}
}

func TestAVPDictionary_CxCount(t *testing.T) {
	if got := countByApplication("Cx"); got != wantCxCount {
		t.Errorf("Cx entry count = %d, want %d", got, wantCxCount)
	}
}

func TestAVPDictionary_S6aCount(t *testing.T) {
	if got := countByApplication("S6a"); got != wantS6aCount {
		t.Errorf("S6a entry count = %d, want %d", got, wantS6aCount)
	}
}

func TestAVPDictionary_CodeIndexMatchesDictionary(t *testing.T) {
	for name, def := range Dictionary {
		byCode, ok := LookupAVPByCode(def.Code)
		if !ok {
			t.Errorf("LookupAVPByCode(%d) for %q: not found", def.Code, name)
			continue
		}
		if byCode.Name != name {
			t.Errorf("LookupAVPByCode(%d) = %q, want %q", def.Code, byCode.Name, name)
		}
	}
}

// sortedDictionaryKeys returns Dictionary's keys in sorted order. Go
// randomises map iteration order, and a gate test whose failure output
// reorders between runs is substantially harder to diff against a spec
// table, so every check below iterates this slice rather than the map
// directly.
func sortedDictionaryKeys(dict AVPDictionary) []string {
	keys := make([]string, 0, len(dict))
	for name := range dict {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

// checkMembersResolve verifies every name listed in a Grouped AVP's Members
// resolves to a real dictionary entry.
func checkMembersResolve(t *testing.T, dict AVPDictionary, keys []string) {
	t.Helper()
	for _, name := range keys {
		def := dict[name]
		for _, member := range def.Members {
			if _, ok := dict[member.Name]; !ok {
				t.Errorf("%q references member %q, which does not resolve to a dictionary entry", name, member.Name)
			}
		}
	}
}

// checkCodesUnique verifies no two entries share a Code value.
func checkCodesUnique(t *testing.T, dict AVPDictionary, keys []string) {
	t.Helper()
	seen := map[uint32]string{}
	for _, name := range keys {
		code := dict[name].Code
		if other, exists := seen[code]; exists {
			t.Errorf("code %d claimed by both %q and %q", code, other, name)
			continue
		}
		seen[code] = name
	}
}

// checkKeysAndKindsValid verifies every entry's map key equals its Name
// field and every entry has a non-empty Kind.
func checkKeysAndKindsValid(t *testing.T, dict AVPDictionary, keys []string) {
	t.Helper()
	for _, name := range keys {
		def := dict[name]
		if def.Name != name {
			t.Errorf("dictionary key %q does not match entry Name %q", name, def.Name)
		}
		if def.Kind == "" {
			t.Errorf("%q has an empty Kind", name)
		}
	}
}

// checkVendorFlagsAgree verifies Vendor and VendorID never disagree: the
// encode path decides the vendor bit from one and writes the other into the
// AVP header, so a mismatch would silently corrupt the wire format.
func checkVendorFlagsAgree(t *testing.T, dict AVPDictionary, keys []string) {
	t.Helper()
	for _, name := range keys {
		def := dict[name]
		if def.Vendor && def.VendorID == 0 {
			t.Errorf("%q has Vendor=true but VendorID=0", name)
		}
		if !def.Vendor && def.VendorID != 0 {
			t.Errorf("%q has VendorID=%d but Vendor=false", name, def.VendorID)
		}
	}
}

// TestAVPDictionary_SelfConsistency is the dictionary's final gate: it
// enforces member resolution, code uniqueness, key/name agreement and
// vendor-flag coherence across the whole loaded dictionary in one place.
//
// A transcription task that lands a partial chunk of a spec table may
// legitimately reference a member a later chunk has not yet added — running
// a strict dangling-reference check against a knowingly-incomplete file
// would produce failures that mean nothing. This gate therefore belongs at
// the end of a transcription sequence, not wired into per-chunk
// verification along the way.
func TestAVPDictionary_SelfConsistency(t *testing.T) {
	keys := sortedDictionaryKeys(Dictionary)
	checkMembersResolve(t, Dictionary, keys)
	checkCodesUnique(t, Dictionary, keys)
	checkKeysAndKindsValid(t, Dictionary, keys)
	checkVendorFlagsAgree(t, Dictionary, keys)
}
