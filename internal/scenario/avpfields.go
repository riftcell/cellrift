package scenario

import (
	"fmt"
	"sort"
	"strings"

	"github.com/riftcell/cellrift/internal/protocols/diameter"
)

// validateAVPFields checks a Diameter step's field names against the
// embedded AVP dictionary. A field name that is not an AVP is caught here,
// before any traffic is sent, rather than surfacing as a request that
// mysteriously omits it. Dotted-path keys (a nested Grouped-AVP mapping,
// flattened by scenario.StepFields) are regrouped by the same
// diameter.GroupDottedFields helper the encoder uses, so the two never
// drift into two different opinions about what a dotted key means.
//
// This does not recurse into sub-steps (step.Steps); Validate does not
// recurse for any of its other per-step checks, and making this one the
// exception would be a surprising inconsistency.
func validateAVPFields(stepID string, fields map[string]string) []ValidationError {
	flat, grouped := diameter.GroupDottedFields(fields)

	var errs []ValidationError
	errs = append(errs, validateFlatAVPNames(stepID, flat)...)
	errs = append(errs, validateGroupedAVPFields(stepID, grouped)...)
	return errs
}

// validateFlatAVPNames checks the undotted subset of a step's fields
// against the embedded AVP dictionary -- the check validateAVPFields always
// ran, now scoped to just the names GroupDottedFields left flat.
func validateFlatAVPNames(stepID string, fields map[string]string) []ValidationError {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []ValidationError
	for _, name := range names {
		// The executor injects body content under reserved __body_* keys
		// (internal/engine/executor.go); those are never AVP names and must
		// never be reported as typos.
		if strings.HasPrefix(name, "__") {
			continue
		}
		if _, ok := diameter.LookupAVP(name); ok {
			continue
		}
		msg := fmt.Sprintf("unknown Diameter AVP %q", name)
		if suggestion := closestAVPName(name); suggestion != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", suggestion)
		}
		errs = append(errs, ValidationError{
			StepID:  stepID,
			Field:   "fields." + name,
			Message: msg,
		})
	}
	return errs
}

// validateGroupedAVPFields checks each dotted-field group's prefix and
// member names, one prefix at a time, in sorted order for deterministic
// error output.
func validateGroupedAVPFields(stepID string, grouped diameter.DottedFieldGroups) []ValidationError {
	prefixes := make([]string, 0, len(grouped))
	for prefix := range grouped {
		prefixes = append(prefixes, prefix)
	}
	sort.Strings(prefixes)

	var errs []ValidationError
	for _, prefix := range prefixes {
		errs = append(errs, validateGroupedPrefix(stepID, prefix, grouped[prefix])...)
	}
	return errs
}

// validateGroupedPrefix validates one Grouped-AVP prefix: an unknown prefix
// or a prefix naming an AVP that isn't Grouped is reported once per member
// key (so every dotted field under it is individually visible in the
// report); a valid Grouped prefix has each of its member names checked
// against that AVP's own Members allowlist.
func validateGroupedPrefix(stepID, prefix string, members map[string]string) []ValidationError {
	def, ok := diameter.LookupAVP(prefix)
	if !ok {
		msg := fmt.Sprintf("unknown Diameter AVP %q", prefix)
		if suggestion := closestAVPName(prefix); suggestion != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", suggestion)
		}
		return groupFieldErrors(stepID, prefix, members, msg)
	}
	if def.Kind != diameter.AVPKindGrouped {
		msg := fmt.Sprintf("AVP %q is not a Grouped AVP and cannot carry members", prefix)
		return groupFieldErrors(stepID, prefix, members, msg)
	}
	return validateGroupMemberNames(stepID, prefix, def.Members, members)
}

// groupFieldErrors returns one ValidationError per member key under prefix,
// all carrying the same message. Used when the prefix itself is invalid
// (unknown or not Grouped), so every dotted field under it is reported
// individually rather than the whole group being silently skipped.
func groupFieldErrors(stepID, prefix string, members map[string]string, msg string) []ValidationError {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)

	errs := make([]ValidationError, 0, len(names))
	for _, name := range names {
		errs = append(errs, ValidationError{
			StepID:  stepID,
			Field:   fmt.Sprintf("fields.%s.%s", prefix, name),
			Message: msg,
		})
	}
	return errs
}

// validateGroupMemberNames checks each member name in a Grouped-AVP group
// against that AVP's own Members allowlist, suggesting the closest member
// name (not a dictionary-wide name) on a miss.
func validateGroupMemberNames(stepID, prefix string, allowed []diameter.GroupMemberRule, members map[string]string) []ValidationError {
	names := make([]string, 0, len(members))
	for name := range members {
		names = append(names, name)
	}
	sort.Strings(names)

	var errs []ValidationError
	for _, name := range names {
		if memberAllowed(allowed, name) {
			continue
		}
		msg := fmt.Sprintf("Grouped AVP %q has no member %q", prefix, name)
		if suggestion := closestMemberName(name, allowed); suggestion != "" {
			msg += fmt.Sprintf(" (did you mean %q?)", suggestion)
		}
		errs = append(errs, ValidationError{
			StepID:  stepID,
			Field:   fmt.Sprintf("fields.%s.%s", prefix, name),
			Message: msg,
		})
	}
	return errs
}

// memberAllowed reports whether name appears in a Grouped AVP's Members
// allowlist.
func memberAllowed(allowed []diameter.GroupMemberRule, name string) bool {
	for _, m := range allowed {
		if m.Name == name {
			return true
		}
	}
	return false
}

// editDistance returns the Levenshtein distance between a and b, using a
// single rolling row rather than a full matrix.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

// min3 returns the smallest of three ints.
func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// closestAVPName returns the dictionary AVP name within edit distance 2 of
// the given name, or "" when nothing is close enough. Candidates are
// compared case-insensitively and ties are broken by choosing the
// lexicographically smallest name, so the same typo always yields the same
// suggestion — Go randomises map iteration order, and a non-deterministic
// error message is both confusing to a user and impossible to assert on.
func closestAVPName(name string) string {
	const maxDistance = 2
	lower := strings.ToLower(name)

	best := ""
	bestDist := maxDistance + 1
	for candidate := range diameter.Dictionary {
		dist := editDistance(lower, strings.ToLower(candidate))
		if dist > maxDistance {
			continue
		}
		if dist < bestDist || (dist == bestDist && candidate < best) {
			best = candidate
			bestDist = dist
		}
	}
	return best
}

// closestMemberName returns the member name within edit distance 2 of name,
// drawn from a single Grouped AVP's own Members list rather than the whole
// dictionary -- reusing editDistance/min3 rather than a second Levenshtein
// implementation. Ties are broken lexicographically, exactly as
// closestAVPName above.
func closestMemberName(name string, members []diameter.GroupMemberRule) string {
	const maxDistance = 2
	lower := strings.ToLower(name)

	best := ""
	bestDist := maxDistance + 1
	for _, m := range members {
		dist := editDistance(lower, strings.ToLower(m.Name))
		if dist > maxDistance {
			continue
		}
		if dist < bestDist || (dist == bestDist && m.Name < best) {
			best = m.Name
			bestDist = dist
		}
	}
	return best
}
