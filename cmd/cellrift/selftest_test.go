package main

// selftest_test.go pins the two commands that exist for a user with no
// infrastructure and no clone: `cellrift selftest` (the only run that passes
// out of the box) and `cellrift init` (the bundled library, unpacked). Both
// go through the real binary so a regression in wiring, not just in a
// helper, turns the build red.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/riftcell/cellrift/scenarios"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelftestCmd_PassesWithNoInfrastructure(t *testing.T) {
	bin := binaryPath(t)
	cmd := exec.Command(bin, "selftest")
	cmd.Dir = t.TempDir() // no scenarios/ directory: everything must come from the binary
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "selftest must exit 0, got:\n%s", out)

	text := string(out)
	assert.Contains(t, text, "5/5 steps passed")
	assert.Contains(t, text, "self-test peers:")
	assert.Contains(t, text, "verify_registered", "the assert_state step must be listed and green")
}

func TestSelftestCmd_HonoursFormat(t *testing.T) {
	bin := binaryPath(t)
	out, err := exec.Command(bin, "selftest", "--format", "junit").CombinedOutput()
	require.NoError(t, err, "got:\n%s", out)
	assert.Contains(t, string(out), "<testsuite")
	assert.Contains(t, string(out), `failures="0"`)
}

// The embedded copy and the on-disk library must be the same files: a
// scenario added to scenarios/ but missed by the go:embed pattern would ship
// in the repo and vanish from `cellrift init`.
func TestEmbeddedLibrary_MatchesDisk(t *testing.T) {
	disk := scenario.ScanLibrary(scenarioDir())
	embedded := scenario.ScanLibraryFS(scenarios.FS)
	require.NotEmpty(t, disk)

	diskPaths := make([]string, len(disk))
	for i, e := range disk {
		rel, err := filepath.Rel(scenarioDir(), e.Path)
		require.NoError(t, err)
		diskPaths[i] = filepath.ToSlash(rel)
	}
	embeddedPaths := make([]string, len(embedded))
	for i, e := range embedded {
		embeddedPaths[i] = e.Path
	}
	assert.ElementsMatch(t, diskPaths, embeddedPaths,
		"scenarios/embed.go's go:embed pattern does not cover every shipped scenario")
}

func TestInitCmd_WritesLibraryAndNeverOverwrites(t *testing.T) {
	bin := binaryPath(t)
	work := t.TempDir()

	out, err := exec.Command(bin, "init", filepath.Join(work, "lib")).CombinedOutput()
	require.NoError(t, err, "got:\n%s", out)
	assert.Contains(t, string(out), "wrote")

	written := scenario.ScanLibrary(filepath.Join(work, "lib"))
	assert.Len(t, written, len(scenario.ScanLibraryFS(scenarios.FS)))

	// Edit one file, run init again: the edit must survive.
	edited := filepath.Join(work, "lib", "ims", "basic_call.yaml")
	require.NoError(t, os.WriteFile(edited, []byte("# mine\n"), 0o644))
	out, err = exec.Command(bin, "init", filepath.Join(work, "lib")).CombinedOutput()
	require.NoError(t, err, "got:\n%s", out)
	assert.Contains(t, string(out), "kept")
	data, err := os.ReadFile(edited)
	require.NoError(t, err)
	assert.Equal(t, "# mine\n", string(data), "init must never overwrite a user's file")
}

func TestInitCmd_DefaultDirIsScenarios(t *testing.T) {
	bin := binaryPath(t)
	cmd := exec.Command(bin, "init")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "got:\n%s", out)
	_, err = os.Stat(filepath.Join(cmd.Dir, "scenarios", "selftest", "loopback.yaml"))
	assert.NoError(t, err)
}

// Outside a clone `cellrift list` used to print "no scenarios found" — a
// dead end for the go-install user. It now lists the bundled copy and says
// how to unpack it.
func TestListCmd_FallsBackToBundledLibrary(t *testing.T) {
	bin := binaryPath(t)
	cmd := exec.Command(bin, "list")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "got:\n%s", out)
	text := string(out)
	assert.Contains(t, text, "bundled in this binary")
	assert.Contains(t, text, "cellrift init")
	assert.Contains(t, text, "ims/basic_call.yaml")
	assert.NotContains(t, text, "no scenarios found")
	assert.Greater(t, strings.Count(text, "\n"), 10)
}
