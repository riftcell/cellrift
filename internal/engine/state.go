// Package engine provides the scenario execution engine for Cellrift.
package engine

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// templatePattern matches {{ variable_name }} tokens with optional whitespace.
var templatePattern = regexp.MustCompile(`\{\{\s*([a-zA-Z_][a-zA-Z0-9_.]*)\s*\}\}`)

// State holds captured variables during scenario execution.
// It is safe for concurrent reads.
type State struct {
	mu   sync.RWMutex
	vars map[string]interface{}
}

// NewState creates an empty State.
func NewState() *State {
	return &State{vars: make(map[string]interface{})}
}

// Set stores a value under the given key.
func (s *State) Set(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vars[key] = value
}

// Get retrieves a value by key.
func (s *State) Get(key string) (interface{}, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.vars[key]
	return v, ok
}

// GetString retrieves a string value by key. Returns "" if not found.
func (s *State) GetString(key string) string {
	v, ok := s.Get(key)
	if !ok {
		return ""
	}
	return formatValue(v)
}

// formatValue renders a captured value for string interpolation and
// comparison. []byte is special-cased to its literal text: fmt's %v verb
// renders a byte slice as a decimal list ("[83 73 80 ...]"), which is
// useless for a raw protocol message captured through the reserved raw
// response path and then referenced in a later step.
func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// All returns a copy of all variables.
func (s *State) All() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cp := make(map[string]interface{}, len(s.vars))
	for k, v := range s.vars {
		cp[k] = v
	}
	return cp
}

// Resolve substitutes all {{ variable }} tokens in a template string using
// the current state. Dotted notation (e.g. step_id.field) is supported by
// flattening the key with dots. Returns an error if any referenced variable
// is undefined.
func (s *State) Resolve(template string) (string, error) {
	var resolveErr error

	result := templatePattern.ReplaceAllStringFunc(template, func(match string) string {
		if resolveErr != nil {
			return match
		}
		// Extract the variable name from {{ name }}.
		sub := templatePattern.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		varName := sub[1]

		val, ok := s.Get(varName)
		if ok {
			return formatValue(val)
		}

		// Try dotted lookup: "step_id.field" → lookup "step_id.field" directly.
		// Already checked above, so this is a true miss.
		resolveErr = fmt.Errorf("undefined variable %q in template", varName)
		return match
	})

	if resolveErr != nil {
		return "", resolveErr
	}
	return result, nil
}

// ResolveMap resolves all values in a map of strings.
func (s *State) ResolveMap(fields map[string]string) (map[string]string, error) {
	resolved := make(map[string]string, len(fields))
	for k, v := range fields {
		rv, err := s.Resolve(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		resolved[k] = rv
	}
	return resolved, nil
}

// LoadVariables populates state from scenario-defined variables.
func (s *State) LoadVariables(vars map[string]string) {
	for k, v := range vars {
		s.Set(k, v)
	}
}

// LoadOverrides applies CLI --var overrides. Each entry is "key=value".
func (s *State) LoadOverrides(overrides []string) {
	for _, o := range overrides {
		parts := strings.SplitN(o, "=", 2)
		if len(parts) == 2 {
			s.Set(parts[0], parts[1])
		}
	}
}

// LoadEnvOverrides loads variables from environment variables matching
// CELLRIFT_VAR_<KEY>. Keys are lowercased and underscores are preserved.
func (s *State) LoadEnvOverrides(environ []string) {
	const prefix = "CELLRIFT_VAR_"
	for _, env := range environ {
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if strings.HasPrefix(parts[0], prefix) {
			key := strings.ToLower(strings.TrimPrefix(parts[0], prefix))
			s.Set(key, parts[1])
		}
	}
}
