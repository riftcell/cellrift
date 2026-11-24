// Package common provides shared protocol abstractions.
//
// This file implements SCTP transport for Diameter (RFC 4960) with automatic
// TCP fallback when SCTP is unavailable. The fallback activates when the
// CELLRIFT_TRANSPORT_FALLBACK=tcp environment variable is set, or when SCTP
// library initialisation fails at runtime.

package common

import (
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/ishidawataru/sctp"

	"github.com/riftcell/cellrift/internal/config"
)

// SCTPTransport implements Transport over one-to-one SOCK_STREAM SCTP
// associations, which RFC 6733 §2.1 requires for Diameter. It mirrors
// TCPTransport's connected-connection model: Connect establishes one
// association that is then held across many Send/Receive pairs. Unlike
// TCPTransport it has no dial-per-send branch — nothing in this project sends
// Diameter without connecting first, and adding one would invent an untested
// path.
type SCTPTransport struct {
	listener  *sctp.SCTPListener
	localAddr *sctp.SCTPAddr
	conns     connState
}

var _ Connector = (*SCTPTransport)(nil)

// newSCTPTransport is the platform-specific SCTP constructor. On Linux with
// the sctp kernel module loaded it binds a real SCTP listener via
// github.com/ishidawataru/sctp. On unsupported platforms, or a Linux kernel
// without SCTP, ListenSCTP returns an error and the caller falls back to TCP
// — this is the single runtime-detection point in this file.
func newSCTPTransport(localAddr string) (Transport, error) {
	if localAddr == "" {
		localAddr = ":0"
	}
	laddr, err := sctp.ResolveSCTPAddr("sctp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("sctp: resolve local addr %q: %w", localAddr, err)
	}
	ln, err := sctp.ListenSCTP("sctp", laddr)
	if err != nil {
		return nil, fmt.Errorf("sctp: listen %q: %w", localAddr, err)
	}
	return &SCTPTransport{listener: ln, localAddr: laddr}, nil
}

// dialLocalAddr picks the single local IP an outbound association binds to.
//
// Passing a nil (or wildcard) local address to DialSCTPExt is the naive
// translation of net.Dial's "let the kernel choose", and it is wrong here.
// SCTP associations are natively multi-homed: with no explicit bind, the
// connected socket's LocalAddr().String() enumerates EVERY local interface,
// slash-joined, e.g. "127.0.0.1/192.168.0.102/172.17.0.1:38601". The Diameter
// handshake feeds that string through net.SplitHostPort and net.ParseIP to
// build the CER's Host-IP-Address, and both calls fail on it — so the CER
// would fail to build on any machine with more than one interface, which is
// nearly every real machine and every CI runner. Binding one explicit IP is
// what net.Dial does implicitly for TCP and what this library requires doing
// by hand.
//
// Port is deliberately left 0 so the kernel assigns an ephemeral source port:
// reusing the listener's port would collide with the listener itself.
func (t *SCTPTransport) dialLocalAddr(target string) (*sctp.SCTPAddr, error) {
	for _, ip := range t.localAddr.IPAddrs {
		if ip.IP != nil && !ip.IP.IsUnspecified() {
			return &sctp.SCTPAddr{IPAddrs: []net.IPAddr{ip}}, nil
		}
	}

	// The ":0" case, which is what every caller actually passes: ask the
	// kernel which source address it would route to target from. A UDP
	// "dial" performs only a route lookup and sends no packet.
	c, err := net.Dial("udp", target)
	if err != nil {
		return nil, fmt.Errorf("sctp connect: pick local addr for %q: %w", target, err)
	}
	defer c.Close()
	ip := c.LocalAddr().(*net.UDPAddr).IP
	return &sctp.SCTPAddr{IPAddrs: []net.IPAddr{{IP: ip}}}, nil
}

// dialResult carries the outcome of a dial run on its own goroutine, back to
// dialWithTimeout's select.
type dialResult struct {
	conn net.Conn
	err  error
}

