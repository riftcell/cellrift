package common

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/riftcell/cellrift/internal/config"
)

// maxMessageSize is the maximum size of a single protocol message (SIP or
// Diameter). 65535 bytes covers the largest possible UDP datagram and is a
// safe upper bound for TCP-framed messages in the test scenarios Cellrift
// targets.
const maxMessageSize = 65535

// Transport abstracts network send/receive for protocol engines.
type Transport interface {
	Send(msg []byte, addr string) error
	Receive(timeout time.Duration) ([]byte, net.Addr, error)
	Close() error
	LocalAddr() net.Addr
}

// Connector is implemented by transports that hold a connection across
// multiple Send/Receive pairs. Connection-oriented protocols (Diameter)
// require this; datagram protocols (SIP/UDP) do not implement it.
type Connector interface {
	Connect(addr string) error
}

// ImpairmentControl is implemented by a transport whose currently active
// delivery-timing impairment can be changed between scenario steps. It is a
// narrow optional interface in the same shape as Connector: a transport that
// does not implement it is simply never impaired, never an error.
//
// cfg is the same map[string]string a step's resolved config carries into
// ProtocolAdapter.Execute. An implementation reads only the impairment keys
// it understands and ignores the rest, matching how every existing
// config-map consumer in this codebase behaves (see internal/engine's
// timeout_ms/check handling).
//
// Calling discipline, which callers MUST honour: a protocol adapter calls
// SetImpairment on EVERY step, including steps that declare no impairment
// at all (with a cfg that produces a zero Profile). This call is the single
// reset point that stops one step's impairment from surviving into the
// next; nothing inside the transport can infer "the previous step is over"
// on its own.
type ImpairmentControl interface {
	SetImpairment(cfg map[string]string) error
}

// UDPTransport implements Transport over UDP.
type UDPTransport struct {
	conn *net.UDPConn
}

// NewUDPTransport creates a UDP transport bound to the given local address.
// Pass "" or ":0" for a system-assigned port.
func NewUDPTransport(localAddr string) (*UDPTransport, error) {
	if localAddr == "" {
		localAddr = ":0"
	}
	addr, err := net.ResolveUDPAddr("udp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve udp addr %q: %w", localAddr, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s: %w", addr, err)
	}
	return &UDPTransport{conn: conn}, nil
}

func (t *UDPTransport) Send(msg []byte, addr string) error {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve target %q: %w", addr, err)
	}
	_, err = t.conn.WriteToUDP(msg, raddr)
	return err
}

func (t *UDPTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	if err := t.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, nil, err
	}
	buf := make([]byte, maxMessageSize)
	n, addr, err := t.conn.ReadFromUDP(buf)
	if err != nil {
		return nil, nil, err
	}
	return buf[:n], addr, nil
}

func (t *UDPTransport) Close() error {
	return t.conn.Close()
}

func (t *UDPTransport) LocalAddr() net.Addr {
	return t.conn.LocalAddr()
}

// TCPTransport implements Transport over TCP.
//
// conns owns the outbound connection, together with the address it was
// Connect-ed to (if any). When Connect has never been called, conns holds
// only a dial-per-send reply socket — see connState's state model. When
// Connect has been called, the connection is held and reused across many
// Send/Receive pairs, which is what makes a Diameter CER/CEA handshake
// (RFC 6733 §5.3) possible.
type TCPTransport struct {
	listener net.Listener
	conns    connState
}

// NewTCPTransport creates a TCP transport listening on the given local address.
// Pass "" or ":0" for a system-assigned port.
func NewTCPTransport(localAddr string) (*TCPTransport, error) {
	if localAddr == "" {
		localAddr = ":0"
	}
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("listen tcp %s: %w", localAddr, err)
	}
	return &TCPTransport{listener: ln}, nil
}

// Connect dials addr and holds the resulting connection for subsequent
// Send/Receive calls. Calling Connect again with the same addr while already
// connected is a no-op; connecting to a different addr requires Close first.
func (t *TCPTransport) Connect(addr string) error {
	err := t.conns.connect(addr, func(a string) (net.Conn, error) {
		return net.DialTimeout("tcp", a, config.TCPConnectTimeoutMs())
	})
	if err != nil {
		return fmt.Errorf("tcp connect %q: %w", addr, err)
	}
	return nil
}

