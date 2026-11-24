package common

// StepStatus represents the outcome of a single step execution.
type StepStatus string

const (
	StatusPassed StepStatus = "passed"
	StatusFailed StepStatus = "failed"
)

// ResponseFields holds the parsed protocol response for a step.
// Keys are protocol-specific field names (e.g. "status_code", "avp.Result-Code");
// values are strings, ints, or nested maps depending on the field.
// Used by the engine for capture expressions and assertion evaluation.
type ResponseFields map[string]interface{}

// AdapterMap maps protocol names (e.g. "sip", "diameter") to the adapter
// that handles steps for that protocol. Populated once per scenario run and
// passed read-only into the engine.
type AdapterMap map[string]ProtocolAdapter

// StepResult holds the outcome of executing a single protocol step.
type StepResult struct {
	Status      StepStatus
	DurationMs  int64
	Response    ResponseFields // Parsed response fields available for capture/assertion.
	RawResponse []byte         // Raw protocol message bytes.
	Error       string
}

// ProtocolAdapter is the interface each protocol engine must implement.
type ProtocolAdapter interface {
	// Execute runs a single step and returns the result.
	// fields contains the resolved step fields (template variables already substituted).
	// config holds the protocol-level configuration from the scenario.
	Execute(action, target, message string, fields map[string]string, config map[string]string) (*StepResult, error)

	// Close releases resources held by the adapter.
	Close() error
}

// ReorderMember is one sub-step of a reorder group, already resolved by the
// engine exactly as an ordinary send step would be. SendOrder — not the
// member's position in the slice — decides the order its bytes reach the
// wire. Config carries that sub-step's own impair: block, so latency,
// jitter, drop and duplicate are per-member, not per-group.
type ReorderMember struct {
	Target    string
	Message   string
	Fields    map[string]string
	Config    map[string]string
	SendOrder int
}

// ReorderMembers is one reorder group's members in DECLARATION order — the
// order the scenario lists them, which is also the order results come back
// in. Named because that ordering contract is the whole point of the type:
// a bare []ReorderMember at a signature would not say which of the two
// orderings (declaration or SendOrder) the slice is in.
type ReorderMembers []ReorderMember

// ReorderSender is implemented by a protocol adapter that can execute a
// batch of send sub-steps whose wire order may differ from their
// declaration order:
//
//  1. build every member's complete wire message first, in declaration
//     order, aborting the whole group if any one fails to build;
//  2. write those already-built messages one at a time, in ascending
//     SendOrder, from the SAME goroutine — reordering the write order of
//     complete messages needs no concurrency, no second reader and no
//     producer/consumer queue;
//  3. read the responses and correlate each back to its originating member.
//
// Results are returned in DECLARATION order — the same indices as members —
// never in SendOrder, so a scenario author's capture:/assert: on a specific
// declared sub-step resolves correctly regardless of physical send timing.
// Members are whole framed messages throughout; nothing here manipulates a
// byte range smaller than one complete message.
//
// timeoutMs is the reorder step's own declared timeout governing the whole
// group's wait, or 0 to mean "use the adapter's default".
//
// Optional by design, exactly like Connector and ImpairmentControl: callers
// type-assert and treat a non-implementing adapter as a defined, reported
// case, never a panic.
type ReorderSender interface {
	ExecuteReorder(members ReorderMembers, timeoutMs int) ([]*StepResult, error)
}