// dialWithTimeout bounds how long the CALLER waits for dial — which is all it
// can do. The SCTP library exposes no fd, connection object or context while a
// dial is in flight (socket creation, bind and connect all happen inside one
// blocking call), so there is nothing to close to interrupt it. This is a
// genuinely weaker guarantee than the close-the-connection-to-unblock idiom
// used elsewhere in this package, which only ever applies to reads on an
// already-established connection.
//
// Concretely: on timeout this returns to the caller on time, but the dial
// goroutine stays parked in its syscall until the kernel's own SCTP INIT
// retransmission timeout expires — minutes, not the configured bound. The
// process-lifetime leak is abandoned, not cancelled, and is acceptable for a
// CLI that runs a scenario and exits. Do not "fix" this into something more
// elaborate: the only tighter option the library offers is SyscallConn plus
// manual non-blocking socket options, which is a much larger commitment than
// this bound is worth.
func dialWithTimeout(timeout time.Duration, dial func() (net.Conn, error)) (net.Conn, error) {
	done := make(chan dialResult, 1)
	go func() {
		conn, err := dial()
		done <- dialResult{conn: conn, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-timer.C:
		// The abandoned goroutine may still succeed later; close its
		// connection instead of leaking it open.
		go func() {
			if r := <-done; r.conn != nil {
				r.conn.Close()
			}
		}()
		return nil, fmt.Errorf("timed out after %s", timeout)
	}
}

// sctpReadResult carries the outcome of a Read run on its own goroutine, back
// to readFromSCTP's select.
type sctpReadResult struct {
	data []byte
	addr net.Addr
	err  error
}

// sctpReadTimeoutError is what readFromSCTP returns when the peer stayed
// silent for the whole read budget.
//
// The condition is a timeout first. The association is closed afterwards, by
// this client, to stop the abandoned Read goroutine from stealing a later
// message off the same connection; the text says so in that order instead of
// leading with a connection-state phrase that reads as though the connection
// was already gone when the peer merely said nothing.
//
// It unwraps to BOTH sentinels because two callers ask it two different
// questions: isDeadConnErr asks "is this connection gone" (net.ErrClosed) and
// the adapters ask "did the peer stay silent" (os.ErrDeadlineExceeded).
//
// It deliberately has no Timeout() or Temporary() method, so the type itself
// is not a net.Error: a caller writing the usual
// `if ne, ok := err.(net.Error); ok && ne.Timeout() { retry }` does not take
// the retry branch, and an SCTP read timeout must not be retried on the same
// connection because readFromSCTP has already closed it. That claim is
// narrow. Because the error unwraps os.ErrDeadlineExceeded, an errors.As
// consumer looking for a Timeout() method still reaches one there; that is
// unchanged from the %w: %w error this type replaced, and removing it would
// mean dropping a sentinel, which the dual contract forbids.
type sctpReadTimeoutError struct{ after time.Duration }

func (e *sctpReadTimeoutError) Error() string {
	return fmt.Sprintf("sctp read: timed out after %s: no data from peer (association then closed by this client)", e.after)
}

func (e *sctpReadTimeoutError) Unwrap() []error {
	return []error{net.ErrClosed, os.ErrDeadlineExceeded}
}

// readFromSCTP reads one message from an SCTP-backed conn within timeout.
//
// It cannot use readFrom's conn.SetReadDeadline: the vendored
// github.com/ishidawataru/sctp library's SCTPConn.SetDeadline,
// SetReadDeadline and SetWriteDeadline are permanent stubs that
// unconditionally return syscall.EOPNOTSUPP — SCTPConn reads via a raw fd in
// blocking mode, bypassing Go's runtime netpoller (and therefore its deadline
// machinery) entirely. This is a genuine library limitation, not an
// environment quirk.
//
// Instead this races Read against a timer, the same abandon-not-cancel shape
// as dialWithTimeout. But an abandoned Read is a WORSE leak than an abandoned
// dial: it stays parked on the SAME live connection a caller may Read from
// again, so a later legitimate Receive could race the abandoned goroutine for
// the next message on the wire — silent data loss or misdelivery, not just a
// leaked goroutine. To make that impossible, a timed-out read closes conn
// outright and returns a *sctpReadTimeoutError, which unwraps to
// net.ErrClosed so isDeadConnErr forces a reconnect. This makes SCTPTransport's Receive timeout guarantee weaker than
// TCP/TLS's: os.ErrDeadlineExceeded there leaves the connection usable, a
// timeout here does not. Callers must treat an SCTP receive timeout as
// connection loss, not a retryable one.
//
// The returned *sctpReadTimeoutError names peer silence as the condition in
// its text and unwraps to BOTH net.ErrClosed and os.ErrDeadlineExceeded, so
// the two questions a caller can ask of it get separately correct answers:
// isDeadConnErr still says "this connection is gone, reconnect" (the
// behaviour above depends on that and must not change), and IsTimeoutErr
// still says "the peer stayed silent". Without the second sentinel a genuine
// SCTP peer-silence timeout would be indistinguishable from a connection that
// died for an unrelated reason, and a step over SCTP would report
// response.timed_out == false while the peer was in fact simply silent.
func readFromSCTP(conn net.Conn, timeout time.Duration) ([]byte, net.Addr, error) {
	done := make(chan sctpReadResult, 1)
	go func() {
		buf := make([]byte, maxMessageSize)
		n, err := conn.Read(buf)
		if err != nil {
			done <- sctpReadResult{err: err}
			return
		}
		done <- sctpReadResult{data: buf[:n], addr: conn.RemoteAddr()}
	}()

	// A stoppable timer, not time.After: this runs once per physical read,
	// and receiveFromHeldConn loops here while assembling a chunked answer,
	// so time.After would leave one full-timeout timer alive per read until
	// it fired — memory only, but unbounded across a long run.
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.data, r.addr, r.err
	case <-timer.C:
		conn.Close()
		return nil, nil, &sctpReadTimeoutError{after: timeout}
	}
}