func (t *TCPTransport) Send(msg []byte, addr string) error {
	conn, connectedAddr := t.conns.snapshot()
	if connectedAddr != "" {
		return sendOnHeldConn("tcp", conn, connectedAddr, msg, addr)
	}

	// No held connection — dial-per-send, exactly as today (SIP's path).
	conn, err := net.DialTimeout("tcp", addr, config.TCPConnectTimeoutMs())
	if err != nil {
		return fmt.Errorf("dial tcp %q: %w", addr, err)
	}
	if _, err := conn.Write(msg); err != nil {
		conn.Close()
		return fmt.Errorf("write tcp %q: %w", addr, err)
	}
	t.conns.stash(conn)
	return nil
}

func (t *TCPTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	conn, connectedAddr := t.conns.snapshot()

	if connectedAddr != "" {
		return receiveFromHeldConn("tcp", &t.conns, conn, connectedAddr, timeout, readFrom)
	}

	if conn != nil {
		// Dial-per-send reply socket (SIP's path) — unchanged: no
		// classification, no clearing, error handling exactly as before.
		return readFrom(conn, timeout)
	}

	// Otherwise accept an inbound connection.
	if dl, ok := t.listener.(interface{ SetDeadline(time.Time) error }); ok {
		_ = dl.SetDeadline(time.Now().Add(timeout))
	}
	inbound, err := t.listener.Accept()
	if err != nil {
		return nil, nil, err
	}
	defer inbound.Close()
	return readFrom(inbound, timeout)
}

// readFrom sets a read deadline and reads one message from conn. Shared by
// both the connected and dial-per-send paths so the read shape stays
// identical everywhere it is used.
func readFrom(conn net.Conn, timeout time.Duration) ([]byte, net.Addr, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, nil, err
	}
	buf := make([]byte, maxMessageSize)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, nil, err
	}
	return buf[:n], conn.RemoteAddr(), nil
}

// Close releases the listener and any open outbound connection. Closing the
// connection here is what unblocks a goroutine parked in conn.Read — the
// idiom the SCTP connect-timeout wrapper copies.
func (t *TCPTransport) Close() error {
	conn := t.conns.takeAll()
	if conn != nil {
		conn.Close()
	}
	return t.listener.Close()
}

// LocalAddr returns the address of the currently active socket. Once Connect
// has succeeded it is the outbound connection's local address — what a CER's
// Host-IP-Address (RFC 6733 §5.3.1) must carry, since a listener bound to
// ":0" resolves to the wildcard address and is meaningless to a real peer.
// Otherwise it is unchanged: the transport's listener address, exactly as
// before the Diameter handshake needed a real one.
//
// The Diameter handshake is the first consumer that needs a genuine
// address; the defect itself is older (an earlier refactor only relocated it — it was harmless while nothing
// consumed LocalAddr()). The un-connected branch below is
// deliberately unchanged: it keys on addr, not conn, because SIP's
// dial-per-send path (connState.stash) sets conn while leaving addr empty,
// and SIP must observe byte-for-byte the same behaviour as before this fix.
func (t *TCPTransport) LocalAddr() net.Addr {
	if conn, addr := t.conns.snapshot(); addr != "" && conn != nil {
		return conn.LocalAddr()
	}
	return t.listener.Addr()
}

// Compile-time interface check: TCPTransport must implement Connector.
var _ Connector = (*TCPTransport)(nil)

// TLSTransport implements Transport over TLS (TCP).
//
// It uses a plain TCP listener for port allocation and LocalAddr(). conns
// owns the outbound connection the same way TCPTransport's does — see
// connState's state model. In UAC mode Send is always called before
// Receive when un-connected, so the listener is never used for actual
// message exchange in that path.
type TLSTransport struct {
	listener net.Listener
	tlsCfg   *tls.Config
	conns    connState
}

