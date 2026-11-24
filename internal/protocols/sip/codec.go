package sip

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/riftcell/cellrift/internal/protocols/common"
)

// SIPMessage represents a parsed SIP request or response.
type SIPMessage struct {
	// Common
	Headers map[string]string
	Body    []byte

	// Request fields (empty for responses)
	Method     string
	RequestURI string

	// Response fields (empty for requests)
	StatusCode   int
	ReasonPhrase string

	// Protocol version, e.g. "SIP/2.0"
	Version string
}

// IsRequest returns true if the message is a SIP request.
func (m *SIPMessage) IsRequest() bool {
	return m.Method != ""
}

// Well-known SIP header names (RFC 3261 and 3GPP extensions).
const (
	HeaderFrom          = "From"
	HeaderTo            = "To"
	HeaderVia           = "Via"
	HeaderContact       = "Contact"
	HeaderCallID        = "Call-ID"
	HeaderCSeq          = "CSeq"
	HeaderAuthorization = "Authorization"
	HeaderWWWAuth       = "WWW-Authenticate"
	// Proxy variants (407). canonicalHeaderName's generic title-casing already
	// yields these exact spellings; TestCanonicalHeaderName_ProxyHeaders pins
	// that, so no canonicalHeaderOverrides entry is needed.
	HeaderProxyAuth          = "Proxy-Authenticate"
	HeaderProxyAuthorization = "Proxy-Authorization"
	HeaderContentLength      = "Content-Length"
	HeaderContentType        = "Content-Type"
	HeaderMaxForwards        = "Max-Forwards"
	HeaderUserAgent          = "User-Agent"
)

// Compact header name mapping (RFC 3261 Section 7.3.3).
var compactHeaders = map[string]string{
	"f": HeaderFrom,
	"t": HeaderTo,
	"v": HeaderVia,
	"m": HeaderContact,
	"i": HeaderCallID,
	"l": HeaderContentLength,
	"c": HeaderContentType,
}

// ParseMessage parses raw SIP bytes into a SIPMessage.
func ParseMessage(raw []byte) (*SIPMessage, error) {
	// Split header block from body at \r\n\r\n.
	parts := bytes.SplitN(raw, []byte("\r\n\r\n"), 2)
	headerBlock := string(parts[0])
	var body []byte
	if len(parts) > 1 {
		body = parts[1]
	}

	lines := strings.Split(headerBlock, "\r\n")
	if len(lines) == 0 {
		return nil, fmt.Errorf("empty SIP message")
	}

	msg := &SIPMessage{
		Headers: make(map[string]string),
		Body:    body,
	}

	// Parse start line.
	if err := parseStartLine(lines[0], msg); err != nil {
		return nil, err
	}

	// Parse headers.
	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		colonIdx := strings.IndexByte(line, ':')
		if colonIdx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:colonIdx])
		value := strings.TrimSpace(line[colonIdx+1:])

		// Expand compact header names.
		if expanded, ok := compactHeaders[strings.ToLower(name)]; ok {
			name = expanded
		}

		// Canonical form: first letter uppercase.
		name = canonicalHeaderName(name)

		// Multi-value headers: append with comma.
		if existing, ok := msg.Headers[name]; ok {
			msg.Headers[name] = existing + ", " + value
		} else {
			msg.Headers[name] = value
		}
	}

	return msg, nil
}

func parseStartLine(line string, msg *SIPMessage) error {
	parts := strings.SplitN(line, " ", 3)
	if len(parts) < 3 {
		return fmt.Errorf("malformed start line: %q", line)
	}

	if strings.HasPrefix(parts[0], "SIP/") {
		// Response: SIP/2.0 200 OK
		msg.Version = parts[0]
		code, err := strconv.Atoi(parts[1])
		if err != nil {
			return fmt.Errorf("invalid status code %q: %w", parts[1], err)
		}
		msg.StatusCode = code
		msg.ReasonPhrase = parts[2]
	} else {
		// Request: REGISTER sip:ims.local SIP/2.0
		msg.Method = parts[0]
		msg.RequestURI = parts[1]
		msg.Version = parts[2]
	}
	return nil
}

// canonicalHeaderOverrides maps lowercase header names to their canonical forms
// for headers that don't follow simple title-case (e.g. "Call-ID" not "Call-Id").
var canonicalHeaderOverrides = map[string]string{
	"call-id":               "Call-ID",
	"cseq":                  "CSeq",
	"www-authenticate":      "WWW-Authenticate",
	"content-length":        "Content-Length",
	"content-type":          "Content-Type",
	"max-forwards":          "Max-Forwards",
	"user-agent":            "User-Agent",
	"p-visited-network-id":  "P-Visited-Network-ID",
	"p-access-network-info": "P-Access-Network-Info",
	"p-charging-vector":     "P-Charging-Vector",
}

