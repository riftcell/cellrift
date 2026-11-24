package diameter

import (
	"embed"
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// AVPKind is the spec-declared data type of an AVP, from the Diameter base
// protocol's data-format vocabulary. Deliberately named AVPKind rather than
// reusing the existing DataType* constants in avp.go: those describe a
// runtime-inferred decode shape (4 bytes -> numeric, otherwise opaque),
// which is a different concept that happens to share some words.
type AVPKind string

const (
	AVPKindOctetString      AVPKind = "OctetString"
	AVPKindUTF8String       AVPKind = "UTF8String"
	AVPKindDiameterIdentity AVPKind = "DiameterIdentity"
	AVPKindDiameterURI      AVPKind = "DiameterURI"
	AVPKindAddress          AVPKind = "Address"
	AVPKindTime             AVPKind = "Time"
	AVPKindUnsigned32       AVPKind = "Unsigned32"
	AVPKindUnsigned64       AVPKind = "Unsigned64"
	AVPKindInteger32        AVPKind = "Integer32"
	AVPKindInteger64        AVPKind = "Integer64"
	AVPKindFloat32          AVPKind = "Float32"
	AVPKindFloat64          AVPKind = "Float64"
	AVPKindEnumerated       AVPKind = "Enumerated"
	AVPKindGrouped          AVPKind = "Grouped"
	AVPKindIPFilterRule     AVPKind = "IPFilterRule"
)

// GroupMemberRule is one member reference inside a Grouped AVP. Members are
// referenced by name, never inlined, so each AVP is defined exactly once.
// Max == 0 means unbounded.
type GroupMemberRule struct {
	Name     string
	Required bool
	Max      int
}

// AVPDefinition is one dictionary entry.
type AVPDefinition struct {
	Name        string
	Code        uint32
	VendorID    uint32
	Kind        AVPKind
	Mandatory   bool
	Vendor      bool
	Members     []GroupMemberRule
	Application string // "Base", "Cx", "S6a" — the source file's application key
}

// AVPDictionary maps an AVP name to its definition. It is the single source
// of truth for every AVP name/code/vendor/type fact in this package: the
// encode path resolves a scenario field name through it, the decode path
// resolves a received code through the index derived from it, and scenario
// validation checks field names against it.
type AVPDictionary map[string]AVPDefinition

// AVPCodeIndex maps an AVP code to its definition. Derived once from
// AVPDictionary at init, never hand-maintained as a parallel table.
type AVPCodeIndex map[uint32]AVPDefinition

// Dictionary is the loaded, merged dictionary. Read-only after init.
var Dictionary AVPDictionary

// dictionaryByCode is the derived reverse index.
var dictionaryByCode AVPCodeIndex

func init() {
	Dictionary, dictionaryByCode = mustLoadDictionary()
}

//go:embed dictionary/*.yaml
var dictionaryFS embed.FS

// dictFile is the top-level YAML shape of one application's dictionary file.
type dictFile struct {
	Application string      `yaml:"application"`
	Spec        string      `yaml:"spec"`
	AVPs        []dictEntry `yaml:"avps"`
}

// dictEntry is one AVP entry within a dictFile.
type dictEntry struct {
	Name     string       `yaml:"name"`
	Code     uint32       `yaml:"code"`
	VendorID uint32       `yaml:"vendor-id"`
	Kind     AVPKind      `yaml:"type"`
	Flags    dictFlags    `yaml:"flags"`
	Members  []dictMember `yaml:"members"`
}

// dictFlags is the flags sub-object of a dictEntry.
type dictFlags struct {
	Mandatory bool `yaml:"mandatory"`
	Vendor    bool `yaml:"vendor"`
}

// dictMember is one member reference within a Grouped dictEntry.
type dictMember struct {
	Name     string `yaml:"name"`
	Required bool   `yaml:"required"`
	Max      int    `yaml:"max"`
}

// LookupAVP returns the definition for an AVP name.
func LookupAVP(name string) (AVPDefinition, bool) {
	def, ok := Dictionary[name]
	return def, ok
}

// LookupAVPByCode returns the definition for an AVP code.
func LookupAVPByCode(code uint32) (AVPDefinition, bool) {
	def, ok := dictionaryByCode[code]
	return def, ok
}

// loadDictionary reads every embedded application file under dictionary/,
// unmarshals it, and folds it into a single AVPDictionary. Directory entries
// are sorted by filename before merging so load order — and therefore any
// duplicate-name error message — is deterministic.
func loadDictionary() (AVPDictionary, error) {
	entries, err := dictionaryFS.ReadDir("dictionary")
	if err != nil {
		return nil, fmt.Errorf("read dictionary dir: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	dict := AVPDictionary{}
	for _, name := range names {
		raw, err := dictionaryFS.ReadFile("dictionary/" + name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var f dictFile
		if err := yaml.Unmarshal(raw, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if err := mergeFile(dict, name, f); err != nil {
			return nil, err
		}
	}
	return dict, nil
}

// mergeFile copies each dictEntry in f into dict as an AVPDefinition,
// stamping Application from f.Application. Returns an error naming both the
// AVP and the file if the name is already present: a name defined in two
// files is the exact failure mode that occurs when an AVP reused across
// specifications is re-declared instead of referenced, and it must be loud.
func mergeFile(dict AVPDictionary, filename string, f dictFile) error {
	for _, e := range f.AVPs {
		if _, exists := dict[e.Name]; exists {
			return fmt.Errorf("diameter: AVP %q in %s is already defined in another dictionary file", e.Name, filename)
		}
		dict[e.Name] = AVPDefinition{
			Name:        e.Name,
			Code:        e.Code,
			VendorID:    e.VendorID,
			Kind:        e.Kind,
			Mandatory:   e.Flags.Mandatory,
			Vendor:      e.Flags.Vendor,
			Members:     toMemberRules(e.Members),
			Application: f.Application,
		}
	}
	return nil
}

// toMemberRules converts the YAML member list into GroupMemberRule values.
func toMemberRules(members []dictMember) []GroupMemberRule {
	if len(members) == 0 {
		return nil
	}
	rules := make([]GroupMemberRule, len(members))
	for i, m := range members {
		rules[i] = GroupMemberRule(m)
	}
	return rules
}

// buildCodeIndex derives the reverse map from dict. The reverse index is
// keyed by code alone because the two consumers that need it — turning a
// received AVP code back into a name for reporting, and deciding whether an
// outgoing AVP must carry the vendor bit — only ever have a bare code in
// hand, never a (code, vendor-id) pair. Uniqueness across the whole
// dictionary is therefore a hard precondition, not a nicety. If a genuine
// cross-vendor code collision ever appears, this is the place that must
// change, and it must change deliberately.
func buildCodeIndex(dict AVPDictionary) (AVPCodeIndex, error) {
	index := AVPCodeIndex{}
	for name, def := range dict {
		if existing, exists := index[def.Code]; exists {
			return nil, fmt.Errorf("diameter: AVP code %d claimed by both %q and %q", def.Code, existing.Name, name)
		}
		index[def.Code] = def
	}
	return index, nil
}

// mustLoadDictionary loads the dictionary and derives its code index,
// panicking with a "diameter: " prefixed message on any error. It runs once
// at package init, so a load or consistency failure surfaces immediately as
// a build/test failure rather than at first use.
func mustLoadDictionary() (AVPDictionary, AVPCodeIndex) {
	dict, err := loadDictionary()
	if err != nil {
		panic(fmt.Sprintf("diameter: %v", err))
	}
	index, err := buildCodeIndex(dict)
	if err != nil {
		panic(fmt.Sprintf("diameter: %v", err))
	}
	return dict, index
}
