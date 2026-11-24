package sip

import (
	"fmt"
	"strings"
	"sync"
)

// DialogState represents the current state of a SIP dialog.
type DialogState string

const (
	StateInitial     DialogState = "initial"
	StateTrying      DialogState = "trying"
	StateRinging     DialogState = "ringing"
	StateEstablished DialogState = "established"
	StateTerminated  DialogState = "terminated"
	// StateRegistered is the state a 2xx to a REGISTER produces. RFC 3261
	// §10 REGISTER creates no dialog, but this tracker keys on Call-ID and
	// a scenario's `assert_state: check: registered` needs somewhere to look;
	// keeping it distinct from StateEstablished also stops EstablishedDialog
	// — which picks the Call-ID an in-dialog BYE or ACK reuses — from ever
	// choosing a registration instead of the call.
	StateRegistered DialogState = "registered"
)

// Dialog tracks the state of a SIP dialog identified by Call-ID + From-tag + To-tag.
type Dialog struct {
	State   DialogState
	CallID  string
	FromTag string
	ToTag   string
	CSeq    int // last CSeq number used; incremented for each new in-dialog request
}

// transitionTable maps each DialogState to the set of states it may move into.
type transitionTable map[DialogState][]DialogState

// validTransitions defines the allowed state machine transitions for a SIP dialog.
//
// The two provisional states may each move to either provisional state,
// including to themselves. RFC 3261 §13.2.2.1 lets a UAC receive any number
// of provisional responses, and real flows depend on it: a retransmitted
// 180, or the canonical VoLTE sequence 100 Trying → 183 Session Progress →
// 180 Ringing → 200 OK (183 maps to StateTrying here, so that flow is
// trying→trying then trying→ringing). Before this, a second provisional of
// the same class was rejected as "invalid dialog transition from ringing to
// ringing", failing the step on a perfectly ordinary call — undetected only
// because the mock UAS sends exactly one 180 before its 200.
//
// StateRegistered may re-enter itself: a refresh REGISTER, or a
// de-registration (Expires: 0), is answered 2xx on the same Call-ID and the
// tracker cannot see the request's Expires to tell them apart.
var validTransitions = transitionTable{
	StateInitial:     {StateTrying, StateRinging, StateEstablished, StateRegistered, StateTerminated},
	StateTrying:      {StateTrying, StateRinging, StateEstablished, StateRegistered, StateTerminated},
	StateRinging:     {StateTrying, StateRinging, StateEstablished, StateTerminated},
	StateEstablished: {StateTerminated},
	StateRegistered:  {StateRegistered, StateTerminated},
	StateTerminated:  {},
}

// Transition moves the dialog to a new state, returning an error if the transition is invalid.
func (d *Dialog) Transition(newState DialogState) error {
	allowed := validTransitions[d.State]
	for _, s := range allowed {
		if s == newState {
			d.State = newState
			return nil
		}
	}
	return fmt.Errorf("invalid dialog transition from %s to %s", d.State, newState)
}

// dialogKey returns the lookup key for a dialog.
func dialogKey(callID, fromTag, toTag string) string {
	return callID + "|" + fromTag + "|" + toTag
}

// dialogRegistry maps dialog keys (callID|fromTag|toTag) to their Dialog instances.
type dialogRegistry map[string]*Dialog

// DialogManager tracks multiple SIP dialogs.
type DialogManager struct {
	mu      sync.RWMutex
	dialogs dialogRegistry
}

// NewDialogManager creates a new DialogManager.
func NewDialogManager() *DialogManager {
	return &DialogManager{
		dialogs: make(dialogRegistry),
	}
}

// GetOrCreate returns an existing dialog or creates a new one in Initial state.
func (dm *DialogManager) GetOrCreate(callID, fromTag, toTag string) *Dialog {
	key := dialogKey(callID, fromTag, toTag)
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if d, ok := dm.dialogs[key]; ok {
		return d
	}
	d := &Dialog{
		State:   StateInitial,
		CallID:  callID,
		FromTag: fromTag,
		ToTag:   toTag,
	}
	dm.dialogs[key] = d
	return d
}

