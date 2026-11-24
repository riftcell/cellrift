package sip

// This file corrupts the output of Serialise, which is already a complete
// and correct SIP message. Serialise itself must never grow a corruption
// branch -- these functions operate strictly after serialisation, splicing
// the already-correct wire bytes it produced.
//
// The duplicate operation is possible ONLY here, after serialisation,
// because SIPMessage.Headers is a map[string]string that cannot hold two
// values for one header name, and ParseMessage coalesces same-named headers
// into a single ", "-joined value (see ParseMessage's header loop above). A
// duplicated header can only be expressed as two distinct lines in the raw
// byte buffer, never as a second map entry.

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/riftcell/cellrift/internal/protocols/common/malform"
)

// crlf is the line terminator every header line and the header/body
// boundary use, matching Serialise's own output exactly.
const crlf = "\r\n"

// splitHeaderBlock splits a serialised SIP message into its start-line, its
// header lines, and its tail (the blank line marking the header/body
// boundary plus any body bytes that follow it). tail is reconstructed
// byte-identically to the original input via the Split/Join inverse
// relationship, regardless of what bytes the body itself contains.
func splitHeaderBlock(raw []byte) (startLine []byte, headerLines [][]byte, tail []byte, err error) {
	lines := bytes.Split(raw, []byte(crlf))
	startLine = lines[0]
	for i := 1; i < len(lines); i++ {
		if len(lines[i]) == 0 {
			return startLine, lines[1:i], bytes.Join(lines[i:], []byte(crlf)), nil
		}
	}
	return nil, nil, nil, fmt.Errorf("sip malform: message has no header/body boundary")
}

// rebuildMessage is splitHeaderBlock's inverse: it joins the start-line and
// header lines with CRLF, then appends the header/body boundary and tail
// unchanged. It always returns a freshly allocated buffer, never a slice
// aliasing any of its inputs.
func rebuildMessage(startLine []byte, headerLines [][]byte, tail []byte) []byte {
	parts := make([][]byte, 0, len(headerLines)+1)
	parts = append(parts, startLine)
	parts = append(parts, headerLines...)
	head := bytes.Join(parts, []byte(crlf))

	out := make([]byte, 0, len(head)+len(crlf)+len(tail))
	out = append(out, head...)
	out = append(out, crlf...)
	out = append(out, tail...)
	return out
}

// headerLineIndex returns the index of the first header line named name
// (case-insensitive), or -1 if no such line exists.
func headerLineIndex(headerLines [][]byte, name string) int {
	for i, line := range headerLines {
		colonIdx := bytes.IndexByte(line, ':')
		if colonIdx < 0 {
			continue
		}
		lineName := strings.TrimSpace(string(line[:colonIdx]))
		if strings.EqualFold(lineName, name) {
			return i
		}
	}
	return -1
}

// missingHeaderError is the single not-present error every malform op
// returns when its target header does not exist in the serialised message.
func missingHeaderError(name string) error {
	return fmt.Errorf("sip malform: header %q is not present in the serialised message", name)
}

// RemoveHeader deletes the named header's line entirely, leaving every other
// line -- including the start-line, the header/body boundary, and the body
// -- byte-identical to the input. Returns missingHeaderError if the header
// is not present.
func RemoveHeader(raw []byte, name string) ([]byte, error) {
	startLine, headerLines, tail, err := splitHeaderBlock(raw)
	if err != nil {
		return nil, err
	}
	idx := headerLineIndex(headerLines, name)
	if idx < 0 {
		return nil, missingHeaderError(name)
	}

	newLines := make([][]byte, 0, len(headerLines)-1)
	newLines = append(newLines, headerLines[:idx]...)
	newLines = append(newLines, headerLines[idx+1:]...)
	return rebuildMessage(startLine, newLines, tail), nil
}

