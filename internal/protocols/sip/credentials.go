package sip

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// sipPasswordEnv is the ambient password variable, the lowest-precedence
// source. internal/config documents and reads the same name through
// SIPPassword(); this package does not import it for one string, and a test
// pins the two spellings together.
const sipPasswordEnv = "CELLRIFT_SIP_PASSWORD"

// ErrNoCredentials is wrapped by the "no password" and "no username" failures
// and by nothing else. It marks the benign case, the author configured no
// authentication, so a caller can tell it from a credentials_file that is
// unreadable, malformed or carries an unknown key (a misconfiguration the
// author must see). Test with errors.Is; the message text is not the contract.
var ErrNoCredentials = errors.New("no SIP credentials configured")

// Credentials is one resolved digest credential pair plus where the password
// came from. Source is carried for diagnostics only; it must never be used to
// branch, or the three layers become three code paths.
type Credentials struct {
	Username string
	Password string
	Source   string // "scenario", "credentials_file", "CELLRIFT_SIP_PASSWORD"
}

// credentialsFile is the on-disk shape of a credentials_file: two keys,
// strictly decoded, nothing else. Strict because the scenario parser is strict
// and a silently ignored "passwd:" typo is the same defect as a silently
// ignored "assertions:" in a scenario.
type credentialsFile struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// ResolveCredentialsFromConfig is the production call: the same pure resolver,
// wired to the real filesystem and the real environment, so there is exactly
// one precedence implementation and the tests exercise the one production uses.
func ResolveCredentialsFromConfig(cfg map[string]string) (Credentials, error) {
	return resolveCredentials(cfg, os.ReadFile, os.Getenv)
}

// resolveCredentials applies the three precedence layers, most explicit first:
//
//  1. cfg["username"] / cfg["password"]: the scenario's protocols.sip fields,
//     already template-resolved (an unresolvable template never reaches here).
//  2. cfg["credentials_file"]: fills only the fields layer 1 left empty. A
//     named file that cannot be read or decoded is an error, never a
//     fall-through. Relative paths are relative to the working directory,
//     matching ca_file and cert_file. The file holds a secret, so it should
//     not be group- or world-readable; the resolver does not check.
//  3. CELLRIFT_SIP_PASSWORD: only when the password is still empty. There is
//     no environment source for the username, and no default username (a
//     digest username is an IMPI and need not equal the From user).
//
// readFile and getenv are injected so each boundary is testable without a
// temp file or a mutated process environment. No returned error contains a
// credential value.
func resolveCredentials(cfg map[string]string, readFile func(string) ([]byte, error), getenv func(string) string) (Credentials, error) {
	c := Credentials{Username: cfg["username"], Password: cfg["password"]}
	if c.Password != "" {
		c.Source = "scenario"
	}
	if path := cfg["credentials_file"]; path != "" {
		f, err := loadCredentialsFile(readFile, path)
		if err != nil {
			return Credentials{}, err
		}
		fillFromFile(&c, f)
	}
	if c.Password == "" {
		if c.Password = getenv(sipPasswordEnv); c.Password != "" {
			c.Source = sipPasswordEnv
		}
	}
	return c, validateCredentials(c)
}

// fillFromFile fills only the fields the scenario left empty.
func fillFromFile(c *Credentials, f credentialsFile) {
	if c.Username == "" {
		c.Username = f.Username
	}
	if c.Password == "" && f.Password != "" {
		c.Password = f.Password
		c.Source = "credentials_file"
	}
}

// loadCredentialsFile reads and strictly decodes a credentials file. An empty
// file is valid and supplies nothing.
func loadCredentialsFile(readFile func(string) ([]byte, error), path string) (credentialsFile, error) {
	var f credentialsFile
	data, err := readFile(path)
	if err != nil {
		return f, fmt.Errorf("credentials file %q: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return credentialsFile{}, fmt.Errorf("credentials file %q: %s", path, scrubDecodeError(err))
	}
	return f, nil
}

// quotedScalar matches the file content yaml.v3 quotes when a tag forces a type
// the value does not have: "cannot decode !!str `VALUE` as a !!int". The match
// is greedy up to the LAST closing quote so a value that itself contains a
// backtick is removed whole. The text between the quotes is a credentials file's
// own content, which is the password.
var quotedScalar = regexp.MustCompile("(?s)(cannot (?:decode|unmarshal) !!\\w+ )`.*`( (?:as a|into) )")

// scrubDecodeError returns err's text with any quoted file content removed. The
// error is flattened with %s rather than wrapped with %w: nothing inspects a
// credentials-file decode error with errors.Is, and keeping the chain would
// keep the unscrubbed text reachable.
func scrubDecodeError(err error) string {
	return quotedScalar.ReplaceAllString(err.Error(), "${1}<value withheld>${2}")
}

// validateCredentials reports a missing password or username by name. Neither
// message contains a value.
func validateCredentials(c Credentials) error {
	if c.Password == "" {
		return fmt.Errorf("%w (password): set protocols.sip.password, protocols.sip.credentials_file, or %s", ErrNoCredentials, sipPasswordEnv)
	}
	if c.Username == "" {
		return fmt.Errorf("%w (username): set protocols.sip.username or supply one in credentials_file (there is no default: a digest username is an IMPI and need not equal the From user)", ErrNoCredentials)
	}
	return nil
}
