//go:build linux && !386

// This file carries the only SCTP abort implementation that can actually run.
//
// The build tag deliberately mirrors github.com/ishidawataru/sctp's own
// sctp_linux.go (`//go:build linux && !386`), because SCTPConn.SyscallConn is
// declared there and nowhere else. Every other target — any non-Linux GOOS,
// plus linux/386 — is served by that library's sctp_unsupported.go, whose
// entry points all return ErrUnsupported, so no association is ever
// established and nothing can be aborted. Matching the tag exactly is what
// keeps `GOOS=darwin go build` and `GOOS=windows go build` compiling; a
// plain `linux` tag would still break on linux/386.

package common

import (
	"fmt"
	"syscall"

	"github.com/ishidawataru/sctp"
)

// armSCTPAbort makes the next close of sc tear the association down with an
// RFC 4960 ABORT instead of a graceful SHUTDOWN, by setting SO_LINGER to
// zero — the mechanism sctp(7) documents, and the same one resetHeldConn
// uses for a TCP RST.
//
// This replaced an earlier attempt that wrote SndRcvInfo{Flags: SCTP_ABORT}
// through the library's cmsg path. That call returned EINVAL on every kernel
// it was tried on, and because the test asserted only the stamped
// connection_state and never the error, the feature was dead while its test
// stayed green all along. Assert on the error, not just the
// outcome field.
func armSCTPAbort(sc *sctp.SCTPConn) error {
	raw, err := sc.SyscallConn()
	if err != nil {
		return fmt.Errorf("sctp abort: raw conn: %w", err)
	}
	var optErr error
	ctlErr := raw.Control(func(fd uintptr) {
		optErr = syscall.SetsockoptLinger(int(fd), syscall.SOL_SOCKET, syscall.SO_LINGER,
			&syscall.Linger{Onoff: 1, Linger: 0})
	})
	if ctlErr != nil {
		return fmt.Errorf("sctp abort: control: %w", ctlErr)
	}
	return optErr
}