// Connect dials addr and holds the resulting association for subsequent
// Send/Receive calls, bounded by config.SCTPConnectTimeoutMs().
func (t *SCTPTransport) Connect(addr string) error {
	laddr, err := t.dialLocalAddr(addr)
	if err != nil {
		return err
	}
	raddr, err := sctp.ResolveSCTPAddr("sctp", addr)
	if err != nil {
		return fmt.Errorf("sctp connect: resolve %q: %w", addr, err)
	}

	err = t.conns.connect(addr, func(a string) (net.Conn, error) {
		return dialWithTimeout(config.SCTPConnectTimeoutMs(), func() (net.Conn, error) {
			// A zero InitMsg is correct: this library's only socket type is
			// one-to-one SOCK_STREAM, so there is no SOCK_SEQPACKET to select
			// by accident.
			return sctp.DialSCTPExt("sctp", laddr, raddr, sctp.InitMsg{})
		})
	})
	if err != nil {
		return fmt.Errorf("sctp connect %q: %w", addr, err)
	}
	return nil
}

func (t *SCTPTransport) Send(msg []byte, addr string) error {
	conn, connectedAddr := t.conns.snapshot()
	if connectedAddr == "" {
		return fmt.Errorf("sctp send: not connected, call Connect first")
	}
	return sendOnHeldConn("sctp", conn, connectedAddr, msg, addr)
}

func (t *SCTPTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	conn, connectedAddr := t.conns.snapshot()
	if connectedAddr != "" {
		return receiveFromHeldConn("sctp", &t.conns, conn, connectedAddr, timeout, readFromSCTP)
	}

	// No held connection — accept one inbound association, mirroring
	// TCPTransport.Receive's listener branch. Unlike TCPTransport,
	// *sctp.SCTPListener exposes no Accept deadline at all (not even the
	// EOPNOTSUPP stub SCTPConn has) — an inbound accept here is genuinely
	// unbounded. This is untested and unused in practice: nothing in this
	// project accepts inbound SCTP without the peer having dialed first
	// (Diameter is client-only), the same reasoning TLSTransport's Receive
	// already documents for its own un-connected branch.
	inbound, err := t.listener.Accept()
	if err != nil {
		return nil, nil, err
	}
	defer inbound.Close()
	return readFromSCTP(inbound, timeout)
}

