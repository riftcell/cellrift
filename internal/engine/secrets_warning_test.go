package engine_test

import (
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/riftcell/cellrift/internal/scenario"
	"github.com/stretchr/testify/assert"
)

// secretAdapter passes every step and reports a fixed secret list.
type secretAdapter struct{ secrets []string }

func (a *secretAdapter) Execute(string, string, string, map[string]string, map[string]string) (*common.StepResult, error) {
	return &common.StepResult{Status: common.StatusPassed}, nil
}
func (a *secretAdapter) Close() error      { return nil }
func (a *secretAdapter) Secrets() []string { return a.secrets }

func runWithSecrets(secrets []string, preWarnings []string, steps int) *engine.RunResult {
	s := &scenario.Scenario{Name: "s"}
	for i := 0; i < steps; i++ {
		s.Steps = append(s.Steps, scenario.Step{ID: "x", Protocol: "sip", Action: "send"})
	}
	adapters := common.AdapterMap{"sip": &secretAdapter{secrets: secrets}}
	return engine.Run(s, adapters, engine.RunOptions{Warnings: preWarnings})
}

func countShortWarnings(r *engine.RunResult) int {
	n := 0
	for _, w := range r.Warnings {
		if len(w) > 0 && w[:len("a configured credential")] == "a configured credential" {
			n++
		}
	}
	return n
}

// A short secret warns exactly once per run (not per step, not per secret),
// the warning names neither the secret nor its length, and the secret is still
// collected so it is still masked.
func TestRun_ShortSecretWarnsOncePerRun(t *testing.T) {
	r := runWithSecrets([]string{"0", "lab", "a-long-enough-secret"}, nil, 5)
	assert.Equal(t, 1, countShortWarnings(r))
	assert.Equal(t, []string{"0", "lab", "a-long-enough-secret"}, r.Secrets)
	for _, w := range r.Warnings {
		assert.NotContains(t, w, "lab")
		assert.NotContains(t, w, "1 character")
	}
}

func TestRun_LongSecretsProduceNoWarning(t *testing.T) {
	r := runWithSecrets([]string{"12345678", "shared-pw-extra"}, nil, 2)
	assert.Zero(t, countShortWarnings(r))
	assert.Empty(t, r.Warnings)
}

func TestRun_ShortSecretWarningKeepsEarlierWarningsAndDoesNotAliasThem(t *testing.T) {
	pre := make([]string, 1, 4)
	pre[0] = "transport substituted"
	r := runWithSecrets([]string{"x"}, pre, 1)
	assert.Equal(t, []string{"transport substituted"}, pre[:1])
	assert.Len(t, r.Warnings, 2)
	assert.Equal(t, "transport substituted", r.Warnings[0])
	assert.Equal(t, "", pre[:2][1], "the caller's backing array was not written")
}
