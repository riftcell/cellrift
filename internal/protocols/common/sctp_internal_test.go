package common

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSCTPOrFallback_FallsBackWhenSCTPFails proves the automatic-fallback
// branch deterministically, via an injected constructor failure — the real
// constructor never fails on an SCTP-capable machine, so without this seam
// the branch would be untestable exactly where it matters most.
func TestSCTPOrFallback_FallsBackWhenSCTPFails(t *testing.T) {
	tr, warnings, err := sctpOrFallback(":0", func(string) (Transport, error) {
		return nil, errors.New("simulated: no kernel sctp")
	})
	require.NoError(t, err)
	defer tr.Close()

	_, ok := tr.(*TCPTransport)
	assert.True(t, ok, "fallback transport must be *TCPTransport")

	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "sctp unavailable: simulated: no kernel sctp")
}

// TestSCTPOrFallback_WarnsOnce proves the fallback WARN line is emitted at
// most once per process even across repeated fallback calls, while every
// call's own warnings slice still reports the substitution.
func TestSCTPOrFallback_WarnsOnce(t *testing.T) {
	origWriter := warnWriter
	t.Cleanup(func() {
		warnWriter = origWriter
		// Reset to a fresh Once rather than restoring a saved copy: sync.Once
		// must never be copied by value (go vet's copylocks check), and no
		// test depends on warnOnce's prior fired/unfired state.
		warnOnce = sync.Once{}
	})

	var buf bytes.Buffer
	warnWriter = &buf
	warnOnce = sync.Once{}

	failing := func(string) (Transport, error) {
		return nil, errors.New("simulated: no kernel sctp")
	}

	tr1, warnings1, err := sctpOrFallback(":0", failing)
	require.NoError(t, err)
	defer tr1.Close()

	tr2, warnings2, err := sctpOrFallback(":0", failing)
	require.NoError(t, err)
	defer tr2.Close()

	assert.Equal(t, 1, strings.Count(buf.String(), "WARN: sctp unavailable"))
	assert.Len(t, warnings1, 1)
	assert.Len(t, warnings2, 1)
}

// TestDialWithTimeout_ReturnsBeforeSlowDialCompletes proves dialWithTimeout
// bounds the caller's wait even when the underlying dial never returns
// within that bound. It needs no kernel SCTP and no network at all.
func TestDialWithTimeout_ReturnsBeforeSlowDialCompletes(t *testing.T) {
	start := time.Now()
	_, err := dialWithTimeout(50*time.Millisecond, func() (net.Conn, error) {
		time.Sleep(2 * time.Second)
		return nil, nil
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
	assert.Less(t, elapsed, 500*time.Millisecond)
}

// TestDialWithTimeout_ClosesLateConnection proves the reaper goroutine
// closes a connection that arrives after the timeout has already fired,
// rather than leaking it open.
func TestDialWithTimeout_ClosesLateConnection(t *testing.T) {
	serverEnd, clientEnd := net.Pipe()

	_, err := dialWithTimeout(10*time.Millisecond, func() (net.Conn, error) {
		time.Sleep(100 * time.Millisecond)
		return clientEnd, nil
	})
	require.Error(t, err)

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, readErr := serverEnd.Read(buf)
		done <- readErr
	}()

	select {
	case readErr := <-done:
		assert.ErrorIs(t, readErr, io.EOF,
			"the reaper must close the late-arriving connection, not leak it open")
	case <-time.After(time.Second):
		t.Fatal("read on the peer end did not observe a close within 1s")
	}
}

// TestReadFromSCTP_TimeoutWrapsBothSentinels proves a readFromSCTP timeout
// satisfies both net.ErrClosed (the reconnect-forcing contract) and
// os.ErrDeadlineExceeded (the timeout classification), and that
// isDeadConnErr/IsTimeoutErr independently agree on that error value. Uses
// net.Pipe() rather than a real SCTP socket: readFromSCTP takes a plain
// net.Conn and the branch under test is the timer branch, so no SCTP kernel
// support is needed.
func TestReadFromSCTP_TimeoutWrapsBothSentinels(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	_, _, err := readFromSCTP(client, 50*time.Millisecond)
	require.Error(t, err)

	assert.True(t, errors.Is(err, net.ErrClosed),
		"reconnect-forcing contract: a timed-out SCTP read must still present as connection loss, got %v", err)
	assert.True(t, errors.Is(err, os.ErrDeadlineExceeded),
		"a genuine peer-silence timeout must also be classifiable as a timeout, got %v", err)
	assert.True(t, isDeadConnErr(err), "the existing connState consumer must still drop the connection")
	assert.True(t, IsTimeoutErr(err), "the new classifier must recognise it")
	assert.Contains(t, err.Error(), "sctp read: timed out after")

	// readFromSCTP closes conn on timeout; a further write must fail.
	_, writeErr := client.Write([]byte("x"))
	assert.Error(t, writeErr, "the timed-out read must have closed the connection")
}

// TestReadFromSCTP_TimeoutTextLeadsWithTheCause pins the rendered text of a
// readFromSCTP timeout. A reader who sees this error in a report must not be
// sent looking for a connection-state fault when the peer simply stayed
// silent - a mistake this repository has already made and corrected in its own
// evidence file - while the dual-sentinel wrapping stays intact.
func TestReadFromSCTP_TimeoutTextLeadsWithTheCause(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()

	_, _, err := readFromSCTP(client, 50*time.Millisecond)
	require.Error(t, err)

	assert.Contains(t, err.Error(), "sctp read: timed out after")
	assert.NotContains(t, err.Error(), "use of closed network connection",
		"the defect being fixed: a peer-silence timeout must not read as a connection-state fault")
	assert.True(t, errors.Is(err, net.ErrClosed), "the dual contract survives the rewording")
	assert.True(t, errors.Is(err, os.ErrDeadlineExceeded), "the dual contract survives the rewording")

	// Every caller concatenates the error like this, so the wrapped text is
	// what a user actually reads.
	wrapped := fmt.Errorf("receive timeout: %w", err)
	assert.True(t, errors.Is(wrapped, net.ErrClosed))
	assert.True(t, errors.Is(wrapped, os.ErrDeadlineExceeded))
	assert.NotContains(t, wrapped.Error(), "use of closed network connection")

	// What is NOT claimed: an As-style search for an interface with a
	// Timeout() bool method still succeeds, via the wrapped
	// os.ErrDeadlineExceeded, exactly as it did for the %w: %w error this
	// replaced. The narrow property is that the outer value is not itself a
	// net.Error.
	_, isNetErr := err.(net.Error)
	assert.False(t, isNetErr,
		"this value must not satisfy net.Error: a caller doing `if ne, ok := err.(net.Error); ok && ne.Timeout()` must not retry - readFromSCTP has already closed the conn")
	_, wrappedIsNetErr := wrapped.(net.Error)
	assert.False(t, wrappedIsNetErr,
		"the fmt.Errorf wrap a caller actually reports must not satisfy net.Error either")
}
