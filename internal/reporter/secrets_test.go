package reporter

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/riftcell/cellrift/internal/engine"
	"github.com/riftcell/cellrift/internal/protocols/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The four shapes a password can take that each defeat a different weaker
// implementation. Shared by the masker tests and the three-reporter tests.
const (
	pwOrdinary = "shared-pw"
	pwE164     = "447700900999"
	pwHostile  = `pw"<&'pw`
	pwPrefix   = "shared-pw-extra"
)

func TestMaskerSecrets(t *testing.T) {
	rows := []struct {
		name    string
		secrets []string
		in      string
		want    string
	}{
		{"ordinary", []string{pwOrdinary}, "Authorization uses shared-pw here", "Authorization uses [REDACTED-SECRET] here"},
		{"hostile characters removed verbatim", []string{pwHostile}, "x " + pwHostile + " y", "x [REDACTED-SECRET] y"},
		{"E164-shaped secret is a secret, not a number", []string{pwE164}, "pw=" + pwE164, "pw=[REDACTED-SECRET]"},
		{"prefix secret registered first", []string{pwOrdinary, pwPrefix}, pwPrefix, "[REDACTED-SECRET]"},
		{"prefix secret registered last", []string{pwPrefix, pwOrdinary}, pwPrefix + " " + pwOrdinary, "[REDACTED-SECRET] [REDACTED-SECRET]"},
		{"duplicate registers once", []string{pwOrdinary, pwOrdinary}, pwOrdinary, "[REDACTED-SECRET]"},
		{"no secrets leaves text alone", nil, "abc " + pwOrdinary, "abc " + pwOrdinary},
	}
	for _, r := range rows {
		for _, mk := range []struct {
			name string
			m    func() *Masker
		}{{"masking", NewMasker}, {"passthrough", NewPassthroughMasker}} {
			t.Run(r.name+"/"+mk.name, func(t *testing.T) {
				m := mk.m()
				for _, s := range r.secrets {
					m.AddSecret(s)
				}
				assert.Equal(t, r.want, m.Mask(r.in))
			})
		}
	}
}

// TestMaskerSecrets_PassthroughStillRemovesSecrets is the reason the secrets
// pass sits before the disabled check. --no-mask exists so an operator can see
// their own MSISDN against their own lab gear; it is not consent to write a
// credential into an uploaded JUnit artifact. A secrets pass placed after the
// early return, the natural place since every other line of Mask is there, is
// silently bypassed by the flag.
func TestMaskerSecrets_PassthroughStillRemovesSecrets(t *testing.T) {
	m := NewPassthroughMasker()
	m.AddSecret(pwOrdinary)
	got := m.Mask("from +447700900001 with " + pwOrdinary)
	assert.Equal(t, "from +447700900001 with [REDACTED-SECRET]", got,
		"the number stays visible under --no-mask and the credential does not")
}

// TestMaskerSecrets_EmptySecretDoesNotGarbleTheReport pins the other cheap
// mistake: strings.ReplaceAll(s, "", tok) inserts tok between every character.
// An unset password is the normal case for every scenario that does not
// authenticate, so this would garble almost every report rather than a rare one.
func TestMaskerSecrets_EmptySecretDoesNotGarbleTheReport(t *testing.T) {
	for _, m := range []*Masker{NewMasker(), NewPassthroughMasker()} {
		m.AddSecret("")
		assert.Equal(t, "abc", m.Mask("abc"))
	}
}

func TestMaskerSecrets_ConcurrentMaskAndAddSecret(t *testing.T) {
	m := NewMasker()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Mask("call +447700900001 " + pwOrdinary)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 200; j++ {
			m.AddSecret(pwOrdinary)
			m.AddSecret(pwPrefix)
		}
	}()
	wg.Wait()
	assert.Equal(t, "[REDACTED-SECRET]", m.Mask(pwOrdinary))
}

// hostileResult builds a RunResult that carries the password in every place a
// reporter is known to print: a step Result.Error, a step Error, a RawResponse
// (which only the terminal reporter emits, and only under --verbose), and a
// run-level Warning. Putting it in all four means no reporter can pass by
// happening not to print the one field the test chose.
func hostileResult(password string) *engine.RunResult {
	return &engine.RunResult{
		ScenarioName: "auth",
		Status:       common.StatusFailed,
		Warnings:     []string{"warn carries " + password},
		Secrets:      []string{password},
		Steps: []engine.StepReport{
			{StepID: "a", Result: &common.StepResult{Status: common.StatusFailed, Error: "result error " + password,
				RawResponse: []byte("SIP/2.0 401\r\nX-Echo: " + password + "\r\n")}},
			{StepID: "b", Error: errors.New("step error " + password),
				Result: &common.StepResult{Status: common.StatusFailed, Error: "unused"}},
		},
	}
}

