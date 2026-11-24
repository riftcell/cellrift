package diameter

import (
	"fmt"
	"strings"
)

// DottedFieldGroups maps a Grouped AVP's field-name prefix to that AVP's
// own member fields, keyed by the remainder of the dotted path. It is how
// a scenario's nested `fields:` mapping arrives at the encoder: the parser
// flattens the nesting to dotted keys so every value stays a string, and
// this is the regrouping back into one AVP per prefix.
type DottedFieldGroups map[string]map[string]string

// GroupDottedFields splits fields into the undotted subset and the dotted
// subset regrouped by first path element. Keys reserved for body injection
// (the "__" prefix) are always routed to flat, never treated as a group.
func GroupDottedFields(fields map[string]string) (map[string]string, DottedFieldGroups) {
	flat := make(map[string]string, len(fields))
	grouped := DottedFieldGroups{}

	for key, val := range fields {
		prefix, member, ok := splitGroupedKey(key)
		if !ok {
			flat[key] = val
			continue
		}
		if grouped[prefix] == nil {
			grouped[prefix] = map[string]string{}
		}
		grouped[prefix][member] = val
	}

	return flat, grouped
}

// splitGroupedKey splits a dotted field key into its Grouped-AVP prefix and
// member remainder. A "__"-prefixed key is never a group regardless of
// whether it happens to contain a dot, so the reserved body-injection
// contract cannot be broken by a future key name.
func splitGroupedKey(key string) (prefix, member string, ok bool) {
	if strings.HasPrefix(key, "__") {
		return "", "", false
	}
	prefix, member, found := strings.Cut(key, ".")
	if !found {
		return "", "", false
	}
	return prefix, member, true
}

// newScopedGroupedAVP builds a Grouped AVP, consulting the AVP's own
// dictionary definition to decide whether it must carry the V bit and a
// Vendor-Id -- the same rule newScopedStringAVP applies to string AVPs, and
// for the same reason: a peer indexes AVPs by (code, vendor-id), so a 3GPP
// Grouped AVP sent plain-M lands in the base namespace where its code does
// not exist. Sourcing the decision from the dictionary rather than a second
// hand-maintained table is what keeps this from going stale.
func newScopedGroupedAVP(code uint32, subs []AVP) AVP {
	if def, ok := LookupAVPByCode(code); ok && def.Vendor {
		return AVP{
			Code:        code,
			Flags:       AVPFlagVendor | AVPFlagMandatory,
			VendorID:    def.VendorID,
			DataType:    DataTypeGrouped,
			GroupedAVPs: subs,
		}
	}
	return NewGroupedAVP(code, subs)
}

// encodeGroupedField builds one Grouped AVP from its already-flattened
// member fields, routing each member back through the same
// dictionary-lookup loop a top-level field takes. Because it re-enters
// buildAVPsFromFields, arbitrarily deep nesting works with no extra code --
// do NOT build any additional depth machinery. Everything the shipped
// scenarios author needs exactly one level; the one genuinely deep Grouped
// AVP in the dictionary, Subscription-Data, appears only on the answer side
// and is built in Go by the mock, never through a scenario field.
func encodeGroupedField(def AVPDefinition, members map[string]string) (AVP, error) {
	if err := validateGroupMembers(def, members); err != nil {
		return AVP{}, err
	}
	subs, err := buildAVPsFromFields(members)
	if err != nil {
		return AVP{}, fmt.Errorf("grouped AVP %q: %w", def.Name, err)
	}
	return newScopedGroupedAVP(def.Code, subs), nil
}

// validateGroupMembers checks every member field name against def.Members,
// the Grouped AVP's own member allowlist, erroring on the first unknown
// name found (map iteration order does not matter here: this is a
// send-time encode check, not a validate-time report that must be
// deterministic across every unknown member at once).
func validateGroupMembers(def AVPDefinition, members map[string]string) error {
	allowed := make(map[string]bool, len(def.Members))
	for _, m := range def.Members {
		allowed[m.Name] = true
	}
	for name := range members {
		if !allowed[name] {
			return fmt.Errorf("grouped AVP %q has no member %q", def.Name, name)
		}
	}
	return nil
}
