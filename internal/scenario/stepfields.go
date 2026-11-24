package scenario

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// StepFields is a step's protocol field map: header name -> value for SIP,
// AVP name -> value for Diameter. A Diameter Grouped AVP is authored as a
// nested YAML mapping and stored here flattened to dotted-path keys
// ("SIP-Auth-Data-Item.SIP-Item-Number"), so every value stays a string and
// every existing consumer -- variable resolution, SIP's header builder,
// the {{ var }} reference scan -- keeps working on a flat map. The Diameter
// encoder regroups the dotted keys back into Grouped AVPs at send time.
type StepFields map[string]string

// UnmarshalYAML decodes a fields: mapping, flattening any nested mapping
// into dotted-path keys so the stored value stays a plain
// map[string]string underneath.
func (f *StepFields) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("fields: expected a mapping, got %s", nodeKindName(node.Kind))
	}
	out := StepFields{}
	if err := flattenMapping(node, "", out); err != nil {
		return err
	}
	*f = out
	return nil
}

// flattenMapping walks one YAML mapping node's key/value pairs, writing
// scalar values into out under prefix-joined dotted keys and recursing into
// nested mappings for Grouped-AVP authoring. A sequence or alias value is an
// error naming the offending field: a field value must be either a scalar
// or another mapping, nothing else parses as an intelligible field.
func flattenMapping(node *yaml.Node, prefix string, out StepFields) error {
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		key := k.Value
		if prefix != "" {
			key = prefix + "." + k.Value
		}

		switch v.Kind {
		case yaml.ScalarNode:
			if _, exists := out[key]; exists {
				return fmt.Errorf("fields: duplicate key %q (one nested, one dotted)", key)
			}
			out[key] = v.Value
		case yaml.MappingNode:
			if err := flattenMapping(v, key, out); err != nil {
				return err
			}
		default:
			return fmt.Errorf("fields: %q must be a scalar or nested mapping, got %s", key, nodeKindName(v.Kind))
		}
	}
	return nil
}

// nodeKindName names a yaml.Kind for error messages.
func nodeKindName(k yaml.Kind) string {
	switch k {
	case yaml.SequenceNode:
		return "a sequence"
	case yaml.AliasNode:
		return "an alias"
	case yaml.DocumentNode:
		return "a document"
	default:
		return "an unsupported node"
	}
}