// NewTLSTransport creates a TLS transport bound to the given local address.
// Pass "" or ":0" for a system-assigned port.
func NewTLSTransport(localAddr string, cfg *tls.Config) (*TLSTransport, error) {
	if localAddr == "" {
		localAddr = ":0"
	}
	ln, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("listen tls %s: %w", localAddr, err)
	}
	return &TLSTransport{listener: ln, tlsCfg: cfg}, nil
}

// Connect dials addr and holds the resulting connection for subsequent
// Send/Receive calls. Calling Connect again with the same addr while already
// connected is a no-op; connecting to a different addr requires Close first.
//
// This uses tls.DialWithDialer rather than splitting dial and handshake: it
// bounds the whole handshake with the dialer's timeout internally, and
// splitting it would only be needed for TCP keepalive, which is deferred along
// with an outbound DWR timer.
func (t *TLSTransport) Connect(addr string) error {
	err := t.conns.connect(addr, func(a string) (net.Conn, error) {
		dialer := &net.Dialer{Timeout: config.TCPConnectTimeoutMs()}
		return tls.DialWithDialer(dialer, "tcp", a, t.tlsCfg)
	})
	if err != nil {
		return fmt.Errorf("tls connect %q: %w", addr, err)
	}
	return nil
}

func (t *TLSTransport) Send(msg []byte, addr string) error {
	conn, connectedAddr := t.conns.snapshot()
	if connectedAddr != "" {
		return sendOnHeldConn("tls", conn, connectedAddr, msg, addr)
	}

	dialer := &net.Dialer{Timeout: config.TCPConnectTimeoutMs()}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, t.tlsCfg)
	if err != nil {
		return fmt.Errorf("dial tls %q: %w", addr, err)
	}
	if _, err := conn.Write(msg); err != nil {
		conn.Close()
		return fmt.Errorf("write tls %q: %w", addr, err)
	}
	t.conns.stash(conn)
	return nil
}

func (t *TLSTransport) Receive(timeout time.Duration) ([]byte, net.Addr, error) {
	conn, connectedAddr := t.conns.snapshot()

	if connectedAddr != "" {
		return receiveFromHeldConn("tls", &t.conns, conn, connectedAddr, timeout, readFrom)
	}

	if conn != nil {
		// Dial-per-send reply socket (SIP's path) — unchanged.
		return readFrom(conn, timeout)
	}

	if dl, ok := t.listener.(interface{ SetDeadline(time.Time) error }); ok {
		_ = dl.SetDeadline(time.Now().Add(timeout))
	}
	inbound, err := t.listener.Accept()
	if err != nil {
		return nil, nil, err
	}
	defer inbound.Close()
	return readFrom(inbound, timeout)
}

// Close releases the listener and any open outbound connection. Closing the
// connection here is what unblocks a goroutine parked in conn.Read — this
// holds for *tls.Conn too (golang/go#23518 was fixed long before the pinned
// Go version), and is the idiom the SCTP connect-timeout wrapper copies.
func (t *TLSTransport) Close() error {
	conn := t.conns.takeAll()
	if conn != nil {
		conn.Close()
	}
	return t.listener.Close()
}

// LocalAddr returns the address of the currently active socket, with the
// same connected-vs-listener semantics as TCPTransport.LocalAddr — see that
// method's doc comment for the full rationale (connected-address fix,
// un-connected branch deliberately unchanged for SIP's sake).
func (t *TLSTransport) LocalAddr() net.Addr {
	if conn, addr := t.conns.snapshot(); addr != "" && conn != nil {
		return conn.LocalAddr()
	}
	return t.listener.Addr()
}

// Compile-time interface check: TLSTransport must implement Connector.
var _ Connector = (*TLSTransport)(nil)

// NewTransport creates a Transport of the given type ("udp" or "tcp").
// For TLS use NewTLSTransport directly.
func NewTransport(protocol, localAddr string) (Transport, error) {
	switch protocol {
	case "udp":
		return NewUDPTransport(localAddr)
	case "tcp":
		return NewTCPTransport(localAddr)
	default:
		return nil, fmt.Errorf("unsupported transport protocol: %s", protocol)
	}
}