// Lookup finds an existing dialog. Returns nil if not found.
// If an exact match (callID+fromTag+toTag) is not found, falls back to callID+fromTag
// with empty toTag (early dialog before remote tag is known).
func (dm *DialogManager) Lookup(callID, fromTag, toTag string) *Dialog {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	key := dialogKey(callID, fromTag, toTag)
	if d, ok := dm.dialogs[key]; ok {
		return d
	}
	// Fallback: match early dialog with empty toTag.
	if toTag != "" {
		earlyKey := dialogKey(callID, fromTag, "")
		return dm.dialogs[earlyKey]
	}
	return nil
}

// UpdateFromResponse updates dialog state based on a SIP response.
//
// The lookup, optional re-keying (early dialog promotion), and state
// transition are performed under a single write lock to prevent races
// under parallel step execution.
func (dm *DialogManager) UpdateFromResponse(msg *SIPMessage) error {
	callID := msg.Headers[HeaderCallID]
	fromTag := extractTag(msg.Headers[HeaderFrom])
	toTag := extractTag(msg.Headers[HeaderTo])

	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Look up exact match first, then fall back to early dialog (no to-tag).
	key := dialogKey(callID, fromTag, toTag)
	d, ok := dm.dialogs[key]
	if !ok && toTag != "" {
		earlyKey := dialogKey(callID, fromTag, "")
		d = dm.dialogs[earlyKey]
		// Promote early dialog: update the to-tag and re-key.
		if d != nil && d.ToTag == "" {
			delete(dm.dialogs, earlyKey)
			d.ToTag = toTag
			dm.dialogs[key] = d
		}
	}
	if d == nil {
		d = &Dialog{
			State:   StateInitial,
			CallID:  callID,
			FromTag: fromTag,
			ToTag:   toTag,
			CSeq:    1, // assume dialog was established with CSeq 1 (INVITE)
		}
		dm.dialogs[dialogKey(callID, fromTag, toTag)] = d
	}

	// Determine the method from the CSeq header (e.g. "2 BYE" → "BYE").
	cseqMethod := ""
	if cseq := msg.Headers[HeaderCSeq]; cseq != "" {
		parts := strings.Fields(cseq)
		if len(parts) == 2 {
			cseqMethod = parts[1]
		}
	}

	var newState DialogState
	switch {
	case msg.StatusCode >= 100 && msg.StatusCode < 200 && msg.StatusCode != 180:
		newState = StateTrying
	case msg.StatusCode == 180:
		newState = StateRinging
	case msg.StatusCode >= 200 && msg.StatusCode < 300:
		newState = successState(cseqMethod)
	case msg.StatusCode >= 300:
		newState = StateTerminated
	default:
		return nil
	}

	return d.Transition(newState)
}

// successState maps the CSeq method a 2xx answers to the state it produces:
// BYE and CANCEL end a dialog, REGISTER registers, anything else (INVITE,
// OPTIONS, ...) establishes.
func successState(cseqMethod string) DialogState {
	switch cseqMethod {
	case "BYE", "CANCEL":
		return StateTerminated
	case "REGISTER":
		return StateRegistered
	default:
		return StateEstablished
	}
}

// NextCSeq increments and returns the next CSeq number for the established
// dialog. Used by the adapter to produce a valid in-dialog CSeq on BYE/ACK.
func (dm *DialogManager) NextCSeq() int {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	for _, d := range dm.dialogs {
		if d.State == StateEstablished {
			d.CSeq++
			return d.CSeq
		}
	}
	return 1
}

// EstablishedDialog returns the first dialog in Established state, or nil.
// Used by the adapter to reuse Call-ID and To-tag for in-dialog requests (BYE, ACK).
func (dm *DialogManager) EstablishedDialog() *Dialog {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, d := range dm.dialogs {
		if d.State == StateEstablished {
			return d
		}
	}
	return nil
}

// HasDialogInState returns true if any tracked dialog is in the given state.
func (dm *DialogManager) HasDialogInState(state DialogState) bool {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, d := range dm.dialogs {
		if d.State == state {
			return true
		}
	}
	return false
}

// extractTag pulls the tag= value from a From or To header.
func extractTag(header string) string {
	for _, part := range strings.Split(header, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "tag=") {
			return strings.TrimPrefix(part, "tag=")
		}
	}
	return ""
}
