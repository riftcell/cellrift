package sip

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadCredentialsFile_DecodeErrorQuotesNoFileContent reproduces a real leak
// path. yaml.v3 quotes the offending scalar when a tag forces a type the value
// does not have ("cannot decode !!str `...` as a !!int"), and the decode error
// reaches a report through the "credentials could not be loaded" diagnosis. The
// quoted text is the password itself, and it is a password the run never
// RESOLVED, so no registry of resolved secrets could remove it: the error text
// has to be clean at its source.
func TestLoadCredentialsFile_DecodeErrorQuotesNoFileContent(t *testing.T) {
	rows := []struct{ name, content, secret string }{
		{"int tag", "username: u\npassword: !!int hunter2secret\n", "hunter2secret"},
		{"float tag", "password: !!float s3cr3tpw\n", "s3cr3tpw"},
		{"username tag", "username: !!int impi-secret-name\npassword: ok\n", "impi-secret-name"},
		{"backtick in value", "password: !!int a` as b`c-secretpw\n", "secretpw"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := resolveCredentials(map[string]string{"credentials_file": "f"}, fileReader(r.content), envReturning(""))
			require.Error(t, err)
			assert.NotContains(t, err.Error(), r.secret)
			assert.Contains(t, err.Error(), "credentials file", "the error must still say what failed")
		})
	}
}

func TestLoadCredentialsFile_ScrubKeepsUsefulDiagnostics(t *testing.T) {
	_, err := resolveCredentials(map[string]string{"credentials_file": "f"}, fileReader("passwd: x\n"), envReturning(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field passwd not found")
	_, err = resolveCredentials(map[string]string{"credentials_file": "f"}, fileReader("password: [a, b\n"), envReturning(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 1")
}
