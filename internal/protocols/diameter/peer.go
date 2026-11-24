package diameter

import (
	"fmt"
	"sync"
)

// peer.go implements the client-only subset of RFC 6733 §5.6's peer state
// machine. It is pure logic — no socket, no message parsing — so the state
// machine is provable without a mock peer, matching handshake.go's buildCER in that
// respect. The adapter drives it from handshake.go and adapter.go.
//
// Why the responder ("R-"prefixed) states are absent: §5.6.4 states flatly,
// "The election is performed on the responder" (RFC 6733 line 4097).
// Election exists only because a full Diameter node can simultaneously
// accept inbound connections and initiate outbound ones, creating the
// possibility that two peers connect to each other at once. Cellrift never
// listens for inbound Diameter connections, so it can never generate or
// receive an R-Conn-CER event, and every state/action that mentions election
// — Wait-Conn-Ack/Elect, Wait-Returns, Win-Election, R-Accept, R-Reject,
// R-Open — is unreachable by construction, not an oversight. This is a
// deliberate decision, not an omission a future reader should "fix" by
// adding them back.
//
// Wait-Conn-Ack itself also does not appear as a distinct state: §5.6
// explicitly permits state-machine simplification ("Any implementation that
// produces equivalent results is considered compliant"), and Cellrift's
// connState.connect (internal/protocols/common/connstate.go) dials
// synchronously — by the time connect returns without error, the transport
// connection is already acknowledged, so there is no observable interval in
// which Cellrift is "waiting for a connection ack" as a state a caller could
// query. The CER is sent immediately after a successful connect, which is
// equivalent to collapsing Wait-Conn-Ack into peerClosed -> peerWaitCEA.
//
// Why a state field with guard helpers, not a
// map[peerState]map[peerEvent]peerState transition table: CLAUDE.md's
// table-driven rule targets switches where every case performs the SAME
// structural operation (e.g. field name -> AVP code). The three transitions
// here each carry a DISTINCT side effect — sending the CER's bytes, parsing
// and validating the CEA, and populating the confirmed application set — so
// a table would need a func value per entry, which is a table in name only
// and adds indirection without adding clarity. This comment records the
// choice made and why, so it reads as decided rather than defaulted.
type peerState int

const (
	// peerClosed is the zero value: no CER sent yet. Matches connState's
	// usable-zero-value style — no constructor required.
	peerClosed peerState = iota
	// peerWaitCEA: CER sent, awaiting CEA.
	peerWaitCEA
	// peerOpen: 2001 CEA received, applications confirmed.
	peerOpen
)

// peerStateNames maps a peerState to its human name, used by requireState's
// error so a mismatch reads as "want Open, have Wait-I-CEA" rather than
// "want 2, have 1".
type peerStateNames map[peerState]string

var peerStateNameTable = peerStateNames{
	peerClosed:  "Closed",
	peerWaitCEA: "Wait-I-CEA",
	peerOpen:    "Open",
}

// String returns the human name for s, or a non-panicking fallback for an
// out-of-range value.
func (s peerState) String() string {
	if name, ok := peerStateNameTable[s]; ok {
		return name
	}
	return fmt.Sprintf("peerState(%d)", int(s))
}

// peer tracks a client-only Diameter connection's handshake state and, once
// Open, the Application-Ids the peer's CEA confirmed it supports.
type peer struct {
	mu    sync.Mutex
	state peerState
	// apps is the Application-Id set the CEA confirmed the peer supports —
	// consulted by supports, which the post-CEA unsupported-application gate calls.
	apps []uint32
}

// requireState returns an error naming both the expected and the actual
// state in words (not integers) if p is not currently in state want.
func (p *peer) requireState(want peerState) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state != want {
		return fmt.Errorf("peer: expected state %s, have %s", want, p.state)
	}
	return nil
}

// setState transitions p to s. It performs no validation of the FROM state:
// the caller (the handshake driver) is responsible for
// only calling setState after the side effect that earns the transition —
// e.g. setState(peerWaitCEA) only after the CER has actually been sent.
func (p *peer) setState(s peerState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = s
}

// setApps records the Application-Ids the CEA confirmed the peer supports.
func (p *peer) setApps(apps []uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.apps = apps
}

// supports reports whether the peer's CEA confirmed appID. Returning false
// on an empty/unset apps slice is correct and intentional: before a CEA has
// been processed there is nothing to support.
func (p *peer) supports(appID uint32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.apps {
		if a == appID {
			return true
		}
	}
	return false
}
