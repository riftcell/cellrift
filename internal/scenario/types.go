package scenario

// Scenario represents a complete test scenario parsed from YAML.
type Scenario struct {
	Name        string                     `yaml:"name"        json:"name"`
	Version     string                     `yaml:"version"     json:"version"`
	Description string                     `yaml:"description" json:"description"`
	Protocols   map[string]*ProtocolConfig `yaml:"protocols"   json:"protocols"`
	Variables   map[string]string          `yaml:"variables"   json:"variables"`
	Steps       []Step                     `yaml:"steps"       json:"steps"`
}

// ProtocolConfig holds transport and identity settings for a single protocol.
type ProtocolConfig struct {
	Transport   string `yaml:"transport"    json:"transport"`
	LocalPort   int    `yaml:"local_port"   json:"local_port,omitempty"`
	OriginHost  string `yaml:"origin_host"  json:"origin_host,omitempty"`
	OriginRealm string `yaml:"origin_realm" json:"origin_realm,omitempty"`
	// TLS cert paths — used when transport is "tls".
	CertFile string `yaml:"cert_file" json:"cert_file,omitempty"`
	KeyFile  string `yaml:"key_file"  json:"key_file,omitempty"`
	CAFile   string `yaml:"ca_file"   json:"ca_file,omitempty"`
	// InsecureSkipVerify disables server certificate verification. It must
	// be requested explicitly: omitting ca_file no longer implies it, since
	// the most natural TLS config (transport: tls, nothing else) used to
	// yield an unauthenticated, MITM-able connection with no warning. When
	// unset, the server certificate is verified against ca_file if given,
	// otherwise the system root pool.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify" json:"insecure_skip_verify,omitempty"`

	// SIP digest credentials. username and password are the most explicit
	// source (layer 1) and may be {{ var }} templates resolved per step;
	// credentials_file names a YAML file with username/password keys (layer 2);
	// the ambient CELLRIFT_SIP_PASSWORD is layer 3. Most explicit wins, and an
	// unresolvable template in any of these is an error rather than a
	// fall-through to a lower layer.
	//
	// SIP only. The struct is shared by both protocols, so the validator, not
	// this type, is what keeps a diameter: block from carrying them.
	Username        string `yaml:"username"         json:"username,omitempty"`
	Password        string `yaml:"password"         json:"password,omitempty"`
	CredentialsFile string `yaml:"credentials_file" json:"credentials_file,omitempty"`
}

// StepBody holds optional message body configuration for a step.
// Exactly one of Template or Content must be set when Body is present.
type StepBody struct {
	ContentType string `yaml:"content_type" json:"content_type"`
	Template    string `yaml:"template"     json:"template,omitempty"` // built-in template name, e.g. "basic_audio_offer"
	Content     string `yaml:"content"      json:"content,omitempty"`  // raw body content (alternative to Template)
}

// ImpairSpec holds a step's deliberate network-level impairment, declared
// in its own impair: block. Kept out of fields: (message content) and out
// of the protocol config: block (connection-level settings) because it
// governs HOW a message is delivered, not what it says or which socket
// carries it. Threading it through fields: would put it in front of the
// Diameter AVP builder and the SIP header builder, both of which treat
// every key they see as message content.
//
// Three independent families live here. Timing (LatencyMs/JitterMs/DelayMs)
// are milliseconds, matching the duration_ms/timeout_ms convention the rest
// of the scenario vocabulary already uses, and a step may declare any
// subset of them. Delivery (Drop/Duplicate) and connection-level faults
// (Reset/Truncate/AbruptClose/SCTPAbort) are booleans; at most one of these
// six booleans may be declared per step — the validator enforces it.
type ImpairSpec struct {
	// LatencyMs is a fixed delay applied before the step's message reaches
	// the wire, and once more before the step's first inbound read.
	LatencyMs int `yaml:"latency_ms" json:"latency_ms,omitempty"`
	// JitterMs is an additional uniform random draw in [0, JitterMs] added
	// to LatencyMs, sampled independently for the send and receive sides.
	JitterMs int `yaml:"jitter_ms"  json:"jitter_ms,omitempty"`
	// DelayMs pauses the adapter after a complete response has been received
	// and parsed, before the step acts on it. Unlike LatencyMs this is not
	// bounded by the step's timeout: its purpose is to exercise the peer's
	// own timeout and retry behaviour.
	DelayMs int `yaml:"delay_ms"   json:"delay_ms,omitempty"`
	// Drop, when true, makes the step build its message in full — exercising
	// the encoder, the AVP dictionary and (for Diameter) the handshake — and
	// then deliberately never write it to the wire, and never wait for a
	// response. A dropped step passes: the impairment did exactly what the
	// scenario declared. It reports response.dropped == true.
	Drop bool `yaml:"drop"      json:"drop,omitempty"`
	// Duplicate, when true, writes the step's already-built bytes to the wire
	// twice and then completes exactly one normal receive cycle. A boolean,
	// not a count: the requirement is "the same message arrives at the peer
	// twice", and a configurable count is deliberately out of scope. It
	// reports response.duplicated == true.
	Duplicate bool `yaml:"duplicate" json:"duplicate,omitempty"`
	// Reset, when true, performs a real hard reset of the connection this
	// step is using — SO_LINGER 0 then close, so a genuine TCP RST reaches
	// the peer rather than a graceful FIN — immediately AFTER the step's own
	// message has been sent, and before any response is read. Sending first
	// is what distinguishes it from Drop: the peer really did receive a
	// message and then watched the connection die under it. It reports
	// response.connection_state == "reset".
	Reset bool `yaml:"reset"        json:"reset,omitempty"`
	// Truncate, when true, writes only the first few bytes of the step's
	// already-encoded message to the wire — deliberately too few to frame or
	// parse — instead of the whole message. The byte count is a fixed
	// internal constant, not scenario-configurable. It reports
	// response.connection_state == "truncated".
	Truncate bool `yaml:"truncate"     json:"truncate,omitempty"`
	// AbruptClose, when true, closes the connection immediately after the
	// step's message has been sent and before any response is read, with no
	// clean shutdown (no TLS close_notify, no lingering FIN wait). It
	// differs from Reset only in the wire signal — FIN rather than RST. It
	// reports response.connection_state == "closed".
	AbruptClose bool `yaml:"abrupt_close" json:"abrupt_close,omitempty"`
	// SCTPAbort, when true, sends a real RFC 4960 ABORT chunk on the step's
	// SCTP association after the step's message has been sent — not the
	// graceful SCTP SHUTDOWN a bare close produces. Valid only on a step
	// whose protocol runs over SCTP; cellrift validate rejects it otherwise.
	// It reports response.connection_state == "aborted".
	SCTPAbort bool `yaml:"sctp_abort"   json:"sctp_abort,omitempty"`
}

