package scenario_test

// readme_example_test.go keeps the README's front-page scenario honest.
//
// That block is the first Cellrift YAML most people will ever read, and a
// broken example on the first screen costs more trust than a missing
// feature. It is also exactly the kind of claim that rots silently: nothing
// else in the build reads the README, so a syntax change or a new required
// field would leave it wrong indefinitely. The first version of it was
// wrong on both counts — no `version:` and an undeclared variable — and
// only running it through the real validator caught that.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/require"
)

// readmeYAMLBlocks returns every ```yaml fenced block in the README.
func readmeYAMLBlocks(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(moduleRoot(), "README.md"))
	require.NoError(t, err, "README.md must be readable")

	var blocks []string
	rest := string(raw)
	for {
		start := strings.Index(rest, "```yaml\n")
		if start < 0 {
			return blocks
		}
		rest = rest[start+len("```yaml\n"):]
		end := strings.Index(rest, "```")
		if end < 0 {
			return blocks
		}
		blocks = append(blocks, rest[:end])
		rest = rest[end+3:]
	}
}

// TestREADME_FirstScenarioExampleValidates parses and validates the README's
// first YAML block through the same code path `cellrift validate` uses.
//
// Only the first block: later ones are deliberate fragments (a bare impair:
// map, a step without its scenario) that were never whole files. The first
// is presented as a complete scenario, so it must be one.
func TestREADME_FirstScenarioExampleValidates(t *testing.T) {
	blocks := readmeYAMLBlocks(t)
	require.NotEmpty(t, blocks, "the README must still carry a YAML example")

	s, err := scenario.Parse([]byte(blocks[0]), "README.md")
	require.NoError(t, err, "the README's first scenario must parse:\n%s", blocks[0])

	errs := scenario.Validate(s)
	require.Empty(t, errs, "the README's first scenario must validate: %v", errs)
}