// renderers maps a reporter name to a function producing its FINAL serialised
// bytes. Assertions run on these bytes, after every escaping step, which is the
// only place an escape-before-mask ordering bug would show.
type renderer func(noMask bool, results []*engine.RunResult) (string, error)

var renderers = map[string]renderer{
	"terminal": func(nm bool, rs []*engine.RunResult) (string, error) {
		return renderTerminal(false, nm, rs)
	},
	"terminal-verbose": func(nm bool, rs []*engine.RunResult) (string, error) {
		return renderTerminal(true, nm, rs)
	},
	"json": func(nm bool, rs []*engine.RunResult) (string, error) {
		var b bytes.Buffer
		err := (&JSONReporter{NoMask: nm}).Report(&b, rs)
		return b.String(), err
	},
	"junit": func(nm bool, rs []*engine.RunResult) (string, error) {
		var b bytes.Buffer
		err := (&JUnitReporter{NoMask: nm}).Report(&b, rs)
		return b.String(), err
	},
}

func renderTerminal(verbose, noMask bool, rs []*engine.RunResult) (string, error) {
	var b bytes.Buffer
	r := NewTerminalReporter(&b, verbose)
	r.NoMask = noMask
	err := r.Report(&b, rs)
	return b.String(), err
}

// escapedForms returns the forms a reporter could write the password in once
// its format has escaped it, computed by the standard library rather than by
// hand so the test cannot be wrong about what escaping produces.
func escapedForms(t *testing.T, pw string) []string {
	j, err := json.Marshal(pw)
	require.NoError(t, err)
	var x bytes.Buffer
	require.NoError(t, xml.EscapeText(&x, []byte(pw)))
	jsonBody := string(j[1 : len(j)-1])
	return []string{pw, jsonBody, x.String(), strings.ReplaceAll(jsonBody, `&`, "&")}
}

func TestSecretMasking_AllThreeReporters(t *testing.T) {
	for _, pw := range []string{pwOrdinary, pwE164, pwHostile, pwPrefix} {
		for name, render := range renderers {
			for _, noMask := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/NoMask=%v", pw, name, noMask), func(t *testing.T) {
					out, err := render(noMask, []*engine.RunResult{hostileResult(pw)})
					require.NoError(t, err)
					assert.Contains(t, out, secretToken, "the secret must have been seen and replaced, not merely absent")
					for _, form := range escapedForms(t, pw) {
						assert.NotContains(t, out, form)
					}
				})
			}
		}
	}
}

func TestSecretMasking_PrefixSecretLeavesNoSuffix(t *testing.T) {
	res := hostileResult(pwPrefix)
	res.Secrets = []string{pwOrdinary, pwPrefix}
	for name, render := range renderers {
		out, err := render(true, []*engine.RunResult{res})
		require.NoError(t, err)
		assert.NotContains(t, out, "-extra", name)
	}
}

func TestSecretMasking_UnionAcrossResults(t *testing.T) {
	a, b := hostileResult("first-secret"), hostileResult("second-secret")
	for name, render := range renderers {
		out, err := render(false, []*engine.RunResult{a, b})
		require.NoError(t, err)
		assert.NotContains(t, out, "first-secret", name)
		assert.NotContains(t, out, "second-secret", name)
	}
}

// TestSecretMasking_NoSecretsIsByteIdentical: an empty Secrets slice and a nil
// one render identically, and an empty secret registers nothing.
func TestSecretMasking_NoSecretsIsByteIdentical(t *testing.T) {
	mk := func(secrets []string) *engine.RunResult {
		r := hostileResult("pw")
		r.Secrets = secrets
		return r
	}
	for name, render := range renderers {
		nilOut, err := render(false, []*engine.RunResult{mk(nil)})
		require.NoError(t, err)
		emptyOut, err := render(false, []*engine.RunResult{mk([]string{})})
		require.NoError(t, err)
		blankOut, err := render(false, []*engine.RunResult{mk([]string{""})})
		require.NoError(t, err)
		assert.Equal(t, nilOut, emptyOut, name)
		assert.Equal(t, nilOut, blankOut, name)
		assert.NotContains(t, nilOut, secretToken, name)
	}
}

// TestSecretMasking_SecretsIsNotAReportField: the registry is consumed by the
// masker and never serialised, even though it is on RunResult.
func TestSecretMasking_SecretsIsNotAReportField(t *testing.T) {
	r := &engine.RunResult{ScenarioName: "s", Status: common.StatusPassed, Secrets: []string{"never-emitted-value"}}
	for name, render := range renderers {
		out, err := render(true, []*engine.RunResult{r})
		require.NoError(t, err)
		assert.NotContains(t, out, "never-emitted-value", name)
		assert.NotContains(t, strings.ToLower(out), "secrets", name)
	}
}
