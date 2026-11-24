package scenario

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// ParseError wraps a YAML parse error with file context.
type ParseError struct {
	File    string
	Line    int
	Column  int
	Message string
}

func (e *ParseError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s:%d:%d: %s", e.File, e.Line, e.Column, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.File, e.Message)
}

// ParseFile reads a YAML scenario file and returns a parsed Scenario.
func ParseFile(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &ParseError{
			File:    path,
			Message: err.Error(),
		}
	}

	return Parse(data, path)
}

// Parse decodes YAML bytes into a Scenario.
//
// Decoding is strict: a key that matches no field of the target struct is a
// parse error, not silently dropped. Without this, `assertions:` (a typo of
// `assert:`) or `targett:` validated clean and the step ran with zero
// assertions — a scenario author could not tell a passing test from one
// whose checks had quietly vanished. docs/scenario.schema.json already
// declares additionalProperties: false; this makes the Go parser agree.
func Parse(data []byte, filename string) (*Scenario, error) {
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		// Extract line/column from yaml.TypeError if available.
		if te, ok := err.(*yaml.TypeError); ok {
			return nil, &ParseError{
				File:    filename,
				Message: te.Error(),
			}
		}
		return nil, &ParseError{
			File:    filename,
			Message: err.Error(),
		}
	}

	return &s, nil
}

// ParseRawYAML unmarshals arbitrary YAML data into the given target.
// Used by the `cellrift assert` command for loading input files.
func ParseRawYAML(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}
