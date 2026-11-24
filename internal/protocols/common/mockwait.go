package common

import (
	"testing"
	"time"
)

// mockwait.go exists because of a whole class of test failure this
// repository shipped and CI caught on the very first push: a test asserting
// on what a mock peer OBSERVED, immediately after the step that sent it.
//
// Every mock peer here reads and records on its own goroutine. A step that
// waits for an answer synchronises the two implicitly — the answer cannot
// exist until the peer has read the request. A step that deliberately does
// NOT wait for an answer (drop, duplicate's second copy, reset, truncate,
// abrupt_close, sctp_abort, action: raw) returns the moment its bytes are
// written, and nothing then orders the peer's read against the assertion
// that follows. On a developer's 16-core machine the peer's goroutine runs
// on another core within microseconds and the assertion always passed; on a
// 2-core CI runner it does not, and four such tests failed on the first
// push. `GOMAXPROCS=1 go test -race ./...` reproduces it every time, which
// is why `make test` now includes that pass.
//
// The fix is to WAIT for the observation, bounded, instead of assuming it.
// The assertion keeps all its force — a request that never arrives still
// fails the test, one second later — while losing an assumption about
// goroutine scheduling that was never true.

// mockObserveTimeout bounds every wait below. It is generous because a
// passing test never spends it: the wait ends the moment the condition
// holds. Only a genuine failure pays the full price.
const mockObserveTimeout = 2 * time.Second

// mockPollInterval is how often WaitUntil re-checks. Deliberately not a
// CELLRIFT_* tunable: this governs test infrastructure, never the behaviour
// of a scenario run.
const mockPollInterval = 200 * time.Microsecond

// WaitUntil blocks until cond returns true, then returns. If cond has not
// become true within mockObserveTimeout it fails t with what, which should
// name the observation being waited for ("the peer to record the UAR"), so
// a genuine failure reads as "the peer never received it" rather than as a
// timeout in an unrelated helper.
//
// cond must be safe to call concurrently with the mock's own goroutines —
// every accessor on the mocks in this repository already is.
func WaitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(mockObserveTimeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", mockObserveTimeout, what)
		}
		time.Sleep(mockPollInterval)
	}
}