func (t *SCTPTransport) Close() error {
	conn := t.conns.takeAll()
	if conn != nil {
		conn.Close()
	}
	return t.listener.Close()
}

// LocalAddr returns the address of the currently active socket. The
// connected branch is what the Diameter CER's Host-IP-Address depends on,
// and it is single-address only because dialLocalAddr bound one explicit IP.
func (t *SCTPTransport) LocalAddr() net.Addr {
	if conn, addr := t.conns.snapshot(); addr != "" && conn != nil {
		return conn.LocalAddr()
	}
	return t.listener.Addr()
}

// sctpConnOf unwraps the held connection down to the concrete type the
// vendored library's lower-level API needs. *sctp.SCTPConn is the only type
// this transport's connState is ever populated with — Connect's dial builds
// it via sctp.DialSCTPExt — so a failure here is an internal inconsistency
// worth naming rather than a user-facing condition.
func sctpConnOf(conn net.Conn) (*sctp.SCTPConn, bool) {
	sc, ok := conn.(*sctp.SCTPConn)
	return sc, ok
}

// ResetConn reports that SCTP has no TCP RST equivalent, and deliberately
// does nothing else.
//
// SO_LINGER-0-then-close is a TCP mechanism for making the kernel emit an
// RST. SCTP's nearest concept is the RFC 4960 ABORT chunk, which this
// transport exposes as its own scenario key (impair: { sctp_abort: true }).
// Quietly downgrading reset to a plain close here would make
// `impair: { reset: true }` on an SCTP step test something other than what
// it says — the same class of silent substitution sctp_abort exists to keep
// distinguishable.
func (t *SCTPTransport) ResetConn() error {
	return fmt.Errorf("sctp reset: SCTP has no TCP RST equivalent; use impair: { sctp_abort: true } for a real RFC 4960 ABORT: %w", ErrConnFaultNotAttempted)
}

// AbruptCloseConn closes the association immediately and clears this
// transport's bookkeeping.
//
// On the wire this is the vendored library's graceful SCTP_EOF shutdown, not
// an abort — "abrupt" here refers to the APPLICATION-level timing: the close
// lands immediately after the step's Send and before any response is read,
// which is the timing this fault type is defined to have. A peer-visible
// hard abort is AbortConn's job, and the two are deliberately separate keys.
//
// dropConn is called directly rather than left to the next Receive's
// isDeadConnErr check: a closed *sctp.SCTPConn's next I/O returns a raw
// syscall.EBADF (the library swaps its fd to -1), which isDeadConnErr does
// not match, so nothing else would ever clear this association.
func (t *SCTPTransport) AbruptCloseConn() error {
	conn, _ := t.conns.snapshot()
	if conn == nil {
		return errNoHeldConn("sctp", "abrupt close")
	}
	err := conn.Close()
	t.conns.dropConn()
	return err
}

// AbortConn tears the held association down with a real RFC 4960 ABORT,
// then releases the local fd and clears this transport's bookkeeping.
//
// A bare SCTPConn.Close() is NOT an abort: the vendored library's Close
// writes SndRcvInfo{Flags: SCTP_EOF} — a graceful SHUTDOWN — before closing
// the fd, and a peer observing that cannot distinguish it from an ordinary
// teardown.
//
// The abort itself is armed by armSCTPAbort, which is platform-split: the
// vendored library exposes SyscallConn only on linux && !386, and returns
// ErrUnsupported from every entry point elsewhere, so SCTP never establishes
// an association on those targets and this method cannot be reached there.
// See sctp_abort_linux.go and sctp_abort_unsupported.go.
func (t *SCTPTransport) AbortConn() error {
	conn, _ := t.conns.snapshot()
	if conn == nil {
		return errNoHeldConn("sctp", "abort")
	}
	sc, ok := sctpConnOf(conn)
	if !ok {
		return fmt.Errorf("sctp abort: expected *sctp.SCTPConn, got %T", conn)
	}
	abortErr := armSCTPAbort(sc)
	// Close afterwards so the local fd is released. Its error is discarded
	// on purpose: the association is already gone, so the SCTP_EOF write
	// Close performs first is EXPECTED to fail, and that failure says
	// nothing about whether the abort itself reached the peer.
	_ = sc.Close()
	t.conns.dropConn()
	return abortErr
}

