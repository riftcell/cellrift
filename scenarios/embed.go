// Package scenarios embeds the shipped scenario library into the cellrift
// binary, so a `go install` user — who has no clone and therefore no
// scenarios/ directory — can still run `cellrift list`, unpack the library
// with `cellrift init`, and see a passing run with `cellrift selftest`.
//
// The package lives at the repository root, next to the YAML it embeds,
// because a go:embed pattern cannot reach a parent directory: nothing under
// internal/ or cmd/ can embed ../../scenarios. It contains no logic; the
// scanning and parsing stay in internal/scenario.
package scenarios

import "embed"

// FS holds every shipped scenario, addressed by its path relative to this
// directory (e.g. "ims/basic_call.yaml"), plus the library's own README so
// `cellrift init` writes the guide next to the files it describes. This Go
// file is not embedded.
//
//go:embed ims/*.yaml diameter/*.yaml selftest/*.yaml README.md
var FS embed.FS

// SelfTestPath is the scenario `cellrift selftest` runs against the
// in-process mock peers it starts.
const SelfTestPath = "selftest/loopback.yaml"
