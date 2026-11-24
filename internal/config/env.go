// Package config centralises runtime tunables read from CELLRIFT_* environment
// variables. All time-based variables are suffixed with _MS to make the unit
// unambiguous at the call site and in documentation.
package config

import (
	"os"
	"strconv"
	"time"
)

// SIPPollIntervalMs returns the sleep duration between state checks in the
// assert_state polling loop.
//
// Override: CELLRIFT_SIP_POLL_INTERVAL_MS (integer milliseconds, default 50).
func SIPPollIntervalMs() time.Duration {
	return envDuration("CELLRIFT_SIP_POLL_INTERVAL_MS", 50)
}

// SIPMaxProvisional returns the maximum number of provisional (1xx) SIP
// responses to consume before treating the request as timed out.
//
// Override: CELLRIFT_SIP_MAX_PROVISIONAL (positive integer, default 50).
func SIPMaxProvisional() int {
	return envInt("CELLRIFT_SIP_MAX_PROVISIONAL", 50)
}

// SIPDuplicateDrainMs returns how long a duplicated SIP step spends
// discarding the peer's response to the SECOND copy of its request, after
// it has already consumed the response to the first.
//
// This exists because SIP has no request/response correlation on this path:
// waitForFinalResponse returns whatever Receive hands back next, with no
// Call-ID, CSeq or branch check. An undrained echo would therefore be read
// by the NEXT step and silently attributed to it. The drain is bounded
// rather than exact because the peer may answer the duplicate once, twice,
// or (per RFC 3261 §17 retransmission handling) not at all.
//
// Override: CELLRIFT_SIP_DUPLICATE_DRAIN_MS (integer milliseconds, default 100).
func SIPDuplicateDrainMs() time.Duration {
	return envDuration("CELLRIFT_SIP_DUPLICATE_DRAIN_MS", 100)
}

// SIPPassword returns the ambient SIP digest password, the lowest-precedence
// credential source: it is used only when the scenario declared no password and
// no credentials file supplied one.
//
// It is NOT a _MS tunable and not a count, so it has no suffix and no default.
// It is also distinct from CELLRIFT_VAR_SIP_PASSWORD, which is the generic
// variable-override mechanism (State.LoadEnvOverrides in internal/engine)
// feeding a {{ sip_password }} template. That path is the most explicit layer,
// because the scenario still had to name the template; this one is the
// fallback when it named nothing.
//
// Override: CELLRIFT_SIP_PASSWORD (any non-empty string; empty means unset).
func SIPPassword() string { return os.Getenv("CELLRIFT_SIP_PASSWORD") }

// TCPConnectTimeoutMs returns the dial timeout for outbound TCP connections.
//
// Override: CELLRIFT_TCP_CONNECT_TIMEOUT_MS (integer milliseconds, default 5000).
func TCPConnectTimeoutMs() time.Duration {
	return envDuration("CELLRIFT_TCP_CONNECT_TIMEOUT_MS", 5000)
}

// SCTPConnectTimeoutMs returns the bound on how long an outbound SCTP dial may
// block the caller.
//
// This is deliberately a separate knob from CELLRIFT_TCP_CONNECT_TIMEOUT_MS
// rather than a reuse of it: the SCTP wrapper can only bound the caller's wait,
// not cancel the dial itself, so an operator raising or lowering it is making a
// different trade than for TCP and should not have to move both at once.
//
// Override: CELLRIFT_SCTP_CONNECT_TIMEOUT_MS (integer milliseconds, default 5000).
func SCTPConnectTimeoutMs() time.Duration {
	return envDuration("CELLRIFT_SCTP_CONNECT_TIMEOUT_MS", 5000)
}

// envDuration reads an env var as a positive integer number of milliseconds.
// Returns the default if the variable is absent, empty, non-numeric, or ≤ 0.
func envDuration(name string, defaultMs int) time.Duration {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(defaultMs) * time.Millisecond
}

// envInt reads an env var as a positive integer.
// Returns the default if the variable is absent, empty, non-numeric, or ≤ 0.
func envInt(name string, defaultVal int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultVal
}