// MalformSpec holds a step's deliberate, post-encode message corruption,
// declared in its own malform: block — kept out of fields: (real protocol
// content) and out of impair: (timing and connection faults) because it
// governs what bytes actually reach the wire after the real encoder has
// already produced correct ones, not what the step's message says or how
// its connection behaves. Corruption is applied to the real encoder's
// output; no parallel encoder exists anywhere in this codebase, and none is
// ever added for this purpose.
//
// Two independent families live here, gated by protocol: AVPLength and
// MsgLength are Diameter-only (they corrupt the 3-byte AVP Length and
// Message Length header fields respectively), and SIPHeader is SIP-only.
// The validator enforces that gate; nothing here does.
type MalformSpec struct {
	// AVPLength overrides one AVP's already-correctly-encoded Length field
	// with a declared, likely-wrong value.
	AVPLength *AVPLengthMalform `yaml:"avp_length" json:"avp_length,omitempty"`
	// MsgLength overrides the message's already-correctly-encoded Message
	// Length header field with a declared, likely-wrong value. A *int,
	// deliberately: msg_length: 0 is a legitimate, deliberately-wrong
	// declaration and must be distinguishable from "not declared" — a bare
	// int could never tell the two apart.
	MsgLength *int `yaml:"msg_length" json:"msg_length,omitempty"`
	// SIPHeader corrupts one already-serialised SIP header line.
	SIPHeader *SIPHeaderMalform `yaml:"sip_header" json:"sip_header,omitempty"`
}

// AVPLengthMalform names the AVP (by its dictionary name, resolved to a
// code) whose encoded Length field should be overwritten, and the value to
// write in its place.
type AVPLengthMalform struct {
	Name  string `yaml:"name"  json:"name"`
	Value int    `yaml:"value" json:"value"`
}

// SIPHeaderMalform names the header and operation (missing/duplicate/
// overwrite) to apply to an already-serialised SIP message. Value is
// required for overwrite, ignored for missing, and used as the appended
// duplicate line's value for duplicate.
type SIPHeaderMalform struct {
	Name  string `yaml:"name"  json:"name"`
	Op    string `yaml:"op"    json:"op"`
	Value string `yaml:"value" json:"value,omitempty"`
}

// RawSpec is the byte source for an action: raw step. Exactly one of Hex and
// Ref is set. Hex is a literal hand-written payload, decoded once by the
// engine; Ref is a {{ }} template reference — typically to a prior step's
// response.__raw capture — resolved by the same engine machinery every
// other templated step field uses.
type RawSpec struct {
	Hex string `yaml:"hex" json:"hex,omitempty"`
	Ref string `yaml:"ref" json:"ref,omitempty"`
}

// Step is a single action within a scenario.
type Step struct {
	ID        string            `yaml:"id"         json:"id"`
	Protocol  string            `yaml:"protocol"   json:"protocol"`
	Action    string            `yaml:"action"     json:"action"`
	Target    string            `yaml:"target"     json:"target,omitempty"`
	Message   string            `yaml:"message"    json:"message,omitempty"`
	Fields    StepFields        `yaml:"fields"     json:"fields,omitempty"`
	Body      *StepBody         `yaml:"body"       json:"body,omitempty"`
	Impair    *ImpairSpec       `yaml:"impair"     json:"impair,omitempty"`
	Malform   *MalformSpec      `yaml:"malform"    json:"malform,omitempty"`
	Raw       *RawSpec          `yaml:"raw"        json:"raw,omitempty"`
	Capture   map[string]string `yaml:"capture"    json:"capture,omitempty"`
	Assert    []string          `yaml:"assert"     json:"assert,omitempty"`
	Check     string            `yaml:"check"      json:"check,omitempty"`
	TimeoutMs int               `yaml:"timeout_ms" json:"timeout_ms,omitempty"`
	// Duration is the pause length in milliseconds for action: wait. The
	// YAML key carries the unit; the field name does not, matching the
	// scenario vocabulary rather than the Go field naming of TimeoutMs.
	Duration int `yaml:"duration_ms" json:"duration_ms,omitempty"`
	// SendOrder is the position in the actual wire-send sequence for a
	// sub-step of an action: reorder group, counted from 1 and distinct from
	// the sub-step's position in the YAML list — declaring them in one order
	// and sending them in another is the entire point. Meaningful ONLY
	// inside a reorder group, exactly as Duration is meaningful only for
	// action: wait, and the validator rejects it anywhere else.
	SendOrder int    `yaml:"send_order" json:"send_order,omitempty"`
	Steps     []Step `yaml:"steps"      json:"steps,omitempty"` // Sub-steps for action: parallel or reorder
}