// DuplicateHeader inserts a second copy of the named header's line
// immediately after the original. When value is empty the inserted line is
// a byte-identical copy of the original; otherwise it carries value under
// the header's canonical name. Appending rather than replacing is the whole
// point: real SIP UAs are inconsistently first-wins or last-wins when they
// see a repeated header, so a scenario must be able to present both values
// at once and let the peer's own (mis)handling show through. Returns
// missingHeaderError if the header is not present.
func DuplicateHeader(raw []byte, name, value string) ([]byte, error) {
	startLine, headerLines, tail, err := splitHeaderBlock(raw)
	if err != nil {
		return nil, err
	}
	idx := headerLineIndex(headerLines, name)
	if idx < 0 {
		return nil, missingHeaderError(name)
	}

	dup := duplicateLine(headerLines[idx], name, value)
	newLines := make([][]byte, 0, len(headerLines)+1)
	newLines = append(newLines, headerLines[:idx+1]...)
	newLines = append(newLines, dup)
	newLines = append(newLines, headerLines[idx+1:]...)
	return rebuildMessage(startLine, newLines, tail), nil
}

// duplicateLine builds the second copy DuplicateHeader inserts: a verbatim
// copy of original when value is empty, or a fresh "Name: value" line
// otherwise.
func duplicateLine(original []byte, name, value string) []byte {
	if value == "" {
		return append([]byte(nil), original...)
	}
	return []byte(canonicalHeaderName(name) + ": " + value)
}

// OverwriteHeader replaces the named header's line in place with
// "Name: value", preserving its position in the header block so ordering is
// unchanged. It never recomputes Content-Length or any other framing field
// -- even when name is itself Content-Length -- because a corrupted message
// that silently repairs its own framing is not a corrupted message. Returns
// missingHeaderError if the header is not present.
func OverwriteHeader(raw []byte, name, value string) ([]byte, error) {
	startLine, headerLines, tail, err := splitHeaderBlock(raw)
	if err != nil {
		return nil, err
	}
	idx := headerLineIndex(headerLines, name)
	if idx < 0 {
		return nil, missingHeaderError(name)
	}

	newLines := make([][]byte, len(headerLines))
	copy(newLines, headerLines)
	newLines[idx] = []byte(canonicalHeaderName(name) + ": " + value)
	return rebuildMessage(startLine, newLines, tail), nil
}

// HeaderOpTable maps a malform: sip_header op name to the byte-splice
// function that performs it. Named because the same three op strings appear
// in three places that must never disagree: this table, the scenario
// validator's accepted-op set, and the op enum published in
// docs/scenario.schema.json. All three read the Op* constants in
// internal/protocols/common/malform, so the strings themselves live in
// exactly one file.
type HeaderOpTable map[string]func(raw []byte, name, value string) ([]byte, error)

// headerOps is the table applyHeaderMalform dispatches through. RemoveHeader
// takes no value, so it is adapted by a small literal rather than widening
// its own signature -- the pure function keeps the signature its behaviour
// justifies.
var headerOps = HeaderOpTable{
	malform.OpMissing:   func(raw []byte, name, _ string) ([]byte, error) { return RemoveHeader(raw, name) },
	malform.OpDuplicate: DuplicateHeader,
	malform.OpOverwrite: OverwriteHeader,
}

// sortedHeaderOps returns headerOps' keys in sorted order, so an
// unknown-op error message can never drift from the table it is generated
// from.
func sortedHeaderOps() []string {
	ops := make([]string, 0, len(headerOps))
	for op := range headerOps {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	return ops
}

// applyHeaderMalform reads a step's declared malform: sip_header block back
// out of config and, if present, dispatches to the matching entry in
// headerOps. A config with no sip_header declared returns raw unchanged and
// a nil error -- the corruption path must be inert when no malform: block
// exists, so an ordinary send is never affected by this function's presence.
func applyHeaderMalform(raw []byte, config map[string]string) ([]byte, error) {
	spec, ok := malform.SIPHeaderOverride(config)
	if !ok {
		return raw, nil
	}
	op, found := headerOps[spec.Op]
	if !found {
		return nil, fmt.Errorf("sip malform: unknown sip_header op %q (allowed: %s)",
			spec.Op, strings.Join(sortedHeaderOps(), ", "))
	}
	return op(raw, spec.Name, spec.Value)
}
