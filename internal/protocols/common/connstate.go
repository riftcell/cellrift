package common

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// connState owns the outbound connection a connection-oriented transport is using,
// together with the address Connect established it for. It is shared by TCPTransport
// and TLSTransport, which had byte-identical copies of this logic.
//
// UDPTransport deliberately does NOT use connState: datagram transports have no
// connection to hold and must not implement Connector. Do not reach for this type
// from a datagram path "for consistency".
//
// State model:
//
//	addr == "", conn == nil  -> dial-per-send, nothing stashed
//	addr == "", conn != nil  -> dial-per-send, reply socket stashed (SIP)
//	addr != "", conn != nil  -> connected, held across Send/Receive (Diameter)
//	addr != "", conn == nil  -> connected but peer gone; Send/Receive error until Connect
type connState struct {
	mu   sync.Mutex
	conn net.Conn
	addr string // non-empty only after a successful Connect
}

// snapshot returns a single locked read of the held connection and its
// connected address, so callers see a consistent (conn, addr) pair.
func (c *connState) snapshot() (net.Conn, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn, c.addr
}

// stash records conn from the dial-per-send path. It leaves addr untouched —
// this path never sets addr, so it can never be mistaken for a Connect-ed
// state.
func (c *connState) stash(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
}

// dropConn clears the held connection but KEEPS addr, recording "connected
// but peer gone" so the next Send/Receive fails loudly instead of silently
// dialling a fresh socket.
func (c *connState) dropConn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = nil
}

// takeAll clears both conn and addr and returns the old connection, for
// Close to close. Closing here is what unblocks a goroutine parked in
// conn.Read — the idiom the SCTP connect-timeout wrapper copies.
func (c *connState) takeAll() net.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn := c.conn
	c.conn = nil
	c.addr = ""
	return conn
}

// connect dials addr via dial and holds the resulting connection, or is a
// no-op if already connected to the same addr.
//
// Invariant: connect holds mu for the FULL duration of dial, not just the
// pointer swap. At most one dial is ever in flight, and every reader of
// (conn, addr) sees a consistent pair — a concurrent Send can never observe
// a half-established state or race a second, orphaned dial.
func (c *connState) connect(addr string, dial func(string) (net.Conn, error)) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if c.addr == addr {
			return nil // idempotent
		}
		return errors.New("already connected to " + c.addr + ", close first")
	}

	conn, err := dial(addr)
	if err != nil {
		return err
	}
	c.conn = conn
	c.addr = addr
	return nil
}

// isDeadConnErr reports whether err means the connection is unusable and must be
// re-established. A read deadline expiring does NOT qualify: os.ErrDeadlineExceeded
// leaves a net.Conn fully usable, and treating it as fatal would break a CER->CEA
// retry by forcing a redial that discards a completed handshake.
func isDeadConnErr(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

// IsDeadConnErr is isDeadConnErr's exported twin, for callers above the
// transport layer that need to keep their OWN bookkeeping in sync with a
// connection this package has already torn down. receiveFromHeldConn calls
// conns.dropConn() internally the moment isDeadConnErr(err) is true, but a
// protocol adapter's own peer-state machine (e.g. Diameter's peerOpen/
// peerClosed) has no way to observe that drop unless it asks — without this,
// a peer-initiated close (a malformed request the peer could not frame, not
// a scenario-declared impair fault) leaves the adapter's peer state stuck at
// Open against a connection connState already knows is gone, so the next
// step's ensureConnected skips reconnecting and fails forever with
// "connection ... is gone, reconnect required" instead of re-handshaking.
// This is the exact reconnection desync (adapter.go's applyConnFault doc comment),
// now closed for the peer-initiated case too, not only the self-inflicted
// one.
func IsDeadConnErr(err error) bool {
	return isDeadConnErr(err)
}

// sendOnHeldConn writes msg to a Connect-ed connection. addr must match the
// address Connect established, and the connection must still be alive —
// callers get a loud error rather than a silent redial in either failure case,
// per connState's fourth state-model row. label is the protocol name used in
// the error text, so TCP, TLS and SCTP keep distinct, greppable messages from
// one implementation.
func sendOnHeldConn(label string, conn net.Conn, connectedAddr string, msg []byte, addr string) error {
	if addr != connectedAddr {
		return fmt.Errorf("%s send: connected to %s, refusing to send to %s", label, connectedAddr, addr)
	}
	if conn == nil {
		return fmt.Errorf("%s send: connection to %s is gone, reconnect required", label, connectedAddr)
	}
	if _, err := conn.Write(msg); err != nil {
		return fmt.Errorf("write %s %q: %w", label, addr, err)
	}
	return nil
}

// readFunc reads one message from conn within timeout, in the shape both
// readFrom (TCP/TLS, via SetReadDeadline) and readFromSCTP (SCTP, via a
// goroutine race — see its doc comment for why) implement.
type readFunc func(conn net.Conn, timeout time.Duration) ([]byte, net.Addr, error)

// receiveFromHeldConn reads from a Connect-ed connection using read. Only in
// this path is a read error classified: os.ErrDeadlineExceeded leaves a
// TCP/TLS connection usable (see isDeadConnErr), so only io.EOF/net.ErrClosed
// clear the held connection. read is a parameter — not always readFrom —
// because SCTP cannot implement a timeout the same way; see readFromSCTP.
func receiveFromHeldConn(label string, conns *connState, conn net.Conn, connectedAddr string, timeout time.Duration, read readFunc) ([]byte, net.Addr, error) {
	if conn == nil {
		return nil, nil, fmt.Errorf("%s receive: connection to %s is gone, reconnect required", label, connectedAddr)
	}
	data, addr, err := read(conn, timeout)
	if err != nil && isDeadConnErr(err) {
		conns.dropConn()
	}
	return data, addr, err
}