// canonicalHeaderName converts a header name to canonical form.
// Special cases are checked first; all others are title-cased per hyphen segment.
func canonicalHeaderName(name string) string {
	if canonical, ok := canonicalHeaderOverrides[strings.ToLower(name)]; ok {
		return canonical
	}
	parts := strings.Split(name, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + strings.ToLower(p[1:])
		}
	}
	return strings.Join(parts, "-")
}

// Serialise converts a SIPMessage back to wire-format bytes.
func Serialise(msg *SIPMessage) []byte {
	var buf bytes.Buffer

	// Start line.
	if msg.IsRequest() {
		fmt.Fprintf(&buf, "%s %s %s\r\n", msg.Method, msg.RequestURI, msg.Version)
	} else {
		fmt.Fprintf(&buf, "%s %d %s\r\n", msg.Version, msg.StatusCode, msg.ReasonPhrase)
	}

	// Headers in a stable order: well-known first, then the rest.
	ordered := []string{
		HeaderVia, HeaderMaxForwards, HeaderFrom, HeaderTo,
		HeaderCallID, HeaderCSeq, HeaderContact, HeaderAuthorization,
		HeaderProxyAuthorization, HeaderWWWAuth, HeaderProxyAuth, HeaderContentType, HeaderContentLength, HeaderUserAgent,
	}
	written := make(map[string]bool)
	for _, name := range ordered {
		if val, ok := msg.Headers[name]; ok {
			fmt.Fprintf(&buf, "%s: %s\r\n", name, val)
			written[name] = true
		}
	}
	for name, val := range msg.Headers {
		if !written[name] {
			fmt.Fprintf(&buf, "%s: %s\r\n", name, val)
		}
	}

	buf.WriteString("\r\n")

	if len(msg.Body) > 0 {
		buf.Write(msg.Body)
	}

	return buf.Bytes()
}

// BuildRequest creates a SIP request message from components.
func BuildRequest(method, requestURI string, headers map[string]string) *SIPMessage {
	msg := &SIPMessage{
		Method:     method,
		RequestURI: requestURI,
		Version:    "SIP/2.0",
		Headers:    make(map[string]string),
	}
	for k, v := range headers {
		msg.Headers[canonicalHeaderName(k)] = v
	}
	// Ensure required defaults.
	if _, ok := msg.Headers[HeaderMaxForwards]; !ok {
		msg.Headers[HeaderMaxForwards] = "70"
	}
	if _, ok := msg.Headers[HeaderContentLength]; !ok {
		msg.Headers[HeaderContentLength] = "0"
	}
	if _, ok := msg.Headers[HeaderUserAgent]; !ok {
		msg.Headers[HeaderUserAgent] = "cellrift/0.1.0"
	}
	return msg
}

// BuildResponse creates a SIP response message.
func BuildResponse(statusCode int, reason string, headers map[string]string) *SIPMessage {
	msg := &SIPMessage{
		StatusCode:   statusCode,
		ReasonPhrase: reason,
		Version:      "SIP/2.0",
		Headers:      make(map[string]string),
	}
	for k, v := range headers {
		msg.Headers[canonicalHeaderName(k)] = v
	}
	if _, ok := msg.Headers[HeaderContentLength]; !ok {
		msg.Headers[HeaderContentLength] = "0"
	}
	return msg
}

// ResponseFieldsMap converts a SIPMessage response into a ResponseFields map for capture/assertion.
// When the response carries an application/sdp body, the parsed SDP fields are
// available under "body.sdp" (keys: "port" int, "codec" string).
func ResponseFieldsMap(msg *SIPMessage) common.ResponseFields {
	m := common.ResponseFields{
		"status_code":   msg.StatusCode,
		"reason_phrase": msg.ReasonPhrase,
	}
	headerMap := make(map[string]interface{})
	for k, v := range msg.Headers {
		headerMap[k] = v
	}
	m["header"] = headerMap

	if len(msg.Body) > 0 {
		if strings.Contains(msg.Headers[HeaderContentType], "application/sdp") {
			m["body"] = map[string]interface{}{
				"sdp": ParseSDP(msg.Body),
			}
		} else {
			m["body"] = map[string]interface{}{
				"raw": string(msg.Body),
			}
		}
	}

	return m
}