// SendTruncated writes the first n bytes of msg on the held association and
// then tears it down. It mirrors Send exactly — this transport has no
// dial-per-send branch, by the same deliberate design Send's own comment
// records — with two changes: fewer bytes, and a teardown afterwards.
func (t *SCTPTransport) SendTruncated(msg []byte, addr string, n int) error {
	conn, connectedAddr := t.conns.snapshot()
	if connectedAddr == "" {
		return fmt.Errorf("sctp send: not connected, call Connect first")
	}
	if err := sendOnHeldConn("sctp", conn, connectedAddr, msg[:clampTruncation(msg, n)], addr); err != nil {
		return err
	}
	return closeAndDrop(&t.conns)
}

var (
	_ ConnFaultInjector = (*SCTPTransport)(nil)
	_ TruncatingSender  = (*SCTPTransport)(nil)
)

// warnWriter is the sink for the fallback WARN line. It is a variable purely so
// an in-package test can capture the output; production always writes to
// stderr, matching how the CLI reports every other non-fatal condition.
var warnWriter io.Writer = os.Stderr

// warnOnce keeps the fallback WARN to a single line per process. Running many
// scenarios in one invocation would otherwise repeat an identical line per
// scenario. The per-run warnings slice is deliberately NOT once-guarded: every
// run result must state its own transport, even when only the first one logs.
var warnOnce sync.Once

// sctpConstructor builds a real SCTP transport. It is a parameter rather than a
// direct call so a test can exercise the fallback branch deterministically:
// on an SCTP-capable machine the real constructor never fails, so without this
// seam the fallback path would be untestable exactly where it matters most.
type sctpConstructor func(localAddr string) (Transport, error)

// NewSCTPOrFallbackTransport creates an SCTP transport if available, otherwise
// falls back to TCP.
//
// warnings is nil when real SCTP was used, and holds exactly one line when TCP
// was substituted. It exists because a silent transport substitution is
// invisible to the JSON, JUnit and terminal reporters, which take the run
// result — not stderr — as their source of truth. A caller that discards it is
// choosing to hide from its users which transport actually carried the run.
func NewSCTPOrFallbackTransport(localAddr string) (Transport, []string, error) {
	return sctpOrFallback(localAddr, newSCTPTransport)
}

// sctpOrFallback implements NewSCTPOrFallbackTransport's behaviour against an
// injectable SCTP constructor. See fallbackToTCP for the actual substitution.
func sctpOrFallback(localAddr string, newSCTP sctpConstructor) (Transport, []string, error) {
	if os.Getenv("CELLRIFT_TRANSPORT_FALLBACK") == "tcp" {
		// A deliberate operator override, not a surprise — no WARN line.
		return fallbackToTCP(localAddr, "forced by CELLRIFT_TRANSPORT_FALLBACK=tcp")
	}

	t, err := newSCTP(localAddr)
	if err == nil {
		return t, nil, nil
	}

	// SCTP unavailable (no kernel module, library missing) — fall back to TCP.
	warnOnce.Do(func() {
		fmt.Fprintf(warnWriter, "WARN: sctp unavailable (%v), using tcp\n", err)
	})
	return fallbackToTCP(localAddr, fmt.Sprintf("sctp unavailable: %v", err))
}

// fallbackToTCP builds the TCP substitute transport and its single-line
// warning. reason is embedded verbatim in the warning text; forced/automatic
// wording is already baked into reason by the caller.
func fallbackToTCP(localAddr, reason string) (Transport, []string, error) {
	tr, err := NewTCPTransport(localAddr)
	if err != nil {
		return nil, nil, fmt.Errorf("sctp fallback to tcp: %w", err)
	}
	return tr, []string{fmt.Sprintf("transport: requested sctp, used tcp (%s)", reason)}, nil
}
