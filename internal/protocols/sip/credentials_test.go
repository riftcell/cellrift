package sip

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/riftcell/cellrift/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	p1 = "pw-scenario-1"
	p2 = "pw-file-2"
	p3 = "pw-env-3"
)

func fileReader(content string) func(string) ([]byte, error) {
	return func(string) ([]byte, error) { return []byte(content), nil }
}

func envReturning(v string) func(string) string {
	return func(name string) string {
		if name == sipPasswordEnv {
			return v
		}
		return ""
	}
}

var noFile = func(string) ([]byte, error) { return nil, errors.New("readFile must not be called") }

const fileU2P2 = "username: u2\npassword: p2\n"

type credRow struct {
	name    string
	cfg     map[string]string
	file    func(string) ([]byte, error)
	env     string
	want    Credentials
	wantErr string
}

func credRows() []credRow {
	return []credRow{
		{name: "layer 1 alone beats env", cfg: map[string]string{"username": "u1", "password": p1}, file: noFile, env: p3,
			want: Credentials{"u1", p1, "scenario"}},
		{name: "layer 2 alone beats env", cfg: map[string]string{"credentials_file": "f"}, file: fileReader(fileU2P2), env: p3,
			want: Credentials{"u2", "p2", "credentials_file"}},
		{name: "layer 3 alone supplies the password", cfg: map[string]string{"username": "u1"}, file: noFile, env: p3,
			want: Credentials{"u1", p3, "CELLRIFT_SIP_PASSWORD"}},
		{name: "boundary 1: scenario beats file", cfg: map[string]string{"username": "u1", "password": p1, "credentials_file": "f"},
			file: fileReader(fileU2P2), env: p3, want: Credentials{"u1", p1, "scenario"}},
		{name: "boundary 1 per field: scenario username, file password", cfg: map[string]string{"credentials_file": "f", "username": "u1"},
			file: fileReader(fileU2P2), env: p3, want: Credentials{"u1", "p2", "credentials_file"}},
		{name: "boundary 2: file beats env", cfg: map[string]string{"credentials_file": "f"},
			file: fileReader(fileU2P2), env: p3, want: Credentials{"u2", "p2", "credentials_file"}},
		{name: "boundary 2: file without a password falls through to env for the password only",
			cfg: map[string]string{"credentials_file": "f"}, file: fileReader("username: u2\n"), env: p3,
			want: Credentials{"u2", p3, "CELLRIFT_SIP_PASSWORD"}},
		{name: "empty credentials file supplies nothing", cfg: map[string]string{"credentials_file": "f", "username": "u1"},
			file: fileReader(""), env: p3, want: Credentials{"u1", p3, "CELLRIFT_SIP_PASSWORD"}},
	}
}

func errRows() []credRow {
	return []credRow{
		{name: "nothing configured", cfg: map[string]string{}, file: noFile,
			wantErr: "no SIP credentials configured (password)"},
		{name: "no username has no default", cfg: map[string]string{"password": p1}, file: noFile,
			wantErr: "no default"},
		{name: "unreadable file is not a fall-through", cfg: map[string]string{"username": "u1", "credentials_file": "/x/creds.yaml"},
			file: func(string) ([]byte, error) { return nil, fs.ErrNotExist }, env: p3, wantErr: `credentials file "/x/creds.yaml"`},
		{name: "malformed file", cfg: map[string]string{"credentials_file": "f"},
			file: fileReader("password: [1,2]\n"), env: p3, wantErr: "credentials file"},
		{name: "unknown key in file", cfg: map[string]string{"credentials_file": "f"},
			file: fileReader("passwd: " + p2 + "\n"), env: p3, wantErr: "passwd"},
	}
}

func TestResolveCredentials(t *testing.T) {
	for _, r := range credRows() {
		t.Run(r.name, func(t *testing.T) {
			got, err := resolveCredentials(r.cfg, r.file, envReturning(r.env))
			require.NoError(t, err)
			assert.Equal(t, r.want, got)
		})
	}
}

func TestResolveCredentials_Errors(t *testing.T) {
	for _, r := range errRows() {
		t.Run(r.name, func(t *testing.T) {
			_, err := resolveCredentials(r.cfg, r.file, envReturning(r.env))
			require.Error(t, err)
			assert.Contains(t, err.Error(), r.wantErr)
		})
	}
}

func TestResolveCredentials_NothingConfiguredNamesAllThreeSources(t *testing.T) {
	_, err := resolveCredentials(map[string]string{}, noFile, envReturning(""))
	require.Error(t, err)
	for _, s := range []string{"protocols.sip.password", "protocols.sip.credentials_file", "CELLRIFT_SIP_PASSWORD"} {
		assert.Contains(t, err.Error(), s)
	}
}

func TestResolveCredentials_UnknownKeyNamesTheField(t *testing.T) {
	_, err := resolveCredentials(map[string]string{"credentials_file": "f"}, fileReader("passwd: x\n"), envReturning(p3))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "field passwd not found")
}

// TestResolveCredentials_NoErrorLeaksAPassword runs every error-producing row
// with three distinct password values and asserts none appears in any error.
func TestResolveCredentials_NoErrorLeaksAPassword(t *testing.T) {
	for _, r := range errRows() {
		cfg := map[string]string{}
		for k, v := range r.cfg {
			cfg[k] = v
		}
		if r.name != "nothing configured" && r.name != "no username has no default" {
			cfg["password"] = p1
		}
		for _, env := range []string{p3, ""} {
			_, err := resolveCredentials(cfg, r.file, envReturning(env))
			if err == nil {
				continue
			}
			for _, secret := range []string{p1, p2, p3} {
				assert.False(t, strings.Contains(err.Error(), secret), "%s: error leaks %s", r.name, secret)
			}
		}
	}
}

// TestCredentials_EnvVarNameMatchesConfigPackage proves the name this package
// reads is the name internal/config documents and reads, because the two are
// separate string literals in separate packages and nothing else connects them.
func TestCredentials_EnvVarNameMatchesConfigPackage(t *testing.T) {
	t.Setenv(sipPasswordEnv, "probe")
	assert.Equal(t, "probe", config.SIPPassword())
}

// TestResolveCredentials_ErrNoCredentialsContract pins both directions of the
// sentinel. The false half is the one that matters: a broken credentials_file
// must never be reported as "no credentials configured".
func TestResolveCredentials_ErrNoCredentialsContract(t *testing.T) {
	rows := []struct {
		name string
		row  credRow
		want bool
	}{
		{"no password is ErrNoCredentials", credRow{cfg: map[string]string{"username": "u"}, file: noFile}, true},
		{"no username is ErrNoCredentials", credRow{cfg: map[string]string{"password": p1}, file: noFile}, true},
		{"unreadable file is not ErrNoCredentials", credRow{cfg: map[string]string{"credentials_file": "/x/c.yaml"},
			file: func(string) ([]byte, error) { return nil, fs.ErrNotExist }}, false},
		{"malformed file is not ErrNoCredentials", credRow{cfg: map[string]string{"credentials_file": "f"},
			file: fileReader("password: [1,2]\n")}, false},
		{"unknown key file is not ErrNoCredentials", credRow{cfg: map[string]string{"credentials_file": "f"},
			file: fileReader("passwd: x\n")}, false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			_, err := resolveCredentials(r.row.cfg, r.row.file, envReturning(""))
			require.Error(t, err)
			assert.Equal(t, r.want, errors.Is(err, ErrNoCredentials))
		})
	}
}
