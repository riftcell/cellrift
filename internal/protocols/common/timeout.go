package common

import (
	"errors"
	"os"
)

// IsTimeoutErr reports whether err means a Receive deadline expired — the
// peer never answered within the step's own timeout_ms — as opposed to any
// other receive-side failure (a dead connection, a malformed frame, a
// watchdog-answer failure).
//
// Deliberately not a type assertion against the standard library's Timeout()
// interface method. That method can report true for errors that are not a
// deadline expiry (https://github.com/golang/go/issues/31449), so errors.Is
// against the standard library's own sentinel is the precise check, and it
// keeps working through every wrapping layer an error crosses on its way up
// from the netpoller.
//
// Sibling of isDeadConnErr in connstate.go: the two ask different questions
// of the same error value ("is this connection unusable" vs "did the peer
// simply stay silent"), and for TCP/TLS the answers are deliberately
// mutually exclusive — an expired deadline leaves the connection fully
// usable. SCTP is the one transport where a timeout is ALSO connection loss;
// see readFromSCTP in sctp.go for why, and note that its error satisfies
// both predicates on purpose.
func IsTimeoutErr(err error) bool {
	return errors.Is(err, os.ErrDeadlineExceeded)
}
