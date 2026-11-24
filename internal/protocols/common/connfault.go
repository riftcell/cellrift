package common

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"github.com/riftcell/cellrift/internal/config"
)

// ConnFaultInjector is implemented by a connection-oriented transport (TCP,
// TLS, SCTP) that can inflict a real, self-inflicted fault on the connection
// it is currently holding: a hard reset, an abrupt close, or — SCTP only — a
// protocol-level association abort.
//
// Every method performs a GENUINE local socket operation and then clears the
// transport's own held-connection bookkeeping, so the very next Send or
// Receive fails through the same "connection is gone, reconnect required"
// path a real peer-inflicted loss would use. A synthetic error value would
// bypass exactly the bookkeeping this feature exists to exercise.
//
// Why the bookkeeping is cleared here rather than left to isDeadConnErr:
// closing a *net.TCPConn makes its next I/O return a net.ErrClosed-class
// error that isDeadConnErr does recognise, but the vendored SCTP library
// swaps its internal fd to -1 on close, so its next Read/Write returns a raw
// syscall.EBADF, which is not net.ErrClosed, io.EOF or io.ErrUnexpectedEOF.
// Relying on classification would therefore leave a dead SCTP association in
// connState forever. Clearing it directly is uniform across all three
// transports and needs no change to isDeadConnErr at all.
//
// UDPTransport deliberately does NOT implement this: a datagram socket has
// no connection lifecycle to fault. An adapter asked to reset a UDP
// transport reports a failed step naming why, which is the correct outcome.
type ConnFaultInjector interface {
	// ResetConn sets SO_LINGER to 0 and closes, so the peer observes a
	// genuine TCP RST rather than a graceful FIN.
	ResetConn() error
	// AbruptCloseConn closes with no clean shutdown and no linger override:
	// a FIN on the wire, but no TLS close_notify and no application-level
	// teardown. It differs from ResetConn ONLY in the wire signal.
	AbruptCloseConn() error
	// AbortConn sends a real RFC 4960 ABORT chunk. TCP and TLS return an
	// error naming that they have no equivalent — a bare close is a
	// different thing and silently substituting one would make an
	// sctp_abort step prove nothing.
	AbortConn() error
}

// TruncatingSender is implemented by every transport — including UDP — that
// can deliberately write fewer bytes of an already-built message than the
// message contains. Unlike ConnFaultInjector this needs no connection
// lifecycle: for UDP the datagram boundary IS the frame boundary, so a short
// write needs no close at all.
type TruncatingSender interface {
	// SendTruncated writes the first n bytes of msg to addr. A
	// connection-oriented implementation then closes and clears its
	// connection, so nothing can later be mistaken for a complete message.
	SendTruncated(msg []byte, addr string, n int) error
}

// ErrConnFaultNotAttempted marks a ConnFaultInjector error that means the
// connection was never touched: SCTP's ResetConn refusing (no TCP RST
// equivalent), TCP/TLS's AbortConn refusing (no SCTP association to abort),
// or a fault firing with nothing held to fault at all. Every OTHER
// ConnFaultInjector error still means the connection was torn down —
// resetHeldConn/abruptCloseHeldConn/AbortConn all call conns.dropConn()
// unconditionally, error or not (see ConnFaultInjector's own doc comment).
// A caller that resets higher-level session state (e.g. the Diameter
// adapter's peer state machine) only when err == nil, without checking this,
// would desync from a connection that actually IS gone whenever the
// underlying op reports a real but non-fatal error — the reconnection-desync class of bug,
// triggered from the opposite direction.
var ErrConnFaultNotAttempted = errors.New("conn fault: connection was not touched")

// errNoHeldConn is the error every fault method returns when the transport
// holds nothing to fault. Connection faults fire AFTER the step's own Send,
// which is what should have established (or stashed) the connection, so this
// is a real inconsistency worth naming rather than a silent no-op.
func errNoHeldConn(label, fault string) error {
	return fmt.Errorf("%s %s: no connection to fault; a connection fault fires after the step's own Send: %w", label, fault, ErrConnFaultNotAttempted)
}

// errAbortNotSCTP is what TCP and TLS return from AbortConn.
func errAbortNotSCTP(label string) error {
	return fmt.Errorf("%s abort: an RFC 4960 ABORT requires an SCTP association; %s has no equivalent — use reset for a TCP RST: %w", label, label, ErrConnFaultNotAttempted)
}

// tcpConnOf unwraps conn down to the *net.TCPConn a SO_LINGER override
// needs. A TLS connection wraps one; crypto/tls exposes it via NetConn.
func tcpConnOf(conn net.Conn) (*net.TCPConn, bool) {
	switch c := conn.(type) {
	case *net.TCPConn:
		return c, true
	case *tls.Conn:
		tc, ok := c.NetConn().(*net.TCPConn)
		return tc, ok
	}
	return nil, false
}

// resetHeldConn is the shared reset mechanic for TCP and TLS: SO_LINGER 0
// then close, so the kernel emits an RST instead of a FIN.
//
// For TLS this closes the UNDERLYING TCP connection directly rather than
// calling tls.Conn.Close, which would first write a close_notify alert — a
// clean shutdown, which is precisely what a reset must not be.
func resetHeldConn(label string, conns *connState) error {
	conn, _ := conns.snapshot()
	if conn == nil {
		return errNoHeldConn(label, "reset")
	}
	tcpConn, ok := tcpConnOf(conn)
	if !ok {
		return fmt.Errorf("%s reset: expected a TCP-backed connection, got %T", label, conn)
	}
	// 0 => abort with RST on Close, not a graceful FIN. Same call
	// internal/protocols/diameter/testing.go's mock-side reset fault makes,
	// applied here to Cellrift's own socket instead of the peer's.
	lingerErr := tcpConn.SetLinger(0)
	closeErr := tcpConn.Close()
	conns.dropConn()
	return errors.Join(lingerErr, closeErr)
}

