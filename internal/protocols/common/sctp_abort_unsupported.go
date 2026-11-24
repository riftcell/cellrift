//go:build !linux || (linux && 386)

// The no-op half of the SCTP abort split. See sctp_abort_linux.go for why the
// tag is shaped this way: it is the exact complement of
// github.com/ishidawataru/sctp's sctp_linux.go, so this file covers every
// target whose SCTPConn has no SyscallConn method.
//
// This exists to keep the package compiling for darwin and windows, which
// `make build`'s cross-compilation step and CI's Non-Linux Build job both
// check. It is not a fallback in any meaningful sense: on these targets the
// vendored library refuses to establish an association at all, so
// SCTPTransport.AbortConn is unreachable and NewSCTPOrFallbackTransport has
// already substituted TCP with a warning long before an abort could be asked
// for.

package common

import (
	"fmt"

	"github.com/ishidawataru/sctp"
)

// armSCTPAbort reports that SO_LINGER cannot be set on this target.
//
// Unreachable in practice. It returns an error rather than nil so that if a
// future refactor ever does reach it, the caller reports a refusal instead of
// silently believing an association was aborted when nothing happened — the
// same class of false green as the EINVAL-returning cmsg write this whole
// mechanism replaced.
func armSCTPAbort(_ *sctp.SCTPConn) error {
	return fmt.Errorf("sctp abort: not supported on this platform (SCTP itself is unavailable here)")
}