// abruptCloseHeldConn is resetHeldConn without the linger override. The
// local caller cannot tell the two apart — both leave connState empty and
// the socket closed; only the peer sees the difference (FIN vs RST).
func abruptCloseHeldConn(label string, conns *connState) error {
	conn, _ := conns.snapshot()
	if conn == nil {
		return errNoHeldConn(label, "abrupt close")
	}
	err := closeUnderlying(conn)
	conns.dropConn()
	return err
}

// closeUnderlying closes the raw TCP socket when one is reachable, falling
// back to the connection's own Close. Reaching past *tls.Conn is deliberate:
// tls.Conn.Close writes close_notify, and an abrupt close must not announce
// itself.
func closeUnderlying(conn net.Conn) error {
	if tcpConn, ok := tcpConnOf(conn); ok {
		return tcpConn.Close()
	}
	return conn.Close()
}

// closeAndDrop tears down the connection a truncated send just used. It is
// abruptCloseHeldConn without the "nothing to fault" error, since a
// truncated send has by definition just written to a connection.
func closeAndDrop(conns *connState) error {
	conn, _ := conns.snapshot()
	conns.dropConn()
	if conn == nil {
		return nil
	}
	return closeUnderlying(conn)
}

// clampTruncation bounds n to a valid slice length for msg, so a caller's
// fixed byte count can never panic on a message shorter than it.
func clampTruncation(msg []byte, n int) int {
	if n < 0 {
		return 0
	}
	return min(n, len(msg))
}

// sendTruncatedOn writes the first n bytes of msg and then tears the
// connection down. It mirrors TCPTransport.Send/TLSTransport.Send's own
// branching exactly: a Connect-ed transport writes on its held connection, an
// un-connected one dials first (the dial-per-send shape SIP uses), so a
// truncated send is the ordinary send path with two deliberate changes —
// fewer bytes, and a teardown afterwards.
func sendTruncatedOn(label string, conns *connState, dial func(string) (net.Conn, error), msg []byte, addr string, n int) error {
	short := msg[:clampTruncation(msg, n)]
	conn, connectedAddr := conns.snapshot()
	if connectedAddr != "" {
		if err := sendOnHeldConn(label, conn, connectedAddr, short, addr); err != nil {
			return err
		}
		return closeAndDrop(conns)
	}
	fresh, err := dial(addr)
	if err != nil {
		return fmt.Errorf("dial %s %q: %w", label, addr, err)
	}
	conns.stash(fresh)
	if _, err := fresh.Write(short); err != nil {
		_ = closeAndDrop(conns)
		return fmt.Errorf("write %s %q (truncated): %w", label, addr, err)
	}
	return closeAndDrop(conns)
}

func (t *TCPTransport) ResetConn() error       { return resetHeldConn("tcp", &t.conns) }
func (t *TCPTransport) AbruptCloseConn() error { return abruptCloseHeldConn("tcp", &t.conns) }
func (t *TCPTransport) AbortConn() error       { return errAbortNotSCTP("tcp") }

func (t *TCPTransport) SendTruncated(msg []byte, addr string, n int) error {
	return sendTruncatedOn("tcp", &t.conns, func(a string) (net.Conn, error) {
		return net.DialTimeout("tcp", a, config.TCPConnectTimeoutMs())
	}, msg, addr, n)
}

func (t *TLSTransport) ResetConn() error       { return resetHeldConn("tls", &t.conns) }
func (t *TLSTransport) AbruptCloseConn() error { return abruptCloseHeldConn("tls", &t.conns) }
func (t *TLSTransport) AbortConn() error       { return errAbortNotSCTP("tls") }

func (t *TLSTransport) SendTruncated(msg []byte, addr string, n int) error {
	return sendTruncatedOn("tls", &t.conns, func(a string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: config.TCPConnectTimeoutMs()}
		return tls.DialWithDialer(dialer, "tcp", a, t.tlsCfg)
	}, msg, addr, n)
}

// SendTruncated writes one deliberately short datagram. Nothing is closed:
// a UDP datagram's own length IS its frame boundary, so the peer's single
// ReadFromUDP already observes exactly the short frame with no further
// signal needed — which is why TruncatingSender covers SIP-over-UDP as a
// first-class case rather than deferring it to a malformed-message feature.
func (t *UDPTransport) SendTruncated(msg []byte, addr string, n int) error {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve target %q: %w", addr, err)
	}
	_, err = t.conn.WriteToUDP(msg[:clampTruncation(msg, n)], raddr)
	return err
}

var (
	_ ConnFaultInjector = (*TCPTransport)(nil)
	_ ConnFaultInjector = (*TLSTransport)(nil)
	_ TruncatingSender  = (*TCPTransport)(nil)
	_ TruncatingSender  = (*TLSTransport)(nil)
	_ TruncatingSender  = (*UDPTransport)(nil)
)

// There is deliberately no `_ ConnFaultInjector = (*UDPTransport)(nil)`
// here: a datagram socket has no connection to reset, close abruptly or
// abort, and satisfying the interface with three error-returning stubs would
// make an unsupported combination look supported at compile time.
